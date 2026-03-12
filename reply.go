package slagent

import (
	"context"
	"fmt"
	"strings"
	"time"

	slackapi "github.com/slack-go/slack"
)

// Replies returns new messages since the last call, filtering by permissions.
// It blocks until ctx is cancelled or messages arrive, polling at the configured interval.
func (t *Thread) Replies(ctx context.Context) ([]Message, error) {
	timer := time.NewTimer(0) // fire immediately on first poll
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}

		msgs, err := t.pollOnce()
		if err != nil {
			return nil, err
		}
		if len(msgs) > 0 {
			return msgs, nil
		}
		timer.Reset(t.config.pollInterval)
	}
}

// PollReplies fetches new messages without blocking (single poll).
func (t *Thread) PollReplies() ([]Message, error) {
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

// pollOnce fetches new messages from the thread, filtering by permissions and own messages.
// Messages posted by slagent are identified by block_id and skipped.
func (t *Thread) pollOnce() ([]Message, error) {
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
	slackMsgs, _, _, err := t.client.GetConversationReplies(params)
	if err != nil {
		return nil, fmt.Errorf("get replies: %w", err)
	}

	var messages []Message
	for _, msg := range slackMsgs {
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

			// Other instances' finalized text — only deliver when agent sees all messages
			visible := t.OpenAccess() || t.Observe() || t.OwnerID() == ""
			if !visible {
				t.advanceLastTS(msg.Timestamp)
				continue
			}

			// Deliver as observe-only so Claude reads but doesn't respond
			user := t.resolveUser(msg.User)
			messages = append(messages, TextMessage{
				User:    user,
				UserID:  msg.User,
				Text:    msg.Text,
				Observe: true,
			})
			t.advanceLastTS(msg.Timestamp)
			continue
		case blockNone:
			// Not a slagent message — skip bot messages (webhooks, integrations)
			if msg.BotID != "" {
				t.advanceLastTS(msg.Timestamp)
				continue
			}
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
			if targeted && targetID != t.instanceID {
				t.advanceLastTS(msg.Timestamp)
				continue
			}
			user := t.resolveUser(msg.User)
			messages = append(messages, StopMessage{User: user, UserID: msg.User})
			t.advanceLastTS(msg.Timestamp)
			continue
		}

		// "quit" terminates the session — owner only
		if strings.EqualFold(text, "quit") {
			if targeted && targetID != t.instanceID {
				t.advanceLastTS(msg.Timestamp)
				continue
			}
			if msg.User != t.OwnerID() {
				t.PostEphemeral(msg.User, t.emoji+" 🚫 Only the session owner can quit.")
				t.advanceLastTS(msg.Timestamp)
				continue
			}
			user := t.resolveUser(msg.User)
			messages = append(messages, QuitMessage{User: user, UserID: msg.User})
			t.advanceLastTS(msg.Timestamp)
			continue
		}

		// Detect near-miss targeting (wrong syntax) and give ephemeral feedback
		if !targeted {
			if hint := mistargeted(msg.Text); hint != "" {
				t.PostEphemeral(msg.User, hint)
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

			// /sandbox — signal sandbox toggle request for Session to handle
			if strings.HasPrefix(rest, "/sandbox") {
				if msg.User != t.OwnerID() {
					t.PostEphemeral(msg.User, t.emoji+" 🚫 Only the thread owner can change sandbox settings.")
					t.advanceLastTS(msg.Timestamp)
					continue
				}
				user := t.resolveUser(msg.User)
				messages = append(messages, SandboxToggle{User: user, UserID: msg.User})
				t.advanceLastTS(msg.Timestamp)
				continue
			}

			// Handle slaude commands (/open, /lock, /close)
			handled, feedback := t.handleCommand(msg.User, rest)
			if feedback != "" {
				t.Post(t.emoji + " " + feedback)
			}
			if handled {
				t.advanceLastTS(msg.Timestamp)
				continue
			}

			// Unknown command — forward to Claude
			if !t.IsAuthorized(msg.User) {
				if t.joined {
					t.refreshTitle()
				}
				if !t.IsAuthorized(msg.User) {
					t.PostEphemeral(msg.User, t.emoji+" 🚫 Not authorized.")
					t.advanceLastTS(msg.Timestamp)
					continue
				}
			}
			t.maybeWelcome(msg.User)
			user := t.resolveUser(msg.User)
			messages = append(messages, CommandMessage{
				User:    user,
				UserID:  msg.User,
				Command: rest,
			})
			t.advanceLastTS(msg.Timestamp)
			continue
		}

		// Check authorization — re-read title if joined and initially denied
		authorized := t.IsAuthorized(msg.User)
		if !authorized && t.joined {
			t.refreshTitle()
			authorized = t.IsAuthorized(msg.User)
		}

		if !authorized {
			// Tell the user they're not authorized when they try to interact directly
			if targeted && targetID == t.instanceID {
				t.PostEphemeral(msg.User, t.emoji+" 🚫 Not authorized. Ask the thread owner to `/open`.")
			} else if !targeted && isMistargetedToUs(msg.Text, t.instanceID) {
				t.PostEphemeral(msg.User, t.emoji+" 🚫 Not authorized. Ask the thread owner to `/open`.")
			}

			// In observe mode, still deliver for passive learning
			if t.IsVisible(msg.User) {
				user := t.resolveUser(msg.User)
				messages = append(messages, TextMessage{
					User:    user,
					UserID:  msg.User,
					Text:    msg.Text,
					Observe: true,
				})
			}
			t.advanceLastTS(msg.Timestamp)
			continue
		}

		// Welcome first-time non-owner users
		t.maybeWelcome(msg.User)

		// Non-command messages are delivered to all instances.
		// Keep original text so Claude sees the :shortcode:: prefix
		// and knows who the message is meant for.
		user := t.resolveUser(msg.User)
		messages = append(messages, TextMessage{
			User:   user,
			UserID: msg.User,
			Text:   msg.Text,
		})
		t.advanceLastTS(msg.Timestamp)
	}

	return messages, nil
}
