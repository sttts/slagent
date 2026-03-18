package main

import (
	"fmt"

	"github.com/sttts/slagent/credential"
)

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
