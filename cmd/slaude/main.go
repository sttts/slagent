package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/alecthomas/kong"
	slackapi "github.com/slack-go/slack"

	"github.com/sttts/slagent"
	"github.com/sttts/slagent/channel"
	slackclient "github.com/sttts/slagent/client"
	"github.com/sttts/slagent/cmd/slaude/internal/perms"
	"github.com/sttts/slagent/cmd/slaude/internal/session"
	"github.com/sttts/slagent/credential"
)

var cli struct {
	Workspace string      `short:"w" help:"Slack workspace URL (e.g. myteam.slack.com). Uses default if omitted." placeholder:"WORKSPACE"`
	Start     StartCmd    `cmd:"" help:"Start a new Slack thread with a Claude session."`
	Join      JoinCmd     `cmd:"" help:"Join an existing Slack thread with a new slaude instance."`
	Resume    ResumeCmd   `cmd:"" help:"Resume an existing session in a Slack thread."`
	Auth      AuthCmd     `cmd:"" help:"Set up Slack credentials."`
	Default   DefaultCmd  `cmd:"" help:"Set the default workspace."`
	Channels  ChannelsCmd `cmd:"" help:"List Slack channels and group DMs."`
	Share     ShareCmd    `cmd:"" help:"Post a plan file to Slack for review."`
	Status    StatusCmd   `cmd:"" help:"Show current configuration."`
	Ps        PsCmd       `cmd:"" help:"List running slaude sessions."`
	Kill      KillCmd     `cmd:"" help:"Kill a running slaude session by emoji or PID."`
}

// StartCmd starts a new interactive session with Claude Code.
type StartCmd struct {
	Channel                    string   `short:"c" help:"Slack channel name or ID." placeholder:"CHANNEL"`
	User                       []string `short:"u" help:"Slack user(s) for DM. Use multiple -u for group DM." placeholder:"USER"`
	Open                       bool     `help:"Start with thread open for all participants (default: locked to owner)."`
	Topic                      []string `arg:"" optional:"" help:"Planning topic."`
	Debug                      bool     `help:"Print raw JSON events from Claude to terminal."`
	NoBye                      bool     `help:"Don't post a goodbye message to Slack on exit."`
	DangerousAutoApprove        string   `help:"Auto-approve sandbox risk level: never|green|yellow (default: never)." default:"never" enum:"never,green,yellow"`
	DangerousAutoApproveNetwork string   `help:"Auto-approve network access: never|known|any (default: never)." default:"never" enum:"never,known,any"`
	ClaudeArgs                 []string `name:"-" hidden:""`
}

// JoinCmd joins an existing Slack thread with a new slaude instance.
type JoinCmd struct {
	URL                        string   `arg:"" help:"Slack thread URL to join."`
	Topic                      []string `arg:"" optional:"" help:"Planning topic."`
	Open                       bool     `help:"Start with thread open for all participants."`
	Closed                     bool     `help:"Start locked to owner only, ignoring thread access state."`
	Debug                      bool     `help:"Print raw JSON events from Claude to terminal."`
	NoBye                      bool     `help:"Don't post a goodbye message to Slack on exit."`
	DangerousAutoApprove        string   `help:"Auto-approve sandbox risk level: never|green|yellow (default: never)." default:"never" enum:"never,green,yellow"`
	DangerousAutoApproveNetwork string   `help:"Auto-approve network access: never|known|any (default: never)." default:"never" enum:"never,known,any"`
	ClaudeArgs                 []string `name:"-" hidden:""`
}

// ResumeCmd resumes an existing session in a Slack thread.
type ResumeCmd struct {
	URL                        string   `arg:"" help:"Slack thread URL with #instanceID fragment."`
	Closed                     bool     `help:"Start locked to owner only, ignoring thread access state."`
	Debug                      bool     `help:"Print raw JSON events from Claude to terminal."`
	NoBye                      bool     `help:"Don't post a goodbye message to Slack on exit."`
	DangerousAutoApprove        string   `help:"Auto-approve sandbox risk level: never|green|yellow (default: never)." default:"never" enum:"never,green,yellow"`
	DangerousAutoApproveNetwork string   `help:"Auto-approve network access: never|known|any (default: never)." default:"never" enum:"never,known,any"`
	ClaudeArgs                 []string `name:"-" hidden:""`
}

// parseThreadURL parses a Slack permalink URL into channel, thread timestamp,
// instance ID, and optional cursor timestamp.
// Format: https://workspace.slack.com/archives/CHANNEL/pTIMESTAMP[#instanceID[@lastTS]]
func parseThreadURL(value string) (ch, threadTS, instanceID, afterTS string) {
	// Extract fragment (#instanceID[@lastTS])
	if idx := strings.LastIndex(value, "#"); idx >= 0 {
		frag := value[idx+1:]
		value = value[:idx]
		if at := strings.Index(frag, "@"); at >= 0 {
			instanceID = frag[:at]
			afterTS = frag[at+1:]
		} else {
			instanceID = frag
		}
	}

	parts := strings.Split(value, "/")
	for i, p := range parts {
		if p == "archives" && i+1 < len(parts) {
			ch = parts[i+1]

			// Optional timestamp after channel ID
			if i+2 < len(parts) {
				tsRaw := parts[i+2]

				// Strip query string
				if idx := strings.Index(tsRaw, "?"); idx >= 0 {
					tsRaw = tsRaw[:idx]
				}

				// Convert pTIMESTAMP → TIMESTAMP.MICROSECONDS
				tsRaw = strings.TrimPrefix(tsRaw, "p")
				if len(tsRaw) > 6 {
					threadTS = tsRaw[:len(tsRaw)-6] + "." + tsRaw[len(tsRaw)-6:]
				} else {
					threadTS = tsRaw
				}
			}
			return
		}
	}
	return "", "", instanceID, afterTS
}

func (cmd *StartCmd) Run() error {
	cfg := session.Config{
		Topic:                      strings.Join(cmd.Topic, " "),
		Channel:                    cmd.Channel,
		OpenAccess:                 cmd.Open,
		Debug:                      cmd.Debug,
		NoBye:                      cmd.NoBye,
		Workspace:                  cli.Workspace,
		ClaudeArgs:                 cmd.ClaudeArgs,
		DangerousAutoApprove:        cmd.DangerousAutoApprove,
		DangerousAutoApproveNetwork: cmd.DangerousAutoApproveNetwork,
	}

	// Ensure credentials exist before any Slack API call
	if err := credential.Ensure(cfg.Workspace, interactiveAuth); err != nil {
		return err
	}

	// Try parsing --channel as a Slack URL
	if cfg.Channel != "" && !isSlackID(cfg.Channel) {
		if ch, _, _, _ := parseThreadURL(cfg.Channel); ch != "" {
			cfg.Channel = ch
		}
	}

	// Resolve --channel name or --user(s) to a channel ID
	if len(cmd.User) > 0 || (cfg.Channel != "" && !isSlackID(cfg.Channel)) {
		client, err := newChannelClient(cfg.Workspace)
		if err != nil {
			return err
		}
		if len(cmd.User) > 0 {
			chID, err := client.ResolveUserChannel(cmd.User...)
			if err != nil {
				return fmt.Errorf("resolving user: %w", err)
			}
			cfg.Channel = chID

			var names []string
			for _, u := range cmd.User {
				names = append(names, "@"+strings.TrimPrefix(u, "@"))
			}
			cfg.ChannelName = strings.Join(names, ", ")
		} else {
			channelName := strings.TrimPrefix(cfg.Channel, "#")
			chID, err := client.ResolveChannelByName(channelName)
			if err != nil {
				return fmt.Errorf("resolving channel: %w", err)
			}
			cfg.Channel = chID
			cfg.ChannelName = "#" + channelName
		}
	}

	// If no channel given, prompt with channel list
	if cfg.Channel == "" {
		ch, name, err := promptChannel(cfg.Workspace)
		if err != nil {
			return err
		}
		cfg.Channel = ch
		cfg.ChannelName = name
	}

	// If no topic given, prompt for one
	if cfg.Topic == "" {
		fmt.Print("📝 Topic: ")
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		cfg.Topic = strings.TrimSpace(line)
	}

	return runSession(cfg)
}

func (cmd *JoinCmd) Run() error {
	ch, threadTS, _, _ := parseThreadURL(cmd.URL)
	if ch == "" || threadTS == "" {
		return fmt.Errorf("invalid thread URL: %s", cmd.URL)
	}

	if err := credential.Ensure(cli.Workspace, interactiveAuth); err != nil {
		return err
	}

	// If neither --open nor --closed specified, read thread title and confirm access mode
	if !cmd.Open && !cmd.Closed {
		creds, err := credential.Load(cli.Workspace)
		if err != nil {
			return err
		}
		client := slackclient.New(creds.EffectiveToken(), creds.Cookie)
		client.SetEnterprise(creds.Enterprise)

		// Fetch thread parent to check access state
		params := &slackapi.GetConversationRepliesParameters{
			ChannelID: ch,
			Timestamp: threadTS,
			Limit:     1,
		}
		msgs, _, _, err := client.GetConversationReplies(params)
		if err == nil && len(msgs) > 0 {
			title := msgs[0].Text
			isLocked := strings.Contains(title, "🔒") || strings.Contains(title, ":lock:")
			var mode string
			if isLocked {
				mode = "locked"
			} else {
				mode = "open"
			}

			fmt.Printf("🔐 Thread is %s. Continue with %s? [Y/o/l] ", mode, mode)
			reader := bufio.NewReader(os.Stdin)
			line, _ := reader.ReadString('\n')
			choice := strings.TrimSpace(strings.ToLower(line))
			switch choice {
			case "o", "open":
				cmd.Open = true
			case "l", "locked", "c", "closed":
				cmd.Closed = true
			case "", "y", "yes":
				// Keep current state: if locked, set closed; if open, set open
				if isLocked {
					cmd.Closed = true
				} else {
					cmd.Open = true
				}
			default:
				return fmt.Errorf("invalid choice: %q", choice)
			}
		}
	}

	cfg := session.Config{
		Topic:                      strings.Join(cmd.Topic, " "),
		Channel:                    ch,
		ResumeThreadTS:             threadTS,
		OpenAccess:                 cmd.Open,
		ClosedAccess:               cmd.Closed,
		Debug:                      cmd.Debug,
		NoBye:                      cmd.NoBye,
		Workspace:                  cli.Workspace,
		ClaudeArgs:                 cmd.ClaudeArgs,
		DangerousAutoApprove:        cmd.DangerousAutoApprove,
		DangerousAutoApproveNetwork: cmd.DangerousAutoApproveNetwork,
	}
	// InstanceID left empty → new instance generated

	return runSession(cfg)
}

func (cmd *ResumeCmd) Run() error {
	ch, threadTS, instanceID, afterTS := parseThreadURL(cmd.URL)
	if ch == "" || threadTS == "" {
		return fmt.Errorf("invalid thread URL: %s", cmd.URL)
	}
	if instanceID == "" {
		return fmt.Errorf("missing instance ID in URL (expected URL#instanceID)")
	}

	if err := credential.Ensure(cli.Workspace, interactiveAuth); err != nil {
		return err
	}

	cfg := session.Config{
		Channel:                    ch,
		ResumeThreadTS:             threadTS,
		ResumeAfterTS:              afterTS,
		InstanceID:                 instanceID,
		ClosedAccess:               cmd.Closed,
		Debug:                      cmd.Debug,
		NoBye:                      cmd.NoBye,
		Workspace:                  cli.Workspace,
		ClaudeArgs:                 cmd.ClaudeArgs,
		DangerousAutoApprove:        cmd.DangerousAutoApprove,
		DangerousAutoApproveNetwork: cmd.DangerousAutoApproveNetwork,
	}

	return runSession(cfg)
}

// runSession runs a session with the given config and prints resume info on exit.
func runSession(cfg session.Config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	resume, runErr := session.Run(ctx, cfg)

	// Always print resume commands, even on error
	if resume != nil && resume.SessionID != "" {
		// Build flags to carry over (only non-default values)
		var flags string
		if cfg.Debug {
			flags += " --debug"
		}
		if cfg.NoBye {
			flags += " --no-bye"
		}
		if cfg.DangerousAutoApprove != "" && cfg.DangerousAutoApprove != "never" {
			flags += " --dangerous-auto-approve " + cfg.DangerousAutoApprove
		}
		if cfg.DangerousAutoApproveNetwork != "" && cfg.DangerousAutoApproveNetwork != "never" {
			flags += " --dangerous-auto-approve-network " + cfg.DangerousAutoApproveNetwork
		}

		fmt.Println()
		fmt.Println("🔄 To resume this session:")
		fmt.Println()
		if resume.ThreadURL != "" && resume.InstanceID != "" {
			frag := resume.InstanceID
			if resume.LastTS != "" {
				frag += "@" + resume.LastTS
			}
			fmt.Printf("  %s resume%s %s#%s -- --resume %s\n", os.Args[0], flags, resume.ThreadURL, frag, resume.SessionID)
		} else {
			fmt.Printf("  %s resume%s (thread URL unavailable) -- --resume %s\n", os.Args[0], flags, resume.SessionID)
		}
		fmt.Println()
		fmt.Println("🤖 To resume in Claude Code directly:")
		fmt.Println()
		fmt.Printf("  claude --resume %s\n", resume.SessionID)
	}

	return runErr
}

// AuthCmd sets up Slack credentials.
type AuthCmd struct {
	Manual bool `help:"Paste a token manually instead of extracting from Slack desktop app."`
}

func (cmd *AuthCmd) Run() error {
	if cmd.Manual {
		return runAuthManual()
	}
	return runAuthExtract()
}

// DefaultCmd sets the default workspace.
type DefaultCmd struct {
	Workspace string `arg:"" help:"Workspace URL to set as default (e.g. myteam.slack.com)."`
}

func (cmd *DefaultCmd) Run() error {
	if err := credential.SetDefault(cmd.Workspace); err != nil {
		return err
	}
	fmt.Printf("✅ Default workspace: %s\n", cmd.Workspace)
	return nil
}

// ChannelsCmd lists accessible Slack channels.
type ChannelsCmd struct{}

func (cmd *ChannelsCmd) Run() error {
	if err := credential.Ensure(cli.Workspace, interactiveAuth); err != nil {
		return err
	}

	client, err := newChannelClient(cli.Workspace)
	if err != nil {
		return err
	}

	channels, err := client.ListChannels(slackProgress)
	fmt.Fprint(os.Stderr, "\r\033[K")
	if err != nil {
		return fmt.Errorf("listing channels: %w", err)
	}

	for i, ch := range channels {
		name := ch.Name
		if ch.Type == "channel" || ch.Type == "group" {
			name = "#" + name
		}
		fmt.Printf("  %2d) %s\n", i+1, name)
	}
	return nil
}

// ShareCmd posts a plan file to Slack.
type ShareCmd struct {
	File    string `arg:"" help:"Plan file to share."`
	Channel string `short:"c" required:"" help:"Slack channel name or ID." placeholder:"CHANNEL"`
}

func (cmd *ShareCmd) Run() error {
	content, err := os.ReadFile(cmd.File)
	if err != nil {
		return fmt.Errorf("reading %s: %w", cmd.File, err)
	}

	if err := credential.Ensure(cli.Workspace, interactiveAuth); err != nil {
		return err
	}

	// Load credentials
	creds, err := credential.Load(cli.Workspace)
	if err != nil {
		return err
	}

	// Resolve channel name if needed
	ch := cmd.Channel
	if !isSlackID(ch) {
		resolver, err := newChannelClient(cli.Workspace)
		if err != nil {
			return err
		}
		ch, err = resolver.ResolveChannelByName(ch)
		if err != nil {
			return fmt.Errorf("resolving channel: %w", err)
		}
	}

	// Use slagent for thread creation and posting
	client := slackclient.New(creds.EffectiveToken(), creds.Cookie)
	thread := slagent.NewThread(client, ch)

	topic := fmt.Sprintf("Plan review: %s", cmd.File)
	url, err := thread.Start(topic)
	if err != nil {
		return err
	}

	if err := thread.PostMarkdown(string(content)); err != nil {
		return fmt.Errorf("posting plan: %w", err)
	}

	fmt.Printf("✅ Plan shared: %s\n", url)
	return nil
}

// StatusCmd shows current configuration.
type StatusCmd struct{}

func (cmd *StatusCmd) Run() error {
	fmt.Println("📊 Status")
	fmt.Println("  ⏸️  No active session.")

	names, defaultName, _ := credential.ListWorkspaces()
	if len(names) == 0 {
		fmt.Println("  ❌ Slack: not configured (run 'slaude auth')")
		return nil
	}

	for _, name := range names {
		creds, err := credential.Load(name)
		if err != nil {
			continue
		}
		token := creds.EffectiveToken()
		if len(token) > 10 {
			token = token[:10]
		}
		marker := "  "
		if name == defaultName {
			marker = "* "
		}
		fmt.Printf("  %s✅ %s (%s token: %s...)\n", marker, name, creds.EffectiveType(), token)
	}
	return nil
}

// PsCmd lists running slaude sessions.
type PsCmd struct{}

func (cmd *PsCmd) Run() error {
	sessions, err := session.ListSessions()
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	fmt.Print(session.FormatSessions(sessions))
	return nil
}

// KillCmd sends SIGINT to a running slaude session identified by emoji or PID.
type KillCmd struct {
	Target string `arg:"" help:"Session emoji (e.g. 'fox', ':fox_face:') or PID." name:"target"`
}

func (cmd *KillCmd) Run() error {
	if err := session.KillSession(cmd.Target); err != nil {
		return err
	}
	fmt.Printf("✅ Sent SIGINT to session %q\n", cmd.Target)
	return nil
}

// extractPassthroughArgs returns args after "--" separator.
func extractPassthroughArgs(args []string) []string {
	for i, a := range args {
		if a == "--" {
			return args[i+1:]
		}
	}
	return nil
}

func main() {
	// Hidden subcommand: _mcp-permissions (started by Claude as MCP server)
	if len(os.Args) >= 2 && os.Args[1] == "_mcp-permissions" {
		var socketPath string
		args := os.Args[2:]
		for i, a := range args {
			if a == "--socket" && i+1 < len(args) {
				socketPath = args[i+1]
			}
		}
		if socketPath == "" {
			fmt.Fprintln(os.Stderr, "usage: slaude _mcp-permissions --socket PATH")
			os.Exit(1)
		}
		if err := perms.RunServer(socketPath); err != nil {
			fmt.Fprintf(os.Stderr, "mcp server: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Extract pass-through args before kong parsing
	passthrough := extractPassthroughArgs(os.Args[1:])

	// Strip everything after -- for kong
	var kongArgs []string
	for _, a := range os.Args[1:] {
		if a == "--" {
			break
		}
		kongArgs = append(kongArgs, a)
	}

	parser, err := kong.New(&cli,
		kong.Name("slaude"),
		kong.Description("Mirror Claude Code sessions to Slack threads."),
		kong.UsageOnError(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	ctx, err := parser.Parse(kongArgs)
	if err != nil {
		parser.FatalIfErrorf(err)
	}

	// Inject pass-through args into session commands
	switch cmd := ctx.Selected().Target.Addr().Interface().(type) {
	case *StartCmd:
		cmd.ClaudeArgs = passthrough
	case *JoinCmd:
		cmd.ClaudeArgs = passthrough
	case *ResumeCmd:
		cmd.ClaudeArgs = passthrough
	}

	if err := ctx.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
}

// newChannelClient creates a channel client, retrying with re-auth on token errors.
func newChannelClient(workspace string) (*channel.Client, error) {
	creds, err := credential.Load(workspace)
	if err != nil {
		return nil, err
	}
	sc := slackclient.New(creds.EffectiveToken(), creds.Cookie)
	sc.SetEnterprise(creds.Enterprise)
	ch, err := channel.New(sc)
	if credential.IsAuthError(err) {
		if rerr := interactiveReauth(); rerr != nil {
			return nil, rerr
		}
		creds, err = credential.Load(workspace)
		if err != nil {
			return nil, err
		}
		sc = slackclient.New(creds.EffectiveToken(), creds.Cookie)
		sc.SetEnterprise(creds.Enterprise)
		ch, err = channel.New(sc)
	}
	return ch, err
}

// interactiveAuth runs credential extraction with user-facing output.
func interactiveAuth() error {
	fmt.Println("No Slack credentials found. Let's set them up.")
	return runAuthExtract()
}

// interactiveReauth re-extracts credentials after a token failure.
func interactiveReauth() error {
	fmt.Println("🔄 Token expired or invalid. Re-extracting from Slack desktop app...")
	return runAuthExtract()
}

// promptChannel lists channels and lets the user pick one, or type @username for a DM.
// Returns (channelID, displayName).
func promptChannel(workspace string) (string, string, error) {
	client, err := newChannelClient(workspace)
	if err != nil {
		return "", "", err
	}

	// Show identity
	if name, _, team, teamURL := client.Identity(); name != "" {
		if teamURL != "" {
			fmt.Fprintf(os.Stderr, "👤 @%s on %s (%s)\n", name, team, teamURL)
		} else if team != "" {
			fmt.Fprintf(os.Stderr, "👤 @%s on %s\n", name, team)
		} else {
			fmt.Fprintf(os.Stderr, "👤 @%s\n", name)
		}
	}

	channels, err := client.ListChannels(slackProgress)
	fmt.Fprint(os.Stderr, "\r\033[K")

	// Re-extract credentials on auth failure and retry once
	if credential.IsAuthError(err) {
		if rerr := interactiveReauth(); rerr != nil {
			return "", "", rerr
		}
		client, err = newChannelClient(workspace)
		if err != nil {
			return "", "", err
		}
		channels, err = client.ListChannels(slackProgress)
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
	if err != nil || len(channels) == 0 {
		// Can't list channels (enterprise restriction, etc.) — prompt manually
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Could not list channels: %v\n", err)
		}
		fmt.Print("📡 Channel ID or URL (or @username / @email / @U... for DM): ")
	} else {
		fmt.Println("📡 Pick a channel (or type @username for a DM):")
		for i, ch := range channels {
			name := ch.Name
			if ch.Type == "channel" || ch.Type == "group" {
				name = "#" + name
			}
			fmt.Printf("  %2d) %s\n", i+1, name)
		}
		fmt.Print("\n📡 Channel: ")
	}
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", fmt.Errorf("no channel selected")
	}

	// @username → resolve to DM channel
	if strings.HasPrefix(line, "@") {
		chID, err := client.ResolveUserChannel(line)
		if err != nil {
			return "", "", err
		}
		return chID, "@" + strings.TrimPrefix(line, "@"), nil
	}

	// No channel list (enterprise, etc.) — treat input as channel ID or URL
	if len(channels) == 0 {
		// Try parsing as a Slack URL
		if ch, _, _, _ := parseThreadURL(line); ch != "" {
			return ch, ch, nil
		}
		line = strings.TrimPrefix(line, "#")
		if isSlackID(line) {
			return line, line, nil
		}
		return "", "", fmt.Errorf("expected a channel ID (e.g. C08HFRFLRC4) or Slack URL, got %q", line)
	}

	// Pick from numbered list
	idx := 0
	fmt.Sscanf(line, "%d", &idx)
	idx--
	if idx < 0 || idx >= len(channels) {
		return "", "", fmt.Errorf("invalid choice: %s", line)
	}
	ch := channels[idx]
	name := ch.Name
	if ch.Type == "channel" || ch.Type == "group" {
		name = "#" + name
	}
	return ch.ID, name, nil
}

func runAuthManual() error {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("🔑 Slack Token Setup")
	fmt.Println(strings.Repeat("─", 40))
	fmt.Println()
	fmt.Println("  1️⃣  Go to https://api.slack.com/apps")
	fmt.Println("  2️⃣  Create a new app (or select existing)")
	fmt.Println("  3️⃣  Go to 'OAuth & Permissions'")
	fmt.Println("  4️⃣  Add scopes (Bot or User Token Scopes):")
	fmt.Println("     • chat:write")
	fmt.Println("     • channels:history")
	fmt.Println("     • groups:history")
	fmt.Println("     • im:history")
	fmt.Println("     • mpim:history")
	fmt.Println("     • channels:read")
	fmt.Println("     • groups:read")
	fmt.Println("     • im:read")
	fmt.Println("     • im:write")
	fmt.Println("     • mpim:read")
	fmt.Println("     • mpim:write")
	fmt.Println("     • reactions:read")
	fmt.Println("     • reactions:write")
	fmt.Println("     • users:read")
	fmt.Println("  5️⃣  Install the app to your workspace")
	fmt.Println("  6️⃣  Copy the token (xoxb-... for bot, xoxp-... for user)")
	fmt.Println()

	fmt.Print("🏢 Workspace URL (e.g. myteam.slack.com): ")
	wsURL, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	wsURL = strings.TrimSpace(wsURL)
	if wsURL == "" {
		return fmt.Errorf("workspace URL is required")
	}
	key := workspaceKey(wsURL)

	fmt.Print("🔐 Paste your token: ")
	token, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	token = strings.TrimSpace(token)

	// Determine token type
	var tokenType string
	switch {
	case strings.HasPrefix(token, "xoxb-"):
		tokenType = "bot"
	case strings.HasPrefix(token, "xoxp-"):
		tokenType = "user"
	default:
		fmt.Fprintf(os.Stderr, "⚠️  Warning: token doesn't start with 'xoxb-' or 'xoxp-'.\n")
		tokenType = "bot"
	}

	creds := &credential.Credentials{Token: token, Type: tokenType}

	// Detect enterprise grid
	sc := slackclient.New(token, "")
	if resp, err := sc.AuthTest(); err == nil && resp.EnterpriseID != "" {
		creds.Enterprise = true
	}

	if err := credential.Save(key, creds); err != nil {
		return fmt.Errorf("saving credentials: %w", err)
	}

	fmt.Printf("\n✅ Credentials saved for %s (%s token)\n", key, tokenType)
	if tokenType == "bot" {
		fmt.Println("💡 Don't forget to invite the bot to your channel: /invite @your-bot-name")
	}
	return nil
}

func runAuthExtract() error {
	fmt.Println("🔍 Extracting Slack credentials from desktop app...")
	fmt.Println("🔐 (you may see a macOS keychain access prompt — please allow access)")

	result, err := credential.Extract()
	if err != nil {
		return err
	}

	// Let user pick a workspace
	var ws credential.Workspace
	if len(result.Workspaces) == 1 {
		ws = result.Workspaces[0]
		fmt.Printf("🏢 Found workspace: %s (%s)\n", ws.Name, ws.URL)
	} else {
		fmt.Println("🏢 Found workspaces:")
		for i, w := range result.Workspaces {
			fmt.Printf("  %d) %s (%s)\n", i+1, w.Name, w.URL)
		}
		fmt.Print("\n👉 Extract token for workspace [1]: ")
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		idx := 0
		if line != "" {
			fmt.Sscanf(line, "%d", &idx)
			idx--
		}
		if idx < 0 || idx >= len(result.Workspaces) {
			idx = 0
		}
		ws = result.Workspaces[idx]
	}

	key := workspaceKey(ws.URL)
	creds := &credential.Credentials{
		Token:  ws.Token,
		Type:   "session",
		Cookie: result.Cookie,
	}

	// Detect enterprise grid — session tokens are unreliable there
	sc := slackclient.New(creds.EffectiveToken(), creds.Cookie)
	if resp, err := sc.AuthTest(); err == nil && resp.EnterpriseID != "" {
		fmt.Println("⚠️ Enterprise grid workspace detected.")
		fmt.Println("  Session tokens (xoxc-) are unreliable on enterprise — Slack revokes them.")
		fmt.Println("  Run 'slaude auth --manual' to create a Slack app and paste a user token (xoxp-).")
		return fmt.Errorf("enterprise grid does not support extracted session tokens")
	}

	if err := credential.Save(key, creds); err != nil {
		return fmt.Errorf("saving credentials: %w", err)
	}
	fmt.Printf("✅ %s added\n", key)

	// Set as default if there isn't one yet, otherwise ask
	_, defaultName, _ := credential.ListWorkspaces()
	if defaultName == "" {
		credential.SetDefault(key)
		fmt.Printf("⭐ Default workspace: %s\n", key)
	} else if defaultName != key {
		reader := bufio.NewReader(os.Stdin)
		fmt.Printf("⭐ Set %s as default workspace? [y/N]: ", key)
		line, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(line)) == "y" {
			credential.SetDefault(key)
			fmt.Printf("⭐ Default workspace: %s\n", key)
		}
	}
	return nil
}

// workspaceKey extracts "team.slack.com" from a workspace URL.
func workspaceKey(url string) string {
	url = strings.TrimPrefix(url, "https://")
	url = strings.TrimPrefix(url, "http://")
	url = strings.TrimSuffix(url, "/")
	return url
}

// isSlackID returns true if s looks like a Slack channel/user ID (e.g. C01234, G01234, D01234).
func isSlackID(s string) bool {
	if len(s) < 2 {
		return false
	}
	prefix := s[0]
	return (prefix == 'C' || prefix == 'G' || prefix == 'D') && s[1] >= '0' && s[1] <= '9'
}

func slackProgress(p channel.ListProgress) {
	switch p.Phase {
	case "checking":
		fmt.Fprintf(os.Stderr, "\r⏳ checking recent activity... %d/%d", p.Done, p.Total)
	default:
		fmt.Fprintf(os.Stderr, "\r📥 fetching channels... %d", p.Done)
	}
}
