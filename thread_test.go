package slagent

import (
	"context"
	"strings"
	"testing"
	"time"

	slackapi "github.com/slack-go/slack"
)

func TestClassifyBlock(t *testing.T) {
	tests := []struct {
		blockID    string
		wantKind   blockKind
		wantID     string
	}{
		{"slagent-abc123", blockFinal, "abc123"},
		{"slagent-abc123~", blockStreaming, "abc123"},
		{"slagent-abc123~act", blockActivity, "abc123"},
		{"other-block", blockNone, ""},
		{"", blockNone, ""},
		{"slagent-", blockFinal, ""},
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
	if !thread.isAuthorized("U_OWNER") {
		t.Error("owner should be authorized")
	}

	// Other user is not
	if thread.isAuthorized("U_OTHER") {
		t.Error("other user should not be authorized")
	}

	// /open command from owner
	if handled, _ := thread.handleCommand("U_OWNER", "/open"); !handled {
		t.Error("/open from owner should be handled")
	}
	if !thread.isAuthorized("U_OTHER") {
		t.Error("other user should be authorized after /open")
	}

	// /close from owner
	if handled, _ := thread.handleCommand("U_OWNER", "/close"); !handled {
		t.Error("/close from owner should be handled")
	}
	if thread.isAuthorized("U_OTHER") {
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
	if thread.isAuthorized("U_ALICE") {
		t.Error("alice should not be authorized initially")
	}

	// /open <@U_ALICE> — allow alice
	thread.handleCommand("U_OWNER", "/open <@U_ALICE>")
	if !thread.isAuthorized("U_ALICE") {
		t.Error("alice should be authorized after /open")
	}
	if thread.isAuthorized("U_BOB") {
		t.Error("bob should not be authorized")
	}

	// /open <@U_BOB> — also allow bob (additive)
	thread.handleCommand("U_OWNER", "/open <@U_BOB>")
	if !thread.isAuthorized("U_ALICE") {
		t.Error("alice should still be authorized")
	}
	if !thread.isAuthorized("U_BOB") {
		t.Error("bob should now be authorized")
	}

	// /lock — reset everything
	thread.handleCommand("U_OWNER", "/lock")
	if thread.isAuthorized("U_ALICE") {
		t.Error("alice should not be authorized after /lock")
	}
	if thread.isAuthorized("U_BOB") {
		t.Error("bob should not be authorized after /lock")
	}
}

func TestOpenMultipleUsersAtOnce(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST", WithOwner("U_OWNER"))

	// /open <@U_ALICE> <@U_BOB> — allow both at once
	thread.handleCommand("U_OWNER", "/open <@U_ALICE> <@U_BOB>")
	if !thread.isAuthorized("U_ALICE") {
		t.Error("alice should be authorized")
	}
	if !thread.isAuthorized("U_BOB") {
		t.Error("bob should be authorized")
	}
	if thread.isAuthorized("U_CAROL") {
		t.Error("carol should not be authorized")
	}
}

func TestLockSpecificUser(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST", WithOwner("U_OWNER"))

	// Open for everyone first
	thread.handleCommand("U_OWNER", "/open")
	if !thread.isAuthorized("U_ALICE") {
		t.Error("alice should be authorized when open")
	}

	// /lock <@U_ALICE> — ban alice specifically
	thread.handleCommand("U_OWNER", "/lock <@U_ALICE>")
	if thread.isAuthorized("U_ALICE") {
		t.Error("alice should be banned")
	}
	if !thread.isAuthorized("U_BOB") {
		t.Error("bob should still be authorized (thread is open)")
	}

	// Owner is never banned
	thread.handleCommand("U_OWNER", "/lock <@U_OWNER>")
	if !thread.isAuthorized("U_OWNER") {
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
	if thread.isAuthorized("U_ALICE") {
		t.Error("alice should be banned")
	}
	if thread.isAuthorized("U_BOB") {
		t.Error("bob should be banned")
	}
	if !thread.isAuthorized("U_CAROL") {
		t.Error("carol should still be authorized")
	}
}

func TestOpenUnbansBannedUser(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST", WithOwner("U_OWNER"))
	thread.handleCommand("U_OWNER", "/open")
	thread.handleCommand("U_OWNER", "/lock <@U_ALICE>")
	if thread.isAuthorized("U_ALICE") {
		t.Error("alice should be banned")
	}

	// /open <@U_ALICE> — unban alice
	thread.handleCommand("U_OWNER", "/open <@U_ALICE>")
	if !thread.isAuthorized("U_ALICE") {
		t.Error("alice should be unbanned after /open")
	}
}

func TestLockRemovesFromAllowed(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST", WithOwner("U_OWNER"))

	// Allow alice specifically
	thread.handleCommand("U_OWNER", "/open <@U_ALICE>")
	if !thread.isAuthorized("U_ALICE") {
		t.Error("alice should be allowed")
	}

	// Ban alice — should remove from allowed too
	thread.handleCommand("U_OWNER", "/lock <@U_ALICE>")
	if thread.isAuthorized("U_ALICE") {
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
			if th.openAccess != tt.wantOpen {
				t.Errorf("openAccess = %v, want %v", th.openAccess, tt.wantOpen)
			}
			for _, uid := range tt.wantAllowed {
				if !th.allowedUsers[uid] {
					t.Errorf("allowedUsers missing %s", uid)
				}
			}
			for _, uid := range tt.wantBanned {
				if !th.bannedUsers[uid] {
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
	if th2.openAccess != false {
		t.Error("roundtrip openAccess should be false")
	}
	if !th2.allowedUsers["U_ALICE"] || !th2.allowedUsers["U_BOB"] {
		t.Errorf("roundtrip allowedUsers = %v, want alice+bob", th2.allowedUsers)
	}
	if !th2.bannedUsers["U_EVIL"] {
		t.Errorf("roundtrip bannedUsers = %v, want evil", th2.bannedUsers)
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

func TestUnauthorizedMessageFeedback(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	thread.Start("Test")

	// Inject a message from unauthorized user
	mock.injectReply("C_TEST", thread.ThreadTS(), "U_OTHER", "hello")
	replies, _ := thread.PollReplies()
	if len(replies) != 0 {
		t.Error("unauthorized message should not produce replies")
	}

	// Check feedback was posted
	msgs := mock.activeMessages()
	var found bool
	for _, m := range msgs {
		if strings.Contains(m.Text, "Not authorized") && m.ThreadTS == thread.ThreadTS() {
			found = true
			break
		}
	}
	if !found {
		t.Error("unauthorized feedback should be posted")
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

func TestMistargeted(t *testing.T) {
	tests := []struct {
		input    string
		wantHint bool
	}{
		// Near-miss: single colon + command
		{":fox_face: /compact", true},
		{":dog: /open", true},
		{"<@U123> :fox_face: /compact", true},

		// Near-miss: Unicode emoji + command (with various spacing/colons)
		{"🦊 /compact", true},
		{"🦊  /compact", true},
		{"🦊: /compact", true},
		{"🐶 /open", true},

		// Correct syntax — not a near-miss
		{":fox_face:: /compact", false},

		// Not a command — no hint needed
		{":fox_face: hello", false},
		{"🦊 hello", false},

		// Unknown emoji — no hint
		{":not_an_emoji: /compact", false},
		{"😀 /compact", false},

		// Plain text
		{"hello world", false},
	}
	for _, tt := range tests {
		hint := mistargeted(tt.input)
		if tt.wantHint && hint == "" {
			t.Errorf("mistargeted(%q) = empty, want hint", tt.input)
		}
		if !tt.wantHint && hint != "" {
			t.Errorf("mistargeted(%q) = %q, want empty", tt.input, hint)
		}
	}
}

func TestMistargetedFeedbackPosted(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST",
		WithOwner("U_OWNER"),
		WithInstanceID("fox_face"),
	)
	thread.Start("Test")

	// Inject a near-miss message: Unicode emoji + command
	mock.injectReply("C_TEST", thread.ThreadTS(), "U_OWNER", "🦊 /compact")
	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatal(err)
	}
	if len(replies) != 0 {
		t.Errorf("near-miss should not produce replies, got %d", len(replies))
	}

	// Check feedback was posted
	msgs := mock.activeMessages()
	var found bool
	for _, m := range msgs {
		if strings.Contains(m.Text, "::") && strings.Contains(m.Text, "fox_face") {
			found = true
			break
		}
	}
	if !found {
		t.Error("near-miss feedback should be posted")
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
		got := parseMention(tt.input)
		if got != tt.want {
			t.Errorf("parseMention(%q) = %q, want %q", tt.input, got, tt.want)
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
	if replies[0].Command != "/compact" {
		t.Errorf("replies[0].Command = %q, want %q", replies[0].Command, "/compact")
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
	if replies[0].Command != "/compact" {
		t.Errorf("replies[0].Command = %q, want %q", replies[0].Command, "/compact")
	}
}

func TestCloseIsAliasForLock(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST", WithOwner("U_OWNER"))
	thread.handleCommand("U_OWNER", "/open <@U_ALICE>")
	if !thread.isAuthorized("U_ALICE") {
		t.Error("alice should be authorized")
	}

	// /close should work like /lock
	thread.handleCommand("U_OWNER", "/close")
	if thread.isAuthorized("U_ALICE") {
		t.Error("alice should not be authorized after /close")
	}
}

func TestThreadOpenAccess(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST", WithOpenAccess())
	if !thread.isAuthorized("U_ANYONE") {
		t.Error("anyone should be authorized with open access")
	}
}

func TestThreadNoOwner(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "C_TEST")
	if !thread.isAuthorized("U_ANYONE") {
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
	if replies[0].Text != "owner message" {
		t.Errorf("reply text = %q, want %q", replies[0].Text, "owner message")
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
	if replies[0].Text != "hello!" {
		t.Errorf("reply text = %q, want %q", replies[0].Text, "hello!")
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
	if replies[0].Text != "human reply" {
		t.Errorf("reply text = %q, want %q", replies[0].Text, "human reply")
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
	if replies[0].Text != "other slaude response" {
		t.Errorf("reply text = %q, want %q", replies[0].Text, "other slaude response")
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
	if replies[0].Text != "human reply" {
		t.Errorf("reply text = %q, want %q", replies[0].Text, "human reply")
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
	if replies[0].Text != "partial" {
		t.Errorf("reply text = %q, want %q", replies[0].Text, "partial")
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
	if replies[0].Text != ":rhinoceros: I finished the task" {
		t.Errorf("reply[0] text = %q, want other instance finalized", replies[0].Text)
	}
	if replies[1].Text != "looks good" {
		t.Errorf("reply[1] text = %q, want human message", replies[1].Text)
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
	if replies[0].Text != ":rhinoceros:: do this task" {
		t.Errorf("reply[0] = %q, want addressed-to-other delivered with prefix", replies[0].Text)
	}
	if replies[1].Text != ":dog:: do that task" {
		t.Errorf("reply[1] = %q, want addressed-to-self delivered with prefix", replies[1].Text)
	}
	if replies[2].Text != "general update" {
		t.Errorf("reply[2] = %q, want untargeted message", replies[2].Text)
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
	if replies[0].Command != "/status" {
		t.Errorf("reply command = %q, want /status", replies[0].Command)
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
	if replies[0].Text != ":rhinoceros: done with refactoring" {
		t.Errorf("reply[0] = %q", replies[0].Text)
	}
	if replies[1].Text != "great work both of you" {
		t.Errorf("reply[1] = %q", replies[1].Text)
	}
	if replies[2].Text != ":rhinoceros: thanks!" {
		t.Errorf("reply[2] = %q", replies[2].Text)
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
	if !replies[0].Stop {
		t.Error("reply.Stop = false, want true")
	}
	if replies[0].Text != "" {
		t.Errorf("reply.Text = %q, want empty", replies[0].Text)
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
	if len(replies) != 1 || !replies[0].Stop {
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
	if !replies[0].Stop {
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
	if len(replies) != 1 || !replies[0].Stop {
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
	if !replies[0].Quit {
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
	if !replies[0].Quit {
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
	if len(replies) != 1 || replies[0].Text != "delayed reply" {
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
		// Targeted with colon + space (renders as 🦊: hello)
		{":fox_face:: hello", "fox_face", "hello", true},
		// Targeted with colon, no space
		{":fox_face::hello", "fox_face", "hello", true},
		// Targeted with colon before /command
		{":fox_face:: /open", "fox_face", "/open", true},
		// Slack space variants (Slack inserts spaces unpredictably)
		{":fox_face: : hello", "fox_face", "hello", true},
		{":fox_face: :/open", "fox_face", "/open", true},
		{":fox_face: : ", "fox_face", "", true},
		{":fox_face:  : /open", "fox_face", "/open", true},  // double space before colon
		{":fox_face: :  /open", "fox_face", "/open", true},  // double space after colon
		{":fox_face:  :  /open", "fox_face", "/open", true}, // double spaces both sides
		// Without trailing colon — NOT targeted (must be explicit)
		{":fox_face: hello", "", ":fox_face: hello", false},
		// No prefix — not targeted
		{"hello world", "", "hello world", false},
		// Unknown shortcode — not targeted
		{":unknown_thing:: hello", "", ":unknown_thing:: hello", false},
		// Empty string
		{"", "", "", false},
		// Just the shortcode with colon, no rest
		{":dog:: ", "dog", "", true},
		{":dog::", "dog", "", true},
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
	if replies[0].Text != ":fox_face:: do this" {
		t.Errorf("replies[0].Text = %q, want %q", replies[0].Text, ":fox_face:: do this")
	}
	if replies[1].Text != ":dog:: do that" {
		t.Errorf("replies[1].Text = %q, want %q", replies[1].Text, ":dog:: do that")
	}
	if replies[2].Text != "general message" {
		t.Errorf("replies[2].Text = %q, want %q", replies[2].Text, "general message")
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
	if replies[0].Command != "/compact" {
		t.Errorf("replies[0].Command = %q, want %q", replies[0].Command, "/compact")
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
	if replies[0].Command != "/compact" {
		t.Errorf("replies[0].Command = %q, want %q", replies[0].Command, "/compact")
	}
	if replies[0].Text != "" {
		t.Errorf("replies[0].Text = %q, want empty", replies[0].Text)
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
