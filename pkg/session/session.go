// Package session orchestrates the pairplan planning session.
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sttts/pairplan/pkg/claude"
	"github.com/sttts/pairplan/pkg/slagent"
	pslack "github.com/sttts/pairplan/pkg/slack"
	"github.com/sttts/pairplan/pkg/terminal"
)

// Config holds session configuration.
type Config struct {
	Topic           string
	Channel         string
	ChannelName     string // display name (e.g. "#general" or "@haarchri")
	PermissionMode  string
	SystemPrompt    string
	ResumeSessionID string // Claude session ID to resume
	ResumeThreadTS  string // Slack thread timestamp to resume
}

// ResumeInfo is returned by Run so the caller can print a resume command.
type ResumeInfo struct {
	SessionID string
	Channel   string
	ThreadTS  string
}

// Session is a running pairplan planning session.
type Session struct {
	cfg    Config
	ui     *terminal.UI
	proc   *claude.Process
	thread *slagent.Thread

	// Slack reply queue: replies collected between turns
	replyMu     sync.Mutex
	replies     []slagent.Reply
	replyNotify chan struct{} // signaled when new replies arrive
}

// Run starts and runs the planning session until the user quits.
// Returns ResumeInfo so the caller can print a resume command.
func Run(ctx context.Context, cfg Config) (*ResumeInfo, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ui := terminal.New()
	sess := &Session{
		cfg:         cfg,
		ui:          ui,
		replyNotify: make(chan struct{}, 1),
	}

	// Set up Slack if channel is specified
	if cfg.Channel != "" {
		creds, err := pslack.LoadCredentials()
		if err != nil {
			return nil, fmt.Errorf("slack credentials: %w", err)
		}
		client := slagent.NewSlackClient(creds.EffectiveToken(), creds.Cookie)

		// Resolve own user ID for @ mentions and thread ownership
		var opts []slagent.ThreadOption
		resp, err := client.AuthTest()
		if err == nil && resp.UserID != "" {
			opts = append(opts, slagent.WithOwner(resp.UserID))
		}

		sess.thread = slagent.NewThread(client, creds.EffectiveToken(), cfg.Channel, opts...)
	}

	// Build system prompt with team feedback framing
	systemPrompt := cfg.SystemPrompt
	if sess.thread != nil {
		extra := "\n\nYou are in a collaborative planning session. " +
			"Messages prefixed with [Team feedback from Slack] contain input from team members " +
			"in a Slack thread. Consider their feedback and incorporate it into the plan."
		systemPrompt += extra
	}

	// Start Claude (with resume if specified)
	opts := []claude.Option{
		claude.WithPermissionMode(cfg.PermissionMode),
	}
	if systemPrompt != "" {
		opts = append(opts, claude.WithSystemPrompt(systemPrompt))
	}
	if cfg.ResumeSessionID != "" {
		opts = append(opts, claude.WithResume(cfg.ResumeSessionID))
	}

	proc, err := claude.Start(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}
	sess.proc = proc
	defer proc.Stop()

	// Resume or start Slack thread
	var threadURL string
	if sess.thread != nil {
		if cfg.ResumeThreadTS != "" {
			sess.thread.Resume(cfg.ResumeThreadTS)
			threadURL = "(resumed)"
		} else {
			url, err := sess.thread.Start(cfg.Topic)
			if err != nil {
				return nil, fmt.Errorf("start slack thread: %w", err)
			}
			threadURL = url
		}
	}

	// Hide cursor during session, restore on exit
	ui.HideCursor()
	defer ui.ShowCursor()

	// Print banner
	channelDisplay := cfg.ChannelName
	if channelDisplay == "" {
		channelDisplay = cfg.Channel
	}
	ui.Banner(cfg.Topic, channelDisplay, threadURL)

	// Send initial topic (skip on resume — Claude already has context)
	if cfg.ResumeSessionID == "" {
		if sess.thread != nil {
			username := currentUser()
			sess.thread.PostUser(username, cfg.Topic)
		}
		if err := proc.Send(cfg.Topic); err != nil {
			return nil, fmt.Errorf("send topic: %w", err)
		}
		if err := sess.readTurn(); err != nil {
			return nil, fmt.Errorf("reading initial response: %w", err)
		}
	}

	// Without Slack, we're done after the initial response
	if sess.thread == nil {
		return nil, nil
	}

	// Start Slack poller
	go sess.pollSlack(ctx)

	// Auto-inject Slack feedback loop
	for {
		ui.Info("⏳ Waiting for team feedback from Slack...")
		replies, ok := sess.waitForReplies(ctx)
		if !ok {
			break
		}

		// Show in terminal
		for _, r := range replies {
			ui.SlackMessage(r.User, r.Text)
		}

		// Format and send to Claude
		var sb strings.Builder
		sb.WriteString("[Team feedback from Slack thread]\n")
		for _, r := range replies {
			fmt.Fprintf(&sb, "@%s: %s\n", r.User, r.Text)
		}

		if err := proc.Send(sb.String()); err != nil {
			ui.Error(fmt.Sprintf("send to claude: %v", err))
			break
		}
		if err := sess.readTurn(); err != nil {
			ui.Error(fmt.Sprintf("reading response: %v", err))
			break
		}
	}

	ui.Info("👋 Session ended.")

	// Build resume info
	resume := &ResumeInfo{
		SessionID: proc.SessionID(),
		Channel:   cfg.Channel,
		ThreadTS:  sess.thread.ThreadTS(),
	}

	return resume, nil
}

// readTurn reads events from Claude until the turn ends (result event).
func (s *Session) readTurn() error {
	s.ui.StartResponse()
	var fullText strings.Builder
	toolSeq := 0
	lastToolID := ""
	lastToolName := ""
	lastToolDetail := ""

	// Set up slagent turn for Slack streaming
	var turn slagent.Turn
	if s.thread != nil {
		turn = s.thread.NewTurn()
	}

	// finishTool marks the last tool as done in Slack.
	finishTool := func() {
		if lastToolID != "" && turn != nil {
			turn.Tool(lastToolID, lastToolName, slagent.ToolDone, lastToolDetail)
			lastToolID = ""
		}
	}

	for {
		evt, err := s.proc.ReadEvent()
		if err != nil {
			if turn != nil {
				turn.Finish()
			}
			s.ui.EndResponse()
			return err
		}
		if evt == nil {
			if turn != nil {
				turn.Finish()
			}
			s.ui.EndResponse()
			return fmt.Errorf("unexpected EOF from Claude")
		}

		switch evt.Type {
		case "text_delta":
			s.ui.StreamText(evt.Text)
			fullText.WriteString(evt.Text)

			// Stream delta to Slack
			if turn != nil {
				turn.Text(evt.Text)
			}

		case "thinking":
			s.ui.Thinking(evt.Text)

			// Stream thinking to Slack
			if turn != nil {
				turn.Thinking(evt.Text)
			}

		case claude.TypeAssistant:
			// Complete message — we already streamed the text, but record it
			if fullText.Len() == 0 && evt.Text != "" {
				s.ui.StreamText(evt.Text)
				fullText.WriteString(evt.Text)
				if turn != nil {
					turn.Text(evt.Text)
				}
			}

		case "tool_use":
			finishTool()
			toolSeq++
			lastToolID = fmt.Sprintf("t%d", toolSeq)
			lastToolName = evt.ToolName
			lastToolDetail = toolDetail(evt.ToolName, evt.ToolInput)
			s.ui.ToolActivity(formatTool(evt.ToolName, evt.ToolInput))

			if turn != nil {
				if p := interactivePrompt(evt.ToolName, evt.ToolInput, s.thread.OwnerID()); p != nil {
					// Post interactive tools with reaction emojis for response
					s.thread.PostPrompt(p.text, p.reactions)
				} else {
					turn.Tool(lastToolID, evt.ToolName, slagent.ToolRunning, lastToolDetail)
				}
			}

		case claude.TypeResult:
			finishTool()
			s.ui.EndResponse()
			if turn != nil {
				turn.Finish()
			}
			return nil

		case claude.TypeSystem:
			// New turn — previous tool (if any) has completed
			finishTool()
		}
	}
}

// waitForReplies blocks until Slack replies are available or context is cancelled.
func (s *Session) waitForReplies(ctx context.Context) ([]slagent.Reply, bool) {
	select {
	case <-ctx.Done():
		return nil, false
	case <-s.replyNotify:
		s.replyMu.Lock()
		replies := s.replies
		s.replies = nil
		s.replyMu.Unlock()
		return replies, true
	}
}

// pollSlack continuously polls for new Slack thread replies.
func (s *Session) pollSlack(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			replies, err := s.thread.PollReplies()
			if err != nil {
				continue
			}
			if len(replies) > 0 {
				s.replyMu.Lock()
				s.replies = append(s.replies, replies...)
				s.replyMu.Unlock()

				// Signal that replies are available
				select {
				case s.replyNotify <- struct{}{}:
				default:
				}
			}
		}
	}
}

func currentUser() string {
	u, err := user.Current()
	if err != nil {
		return "developer"
	}
	return u.Username
}

// promptMsg holds a Slack message with reaction emojis for interactive response.
type promptMsg struct {
	text      string
	reactions []string
}

// Number emoji reaction names for multi-choice options.
var numberReactions = []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}

// interactivePrompt returns a formatted Slack prompt with reactions for interactive tools,
// or nil if the tool is not interactive.
func interactivePrompt(toolName, rawInput, ownerID string) *promptMsg {
	var input map[string]interface{}
	json.Unmarshal([]byte(rawInput), &input)

	str := func(key string) string {
		if v, ok := input[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}

	mention := ""
	if ownerID != "" {
		mention = fmt.Sprintf(" <@%s>", ownerID)
	}

	switch toolName {
	case "ExitPlanMode":
		return &promptMsg{
			text:      fmt.Sprintf("🗳️ *Claude wants to exit plan mode.*%s", mention),
			reactions: []string{"white_check_mark", "x"},
		}
	case "EnterPlanMode":
		return &promptMsg{
			text:      fmt.Sprintf("🗳️ *Claude wants to enter plan mode.*%s", mention),
			reactions: []string{"white_check_mark", "x"},
		}
	case "AskUserQuestion":
		q := str("question")
		if q == "" {
			q = "Claude has a question."
		}

		// Check for allowedPrompts (multiple choice options)
		if raw, ok := input["allowedPrompts"]; ok {
			if arr, ok := raw.([]interface{}); ok && len(arr) > 0 {
				var lines []string
				var reactions []string
				lines = append(lines, fmt.Sprintf("❓ *Claude asks:*%s\n%s\n", mention, q))
				for i, opt := range arr {
					if i >= len(numberReactions) {
						break
					}
					label, _ := opt.(string)
					lines = append(lines, fmt.Sprintf("%s  %s", numberEmoji(i), label))
					reactions = append(reactions, numberReactions[i])
				}
				return &promptMsg{
					text:      strings.Join(lines, "\n"),
					reactions: reactions,
				}
			}
		}

		return &promptMsg{
			text:      fmt.Sprintf("❓ *Claude asks:*%s\n%s", mention, q),
			reactions: []string{"white_check_mark", "x"},
		}
	}
	return nil
}

// numberEmoji returns a display emoji for an index (0-based).
func numberEmoji(i int) string {
	emojis := []string{"1️⃣", "2️⃣", "3️⃣", "4️⃣", "5️⃣", "6️⃣", "7️⃣", "8️⃣", "9️⃣"}
	if i < len(emojis) {
		return emojis[i]
	}
	return fmt.Sprintf("%d.", i+1)
}

// toolDetail extracts the raw detail string for slagent (no emoji).
func toolDetail(toolName, rawInput string) string {
	var input map[string]interface{}
	json.Unmarshal([]byte(rawInput), &input)

	str := func(key string) string {
		if v, ok := input[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}

	switch toolName {
	case "Read":
		return filepath.Base(str("file_path"))
	case "Write":
		return filepath.Base(str("file_path"))
	case "Edit":
		return filepath.Base(str("file_path"))
	case "Glob":
		return str("pattern")
	case "Grep":
		p := str("pattern")
		if path := str("path"); path != "" {
			return p + " in " + filepath.Base(path)
		}
		return p
	case "Bash":
		cmd := str("command")
		if len(cmd) > 60 {
			cmd = cmd[:57] + "..."
		}
		return cmd
	case "Agent":
		if d := str("description"); d != "" {
			return d
		}
		p := str("prompt")
		if len(p) > 60 {
			p = p[:57] + "..."
		}
		return p
	case "WebFetch":
		return str("url")
	case "WebSearch":
		return str("query")
	case "ExitPlanMode":
		return "ready for approval"
	case "EnterPlanMode":
		return "switching to plan mode"
	case "AskUserQuestion":
		return str("question")
	case "TodoWrite", "TaskCreate", "TaskUpdate":
		return str("subject")
	default:
		s := rawInput
		if len(s) > 60 {
			s = s[:57] + "..."
		}
		return s
	}
}

// formatTool returns a pretty one-line summary of a tool use.
func formatTool(toolName, rawInput string) string {
	var input map[string]interface{}
	json.Unmarshal([]byte(rawInput), &input)

	str := func(key string) string {
		if v, ok := input[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}

	switch toolName {
	case "Read":
		return fmt.Sprintf("📄 %s", filepath.Base(str("file_path")))
	case "Write":
		return fmt.Sprintf("✏️  %s (new)", filepath.Base(str("file_path")))
	case "Edit":
		return fmt.Sprintf("✏️  %s", filepath.Base(str("file_path")))
	case "Glob":
		return fmt.Sprintf("🔍 %s", str("pattern"))
	case "Grep":
		p := str("pattern")
		if path := str("path"); path != "" {
			return fmt.Sprintf("🔍 %s in %s", p, filepath.Base(path))
		}
		return fmt.Sprintf("🔍 %s", p)
	case "Bash":
		cmd := str("command")
		if len(cmd) > 60 {
			cmd = cmd[:57] + "..."
		}
		return fmt.Sprintf("💻 %s", cmd)
	case "Agent":
		if d := str("description"); d != "" {
			return fmt.Sprintf("🤖 %s", d)
		}
		p := str("prompt")
		if len(p) > 60 {
			p = p[:57] + "..."
		}
		return fmt.Sprintf("🤖 %s", p)
	case "WebFetch":
		return fmt.Sprintf("🌐 %s", str("url"))
	case "WebSearch":
		return fmt.Sprintf("🔎 %s", str("query"))
	case "TodoWrite", "TaskCreate", "TaskUpdate":
		return fmt.Sprintf("📋 %s", str("subject"))
	case "ExitPlanMode":
		return "📋 ready for approval"
	case "EnterPlanMode":
		return "📋 switching to plan mode"
	case "AskUserQuestion":
		return fmt.Sprintf("❓ %s", str("question"))
	default:
		summary := rawInput
		if len(summary) > 60 {
			summary = summary[:57] + "..."
		}
		return fmt.Sprintf("%s: %s", toolName, summary)
	}
}
