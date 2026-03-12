// Package slagent provides a unified streaming interface for Slack agent sessions.
//
// It supports two backends transparently:
//   - Native streaming (bot tokens, xoxb-*): uses chat.startStream/appendStream/stopStream
//   - Compat streaming (session/user tokens): uses chat.postMessage/chat.update
//
// The backend is selected automatically based on the token type.
package slagent

import (
	"context"
	"crypto/rand"
	"io"
	"sort"
	"strings"
	"time"

	slackapi "github.com/slack-go/slack"
)

// Tool status constants for use with Turn.Tool().
const (
	ToolRunning = "running"
	ToolDone    = "done"
	ToolError   = "error"
)

// Prompter is the interface for interactive prompt flows (permission, plan mode, sandbox).
// Thread implements this implicitly.
type Prompter interface {
	PostPrompt(text string, reactions []string) (string, error)
	PollReaction(ts string, expected []string) (string, error)
	DeleteMessage(ts string) error
	UpdateMessage(ts, text string) error
	RemoveAllReactions(ts string, reactions []string)
	AddReaction(ts, name string)
	GetReactions(ts string) ([]slackapi.ItemReaction, error)
}

// PollReaction posts a prompt and polls for a reaction until one is selected,
// the context is cancelled, or the timeout expires. Returns the selected reaction
// name or "" on timeout/cancel.
func PollReaction(ctx context.Context, p Prompter, msgTS string,
	reactions []string, timeout time.Duration) (string, error) {

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
		selected, err := p.PollReaction(msgTS, reactions)
		if err != nil {
			continue
		}
		if selected != "" {
			return selected, nil
		}
	}
	return "", nil
}

// Message is a typed event from a thread participant.
type Message interface{ message() }

// TextMessage is a regular text message from a user.
type TextMessage struct {
	User, UserID, Text string
	Observe            bool // observe-only: user not authorized to get responses
}

// CommandMessage is a /command to forward to Claude.
type CommandMessage struct {
	User, UserID, Command string
}

// StopMessage requests interrupting the current turn.
type StopMessage struct {
	User, UserID string
}

// QuitMessage requests terminating the session (owner only).
type QuitMessage struct {
	User, UserID string
}

// SandboxToggle requests a sandbox enable/disable (owner only).
type SandboxToggle struct {
	User, UserID string
	Enable       *bool // nil if cancelled/timed out
}

func (TextMessage) message()    {}
func (CommandMessage) message() {}
func (StopMessage) message()    {}
func (QuitMessage) message()    {}
func (SandboxToggle) message()  {}

// ThreadOption configures a Thread.
type ThreadOption func(*threadConfig)

type threadConfig struct {
	ownerID           string
	instanceID        string
	openAccess        bool
	observe           bool
	pollInterval      time.Duration
	bufferSize        int
	markdownConverter func(string) string
	apiURL            string     // base URL for native streaming API calls (testing)
	slackLog          io.Writer  // if non-nil, log all Slack API calls here
	thinkingEmoji     string     // Slack shortcode for thinking indicator (e.g. ":claude:")
}

// identityEmojis maps Slack short codes to emoji for identity selection.
// The instance ID IS the short code (e.g. "dog"), making URLs readable:
// https://team.slack.com/archives/C123/p1234567890#dog
var identityEmojis = map[string]string{
	// Animals
	"dog": "🐶", "cat": "🐱", "mouse": "🐭", "hamster": "🐹",
	"rabbit": "🐰", "fox_face": "🦊", "bear": "🐻", "panda_face": "🐼",
	"koala": "🐨", "tiger": "🐯", "lion_face": "🦁", "cow": "🐮",
	"pig": "🐷", "frog": "🐸", "monkey": "🐵", "chicken": "🐔",
	"penguin": "🐧", "bird": "🐦", "eagle": "🦅", "duck": "🦆",
	"owl": "🦉", "bat": "🦇", "wolf": "🐺", "boar": "🐗",
	"horse": "🐴", "unicorn_face": "🦄", "bee": "🐝", "bug": "🐛",
	"butterfly": "🦋", "snail": "🐌", "ladybug": "🐞", "ant": "🐜",
	"turtle": "🐢", "snake": "🐍", "lizard": "🦎", "t-rex": "🦖",
	"sauropod": "🦕", "octopus": "🐙", "squid": "🦑", "shrimp": "🦐",
	"lobster": "🦞", "crab": "🦀", "blowfish": "🐡", "fish": "🐠",
	"dolphin": "🐬", "whale": "🐳", "shark": "🦈", "crocodile": "🐊",
	"leopard": "🐆", "zebra_face": "🦓", "gorilla": "🦍", "elephant": "🐘",
	"hippopotamus": "🦛", "rhinoceros": "🦏", "camel": "🐫", "giraffe_face": "🦒",
	"kangaroo": "🦘", "ox": "🐂", "deer": "🦌", "rooster": "🐓",
	"turkey": "🦃", "peacock": "🦚", "parrot": "🦜", "swan": "🦢",
	"flamingo": "🦩", "raccoon": "🦝", "badger": "🦡", "otter": "🦦",
	"sloth": "🦥", "hedgehog": "🦔", "chipmunk": "🐿",
	// Neutral person heads
	"baby": "👶", "boy": "👦", "girl": "👧", "man": "👨",
	"woman": "👩", "older_man": "👴", "older_woman": "👵", "child": "🧒",
	"adult": "🧑",
}

// identityKeys is the sorted list of short codes for random selection.
var identityKeys []string

func init() {
	identityKeys = make([]string, 0, len(identityEmojis))
	for k := range identityEmojis {
		identityKeys = append(identityKeys, k)
	}
	// Sort for deterministic ordering
	sort.Strings(identityKeys)
}

// randomInstanceID picks a random emoji short code as instance ID.
func randomInstanceID() string {
	b := make([]byte, 4)
	rand.Read(b)
	var n uint32
	for _, v := range b {
		n = n*256 + uint32(v)
	}
	return identityKeys[n%uint32(len(identityKeys))]
}

// InstanceEmoji returns the emoji for a given instance ID (short code).
// Falls back to 🤖 for unknown IDs.
func InstanceEmoji(instanceID string) string {
	if e, ok := identityEmojis[instanceID]; ok {
		return e
	}
	return "🤖"
}

// ShortcodesToUnicode converts Slack shortcodes to Unicode emoji.
// Handles :lock:, :thread:, and all identity emoji shortcodes.
func ShortcodesToUnicode(text string) string {
	text = strings.ReplaceAll(text, ":eyes:", "👀")
	text = strings.ReplaceAll(text, ":lock:", "🔒")
	text = strings.ReplaceAll(text, ":thread:", "🧵")
	for shortcode, emoji := range identityEmojis {
		text = strings.ReplaceAll(text, ":"+shortcode+":", emoji)
	}
	return text
}

func defaultConfig() threadConfig {
	return threadConfig{
		pollInterval:      1 * time.Second,
		bufferSize:        256,
		markdownConverter: MarkdownToMrkdwn,
		thinkingEmoji:     ":claude:",
	}
}

// WithThinkingEmoji sets the Slack shortcode used as thinking/running indicator.
// Default is ":claude:". Example: ":claude-thinking:" for workspaces with custom emoji.
func WithThinkingEmoji(shortcode string) ThreadOption {
	return func(c *threadConfig) { c.thinkingEmoji = shortcode }
}

// WithOwner restricts the thread to only accept input from the given user ID.
func WithOwner(userID string) ThreadOption {
	return func(c *threadConfig) { c.ownerID = userID }
}

// WithOpenAccess allows all thread participants to send input.
func WithOpenAccess() ThreadOption {
	return func(c *threadConfig) { c.openAccess = true }
}

// WithObserve enables observe mode: all messages are delivered for passive
// learning, but the agent only responds to authorized users.
func WithObserve() ThreadOption {
	return func(c *threadConfig) { c.observe = true }
}

// WithPollInterval sets the polling interval for new replies.
func WithPollInterval(d time.Duration) ThreadOption {
	return func(c *threadConfig) { c.pollInterval = d }
}

// WithBufferSize sets the text buffer size before flushing (native backend).
func WithBufferSize(n int) ThreadOption {
	return func(c *threadConfig) { c.bufferSize = n }
}

// WithMarkdownConverter sets a custom markdown-to-mrkdwn converter.
func WithMarkdownConverter(fn func(string) string) ThreadOption {
	return func(c *threadConfig) { c.markdownConverter = fn }
}

// withAPIURL sets the base URL for native streaming API calls (testing only).
func withAPIURL(url string) ThreadOption {
	return func(c *threadConfig) { c.apiURL = url }
}

// WithInstanceID sets a specific instance ID for message tagging.
// If not set, a random one is generated. On resume, pass the original
// instance ID so the poller correctly identifies messages from this session.
func WithInstanceID(id string) ThreadOption {
	return func(c *threadConfig) { c.instanceID = id }
}

// WithSlackLog enables logging of all Slack API calls to w.
func WithSlackLog(w io.Writer) ThreadOption {
	return func(c *threadConfig) { c.slackLog = w }
}

