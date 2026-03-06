// Package slack handles Slack thread mirroring for pairplan.
package slack

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	slackapi "github.com/slack-go/slack"
)

// Client wraps the Slack API for pairplan's needs.
type Client struct {
	api       *slackapi.Client
	channel   string
	threadTS  string // parent message timestamp (thread identifier)
	lastTS    string // timestamp of last seen reply
	userCache map[string]string
	mu        sync.Mutex
}

// Credentials holds the stored Slack bot token.
type Credentials struct {
	BotToken string `json:"bot_token"`
}

// CredentialsPath returns the path to the credentials file.
func CredentialsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "pairplan", "credentials.json")
}

// LoadCredentials reads stored credentials.
func LoadCredentials() (*Credentials, error) {
	data, err := os.ReadFile(CredentialsPath())
	if err != nil {
		return nil, fmt.Errorf("no credentials found (run 'pairplan auth'): %w", err)
	}
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	if creds.BotToken == "" {
		return nil, fmt.Errorf("empty bot token (run 'pairplan auth')")
	}
	return &creds, nil
}

// SaveCredentials writes credentials to disk.
func SaveCredentials(creds *Credentials) error {
	path := CredentialsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// New creates a Slack client from stored credentials.
func New(channel string) (*Client, error) {
	creds, err := LoadCredentials()
	if err != nil {
		return nil, err
	}
	return &Client{
		api:       slackapi.New(creds.BotToken),
		channel:   channel,
		userCache: make(map[string]string),
	}, nil
}

// NewWithToken creates a Slack client with an explicit token.
func NewWithToken(token, channel string) *Client {
	return &Client{
		api:       slackapi.New(token),
		channel:   channel,
		userCache: make(map[string]string),
	}
}

// StartThread posts the thread parent message and returns the thread URL.
func (c *Client) StartThread(topic string) (string, error) {
	text := fmt.Sprintf(":clipboard: *Planning session: %s*", topic)

	_, ts, err := c.api.PostMessage(
		c.channel,
		slackapi.MsgOptionText(text, false),
	)
	if err != nil {
		return "", fmt.Errorf("post thread parent: %w", err)
	}

	c.mu.Lock()
	c.threadTS = ts
	c.lastTS = ts
	c.mu.Unlock()

	// Build permalink
	link, err := c.api.GetPermalink(&slackapi.PermalinkParameters{
		Channel: c.channel,
		Ts:      ts,
	})
	if err != nil {
		// Non-fatal: we still have the thread
		return fmt.Sprintf("(thread started, permalink unavailable: %v)", err), nil
	}
	return link, nil
}

// PostClaudeMessage posts Claude's response to the thread.
func (c *Client) PostClaudeMessage(text string) error {
	if c.threadTS == "" {
		return fmt.Errorf("no active thread")
	}
	msg := fmt.Sprintf(":robot_face: %s", text)
	// Truncate if too long for Slack (max ~40k chars, we use 4000 as a sane limit)
	if len(msg) > 4000 {
		msg = msg[:3990] + "\n_(truncated)_"
	}
	_, _, err := c.api.PostMessage(
		c.channel,
		slackapi.MsgOptionText(msg, false),
		slackapi.MsgOptionTS(c.threadTS),
	)
	return err
}

// PostUserMessage posts the local user's message to the thread.
func (c *Client) PostUserMessage(user, text string) error {
	if c.threadTS == "" {
		return fmt.Errorf("no active thread")
	}
	msg := fmt.Sprintf(":bust_in_silhouette: @%s: %s", user, text)
	_, _, err := c.api.PostMessage(
		c.channel,
		slackapi.MsgOptionText(msg, false),
		slackapi.MsgOptionTS(c.threadTS),
	)
	return err
}

// PostToolActivity posts a brief tool activity summary to the thread.
func (c *Client) PostToolActivity(summary string) error {
	if c.threadTS == "" {
		return fmt.Errorf("no active thread")
	}
	msg := fmt.Sprintf(":wrench: %s", summary)
	_, _, err := c.api.PostMessage(
		c.channel,
		slackapi.MsgOptionText(msg, false),
		slackapi.MsgOptionTS(c.threadTS),
	)
	return err
}

// PostSessionEnd posts a session-ended message.
func (c *Client) PostSessionEnd() error {
	if c.threadTS == "" {
		return nil
	}
	_, _, err := c.api.PostMessage(
		c.channel,
		slackapi.MsgOptionText(":white_check_mark: Planning session ended.", false),
		slackapi.MsgOptionTS(c.threadTS),
	)
	return err
}

// Reply is a message from a Slack thread participant.
type Reply struct {
	User string
	Text string
}

// PollReplies fetches new replies in the thread since the last poll.
func (c *Client) PollReplies() ([]Reply, error) {
	c.mu.Lock()
	threadTS := c.threadTS
	oldest := c.lastTS
	c.mu.Unlock()

	if threadTS == "" {
		return nil, nil
	}

	params := &slackapi.GetConversationRepliesParameters{
		ChannelID: c.channel,
		Timestamp: threadTS,
		Oldest:    oldest,
	}

	msgs, _, _, err := c.api.GetConversationReplies(params)
	if err != nil {
		return nil, fmt.Errorf("get replies: %w", err)
	}

	var replies []Reply
	for _, msg := range msgs {
		// Skip the parent message and our own bot messages
		if msg.Timestamp == threadTS || msg.Timestamp <= oldest {
			continue
		}
		if msg.BotID != "" {
			continue
		}

		user := c.resolveUser(msg.User)
		replies = append(replies, Reply{User: user, Text: msg.Text})

		c.mu.Lock()
		if msg.Timestamp > c.lastTS {
			c.lastTS = msg.Timestamp
		}
		c.mu.Unlock()
	}

	return replies, nil
}

// ThreadTS returns the thread timestamp.
func (c *Client) ThreadTS() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.threadTS
}

func (c *Client) resolveUser(userID string) string {
	c.mu.Lock()
	if name, ok := c.userCache[userID]; ok {
		c.mu.Unlock()
		return name
	}
	c.mu.Unlock()

	info, err := c.api.GetUserInfo(userID)
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

	c.mu.Lock()
	c.userCache[userID] = name
	c.mu.Unlock()

	return name
}

// PollInterval is the recommended interval between PollReplies calls.
const PollInterval = 3 * time.Second
