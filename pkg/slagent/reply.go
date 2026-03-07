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
	for {
		replies, err := t.pollOnce()
		if err != nil {
			return nil, err
		}
		if len(replies) > 0 {
			return replies, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(t.config.pollInterval):
		}
	}
}

// PollReplies fetches new replies without blocking (single poll).
func (t *Thread) PollReplies() ([]Reply, error) {
	return t.pollOnce()
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
			t.mu.Lock()
			if msg.Timestamp > t.lastTS {
				t.lastTS = msg.Timestamp
			}
			t.mu.Unlock()
			continue
		}

		// Skip bot messages
		if msg.BotID != "" {
			t.mu.Lock()
			if msg.Timestamp > t.lastTS {
				t.lastTS = msg.Timestamp
			}
			t.mu.Unlock()
			continue
		}

		// Handle !open / !close commands
		if t.handleCommand(msg.User, msg.Text) {
			t.mu.Lock()
			if msg.Timestamp > t.lastTS {
				t.lastTS = msg.Timestamp
			}
			t.mu.Unlock()
			continue
		}

		// Check authorization
		if !t.isAuthorized(msg.User) {
			t.mu.Lock()
			if msg.Timestamp > t.lastTS {
				t.lastTS = msg.Timestamp
			}
			t.mu.Unlock()
			continue
		}

		user := t.resolveUser(msg.User)
		replies = append(replies, Reply{
			User:   user,
			UserID: msg.User,
			Text:   msg.Text,
		})

		t.mu.Lock()
		if msg.Timestamp > t.lastTS {
			t.lastTS = msg.Timestamp
		}
		t.mu.Unlock()
	}

	return replies, nil
}
