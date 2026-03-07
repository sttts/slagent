// Package slagent provides a unified streaming interface for Slack agent sessions.
//
// It supports two backends transparently:
//   - Native streaming (bot tokens, xoxb-*): uses chat.startStream/appendStream/stopStream
//   - Compat streaming (session/user tokens): uses chat.postMessage/chat.update
//
// The backend is selected automatically based on the token type.
package slagent

import (
	"time"

	slackapi "github.com/slack-go/slack"
)

// Kind describes the semantic type of streamed content.
type Kind int

const (
	KindText     Kind = iota // Actual response text (markdown)
	KindThinking             // Internal reasoning / chain-of-thought
	KindTool                 // Tool invocation activity
	KindStatus               // Transient status ("searching...", "compiling...")
)

// Reply is a message from a thread participant.
type Reply struct {
	User   string // Display name
	UserID string // Slack user ID
	Text   string
}

// ThreadOption configures a Thread.
type ThreadOption func(*threadConfig)

type threadConfig struct {
	ownerID           string
	openAccess        bool
	pollInterval      time.Duration
	bufferSize        int
	markdownConverter func(string) string
	cookie            string // xoxd-... for session tokens
}

func defaultConfig() threadConfig {
	return threadConfig{
		pollInterval:      3 * time.Second,
		bufferSize:        256,
		markdownConverter: MarkdownToMrkdwn,
	}
}

// WithOwner restricts the thread to only accept input from the given user ID.
func WithOwner(userID string) ThreadOption {
	return func(c *threadConfig) { c.ownerID = userID }
}

// WithOpenAccess allows all thread participants to send input.
func WithOpenAccess() ThreadOption {
	return func(c *threadConfig) { c.openAccess = true }
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

// WithCookie sets the session cookie for xoxc token authentication.
func WithCookie(cookie string) ThreadOption {
	return func(c *threadConfig) { c.cookie = cookie }
}

// NewSlackClient creates a *slack.Client with optional cookie support.
func NewSlackClient(token, cookie string) *slackapi.Client {
	if cookie != "" {
		return slackapi.New(token, slackapi.OptionHTTPClient(
			&cookieHTTPClient{cookie: cookie},
		))
	}
	return slackapi.New(token)
}
