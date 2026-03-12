package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sttts/slagent"
	"github.com/sttts/slagent/cmd/slaude/internal/perms"
)

const permissionTimeout = 5 * time.Minute

// handlePermission processes a permission request from the MCP server.
func (s *Session) handlePermission(req *perms.PermissionRequest) *perms.PermissionResponse {
	if s.debugLog != nil {
		raw, _ := json.Marshal(req)
		fmt.Fprintf(s.debugLog, "permission_request: %s\n", raw)
		s.ui.Info(fmt.Sprintf("  🔐 Raw: %s", raw))
	}

	detail := toolDetail(req.ToolName, string(req.Input))

	// Auto-approve safe interactive tools
	switch req.ToolName {
	case "TodoWrite", "TaskCreate", "TaskUpdate", "TaskGet", "TaskList":
		s.ui.ToolActivity(fmt.Sprintf("  ✅ %s: %s", req.ToolName, detail))
		return &perms.PermissionResponse{Behavior: "allow"}
	case "AskUserQuestion":
		return s.handleAskUserQuestion(req)
	case "EnterPlanMode", "ExitPlanMode":
		// Approval prompt is shown from the tool_use handler
		// (approvePlanModeTransition). Wait for its result here so Claude
		// gets the correct allow/deny MCP response.
		select {
		case approved := <-s.planApproval:
			if approved {
				return &perms.PermissionResponse{Behavior: "allow"}
			}
			return &perms.PermissionResponse{Behavior: "deny", Message: "denied via Slack"}
		case <-s.ctx.Done():
			return &perms.PermissionResponse{Behavior: "deny", Message: "session cancelled"}
		}
	}

	// Classify via AI
	s.ui.ToolActivity(fmt.Sprintf("  🔐 %s: %s — classifying...", req.ToolName, detail))
	cls, clsErr := classifyPermission(s.ctx, req.ToolName, req.Input)
	if clsErr != nil {
		s.ui.Error(fmt.Sprintf("classification error: %v", clsErr))
		if s.debugLog != nil {
			fmt.Fprintf(s.debugLog, "classification_error: %v\n", clsErr)
		}
	}
	if s.ctx.Err() != nil {
		return &perms.PermissionResponse{Behavior: "deny", Message: "session cancelled"}
	}

	// Terminal display
	emoji := levelEmoji(cls.Level)
	var netTag string
	if cls.Network {
		netTag = "🌐"
	}
	s.ui.ToolActivity(fmt.Sprintf("  %s%s %s: %s — %s", emoji, netTag, req.ToolName, detail, cls.Reasoning))

	// Auto-approve check
	autoApproveLevel := s.cfg.DangerousAutoApprove
	autoApproveNet := s.cfg.DangerousAutoApproveNetwork
	if autoApproveLevel == "" {
		autoApproveLevel = "never"
	}
	if autoApproveNet == "" {
		autoApproveNet = "never"
	}

	sandboxOK := levelAllowed(cls.Level, autoApproveLevel)
	networkOK := true
	if cls.Network {
		switch autoApproveNet {
		case "any":
			networkOK = true
		case "known":
			networkOK = s.knownHosts.matchRequest(cls.NetworkDst, cls.NetworkPath, cls.Method)
		default:
			networkOK = false
		}
	}

	if sandboxOK && networkOK {
		var reason string
		if cls.Network {
			knownTag := "unknown"
			if s.knownHosts.matchRequest(cls.NetworkDst, cls.NetworkPath, cls.Method) {
				knownTag = "known"
			}
			reason = fmt.Sprintf("%s+%s", cls.Level, knownTag)
		} else {
			reason = cls.Level
		}
		s.ui.ToolActivity(fmt.Sprintf("  ✅ Auto-approved (%s)", reason))
		return &perms.PermissionResponse{Behavior: "allow"}
	}

	// Escalate to Slack
	prompt := fmt.Sprintf("%s%s *Permission request*: %s", emoji, netTag, req.ToolName)
	if detail != "" {
		prompt += ": `" + detail + "`"
	}
	if cls.Network {
		dest := cls.NetworkDst + cls.NetworkPath
		if cls.Method != "" {
			dest = cls.Method + " " + dest
		}
		prompt += fmt.Sprintf("\n> %s risk → `%s` — %s", strings.ToUpper(cls.Level), dest, cls.Reasoning)
	} else {
		prompt += fmt.Sprintf("\n> %s risk — %s", strings.ToUpper(cls.Level), cls.Reasoning)
	}

	var reactions []string
	if cls.Network {
		reactions = []string{"white_check_mark", "floppy_disk", "x"}
	} else {
		reactions = []string{"white_check_mark", "x"}
	}

	msgTS, err := s.thread.PostPrompt(prompt, reactions)
	if err != nil {
		s.ui.ToolActivity("❌ Denied (failed to post to Slack)")
		return &perms.PermissionResponse{Behavior: "deny", Message: "failed to post permission prompt to Slack"}
	}

	// Poll for reaction
	selected, err := slagent.PollReaction(s.ctx, s.thread, msgTS, reactions, permissionTimeout)
	if err != nil {
		s.thread.DeleteMessage(msgTS)
		return &perms.PermissionResponse{Behavior: "deny", Message: "session cancelled"}
	}

	switch selected {
	case "white_check_mark":
		s.thread.DeleteMessage(msgTS)
		s.ui.ToolActivity(fmt.Sprintf("  ✅ Approved: %s: %s", req.ToolName, detail))
		return &perms.PermissionResponse{Behavior: "allow"}
	case "floppy_disk":
		if cls.Network && cls.NetworkDst != "" && cls.NetworkDst != "unknown" {
			s.knownHosts.add(cls.NetworkDst)
			s.ui.Info(fmt.Sprintf("  💾 Remembered %s as known host", cls.NetworkDst))
		}
		s.thread.DeleteMessage(msgTS)
		s.ui.ToolActivity(fmt.Sprintf("  ✅ Approved+saved: %s: %s", req.ToolName, detail))
		return &perms.PermissionResponse{Behavior: "allow"}
	case "x":
		s.thread.DeleteMessage(msgTS)
		s.ui.ToolActivity(fmt.Sprintf("  ❌ Denied: %s: %s", req.ToolName, detail))
		return &perms.PermissionResponse{Behavior: "deny", Message: "denied via Slack"}
	}

	s.thread.DeleteMessage(msgTS)
	s.ui.ToolActivity(fmt.Sprintf("  ⏰ Timed out: %s: %s", req.ToolName, detail))
	return &perms.PermissionResponse{Behavior: "deny", Message: "permission request timed out"}
}
