package slagent

import (
	"fmt"
	"strings"
	"sync"

	slackapi "github.com/slack-go/slack"

	"github.com/sttts/slagent/access"
	"github.com/sttts/slagent/client"
)

// slagentBlockPrefix is the prefix for block IDs on all slagent-posted messages.
// The full block_id is "slagent-{instanceID}" with optional suffixes:
//   - "slagent-{id}"      — finalized text message
//   - "slagent-{id}~"     — streaming text (not yet final)
//   - "slagent-{id}~act"  — activity message (always skip)
const slagentBlockPrefix = "slagent-"

// blockKind classifies a slagent block_id.
type blockKind int

const (
	blockNone     blockKind = iota // not a slagent block
	blockFinal                     // finalized text
	blockStreaming                 // streaming text (not yet final)
	blockActivity                  // activity (always skip)
)

// classifyBlock returns the kind and instance ID for a block_id.
func classifyBlock(blockID string) (blockKind, string) {
	if !strings.HasPrefix(blockID, slagentBlockPrefix) {
		return blockNone, ""
	}
	rest := blockID[len(slagentBlockPrefix):]

	if strings.HasSuffix(rest, "~act") {
		return blockActivity, rest[:len(rest)-4]
	}
	if strings.HasSuffix(rest, "~") {
		return blockStreaming, rest[:len(rest)-1]
	}
	return blockFinal, rest
}

// classifyBlocks returns the kind and instance ID of the first slagent block found.
func classifyBlocks(blocks slackapi.Blocks) (blockKind, string) {
	for _, b := range blocks.BlockSet {
		kind, id := classifyBlock(b.ID())
		if kind != blockNone {
			return kind, id
		}
	}
	return blockNone, ""
}

// slagentSection wraps text in a section block tagged with this thread's block_id.
func (t *Thread) slagentSection(text string) *slackapi.SectionBlock {
	s := slackapi.NewSectionBlock(
		slackapi.NewTextBlockObject("mrkdwn", text, false, false),
		nil, nil,
	)
	s.BlockID = t.blockID
	return s
}

// tagBlocks sets this thread's block_id on the first section/context block.
func (t *Thread) tagBlocks(blocks []slackapi.Block) []slackapi.Block {
	out := make([]slackapi.Block, len(blocks))
	copy(out, blocks)
	for i, b := range out {
		switch bb := b.(type) {
		case *slackapi.SectionBlock:
			c := *bb
			c.BlockID = t.blockID
			out[i] = &c
			return out
		case *slackapi.ContextBlock:
			c := *bb
			c.BlockID = t.blockID
			out[i] = &c
			return out
		}
	}
	return out
}

// InstanceID returns the instance identifier used in block_id tagging.
func (t *Thread) InstanceID() string {
	return t.instanceID
}

// Emoji returns the identity emoji for this thread instance.
func (t *Thread) Emoji() string {
	return t.emoji
}

// ThinkingEmoji returns the Slack shortcode for thinking/running indicator.
func (t *Thread) ThinkingEmoji() string {
	return t.config.thinkingEmoji
}

// maybeWelcome posts a one-time welcome ephemeral to a non-owner authorized user.
func (t *Thread) maybeWelcome(userID string) {
	if userID == t.OwnerID() {
		return
	}
	t.mu.Lock()
	if t.welcomed == nil {
		t.welcomed = make(map[string]bool)
	}
	if t.welcomed[userID] {
		t.mu.Unlock()
		return
	}
	t.welcomed[userID] = true
	t.mu.Unlock()

	t.PostEphemeral(userID, fmt.Sprintf("👋 Welcome! This is your first time interacting with %s. All conversations are logged for transparency and audit. Please use responsibly — and have fun!", t.emoji))
}

// logSlack writes a Slack API action to the Thread's log writer if configured.
func (t *Thread) logSlack(action, content string) {
	if t.config.slackLog == nil {
		return
	}
	fmt.Fprintf(t.config.slackLog, "[slack] %s: %s\n", action, content)
}

// Thread manages an agent session in a Slack thread.
type Thread struct {
	client  *client.Client
	channel string
	threadTS   string
	instanceID string // unique per slaude instance, used in block_id
	blockID    string // "slagent-{instanceID}", cached
	emoji      string // identity emoji derived from instanceID
	config     threadConfig

	// Access control (embedded — exports IsAuthorized, IsVisible, SetOpen, etc.)
	*access.Controller
	topic      string // parsed topic text (without emojis/mentions)
	title      string // full thread message with shortcodes → Unicode
	modeSuffix string // appended to title (e.g. " — 📋 planning")
	joined     bool   // true if joined/resumed (don't persist access to title)

	// Reply tracking
	lastTS string

	// User resolution
	userCache map[string]string
	welcomed  map[string]bool // users who received the welcome ephemeral

	mu sync.Mutex
}

// NewThread creates a new thread manager.
func NewThread(c *client.Client, channel string, opts ...ThreadOption) *Thread {
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}

	instanceID := cfg.instanceID
	if instanceID == "" {
		instanceID = randomInstanceID()
	}

	ac := access.New(cfg.ownerID)
	if cfg.openAccess {
		ac.SetOpen()
	}
	if cfg.observe {
		ac.SetObserve(true)
	}

	t := &Thread{
		client:     c,
		channel:    channel,
		instanceID: instanceID,
		blockID:    slagentBlockPrefix + instanceID,
		emoji:      InstanceEmoji(instanceID),
		config:     cfg,
		Controller: ac,
		userCache:  make(map[string]string),
	}
	return t
}

// Start posts the initial thread message and returns the thread URL.
// Must be called before any concurrent access to the thread.
func (t *Thread) Start(title string) (string, error) {
	if title == "" {
		title = "Agent session"
	}
	t.topic = title

	label := t.formatTitle()

	t.logSlack("postMessage(thread-start)", label)
	_, ts, err := t.client.PostMessage(
		t.channel,
		slackapi.MsgOptionBlocks(t.slagentSection(label)),
		slackapi.MsgOptionText(label, false),
	)
	if err != nil {
		return "", fmt.Errorf("post thread parent: %w", err)
	}

	t.threadTS = ts
	t.lastTS = ts

	link, err := t.client.GetPermalink(&slackapi.PermalinkParameters{
		Channel: t.channel,
		Ts:      ts,
	})
	if err != nil {
		return fmt.Sprintf("(thread started, permalink unavailable: %v)", err), nil
	}

	// Strip query string — we only need the path
	if idx := strings.Index(link, "?"); idx >= 0 {
		link = link[:idx]
	}
	return link, nil
}

// Resume attaches to an existing thread and recovers access state from the title.
// If afterTS is provided, it is used as the cursor position (skipping all messages
// up to that point). Otherwise, all replies are fetched and returned for history
// injection (fresh join). Returns the fetched messages (excluding the parent).
func (t *Thread) Resume(threadTS string, afterTS ...string) []slackapi.Message {
	t.mu.Lock()
	t.threadTS = threadTS
	t.lastTS = threadTS
	t.joined = true
	t.mu.Unlock()

	// If caller provides a cursor, use it and only fetch the parent for title
	if len(afterTS) > 0 && afterTS[0] != "" {
		params := &slackapi.GetConversationRepliesParameters{
			ChannelID: t.channel,
			Timestamp: threadTS,
			Limit:     1,
		}
		msgs, _, _, err := t.client.GetConversationReplies(params)
		if err == nil && len(msgs) > 0 {
			t.parseTitle(msgs[0].Text)
		}
		t.mu.Lock()
		t.lastTS = afterTS[0]
		t.mu.Unlock()
		return nil
	}

	// No cursor — fetch replies and advance to the latest
	params := &slackapi.GetConversationRepliesParameters{
		ChannelID: t.channel,
		Timestamp: threadTS,
	}
	msgs, _, _, err := t.client.GetConversationReplies(params)
	if err != nil || len(msgs) == 0 {
		return nil
	}
	t.parseTitle(msgs[0].Text)

	// Advance past all existing replies
	if latest := msgs[len(msgs)-1].Timestamp; latest > threadTS {
		t.mu.Lock()
		t.lastTS = latest
		t.mu.Unlock()
	}

	// Return replies (skip parent message)
	if len(msgs) > 1 {
		return msgs[1:]
	}
	return nil
}

// FormatHistory formats thread messages into a text block for Claude to absorb.
// Messages from slagent instances are identified by block_id and shown with their
// emoji prefix. Human messages show the resolved user name.
func (t *Thread) FormatHistory(msgs []slackapi.Message) string {
	if len(msgs) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, msg := range msgs {
		// Skip activity and streaming messages
		kind, _ := classifyBlocks(msg.Blocks)
		if kind == blockActivity || kind == blockStreaming {
			continue
		}

		// Slagent messages: use the text as-is (already has emoji prefix)
		if kind == blockFinal {
			sb.WriteString(msg.Text)
			sb.WriteByte('\n')
			continue
		}

		// Skip bot messages
		if msg.BotID != "" {
			continue
		}

		// Human message
		user := t.resolveUser(msg.User)
		sb.WriteString("@")
		sb.WriteString(user)
		sb.WriteString(": ")
		sb.WriteString(msg.Text)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// NewTurn begins a new response turn.
func (t *Thread) NewTurn() Turn {
	t.mu.Lock()
	threadTS := t.threadTS
	t.mu.Unlock()

	// Select backend based on token type
	var w turnWriter
	if isNativeToken(t.client.Token()) {
		w = newNativeTurn(t.client.Token(), t.config.apiURL, t.channel, threadTS, t.config.markdownConverter, t.config.bufferSize)
	} else {
		w = newCompatTurn(t.client.Client, t.channel, threadTS, t.blockID, t.emoji, t.config.thinkingEmoji, t.config.quoteMessages, t.config.slackLog)
	}
	return &turnImpl{w: w}
}

// Post sends a plain message in the thread.
// PostEphemeral sends a message visible only to the specified user in the thread.
func (t *Thread) PostEphemeral(userID, text string) {
	t.mu.Lock()
	threadTS := t.threadTS
	t.mu.Unlock()

	if threadTS == "" {
		return
	}
	t.logSlack("postEphemeral("+userID+")", text)
	t.client.PostEphemeral(
		t.channel,
		userID,
		slackapi.MsgOptionText(text, false),
		slackapi.MsgOptionTS(threadTS),
	)
}

func (t *Thread) Post(text string) (string, error) {
	t.mu.Lock()
	threadTS := t.threadTS
	t.mu.Unlock()

	if threadTS == "" {
		return "", fmt.Errorf("no active thread")
	}

	t.logSlack("postMessage(post)", text)
	_, ts, err := t.client.PostMessage(
		t.channel,
		slackapi.MsgOptionBlocks(t.slagentSection(text)),
		slackapi.MsgOptionText(text, false),
		slackapi.MsgOptionTS(threadTS),
	)
	return ts, err
}

// UpdateMessage updates an existing message in the thread.
func (t *Thread) UpdateMessage(msgTS, text string) error {
	t.logSlack("updateMessage(post)", text)
	_, _, _, err := t.client.UpdateMessage(
		t.channel,
		msgTS,
		slackapi.MsgOptionBlocks(t.slagentSection(text)),
		slackapi.MsgOptionText(text, false),
	)
	return err
}

// PostPrompt posts a message and adds reaction emojis for interactive responses.
// Returns the message timestamp for use with PollReaction.
// Reaction names use Slack short codes without colons (e.g. "white_check_mark", "one").
func (t *Thread) PostPrompt(text string, reactions []string) (string, error) {
	t.mu.Lock()
	threadTS := t.threadTS
	t.mu.Unlock()

	if threadTS == "" {
		return "", fmt.Errorf("no active thread")
	}

	t.logSlack("postMessage(prompt)", text)
	_, ts, err := t.client.PostMessage(
		t.channel,
		slackapi.MsgOptionBlocks(t.slagentSection(text)),
		slackapi.MsgOptionText(text, false),
		slackapi.MsgOptionTS(threadTS),
	)
	if err != nil {
		return "", err
	}

	// Pre-add reaction emojis as clickable options (session/user tokens only).
	// Bot tokens will use Block Kit buttons via Socket Mode instead.
	if !isNativeToken(t.client.Token()) {
		for _, r := range reactions {
			t.client.AddReaction(r, slackapi.ItemRef{
				Channel:   t.channel,
				Timestamp: ts,
			})
		}
	}

	return ts, nil
}

// PollReaction checks which pre-added reaction the owner has clicked (removed).
// All expected reactions are pre-added by us (the owner via session token).
// When the owner clicks one, Slack toggles it off. We detect selection by
// checking which expected reaction no longer has the owner in its user list.
// Other users adding reactions is a noop — only the owner's removal counts.
// Returns the selected reaction name, or "" if no selection yet.
func (t *Thread) PollReaction(msgTS string, expected []string) (string, error) {
	item, err := t.client.GetReactions(slackapi.ItemRef{
		Channel:   t.channel,
		Timestamp: msgTS,
	}, slackapi.NewGetReactionsParameters())
	if err != nil {
		return "", fmt.Errorf("get reactions: %w", err)
	}

	// Build map: reaction name → whether the owner is still in the user list
	ownerPresent := make(map[string]bool)
	for _, r := range item.Reactions {
		for _, u := range r.Users {
			if u == t.OwnerID() {
				ownerPresent[r.Name] = true
				break
			}
		}
	}

	// The reaction where the owner is no longer present is the selection
	for _, r := range expected {
		if !ownerPresent[r] {
			return r, nil
		}
	}

	// Check non-owner reactions
	for _, r := range item.Reactions {
		for _, u := range r.Users {
			if u == t.OwnerID() {
				continue
			}
			switch r.Name {
			case "x":
				// Non-owner ❌ = deny
				return "x", nil
			case "white_check_mark", "floppy_disk":
				// Non-owner tried to approve — remove their reaction and warn
				t.client.RemoveReaction(r.Name, slackapi.ItemRef{
					Channel:   t.channel,
					Timestamp: msgTS,
				})
				t.PostEphemeral(u, fmt.Sprintf("🚫 Only <@%s> can approve permissions.", t.OwnerID()))
			}
		}
	}

	return "", nil
}

// FinalizeReaction cleans up reactions after the owner has made a selection.
// It removes all non-selected reactions and re-adds the selected one
// (which was toggled off when the owner clicked it).
func (t *Thread) FinalizeReaction(msgTS, selected string, all []string) {
	ref := slackapi.ItemRef{Channel: t.channel, Timestamp: msgTS}

	// Re-add the selected reaction (owner's click toggled it off)
	t.client.AddReaction(selected, ref)

	// Remove the non-selected reactions
	for _, r := range all {
		if r != selected {
			t.client.RemoveReaction(r, ref)
		}
	}
}

// GetReactions returns the reactions on a message.
func (t *Thread) GetReactions(msgTS string) ([]slackapi.ItemReaction, error) {
	item, err := t.client.GetReactions(slackapi.ItemRef{
		Channel:   t.channel,
		Timestamp: msgTS,
	}, slackapi.NewGetReactionsParameters())
	if err != nil {
		return nil, err
	}
	return item.Reactions, nil
}

// RemoveAllReactions removes all given reactions from a message.
func (t *Thread) RemoveAllReactions(msgTS string, reactions []string) {
	ref := slackapi.ItemRef{Channel: t.channel, Timestamp: msgTS}
	for _, r := range reactions {
		t.client.RemoveReaction(r, ref)
	}
}

// AddReaction adds a single reaction to a message.
func (t *Thread) AddReaction(msgTS, reaction string) {
	t.client.AddReaction(reaction, slackapi.ItemRef{Channel: t.channel, Timestamp: msgTS})
}

// DeleteMessage deletes a message from the thread.
func (t *Thread) DeleteMessage(msgTS string) error {
	t.logSlack("deleteMessage", msgTS)
	_, _, err := t.client.DeleteMessage(t.channel, msgTS)
	return err
}

// PostBlocks sends a message with blocks in the thread.
func (t *Thread) PostBlocks(fallback string, blocks ...slackapi.Block) error {
	t.mu.Lock()
	threadTS := t.threadTS
	t.mu.Unlock()

	if threadTS == "" {
		return fmt.Errorf("no active thread")
	}

	tagged := t.tagBlocks(blocks)
	t.logSlack("postMessage(blocks)", fallback)
	_, _, err := t.client.PostMessage(
		t.channel,
		slackapi.MsgOptionBlocks(tagged...),
		slackapi.MsgOptionText(fallback, false),
		slackapi.MsgOptionTS(threadTS),
	)
	return err
}

// PostUser posts a user message with context block ("👤 @user") and text section.
func (t *Thread) PostUser(user, text string) error {
	t.mu.Lock()
	threadTS := t.threadTS
	t.mu.Unlock()

	if threadTS == "" {
		return fmt.Errorf("no active thread")
	}

	ctx := slackapi.NewContextBlock(t.blockID,
		slackapi.NewTextBlockObject("mrkdwn", fmt.Sprintf("👤 @%s", user), false, false),
	)
	section := slackapi.NewSectionBlock(
		slackapi.NewTextBlockObject("mrkdwn", text, false, false),
		nil, nil,
	)
	fallback := fmt.Sprintf("@%s: %s", user, text)
	t.logSlack("postMessage(user)", fallback)
	_, _, err := t.client.PostMessage(
		t.channel,
		slackapi.MsgOptionBlocks(ctx, section),
		slackapi.MsgOptionText(fallback, false),
		slackapi.MsgOptionTS(threadTS),
	)
	return err
}

// PostMarkdown posts markdown content as code blocks in the thread.
func (t *Thread) PostMarkdown(text string) error {
	t.mu.Lock()
	threadTS := t.threadTS
	t.mu.Unlock()

	if threadTS == "" {
		return fmt.Errorf("no active thread")
	}

	// Wrap in code block; reserve 6 chars for the ``` fences + newlines
	chunks := splitAtLines(text, maxBlockTextLen-8)
	for _, chunk := range chunks {
		fenced := "```\n" + chunk + "\n```"
		section := t.slagentSection(fenced)
		t.logSlack("postMessage(markdown)", fenced)
		_, _, err := t.client.PostMessage(
			t.channel,
			slackapi.MsgOptionBlocks(section),
			slackapi.MsgOptionText(chunk, false),
			slackapi.MsgOptionTS(threadTS),
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// ThreadTS returns the thread timestamp.
func (t *Thread) ThreadTS() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.threadTS
}

// Channel returns the channel ID.
func (t *Thread) Channel() string {
	return t.channel
}

// LastTS returns the timestamp of the last seen message.
func (t *Thread) LastTS() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastTS
}

// URL returns the Slack permalink for this thread.
func (t *Thread) URL() string {
	t.mu.Lock()
	ts := t.threadTS
	t.mu.Unlock()

	if ts == "" {
		return ""
	}
	link, err := t.client.GetPermalink(&slackapi.PermalinkParameters{
		Channel: t.channel,
		Ts:      ts,
	})
	if err != nil {
		return ""
	}

	// Strip query string — we only need the path
	if idx := strings.Index(link, "?"); idx >= 0 {
		link = link[:idx]
	}
	return link
}

// resolveUser resolves a user ID to a display name, with caching.
func (t *Thread) resolveUser(userID string) string {
	t.mu.Lock()
	if name, ok := t.userCache[userID]; ok {
		t.mu.Unlock()
		return name
	}
	t.mu.Unlock()

	info, err := t.client.GetUserInfo(userID)
	if err != nil {
		return userID
	}

	name := info.Profile.DisplayName
	if name == "" {
		name = info.RealName
	}
	if name == "" {
		name = info.Name
	}

	t.mu.Lock()
	t.userCache[userID] = name
	t.mu.Unlock()
	return name
}


