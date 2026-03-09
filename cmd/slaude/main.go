package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/alecthomas/kong"

	"github.com/sttts/slagent"
	"github.com/sttts/slagent/channel"
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
}

// StartCmd starts a new interactive session with Claude Code.
type StartCmd struct {
	Channel    string   `short:"c" help:"Slack channel name or ID." placeholder:"CHANNEL"`
	User       []string `short:"u" help:"Slack user(s) for DM. Use multiple -u for group DM." placeholder:"USER"`
	Open       bool     `help:"Start with thread open for all participants (default: locked to owner)."`
	Topic      []string `arg:"" optional:"" help:"Planning topic."`
	Debug      bool     `help:"Print raw JSON events from Claude to terminal."`
	ClaudeArgs []string `name:"-" hidden:""`
}

// JoinCmd joins an existing Slack thread with a new slaude instance.
type JoinCmd struct {
	URL        string   `arg:"" help:"Slack thread URL to join."`
	Topic      []string `arg:"" optional:"" help:"Planning topic."`
	Debug      bool     `help:"Print raw JSON events from Claude to terminal."`
	ClaudeArgs []string `name:"-" hidden:""`
}

// ResumeCmd resumes an existing session in a Slack thread.
type ResumeCmd struct {
	URL        string   `arg:"" help:"Slack thread URL with #instanceID fragment."`
	Debug      bool     `help:"Print raw JSON events from Claude to terminal."`
	ClaudeArgs []string `name:"-" hidden:""`
}

// parseThreadURL parses a Slack permalink URL into channel and thread timestamp.
// Format: https://workspace.slack.com/archives/CHANNEL/pTIMESTAMP[#instanceID]
// Returns (channel, threadTS, instanceID).
func parseThreadURL(value string) (ch, threadTS, instanceID string) {
	// Extract instance ID from URL fragment (#instanceID)
	if idx := strings.LastIndex(value, "#"); idx >= 0 {
		instanceID = value[idx+1:]
		value = value[:idx]
	}

	parts := strings.Split(value, "/")
	for i, p := range parts {
		if p == "archives" && i+2 < len(parts) {
			ch = parts[i+1]
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
			return
		}
	}
	return "", "", instanceID
}

func (cmd *StartCmd) Run() error {
	cfg := session.Config{
		Topic:      strings.Join(cmd.Topic, " "),
		Channel:    cmd.Channel,
		OpenAccess: cmd.Open,
		Debug:      cmd.Debug,
		Workspace:  cli.Workspace,
		ClaudeArgs: cmd.ClaudeArgs,
	}

	// Resolve --channel name or --user(s) to a channel ID
	if len(cmd.User) > 0 || (cfg.Channel != "" && !isSlackID(cfg.Channel)) {
		client, err := channel.New().WithWorkspace(cfg.Workspace).Build()
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

	// If no channel given, prompt with channel list when credentials exist
	if cfg.Channel == "" {
		if _, err := credential.Load(cfg.Workspace); err == nil {
			cfg.Channel, cfg.ChannelName = promptChannel(cfg.Workspace)
		}
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
	ch, threadTS, _ := parseThreadURL(cmd.URL)
	if ch == "" || threadTS == "" {
		return fmt.Errorf("invalid thread URL: %s", cmd.URL)
	}

	cfg := session.Config{
		Topic:          strings.Join(cmd.Topic, " "),
		Channel:        ch,
		ResumeThreadTS: threadTS,
		Debug:          cmd.Debug,
		Workspace:      cli.Workspace,
		ClaudeArgs:     cmd.ClaudeArgs,
	}
	// InstanceID left empty → new instance generated

	return runSession(cfg)
}

func (cmd *ResumeCmd) Run() error {
	ch, threadTS, instanceID := parseThreadURL(cmd.URL)
	if ch == "" || threadTS == "" {
		return fmt.Errorf("invalid thread URL: %s", cmd.URL)
	}
	if instanceID == "" {
		return fmt.Errorf("missing instance ID in URL (expected URL#instanceID)")
	}

	cfg := session.Config{
		Channel:        ch,
		ResumeThreadTS: threadTS,
		InstanceID:     instanceID,
		Debug:          cmd.Debug,
		Workspace:      cli.Workspace,
		ClaudeArgs:     cmd.ClaudeArgs,
	}

	return runSession(cfg)
}

// runSession runs a session with the given config and prints resume info on exit.
func runSession(cfg session.Config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	resume, err := session.Run(ctx, cfg)
	if err != nil {
		return err
	}

	// Print resume commands
	if resume != nil && resume.SessionID != "" {
		fmt.Println()
		fmt.Println("🔄 To resume this session:")
		if resume.ThreadURL != "" && resume.InstanceID != "" {
			fmt.Printf("  slaude resume '%s#%s' -- --resume %s\n", resume.ThreadURL, resume.InstanceID, resume.SessionID)
		} else {
			fmt.Printf("  slaude resume (thread URL unavailable) -- --resume %s\n", resume.SessionID)
		}
		fmt.Println()
		fmt.Println("🤖 To resume in Claude Code directly:")
		fmt.Printf("  claude --resume %s\n", resume.SessionID)
	}

	return nil
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
	client, err := channel.New().WithWorkspace(cli.Workspace).Build()
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

	// Load credentials
	creds, err := credential.Load(cli.Workspace)
	if err != nil {
		return err
	}

	// Resolve channel name if needed
	ch := cmd.Channel
	if !isSlackID(ch) {
		resolver, err := channel.New().WithWorkspace(cli.Workspace).Build()
		if err != nil {
			return err
		}
		ch, err = resolver.ResolveChannelByName(ch)
		if err != nil {
			return fmt.Errorf("resolving channel: %w", err)
		}
	}

	// Use slagent for thread creation and posting
	client := slagent.NewSlackClient(creds.EffectiveToken(), creds.Cookie)
	thread := slagent.NewThread(client, creds.EffectiveToken(), ch)

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

	ctx := kong.Parse(&cli,
		kong.Name("slaude"),
		kong.Description("Mirror Claude Code sessions to Slack threads."),
		kong.UsageOnError(),
		kong.Vars{"args": strings.Join(kongArgs, " ")},
	)

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

// promptChannel lists channels and lets the user pick one, or type @username for a DM.
// Returns (channelID, displayName).
func promptChannel(workspace string) (string, string) {
	client, err := channel.New().WithWorkspace(workspace).Build()
	if err != nil {
		return "", ""
	}

	channels, err := client.ListChannels(slackProgress)
	fmt.Fprint(os.Stderr, "\r\033[K")
	if err != nil || len(channels) == 0 {
		return "", ""
	}

	fmt.Println("📡 Pick a channel (or type @username for a DM):")
	for i, ch := range channels {
		name := ch.Name
		if ch.Type == "channel" || ch.Type == "group" {
			name = "#" + name
		}
		fmt.Printf("  %2d) %s\n", i+1, name)
	}
	fmt.Print("\n📡 Channel: ")
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return "", ""
	}

	// @username → resolve to DM channel
	if strings.HasPrefix(line, "@") {
		chID, err := client.ResolveUserChannel(line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			return "", ""
		}
		return chID, "@" + strings.TrimPrefix(line, "@")
	}

	idx := 0
	fmt.Sscanf(line, "%d", &idx)
	idx--
	if idx < 0 || idx >= len(channels) {
		fmt.Fprintf(os.Stderr, "❌ Invalid choice\n")
		return "", ""
	}
	ch := channels[idx]
	name := ch.Name
	if ch.Type == "channel" || ch.Type == "group" {
		name = "#" + name
	}
	return ch.ID, name
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
	fmt.Println("     • channels:read")
	fmt.Println("     • groups:read")
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
	fmt.Println()

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
	if err := credential.Save(key, creds); err != nil {
		return fmt.Errorf("saving credentials: %w", err)
	}
	fmt.Printf("\n✅ %s added\n", key)

	// Ask whether to set as default
	_, defaultName, _ := credential.ListWorkspaces()
	if defaultName != key {
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
