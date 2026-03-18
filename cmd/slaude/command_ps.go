package main

import (
	"fmt"

	"github.com/sttts/slagent/cmd/slaude/internal/session"
)

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
