// Package channel provides Slack channel and user resolution.
package channel

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

	"github.com/sttts/slagent/credential"
)

// Client wraps the Slack API for credential-based channel and user resolution.
type Client struct {
	api        *slackapi.Client
	httpClient *http.Client
	token      string
	cookie     string
	userCache  map[string]string
	mu         sync.Mutex

	// Token type and identity
	tokenType string // "bot", "user", or "session"
	ownUserID string // set via auth.test for user/session tokens
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

// New creates a Slack client from stored credentials for channel/user resolution.
func New() (*Client, error) {
	creds, err := credential.Load()
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
		userCache:  make(map[string]string),
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

// ResolveUserChannel looks up one or more users by name and opens a DM/group DM channel.
// Names can be "@username" or just "username".
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
	return filepath.Join(home, ".config", "slagent", "users-cache.json")
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
