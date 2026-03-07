package slagent

import (
	"context"
	"fmt"
	"time"

	slackapi "github.com/slack-go/slack"
)

// Replies returns new replies since the last call, filtering by permissions.
// It blocks until ctx is cancelled or replies arrive, polling at the configured interval.
func (t *Thread) Replies(ctx context.Context) ([]Reply, error) {
	timer := time.NewTimer(0) // fire immediately on first poll
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}

		replies, err := t.pollOnce()
		if err != nil {
			return nil, err
		}
		if len(replies) > 0 {
			return replies, nil
		}
		timer.Reset(t.config.pollInterval)
	}
}

// PollReplies fetches new replies without blocking (single poll).
func (t *Thread) PollReplies() ([]Reply, error) {
	return t.pollOnce()
}

// advanceLastTS updates lastTS if ts is newer. Must be called without lock held.
func (t *Thread) advanceLastTS(ts string) {
	t.mu.Lock()
	if ts > t.lastTS {
		t.lastTS = ts
	}
	t.mu.Unlock()
}

// pollOnce fetches new replies from the thread, filtering by permissions and own messages.
func (t *Thread) pollOnce() ([]Reply, error) {
	t.mu.Lock()
	threadTS := t.threadTS
	oldest := t.lastTS
	t.mu.Unlock()

	if threadTS == "" {
		return nil, nil
	}

	params := &slackapi.GetConversationRepliesParameters{
		ChannelID: t.channel,
		Timestamp: threadTS,
		Oldest:    oldest,
	}
	msgs, _, _, err := t.api.GetConversationReplies(params)
	if err != nil {
		return nil, fmt.Errorf("get replies: %w", err)
	}

	var replies []Reply
	for _, msg := range msgs {
		// Skip parent and already-seen messages
		if msg.Timestamp == threadTS || msg.Timestamp <= oldest {
			continue
		}

		// Skip messages we posted
		t.mu.Lock()
		ours := t.postedTS[msg.Timestamp]
		t.mu.Unlock()
		if ours {
			t.advanceLastTS(msg.Timestamp)
			continue
		}

		// Skip bot messages
		if msg.BotID != "" {
			t.advanceLastTS(msg.Timestamp)
			continue
		}

		// Handle !open / !close commands
		if t.handleCommand(msg.User, msg.Text) {
			t.advanceLastTS(msg.Timestamp)
			continue
		}

		// Check authorization
		if !t.isAuthorized(msg.User) {
			t.advanceLastTS(msg.Timestamp)
			continue
		}

		user := t.resolveUser(msg.User)
		replies = append(replies, Reply{
			User:   user,
			UserID: msg.User,
			Text:   msg.Text,
		})
		t.advanceLastTS(msg.Timestamp)
	}

	return replies, nil
}
