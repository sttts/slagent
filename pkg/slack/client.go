// Package slack handles Slack thread mirroring for pairplan.
package slack

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	slackapi "github.com/slack-go/slack"
)

// Client wraps the Slack API for pairplan's needs.
type Client struct {
	api        *slackapi.Client
	httpClient *http.Client
	token      string
	cookie     string
	channel    string
	threadTS   string // parent message timestamp (thread identifier)
	lastTS     string // timestamp of last seen reply
	userCache  map[string]string
	postedTS   map[string]bool // timestamps of messages we posted via API
	mu         sync.Mutex

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
	httpClient := &http.Client{}
	var opts []slackapi.Option
	if creds.Cookie != "" {
		opts = append(opts, slackapi.OptionHTTPClient(
			&cookieHTTPClient{inner: httpClient, cookie: creds.Cookie},
		))
	}

	c := &Client{
		api:        slackapi.New(token, opts...),
		httpClient: httpClient,
		token:      token,
		cookie:     creds.Cookie,
		channel:    channel,
		userCache:  make(map[string]string),
		postedTS:   make(map[string]bool),
		tokenType:  tokenType,
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
	title := "🧵 Planning session"
	if topic != "" {
		title = fmt.Sprintf("🧵 Plan: %s", topic)
	}
	headerText := slackapi.NewTextBlockObject("plain_text", title, true, false)
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

// PostClaudeMessage posts Claude's response as formatted Slack mrkdwn.
func (c *Client) PostClaudeMessage(text string) error {
	if c.threadTS == "" {
		return fmt.Errorf("no active thread")
	}

	mrkdwn := markdownToMrkdwn(text)

	// Split into chunks that fit in Section blocks
	chunks := splitAtLines(mrkdwn, maxBlockTextLen)
	for _, chunk := range chunks {
		section := slackapi.NewSectionBlock(
			slackapi.NewTextBlockObject("mrkdwn", chunk, false, false),
			nil, nil,
		)
		_, ts, err := c.api.PostMessage(
			c.channel,
			slackapi.MsgOptionBlocks(section),
			slackapi.MsgOptionText(chunk, false),
			slackapi.MsgOptionTS(c.threadTS),
		)
		if err != nil {
			return err
		}
		c.trackPosted(ts)
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

var (
	reHeading    = regexp.MustCompile(`(?m)^(#{1,6})\s+(.+)$`)
	reBold       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalic     = regexp.MustCompile(`(?:^|[^*])_(.+?)_`)
	reLink       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reListDash   = regexp.MustCompile(`(?m)^(\s*)[-*]\s+`)
	reStrikethrough = regexp.MustCompile(`~~(.+?)~~`)
)

// markdownToMrkdwn converts Markdown to Slack mrkdwn format.
func markdownToMrkdwn(text string) string {
	// Headings: # Foo → *Foo*
	text = reHeading.ReplaceAllString(text, "*$2*")

	// Bold: **foo** → *foo*
	text = reBold.ReplaceAllString(text, "*$1*")

	// Links: [text](url) → <url|text>
	text = reLink.ReplaceAllString(text, "<$2|$1>")

	// Unordered lists: - item → • item
	text = reListDash.ReplaceAllString(text, "${1}• ")

	// Strikethrough: ~~foo~~ → ~foo~
	text = reStrikethrough.ReplaceAllString(text, "~$1~")

	return text
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
	_, ts, err := c.api.PostMessage(
		c.channel,
		slackapi.MsgOptionBlocks(ctx, section),
		slackapi.MsgOptionText(fmt.Sprintf("@%s: %s", user, text), false),
		slackapi.MsgOptionTS(c.threadTS),
	)
	if err == nil {
		c.trackPosted(ts)
	}
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
	_, ts, err := c.api.PostMessage(
		c.channel,
		slackapi.MsgOptionBlocks(section, divider),
		slackapi.MsgOptionText("Planning session ended.", false),
		slackapi.MsgOptionTS(c.threadTS),
	)
	if err == nil {
		c.trackPosted(ts)
	}
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

		// Skip messages we posted via the API
		c.mu.Lock()
		ours := c.postedTS[msg.Timestamp]
		c.mu.Unlock()
		if ours {
			continue
		}

		// Skip bot messages (from other bots)
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

// ResumeThread resumes polling an existing thread.
func (c *Client) ResumeThread(threadTS string) {
	c.mu.Lock()
	c.threadTS = threadTS
	c.lastTS = threadTS
	c.mu.Unlock()
}

// trackPosted records a message timestamp as posted by us.
func (c *Client) trackPosted(ts string) {
	c.mu.Lock()
	c.postedTS[ts] = true
	c.mu.Unlock()
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

// ResolveChannelByName looks up a channel by name and returns its ID.
// The input can be "#channel-name" or just "channel-name".
func (c *Client) ResolveChannelByName(name string) (string, error) {
	name = strings.TrimPrefix(name, "#")
	params := &slackapi.GetConversationsForUserParameters{
		Types: []string{"public_channel", "private_channel", "mpim"},
		Limit: 200,
	}
	for {
		convs, cursor, err := c.api.GetConversationsForUser(params)
		if err != nil {
			return "", fmt.Errorf("list channels: %w", err)
		}
		for _, ch := range convs {
			if strings.EqualFold(ch.Name, name) {
				return ch.ID, nil
			}
		}
		if cursor == "" {
			break
		}
		params.Cursor = cursor
	}
	return "", fmt.Errorf("channel %q not found", name)
}

// cachedUser is a minimal user record for the on-disk cache.
type cachedUser struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	RealName    string `json:"real_name,omitempty"`
}

type usersCache struct {
	FetchedAt time.Time    `json:"fetched_at"`
	Users     []cachedUser `json:"users"`
}

const usersCacheTTL = 1 * time.Hour

func usersCachePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "pairplan", "users-cache.json")
}

// loadUsersCache returns cached users if the cache is fresh.
func loadUsersCache() ([]cachedUser, bool) {
	data, err := os.ReadFile(usersCachePath())
	if err != nil {
		return nil, false
	}
	var cache usersCache
	if json.Unmarshal(data, &cache) != nil {
		return nil, false
	}
	if time.Since(cache.FetchedAt) > usersCacheTTL {
		return nil, false
	}
	return cache.Users, true
}

// saveUsersCache writes the users cache to disk.
func saveUsersCache(users []cachedUser) {
	cache := usersCache{FetchedAt: time.Now(), Users: users}
	data, err := json.Marshal(cache)
	if err != nil {
		return
	}
	os.WriteFile(usersCachePath(), data, 0o600)
}

// ResolveUserChannel looks up one or more users by name and opens a DM/group DM channel.
// Names can be "@username" or just "username".
// Uses search.modules API (single call per user), falls back to on-disk cache.
func (c *Client) ResolveUserChannel(names ...string) (string, error) {
	var userIDs []string
	for _, name := range names {
		name = strings.TrimPrefix(name, "@")
		userID, err := c.resolveOneUser(name)
		if err != nil {
			return "", fmt.Errorf("user %q not found", name)
		}
		userIDs = append(userIDs, userID)
	}

	// Open DM (1 user) or group DM (multiple users)
	ch, _, _, err := c.api.OpenConversation(&slackapi.OpenConversationParameters{
		Users: userIDs,
	})
	if err != nil {
		return "", fmt.Errorf("open conversation: %w", err)
	}
	return ch.ID, nil
}

// resolveOneUser resolves a single username to a user ID.
func (c *Client) resolveOneUser(name string) (string, error) {
	// Try search.modules first (single API call, instant)
	if userID, err := c.searchUser(name); err == nil {
		return userID, nil
	}

	// Fallback: check on-disk cache
	if users, ok := loadUsersCache(); ok {
		for _, u := range users {
			if strings.EqualFold(u.Name, name) ||
				strings.EqualFold(u.DisplayName, name) ||
				strings.EqualFold(u.RealName, name) {
				return u.ID, nil
			}
		}
	}

	return "", fmt.Errorf("not found")
}

// searchUser calls the undocumented search.modules API with module=people
// to find a user by name in a single API call.
func (c *Client) searchUser(query string) (string, error) {
	body := fmt.Sprintf("query=%s&count=5&module=people", query)
	req, err := http.NewRequest("POST", "https://slack.com/api/search.modules", strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if c.cookie != "" {
		req.Header.Set("Cookie", fmt.Sprintf("d=%s", c.cookie))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		OK    bool `json:"ok"`
		Items []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Profile  struct {
				DisplayName string `json:"display_name"`
				RealName    string `json:"real_name"`
			} `json:"profile"`
		} `json:"items"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if !result.OK {
		return "", fmt.Errorf("%s", result.Error)
	}

	// Prefer exact match
	for _, u := range result.Items {
		if strings.EqualFold(u.Username, query) ||
			strings.EqualFold(u.Profile.DisplayName, query) ||
			strings.EqualFold(u.Profile.RealName, query) {
			return u.ID, nil
		}
	}
	if len(result.Items) > 0 {
		return result.Items[0].ID, nil
	}
	return "", fmt.Errorf("no results")
}


// PollInterval is the recommended interval between PollReplies calls.
const PollInterval = 3 * time.Second

// Channel represents a Slack channel for listing.
type Channel struct {
	ID   string
	Name string
	Type string // "channel", "group", "mpim", "im"
}

// ListProgress receives progress updates during ListChannels.
type ListProgress struct {
	Phase string // "listing" or "checking"
	Done  int
	Total int // set during "checking" phase
}

// ListChannels returns channels the user is a member of.
// Channels/groups are always included. Group DMs (mpim) are filtered
// to those with activity in the last 30 days.
func (c *Client) ListChannels(progress func(ListProgress)) ([]Channel, error) {
	params := &slackapi.GetConversationsForUserParameters{
		Types:           []string{"public_channel", "private_channel", "mpim"},
		Limit:           200,
		ExcludeArchived: true,
	}

	// Phase 1: collect all conversations
	type candidate struct {
		id, name string
		members  []string
	}
	var result []Channel
	var dmsToCheck []candidate
	for {
		convs, cursor, err := c.api.GetConversationsForUser(params)
		if err != nil {
			return nil, fmt.Errorf("get conversations: %w", err)
		}
		for _, ch := range convs {
			if ch.IsMpIM {
				dmsToCheck = append(dmsToCheck, candidate{ch.ID, ch.Name, ch.Members})
			} else {
				chType := "channel"
				if ch.IsPrivate {
					chType = "group"
				}
				result = append(result, Channel{ID: ch.ID, Name: ch.Name, Type: chType})
			}
		}
		if progress != nil {
			progress(ListProgress{Phase: "listing", Done: len(result) + len(dmsToCheck)})
		}
		if cursor == "" {
			break
		}
		params.Cursor = cursor
	}

	// Phase 2: check DMs for 30-day activity (concurrent)
	if len(dmsToCheck) > 0 {
		cutoff := float64(time.Now().Add(-30 * 24 * time.Hour).Unix())
		type dmResult struct {
			ch Channel
			ok bool
		}
		results := make(chan dmResult, len(dmsToCheck))
		sem := make(chan struct{}, 50)

		for _, cand := range dmsToCheck {
			sem <- struct{}{}
			go func(cand candidate) {
				defer func() { <-sem }()
				hist, err := c.api.GetConversationHistory(&slackapi.GetConversationHistoryParameters{
					ChannelID: cand.id,
					Limit:     1,
				})
				if err != nil || hist == nil || len(hist.Messages) == 0 {
					results <- dmResult{}
					return
				}
				ts, _ := strconv.ParseFloat(hist.Messages[0].Timestamp, 64)
				if ts < cutoff {
					results <- dmResult{}
					return
				}
				name := c.resolveMemberNames(cand.members)
				results <- dmResult{
					ch: Channel{ID: cand.id, Name: name, Type: "mpim"},
					ok: true,
				}
			}(cand)
		}

		checked := 0
		for range dmsToCheck {
			r := <-results
			checked++
			if progress != nil && checked%5 == 0 {
				progress(ListProgress{Phase: "checking", Done: checked, Total: len(dmsToCheck)})
			}
			if r.ok {
				result = append(result, r.ch)
			}
		}
	}

	// Sort: channels/groups first, then mpim, then im — alphabetical within each
	typeOrder := map[string]int{"channel": 0, "group": 0, "mpim": 1, "im": 2}
	sort.Slice(result, func(i, j int) bool {
		oi, oj := typeOrder[result[i].Type], typeOrder[result[j].Type]
		if oi != oj {
			return oi < oj
		}
		return result[i].Name < result[j].Name
	})

	return result, nil
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

// LiveStatus manages live-updating status messages in Slack.
// Thinking and tools are separate messages, each updated in-place within its type.
type LiveStatus struct {
	client     *Client
	thinkTS    string   // timestamp of thinking message
	toolTS     string   // timestamp of tool activity message
	tools      []string // recent tool summaries
	lastUpdate time.Time
	mu         sync.Mutex
}

const maxToolHistory = 5

// NewLiveStatus creates a LiveStatus tied to the given client.
func (c *Client) NewLiveStatus() *LiveStatus {
	return &LiveStatus{client: c}
}

// StartThinking posts the initial thinking indicator.
func (ls *LiveStatus) StartThinking() {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	if ls.client.threadTS == "" {
		return
	}

	ctx := slackapi.NewContextBlock("",
		slackapi.NewTextBlockObject("mrkdwn", "💭 _thinking..._", false, false),
	)
	_, ts, err := ls.client.api.PostMessage(
		ls.client.channel,
		slackapi.MsgOptionBlocks(ctx),
		slackapi.MsgOptionText("thinking...", false),
		slackapi.MsgOptionTS(ls.client.threadTS),
	)
	if err != nil {
		return
	}
	ls.thinkTS = ts
	ls.client.trackPosted(ts)
	ls.lastUpdate = time.Now()
}

// UpdateThinking updates the thinking message with accumulated text, throttled to 1/sec.
func (ls *LiveStatus) UpdateThinking(text string) {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	if ls.thinkTS == "" || time.Since(ls.lastUpdate) < time.Second {
		return
	}

	// Show last ~500 chars, last 5 lines
	display := text
	if len(display) > 500 {
		display = "…" + display[len(display)-499:]
	}
	lines := strings.Split(display, "\n")
	if len(lines) > 5 {
		lines = append([]string{"…"}, lines[len(lines)-5:]...)
	}

	content := fmt.Sprintf("💭 _thinking..._\n```%s```", strings.Join(lines, "\n"))
	ctx := slackapi.NewContextBlock("",
		slackapi.NewTextBlockObject("mrkdwn", content, false, false),
	)
	ls.client.api.UpdateMessage(
		ls.client.channel,
		ls.thinkTS,
		slackapi.MsgOptionBlocks(ctx),
		slackapi.MsgOptionText("thinking...", false),
	)
	ls.lastUpdate = time.Now()
}

// UpdateTool adds a tool activity entry (separate message from thinking).
func (ls *LiveStatus) UpdateTool(summary string) {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	if ls.client.threadTS == "" {
		return
	}

	ls.tools = append(ls.tools, summary)
	if len(ls.tools) > maxToolHistory {
		ls.tools = ls.tools[len(ls.tools)-maxToolHistory:]
	}

	// Build display with scrolling history
	var lines []string
	for i, t := range ls.tools {
		if i == len(ls.tools)-1 {
			lines = append(lines, fmt.Sprintf("🔧 %s", t))
		} else {
			lines = append(lines, fmt.Sprintf("      %s", t))
		}
	}
	display := strings.Join(lines, "\n")

	ctx := slackapi.NewContextBlock("",
		slackapi.NewTextBlockObject("mrkdwn", display, false, false),
	)

	if ls.toolTS == "" {
		_, ts, err := ls.client.api.PostMessage(
			ls.client.channel,
			slackapi.MsgOptionBlocks(ctx),
			slackapi.MsgOptionText(display, false),
			slackapi.MsgOptionTS(ls.client.threadTS),
		)
		if err == nil {
			ls.toolTS = ts
			ls.client.trackPosted(ts)
		}
	} else {
		ls.client.api.UpdateMessage(
			ls.client.channel,
			ls.toolTS,
			slackapi.MsgOptionBlocks(ctx),
			slackapi.MsgOptionText(display, false),
		)
	}
}

// Done deletes both status messages.
func (ls *LiveStatus) Done() {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	if ls.thinkTS != "" {
		ls.client.api.DeleteMessage(ls.client.channel, ls.thinkTS)
		ls.thinkTS = ""
	}
	if ls.toolTS != "" {
		ls.client.api.DeleteMessage(ls.client.channel, ls.toolTS)
		ls.toolTS = ""
	}
	ls.tools = nil
}

// LiveResponse streams Claude's text response to Slack, updating in-place.
type LiveResponse struct {
	client     *Client
	ts         string // timestamp of the streaming message
	lastUpdate time.Time
	mu         sync.Mutex
}

// NewLiveResponse creates a LiveResponse tied to the given client.
func (c *Client) NewLiveResponse() *LiveResponse {
	return &LiveResponse{client: c}
}

// Update posts or updates the streaming response as plain text, throttled to 1/sec.
func (lr *LiveResponse) Update(text string) {
	lr.mu.Lock()
	defer lr.mu.Unlock()

	if lr.client.threadTS == "" {
		return
	}

	// Throttle updates to at most once per second
	if lr.ts != "" && time.Since(lr.lastUpdate) < time.Second {
		return
	}

	// Show last ~2800 chars as mrkdwn (no code blocks to avoid re-collapse on update)
	display := text
	if len(display) > 2800 {
		display = "…" + display[len(display)-2799:]
	}
	display = markdownToMrkdwn(display)

	section := slackapi.NewSectionBlock(
		slackapi.NewTextBlockObject("mrkdwn", display, false, false),
		nil, nil,
	)

	if lr.ts == "" {
		_, ts, err := lr.client.api.PostMessage(
			lr.client.channel,
			slackapi.MsgOptionBlocks(section),
			slackapi.MsgOptionText(display, false),
			slackapi.MsgOptionTS(lr.client.threadTS),
		)
		if err == nil {
			lr.ts = ts
			lr.client.trackPosted(ts)
		}
	} else {
		lr.client.api.UpdateMessage(
			lr.client.channel,
			lr.ts,
			slackapi.MsgOptionBlocks(section),
			slackapi.MsgOptionText(display, false),
		)
	}
	lr.lastUpdate = time.Now()
}

// Finish replaces the streaming message with the final complete response.
// Deletes the live message and posts the full text with proper auto-split.
func (lr *LiveResponse) Finish(text string) {
	lr.mu.Lock()
	ts := lr.ts
	lr.ts = ""
	lr.mu.Unlock()

	if ts != "" {
		lr.client.api.DeleteMessage(lr.client.channel, ts)
	}
	if text != "" {
		lr.client.PostClaudeMessage(text)
	}
}
