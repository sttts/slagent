package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/mattn/go-isatty"

	"github.com/sttts/slagent/channel"
	slackclient "github.com/sttts/slagent/client"
	"github.com/sttts/slagent/cmd/slaude/internal/perms"
	"github.com/sttts/slagent/cmd/slaude/internal/session"
	"github.com/sttts/slagent/credential"
)

// version is set by goreleaser via ldflags.
var version = "dev"

var cli struct {
	Workspace string      `short:"w" help:"Slack workspace URL (e.g. myteam.slack.com). Uses default if omitted." placeholder:"WORKSPACE"`
	Version   VersionCmd  `cmd:"" help:"Print version and exit."`
	Start     StartCmd    `cmd:"" help:"Start a new Slack thread with a Claude session."`
	Join      JoinCmd     `cmd:"" help:"Join an existing Slack thread with a new slaude instance."`
	Resume    ResumeCmd   `cmd:"" help:"Resume an existing session in a Slack thread."`
	Read      ReadCmd     `cmd:"" help:"Read a Slack thread and process with Claude."`
	Auth      AuthCmd     `cmd:"" help:"Set up Slack credentials."`
	Default   DefaultCmd  `cmd:"" help:"Set the default workspace."`
	Channels  ChannelsCmd `cmd:"" help:"List Slack channels and group DMs."`
	Share     ShareCmd    `cmd:"" help:"Post a plan file to Slack for review."`
	Status    StatusCmd   `cmd:"" help:"Show current configuration."`
	Ps        PsCmd       `cmd:"" help:"List running slaude sessions."`
	Kill      KillCmd     `cmd:"" help:"Kill a running slaude session by emoji or PID."`
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

// resolveAccessMode resolves the access mode from CLI flags, interactive prompt, or defaults.
// Exactly one of locked/observe/open will be true on return.
func resolveAccessMode(locked, observe, open *bool, command string) error {
	// Count how many flags are set
	n := 0
	if *locked {
		n++
	}
	if *observe {
		n++
	}
	if *open {
		n++
	}
	if n > 1 {
		return fmt.Errorf("--locked, --observe, and --open are mutually exclusive")
	}
	if n == 1 {
		return nil
	}

	// No flag given — prompt if interactive, otherwise use defaults
	if isatty.IsTerminal(os.Stdin.Fd()) {
		// Show prompt with default highlighted per command
		var prompt string
		switch command {
		case "start":
			prompt = "🔐 Closed, observe, or open? [Cbo] "
		default:
			prompt = "🔐 closed, oBserve, or open? [cBo] "
		}
		fmt.Print(prompt)
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		switch strings.TrimSpace(strings.ToLower(line)) {
		case "c", "closed", "locked":
			*locked = true
		case "b", "observe":
			*observe = true
		case "o", "open":
			*open = true
		case "":
			// Default per command
			switch command {
			case "start":
				*locked = true
			case "join":
				*observe = true
			case "resume":
				*observe = true
			}
		default:
			return fmt.Errorf("invalid choice")
		}
	} else {
		// Non-interactive defaults
		switch command {
		case "start":
			*locked = true
		case "join":
			*observe = true
		case "resume":
			*observe = true
		}
	}
	return nil
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
		if cfg.Observe {
			flags += " --observe"
		} else if cfg.OpenAccess {
			flags += " --open"
		} else {
			flags += " --locked"
		}
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

	// Handle --version before kong parsing
	if len(kongArgs) == 1 && (kongArgs[0] == "--version" || kongArgs[0] == "-v") {
		fmt.Println(version)
		return
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

// workspaceKey extracts "team.slack.com" from a workspace URL.
func workspaceKey(url string) string {
	url = strings.TrimPrefix(url, "https://")
	url = strings.TrimPrefix(url, "http://")
	url = strings.TrimSuffix(url, "/")
	return url
}

// workspaceFromURL extracts "team.slack.com" from a Slack permalink URL.
func workspaceFromURL(u string) string {
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	if idx := strings.Index(u, "/"); idx >= 0 {
		return u[:idx]
	}
	return u
}

// isSlackID returns true if s looks like a Slack channel/user/DM ID
// (e.g. C01234, G01234, D01234, DCBN5L04R).
func isSlackID(s string) bool {
	if len(s) < 2 {
		return false
	}
	prefix := s[0]
	if prefix != 'C' && prefix != 'G' && prefix != 'D' {
		return false
	}

	// Rest must be uppercase alphanumeric
	for _, c := range s[1:] {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	return true
}

func slackProgress(p channel.ListProgress) {
	switch p.Phase {
	case "checking":
		fmt.Fprintf(os.Stderr, "\r⏳ checking recent activity... %d/%d", p.Done, p.Total)
	default:
		fmt.Fprintf(os.Stderr, "\r📥 fetching channels... %d", p.Done)
	}
}
