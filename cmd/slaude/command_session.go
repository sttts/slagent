package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/sttts/slagent/cmd/slaude/internal/session"
	"github.com/sttts/slagent/credential"
)

// VersionCmd prints the version and exits.
type VersionCmd struct{}

func (cmd *VersionCmd) Run() error {
	fmt.Println(version)
	return nil
}

// StartCmd starts a new interactive session with Claude Code.
type StartCmd struct {
	Locked                     bool     `help:"Lock thread to owner only (default for start)."`
	Observe                    bool     `help:"Observe mode: read all messages, respond only to owner."`
	Open                       bool     `help:"Open thread for all participants."`
	Target                     string   `arg:"" optional:"" help:"Slack URL, #channel, @user, or channel ID."`
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
	Locked                     bool     `help:"Lock thread to owner only."`
	Observe                    bool     `help:"Observe mode: read all messages, respond only to owner (default for join)."`
	Open                       bool     `help:"Open thread for all participants."`
	Debug                      bool     `help:"Print raw JSON events from Claude to terminal."`
	NoBye                      bool     `help:"Don't post a goodbye message to Slack on exit."`
	DangerousAutoApprove        string   `help:"Auto-approve sandbox risk level: never|green|yellow (default: never)." default:"never" enum:"never,green,yellow"`
	DangerousAutoApproveNetwork string   `help:"Auto-approve network access: never|known|any (default: never)." default:"never" enum:"never,known,any"`
	ClaudeArgs                 []string `name:"-" hidden:""`
}

// ResumeCmd resumes an existing session in a Slack thread.
type ResumeCmd struct {
	URL                        string   `arg:"" help:"Slack thread URL with #instanceID fragment."`
	Locked                     bool     `help:"Lock thread to owner only."`
	Observe                    bool     `help:"Observe mode: read all messages, respond only to owner."`
	Open                       bool     `help:"Open thread for all participants."`
	Debug                      bool     `help:"Print raw JSON events from Claude to terminal."`
	NoBye                      bool     `help:"Don't post a goodbye message to Slack on exit."`
	DangerousAutoApprove        string   `help:"Auto-approve sandbox risk level: never|green|yellow (default: never)." default:"never" enum:"never,green,yellow"`
	DangerousAutoApproveNetwork string   `help:"Auto-approve network access: never|known|any (default: never)." default:"never" enum:"never,known,any"`
	ClaudeArgs                 []string `name:"-" hidden:""`
}

func (cmd *StartCmd) Run() error {
	// Resolve target: URL, #channel, @user, or channel ID
	var channel string
	var users []string
	workspace := cli.Workspace
	target := cmd.Target
	if target != "" {
		switch {
		case strings.Contains(target, "/archives/"):
			if ch, _, _, _ := parseThreadURL(target); ch != "" {
				channel = ch
			}
			if workspace == "" {
				workspace = workspaceFromURL(target)
			}
		case strings.HasPrefix(target, "@"):
			users = []string{strings.TrimPrefix(target, "@")}
		default:
			channel = target
		}
	}

	cfg := session.Config{
		Topic:                      strings.Join(cmd.Topic, " "),
		Channel:                    channel,
		Version:                    version,
		Debug:                      cmd.Debug,
		NoBye:                      cmd.NoBye,
		Workspace:                  workspace,
		ClaudeArgs:                 cmd.ClaudeArgs,
		DangerousAutoApprove:        cmd.DangerousAutoApprove,
		DangerousAutoApproveNetwork: cmd.DangerousAutoApproveNetwork,
	}

	// Ensure credentials exist before any Slack API call
	if err := credential.Ensure(cfg.Workspace, interactiveAuth); err != nil {
		return err
	}

	// Resolve channel name or @user to a channel ID
	if len(users) > 0 || (cfg.Channel != "" && !isSlackID(cfg.Channel)) {
		client, err := newChannelClient(cfg.Workspace)
		if err != nil {
			return err
		}

		if len(users) > 0 {
			chID, err := client.ResolveUserChannel(users...)
			if err != nil {
				return fmt.Errorf("resolving user: %w", err)
			}
			cfg.Channel = chID

			var names []string
			for _, u := range users {
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

	// Resolve access mode last: channel → topic → mode
	if err := resolveAccessMode(&cmd.Locked, &cmd.Observe, &cmd.Open, "start"); err != nil {
		return err
	}
	cfg.OpenAccess = cmd.Open
	cfg.ClosedAccess = cmd.Locked
	cfg.Observe = cmd.Observe

	return runSession(cfg)
}

func (cmd *JoinCmd) Run() error {
	ch, threadTS, _, _ := parseThreadURL(cmd.URL)
	if ch == "" || threadTS == "" {
		return fmt.Errorf("invalid thread URL: %s", cmd.URL)
	}

	// Extract workspace from URL if not specified via -w
	workspace := cli.Workspace
	if workspace == "" {
		workspace = workspaceFromURL(cmd.URL)
	}

	if err := credential.Ensure(workspace, interactiveAuth); err != nil {
		return err
	}

	// Resolve access mode: --locked/--observe/--open, or prompt/default
	if err := resolveAccessMode(&cmd.Locked, &cmd.Observe, &cmd.Open, "join"); err != nil {
		return err
	}

	cfg := session.Config{
		Topic:                      strings.Join(cmd.Topic, " "),
		Channel:                    ch,
		ResumeThreadTS:             threadTS,
		Version:                    version,
		OpenAccess:                 cmd.Open,
		ClosedAccess:               cmd.Locked,
		Observe:                    cmd.Observe,
		Debug:                      cmd.Debug,
		NoBye:                      cmd.NoBye,
		Workspace:                  workspace,
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

	// Extract workspace from URL if not specified via -w
	workspace := cli.Workspace
	if workspace == "" {
		workspace = workspaceFromURL(cmd.URL)
	}

	if err := credential.Ensure(workspace, interactiveAuth); err != nil {
		return err
	}

	// Resolve access mode: --locked/--observe/--open, or prompt/default
	if err := resolveAccessMode(&cmd.Locked, &cmd.Observe, &cmd.Open, "resume"); err != nil {
		return err
	}

	cfg := session.Config{
		Channel:                    ch,
		ResumeThreadTS:             threadTS,
		ResumeAfterTS:              afterTS,
		InstanceID:                 instanceID,
		Version:                    version,
		OpenAccess:                 cmd.Open,
		ClosedAccess:               cmd.Locked,
		Observe:                    cmd.Observe,
		Debug:                      cmd.Debug,
		NoBye:                      cmd.NoBye,
		Workspace:                  workspace,
		ClaudeArgs:                 cmd.ClaudeArgs,
		DangerousAutoApprove:        cmd.DangerousAutoApprove,
		DangerousAutoApproveNetwork: cmd.DangerousAutoApproveNetwork,
	}

	return runSession(cfg)
}
