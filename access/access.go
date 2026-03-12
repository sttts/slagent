// Package access provides a pure access-control state machine for thread ownership.
// It has no Slack dependency and is independently testable.
package access

import (
	"fmt"
	"sort"
	"sync"
)

// State is a snapshot of access control configuration for title encoding/decoding.
type State struct {
	OwnerID      string
	OpenAccess   bool
	Observe      bool
	AllowedUsers []string
	BannedUsers  []string
}

// Controller manages thread access control state.
type Controller struct {
	mu           sync.Mutex
	ownerID      string
	openAccess   bool
	observe      bool
	allowedUsers map[string]bool
	bannedUsers  map[string]bool
}

// New creates a new access controller with the given owner.
func New(ownerID string) *Controller {
	return &Controller{
		ownerID:      ownerID,
		allowedUsers: make(map[string]bool),
		bannedUsers:  make(map[string]bool),
	}
}

// OwnerID returns the configured owner user ID.
func (c *Controller) OwnerID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ownerID
}

// IsAuthorized checks whether a user is allowed to interact.
func (c *Controller) IsAuthorized(userID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Banned users are always blocked (except owner)
	if c.bannedUsers[userID] && userID != c.ownerID {
		return false
	}
	if c.openAccess {
		return true
	}
	if c.ownerID == "" {
		return true // no owner restriction
	}
	if userID == c.ownerID {
		return true
	}
	return c.allowedUsers[userID]
}

// IsVisible returns true if a message from userID should be delivered to the agent.
// In observe mode, all messages are visible even if the user is not authorized.
func (c *Controller) IsVisible(userID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.openAccess {
		return true
	}
	if c.observe {
		return true
	}

	// Inline isAuthorized logic to avoid double-locking
	if c.bannedUsers[userID] && userID != c.ownerID {
		return false
	}
	if c.ownerID == "" || userID == c.ownerID {
		return true
	}
	return c.allowedUsers[userID]
}

// SetOpen overrides the access state to open for all participants.
func (c *Controller) SetOpen() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.openAccess = true
	c.observe = false
	c.allowedUsers = make(map[string]bool)
}

// SetClosed resets the access state to locked (owner only), disables observe.
func (c *Controller) SetClosed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.openAccess = false
	c.observe = false
	c.allowedUsers = make(map[string]bool)
	c.bannedUsers = make(map[string]bool)
}

// SetObserve toggles the observe flag. When on, all messages are delivered
// for passive learning, but the agent only responds to authorized users.
func (c *Controller) SetObserve(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.observe = on
}

// Observe returns whether the observe flag is set.
func (c *Controller) Observe() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.observe
}

// AllowUser adds a user to the allowed list and removes from banned.
func (c *Controller) AllowUser(userID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.allowedUsers[userID] = true
	delete(c.bannedUsers, userID)
}

// BanUser adds a user to the banned list and removes from allowed.
func (c *Controller) BanUser(userID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bannedUsers[userID] = true
	delete(c.allowedUsers, userID)
}

// AccessMode returns a human-readable description of the current access state.
func (c *Controller) AccessMode() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	var base string
	if c.openAccess {
		base = "open"
	} else if len(c.allowedUsers) > 0 {
		base = fmt.Sprintf("restricted (%d users)", len(c.allowedUsers))
	} else {
		base = "locked"
	}
	if c.observe && !c.openAccess {
		return "observe+" + base
	}
	return base
}

// State returns a snapshot of the current access state for title encoding.
func (c *Controller) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()

	s := State{
		OwnerID:    c.ownerID,
		OpenAccess: c.openAccess,
		Observe:    c.observe,
	}
	for u := range c.allowedUsers {
		s.AllowedUsers = append(s.AllowedUsers, u)
	}
	for u := range c.bannedUsers {
		s.BannedUsers = append(s.BannedUsers, u)
	}
	sort.Strings(s.AllowedUsers)
	sort.Strings(s.BannedUsers)
	return s
}

// Apply restores access state from a snapshot (e.g. parsed from thread title).
func (c *Controller) Apply(s State) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.openAccess = s.OpenAccess
	c.observe = s.Observe
	c.allowedUsers = make(map[string]bool)
	c.bannedUsers = make(map[string]bool)
	for _, u := range s.AllowedUsers {
		c.allowedUsers[u] = true
	}
	for _, u := range s.BannedUsers {
		c.bannedUsers[u] = true
	}
}

// HandleCommand processes access control commands (/open, /lock, /close, /observe).
// Only the owner can run these. Returns (handled, feedback).
func (c *Controller) HandleCommand(userID, cmd string) (bool, string) {
	// Parse command parts
	parts := splitFields(cmd)
	if len(parts) == 0 {
		return false, ""
	}

	switch parts[0] {
	case "/open", "/lock", "/close", "/observe":
		// allow
	default:
		return false, ""
	}

	c.mu.Lock()
	if c.ownerID != "" && userID != c.ownerID {
		c.mu.Unlock()
		return true, "🚫 Only the thread owner can use access commands."
	}

	var feedback string
	switch parts[0] {
	case "/open":
		if len(parts) == 1 {
			c.openAccess = true
			c.observe = false
			c.allowedUsers = make(map[string]bool)
			feedback = "🔓 Thread opened for everyone."
		} else {
			c.openAccess = false
			var added []string
			for _, mention := range parts[1:] {
				if uid := ParseMention(mention); uid != "" {
					c.allowedUsers[uid] = true
					delete(c.bannedUsers, uid)
					added = append(added, mention)
				}
			}
			feedback = fmt.Sprintf("🔓 Access granted to %s.", joinStrings(added, " "))
		}
	case "/lock", "/close":
		if len(parts) == 1 {
			c.openAccess = false
			c.observe = false
			c.allowedUsers = make(map[string]bool)
			c.bannedUsers = make(map[string]bool)
			feedback = "🔒 Thread locked to owner only."
		} else {
			var banned []string
			for _, mention := range parts[1:] {
				if uid := ParseMention(mention); uid != "" {
					c.bannedUsers[uid] = true
					delete(c.allowedUsers, uid)
					banned = append(banned, mention)
				}
			}
			feedback = fmt.Sprintf("🔒 Banned %s.", joinStrings(banned, " "))
		}
	case "/observe":
		if c.observe {
			c.observe = false
			feedback = "👀 Observe mode off."
		} else {
			c.openAccess = false
			c.observe = true
			c.allowedUsers = make(map[string]bool)
			feedback = "👀 Observe mode on — reading all messages, responding only to owner."
		}
	}
	c.mu.Unlock()
	return true, feedback
}

// OpenAccess returns whether the thread is open for all.
func (c *Controller) OpenAccess() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.openAccess
}

// ParseMention extracts a user ID from a Slack mention ("<@U123>").
func ParseMention(s string) string {
	if len(s) < 4 || s[0] != '<' || s[1] != '@' || s[len(s)-1] != '>' {
		return ""
	}
	uid := s[2 : len(s)-1]

	// Strip display name suffix: <@U12345|sttts> → U12345
	for i := 0; i < len(uid); i++ {
		if uid[i] == '|' {
			return uid[:i]
		}
	}
	return uid
}

// splitFields splits a string on whitespace.
func splitFields(s string) []string {
	var fields []string
	start := -1
	for i, c := range s {
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if start >= 0 {
				fields = append(fields, s[start:i])
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		fields = append(fields, s[start:])
	}
	return fields
}

// joinStrings joins strings with a separator.
func joinStrings(ss []string, sep string) string {
	if len(ss) == 0 {
		return ""
	}
	result := ss[0]
	for _, s := range ss[1:] {
		result += sep + s
	}
	return result
}
