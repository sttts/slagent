package slagent

import (
	"context"
	"testing"
	"time"

	slackapi "github.com/slack-go/slack"
)

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

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST", WithOwner("U_OWNER"))

	// Owner is authorized
	if !thread.isAuthorized("U_OWNER") {
		t.Error("owner should be authorized")
	}

	// Other user is not
	if thread.isAuthorized("U_OTHER") {
		t.Error("other user should not be authorized")
	}

	// !open command from owner
	if !thread.handleCommand("U_OWNER", "!open") {
		t.Error("!open from owner should be handled")
	}
	if !thread.isAuthorized("U_OTHER") {
		t.Error("other user should be authorized after !open")
	}

	// !close from owner
	if !thread.handleCommand("U_OWNER", "!close") {
		t.Error("!close from owner should be handled")
	}
	if thread.isAuthorized("U_OTHER") {
		t.Error("other user should not be authorized after !close")
	}

	// !open from non-owner is ignored
	if thread.handleCommand("U_OTHER", "!open") {
		t.Error("!open from non-owner should not be handled")
	}
}

func TestThreadOpenAccess(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST", WithOpenAccess())
	if !thread.isAuthorized("U_ANYONE") {
		t.Error("anyone should be authorized with open access")
	}
}

func TestThreadNoOwner(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	if !thread.isAuthorized("U_ANYONE") {
		t.Error("anyone should be authorized with no owner set")
	}
}

func TestThreadStartAndResume(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")

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

	// Resume
	thread2 := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread2.Resume("1700000001.000000")
	if thread2.ThreadTS() != "1700000001.000000" {
		t.Errorf("ThreadTS = %q after Resume, want 1700000001.000000", thread2.ThreadTS())
	}
}

func TestThreadPost(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Start("Test")

	err := thread.Post("hello from bot")
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

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
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

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	// No Start or Resume — threadTS is empty

	err := thread.Post("should fail")
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

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
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

	thread := NewThread(mock.botClient(), "xoxb-test-token", "C_TEST", withAPIURL(mock.apiURL()))
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

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST", WithOwner("U_OWNER"))
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

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST", WithOwner("U_OWNER"))
	thread.Start("Test")
	threadTS := thread.ThreadTS()

	// Owner sends !open
	mock.injectReply("C_TEST", threadTS, "U_OWNER", "!open")

	// Other user sends a message
	mock.injectReply("C_TEST", threadTS, "U_OTHER", "hello!")

	replies, err := thread.PollReplies()
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}

	// !open is consumed as command, then U_OTHER's message comes through
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

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
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

func TestRepliesBlockingWithCancel(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST",
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

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST",
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

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST",
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

func TestAdvanceLastTS(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
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
