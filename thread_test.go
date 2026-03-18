package slagent

import (
	"context"
	"strings"
	"testing"
	"time"

	slackapi "github.com/slack-go/slack"

	"github.com/sttts/slagent/access"
)

// isStop returns true if msg is a StopMessage.
func isStop(msg Message) bool {
	_, ok := msg.(StopMessage)
	return ok
}

// isQuit returns true if msg is a QuitMessage.
func isQuit(msg Message) bool {
	_, ok := msg.(QuitMessage)
	return ok
}

func TestClassifyBlock(t *testing.T) {
	tests := []struct {
		blockID    string
		wantKind   BlockKind
		wantID     string
	}{
		{"slagent-abc123", BlockFinal, "abc123"},
		{"slagent-abc123~", BlockStreaming, "abc123"},
		{"slagent-abc123~act", BlockActivity, "abc123"},
		{"other-block", BlockNone, ""},
		{"", BlockNone, ""},
		{"slagent-", BlockFinal, ""},
	}
	for _, tt := range tests {
		kind, id := classifyBlock(tt.blockID)
		if kind != tt.wantKind || id != tt.wantID {
			t.Errorf("classifyBlock(%q) = (%d, %q), want (%d, %q)", tt.blockID, kind, id, tt.wantKind, tt.wantID)
		}
	}
}

func TestIsNativeToken(t *testing.T) {
	tests := []struct {
		token string
		want  bool
	}{
		{"xoxb-123-456", true},
		{"xoxc-123-456", false},
		{"xoxp-123-456", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isNativeToken(tt.token); got != tt.want {
			t.Errorf("isNativeToken(%q) = %v, want %v", tt.token, got, tt.want)
		}
	}
}

func TestThreadPermissions(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST", WithOwner("U_OWNER"))

	// Owner is authorized
	if !thread.IsAuthorized("U_OWNER") {
		t.Error("owner should be authorized")
	}

	// Other user is not
	if thread.IsAuthorized("U_OTHER") {
		t.Error("other user should not be authorized")
	}

	// /open command from owner
	if handled, _ := thread.handleCommand("U_OWNER", "/open"); !handled {
		t.Error("/open from owner should be handled")
	}
	if !thread.IsAuthorized("U_OTHER") {
		t.Error("other user should be authorized after /open")
	}

	// /close from owner
	if handled, _ := thread.handleCommand("U_OWNER", "/close"); !handled {
		t.Error("/close from owner should be handled")
	}
	if thread.IsAuthorized("U_OTHER") {
		t.Error("other user should not be authorized after /close")
	}

	// /open from non-owner returns handled=true but with denial feedback
	if handled, feedback := thread.handleCommand("U_OTHER", "/open"); !handled {
		t.Error("/open from non-owner should be handled (with denial)")
	} else if feedback == "" {
		t.Error("/open from non-owner should return feedback")
	}
}

func TestOpenForSpecificUsers(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST", WithOwner("U_OWNER"))

	// By default, only owner is authorized
	if thread.IsAuthorized("U_ALICE") {
		t.Error("alice should not be authorized initially")
	}

	// /open <@U_ALICE> — allow alice
	thread.handleCommand("U_OWNER", "/open <@U_ALICE>")
	if !thread.IsAuthorized("U_ALICE") {
		t.Error("alice should be authorized after /open")
	}
	if thread.IsAuthorized("U_BOB") {
		t.Error("bob should not be authorized")
	}

	// /open <@U_BOB> — also allow bob (additive)
	thread.handleCommand("U_OWNER", "/open <@U_BOB>")
	if !thread.IsAuthorized("U_ALICE") {
		t.Error("alice should still be authorized")
	}
	if !thread.IsAuthorized("U_BOB") {
		t.Error("bob should now be authorized")
	}

	// /lock — reset everything
	thread.handleCommand("U_OWNER", "/lock")
	if thread.IsAuthorized("U_ALICE") {
		t.Error("alice should not be authorized after /lock")
	}
	if thread.IsAuthorized("U_BOB") {
		t.Error("bob should not be authorized after /lock")
	}
}

func TestOpenMultipleUsersAtOnce(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST", WithOwner("U_OWNER"))

	// /open <@U_ALICE> <@U_BOB> — allow both at once
	thread.handleCommand("U_OWNER", "/open <@U_ALICE> <@U_BOB>")
	if !thread.IsAuthorized("U_ALICE") {
		t.Error("alice should be authorized")
	}
	if !thread.IsAuthorized("U_BOB") {
		t.Error("bob should be authorized")
	}
	if thread.IsAuthorized("U_CAROL") {
		t.Error("carol should not be authorized")
	}
}

func TestLockSpecificUser(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST", WithOwner("U_OWNER"))

	// Open for everyone first
	thread.handleCommand("U_OWNER", "/open")
	if !thread.IsAuthorized("U_ALICE") {
		t.Error("alice should be authorized when open")
	}

	// /lock <@U_ALICE> — ban alice specifically
	thread.handleCommand("U_OWNER", "/lock <@U_ALICE>")
	if thread.IsAuthorized("U_ALICE") {
		t.Error("alice should be banned")
	}
	if !thread.IsAuthorized("U_BOB") {
		t.Error("bob should still be authorized (thread is open)")
	}

	// Owner is never banned
	thread.handleCommand("U_OWNER", "/lock <@U_OWNER>")
	if !thread.IsAuthorized("U_OWNER") {
		t.Error("owner should never be banned")
	}
}

func TestLockMultipleUsers(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST", WithOwner("U_OWNER"))
	thread.handleCommand("U_OWNER", "/open")

	// /lock <@U_ALICE> <@U_BOB> — ban both
	thread.handleCommand("U_OWNER", "/lock <@U_ALICE> <@U_BOB>")
	if thread.IsAuthorized("U_ALICE") {
		t.Error("alice should be banned")
	}
	if thread.IsAuthorized("U_BOB") {
		t.Error("bob should be banned")
	}
	if !thread.IsAuthorized("U_CAROL") {
		t.Error("carol should still be authorized")
	}
}

func TestOpenUnbansBannedUser(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST", WithOwner("U_OWNER"))
	thread.handleCommand("U_OWNER", "/open")
	thread.handleCommand("U_OWNER", "/lock <@U_ALICE>")
	if thread.IsAuthorized("U_ALICE") {
		t.Error("alice should be banned")
	}

	// /open <@U_ALICE> — unban alice
	thread.handleCommand("U_OWNER", "/open <@U_ALICE>")
	if !thread.IsAuthorized("U_ALICE") {
		t.Error("alice should be unbanned after /open")
	}
}

func TestLockRemovesFromAllowed(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST", WithOwner("U_OWNER"))

	// Allow alice specifically
	thread.handleCommand("U_OWNER", "/open <@U_ALICE>")
	if !thread.IsAuthorized("U_ALICE") {
		t.Error("alice should be allowed")
	}

	// Ban alice — should remove from allowed too
	thread.handleCommand("U_OWNER", "/lock <@U_ALICE>")
	if thread.IsAuthorized("U_ALICE") {
		t.Error("alice should be banned and removed from allowed")
	}
}

func TestFormatTitle(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	tests := []struct {
		name  string
		setup func(*Thread)
		want  string
	}{
		{
			name:  "locked (default)",
			setup: func(th *Thread) {},
			want:  ":fox_face:🔒🧵 Test Topic",
		},
		{
			name: "open for all",
			setup: func(th *Thread) {
				th.handleCommand("U_OWNER", "/open")
			},
			want: ":fox_face:🧵 Test Topic",
		},
		{
			name: "open for specific user",
			setup: func(th *Thread) {
				th.handleCommand("U_OWNER", "/open <@U_ALICE>")
			},
			want: ":fox_face:🧵 <@U_ALICE> Test Topic",
		},
		{
			name: "open for multiple users",
			setup: func(th *Thread) {
				th.handleCommand("U_OWNER", "/open <@U_ALICE> <@U_BOB>")
			},
			want: ":fox_face:🧵 <@U_ALICE> <@U_BOB> Test Topic",
		},
		{
			name: "banned user",
			setup: func(th *Thread) {
				th.handleCommand("U_OWNER", "/open")
				th.handleCommand("U_OWNER", "/lock <@U_EVIL>")
			},
			want: ":fox_face:🧵 Test Topic (🔒 <@U_EVIL>)",
		},
		{
			name: "allowed and banned",
			setup: func(th *Thread) {
				th.handleCommand("U_OWNER", "/open <@U_ALICE>")
				th.handleCommand("U_OWNER", "/lock <@U_EVIL>")
			},
			want: ":fox_face:🧵 <@U_ALICE> Test Topic (🔒 <@U_EVIL>)",
		},
		{
			name: "with mode suffix",
			setup: func(th *Thread) {
				th.modeSuffix = " — 📋 planning"
			},
			want: ":fox_face:🔒🧵 Test Topic — 📋 planning",
		},
		{
			name: "mode suffix with bans",
			setup: func(th *Thread) {
				th.handleCommand("U_OWNER", "/open")
				th.handleCommand("U_OWNER", "/lock <@U_EVIL>")
				th.modeSuffix = " — 📋 planning"
			},
			want: ":fox_face:🧵 Test Topic — 📋 planning (🔒 <@U_EVIL>)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			th := NewThread(mock.client(), "C_TEST",
				WithOwner("U_OWNER"),
				WithInstanceID("fox_face"),
			)
			th.topic = "Test Topic"
			tt.setup(th)
			got := th.formatTitle()
			if got != tt.want {
				t.Errorf("formatTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseTitle(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	tests := []struct {
		name         string
		text         string
		wantTitle    string
		wantOpen     bool
		wantAllowed  []string
		wantBanned   []string
	}{
		{
			name:      "locked",
			text:      ":fox_face:🔒🧵 My Topic",
			wantTitle: "My Topic",
			wantOpen:  false,
		},
		{
			name:      "open",
			text:      ":fox_face:🧵 My Topic",
			wantTitle: "My Topic",
			wantOpen:  true,
		},
		{
			name:        "selective access",
			text:        ":fox_face:🧵 <@U_ALICE> <@U_BOB> My Topic",
			wantTitle:   "My Topic",
			wantOpen:    false,
			wantAllowed: []string{"U_ALICE", "U_BOB"},
		},
		{
			name:       "banned users",
			text:       ":fox_face:🧵 My Topic (🔒 <@U_EVIL>)",
			wantTitle:  "My Topic",
			wantOpen:   true,
			wantBanned: []string{"U_EVIL"},
		},
		{
			name:        "allowed and banned",
			text:        ":fox_face:🧵 <@U_ALICE> My Topic (🔒 <@U_EVIL>)",
			wantTitle:   "My Topic",
			wantOpen:    false,
			wantAllowed: []string{"U_ALICE"},
			wantBanned:  []string{"U_EVIL"},
		},
		{
			name:      "locked shortcodes",
			text:      ":fox_face::lock::thread: My Topic",
			wantTitle: "My Topic",
			wantOpen:  false,
		},
		{
			name:      "open shortcodes",
			text:      ":fox_face::thread: My Topic",
			wantTitle: "My Topic",
			wantOpen:  true,
		},
		{
			name:       "banned shortcodes",
			text:       ":fox_face::thread: My Topic (:lock: <@U_EVIL>)",
			wantTitle:  "My Topic",
			wantOpen:   true,
			wantBanned: []string{"U_EVIL"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			th := NewThread(mock.client(), "C_TEST",
				WithInstanceID("fox_face"),
			)
			th.parseTitle(tt.text)

			if th.topic != tt.wantTitle {
				t.Errorf("title = %q, want %q", th.topic, tt.wantTitle)
			}
			if th.OpenAccess() != tt.wantOpen {
				t.Errorf("openAccess = %v, want %v", th.OpenAccess(), tt.wantOpen)
			}
			st := th.Controller.State()
			allowedSet := make(map[string]bool)
			for _, u := range st.AllowedUsers {
				allowedSet[u] = true
			}
			bannedSet := make(map[string]bool)
			for _, u := range st.BannedUsers {
				bannedSet[u] = true
			}
			for _, uid := range tt.wantAllowed {
				if !allowedSet[uid] {
					t.Errorf("allowedUsers missing %s", uid)
				}
			}
			for _, uid := range tt.wantBanned {
				if !bannedSet[uid] {
					t.Errorf("bannedUsers missing %s", uid)
				}
			}
		})
	}
}

func TestParseTitleRoundtrip(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	// Build a thread with allowed + banned users, format title, parse it back
	th := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	th.topic = "Design API"
	th.handleCommand("U_OWNER", "/open <@U_ALICE> <@U_BOB>")
	th.handleCommand("U_OWNER", "/lock <@U_EVIL>")

	label := th.formatTitle()

	// Parse into a fresh thread
	th2 := NewThread(mock.client(), "C_TEST",
		WithInstanceID("fox_face"),
	)
	th2.parseTitle(label)

	if th2.topic != "Design API" {
		t.Errorf("roundtrip title = %q, want %q", th2.topic, "Design API")
	}
	if th2.OpenAccess() != false {
		t.Error("roundtrip openAccess should be false")
	}
	st2 := th2.Controller.State()
	allowedSet := make(map[string]bool)
	for _, u := range st2.AllowedUsers {
		allowedSet[u] = true
	}
	bannedSet := make(map[string]bool)
	for _, u := range st2.BannedUsers {
		bannedSet[u] = true
	}
	if !allowedSet["U_ALICE"] || !allowedSet["U_BOB"] {
		t.Errorf("roundtrip allowedUsers = %v, want alice+bob", st2.AllowedUsers)
	}
	if !bannedSet["U_EVIL"] {
		t.Errorf("roundtrip bannedUsers = %v, want evil", st2.BannedUsers)
	}
}

func TestParseTitleStripsModeSuffix(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	tests := []struct {
		name      string
		title     string
		wantTopic string
	}{
		{
			name:      "no mode suffix",
			title:     ":fox_face:🔒🧵 Build CLI",
			wantTopic: "Build CLI",
		},
		{
			name:      "with planning suffix",
			title:     ":fox_face:🔒🧵 Build CLI — 📋 planning",
			wantTopic: "Build CLI",
		},
		{
			name:      "open with planning suffix",
			title:     ":fox_face:🧵 Build CLI — 📋 planning",
			wantTopic: "Build CLI",
		},
		{
			name:      "with bans and planning suffix",
			title:     ":fox_face:🧵 Build CLI — 📋 planning (🔒 <@U_EVIL>)",
			wantTopic: "Build CLI",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			th := NewThread(mock.client(), "C_TEST", WithInstanceID("fox_face"))
			th.parseTitle(tt.title)
			if th.topic != tt.wantTopic {
				t.Errorf("topic = %q, want %q", th.topic, tt.wantTopic)
			}
		})
	}
}

func TestModeSuffixRoundtrip(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	// Format with mode suffix, parse back — topic should be clean
	th := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	th.topic = "Refactor session"
	th.modeSuffix = " — 📋 planning"

	label := th.formatTitle()
	if !strings.Contains(label, " — 📋 planning") {
		t.Errorf("formatted title should contain mode suffix, got %q", label)
	}

	// Parse into fresh thread
	th2 := NewThread(mock.client(), "C_TEST", WithInstanceID("fox_face"))
	th2.parseTitle(label)

	if th2.topic != "Refactor session" {
		t.Errorf("roundtrip topic = %q, want %q", th2.topic, "Refactor session")
	}
}

func TestModeSuffixWithBansRoundtrip(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	th := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	th.topic = "Design API"
	th.modeSuffix = " — 📋 planning"
	th.handleCommand("U_OWNER", "/open")
	th.handleCommand("U_OWNER", "/lock <@U_EVIL>")

	label := th.formatTitle()

	th2 := NewThread(mock.client(), "C_TEST", WithInstanceID("fox_face"))
	th2.parseTitle(label)

	if th2.topic != "Design API" {
		t.Errorf("topic = %q, want %q", th2.topic, "Design API")
	}
	st2 := th2.Controller.State()
	bannedSet := make(map[string]bool)
	for _, u := range st2.BannedUsers {
		bannedSet[u] = true
	}
	if !bannedSet["U_EVIL"] {
		t.Errorf("bannedUsers = %v, want U_EVIL", st2.BannedUsers)
	}
}

func TestObserveFormatTitle(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	tests := []struct {
		name  string
		setup func(*Thread)
		want  string
	}{
		{
			name: "observe closed (no allowed users)",
			setup: func(th *Thread) {
				th.handleCommand("U_OWNER", "/observe")
			},
			want: ":fox_face:👀🧵 Test Topic",
		},
		{
			name: "observe with allowed users",
			setup: func(th *Thread) {
				th.handleCommand("U_OWNER", "/open <@U_ALICE>")
				th.SetObserve(true)
			},
			want: ":fox_face:👀🧵 <@U_ALICE> Test Topic",
		},
		{
			name: "observe off after toggle",
			setup: func(th *Thread) {
				th.handleCommand("U_OWNER", "/observe") // on
				th.handleCommand("U_OWNER", "/observe") // off
			},
			want: ":fox_face:🔒🧵 Test Topic",
		},
		{
			name: "open mode ignores observe",
			setup: func(th *Thread) {
				th.handleCommand("U_OWNER", "/open")
			},
			want: ":fox_face:🧵 Test Topic",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			th := NewThread(mock.client(), "C_TEST",
				WithOwner("U_OWNER"),
				WithInstanceID("fox_face"),
			)
			th.topic = "Test Topic"
			tt.setup(th)
			got := th.formatTitle()
			if got != tt.want {
				t.Errorf("formatTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestObserveParseTitle(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	tests := []struct {
		name        string
		text        string
		wantObserve bool
		wantOpen    bool
		wantTopic   string
		wantAllowed []string
	}{
		{
			name:        "observe closed",
			text:        ":fox_face:👀🧵 My Topic",
			wantObserve: true,
			wantOpen:    false,
			wantTopic:   "My Topic",
		},
		{
			name:        "observe with allowed users",
			text:        ":fox_face:👀🧵 <@U_ALICE> My Topic",
			wantObserve: true,
			wantOpen:    false,
			wantTopic:   "My Topic",
			wantAllowed: []string{"U_ALICE"},
		},
		{
			name:        "observe shortcodes",
			text:        ":fox_face::eyes::thread: My Topic",
			wantObserve: true,
			wantOpen:    false,
			wantTopic:   "My Topic",
		},
		{
			name:        "locked (no observe)",
			text:        ":fox_face:🔒🧵 My Topic",
			wantObserve: false,
			wantOpen:    false,
			wantTopic:   "My Topic",
		},
		{
			name:        "open (no observe)",
			text:        ":fox_face:🧵 My Topic",
			wantObserve: false,
			wantOpen:    true,
			wantTopic:   "My Topic",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			th := NewThread(mock.client(), "C_TEST", WithInstanceID("fox_face"))
			th.parseTitle(tt.text)
			if th.Observe() != tt.wantObserve {
				t.Errorf("observe = %v, want %v", th.Observe(), tt.wantObserve)
			}
			if th.OpenAccess() != tt.wantOpen {
				t.Errorf("openAccess = %v, want %v", th.OpenAccess(), tt.wantOpen)
			}
			if th.topic != tt.wantTopic {
				t.Errorf("topic = %q, want %q", th.topic, tt.wantTopic)
			}
			st := th.Controller.State()
			allowedSet := make(map[string]bool)
			for _, u := range st.AllowedUsers {
				allowedSet[u] = true
			}
			for _, uid := range tt.wantAllowed {
				if !allowedSet[uid] {
					t.Errorf("allowedUsers missing %s", uid)
				}
			}
		})
	}
}

func TestObserveRoundtrip(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	// Observe + closed
	th := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	th.topic = "Watch this"
	th.handleCommand("U_OWNER", "/observe")

	label := th.formatTitle()

	th2 := NewThread(mock.client(), "C_TEST", WithInstanceID("fox_face"))
	th2.parseTitle(label)

	if th2.topic != "Watch this" {
		t.Errorf("roundtrip topic = %q, want %q", th2.topic, "Watch this")
	}
	if !th2.Observe() {
		t.Error("roundtrip observe should be true")
	}
	if th2.OpenAccess() {
		t.Error("roundtrip openAccess should be false")
	}
}

func TestObserveIsVisible(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	th := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithObserve(),
	)

	// Owner is both authorized and visible
	if !th.IsAuthorized("U_OWNER") {
		t.Error("owner should be authorized")
	}
	if !th.IsVisible("U_OWNER") {
		t.Error("owner should be visible")
	}

	// Other user is not authorized but IS visible in observe mode
	if th.IsAuthorized("U_OTHER") {
		t.Error("other user should not be authorized in observe mode")
	}
	if !th.IsVisible("U_OTHER") {
		t.Error("other user should be visible in observe mode")
	}

	// Turn off observe
	th.SetObserve(false)
	if th.IsVisible("U_OTHER") {
		t.Error("other user should not be visible when observe is off")
	}
}

func TestObserveAccessMode(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	th := NewThread(mock.client(), "C_TEST", WithOwner("U_OWNER"))

	if th.AccessMode() != "locked" {
		t.Errorf("default should be locked, got %q", th.AccessMode())
	}

	th.SetObserve(true)
	if th.AccessMode() != "observe+locked" {
		t.Errorf("observe+locked should be observe+locked, got %q", th.AccessMode())
	}

	th.SetOpen()
	if th.AccessMode() != "open" {
		t.Errorf("open should be open, got %q", th.AccessMode())
	}
}

func TestObserveCommand(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	th := NewThread(mock.client(), "C_TEST", WithOwner("U_OWNER"))

	// /observe toggles on
	handled, feedback := th.handleCommand("U_OWNER", "/observe")
	if !handled {
		t.Error("/observe should be handled")
	}
	if !strings.Contains(feedback, "on") {
		t.Errorf("expected 'on' in feedback, got %q", feedback)
	}
	if !th.Observe() {
		t.Error("observe should be true")
	}
	if th.OpenAccess() {
		t.Error("openAccess should be false after /observe")
	}

	// /observe toggles off
	handled, feedback = th.handleCommand("U_OWNER", "/observe")
	if !handled {
		t.Error("/observe should be handled")
	}
	if !strings.Contains(feedback, "off") {
		t.Errorf("expected 'off' in feedback, got %q", feedback)
	}
	if th.Observe() {
		t.Error("observe should be false after second /observe")
	}

	// Non-owner can't use /observe
	th.handleCommand("U_OWNER", "/observe") // turn on
	_, feedback = th.handleCommand("U_OTHER", "/observe")
	if !strings.Contains(feedback, "🚫") {
		t.Errorf("non-owner should get denied, got %q", feedback)
	}
}

func TestLockDisablesObserve(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	th := NewThread(mock.client(), "C_TEST", WithOwner("U_OWNER"))
	th.handleCommand("U_OWNER", "/observe")
	if !th.Observe() {
		t.Error("observe should be on")
	}

	// /lock disables observe
	th.handleCommand("U_OWNER", "/lock")
	if th.Observe() {
		t.Error("observe should be off after /lock")
	}
}

func TestOpenDisablesObserve(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	th := NewThread(mock.client(), "C_TEST", WithOwner("U_OWNER"))
	th.handleCommand("U_OWNER", "/observe")
	if !th.Observe() {
		t.Error("observe should be on")
	}

	// /open disables observe
	th.handleCommand("U_OWNER", "/open")
	if th.Observe() {
		t.Error("observe should be off after /open")
	}
}

func TestThreadTitleUpdatedOnAccessChange(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	thread.Start("My Task")

	// Default is locked — title should contain 🔒
	parentMsg := mock.postedMessages()[0]
	if !strings.Contains(parentMsg.Text, "🔒") {
		t.Errorf("initial title should contain 🔒, got %q", parentMsg.Text)
	}

	// /open — title should no longer contain 🔒
	thread.handleCommand("U_OWNER", "/open")
	msgs := mock.postedMessages()
	var updated string
	for _, m := range msgs {
		if m.TS == parentMsg.TS && m.IsUpdate {
			updated = m.Text
		}
	}
	if strings.Contains(updated, "🔒") {
		t.Errorf("title after /open should not contain 🔒, got %q", updated)
	}
}

func TestHandleCommandUnknownCommand(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST", WithOwner("U_OWNER"))
	if handled, _ := thread.handleCommand("U_OWNER", "/unknown"); handled {
		t.Error("/unknown should not be handled")
	}
	if handled, _ := thread.handleCommand("U_OWNER", "/status"); handled {
		t.Error("/status should not be handled")
	}
}

func TestHandleCommandNonOwnerBlocked(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST", WithOwner("U_OWNER"))

	// Non-owner commands are handled (recognized) but denied with feedback
	for _, cmd := range []string{"/open", "/lock", "/open <@U_ALICE>", "/lock <@U_ALICE>"} {
		handled, feedback := thread.handleCommand("U_OTHER", cmd)
		if !handled {
			t.Errorf("non-owner %q should be handled (recognized)", cmd)
		}
		if !strings.Contains(feedback, "owner") {
			t.Errorf("non-owner %q feedback should mention owner, got %q", cmd, feedback)
		}
	}
}

func TestCommandFeedback(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	thread.Start("Test")

	tests := []struct {
		cmd          string
		wantContains string
	}{
		{"/open", "🔓"},
		{"/lock", "🔒"},
		{"/open <@U_ALICE>", "🔓"},
		{"/lock <@U_ALICE>", "🔒"},
		{"/close", "🔒"},
	}
	for _, tt := range tests {
		_, feedback := thread.handleCommand("U_OWNER", tt.cmd)
		if !strings.Contains(feedback, tt.wantContains) {
			t.Errorf("handleCommand(%q) feedback = %q, want containing %q", tt.cmd, feedback, tt.wantContains)
		}
	}
}

func TestCommandFeedbackPostedToSlack(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	thread.Start("Test")

	// Inject a /open command from the owner
	mock.injectReply("C_TEST", thread.ThreadTS(), "U_OWNER", ":fox_face:: /open")
	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatal(err)
	}
	if len(replies) != 0 {
		t.Errorf("handled command should not produce replies, got %d", len(replies))
	}

	// Check that feedback was posted
	msgs := mock.activeMessages()
	var found bool
	for _, m := range msgs {
		if strings.Contains(m.Text, "🔓") && m.ThreadTS == thread.ThreadTS() {
			found = true
			break
		}
	}
	if !found {
		t.Error("feedback message should be posted to thread")
	}
}

func TestUnauthorizedBroadcastSilentlySkipped(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	thread.Start("Test")

	// Broadcast from unauthorized user — silently skipped
	mock.injectReply("C_TEST", thread.ThreadTS(), "U_OTHER", "hello everyone")
	replies, _ := thread.PollReplies()
	if len(replies) != 0 {
		t.Error("unauthorized broadcast should not produce replies")
	}
	for _, m := range mock.activeMessages() {
		if strings.Contains(m.Text, "Not authorized") {
			t.Error("broadcast from unauthorized user should not trigger feedback")
		}
	}
}

func TestUnauthorizedTargetedGetsFeedback(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	thread.Start("Test")

	// Targeted message from unauthorized user — gets ephemeral feedback
	mock.injectReply("C_TEST", thread.ThreadTS(), "U_OTHER", ":fox_face:: help me")
	replies, _ := thread.PollReplies()
	if len(replies) != 0 {
		t.Error("unauthorized targeted message should not produce replies")
	}
	var found bool
	for _, m := range mock.ephemeralMessages() {
		if strings.Contains(m.Text, "Not authorized") && m.UserID == "U_OTHER" {
			found = true
		}
	}
	if !found {
		t.Error("targeted message from unauthorized user should trigger ephemeral feedback")
	}
}

func TestOpenForSpecificUser(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	thread.Start("Test")

	// Owner opens for specific user via command
	mock.injectReply("C_TEST", thread.ThreadTS(), "U_OWNER", ":fox_face:: /open <@U_ALICE>")
	thread.PollReplies()

	// Verify Alice is now authorized
	if !thread.IsAuthorized("U_ALICE") {
		t.Error("U_ALICE should be authorized after /open <@U_ALICE>")
	}

	// Other users still not authorized
	if thread.IsAuthorized("U_BOB") {
		t.Error("U_BOB should not be authorized")
	}

	// Owner still authorized
	if !thread.IsAuthorized("U_OWNER") {
		t.Error("owner should always be authorized")
	}

	// Alice can now send messages
	mock.injectReply("C_TEST", thread.ThreadTS(), "U_ALICE", ":fox_face:: hello")
	replies, _ := thread.PollReplies()
	if len(replies) != 1 {
		t.Fatalf("Alice's message should produce 1 reply, got %d", len(replies))
	}
	if !strings.Contains(replies[0].(TextMessage).Text, "hello") {
		t.Errorf("reply text = %q, want 'hello'", replies[0].(TextMessage).Text)
	}
}

func TestOpenForAllThenLock(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	thread.Start("Test")

	// Open for everyone
	mock.injectReply("C_TEST", thread.ThreadTS(), "U_OWNER", ":fox_face:: /open")
	thread.PollReplies()

	if !thread.IsAuthorized("U_ANYONE") {
		t.Error("anyone should be authorized after /open")
	}

	// Lock again
	mock.injectReply("C_TEST", thread.ThreadTS(), "U_OWNER", ":fox_face:: /lock")
	thread.PollReplies()

	if thread.IsAuthorized("U_ANYONE") {
		t.Error("U_ANYONE should not be authorized after /lock")
	}
	if !thread.IsAuthorized("U_OWNER") {
		t.Error("owner should still be authorized after /lock")
	}
}

func TestOpenWithDisplayNameSuffix(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	thread.Start("Test")

	// Slack sometimes sends <@U_ALICE|alice> with display name
	mock.injectReply("C_TEST", thread.ThreadTS(), "U_OWNER", ":fox_face:: /open <@U_ALICE|alice>")
	thread.PollReplies()

	// Should still resolve to U_ALICE (not U_ALICE|alice)
	if !thread.IsAuthorized("U_ALICE") {
		t.Error("U_ALICE should be authorized (display name suffix stripped)")
	}
}

func TestLockBanUser(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	thread.Start("Test")

	// Open for everyone first
	mock.injectReply("C_TEST", thread.ThreadTS(), "U_OWNER", ":fox_face:: /open")
	thread.PollReplies()

	// Ban specific user
	mock.injectReply("C_TEST", thread.ThreadTS(), "U_OWNER", ":fox_face:: /lock <@U_BAD>")
	thread.PollReplies()

	// Thread is still open (ban doesn't close)
	if !thread.IsAuthorized("U_GOOD") {
		t.Error("unbanned user should still be authorized")
	}
	if thread.IsAuthorized("U_BAD") {
		t.Error("banned user should not be authorized")
	}
}

func TestNonOwnerCannotRunAccessCommands(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	thread.Start("Test")

	// Non-owner tries /open
	mock.injectReply("C_TEST", thread.ThreadTS(), "U_OTHER", ":fox_face:: /open")
	thread.PollReplies()

	// Should still be locked
	if thread.IsAuthorized("U_OTHER") {
		t.Error("non-owner /open should not change access")
	}

	// Feedback should mention owner
	var found bool
	for _, m := range mock.activeMessages() {
		if strings.Contains(m.Text, "owner") && m.ThreadTS == thread.ThreadTS() {
			found = true
		}
	}
	if !found {
		t.Error("non-owner /open should get owner feedback")
	}
}

func TestHelpCommand(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	thread.Start("Test")

	// /help via targeted command
	handled, feedback := thread.handleCommand("U_OWNER", "/help")
	if !handled {
		t.Error("/help should be handled")
	}
	if !strings.Contains(feedback, "slaude") || !strings.Contains(feedback, "/open") {
		t.Errorf("/help feedback should contain usage info, got %q", feedback)
	}

	// /help from non-owner should also work (not restricted)
	handled, feedback = thread.handleCommand("U_OTHER", "/help")
	if !handled {
		t.Error("/help from non-owner should be handled")
	}
	if !strings.Contains(feedback, "slaude") {
		t.Errorf("/help from non-owner should still show help, got %q", feedback)
	}
}

func TestBareHelpMessage(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	thread.Start("Test")

	// Bare "help" message
	mock.injectReply("C_TEST", thread.ThreadTS(), "U_OTHER", "help")
	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatal(err)
	}
	if len(replies) != 0 {
		t.Errorf("bare help should not produce replies, got %d", len(replies))
	}

	// Check help text was posted
	msgs := mock.activeMessages()
	var found bool
	for _, m := range msgs {
		if strings.Contains(m.Text, "slaude") && strings.Contains(m.Text, "/open") {
			found = true
			break
		}
	}
	if !found {
		t.Error("help text should be posted for bare 'help' message")
	}
}

func TestParseMention(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"<@U123>", "U123"},
		{"<@U_ALICE>", "U_ALICE"},
		{"hello", ""},
		{"<@>", ""},
		{"<@U123", ""},
		{"U123>", ""},
	}
	for _, tt := range tests {
		got := access.ParseMention(tt.input)
		if got != tt.want {
			t.Errorf("ParseMention(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNonOwnerCannotSendCommands(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Non-owner sends a /command via emoji prefix — should be ignored (not forwarded)
	mock.injectReply("C_TEST", threadTS, "U_OTHER", ":fox_face:: /compact")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}

	// Non-owner is not authorized, so the command should not be forwarded
	if len(replies) != 0 {
		t.Fatalf("replies = %d, want 0 (non-owner command should be ignored)", len(replies))
	}

	// Owner sends the same command — should be forwarded
	mock.injectReply("C_TEST", threadTS, "U_OWNER", ":fox_face:: /compact")

	replies, err = thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 1 {
		t.Fatalf("replies = %d, want 1", len(replies))
	}
	if replies[0].(CommandMessage).Command != "/compact" {
		t.Errorf("replies[0].(CommandMessage).Command = %q, want %q", replies[0].(CommandMessage).Command, "/compact")
	}
}

func TestNonOwnerCommandAfterOpen(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Open the thread for everyone
	thread.handleCommand("U_OWNER", "/open")

	// Non-owner sends a /command — should be forwarded (thread is open)
	mock.injectReply("C_TEST", threadTS, "U_OTHER", ":fox_face:: /compact")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 1 {
		t.Fatalf("replies = %d, want 1 (authorized user's command should be forwarded)", len(replies))
	}
	if replies[0].(CommandMessage).Command != "/compact" {
		t.Errorf("replies[0].(CommandMessage).Command = %q, want %q", replies[0].(CommandMessage).Command, "/compact")
	}
}

func TestCloseIsAliasForLock(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST", WithOwner("U_OWNER"))
	thread.handleCommand("U_OWNER", "/open <@U_ALICE>")
	if !thread.IsAuthorized("U_ALICE") {
		t.Error("alice should be authorized")
	}

	// /close should work like /lock
	thread.handleCommand("U_OWNER", "/close")
	if thread.IsAuthorized("U_ALICE") {
		t.Error("alice should not be authorized after /close")
	}
}

func TestThreadOpenAccess(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST", WithOpenAccess())
	if !thread.IsAuthorized("U_ANYONE") {
		t.Error("anyone should be authorized with open access")
	}
}

func TestThreadNoOwner(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST")
	if !thread.IsAuthorized("U_ANYONE") {
		t.Error("anyone should be authorized with no owner set")
	}
}

func TestThreadStartAndResume(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST")

	url, err := thread.Start("Test Plan")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if url == "" {
		t.Error("Start returned empty URL")
	}
	if thread.ThreadTS() == "" {
		t.Error("ThreadTS is empty after Start")
	}

	// Resume without cursor — advances lastTS to latest reply
	thread2 := NewThread(mock.client(), "C_TEST")
	thread2.Resume("1700000001.000000")
	if thread2.ThreadTS() != "1700000001.000000" {
		t.Errorf("ThreadTS = %q after Resume, want 1700000001.000000", thread2.ThreadTS())
	}
}

func TestThreadPost(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST")
	thread.Start("Test")

	_, err := thread.Post("hello from bot")
	if err != nil {
		t.Fatalf("Post: %v", err)
	}

	found := false
	for _, m := range mock.postedMessages() {
		if m.Text == "hello from bot" {
			found = true
		}
	}
	if !found {
		t.Error("posted message not found in mock")
	}
}

func TestThreadPostBlocks(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST")
	thread.Start("Test")

	section := slackapi.NewSectionBlock(
		slackapi.NewTextBlockObject("mrkdwn", "block text", false, false),
		nil, nil,
	)
	err := thread.PostBlocks("fallback", section)
	if err != nil {
		t.Fatalf("PostBlocks: %v", err)
	}

	found := false
	for _, m := range mock.postedMessages() {
		if m.Text == "fallback" {
			found = true
		}
	}
	if !found {
		t.Error("PostBlocks message not found")
	}
}

func TestThreadPostNoThread(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST")
	// No Start or Resume — threadTS is empty

	_, err := thread.Post("should fail")
	if err == nil {
		t.Error("Post should fail without active thread")
	}

	err = thread.PostBlocks("should fail")
	if err == nil {
		t.Error("PostBlocks should fail without active thread")
	}
}

func TestNewTurnSelectsCompat(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST")
	thread.Resume("1700000001.000000")
	turn := thread.NewTurn()
	impl := turn.(*turnImpl)
	if _, ok := impl.w.(*compatTurn); !ok {
		t.Error("expected compatTurn for xoxc token")
	}
}

func TestNewTurnSelectsNative(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.botClient(), "C_TEST", withAPIURL(mock.apiURL()))
	thread.Resume("1700000001.000000")
	turn := thread.NewTurn()
	impl := turn.(*turnImpl)
	if _, ok := impl.w.(*nativeTurn); !ok {
		t.Error("expected nativeTurn for xoxb token")
	}
}

func TestPollRepliesFiltering(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST", WithOwner("U_OWNER"))
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Inject a reply from the owner
	mock.injectReply("C_TEST", threadTS, "U_OWNER", "owner message")

	// Inject a reply from unauthorized user
	mock.injectReply("C_TEST", threadTS, "U_OTHER", "other message")

	// Inject a bot message
	mock.injectBotReply("C_TEST", threadTS, "B_BOT", "bot message")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}

	// Should only get the owner's message (other is unauthorized, bot is skipped)
	if len(replies) != 1 {
		t.Fatalf("replies = %d, want 1", len(replies))
	}
	if replies[0].(TextMessage).Text != "owner message" {
		t.Errorf("reply text = %q, want %q", replies[0].(TextMessage).Text, "owner message")
	}
}

func TestPollRepliesOpenClose(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Owner sends :fox_face:: /open (emoji-prefix command)
	mock.injectReply("C_TEST", threadTS, "U_OWNER", ":fox_face:: /open")

	// Other user sends a message
	mock.injectReply("C_TEST", threadTS, "U_OTHER", "hello!")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}

	// /open is consumed as command, then U_OTHER's message comes through
	if len(replies) != 1 {
		t.Fatalf("replies = %d, want 1", len(replies))
	}
	if replies[0].(TextMessage).Text != "hello!" {
		t.Errorf("reply text = %q, want %q", replies[0].(TextMessage).Text, "hello!")
	}
}

func TestPollRepliesSkipsOwnMessages(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST")
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Post a message via the thread (tracked as our own)
	thread.Post("my own message")

	// Inject a real user reply
	mock.injectReply("C_TEST", threadTS, "U_HUMAN", "human reply")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}

	// Should only get the human reply, not our own
	if len(replies) != 1 {
		t.Fatalf("replies = %d, want 1", len(replies))
	}
	if replies[0].(TextMessage).Text != "human reply" {
		t.Errorf("reply text = %q, want %q", replies[0].(TextMessage).Text, "human reply")
	}
}

func TestPollRepliesSkipsStreamingMessages(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithInstanceID("aaaa"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Another slaude instance posts a streaming message (~ suffix)
	mock.injectSlagentReply("C_TEST", threadTS, "partial response", "slagent-bbbb~")

	// Should not see any replies (streaming message not finalized)
	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 0 {
		t.Fatalf("replies = %d, want 0 (streaming should be skipped)", len(replies))
	}
}

func TestPollRepliesDeliversFinalizedFromOtherInstance(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithInstanceID("aaaa"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Another slaude instance posts a finalized message (no suffix) — should be delivered
	mock.injectSlagentReply("C_TEST", threadTS, "other slaude response", "slagent-bbbb")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 1 {
		t.Fatalf("replies = %d, want 1 (other instance finalized should be delivered)", len(replies))
	}
	if replies[0].(TextMessage).Text != "other slaude response" {
		t.Errorf("reply text = %q, want %q", replies[0].(TextMessage).Text, "other slaude response")
	}
	if !replies[0].(TextMessage).Observe {
		t.Error("other-instance messages should be marked as observe-only")
	}
}

func TestPollRepliesSkipsFinalizedFromOwnInstance(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithInstanceID("aaaa"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Own finalized message — should be skipped
	mock.injectSlagentReply("C_TEST", threadTS, "my own response", "slagent-aaaa")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 0 {
		t.Fatalf("replies = %d, want 0 (own finalized should be skipped)", len(replies))
	}
}

func TestPollRepliesSkipsActivityFromAllInstances(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithInstanceID("aaaa"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Activity from our instance
	mock.injectSlagentReply("C_TEST", threadTS, "thinking...", "slagent-aaaa~act")

	// Activity from another instance
	mock.injectSlagentReply("C_TEST", threadTS, "reading files...", "slagent-bbbb~act")

	// Inject a real user reply after the activity messages
	mock.injectReply("C_TEST", threadTS, "U_HUMAN", "human reply")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 1 {
		t.Fatalf("replies = %d, want 1 (activity should be skipped)", len(replies))
	}
	if replies[0].(TextMessage).Text != "human reply" {
		t.Errorf("reply text = %q, want %q", replies[0].(TextMessage).Text, "human reply")
	}
}

func TestPollRepliesStreamingThenFinalizedSkipped(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithInstanceID("aaaa"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Another instance posts a streaming message
	ts := mock.injectSlagentReply("C_TEST", threadTS, "partial", "slagent-bbbb~")

	// First poll: streaming message skipped, not advanced
	replies, _ := thread.PollReplies()
	if len(replies) != 0 {
		t.Fatalf("first poll: replies = %d, want 0", len(replies))
	}

	// Simulate finalization: update block_id to remove ~ suffix
	mock.updateBlockID(ts, "slagent-bbbb")

	// Second poll: finalized message from other instance is delivered
	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 1 {
		t.Fatalf("second poll: replies = %d, want 1 (other instance finalized should be delivered)", len(replies))
	}
	if replies[0].(TextMessage).Text != "partial" {
		t.Errorf("reply text = %q, want %q", replies[0].(TextMessage).Text, "partial")
	}
}

// Tests for multi-instance message visibility rules:
// - Own finalized: skip (never see your own output)
// - Own streaming: skip (don't advance cursor)
// - Own activity: skip
// - Other finalized: DELIVER (agent perceives, system prompt controls behavior)
// - Other streaming: skip (not finalized yet, don't advance cursor)
// - Other activity: skip
// - Human messages: always deliver (subject to authorization)

func TestMultiInstanceVisibility(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithInstanceID("dog"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Own activity — skip
	mock.injectSlagentReply("C_TEST", threadTS, "reading file", "slagent-dog~act")

	// Other activity — skip
	mock.injectSlagentReply("C_TEST", threadTS, "thinking...", "slagent-rhino~act")

	// Own streaming — skip
	mock.injectSlagentReply("C_TEST", threadTS, "partial own text", "slagent-dog~")

	// Other streaming — skip (not finalized)
	mock.injectSlagentReply("C_TEST", threadTS, "partial other text", "slagent-rhino~")

	// Own finalized — skip
	mock.injectSlagentReply("C_TEST", threadTS, "my final response", "slagent-dog")

	// Other finalized — DELIVER
	mock.injectSlagentReply("C_TEST", threadTS, ":rhinoceros: I finished the task", "slagent-rhino")

	// Human message — DELIVER
	mock.injectReply("C_TEST", threadTS, "U_HUMAN", "looks good")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 2 {
		t.Fatalf("replies = %d, want 2 (other finalized + human)", len(replies))
	}
	if replies[0].(TextMessage).Text != ":rhinoceros: I finished the task" {
		t.Errorf("reply[0] text = %q, want other instance finalized", replies[0].(TextMessage).Text)
	}
	if replies[1].(TextMessage).Text != "looks good" {
		t.Errorf("reply[1] text = %q, want human message", replies[1].(TextMessage).Text)
	}
}

func TestMultiInstanceAddressedToOther(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithInstanceID("dog"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Human addresses rhino — delivered (dog perceives, system prompt says don't act)
	mock.injectReply("C_TEST", threadTS, "U_HUMAN", ":rhinoceros:: do this task")

	// Human addresses dog — delivered (dog acts on it)
	mock.injectReply("C_TEST", threadTS, "U_HUMAN", ":dog:: do that task")

	// Untargeted — delivered (dog acts normally)
	mock.injectReply("C_TEST", threadTS, "U_HUMAN", "general update")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}

	// All 3 non-command messages are delivered
	if len(replies) != 3 {
		t.Fatalf("replies = %d, want 3", len(replies))
	}
	if replies[0].(TextMessage).Text != ":rhinoceros:: do this task" {
		t.Errorf("reply[0] = %q, want addressed-to-other delivered with prefix", replies[0].(TextMessage).Text)
	}
	if replies[1].(TextMessage).Text != ":dog:: do that task" {
		t.Errorf("reply[1] = %q, want addressed-to-self delivered with prefix", replies[1].(TextMessage).Text)
	}
	if replies[2].(TextMessage).Text != "general update" {
		t.Errorf("reply[2] = %q, want untargeted message", replies[2].(TextMessage).Text)
	}
}

func TestMultiInstanceCommandsAreExclusive(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithInstanceID("dog"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Command to rhino — exclusive, dog should NOT see it
	mock.injectReply("C_TEST", threadTS, "U_HUMAN", ":rhinoceros:: /compact")

	// Command to dog — exclusive, dog SHOULD see it
	mock.injectReply("C_TEST", threadTS, "U_HUMAN", ":dog:: /status")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 1 {
		t.Fatalf("replies = %d, want 1 (only dog's command)", len(replies))
	}
	if replies[0].(CommandMessage).Command != "/status" {
		t.Errorf("reply command = %q, want /status", replies[0].(CommandMessage).Command)
	}
}

func TestMultiInstanceOtherFinalizedThenHuman(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithInstanceID("dog"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Other instance responds, then human replies
	mock.injectSlagentReply("C_TEST", threadTS, ":rhinoceros: done with refactoring", "slagent-rhino")
	mock.injectReply("C_TEST", threadTS, "U_HUMAN", "great work both of you")
	mock.injectSlagentReply("C_TEST", threadTS, ":rhinoceros: thanks!", "slagent-rhino")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}

	// All 3 delivered: 2 from rhino (finalized) + 1 from human
	if len(replies) != 3 {
		t.Fatalf("replies = %d, want 3", len(replies))
	}
	if replies[0].(TextMessage).Text != ":rhinoceros: done with refactoring" {
		t.Errorf("reply[0] = %q", replies[0].(TextMessage).Text)
	}
	if replies[1].(TextMessage).Text != "great work both of you" {
		t.Errorf("reply[1] = %q", replies[1].(TextMessage).Text)
	}
	if replies[2].(TextMessage).Text != ":rhinoceros: thanks!" {
		t.Errorf("reply[2] = %q", replies[2].(TextMessage).Text)
	}
}

func TestStopBare(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithInstanceID("dog"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	mock.injectReply("C_TEST", threadTS, "U_HUMAN", "stop")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 1 {
		t.Fatalf("replies = %d, want 1", len(replies))
	}
	if !isStop(replies[0]) {
		t.Error("reply.Stop = false, want true")
	}
}

func TestStopCaseInsensitive(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithInstanceID("dog"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	mock.injectReply("C_TEST", threadTS, "U_HUMAN", "STOP")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 1 || !isStop(replies[0]) {
		t.Fatalf("expected Stop reply for 'STOP'")
	}
}

func TestStopTargeted(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithInstanceID("dog"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Targeted to this instance — should be delivered as stop
	mock.injectReply("C_TEST", threadTS, "U_HUMAN", ":dog:: stop")

	// Targeted to another instance — should be ignored
	mock.injectReply("C_TEST", threadTS, "U_HUMAN", ":rhinoceros:: stop")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 1 {
		t.Fatalf("replies = %d, want 1 (only targeted stop)", len(replies))
	}
	if !isStop(replies[0]) {
		t.Error("reply.Stop = false, want true")
	}
}

func TestStopWithSpaces(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithInstanceID("dog"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	mock.injectReply("C_TEST", threadTS, "U_HUMAN", "  stop  ")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 1 || !isStop(replies[0]) {
		t.Fatalf("expected Stop reply for '  stop  '")
	}
}

func TestQuitByOwner(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithInstanceID("dog"),
		WithOwner("U_OWNER"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	mock.injectReply("C_TEST", threadTS, "U_OWNER", "quit")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 1 {
		t.Fatalf("replies = %d, want 1", len(replies))
	}
	if !isQuit(replies[0]) {
		t.Error("reply.Quit = false, want true")
	}
}

func TestQuitByNonOwnerDenied(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithInstanceID("dog"),
		WithOwner("U_OWNER"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Non-owner tries to quit — should be denied
	mock.injectReply("C_TEST", threadTS, "U_OTHER", "quit")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 0 {
		t.Fatalf("replies = %d, want 0 (non-owner quit should be denied)", len(replies))
	}
}

func TestQuitTargeted(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithInstanceID("dog"),
		WithOwner("U_OWNER"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Targeted to another instance — ignored
	mock.injectReply("C_TEST", threadTS, "U_OWNER", ":rhinoceros:: quit")

	// Targeted to this instance — delivered
	mock.injectReply("C_TEST", threadTS, "U_OWNER", ":dog:: quit")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 1 {
		t.Fatalf("replies = %d, want 1", len(replies))
	}
	if !isQuit(replies[0]) {
		t.Error("reply.Quit = false, want true")
	}
}

func TestRepliesBlockingWithCancel(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithPollInterval(50*time.Millisecond),
	)
	thread.Start("Test")

	// Cancel the context immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := thread.Replies(ctx)
	if err != context.Canceled {
		t.Errorf("Replies error = %v, want context.Canceled", err)
	}
}

func TestRepliesBlockingWithTimeout(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithPollInterval(50*time.Millisecond),
	)
	thread.Start("Test")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := thread.Replies(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("Replies error = %v, want context.DeadlineExceeded", err)
	}
}

func TestRepliesBlockingReturnsOnReply(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithPollInterval(50*time.Millisecond),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Inject a reply after a short delay
	go func() {
		time.Sleep(100 * time.Millisecond)
		mock.injectReply("C_TEST", threadTS, "U_HUMAN", "delayed reply")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	replies, err := thread.Replies(ctx)
	if err != nil {
		t.Fatalf("Replies: %v", err)
	}
	if len(replies) != 1 || replies[0].(TextMessage).Text != "delayed reply" {
		t.Errorf("unexpected replies: %v", replies)
	}
}

func TestParseInstancePrefix(t *testing.T) {
	tests := []struct {
		text       string
		wantID     string
		wantRest   string
		wantTarget bool
	}{
		// Double colon (legacy syntax, still works)
		{":fox_face:: hello", "fox_face", "hello", true},
		{":fox_face::hello", "fox_face", "hello", true},
		{":fox_face:: /open", "fox_face", "/open", true},

		// Single colon (new: also targeted)
		{":fox_face: hello", "fox_face", "hello", true},
		{":fox_face: /open", "fox_face", "/open", true},

		// No space after shortcode
		{":fox_face:hello", "fox_face", "hello", true},

		// Slack space variants (Slack inserts spaces unpredictably)
		{":fox_face: : hello", "fox_face", "hello", true},
		{":fox_face: :/open", "fox_face", "/open", true},
		{":fox_face: : ", "fox_face", "", true},
		{":fox_face:  : /open", "fox_face", "/open", true},
		{":fox_face: :  /open", "fox_face", "/open", true},
		{":fox_face:  :  /open", "fox_face", "/open", true},

		// No prefix — not targeted
		{"hello world", "", "hello world", false},
		// Unknown shortcode — not targeted
		{":unknown_thing:: hello", "", ":unknown_thing:: hello", false},
		// Empty string
		{"", "", "", false},
		// Just the shortcode with colon, no rest
		{":dog:: ", "dog", "", true},
		{":dog::", "dog", "", true},
		{":dog: ", "dog", "", true},
		{":dog:", "dog", "", true},
	}
	for _, tt := range tests {
		id, rest, targeted := parseInstancePrefix(tt.text)
		if id != tt.wantID || rest != tt.wantRest || targeted != tt.wantTarget {
			t.Errorf("parseInstancePrefix(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.text, id, rest, targeted, tt.wantID, tt.wantRest, tt.wantTarget)
		}
	}
}

func TestParseMessageStripsAtMentions(t *testing.T) {
	tests := []struct {
		text       string
		wantID     string
		wantRest   string
		wantTarget bool
	}{
		// @mention then :shortcode::
		{"<@U123> :fox_face:: do something", "fox_face", "do something", true},
		// Multiple @mentions
		{"<@U123> <@U456> :dog:: hello", "dog", "hello", true},
		// @mention without shortcode — not targeted
		{"<@U123> hello", "", "hello", false},
		// No @mention, with colon
		{":cat:: meow", "cat", "meow", true},
	}
	for _, tt := range tests {
		id, rest, targeted := parseMessage(tt.text)
		if id != tt.wantID || rest != tt.wantRest || targeted != tt.wantTarget {
			t.Errorf("parseMessage(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.text, id, rest, targeted, tt.wantID, tt.wantRest, tt.wantTarget)
		}
	}
}

func TestPollRepliesEmojiPrefixTargeting(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithInstanceID("fox_face"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Message targeted at this instance — delivered with original text
	mock.injectReply("C_TEST", threadTS, "U_HUMAN", ":fox_face:: do this")

	// Message targeted at another instance — also delivered (non-command)
	mock.injectReply("C_TEST", threadTS, "U_HUMAN", ":dog:: do that")

	// Untargeted message — delivered as-is
	mock.injectReply("C_TEST", threadTS, "U_HUMAN", "general message")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}

	// All 3 messages delivered (non-command messages are broadcast)
	if len(replies) != 3 {
		t.Fatalf("replies = %d, want 3", len(replies))
	}

	// All keep original text (prefix included so Claude sees targeting)
	if replies[0].(TextMessage).Text != ":fox_face:: do this" {
		t.Errorf("replies[0].(TextMessage).Text = %q, want %q", replies[0].(TextMessage).Text, ":fox_face:: do this")
	}
	if replies[1].(TextMessage).Text != ":dog:: do that" {
		t.Errorf("replies[1].(TextMessage).Text = %q, want %q", replies[1].(TextMessage).Text, ":dog:: do that")
	}
	if replies[2].(TextMessage).Text != "general message" {
		t.Errorf("replies[2].(TextMessage).Text = %q, want %q", replies[2].(TextMessage).Text, "general message")
	}
}

func TestPollRepliesCommandOnlyForTargetInstance(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithInstanceID("fox_face"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Command targeted at another instance — should be ignored
	mock.injectReply("C_TEST", threadTS, "U_HUMAN", ":dog:: /compact")

	// Command targeted at this instance — should be forwarded
	mock.injectReply("C_TEST", threadTS, "U_HUMAN", ":fox_face:: /compact")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}

	if len(replies) != 1 {
		t.Fatalf("replies = %d, want 1", len(replies))
	}
	if replies[0].(CommandMessage).Command != "/compact" {
		t.Errorf("replies[0].(CommandMessage).Command = %q, want %q", replies[0].(CommandMessage).Command, "/compact")
	}
}

func TestPollRepliesEmojiPrefixCommand(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithInstanceID("fox_face"),
	)
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Unknown /command targeted at this instance — forwarded to Claude
	mock.injectReply("C_TEST", threadTS, "U_HUMAN", ":fox_face:: /compact")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}

	if len(replies) != 1 {
		t.Fatalf("replies = %d, want 1", len(replies))
	}
	if replies[0].(CommandMessage).Command != "/compact" {
		t.Errorf("replies[0].(CommandMessage).Command = %q, want %q", replies[0].(CommandMessage).Command, "/compact")
	}
}

func TestResumeWithAfterTS(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	// Post a thread with replies
	thread := NewThread(mock.client(), "C_TEST", WithInstanceID("dog"), WithOwner("U_OWNER"))
	thread.Start("Test Plan")
	thread.Post("first reply")
	thread.Post("second reply")

	// Resume with explicit afterTS — should skip all messages up to that point
	thread2 := NewThread(mock.client(), "C_TEST", WithInstanceID("dog"), WithOwner("U_OWNER"))
	thread2.Resume(thread.ThreadTS(), "1700000099.000000")

	if thread2.LastTS() != "1700000099.000000" {
		t.Errorf("LastTS = %q, want 1700000099.000000", thread2.LastTS())
	}
}

func TestResumeWithoutAfterTSAdvancesToLatest(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	// Post a thread with replies
	thread := NewThread(mock.client(), "C_TEST", WithInstanceID("dog"), WithOwner("U_OWNER"))
	thread.Start("Test Plan")
	thread.Post("first reply")
	thread.Post("second reply")

	// Capture the latest message TS
	active := mock.activeMessages()
	latestTS := active[len(active)-1].TS

	// Resume without afterTS — should advance to latest reply
	thread2 := NewThread(mock.client(), "C_TEST", WithInstanceID("dog"), WithOwner("U_OWNER"))
	thread2.Resume(thread.ThreadTS())

	if thread2.LastTS() != latestTS {
		t.Errorf("LastTS = %q, want %q (latest message)", thread2.LastTS(), latestTS)
	}
}

func TestResumeWithAfterTSSkipsOldReplies(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	// Post a thread with messages
	thread := NewThread(mock.client(), "C_TEST", WithInstanceID("dog"), WithOwner("U_OWNER"))
	thread.Start("Test Plan")
	threadTS := thread.ThreadTS()

	// Simulate old messages
	mock.injectReply("C_TEST", threadTS, "U_OWNER", ":dog:: /open")
	mock.injectReply("C_TEST", threadTS, "U_OWNER", "old feedback")

	// Get the TS of the last old message
	active := mock.activeMessages()
	lastOldTS := active[len(active)-1].TS

	// Resume with afterTS set to last old message
	thread2 := NewThread(mock.client(), "C_TEST", WithInstanceID("dog"), WithOwner("U_OWNER"))
	thread2.Resume(threadTS, lastOldTS)

	// Poll should return nothing (all messages are before afterTS)
	replies, err := thread2.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 0 {
		t.Errorf("expected 0 replies after resume with afterTS, got %d", len(replies))
	}

	// New message after resume should be visible
	mock.injectReply("C_TEST", threadTS, "U_OWNER", "new feedback")
	replies, err = thread2.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 1 {
		t.Errorf("expected 1 new reply, got %d", len(replies))
	}
}

func TestAdvanceLastTS(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST")
	thread.lastTS = "1700000001.000000"

	// Newer timestamp advances
	thread.advanceLastTS("1700000005.000000")
	if thread.lastTS != "1700000005.000000" {
		t.Errorf("lastTS = %q, want 1700000005.000000", thread.lastTS)
	}

	// Older timestamp does not regress
	thread.advanceLastTS("1700000002.000000")
	if thread.lastTS != "1700000005.000000" {
		t.Errorf("lastTS = %q, want 1700000005.000000 (should not regress)", thread.lastTS)
	}
}

func TestOtherInstanceFilteredInClosedMode(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox"),
	)
	thread.Start("Test")

	// Other slagent instance posts a finalized message
	mock.injectSlagentReply("C_TEST", thread.ThreadTS(), "hello from dog", "slagent-dog")

	// In closed mode (default), other-instance messages should be filtered
	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 0 {
		t.Errorf("got %d replies, want 0 (other-instance messages should be filtered in closed mode)", len(replies))
	}
}

func TestOtherInstanceVisibleInObserveMode(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox"),
		WithObserve(),
	)
	thread.Start("Test")

	// Other slagent instance posts a finalized message
	mock.injectSlagentReply("C_TEST", thread.ThreadTS(), "hello from dog", "slagent-dog")

	// In observe mode, other-instance messages should be delivered
	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1 (other-instance messages should be visible in observe mode)", len(replies))
	}
	if replies[0].(TextMessage).Text != "hello from dog" {
		t.Errorf("reply text = %q, want %q", replies[0].(TextMessage).Text, "hello from dog")
	}
	if !replies[0].(TextMessage).Observe {
		t.Error("other-instance messages should be marked as observe-only")
	}
}

func TestOtherInstanceVisibleInOpenMode(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox"),
	)
	thread.Start("Test")

	// Open the thread
	thread.handleCommand("U_OWNER", "/open")

	// Other slagent instance posts a finalized message
	mock.injectSlagentReply("C_TEST", thread.ThreadTS(), "hello from dog", "slagent-dog")

	// In open mode, other-instance messages should be delivered
	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1 (other-instance messages should be visible in open mode)", len(replies))
	}
}

func TestOtherInstanceStreamingSkipped(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox"),
		WithObserve(),
	)
	thread.Start("Test")

	// Other instance streaming (not finalized yet) — should be skipped
	mock.injectSlagentReply("C_TEST", thread.ThreadTS(), "typing...", "slagent-dog~")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 0 {
		t.Errorf("got %d replies, want 0 (streaming messages should be skipped)", len(replies))
	}
}

func TestOtherInstanceActivitySkipped(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox"),
		WithObserve(),
	)
	thread.Start("Test")

	// Other instance activity message — always skipped
	mock.injectSlagentReply("C_TEST", thread.ThreadTS(), "running tool", "slagent-dog~act")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 0 {
		t.Errorf("got %d replies, want 0 (activity messages should always be skipped)", len(replies))
	}
}

func TestOwnInstanceFinalSkipped(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox"),
		WithObserve(),
	)
	thread.Start("Test")

	// Own finalized message — always skipped
	mock.injectSlagentReply("C_TEST", thread.ThreadTS(), "my own message", "slagent-fox")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if len(replies) != 0 {
		t.Errorf("got %d replies, want 0 (own messages should always be skipped)", len(replies))
	}
}

func TestObserveModeToggleFiltersOtherInstance(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox"),
	)
	thread.Start("Test")

	// Initially closed — other-instance filtered
	mock.injectSlagentReply("C_TEST", thread.ThreadTS(), "msg1", "slagent-dog")
	replies, _ := thread.PollReplies()
	if len(replies) != 0 {
		t.Error("closed mode: other-instance message should be filtered")
	}

	// Enable observe — other-instance visible
	thread.handleCommand("U_OWNER", "/observe")
	mock.injectSlagentReply("C_TEST", thread.ThreadTS(), "msg2", "slagent-dog")
	replies, _ = thread.PollReplies()
	if len(replies) != 1 {
		t.Errorf("observe mode: got %d replies, want 1", len(replies))
	}

	// Disable observe — other-instance filtered again
	thread.handleCommand("U_OWNER", "/observe")
	mock.injectSlagentReply("C_TEST", thread.ThreadTS(), "msg3", "slagent-dog")
	replies, _ = thread.PollReplies()
	if len(replies) != 0 {
		t.Error("after observe off: other-instance message should be filtered")
	}
}
