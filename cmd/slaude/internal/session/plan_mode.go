package session

import (
	"fmt"

	"github.com/sttts/slagent"
)

// approvePlanModeTransition posts a Slack prompt for plan mode enter/exit,
// waits for the owner's reaction, and transitions mode on approval.
// toolInput is the raw JSON tool input (used to extract the plan on exit).
// Signals the result on s.planApproval so the MCP handler can relay it to Claude.
func (s *Session) approvePlanModeTransition(isEnter bool, toolInput string) {
	// Signal result so handlePlanModePermission (MCP goroutine) can respond.
	approved := false
	defer func() {
		select {
		case s.planApproval <- approved:
		default:
		}
	}()

	if s.thread == nil {
		approved = true
		return
	}

	emoji := s.thread.Emoji()
	label := "exit"
	if isEnter {
		label = "enter"
	}

	prompt := fmt.Sprintf("%s 🗳️ *Claude wants to %s plan mode.*", emoji, label)
	if ownerID := s.thread.OwnerID(); ownerID != "" {
		prompt += fmt.Sprintf(" <@%s>", ownerID)
	}

	reactions := []string{"white_check_mark", "x"}
	msgTS, err := s.thread.PostPrompt(prompt, reactions)
	if err != nil {
		return
	}

	// Poll for owner reaction
	selected, err := slagent.PollReaction(s.ctx, s.thread, msgTS, reactions, permissionTimeout)
	if err != nil {
		s.thread.DeleteMessage(msgTS)
		return
	}

	switch selected {
	case "white_check_mark":
		s.thread.DeleteMessage(msgTS)
		if isEnter {
			s.thread.SetModeSuffix(" — 📋 planning")
			s.thread.Post(emoji + " 📋 Entered plan mode")
		} else {
			s.thread.SetModeSuffix("")
			s.thread.Post(emoji + " ⚡ Exited plan mode")

			// Post the plan as a code block
			if block := toolCodeBlock("ExitPlanMode", toolInput); block != "" {
				s.thread.Post(emoji + " " + block)
			}
		}
		s.ui.ToolActivity(fmt.Sprintf("  ✅ Approved: %s plan mode", label))
		approved = true
		return
	case "x":
		s.thread.DeleteMessage(msgTS)
		s.ui.ToolActivity(fmt.Sprintf("  ❌ Denied: %s plan mode", label))
		return
	}

	s.thread.DeleteMessage(msgTS)
	s.ui.ToolActivity(fmt.Sprintf("  ⏰ Timed out: %s plan mode", label))
}
