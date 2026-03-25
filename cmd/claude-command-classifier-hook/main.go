// Command claude-command-classifier-hook is a Claude Code PreToolUse hook
// that classifies tool calls by risk level using AI.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/sttts/slagent/cmd/slaude/shared/classify"
)

// hookInput is the JSON read from stdin for a PreToolUse hook.
type hookInput struct {
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	HookEventName string          `json:"hook_event_name"`
}

// hookOutput is the JSON written to stdout.
type hookOutput struct {
	HookSpecificOutput hookSpecific `json:"hookSpecificOutput"`
}

type hookSpecific struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason"`
	AdditionalContext        string `json:"additionalContext,omitempty"`
}

// safeTools are auto-approved without classification.
var safeTools = map[string]bool{
	"TodoWrite":  true,
	"TaskCreate": true,
	"TaskUpdate": true,
	"TaskGet":    true,
	"TaskList":   true,
	"TaskOutput": true,
	"TaskStop":   true,
}

var logFile *os.File

func main() {
	autoApproveFlag := flag.String("auto-approve", "", "auto-approve threshold: never, green, yellow (overrides config)")
	autoApproveNetFlag := flag.String("auto-approve-network", "", "auto-approve network policy: never, known, any (overrides config)")
	notApprovedFlag := flag.String("not-approved", "passthrough", "action for non-approved tools: passthrough (defer to Claude Code defaults) or ask (prompt user)")
	logFileFlag := flag.String("log-file", "", "path to log file for classification decisions")
	flag.Parse()

	// Set up file logging
	if *logFileFlag != "" {
		f, err := os.OpenFile(*logFileFlag, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err == nil {
			logFile = f
			defer logFile.Close()
		}
	}

	// Load config
	cfg := classify.LoadConfig()

	// CLI flags override config
	if *autoApproveFlag != "" {
		cfg.AutoApprove = *autoApproveFlag
	}
	if *autoApproveNetFlag != "" {
		cfg.AutoApproveNetwork = *autoApproveNetFlag
	}

	passthrough := *notApprovedFlag == "passthrough"

	// Read hook input from stdin
	var input hookInput
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		if passthrough {
			logf("passthrough: failed to read hook input: %v", err)
			return
		}
		writeResult("ask", fmt.Sprintf("Failed to read hook input: %v", err), "")
		return
	}

	toolDesc := input.ToolName

	// Auto-approve safe tools
	if safeTools[input.ToolName] {
		logf("allow: %s (safe tool): %s", toolDesc, string(input.ToolInput))
		writeResult("allow", fmt.Sprintf("Auto-approved safe tool: %s", input.ToolName), "")
		return
	}

	// EnterPlanMode is safe (just switches to planning); ExitPlanMode needs approval
	if input.ToolName == "EnterPlanMode" {
		logf("allow: %s (safe tool): %s", toolDesc, string(input.ToolInput))
		writeResult("allow", "Auto-approved safe tool: EnterPlanMode", "")
		return
	}
	if input.ToolName == "ExitPlanMode" {
		logf("ask: %s (plan mode exit must be approved): %s", toolDesc, string(input.ToolInput))
		writeResult("ask", "Plan mode exit: ExitPlanMode", "")
		return
	}

	// Run AI classification
	backend := classify.DefaultBackend()
	t0 := time.Now()
	cls, clsErr := classify.ClassifyWith(context.Background(), backend, input.ToolName, input.ToolInput, cfg.Rules...)
	dur := time.Since(t0)

	if clsErr != nil {
		if passthrough {
			logf("[%.1fs/%s] passthrough: %s classification failed: %v: %s", dur.Seconds(), backend.Name(), toolDesc, clsErr, string(input.ToolInput))
			return
		}
		logf("[%.1fs/%s] ask: %s classification failed: %v: %s", dur.Seconds(), backend.Name(), toolDesc, clsErr, string(input.ToolInput))
		writeResult("ask", fmt.Sprintf("Classification failed: %v", clsErr), "")
		return
	}

	// Build known hosts for network checks
	knownHosts := cfg.KnownHosts
	if knownHosts == nil {
		knownHosts = &classify.KnownHostSet{Dests: append([]classify.KnownDest{}, classify.DefaultKnownDests...)}
	}

	// Check against thresholds
	emoji := classify.LevelEmoji(cls.Level)
	sandboxOK := classify.LevelAllowed(cls.Level, cfg.AutoApprove)
	networkOK := true
	if cls.Network {
		switch cfg.AutoApproveNetwork {
		case "any":
			networkOK = true
		case "known":
			for _, dst := range cls.NetworkDests() {
				if !knownHosts.MatchRequest(dst, cls.NetworkPath, cls.Method) {
					networkOK = false
					break
				}
			}
		default:
			networkOK = false
		}
	}

	if sandboxOK && networkOK {
		var reason string
		if cls.Network {
			knownTag := "unknown"
			allKnown := true
			for _, dst := range cls.NetworkDests() {
				if !knownHosts.MatchRequest(dst, cls.NetworkPath, cls.Method) {
					allKnown = false
					break
				}
			}
			if allKnown {
				knownTag = "known"
			}
			reason = fmt.Sprintf("%s %s (%s+%s) %s", emoji, input.ToolName, cls.Level, knownTag, cls.Reasoning)
		} else {
			reason = fmt.Sprintf("%s %s (%s) %s", emoji, input.ToolName, cls.Level, cls.Reasoning)
		}
		logf("[%.1fs/%s] allow: %s %s %s: %s", dur.Seconds(), backend.Name(), toolDesc, emoji, cls.Reasoning, string(input.ToolInput))
		writeResult("allow", reason, fmt.Sprintf("Classification: %s, network: %v", cls.Level, cls.Network))
		return
	}

	// Outside threshold — ask user or passthrough
	var detail strings.Builder
	fmt.Fprintf(&detail, "%s %s", emoji, cls.Reasoning)
	if cls.Network {
		dest := cls.NetworkDst + cls.NetworkPath
		if cls.Method != "" {
			dest = cls.Method + " " + dest
		}
		fmt.Fprintf(&detail, " [%s → %s]", strings.ToUpper(cls.Level), dest)
	} else {
		fmt.Fprintf(&detail, " [%s]", strings.ToUpper(cls.Level))
	}

	if passthrough {
		logf("[%.1fs/%s] passthrough: %s %s: %s", dur.Seconds(), backend.Name(), toolDesc, detail.String(), string(input.ToolInput))
		return
	}
	logf("[%.1fs/%s] ask: %s %s: %s", dur.Seconds(), backend.Name(), toolDesc, detail.String(), string(input.ToolInput))
	writeResult("ask", detail.String(), fmt.Sprintf("Classification: %s, network: %v dst=%s", cls.Level, cls.Network, cls.NetworkDst))
}

func logf(format string, args ...any) {
	if logFile != nil {
		msg := fmt.Sprintf(format, args...)
		log.New(logFile, "", 0).Printf("%s %s", time.Now().Format("2006-01-02T15:04:05.000"), msg)
	}
}

func writeResult(decision, reason, context string) {
	out := hookOutput{
		HookSpecificOutput: hookSpecific{
			HookEventName:            "PreToolUse",
			PermissionDecision:       decision,
			PermissionDecisionReason: reason,
			AdditionalContext:        context,
		},
	}
	json.NewEncoder(os.Stdout).Encode(out)
}
