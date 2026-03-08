package slagent

import (
	"fmt"
	"strings"
	"sync"

	slackapi "github.com/slack-go/slack"
)

// Thread manages an agent session in a Slack thread.
type Thread struct {
	api      *slackapi.Client
	token    string // raw token for backend detection and native API calls
	channel  string
	threadTS string
	config   threadConfig

	// Permissions
	ownerID    string
	openAccess bool

	// Reply tracking
	lastTS   string
	postedTS map[string]bool

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

	t := &Thread{
		api:        client,
		token:      token,
		channel:    channel,
		config:     cfg,
		ownerID:    cfg.ownerID,
		openAccess: cfg.openAccess,
		postedTS:   make(map[string]bool),
		userCache:  make(map[string]string),
	}
	return t
}

// Start posts the initial thread message and returns the thread URL.
func (t *Thread) Start(title string) (string, error) {
	label := "🧵 Agent session"
	if title != "" {
		label = fmt.Sprintf("🧵 %s", title)
	}
	headerText := slackapi.NewTextBlockObject("plain_text", label, true, false)
	header := slackapi.NewHeaderBlock(headerText)

	_, ts, err := t.api.PostMessage(
		t.channel,
		slackapi.MsgOptionBlocks(header),
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

	posted := func(ts string) {
		t.mu.Lock()
		t.postedTS[ts] = true
		t.mu.Unlock()
	}

	// Select backend based on token type
	var w turnWriter
	if isNativeToken(t.token) {
		w = newNativeTurn(t.token, t.config.apiURL, t.channel, threadTS, t.config.markdownConverter, posted, t.config.bufferSize)
	} else {
		w = newCompatTurn(t.api, t.channel, threadTS, posted)
	}
	return &turnImpl{w: w}
}

// Post sends a plain message in the thread.
func (t *Thread) Post(text string) error {
	t.mu.Lock()
	threadTS := t.threadTS
	t.mu.Unlock()

	if threadTS == "" {
		return fmt.Errorf("no active thread")
	}

	_, ts, err := t.api.PostMessage(
		t.channel,
		slackapi.MsgOptionText(text, false),
		slackapi.MsgOptionTS(threadTS),
	)
	if err != nil {
		return err
	}

	t.mu.Lock()
	t.postedTS[ts] = true
	t.mu.Unlock()
	return nil
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

	_, ts, err := t.api.PostMessage(
		t.channel,
		slackapi.MsgOptionText(text, false),
		slackapi.MsgOptionTS(threadTS),
	)
	if err != nil {
		return "", err
	}

	t.mu.Lock()
	t.postedTS[ts] = true
	t.mu.Unlock()

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

	_, ts, err := t.api.PostMessage(
		t.channel,
		slackapi.MsgOptionBlocks(blocks...),
		slackapi.MsgOptionText(fallback, false),
		slackapi.MsgOptionTS(threadTS),
	)
	if err != nil {
		return err
	}

	t.mu.Lock()
	t.postedTS[ts] = true
	t.mu.Unlock()
	return nil
}

// PostUser posts a user message with context block ("👤 @user") and text section.
func (t *Thread) PostUser(user, text string) error {
	t.mu.Lock()
	threadTS := t.threadTS
	t.mu.Unlock()

	if threadTS == "" {
		return fmt.Errorf("no active thread")
	}

	ctx := slackapi.NewContextBlock("",
		slackapi.NewTextBlockObject("mrkdwn", fmt.Sprintf("👤 @%s", user), false, false),
	)
	section := slackapi.NewSectionBlock(
		slackapi.NewTextBlockObject("mrkdwn", text, false, false),
		nil, nil,
	)
	_, ts, err := t.api.PostMessage(
		t.channel,
		slackapi.MsgOptionBlocks(ctx, section),
		slackapi.MsgOptionText(fmt.Sprintf("@%s: %s", user, text), false),
		slackapi.MsgOptionTS(threadTS),
	)
	if err != nil {
		return err
	}

	t.mu.Lock()
	t.postedTS[ts] = true
	t.mu.Unlock()
	return nil
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
		section := slackapi.NewSectionBlock(
			slackapi.NewTextBlockObject("mrkdwn", fenced, false, false),
			nil, nil,
		)
		_, ts, err := t.api.PostMessage(
			t.channel,
			slackapi.MsgOptionBlocks(section),
			slackapi.MsgOptionText(chunk, false),
			slackapi.MsgOptionTS(threadTS),
		)
		if err != nil {
			return err
		}
		t.mu.Lock()
		t.postedTS[ts] = true
		t.mu.Unlock()
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
