package slagent

import (
	"fmt"
	"strings"
	"sync"
	"time"

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

// formatTitle builds the thread parent label reflecting access state.
//
//	Open for all:                  ":instanceID:🧵 Topic"
//	Selective (allowed users):     ":instanceID:🧵 <@U1> <@U2> Topic"
//	Selective + observe:           ":instanceID:👀🧵 <@U1> <@U2> Topic"
//	Locked (owner only):           ":instanceID:🔒🧵 Topic"
//	Locked + observe (= observe):  ":instanceID:👀🧵 Topic"
//	With bans (appended):          "... (🔒 <@U3>)"
func (t *Thread) formatTitle() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	title := t.topic
	if title == "" {
		title = "Agent session"
	}

	st := t.Controller.State()

	// Build access marker: 👀 replaces 🔒 when observe is on
	var label string
	if st.OpenAccess {
		label = fmt.Sprintf(":%s:🧵 %s", t.instanceID, title)
	} else if st.Observe {
		// Observe implies closed; 👀 replaces 🔒
		if len(st.AllowedUsers) > 0 {
			var mentions []string
			for _, u := range st.AllowedUsers {
				mentions = append(mentions, fmt.Sprintf("<@%s>", u))
			}
			label = fmt.Sprintf(":%s:👀🧵 %s %s", t.instanceID, strings.Join(mentions, " "), title)
		} else {
			label = fmt.Sprintf(":%s:👀🧵 %s", t.instanceID, title)
		}
	} else if len(st.AllowedUsers) > 0 {
		// Selective without observe
		var mentions []string
		for _, u := range st.AllowedUsers {
			mentions = append(mentions, fmt.Sprintf("<@%s>", u))
		}
		label = fmt.Sprintf(":%s:🧵 %s %s", t.instanceID, strings.Join(mentions, " "), title)
	} else {
		// Locked (no allowed users, no observe)
		label = fmt.Sprintf(":%s:🔒🧵 %s", t.instanceID, title)
	}

	// Append mode suffix (e.g. " — 📋 planning")
	if t.modeSuffix != "" {
		label += t.modeSuffix
	}

	// Append ban list
	if len(st.BannedUsers) > 0 {
		var mentions []string
		for _, u := range st.BannedUsers {
			mentions = append(mentions, fmt.Sprintf("<@%s>", u))
		}
		label += fmt.Sprintf(" (🔒 %s)", strings.Join(mentions, " "))
	}

	// t.title is for terminal display — convert shortcode to Unicode
	t.title = ShortcodesToUnicode(label)
	return label
}

// Title returns the full thread title with shortcodes converted to Unicode.
func (t *Thread) Title() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.title
}

// Topic returns the parsed topic text (without emojis, mentions, access markers).
func (t *Thread) Topic() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.topic
}

// refreshTitle re-fetches the thread parent message and re-parses the title.
// Used by joined instances to pick up access changes made by the original instance.
func (t *Thread) refreshTitle() {
	t.mu.Lock()
	threadTS := t.threadTS
	t.mu.Unlock()

	if threadTS == "" {
		return
	}
	params := &slackapi.GetConversationRepliesParameters{
		ChannelID: t.channel,
		Timestamp: threadTS,
		Limit:     1,
	}
	msgs, _, _, err := t.client.GetConversationReplies(params)
	if err == nil && len(msgs) > 0 {
		t.parseTitle(msgs[0].Text)
	}
}

// parseTitle recovers access state from a thread parent message.
// Handles both Unicode (🔒🧵) and Slack shortcode (:lock::thread:) formats.
func (t *Thread) parseTitle(text string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Normalize shortcodes to Unicode for consistent parsing
	text = ShortcodesToUnicode(text)
	t.title = text

	// Detect 👀 observe marker (👀🧵 = observe mode, replaces 🔒)
	observe := strings.Contains(text, "👀🧵")
	locked := strings.Contains(text, "🔒🧵")

	// Extract content after 🧵 (with optional space)
	if idx := strings.Index(text, "🧵 "); idx >= 0 {
		t.topic = text[idx+len("🧵 "):]
	} else if idx := strings.Index(text, "🧵"); idx >= 0 {
		t.topic = text[idx+len("🧵"):]
	}

	// Parse "(🔒 <@U3>)" — banned users (strip from title)
	bannedUsers := make(map[string]bool)
	if idx := strings.Index(t.topic, " (🔒 "); idx >= 0 {
		end := strings.Index(t.topic[idx:], ")")
		if end >= 0 {
			extractMentions(t.topic[idx:idx+end+1], bannedUsers)
			t.topic = strings.TrimSpace(t.topic[:idx] + t.topic[idx+end+1:])
		}
	}

	// Strip mode suffix (e.g. " — 📋 planning") — not part of the topic.
	if idx := strings.LastIndex(t.topic, " — 📋"); idx >= 0 {
		t.topic = t.topic[:idx]
	}

	// Build access state
	var st access.State
	st.Observe = observe
	for u := range bannedUsers {
		st.BannedUsers = append(st.BannedUsers, u)
	}

	if locked {
		st.OpenAccess = false
		t.Controller.Apply(st)
		return
	}

	// Not locked: parse leading <@...> mentions as allowed users
	allowedUsers := make(map[string]bool)
	for strings.HasPrefix(t.topic, "<@") {
		end := strings.Index(t.topic, ">")
		if end < 0 {
			break
		}
		uid := t.topic[2:end]

		// Strip display name suffix: <@U12345|sttts> → U12345
		if idx := strings.Index(uid, "|"); idx >= 0 {
			uid = uid[:idx]
		}
		allowedUsers[uid] = true
		t.topic = strings.TrimLeft(t.topic[end+1:], " ")
	}
	for u := range allowedUsers {
		st.AllowedUsers = append(st.AllowedUsers, u)
	}

	// 👀 means not open (observe replaces 🔒); otherwise open if no allowed users
	if observe {
		st.OpenAccess = false
	} else {
		st.OpenAccess = len(allowedUsers) == 0
	}
	t.Controller.Apply(st)
}

// extractMentions parses <@U...> mentions from a string into the target map.
func extractMentions(s string, target map[string]bool) {
	rest := s
	for {
		start := strings.Index(rest, "<@")
		if start < 0 {
			break
		}
		end := strings.Index(rest[start:], ">")
		if end < 0 {
			break
		}
		uid := rest[start+2 : start+end]

		// Strip display name suffix: <@U12345|sttts> → U12345
		if idx := strings.Index(uid, "|"); idx >= 0 {
			uid = uid[:idx]
		}
		target[uid] = true
		rest = rest[start+end+1:]
	}
}

// updateTitle updates the thread parent message to reflect current access state.
func (t *Thread) updateTitle() {
	t.mu.Lock()
	threadTS := t.threadTS
	t.mu.Unlock()

	if threadTS == "" {
		return
	}

	label := t.formatTitle()
	t.logSlack("updateMessage(title)", label)
	t.client.UpdateMessage(
		t.channel,
		threadTS,
		slackapi.MsgOptionBlocks(t.slagentSection(label)),
		slackapi.MsgOptionText(label, false),
	)
}


// SetModeSuffix sets a suffix appended to the thread title (e.g. " — 📋 planning")
// and updates the thread parent message. Pass "" to clear.
func (t *Thread) SetModeSuffix(suffix string) {
	t.mu.Lock()
	old := t.modeSuffix
	t.modeSuffix = suffix
	t.mu.Unlock()

	if suffix != old {
		t.updateTitle()
	}
}

// parseInstancePrefix checks if text starts with a :shortcode:: prefix (emoji + colon).
// The format is ":shortcode:: message" which renders in Slack as "🦊: message".
// Returns the shortcode (instance ID), remaining text, and whether a prefix was found.
func parseInstancePrefix(text string) (instanceID, rest string, targeted bool) {
	if !strings.HasPrefix(text, ":") {
		return "", text, false
	}

	// Find the closing colon of the shortcode
	end := strings.Index(text[1:], ":")
	if end < 0 {
		return "", text, false
	}
	shortcode := text[1 : end+1]

	// Verify it's a known identity emoji
	if _, ok := identityEmojis[shortcode]; !ok {
		return "", text, false
	}

	rest = text[end+2:] // skip past ":shortcode:"

	// Require colon after shortcode: ":fox_face::" or ":fox_face: :" (Slack inserts spaces)
	// Trim any spaces Slack may have inserted between shortcode and trailing colon.
	trimmed := strings.TrimLeft(rest, " ")
	if !strings.HasPrefix(trimmed, ":") {
		return "", text, false
	}
	rest = strings.TrimLeft(trimmed[1:], " ")
	return shortcode, rest, true
}

// ParseMessage extracts the target instance and cleaned text from a Slack message.
// Strips leading @mentions, then checks for :shortcode: prefix.
func ParseMessage(text string) (instanceID, cleaned string, targeted bool) {
	return parseMessage(text)
}

func parseMessage(text string) (instanceID, cleaned string, targeted bool) {
	s := text

	// Strip leading @mentions (Slack format: <@U123>)
	for strings.HasPrefix(s, "<@") {
		if idx := strings.Index(s, ">"); idx >= 0 {
			s = strings.TrimLeft(s[idx+1:], " ")
		} else {
			break
		}
	}
	return parseInstancePrefix(s)
}

// handleCommand processes /open, /lock, /close, /observe, and /help commands.
// Returns (handled, feedback): handled is true for known commands,
// feedback is a status message to post in the thread.
func (t *Thread) handleCommand(userID, cmd string) (bool, string) {
	cmd = strings.TrimSpace(cmd)
	if strings.HasPrefix(cmd, "/help") {
		return true, t.helpText()
	}

	handled, feedback := t.HandleCommand(userID, cmd)
	if !handled {
		return false, ""
	}

	// Update thread parent to reflect new access state (only if we created the thread)
	if !t.joined {
		t.updateTitle()
	}
	return true, feedback
}

// mistargeted checks if a message looks like a failed attempt at :shortcode:: targeting.
// Detects two patterns:
//   - ":fox_face: /cmd" — shortcode with single colon (missing trailing colon)
//   - "🦊 /cmd" — Unicode emoji instead of shortcode syntax
//
// Returns a hint message or "" if no near-miss detected.
func mistargeted(text string) string {
	// Strip leading @mentions
	s := text
	for strings.HasPrefix(s, "<@") {
		if idx := strings.Index(s, ">"); idx >= 0 {
			s = strings.TrimLeft(s[idx+1:], " ")
		} else {
			break
		}
	}

	// Pattern 1: ":shortcode: /cmd" (single colon, missing trailing colon)
	if strings.HasPrefix(s, ":") {
		end := strings.Index(s[1:], ":")
		if end >= 0 {
			shortcode := s[1 : end+1]
			if _, ok := identityEmojis[shortcode]; ok {
				rest := strings.TrimLeft(s[end+2:], " ")
				if strings.HasPrefix(rest, "/") {
					return fmt.Sprintf("💡 Use `:%s:: %s` (with `::`) to target an instance.", shortcode, rest)
				}
			}
		}
	}

	// Pattern 2: "🦊 /cmd" or "🦊: /cmd" (Unicode emoji, with optional colon/spaces)
	for emoji, shortcode := range reverseEmojis {
		if strings.HasPrefix(s, emoji) {
			rest := strings.TrimLeft(s[len(emoji):], ": ")
			if strings.HasPrefix(rest, "/") {
				return fmt.Sprintf("💡 Use `:%s:: %s` (with `::`) to target an instance.", shortcode, rest)
			}
		}
	}
	return ""
}

// handleSandboxCommand posts an interactive toggle for sandbox enabled/disabled
// and polls for the owner's reaction. Returns the selected value (true=enable,
// false=disable) and whether a selection was made.
func (t *Thread) handleSandboxCommand() (*bool, bool) {
	t.mu.Lock()
	threadTS := t.threadTS
	t.mu.Unlock()

	if threadTS == "" {
		return nil, false
	}

	text := fmt.Sprintf("%s 🔒 *Sandbox*\n> 1️⃣  *Enable* — OS-level filesystem and network isolation\n> 2️⃣  *Disable* — No sandbox restrictions", t.emoji)
	reactions := []string{"one", "two", "x"}
	msgTS, err := t.PostPrompt(text, reactions)
	if err != nil {
		return nil, false
	}

	// Poll for reaction (up to 2 minutes)
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		selected, err := t.PollReaction(msgTS, reactions)
		if err != nil {
			continue
		}
		switch selected {
		case "one":
			t.RemoveAllReactions(msgTS, reactions)
			t.UpdateMessage(msgTS, fmt.Sprintf("%s 🔒 *Sandbox*: 👉 *enabled*", t.emoji))
			v := true
			return &v, true
		case "two":
			t.RemoveAllReactions(msgTS, reactions)
			t.UpdateMessage(msgTS, fmt.Sprintf("%s 🔒 *Sandbox*: 👉 *disabled*", t.emoji))
			v := false
			return &v, true
		case "x":
			t.RemoveAllReactions(msgTS, reactions)
			t.UpdateMessage(msgTS, fmt.Sprintf("%s 🔒 *Sandbox*: ❌ cancelled", t.emoji))
			return nil, false
		}
	}

	// Timeout
	t.RemoveAllReactions(msgTS, reactions)
	t.UpdateMessage(msgTS, fmt.Sprintf("%s 🔒 *Sandbox*: ⏰ timed out", t.emoji))
	return nil, false
}

// isMistargetedToUs returns true if the message starts with our emoji shortcode
// but without the trailing colon (e.g. ":fox_face: text" instead of ":fox_face:: text").
func isMistargetedToUs(text, instanceID string) bool {
	// Strip leading @mentions
	s := text
	for strings.HasPrefix(s, "<@") {
		if idx := strings.Index(s, ">"); idx >= 0 {
			s = strings.TrimLeft(s[idx+1:], " ")
		} else {
			break
		}
	}

	// Check for ":instanceID: text" (single colon)
	prefix := ":" + instanceID + ": "
	if strings.HasPrefix(s, prefix) {
		return true
	}

	// Check for Unicode emoji prefix
	if emoji, ok := identityEmojis[instanceID]; ok {
		if strings.HasPrefix(s, emoji) {
			rest := strings.TrimLeft(s[len(emoji):], " ")
			// Make sure it's not the proper "::" syntax (already handled as targeted)
			if !strings.HasPrefix(rest, ":") {
				return true
			}
		}
	}

	return false
}

// helpText returns the help message for the thread.
func (t *Thread) helpText() string {
	emoji := t.emoji
	id := t.instanceID
	return fmt.Sprintf(""+
		"*slaude — thread commands*\n"+
		"\n"+
		"*Targeting* (type `:%s::` in Slack, renders as %s:)\n"+
		"  `:%s:: message` — address this instance\n"+
		"  `:%s:: /command` — send command exclusively to this instance\n"+
		"  Messages without prefix are broadcast to all instances.\n"+
		"\n"+
		"*Access control* (owner only)\n"+
		"  `:%s:: /open` — open thread for everyone\n"+
		"  `:%s:: /open @user` — allow specific users\n"+
		"  `:%s:: /lock` — lock to owner only\n"+
		"  `:%s:: /lock @user` — ban specific users\n"+
		"  `:%s:: /close` — alias for /lock\n"+
		"  `:%s:: /observe` — toggle observe mode (read all, respond to owner)\n"+
		"\n"+
		"*Session* (owner only)\n"+
		"  `:%s:: /sandbox` — toggle sandbox on/off (restarts session)\n"+
		"\n"+
		"*Control*\n"+
		"  `stop` — interrupt current turn (all instances, anyone)\n"+
		"  `:%s:: stop` — interrupt this instance only\n"+
		"  `quit` — terminate session (owner only)\n"+
		"  `:%s:: quit` — terminate this instance only\n"+
		"  `:%s:: /help` — show this help\n"+
		"  Other `/commands` are forwarded to Claude.",
		id, emoji,
		id, id, id, id, id, id, id, id, id, id, id, id)
}

