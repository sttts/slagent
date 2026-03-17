// Package classify provides AI-based tool call risk classification.
package classify

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Classification holds the AI-assessed risk of a permission request.
type Classification struct {
	Level       string
	Network     bool
	NetworkDst  string
	NetworkPath string
	Method      string
	Reasoning   string
}

const classificationTimeout = 30 * time.Second

// Classify assesses the risk of a tool call using the default backend.
// Optional rules are appended to the classification prompt.
func Classify(ctx context.Context, toolName string, input json.RawMessage, rules ...string) (*Classification, error) {
	return ClassifyWith(ctx, DefaultBackend(), toolName, input, rules...)
}

// ClassifyWith assesses the risk of a tool call using the given backend.
func ClassifyWith(ctx context.Context, backend Backend, toolName string, input json.RawMessage, rules ...string) (*Classification, error) {
	prompt := buildPrompt(toolName, string(input), rules)

	ctx, cancel := context.WithTimeout(ctx, classificationTimeout)
	defer cancel()

	out, err := backend.Complete(ctx, prompt)
	if err != nil {
		return &Classification{Level: "red", Network: true, NetworkDst: "unknown", Reasoning: "classification failed"}, err
	}
	return Parse(out), nil
}

// buildPrompt constructs the classification prompt.
func buildPrompt(toolName, input string, rules []string) string {
	// Strip dangerouslyDisableSandbox from what haiku sees — it's a Claude Code
	// runtime flag that consistently misleads the classifier into bumping risk.
	// Strip Claude Code runtime fields that mislead the classifier.
	// - dangerouslyDisableSandbox: runtime flag, not a security indicator
	// - description: Claude's own summary that biases haiku ("mutation", "push")
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(input), &m) == nil {
		changed := false
		for _, key := range []string{"dangerouslyDisableSandbox", "description"} {
			if _, ok := m[key]; ok {
				delete(m, key)
				changed = true
			}
		}
		if changed {
			if cleaned, err := json.Marshal(m); err == nil {
				input = string(cleaned)
			}
		}
	}

	cwd, _ := os.Getwd()
	prompt := fmt.Sprintf(`You are a security classifier for Claude Code tool permission requests.

Classify this tool call by sandbox escape risk and network access.

Tool: %s
Input: %s
Working directory (project root): %s

IMPORTANT — path traversal rules:
- File paths that stay WITHIN the working directory are safe (GREEN for reads, YELLOW for writes).
- Paths that escape the working directory via "..", absolute paths outside it, or symlinks pointing outside it are DANGEROUS. Reading files outside the project is at least YELLOW (information disclosure risk). Writing or executing outside is RED.
- Resolve relative paths against the working directory to determine if they escape. For example, if working directory is /home/user/project, then "../foo" resolves to /home/user/foo which is OUTSIDE the project.
- Be especially wary of paths targeting home directories, /etc, /tmp, credential files, or other sensitive locations.

Risk levels:
- GREEN: read-only operations on files WITHIN the project directory, safe searches, listing. Also GREEN: read-only network access to well-known developer services (GitHub, GitLab, Go proxy, npm, PyPI, etc.) via standard CLI tools (glab, gh, curl for APIs, go mod download). Network access to known-safe hosts for reading data is GREEN, not YELLOW.
- YELLOW: local writes to project files, running tests, installing deps from known sources, reading files OUTSIDE the project directory, network writes (POST/PUT/DELETE) to known services
- RED: writing or executing outside the project, arbitrary code execution with untrusted input, modifying system files, exfiltrating data, credential access, destructive ops, network access to unknown/untrusted hosts

Network: does this operation access the network? If yes, what destination, path, and HTTP method?

Respond with EXACTLY one line in this format:
LEVEL|NETWORK_STATUS|reasoning

Where:
- LEVEL is GREEN, YELLOW, or RED
- NETWORK_STATUS is either "NONE" or "NETWORK:METHOD:host/path" (e.g. "NETWORK:GET:api.github.com/repos/foo" or "NETWORK:GET:proxy.golang.org" or "NETWORK:unknown")
- METHOD is the HTTP method (GET, POST, PUT, DELETE, etc.) or omitted if unknown
- reasoning is a short one-sentence explanation

Examples:
GREEN|NONE|Reading source file within project
YELLOW|NONE|Writing test file in project directory
YELLOW|NONE|Reading file outside project directory via path traversal
RED|NONE|Writing to file outside project directory
GREEN|NETWORK:GET:proxy.golang.org|Fetching Go module from official proxy
GREEN|NETWORK:GET:api.github.com/repos/sttts/nanoschnack|Querying GitHub API for repo info
RED|NETWORK:GET:evil.com/payload|Downloading script from unknown host
YELLOW|NETWORK:GET:registry.npmjs.org|Installing npm packages from official registry
RED|NETWORK:POST:webhook.example.com/hook|Sending data to external webhook
GREEN|NONE|Standard git add and commit within project directory
GREEN|NETWORK:GET:known-host.example.com|Read-only CLI tool accessing known developer service`, toolName, input, cwd)

	// Split rules: entries matching LEVEL|...|... go into examples, rest are rules
	var extraRules, extraExamples []string
	for _, r := range rules {
		parts := strings.SplitN(r, "|", 3)
		if len(parts) == 3 {
			level := strings.TrimSpace(strings.ToUpper(parts[0]))
			if level == "GREEN" || level == "YELLOW" || level == "RED" {
				extraExamples = append(extraExamples, r)
				continue
			}
		}
		extraRules = append(extraRules, r)
	}

	var b strings.Builder
	b.WriteString(prompt)

	// Inject examples into the prompt (haiku respects these more than rules)
	if len(extraExamples) > 0 {
		b.WriteByte('\n')
		for _, ex := range extraExamples {
			b.WriteString(ex)
			b.WriteByte('\n')
		}
	}

	// Append rules as instructions
	if len(extraRules) > 0 {
		b.WriteString("\n\nAdditional project-specific classification rules:\n")
		for _, r := range extraRules {
			b.WriteString("- ")
			b.WriteString(r)
			b.WriteByte('\n')
		}
	}

	return b.String()
}

// Parse parses a "LEVEL|NETWORK_STATUS|reasoning" line.
func Parse(line string) *Classification {
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	parts := strings.SplitN(line, "|", 3)
	if len(parts) < 3 {
		return &Classification{Level: "red", Network: true, NetworkDst: "unknown", Reasoning: "unparseable response"}
	}

	c := &Classification{Reasoning: strings.TrimSpace(parts[2])}

	switch strings.TrimSpace(strings.ToUpper(parts[0])) {
	case "GREEN":
		c.Level = "green"
	case "YELLOW":
		c.Level = "yellow"
	default:
		c.Level = "red"
	}

	netPart := strings.TrimSpace(parts[1])
	if strings.HasPrefix(strings.ToUpper(netPart), "NETWORK:") {
		c.Network = true
		rest := strings.TrimSpace(netPart[len("NETWORK:"):])
		if colonIdx := strings.Index(rest, ":"); colonIdx > 0 {
			maybeMethod := strings.ToUpper(rest[:colonIdx])
			if IsHTTPMethod(maybeMethod) {
				c.Method = maybeMethod
				rest = rest[colonIdx+1:]
			}
		}
		if slashIdx := strings.Index(rest, "/"); slashIdx > 0 {
			c.NetworkDst = rest[:slashIdx]
			c.NetworkPath = rest[slashIdx:]
		} else {
			c.NetworkDst = rest
		}
		if c.NetworkDst == "" {
			c.NetworkDst = "unknown"
		}
	}
	return c
}

// NetworkDests returns all network destinations, splitting comma-separated values.
// Haiku sometimes returns multiple hosts like "proxy.golang.org,sum.golang.org".
func (c *Classification) NetworkDests() []string {
	if c.NetworkDst == "" {
		return nil
	}
	var dests []string
	for _, d := range strings.Split(c.NetworkDst, ",") {
		d = strings.TrimSpace(d)
		if d != "" {
			dests = append(dests, d)
		}
	}
	return dests
}

// IsHTTPMethod returns true if s is a recognized HTTP method.
func IsHTTPMethod(s string) bool {
	switch s {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return true
	}
	return false
}

// LevelEmoji returns the emoji for a classification level.
func LevelEmoji(level string) string {
	switch level {
	case "green":
		return "🟢"
	case "yellow":
		return "🟡"
	default:
		return "🔴"
	}
}

// LevelAllowed returns true if level is within the auto-approve threshold.
func LevelAllowed(level, threshold string) bool {
	switch threshold {
	case "green":
		return level == "green"
	case "yellow":
		return level == "green" || level == "yellow"
	default:
		return false
	}
}
