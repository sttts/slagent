// Package session orchestrates the pairplan planning session.
package session

import (
	"context"
	"fmt"
	"os/user"
	"strings"
	"sync"
	"time"

	"github.com/sttts/pairplan/pkg/claude"
	pslack "github.com/sttts/pairplan/pkg/slack"
	"github.com/sttts/pairplan/pkg/terminal"
)

// Config holds session configuration.
type Config struct {
	Topic          string
	Channel        string
	PermissionMode string
	SystemPrompt   string
}

// Session is a running pairplan planning session.
type Session struct {
	cfg   Config
	ui    *terminal.UI
	proc  *claude.Process
	slack *pslack.Client

	// Slack reply queue: replies collected between turns
	replyMu sync.Mutex
	replies []pslack.Reply
}

// Run starts and runs the planning session until the user quits.
func Run(ctx context.Context, cfg Config) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ui := terminal.New()
	sess := &Session{cfg: cfg, ui: ui}

	// Set up Slack if channel is specified
	if cfg.Channel != "" {
		slackClient, err := pslack.New(cfg.Channel)
		if err != nil {
			return fmt.Errorf("slack: %w", err)
		}
		sess.slack = slackClient
	}

	// Build system prompt with team feedback framing
	systemPrompt := cfg.SystemPrompt
	if sess.slack != nil {
		extra := "\n\nYou are in a collaborative planning session. " +
			"Messages prefixed with [Team feedback from Slack] contain input from team members " +
			"in a Slack thread. Consider their feedback and incorporate it into the plan."
		systemPrompt += extra
	}

	// Start Claude
	opts := []claude.Option{
		claude.WithPermissionMode(cfg.PermissionMode),
	}
	if systemPrompt != "" {
		opts = append(opts, claude.WithSystemPrompt(systemPrompt))
	}

	proc, err := claude.Start(ctx, opts...)
	if err != nil {
		return fmt.Errorf("start claude: %w", err)
	}
	sess.proc = proc
	defer proc.Stop()

	// Start Slack thread
	var threadURL string
	if sess.slack != nil {
		topic := cfg.Topic
		if topic == "" {
			topic = "Planning session"
		}
		url, err := sess.slack.StartThread(topic)
		if err != nil {
			return fmt.Errorf("start slack thread: %w", err)
		}
		threadURL = url
	}

	// Print banner
	ui.Banner(cfg.Topic, cfg.Channel, threadURL)

	// Start Slack poller
	if sess.slack != nil {
		go sess.pollSlack(ctx)
	}

	// Main loop: prompt → send → stream response → repeat
	for {
		// Get user input
		text, ok := ui.Prompt()
		if !ok {
			ui.Info("Session ended.")
			break
		}
		if text == "" {
			continue
		}

		// Handle special commands
		if handleCommand(text, ui, sess) {
			continue
		}

		// Display and mirror to Slack
		ui.UserMessage(text)
		if sess.slack != nil {
			username := currentUser()
			go sess.slack.PostUserMessage(username, text)
		}

		// Send to Claude
		if err := proc.Send(text); err != nil {
			ui.Error(fmt.Sprintf("send to claude: %v", err))
			break
		}

		// Read Claude's response
		if err := sess.readTurn(); err != nil {
			ui.Error(fmt.Sprintf("reading response: %v", err))
			break
		}

		// Inject any queued Slack replies
		sess.injectSlackReplies()
	}

	// Post session end to Slack
	if sess.slack != nil {
		sess.slack.PostSessionEnd()
	}

	return nil
}

// readTurn reads events from Claude until the turn ends (result event).
func (s *Session) readTurn() error {
	s.ui.StartResponse()
	var fullText strings.Builder
	thinkingShown := false

	for {
		evt, err := s.proc.ReadEvent()
		if err != nil {
			s.ui.EndResponse()
			return err
		}
		if evt == nil {
			s.ui.EndResponse()
			return fmt.Errorf("unexpected EOF from Claude")
		}

		switch evt.Type {
		case "text_delta":
			s.ui.StreamText(evt.Text)
			fullText.WriteString(evt.Text)

		case "thinking":
			if !thinkingShown {
				s.ui.Thinking()
				thinkingShown = true
			}

		case claude.TypeAssistant:
			// Complete message — we already streamed the text, but record it
			if fullText.Len() == 0 && evt.Text != "" {
				// No streaming happened, print the full text
				s.ui.StreamText(evt.Text)
				fullText.WriteString(evt.Text)
			}

		case "tool_use":
			s.ui.ToolActivity(evt.ToolName, summarizeToolInput(evt.ToolInput))
			if s.slack != nil {
				go s.slack.PostToolActivity(fmt.Sprintf("%s: %s", evt.ToolName, summarizeToolInput(evt.ToolInput)))
			}

		case claude.TypeResult:
			s.ui.EndResponse()

			// Post complete response to Slack
			text := fullText.String()
			if s.slack != nil && text != "" {
				go s.slack.PostClaudeMessage(text)
			}
			return nil

		case claude.TypeSystem:
			// Ignore system events (emitted at start of each turn)
		}
	}
}

// injectSlackReplies sends any queued Slack replies to Claude.
func (s *Session) injectSlackReplies() {
	s.replyMu.Lock()
	replies := s.replies
	s.replies = nil
	s.replyMu.Unlock()

	if len(replies) == 0 {
		return
	}

	// Show in terminal
	for _, r := range replies {
		s.ui.SlackMessage(r.User, r.Text)
	}

	// Format as a single message for Claude
	var sb strings.Builder
	sb.WriteString("[Team feedback from Slack thread]\n")
	for _, r := range replies {
		fmt.Fprintf(&sb, "@%s: %s\n", r.User, r.Text)
	}

	if err := s.proc.Send(sb.String()); err != nil {
		s.ui.Error(fmt.Sprintf("inject slack replies: %v", err))
		return
	}

	// Read Claude's response to the feedback
	if err := s.readTurn(); err != nil {
		s.ui.Error(fmt.Sprintf("reading response to slack feedback: %v", err))
	}
}

// pollSlack continuously polls for new Slack thread replies.
func (s *Session) pollSlack(ctx context.Context) {
	ticker := time.NewTicker(pslack.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			replies, err := s.slack.PollReplies()
			if err != nil {
				// Silently ignore poll errors
				continue
			}
			if len(replies) > 0 {
				s.replyMu.Lock()
				s.replies = append(s.replies, replies...)
				s.replyMu.Unlock()
			}
		}
	}
}

// handleCommand processes special /commands. Returns true if handled.
func handleCommand(text string, ui *terminal.UI, sess *Session) bool {
	switch {
	case text == "/quit" || text == "/exit":
		ui.Info("Use Ctrl-D or Ctrl-C to exit.")
		return true
	case text == "/status":
		ui.Info(fmt.Sprintf("Session ID: %s", sess.proc.SessionID()))
		if sess.slack != nil {
			ui.Info(fmt.Sprintf("Slack thread: %s", sess.slack.ThreadTS()))
		}
		return true
	}
	return false
}

func currentUser() string {
	u, err := user.Current()
	if err != nil {
		return "developer"
	}
	return u.Username
}

func summarizeToolInput(input string) string {
	if len(input) > 80 {
		return input[:77] + "..."
	}
	return input
}
