package main

import (
	"fmt"
	"os"

	"github.com/sttts/slagent"
	slackclient "github.com/sttts/slagent/client"
	"github.com/sttts/slagent/credential"
)

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
