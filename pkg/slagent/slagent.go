// Package slagent provides a unified streaming interface for Slack agent sessions.
//
// It supports two backends transparently:
//   - Native streaming (bot tokens, xoxb-*): uses chat.startStream/appendStream/stopStream
//   - Compat streaming (session/user tokens): uses chat.postMessage/chat.update
//
// The backend is selected automatically based on the token type.
package slagent

import (
	"fmt"
	"net/http"
	"time"

	slackapi "github.com/slack-go/slack"
)

// Tool status constants for use with Turn.Tool().
const (
	ToolRunning = "running"
	ToolDone    = "done"
	ToolError   = "error"
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
	apiURL            string // base URL for native streaming API calls (testing)
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

// withAPIURL sets the base URL for native streaming API calls (testing only).
func withAPIURL(url string) ThreadOption {
	return func(c *threadConfig) { c.apiURL = url }
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

// cookieHTTPClient wraps http.Client and injects the d= cookie on every request.
type cookieHTTPClient struct {
	inner  *http.Client
	cookie string
}

func (c *cookieHTTPClient) Do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Cookie", fmt.Sprintf("d=%s", c.cookie))
	if c.inner == nil {
		return http.DefaultClient.Do(req)
	}
	return c.inner.Do(req)
}
