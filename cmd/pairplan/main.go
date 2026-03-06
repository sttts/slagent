package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/sttts/pairplan/pkg/session"
	pslack "github.com/sttts/pairplan/pkg/slack"
	"github.com/sttts/pairplan/pkg/slack/extract"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "start":
		cmdStart()
	case "auth":
		cmdAuth()
	case "channels":
		cmdChannels()
	case "share":
		cmdShare()
	case "status":
		cmdStatus()
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func cmdStart() {
	cfg := session.Config{
		PermissionMode: "plan",
	}

	// Parse flags
	var targetUser string
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--channel", "-c":
			i++
			if i < len(args) {
				cfg.Channel = args[i]
			}
		case "--user", "-u":
			i++
			if i < len(args) {
				targetUser = args[i]
			}
		case "--topic", "-t":
			i++
			if i < len(args) {
				cfg.Topic = args[i]
			}
		case "--permission-mode":
			i++
			if i < len(args) {
				cfg.PermissionMode = args[i]
			}
		default:
			// Treat remaining args as topic
			if cfg.Topic == "" {
				cfg.Topic = strings.Join(args[i:], " ")
				i = len(args)
			}
		}
	}

	// Resolve --channel name or --user to a channel ID
	if targetUser != "" || (cfg.Channel != "" && !isSlackID(cfg.Channel)) {
		client, err := pslack.New("")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if targetUser != "" {
			chID, err := client.ResolveUserChannel(targetUser, userProgress)
			fmt.Fprint(os.Stderr, "\r\033[K")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving user: %v\n", err)
				os.Exit(1)
			}
			cfg.Channel = chID
		} else {
			chID, err := client.ResolveChannelByName(cfg.Channel)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving channel: %v\n", err)
				os.Exit(1)
			}
			cfg.Channel = chID
		}
	}

	// If no channel given, prompt with channel list when credentials exist
	if cfg.Channel == "" {
		if _, err := pslack.LoadCredentials(); err == nil {
			cfg.Channel = promptChannel()
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := session.Run(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// promptChannel lists channels and lets the user pick one, or type @username for a DM.
func promptChannel() string {
	client, err := pslack.New("")
	if err != nil {
		return ""
	}

	channels, err := client.ListChannels(slackProgress)
	fmt.Fprint(os.Stderr, "\r\033[K")
	if err != nil || len(channels) == 0 {
		return ""
	}

	fmt.Println("Pick a channel (or type @username for a DM):")
	for i, ch := range channels {
		name := ch.Name
		if ch.Type == "channel" || ch.Type == "group" {
			name = "#" + name
		}
		fmt.Printf("  %2d) %s\n", i+1, name)
	}
	fmt.Print("\nChannel: ")
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}

	// @username → resolve to DM channel
	if strings.HasPrefix(line, "@") {
		chID, err := client.ResolveUserChannel(line, userProgress)
		fmt.Fprint(os.Stderr, "\r\033[K")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return ""
		}
		return chID
	}

	idx := 0
	fmt.Sscanf(line, "%d", &idx)
	idx--
	if idx < 0 || idx >= len(channels) {
		fmt.Fprintf(os.Stderr, "Invalid choice\n")
		return ""
	}
	return channels[idx].ID
}

func cmdAuth() {
	// Check for --extract flag
	for _, arg := range os.Args[2:] {
		if arg == "--extract" {
			cmdAuthExtract()
			return
		}
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Println("Slack Token Setup")
	fmt.Println(strings.Repeat("─", 40))
	fmt.Println()
	fmt.Println("1. Go to https://api.slack.com/apps")
	fmt.Println("2. Create a new app (or select existing)")
	fmt.Println("3. Go to 'OAuth & Permissions'")
	fmt.Println("4. Add scopes (Bot or User Token Scopes):")
	fmt.Println("   - chat:write")
	fmt.Println("   - channels:history")
	fmt.Println("   - groups:history")
	fmt.Println("   - channels:read")
	fmt.Println("   - groups:read")
	fmt.Println("   - users:read")
	fmt.Println("5. Install the app to your workspace")
	fmt.Println("6. Copy the token (xoxb-... for bot, xoxp-... for user)")
	fmt.Println()

	fmt.Print("Paste your token: ")
	token, err := reader.ReadString('\n')
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
		os.Exit(1)
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
		fmt.Fprintf(os.Stderr, "Warning: token doesn't start with 'xoxb-' or 'xoxp-'.\n")
		tokenType = "bot"
	}

	creds := &pslack.Credentials{Token: token, Type: tokenType}
	if err := pslack.SaveCredentials(creds); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving credentials: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nCredentials saved to %s (%s token)\n", pslack.CredentialsPath(), tokenType)
	if tokenType == "bot" {
		fmt.Println("Don't forget to invite the bot to your channel: /invite @your-bot-name")
	}
}

func cmdShare() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: pairplan share <plan-file> [--channel CHANNEL]\n")
		os.Exit(1)
	}

	planFile := os.Args[2]
	var channel string

	for i := 3; i < len(os.Args); i++ {
		if (os.Args[i] == "--channel" || os.Args[i] == "-c") && i+1 < len(os.Args) {
			i++
			channel = os.Args[i]
		}
	}

	if channel == "" {
		fmt.Fprintf(os.Stderr, "Error: --channel is required\n")
		os.Exit(1)
	}

	content, err := os.ReadFile(planFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", planFile, err)
		os.Exit(1)
	}

	client, err := pslack.New(channel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	topic := fmt.Sprintf("Plan review: %s", planFile)
	url, err := client.StartThread(topic)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if err := client.PostClaudeMessage(string(content)); err != nil {
		fmt.Fprintf(os.Stderr, "Error posting plan: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Plan shared: %s\n", url)
}

func cmdStatus() {
	fmt.Println("No active session.")
	creds, err := pslack.LoadCredentials()
	if err != nil {
		fmt.Println("Slack: not configured (run 'pairplan auth')")
	} else {
		token := creds.EffectiveToken()
		if len(token) > 10 {
			token = token[:10]
		}
		fmt.Printf("Slack: configured (%s token: %s...)\n", creds.EffectiveType(), token)
	}
}

func cmdAuthExtract() {
	fmt.Println("Extracting Slack credentials from desktop app...")
	fmt.Println("(you may see a macOS keychain access prompt — please allow access)")
	fmt.Println()

	result, err := extract.Extract()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Choose workspace
	var ws extract.Workspace
	if len(result.Workspaces) == 1 {
		ws = result.Workspaces[0]
		fmt.Printf("Found workspace: %s (%s)\n", ws.Name, ws.URL)
	} else {
		fmt.Println("Found workspaces:")
		for i, w := range result.Workspaces {
			fmt.Printf("  %d) %s (%s)\n", i+1, w.Name, w.URL)
		}
		fmt.Print("\nChoose workspace [1]: ")
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

	creds := &pslack.Credentials{
		Token:  ws.Token,
		Type:   "session",
		Cookie: result.Cookie,
	}
	if err := pslack.SaveCredentials(creds); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving credentials: %v\n", err)
		os.Exit(1)
	}

	tokenPreview := ws.Token
	if len(tokenPreview) > 14 {
		tokenPreview = tokenPreview[:14]
	}
	fmt.Printf("\nCredentials saved for %s (token: %s...)\n", ws.Name, tokenPreview)
	fmt.Printf("Credentials file: %s\n", pslack.CredentialsPath())
}

func cmdChannels() {
	client, err := pslack.New("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	channels, err := client.ListChannels(slackProgress)
	fmt.Fprint(os.Stderr, "\r\033[K")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing channels: %v\n", err)
		os.Exit(1)
	}

	for i, ch := range channels {
		name := ch.Name
		if ch.Type == "channel" || ch.Type == "group" {
			name = "#" + name
		}
		fmt.Printf("  %2d) %s\n", i+1, name)
	}
}

// isSlackID returns true if s looks like a Slack channel/user ID (e.g. C01234, G01234, D01234).
func isSlackID(s string) bool {
	if len(s) < 2 {
		return false
	}
	prefix := s[0]
	return (prefix == 'C' || prefix == 'G' || prefix == 'D') && s[1] >= '0' && s[1] <= '9'
}

func userProgress(checked int) {
	fmt.Fprintf(os.Stderr, "\rsearching users... %d", checked)
}

func slackProgress(p pslack.ListProgress) {
	switch p.Phase {
	case "checking":
		fmt.Fprintf(os.Stderr, "\rchecking recent activity... %d/%d", p.Done, p.Total)
	default:
		fmt.Fprintf(os.Stderr, "\rfetching channels... %d", p.Done)
	}
}

func printUsage() {
	fmt.Println(`pairplan — Mirror Claude Code planning sessions to Slack

Usage:
  pairplan start [--channel C] [--user @name] [--topic "what we're planning"]
      Start a planning session. Mirrors to Slack if --channel or --user is given.

  pairplan auth
      Set up Slack token (paste xoxb-/xoxp- token).

  pairplan auth --extract
      Auto-extract session token from local Slack desktop app.

  pairplan channels
      List your Slack channels and group DMs.

  pairplan share <plan-file> --channel C
      Post a plan file to Slack for review.

  pairplan status
      Show current configuration.`)
}
