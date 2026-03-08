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
	"github.com/sttts/slagent/cmd/slaude/internal/session"
	"github.com/sttts/slagent/credential"
)

var cli struct {
	Start    StartCmd    `cmd:"" help:"Start a planning session mirrored to Slack."`
	Auth     AuthCmd     `cmd:"" help:"Set up Slack credentials."`
	Channels ChannelsCmd `cmd:"" help:"List Slack channels and group DMs."`
	Share    ShareCmd    `cmd:"" help:"Post a plan file to Slack for review."`
	Status   StatusCmd   `cmd:"" help:"Show current configuration."`
}

// StartCmd starts an interactive planning session with Claude Code.
type StartCmd struct {
	Channel      string   `short:"c" help:"Slack channel name or ID." placeholder:"CHANNEL"`
	User         []string `short:"u" help:"Slack user(s) for DM. Use multiple -u for group DM." placeholder:"USER"`
	Topic        []string `arg:"" optional:"" help:"Planning topic."`
	ResumeThread string   `help:"Slack thread timestamp to resume." placeholder:"THREAD_TS"`
	Debug        bool     `help:"Print raw JSON events from Claude to terminal."`
	ClaudeArgs   []string `name:"-" hidden:""`
}

func (cmd *StartCmd) Run() error {
	cfg := session.Config{
		Topic:          strings.Join(cmd.Topic, " "),
		Channel:        cmd.Channel,
		ResumeThreadTS: cmd.ResumeThread,
		Debug:          cmd.Debug,
		ClaudeArgs:     cmd.ClaudeArgs,
	}

	// Resolve --channel name or --user(s) to a channel ID
	if len(cmd.User) > 0 || (cfg.Channel != "" && !isSlackID(cfg.Channel)) {
		client, err := channel.New()
		if err != nil {
			return err
		}
		if len(cmd.User) > 0 {
			chID, err := client.ResolveUserChannel(cmd.User...)
			if err != nil {
				return fmt.Errorf("resolving user: %w", err)
			}
			cfg.Channel = chID

			// Build display name
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
		if _, err := credential.Load(); err == nil {
			cfg.Channel, cfg.ChannelName = promptChannel()
		}
	}

	// Detect resume from pass-through args
	isResume := false
	for _, a := range cfg.ClaudeArgs {
		if a == "--resume" || strings.HasPrefix(a, "--resume=") {
			isResume = true
			break
		}
	}

	// If no topic given (and not resuming), prompt for one
	if cfg.Topic == "" && !isResume {
		fmt.Print("📝 Topic: ")
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		cfg.Topic = strings.TrimSpace(line)
	}

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
		args := "slaude"
		if resume.Channel != "" {
			args += fmt.Sprintf(" -c %s", resume.Channel)
		}
		if resume.ThreadTS != "" {
			args += fmt.Sprintf(" --resume-thread %s", resume.ThreadTS)
		}
		args += fmt.Sprintf(" -- --resume %s", resume.SessionID)
		fmt.Printf("  %s\n", args)
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

// ChannelsCmd lists accessible Slack channels.
type ChannelsCmd struct{}

func (cmd *ChannelsCmd) Run() error {
	client, err := channel.New()
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
	creds, err := credential.Load()
	if err != nil {
		return err
	}

	// Resolve channel name if needed
	ch := cmd.Channel
	if !isSlackID(ch) {
		resolver, err := channel.New()
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
	creds, err := credential.Load()
	if err != nil {
		fmt.Println("  ❌ Slack: not configured (run 'slaude auth')")
	} else {
		token := creds.EffectiveToken()
		if len(token) > 10 {
			token = token[:10]
		}
		fmt.Printf("  ✅ Slack: configured (%s token: %s...)\n", creds.EffectiveType(), token)
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

	// Inject pass-through args into StartCmd
	if start, ok := ctx.Selected().Target.Addr().Interface().(*StartCmd); ok {
		start.ClaudeArgs = passthrough
	}

	if err := ctx.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
}

// promptChannel lists channels and lets the user pick one, or type @username for a DM.
// Returns (channelID, displayName).
func promptChannel() (string, string) {
	client, err := channel.New()
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
	if err := credential.Save(creds); err != nil {
		return fmt.Errorf("saving credentials: %w", err)
	}

	fmt.Printf("\n✅ Credentials saved to %s (%s token)\n", credential.Path(), tokenType)
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

	// Choose workspace
	var ws credential.Workspace
	if len(result.Workspaces) == 1 {
		ws = result.Workspaces[0]
		fmt.Printf("🏢 Found workspace: %s (%s)\n", ws.Name, ws.URL)
	} else {
		fmt.Println("🏢 Found workspaces:")
		for i, w := range result.Workspaces {
			fmt.Printf("  %d) %s (%s)\n", i+1, w.Name, w.URL)
		}
		fmt.Print("\n👉 Choose workspace [1]: ")
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

	creds := &credential.Credentials{
		Token:  ws.Token,
		Type:   "session",
		Cookie: result.Cookie,
	}
	if err := credential.Save(creds); err != nil {
		return fmt.Errorf("saving credentials: %w", err)
	}

	tokenPreview := ws.Token
	if len(tokenPreview) > 14 {
		tokenPreview = tokenPreview[:14]
	}
	fmt.Printf("\n✅ Credentials saved for %s (token: %s...)\n", ws.Name, tokenPreview)
	fmt.Printf("📁 Credentials file: %s\n", credential.Path())
	return nil
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
