// Package slack handles Slack thread mirroring for pairplan.
package slack

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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

	// Token type and identity
	tokenType string // "bot", "user", or "session"
	ownUserID string // set via auth.test for user/session tokens
}

// Credentials holds the stored Slack token.
type Credentials struct {
	Token    string `json:"token,omitempty"`
	Type     string `json:"type,omitempty"`       // "bot", "user", or "session"
	Cookie   string `json:"cookie,omitempty"`      // xoxd-... for xoxc session tokens
	BotToken string `json:"bot_token,omitempty"`   // backwards compat
}

// cookieHTTPClient wraps http.Client and injects the d= cookie on every request.
type cookieHTTPClient struct {
	inner  *http.Client
	cookie string
}

func (c *cookieHTTPClient) Do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Cookie", fmt.Sprintf("d=%s", c.cookie))
	return c.inner.Do(req)
}

// EffectiveToken returns the token to use, preferring Token over BotToken.
func (c *Credentials) EffectiveToken() string {
	if c.Token != "" {
		return c.Token
	}
	return c.BotToken
}

// EffectiveType returns the token type, inferring from prefix if not set.
func (c *Credentials) EffectiveType() string {
	if c.Type != "" {
		return c.Type
	}
	token := c.EffectiveToken()
	switch {
	case strings.HasPrefix(token, "xoxp-"):
		return "user"
	case strings.HasPrefix(token, "xoxc-"):
		return "session"
	default:
		return "bot"
	}
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
	if creds.EffectiveToken() == "" {
		return nil, fmt.Errorf("empty token (run 'pairplan auth')")
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
	token := creds.EffectiveToken()
	tokenType := creds.EffectiveType()

	// Build slack client options, inject cookie for session tokens
	var opts []slackapi.Option
	if creds.Cookie != "" {
		opts = append(opts, slackapi.OptionHTTPClient(
			&cookieHTTPClient{inner: &http.Client{}, cookie: creds.Cookie},
		))
	}

	c := &Client{
		api:       slackapi.New(token, opts...),
		channel:   channel,
		userCache: make(map[string]string),
		tokenType: tokenType,
	}

	// For user/session tokens, resolve own user ID via auth.test
	if tokenType == "user" || tokenType == "session" {
		resp, err := c.api.AuthTest()
		if err != nil {
			return nil, fmt.Errorf("auth.test: %w", err)
		}
		c.ownUserID = resp.UserID
	}
	return c, nil
}

// NewWithToken creates a Slack client with an explicit token.
func NewWithToken(token, channel string) *Client {
	tokenType := "bot"
	if strings.HasPrefix(token, "xoxp-") {
		tokenType = "user"
	}
	return &Client{
		api:       slackapi.New(token),
		channel:   channel,
		userCache: make(map[string]string),
		tokenType: tokenType,
	}
}

// maxBlockTextLen is the maximum text length for a single Slack Section block.
const maxBlockTextLen = 3000

// StartThread posts the thread parent message and returns the thread URL.
func (c *Client) StartThread(topic string) (string, error) {
	headerText := slackapi.NewTextBlockObject("plain_text", fmt.Sprintf("📋 Planning session: %s", topic), true, false)
	header := slackapi.NewHeaderBlock(headerText)

	_, ts, err := c.api.PostMessage(
		c.channel,
		slackapi.MsgOptionBlocks(header),
		slackapi.MsgOptionText(fmt.Sprintf("Planning session: %s", topic), false),
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
		return fmt.Sprintf("(thread started, permalink unavailable: %v)", err), nil
	}
	return link, nil
}

// PostClaudeMessage posts Claude's response to the thread with auto-split.
func (c *Client) PostClaudeMessage(text string) error {
	if c.threadTS == "" {
		return fmt.Errorf("no active thread")
	}

	// Short message: single Section block
	if len(text) <= maxBlockTextLen {
		msg := fmt.Sprintf("🤖 %s", text)
		section := slackapi.NewSectionBlock(
			slackapi.NewTextBlockObject("mrkdwn", msg, false, false),
			nil, nil,
		)
		_, _, err := c.api.PostMessage(
			c.channel,
			slackapi.MsgOptionBlocks(section),
			slackapi.MsgOptionText(msg, false),
			slackapi.MsgOptionTS(c.threadTS),
		)
		return err
	}

	// Long message: split at line boundaries, each chunk in a code block
	chunks := splitAtLines(text, maxBlockTextLen-20) // leave room for ``` markers
	for _, chunk := range chunks {
		wrapped := fmt.Sprintf("```\n%s\n```", chunk)
		section := slackapi.NewSectionBlock(
			slackapi.NewTextBlockObject("mrkdwn", wrapped, false, false),
			nil, nil,
		)
		_, _, err := c.api.PostMessage(
			c.channel,
			slackapi.MsgOptionBlocks(section),
			slackapi.MsgOptionText(chunk, false),
			slackapi.MsgOptionTS(c.threadTS),
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// splitAtLines splits text into chunks of at most maxLen bytes at line boundaries.
func splitAtLines(text string, maxLen int) []string {
	var chunks []string
	for len(text) > 0 {
		if len(text) <= maxLen {
			chunks = append(chunks, text)
			break
		}

		// Find last newline within maxLen
		cut := strings.LastIndex(text[:maxLen], "\n")
		if cut <= 0 {
			// No newline found, hard-cut at maxLen
			cut = maxLen
		} else {
			cut++ // include the newline
		}
		chunks = append(chunks, text[:cut])
		text = text[cut:]
	}
	return chunks
}

// PostUserMessage posts the local user's message to the thread.
func (c *Client) PostUserMessage(user, text string) error {
	if c.threadTS == "" {
		return fmt.Errorf("no active thread")
	}

	ctx := slackapi.NewContextBlock("",
		slackapi.NewTextBlockObject("mrkdwn", fmt.Sprintf("👤 @%s", user), false, false),
	)
	section := slackapi.NewSectionBlock(
		slackapi.NewTextBlockObject("mrkdwn", text, false, false),
		nil, nil,
	)
	_, _, err := c.api.PostMessage(
		c.channel,
		slackapi.MsgOptionBlocks(ctx, section),
		slackapi.MsgOptionText(fmt.Sprintf("@%s: %s", user, text), false),
		slackapi.MsgOptionTS(c.threadTS),
	)
	return err
}

// PostToolActivity posts a brief tool activity summary to the thread.
func (c *Client) PostToolActivity(summary string) error {
	if c.threadTS == "" {
		return fmt.Errorf("no active thread")
	}

	ctx := slackapi.NewContextBlock("",
		slackapi.NewTextBlockObject("mrkdwn", fmt.Sprintf("🔧 %s", summary), false, false),
	)
	_, _, err := c.api.PostMessage(
		c.channel,
		slackapi.MsgOptionBlocks(ctx),
		slackapi.MsgOptionText(fmt.Sprintf("🔧 %s", summary), false),
		slackapi.MsgOptionTS(c.threadTS),
	)
	return err
}

// PostSessionEnd posts a session-ended message.
func (c *Client) PostSessionEnd() error {
	if c.threadTS == "" {
		return nil
	}

	section := slackapi.NewSectionBlock(
		slackapi.NewTextBlockObject("mrkdwn", "✅ Planning session ended.", false, false),
		nil, nil,
	)
	divider := slackapi.NewDividerBlock()
	_, _, err := c.api.PostMessage(
		c.channel,
		slackapi.MsgOptionBlocks(section, divider),
		slackapi.MsgOptionText("Planning session ended.", false),
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
		// Skip the parent message and already-seen messages
		if msg.Timestamp == threadTS || msg.Timestamp <= oldest {
			continue
		}

		// Skip our own messages: by UserID for user/session tokens, by BotID for bot tokens
		if c.tokenType == "user" || c.tokenType == "session" {
			if msg.User == c.ownUserID {
				continue
			}
		} else {
			if msg.BotID != "" {
				continue
			}
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

// Channel represents a Slack channel for listing.
type Channel struct {
	ID           string
	Name         string
	Type         string  // "channel", "group", "mpim"
	LastActivity float64 // unix timestamp of last message
}

// ListProgress receives progress updates during ListChannels.
type ListProgress struct {
	Phase string // "listing" or "checking"
	Done  int
	Total int
}

// ListChannels returns channels the user is a member of that had activity
// in the last 30 days, sorted by most recent first.
// Channels are grouped: channels/groups first, then mpim.
// mpim names are resolved to @displayname format.
func (c *Client) ListChannels(progress func(ListProgress)) ([]Channel, error) {
	params := &slackapi.GetConversationsForUserParameters{
		Types:           []string{"public_channel", "private_channel", "mpim", "im"},
		Limit:           200,
		ExcludeArchived: true,
	}

	// Collect all candidate channels
	type candidate struct {
		id, name, chType string
		members          []string // for mpim name resolution
	}
	var candidates []candidate
	for {
		channels, cursor, err := c.api.GetConversationsForUser(params)
		if err != nil {
			return nil, fmt.Errorf("get conversations: %w", err)
		}
		for _, ch := range channels {
			chType := "channel"
			switch {
			case ch.IsIM:
				chType = "im"
			case ch.IsMpIM:
				chType = "mpim"
			case ch.IsPrivate:
				chType = "group"
			}
			name := ch.Name
			if name == "" {
				name = ch.ID
			}
			var members []string
			if ch.IsIM {
				members = []string{ch.User}
			} else {
				members = ch.Members
			}
			candidates = append(candidates, candidate{ch.ID, name, chType, members})
		}
		if progress != nil {
			progress(ListProgress{Phase: "listing", Done: len(candidates)})
		}
		if cursor == "" {
			break
		}
		params.Cursor = cursor
	}

	// Fetch last message timestamp for each channel concurrently
	type indexedResult struct {
		idx int
		ch  Channel
		ok  bool
	}
	cutoff := float64(time.Now().Add(-30 * 24 * time.Hour).Unix())
	results := make(chan indexedResult, len(candidates))
	total := len(candidates)

	if progress != nil {
		progress(ListProgress{Phase: "checking", Done: 0, Total: total})
	}

	// Limit concurrency to 10 to avoid rate limits
	var checked int
	sem := make(chan struct{}, 10)
	for i, cand := range candidates {
		sem <- struct{}{}
		go func(i int, cand candidate) {
			defer func() { <-sem }()
			hist, err := c.api.GetConversationHistory(&slackapi.GetConversationHistoryParameters{
				ChannelID: cand.id,
				Limit:     1,
			})
			if err != nil || hist == nil || len(hist.Messages) == 0 {
				results <- indexedResult{idx: i}
				return
			}
			ts, _ := strconv.ParseFloat(hist.Messages[0].Timestamp, 64)
			if ts < cutoff {
				results <- indexedResult{idx: i}
				return
			}

			// Resolve member names for mpim and im
			name := cand.name
			if (cand.chType == "mpim" || cand.chType == "im") && len(cand.members) > 0 {
				name = c.resolveMemberNames(cand.members)
			}

			results <- indexedResult{
				idx: i,
				ch:  Channel{ID: cand.id, Name: name, Type: cand.chType, LastActivity: ts},
				ok:  true,
			}
		}(i, cand)
	}

	// Collect results with progress
	var active []Channel
	for range candidates {
		r := <-results
		checked++
		if progress != nil && checked%5 == 0 {
			progress(ListProgress{Phase: "checking", Done: checked, Total: total})
		}
		if r.ok {
			active = append(active, r.ch)
		}
	}

	// Sort: channels/groups first, then mpim, then im — alphabetical within each
	typeOrder := map[string]int{"channel": 0, "group": 0, "mpim": 1, "im": 2}
	sort.Slice(active, func(i, j int) bool {
		oi, oj := typeOrder[active[i].Type], typeOrder[active[j].Type]
		if oi != oj {
			return oi < oj
		}
		return active[i].Name < active[j].Name
	})

	return active, nil
}

// resolveMemberNames converts mpim member IDs to "@name, @name, ..." format,
// excluding the authenticated user.
func (c *Client) resolveMemberNames(members []string) string {
	var names []string
	for _, uid := range members {
		if uid == c.ownUserID {
			continue
		}
		names = append(names, "@"+c.resolveUser(uid))
	}
	if len(names) == 0 {
		return "(empty group)"
	}
	return strings.Join(names, ", ")
}

// LiveThinking manages a live-updating thinking indicator in Slack.
type LiveThinking struct {
	client     *Client
	ts         string // timestamp of the thinking message
	lastUpdate time.Time
	mu         sync.Mutex
}

// NewLiveThinking creates a LiveThinking tied to the given client.
func (c *Client) NewLiveThinking() *LiveThinking {
	return &LiveThinking{client: c}
}

// Start posts the initial thinking indicator message.
func (lt *LiveThinking) Start() {
	lt.mu.Lock()
	defer lt.mu.Unlock()

	if lt.client.threadTS == "" {
		return
	}

	ctx := slackapi.NewContextBlock("",
		slackapi.NewTextBlockObject("mrkdwn", "🤔 _thinking..._", false, false),
	)
	_, ts, err := lt.client.api.PostMessage(
		lt.client.channel,
		slackapi.MsgOptionBlocks(ctx),
		slackapi.MsgOptionText("thinking...", false),
		slackapi.MsgOptionTS(lt.client.threadTS),
	)
	if err != nil {
		return
	}
	lt.ts = ts
	lt.lastUpdate = time.Now()
}

// Update updates the thinking message with accumulated text, throttled to 1/sec.
func (lt *LiveThinking) Update(text string) {
	lt.mu.Lock()
	defer lt.mu.Unlock()

	if lt.ts == "" {
		return
	}

	// Throttle updates to at most once per second
	if time.Since(lt.lastUpdate) < time.Second {
		return
	}

	// Truncate to last ~2000 chars
	display := text
	if len(display) > 2000 {
		display = "…" + display[len(display)-1999:]
	}

	ctx := slackapi.NewContextBlock("",
		slackapi.NewTextBlockObject("mrkdwn", fmt.Sprintf("🤔 _thinking..._\n```%s```", display), false, false),
	)
	lt.client.api.UpdateMessage(
		lt.client.channel,
		lt.ts,
		slackapi.MsgOptionBlocks(ctx),
		slackapi.MsgOptionText("thinking...", false),
	)
	lt.lastUpdate = time.Now()
}

// Done deletes the thinking message.
func (lt *LiveThinking) Done() {
	lt.mu.Lock()
	defer lt.mu.Unlock()

	if lt.ts == "" {
		return
	}
	lt.client.api.DeleteMessage(lt.client.channel, lt.ts)
	lt.ts = ""
}
