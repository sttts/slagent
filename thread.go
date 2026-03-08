package slagent

import (
	"fmt"
	"strings"
	"sync"

	slackapi "github.com/slack-go/slack"
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

// logSlack writes a Slack API action to the Thread's log writer if configured.
func (t *Thread) logSlack(action, content string) {
	if t.config.slackLog == nil {
		return
	}
	fmt.Fprintf(t.config.slackLog, "[slack] %s: %s\n", action, content)
}

// Thread manages an agent session in a Slack thread.
type Thread struct {
	api        *slackapi.Client
	token      string // raw token for backend detection and native API calls
	channel    string
	threadTS   string
	instanceID string // unique per slaude instance, used in block_id
	blockID    string // "slagent-{instanceID}", cached
	emoji      string // identity emoji derived from instanceID
	config     threadConfig

	// Permissions
	ownerID    string
	openAccess bool

	// Reply tracking
	lastTS string

	// User resolution
	userCache map[string]string

	mu sync.Mutex
}

// NewThread creates a new thread manager.
// The token is needed for backend detection (xoxb- → native, xoxc-/xoxp- → compat)
// and for native streaming API calls.
func NewThread(client *slackapi.Client, token, channel string, opts ...ThreadOption) *Thread {
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}

	instanceID := cfg.instanceID
	if instanceID == "" {
		instanceID = randomInstanceID()
	}

	t := &Thread{
		api:        client,
		token:      token,
		channel:    channel,
		instanceID: instanceID,
		blockID:    slagentBlockPrefix + instanceID,
		emoji:      InstanceEmoji(instanceID),
		config:     cfg,
		ownerID:    cfg.ownerID,
		openAccess: cfg.openAccess,
		userCache:  make(map[string]string),
	}
	return t
}

// Start posts the initial thread message and returns the thread URL.
func (t *Thread) Start(title string) (string, error) {
	label := fmt.Sprintf(":%s::thread: Agent session", t.instanceID)
	if title != "" {
		label = fmt.Sprintf(":%s::thread: %s", t.instanceID, title)
	}

	t.logSlack("postMessage(thread-start)", label)
	_, ts, err := t.api.PostMessage(
		t.channel,
		slackapi.MsgOptionBlocks(t.slagentSection(label)),
		slackapi.MsgOptionText(label, false),
	)
	if err != nil {
		return "", fmt.Errorf("post thread parent: %w", err)
	}

	t.mu.Lock()
	t.threadTS = ts
	t.lastTS = ts
	t.mu.Unlock()

	link, err := t.api.GetPermalink(&slackapi.PermalinkParameters{
		Channel: t.channel,
		Ts:      ts,
	})
	if err != nil {
		return fmt.Sprintf("(thread started, permalink unavailable: %v)", err), nil
	}
	return link, nil
}

// Resume attaches to an existing thread.
func (t *Thread) Resume(threadTS string) {
	t.mu.Lock()
	t.threadTS = threadTS
	t.lastTS = threadTS
	t.mu.Unlock()
}

// NewTurn begins a new response turn.
func (t *Thread) NewTurn() Turn {
	t.mu.Lock()
	threadTS := t.threadTS
	t.mu.Unlock()

	// Select backend based on token type
	var w turnWriter
	if isNativeToken(t.token) {
		w = newNativeTurn(t.token, t.config.apiURL, t.channel, threadTS, t.config.markdownConverter, t.config.bufferSize)
	} else {
		w = newCompatTurn(t.api, t.channel, threadTS, t.blockID, t.emoji, t.config.slackLog)
	}
	return &turnImpl{w: w}
}

// Post sends a plain message in the thread.
func (t *Thread) Post(text string) (string, error) {
	t.mu.Lock()
	threadTS := t.threadTS
	t.mu.Unlock()

	if threadTS == "" {
		return "", fmt.Errorf("no active thread")
	}

	t.logSlack("postMessage(post)", text)
	_, ts, err := t.api.PostMessage(
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
	_, _, _, err := t.api.UpdateMessage(
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
	_, ts, err := t.api.PostMessage(
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
	if !isNativeToken(t.token) {
		for _, r := range reactions {
			t.api.AddReaction(r, slackapi.ItemRef{
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
	item, err := t.api.GetReactions(slackapi.ItemRef{
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
			if u == t.ownerID {
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

	return "", nil
}

// FinalizeReaction cleans up reactions after the owner has made a selection.
// It removes all non-selected reactions and re-adds the selected one
// (which was toggled off when the owner clicked it).
func (t *Thread) FinalizeReaction(msgTS, selected string, all []string) {
	ref := slackapi.ItemRef{Channel: t.channel, Timestamp: msgTS}

	// Re-add the selected reaction (owner's click toggled it off)
	t.api.AddReaction(selected, ref)

	// Remove the non-selected reactions
	for _, r := range all {
		if r != selected {
			t.api.RemoveReaction(r, ref)
		}
	}
}

// DeleteMessage deletes a message from the thread.
func (t *Thread) DeleteMessage(msgTS string) error {
	t.logSlack("deleteMessage", msgTS)
	_, _, err := t.api.DeleteMessage(t.channel, msgTS)
	return err
}

// OwnerID returns the configured owner user ID.
func (t *Thread) OwnerID() string {
	return t.ownerID
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
	_, _, err := t.api.PostMessage(
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
	_, _, err := t.api.PostMessage(
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
		_, _, err := t.api.PostMessage(
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

// resolveUser resolves a user ID to a display name, with caching.
func (t *Thread) resolveUser(userID string) string {
	t.mu.Lock()
	if name, ok := t.userCache[userID]; ok {
		t.mu.Unlock()
		return name
	}
	t.mu.Unlock()

	info, err := t.api.GetUserInfo(userID)
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

// isAuthorized checks whether a user is allowed to interact.
func (t *Thread) isAuthorized(userID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.openAccess {
		return true
	}
	if t.ownerID == "" {
		return true // no owner restriction
	}
	return userID == t.ownerID
}

// handleCommand processes !open / !close commands. Returns true if the message was a command.
func (t *Thread) handleCommand(userID, text string) bool {
	cmd := strings.TrimSpace(text)
	if cmd != "!open" && cmd != "!close" {
		return false
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Only the owner can run commands
	if t.ownerID != "" && userID != t.ownerID {
		return false
	}

	t.openAccess = cmd == "!open"
	return true
}
