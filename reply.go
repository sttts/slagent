package slagent

import (
	"context"
	"fmt"
	"strings"
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
// Messages posted by slagent are identified by block_id and skipped.
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

		// Classify slagent blocks by kind and source instance
		kind, sourceID := classifyBlocks(msg.Blocks)
		switch kind {
		case blockActivity:
			// Activity messages: always skip from all instances
			t.advanceLastTS(msg.Timestamp)
			continue
		case blockStreaming:
			// Streaming text: not finalized yet, skip (don't advance — re-check next poll)
			continue
		case blockFinal:
			if sourceID == t.instanceID {
				// Own finalized messages — skip
				t.advanceLastTS(msg.Timestamp)
				continue
			}
			// Other instances' finalized text — deliver so agent perceives it
		}

		// Skip bot messages
		if msg.BotID != "" {
			t.advanceLastTS(msg.Timestamp)
			continue
		}

		// Parse :shortcode:: prefix targeting
		targetID, rest, targeted := parseMessage(msg.Text)

		// "help" and "stop" bypass authorization — anyone can use them
		text := strings.TrimSpace(msg.Text)
		if targeted {
			text = strings.TrimSpace(rest)
		}

		if strings.EqualFold(text, "help") {
			if targeted && targetID != t.instanceID {
				t.advanceLastTS(msg.Timestamp)
				continue
			}
			t.Post(t.helpText())
			t.advanceLastTS(msg.Timestamp)
			continue
		}
		if strings.EqualFold(text, "stop") {
			// Targeted stop is instance-exclusive
			if targeted && targetID != t.instanceID {
				t.advanceLastTS(msg.Timestamp)
				continue
			}
			user := t.resolveUser(msg.User)
			replies = append(replies, Reply{User: user, UserID: msg.User, Stop: true})
			t.advanceLastTS(msg.Timestamp)
			continue
		}

		// Detect near-miss targeting (wrong syntax) and give feedback
		if !targeted {
			if hint := mistargeted(msg.Text); hint != "" {
				t.Post(hint)
				t.advanceLastTS(msg.Timestamp)
				continue
			}
		}

		if targeted && strings.HasPrefix(rest, "/") {
			// Commands are instance-exclusive
			if targetID != t.instanceID {
				t.advanceLastTS(msg.Timestamp)
				continue
			}

			// Handle slaude commands (/open, /lock, /close)
			handled, feedback := t.handleCommand(msg.User, rest)
			if feedback != "" {
				t.Post(feedback)
			}
			if handled {
				t.advanceLastTS(msg.Timestamp)
				continue
			}

			// Unknown command — forward to Claude
			if !t.isAuthorized(msg.User) {
				t.Post("🚫 Not authorized.")
				t.advanceLastTS(msg.Timestamp)
				continue
			}
			user := t.resolveUser(msg.User)
			replies = append(replies, Reply{
				User:    user,
				UserID:  msg.User,
				Command: rest,
			})
			t.advanceLastTS(msg.Timestamp)
			continue
		}

		// Check authorization
		if !t.isAuthorized(msg.User) {
			t.Post("🚫 Not authorized. Ask the thread owner to `/open`.")
			t.advanceLastTS(msg.Timestamp)
			continue
		}

		// Non-command messages are delivered to all instances.
		// Keep original text so Claude sees the :shortcode:: prefix
		// and knows who the message is meant for.
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
