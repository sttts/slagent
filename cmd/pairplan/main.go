package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	pslack "github.com/sttts/pairplan/pkg/slack"
	"github.com/sttts/pairplan/pkg/session"
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
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--channel", "-c":
			i++
			if i < len(args) {
				cfg.Channel = args[i]
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

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := session.Run(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func cmdAuth() {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("Slack Bot Token Setup")
	fmt.Println(strings.Repeat("─", 40))
	fmt.Println()
	fmt.Println("1. Go to https://api.slack.com/apps")
	fmt.Println("2. Create a new app (or select existing)")
	fmt.Println("3. Go to 'OAuth & Permissions'")
	fmt.Println("4. Add these Bot Token Scopes:")
	fmt.Println("   - chat:write")
	fmt.Println("   - channels:history")
	fmt.Println("   - groups:history")
	fmt.Println("   - channels:read")
	fmt.Println("   - groups:read")
	fmt.Println("   - users:read")
	fmt.Println("5. Install the app to your workspace")
	fmt.Println("6. Copy the Bot User OAuth Token (xoxb-...)")
	fmt.Println()

	fmt.Print("Paste your bot token: ")
	token, err := reader.ReadString('\n')
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
		os.Exit(1)
	}
	token = strings.TrimSpace(token)

	if !strings.HasPrefix(token, "xoxb-") {
		fmt.Fprintf(os.Stderr, "Warning: token doesn't start with 'xoxb-'. Are you sure this is a bot token?\n")
	}

	if err := pslack.SaveCredentials(&pslack.Credentials{BotToken: token}); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving credentials: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nCredentials saved to %s\n", pslack.CredentialsPath())
	fmt.Println("Don't forget to invite the bot to your channel: /invite @your-bot-name")
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
		token := creds.BotToken
		if len(token) > 10 {
			token = token[:10]
		}
		fmt.Printf("Slack: configured (token: %s...)\n", token)
	}
}

func printUsage() {
	fmt.Println(`pairplan — Mirror Claude Code planning sessions to Slack

Usage:
  pairplan start [--channel C] [--topic "what we're planning"]
      Start a planning session. Mirrors to Slack if --channel is given.

  pairplan auth
      Set up Slack bot token.

  pairplan share <plan-file> --channel C
      Post a plan file to Slack for review.

  pairplan status
      Show current configuration.`)
}
