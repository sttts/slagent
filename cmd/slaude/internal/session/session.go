// Package session orchestrates the slaude planning session.
package session

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/sttts/slagent"
	"github.com/sttts/slagent/cmd/slaude/internal/claude"
	"github.com/sttts/slagent/cmd/slaude/internal/perms"
	"github.com/sttts/slagent/cmd/slaude/internal/terminal"
)

// Config holds session configuration.
type Config struct {
	Topic          string
	Channel        string
	ChannelName    string   // display name (e.g. "#general" or "@haarchri")
	ResumeThreadTS string   // Slack thread timestamp to resume
	ResumeAfterTS  string   // skip messages up to this timestamp on resume
	InstanceID     string   // slagent instance ID (for resume; empty = generate new)
	OpenAccess     bool     // start with thread open for all participants
	ClosedAccess   bool     // override inherited access to locked (join/resume)
	Debug          bool     // write raw JSON events to debug.log
	NoBye          bool     // don't post goodbye message on exit
	Workspace      string   // Slack workspace (empty = default)
	ClaudeArgs     []string // pass-through args for Claude subprocess

	// AI-based permission auto-approve settings
	DangerousAutoApprove        string // "never", "green", "yellow"
	DangerousAutoApproveNetwork string // "never", "known", "any"
}

// ResumeInfo is returned by Run so the caller can print a resume command.
type ResumeInfo struct {
	SessionID  string
	Channel    string
	ThreadTS   string
	ThreadURL  string // Slack permalink (empty if unavailable)
	InstanceID string
	LastTS     string // cursor: last seen message timestamp
}

// Session is a running slaude session.
type Session struct {
	cfg      Config
	ctx      context.Context    // session lifetime context
	ui       *terminal.UI
	proc     *claude.Process
	thread   *slagent.Thread
	debugLog *os.File // debug.log file (nil when --debug is off)
	slackLog *os.File // slack.log file (nil when --debug is off)

	cancel    context.CancelFunc // cancels the session context
	slackUser string             // Slack identity for banner (e.g. "@user on team")

	// Slack reply queue: replies collected between turns
	replyMu     sync.Mutex
	replies     []slagent.Reply
	replyNotify chan struct{} // signaled when new replies arrive
	stopNotify  chan struct{} // signaled when a "stop" message arrives

	// Task tracking: TodoWrite state mirrored to Slack
	todos   []todo
	todosTS string // Slack message timestamp for the tasks message

	// Known-safe network destinations (for auto-approve with "known" level)
	knownHosts *knownHostSet

	// Silent turn suppression: stop showing thinking activity after N silent turns
	silentTurnsLeft int // decremented on silent turns, reset on output
}

// todo is a single item from Claude's TodoWrite tool.
type todo struct {
	Content string `json:"content"`
	Status  string `json:"status"` // pending, in_progress, completed
}

// Run starts and runs the planning session until the user quits.
// Returns ResumeInfo so the caller can print a resume command.
func Run(ctx context.Context, cfg Config) (*ResumeInfo, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Terminal output: stdout, or tee to terminal.log in debug mode
	var ui *terminal.UI
	if cfg.Debug {
		termLog, err := os.Create("terminal.log")
		if err != nil {
			return nil, fmt.Errorf("create terminal.log: %w", err)
		}
		defer termLog.Close()
		ui = terminal.NewWithWriter(io.MultiWriter(os.Stdout, termLog))
	} else {
		ui = terminal.New()
	}

	sess := &Session{
		cfg:         cfg,
		ctx:         ctx,
		ui:          ui,
		cancel:      cancel,
		replyNotify: make(chan struct{}, 1),
		stopNotify:  make(chan struct{}, 1),
		knownHosts:      loadKnownHosts(),
		silentTurnsLeft: 3,
	}

	// Open debug log
	if cfg.Debug {
		f, err := os.Create("debug.log")
		if err != nil {
			return nil, fmt.Errorf("create debug.log: %w", err)
		}
		defer f.Close()
		sess.debugLog = f
		ui.Info("📝 Debug logs: debug.log, slack.log, terminal.log")
	}

	// Set up Slack if channel is specified
	if cfg.Channel != "" {
		if err := sess.connectSlack(); err != nil {
			return nil, err
		}
		if sess.slackLog != nil {
			defer sess.slackLog.Close()
		}
	}

	extraArgs := sess.buildExtraArgs()

	// Start permission listener for Slack-based tool approval
	if sess.thread != nil {
		slaudeBin, _ := os.Executable()
		permListener, err := perms.NewListener(sess.handlePermission)
		if err != nil {
			return nil, fmt.Errorf("start permission listener: %w", err)
		}
		permListener.Start()
		defer permListener.Stop()

		// Write MCP config to temp file for Claude's --mcp-config flag
		mcpCfgFile, err := permListener.MCPConfigFile(slaudeBin)
		if err != nil {
			return nil, fmt.Errorf("write mcp config: %w", err)
		}
		defer os.Remove(mcpCfgFile)

		if cfg.Debug {
			mcpCfgContent, _ := os.ReadFile(mcpCfgFile)
			ui.Info(fmt.Sprintf("📝 MCP config: %s → %s", mcpCfgFile, string(mcpCfgContent)))
			ui.Info(fmt.Sprintf("📝 Permission tool: %s", perms.PermissionToolRef()))
		}

		extraArgs = append(extraArgs,
			"--mcp-config", mcpCfgFile,
			"--permission-prompt-tool", perms.PermissionToolRef(),
		)
	}

	// Start Claude with pass-through args
	var claudeOpts []claude.Option
	claudeOpts = append(claudeOpts, claude.WithExtraArgs(extraArgs))

	// In debug mode, tee Claude's stderr to claude-stderr.log
	if cfg.Debug {
		stderrLog, err := os.Create("claude-stderr.log")
		if err != nil {
			return nil, fmt.Errorf("create claude-stderr.log: %w", err)
		}
		defer stderrLog.Close()
		claudeOpts = append(claudeOpts, claude.WithStderr(io.MultiWriter(os.Stderr, stderrLog)))
		ui.Info("📝 Claude stderr: claude-stderr.log")
	}

	proc, err := claude.Start(ctx, claudeOpts...)
	if err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}
	sess.proc = proc
	defer proc.Stop()

	// Resume or start Slack thread
	if sess.thread != nil {
		if err := sess.startThread(); err != nil {
			return nil, err
		}
		// Register session state so 'slaude ps' and 'slaude kill' can find it.
		pid := os.Getpid()
		_ = RegisterSession(SessionState{
			PID:        pid,
			Emoji:      sess.thread.Emoji(),
			InstanceID: sess.thread.InstanceID(),
			Channel:    cfg.ChannelName,
			ThreadURL:  sess.thread.URL(),
			Workspace:  cfg.Workspace,
			StartedAt:  time.Now().Unix(),
		})
		defer UnregisterSession(pid)
	}

	// Hide cursor during session, restore on exit
	ui.HideCursor()
	defer ui.ShowCursor()

	sess.banner()

	// Send initial topic (skip on resume or if no topic given)
	if err := sess.initialTurn(); err != nil {
		return nil, err
	}

	// Without Slack, we're done after the initial response
	if sess.thread == nil {
		return nil, nil
	}

	// Slack feedback loop until session ends
	go sess.pollSlack(ctx)
	sess.feedbackLoop(ctx)

	ui.Info("👋 Session ended.")
	if !cfg.NoBye {
		sess.thread.Post(fmt.Sprintf("%s 👋 Session ended.", sess.thread.Emoji()))
	}

	return sess.resumeInfo(), nil
}

// banner prints the session banner to the terminal.
func (s *Session) banner() {
	channelDisplay := s.cfg.ChannelName
	if channelDisplay == "" {
		channelDisplay = s.cfg.Channel
	}
	opts := terminal.BannerOpts{
		Channel: channelDisplay,
		User:    s.slackUser,
	}
	if s.thread != nil {
		opts.Identity = fmt.Sprintf("%s %s", s.thread.Emoji(), s.thread.InstanceID())
		opts.Header = s.thread.Title()
		opts.Access = s.thread.AccessMode()
		if u := s.thread.URL(); u != "" {
			opts.JoinCmd = fmt.Sprintf("slaude join %s", u)
		}
	}
	opts.AutoApprove = autoApproveSummary(s.cfg.DangerousAutoApprove, s.cfg.DangerousAutoApproveNetwork)
	s.ui.Banner(opts)
}

// initialTurn sends the topic to Claude and reads the first response.
// Skipped on resume or when no topic is given.
func (s *Session) initialTurn() error {
	if s.cfg.Topic == "" || hasArg(s.cfg.ClaudeArgs, "--resume") {
		return nil
	}
	if err := s.proc.Send(s.cfg.Topic); err != nil {
		return fmt.Errorf("send topic: %w", err)
	}
	if err := s.readTurn(); err != nil {
		return fmt.Errorf("reading initial response: %w", err)
	}
	return nil
}

// resumeInfo builds the resume information for the caller.
func (s *Session) resumeInfo() *ResumeInfo {
	return &ResumeInfo{
		SessionID:  s.proc.SessionID(),
		Channel:    s.cfg.Channel,
		ThreadTS:   s.thread.ThreadTS(),
		ThreadURL:  s.thread.URL(),
		InstanceID: s.thread.InstanceID(),
		LastTS:     s.thread.LastTS(),
	}
}
