package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	slackclient "github.com/sttts/slagent/client"
	"github.com/sttts/slagent/credential"
)

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
