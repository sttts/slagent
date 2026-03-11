package session

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

func currentUser() string {
	u, err := user.Current()
	if err != nil {
		return "developer"
	}
	return u.Username
}

// formatToolStart returns a short label for a tool_start event (no input yet).
func formatToolStart(toolName string) string {
	switch toolName {
	case "Read":
		return "📄 Read"
	case "Write":
		return "📝 Write"
	case "Edit":
		return "📝 Edit"
	case "Glob":
		return "🔍 Glob"
	case "Grep":
		return "🔍 Grep"
	case "Bash":
		return "💻 Bash"
	case "Agent":
		return "🤖 Agent"
	case "WebFetch":
		return "🌐 WebFetch"
	case "WebSearch":
		return "🔎 WebSearch"
	default:
		return "🔧 " + toolName
	}
}

// hasQuestionsFormat returns true if the AskUserQuestion input uses the new questions array format.
func hasQuestionsFormat(rawInput string) bool {
	var input map[string]interface{}
	if err := json.Unmarshal([]byte(rawInput), &input); err != nil {
		return false
	}
	if raw, ok := input["questions"]; ok {
		if arr, ok := raw.([]interface{}); ok && len(arr) > 0 {
			return true
		}
	}
	return false
}

// promptMsg holds a Slack message with reaction emojis for interactive response.
type promptMsg struct {
	text      string
	reactions []string
}

// Number emoji reaction names for multi-choice options.
var numberReactions = []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}

// interactivePrompt returns a formatted Slack prompt with reactions for interactive tools,
// or nil if the tool is not interactive.
func interactivePrompt(toolName, rawInput, ownerID, emoji string) *promptMsg {
	var input map[string]interface{}
	json.Unmarshal([]byte(rawInput), &input)

	str := func(key string) string {
		if v, ok := input[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}

	mention := ""
	if ownerID != "" {
		mention = fmt.Sprintf(" <@%s>", ownerID)
	}

	prefix := emoji
	if prefix == "" {
		prefix = "❓"
	}

	switch toolName {
	case "ExitPlanMode":
		return &promptMsg{
			text:      fmt.Sprintf("%s 🗳️ *Claude wants to exit plan mode.*%s", prefix, mention),
			reactions: []string{"white_check_mark", "x"},
		}
	case "EnterPlanMode":
		return &promptMsg{
			text:      fmt.Sprintf("%s 🗳️ *Claude wants to enter plan mode.*%s", prefix, mention),
			reactions: []string{"white_check_mark", "x"},
		}
	case "AskUserQuestion":
		// New questions format is handled by handleAskUserQuestion via MCP permission flow.

		// Legacy format: allowedPrompts
		q := str("question")
		if raw, ok := input["allowedPrompts"]; ok {
			if arr, ok := raw.([]interface{}); ok && len(arr) > 0 {
				var lines []string
				var reactions []string
				lines = append(lines, fmt.Sprintf("%s%s\n%s\n", prefix, mention, q))
				for i, opt := range arr {
					if i >= len(numberReactions) {
						break
					}
					label, _ := opt.(string)
					lines = append(lines, fmt.Sprintf("%s  %s", numberEmoji(i), label))
					reactions = append(reactions, numberReactions[i])
				}
				return &promptMsg{
					text:      strings.Join(lines, "\n"),
					reactions: reactions,
				}
			}
		}
		return nil
	}
	return nil
}

// numberEmoji returns a display emoji for an index (0-based).
func numberEmoji(i int) string {
	emojis := []string{"1️⃣", "2️⃣", "3️⃣", "4️⃣", "5️⃣", "6️⃣", "7️⃣", "8️⃣", "9️⃣"}
	if i < len(emojis) {
		return emojis[i]
	}
	return fmt.Sprintf("%d.", i+1)
}

// toolCodeBlock returns a code-block message for Edit (unified diff) or Write (content preview).
func toolCodeBlock(toolName, rawInput string) string {
	var input map[string]interface{}
	json.Unmarshal([]byte(rawInput), &input)

	str := func(key string) string {
		if v, ok := input[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}

	switch toolName {
	case "Edit":
		fp := str("file_path")
		old := str("old_string")
		new := str("new_string")
		if fp == "" || (old == "" && new == "") {
			return ""
		}
		name := filepath.Base(fp)
		var b strings.Builder
		fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", name, name)
		for _, line := range strings.Split(old, "\n") {
			fmt.Fprintf(&b, "-%s\n", line)
		}
		for _, line := range strings.Split(new, "\n") {
			fmt.Fprintf(&b, "+%s\n", line)
		}
		diff := strings.TrimRight(b.String(), "\n")
		diff = strings.ReplaceAll(diff, "```", "'''")
		return fmt.Sprintf("📝 %s\n```\n%s\n```", name, diff)

	case "Write":
		fp := str("file_path")
		content := str("content")
		if fp == "" || content == "" {
			return ""
		}
		name := filepath.Base(fp)
		lines := strings.Split(content, "\n")
		truncated := false
		if len(lines) > 15 {
			lines = lines[:15]
			truncated = true
		}
		preview := strings.Join(lines, "\n")
		if truncated {
			preview += "\n..."
		}
		preview = strings.ReplaceAll(preview, "```", "'''")
		label := fmt.Sprintf("📝 %s (new, %d lines)", name, len(strings.Split(content, "\n")))
		return fmt.Sprintf("%s\n```\n%s\n```", label, preview)
	}
	return ""
}

// toolDetail extracts the raw detail string for slagent (no emoji).
func toolDetail(toolName, rawInput string) string {
	var input map[string]interface{}
	json.Unmarshal([]byte(rawInput), &input)

	str := func(key string) string {
		if v, ok := input[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}

	switch toolName {
	case "Read", "Write", "Edit":
		return filepath.Base(str("file_path"))
	case "Glob":
		return str("pattern")
	case "Grep":
		p := str("pattern")
		if path := str("path"); path != "" {
			return p + " in " + filepath.Base(path)
		}
		return p
	case "Bash":
		return truncate(str("command"), 60)
	case "Agent":
		if d := str("description"); d != "" {
			return d
		}
		return truncate(str("prompt"), 60)
	case "WebFetch":
		return str("url")
	case "WebSearch":
		return str("query")
	case "ExitPlanMode":
		return "ready for approval"
	case "EnterPlanMode":
		return "switching to plan mode"
	case "AskUserQuestion":
		return str("question")
	case "ToolSearch":
		return str("query")
	case "TodoWrite", "TaskCreate", "TaskUpdate":
		return str("subject")
	case "Skill":
		return str("skill")
	default:
		return truncate(rawInput, 60)
	}
}

// formatTool returns a pretty one-line summary of a tool use.
func formatTool(toolName, rawInput string) string {
	var input map[string]interface{}
	json.Unmarshal([]byte(rawInput), &input)

	str := func(key string) string {
		if v, ok := input[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}

	switch toolName {
	case "Read":
		return fmt.Sprintf("📄 %s", filepath.Base(str("file_path")))
	case "Write":
		return fmt.Sprintf("📝 %s (new)", filepath.Base(str("file_path")))
	case "Edit":
		return fmt.Sprintf("📝 %s", filepath.Base(str("file_path")))
	case "Glob":
		return fmt.Sprintf("🔍 %s", str("pattern"))
	case "Grep":
		p := str("pattern")
		if path := str("path"); path != "" {
			return fmt.Sprintf("🔍 %s in %s", p, filepath.Base(path))
		}
		return fmt.Sprintf("🔍 %s", p)
	case "Bash":
		return fmt.Sprintf("💻 %s", truncate(str("command"), 60))
	case "Agent":
		if d := str("description"); d != "" {
			return fmt.Sprintf("🤖 %s", d)
		}
		return fmt.Sprintf("🤖 %s", truncate(str("prompt"), 60))
	case "WebFetch":
		return fmt.Sprintf("🌐 %s", str("url"))
	case "WebSearch":
		return fmt.Sprintf("🔎 %s", str("query"))
	case "ToolSearch":
		return fmt.Sprintf("🔍 %s", str("query"))
	case "TodoWrite", "TaskCreate", "TaskUpdate":
		return fmt.Sprintf("📋 %s", str("subject"))
	case "ExitPlanMode":
		return "📋 ready for approval"
	case "EnterPlanMode":
		return "📋 switching to plan mode"
	case "AskUserQuestion":
		return fmt.Sprintf("❓ %s", str("question"))
	case "Skill":
		return fmt.Sprintf("⚡ %s", str("skill"))
	default:
		return fmt.Sprintf("🔧 %s: %s", toolName, truncate(rawInput, 60))
	}
}

// findArg returns the index of the given flag in args, or -1 if not found.
func findArg(args []string, flag string) int {
	for i, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return i
		}
	}
	return -1
}

// hasArg returns true if the given flag appears in args.
func hasArg(args []string, flag string) bool {
	return findArg(args, flag) >= 0
}

// soulPaths returns candidate paths for SOUL.md, in priority order.
func soulPaths() []string {
	paths := []string{"SOUL.md"}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "slagent", "SOUL.md"))
	}
	return paths
}


// truncate shortens s to max characters with "..." suffix.
func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max-3] + "..."
	}
	return s
}
