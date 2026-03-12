package access

import (
	"testing"
)

func TestNewController(t *testing.T) {
	c := New("U001")
	if c.OwnerID() != "U001" {
		t.Errorf("OwnerID() = %q, want U001", c.OwnerID())
	}
	// Default: locked to owner
	if c.IsAuthorized("U001") != true {
		t.Error("owner should be authorized")
	}
	if c.IsAuthorized("U002") != false {
		t.Error("non-owner should not be authorized in locked mode")
	}
}

func TestNoOwner(t *testing.T) {
	c := New("")
	// No owner means everyone is authorized
	if !c.IsAuthorized("U001") {
		t.Error("anyone should be authorized with no owner")
	}
	if !c.IsAuthorized("U002") {
		t.Error("anyone should be authorized with no owner")
	}
}

func TestSetOpen(t *testing.T) {
	c := New("U001")
	c.SetOpen()
	if !c.IsAuthorized("U002") {
		t.Error("non-owner should be authorized in open mode")
	}
	if c.Observe() {
		t.Error("SetOpen should disable observe")
	}
}

func TestSetClosed(t *testing.T) {
	c := New("U001")
	c.SetOpen()
	c.SetClosed()
	if c.IsAuthorized("U002") {
		t.Error("non-owner should not be authorized after SetClosed")
	}
	if c.IsAuthorized("U001") != true {
		t.Error("owner should still be authorized after SetClosed")
	}
}

func TestAllowUser(t *testing.T) {
	c := New("U001")
	c.AllowUser("U002")
	if !c.IsAuthorized("U002") {
		t.Error("allowed user should be authorized")
	}
	if c.IsAuthorized("U003") {
		t.Error("non-allowed user should not be authorized")
	}
}

func TestBanUser(t *testing.T) {
	c := New("U001")
	c.SetOpen()
	c.BanUser("U002")
	if c.IsAuthorized("U002") {
		t.Error("banned user should not be authorized")
	}
	if !c.IsAuthorized("U003") {
		t.Error("non-banned user should be authorized in open mode")
	}
}

func TestBanOwnerNoEffect(t *testing.T) {
	c := New("U001")
	c.BanUser("U001")
	if !c.IsAuthorized("U001") {
		t.Error("owner cannot be banned")
	}
}

func TestAllowRemovesBan(t *testing.T) {
	c := New("U001")
	c.BanUser("U002")
	c.AllowUser("U002")
	if !c.IsAuthorized("U002") {
		t.Error("AllowUser should remove ban")
	}
}

func TestBanRemovesAllow(t *testing.T) {
	c := New("U001")
	c.AllowUser("U002")
	c.BanUser("U002")
	if c.IsAuthorized("U002") {
		t.Error("BanUser should remove allow")
	}
}

func TestObserveMode(t *testing.T) {
	c := New("U001")
	c.SetObserve(true)

	// Non-owner: not authorized but visible
	if c.IsAuthorized("U002") {
		t.Error("non-owner should not be authorized in observe mode")
	}
	if !c.IsVisible("U002") {
		t.Error("everyone should be visible in observe mode")
	}

	// Owner: authorized and visible
	if !c.IsAuthorized("U001") {
		t.Error("owner should be authorized in observe mode")
	}
}

func TestIsVisibleLocked(t *testing.T) {
	c := New("U001")
	// Locked: only owner visible
	if c.IsVisible("U002") {
		t.Error("non-owner should not be visible in locked mode")
	}
	if !c.IsVisible("U001") {
		t.Error("owner should be visible in locked mode")
	}
}

func TestIsVisibleOpen(t *testing.T) {
	c := New("U001")
	c.SetOpen()
	if !c.IsVisible("U002") {
		t.Error("everyone should be visible in open mode")
	}
}

func TestAccessMode(t *testing.T) {
	c := New("U001")
	if c.AccessMode() != "locked" {
		t.Errorf("AccessMode() = %q, want locked", c.AccessMode())
	}

	c.SetOpen()
	if c.AccessMode() != "open" {
		t.Errorf("AccessMode() = %q, want open", c.AccessMode())
	}

	c.SetClosed()
	c.AllowUser("U002")
	if c.AccessMode() != "restricted (1 users)" {
		t.Errorf("AccessMode() = %q, want restricted", c.AccessMode())
	}

	c.SetClosed()
	c.SetObserve(true)
	if c.AccessMode() != "observe+locked" {
		t.Errorf("AccessMode() = %q, want observe+locked", c.AccessMode())
	}
}

func TestStateRoundtrip(t *testing.T) {
	c := New("U001")
	c.AllowUser("U002")
	c.AllowUser("U003")
	c.BanUser("U004")
	c.SetObserve(true)

	s := c.State()
	if s.OwnerID != "U001" {
		t.Errorf("State().OwnerID = %q", s.OwnerID)
	}
	if len(s.AllowedUsers) != 2 {
		t.Errorf("State().AllowedUsers = %v", s.AllowedUsers)
	}
	if len(s.BannedUsers) != 1 || s.BannedUsers[0] != "U004" {
		t.Errorf("State().BannedUsers = %v", s.BannedUsers)
	}

	// Apply to fresh controller
	c2 := New("U001")
	c2.Apply(s)
	if !c2.IsAuthorized("U002") {
		t.Error("U002 should be authorized after Apply")
	}
	if c2.IsAuthorized("U004") {
		t.Error("U004 should be banned after Apply")
	}
	if !c2.Observe() {
		t.Error("observe should be set after Apply")
	}
}

func TestHandleCommandOpen(t *testing.T) {
	c := New("U001")
	handled, feedback := c.HandleCommand("U001", "/open")
	if !handled {
		t.Error("/open should be handled")
	}
	if feedback != "🔓 Thread opened for everyone." {
		t.Errorf("feedback = %q", feedback)
	}
	if !c.OpenAccess() {
		t.Error("should be open after /open")
	}
}

func TestHandleCommandOpenUsers(t *testing.T) {
	c := New("U001")
	handled, _ := c.HandleCommand("U001", "/open <@U002> <@U003>")
	if !handled {
		t.Error("/open with users should be handled")
	}
	if c.OpenAccess() {
		t.Error("should not be fully open after /open with users")
	}
	if !c.IsAuthorized("U002") {
		t.Error("U002 should be authorized")
	}
	if !c.IsAuthorized("U003") {
		t.Error("U003 should be authorized")
	}
}

func TestHandleCommandLock(t *testing.T) {
	c := New("U001")
	c.SetOpen()
	handled, _ := c.HandleCommand("U001", "/lock")
	if !handled {
		t.Error("/lock should be handled")
	}
	if c.IsAuthorized("U002") {
		t.Error("should be locked after /lock")
	}
}

func TestHandleCommandLockBan(t *testing.T) {
	c := New("U001")
	c.SetOpen()
	handled, _ := c.HandleCommand("U001", "/lock <@U002>")
	if !handled {
		t.Error("/lock with users should be handled")
	}
	if c.IsAuthorized("U002") {
		t.Error("U002 should be banned")
	}
	// Open mode should still be active
	if !c.IsAuthorized("U003") {
		t.Error("U003 should still be authorized (open mode)")
	}
}

func TestHandleCommandObserve(t *testing.T) {
	c := New("U001")
	handled, _ := c.HandleCommand("U001", "/observe")
	if !handled {
		t.Error("/observe should be handled")
	}
	if !c.Observe() {
		t.Error("observe should be on")
	}

	// Toggle off
	c.HandleCommand("U001", "/observe")
	if c.Observe() {
		t.Error("observe should be off after toggle")
	}
}

func TestHandleCommandNonOwner(t *testing.T) {
	c := New("U001")
	handled, feedback := c.HandleCommand("U002", "/open")
	if !handled {
		t.Error("should still be handled (to give error)")
	}
	if feedback != "🚫 Only the thread owner can use access commands." {
		t.Errorf("feedback = %q", feedback)
	}
}

func TestHandleCommandUnknown(t *testing.T) {
	c := New("U001")
	handled, _ := c.HandleCommand("U001", "/unknown")
	if handled {
		t.Error("unknown command should not be handled")
	}
}

func TestParseMention(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"<@U123>", "U123"},
		{"<@U12345|sttts>", "U12345"},
		{"not a mention", ""},
		{"<@>", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := ParseMention(tt.input); got != tt.want {
			t.Errorf("ParseMention(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
