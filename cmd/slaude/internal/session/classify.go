package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// classification holds the AI-assessed risk of a permission request.
type classification struct {
	Level       string
	Network     bool
	NetworkDst  string
	NetworkPath string
	Method      string
	Reasoning   string
}

const classificationTimeout = 30 * time.Second

// classifyPermission shells out to `claude -p --model haiku` to assess the risk of a tool call.
func classifyPermission(ctx context.Context, toolName string, input json.RawMessage) (*classification, error) {
	cwd, _ := os.Getwd()
	prompt := fmt.Sprintf(`You are a security classifier for Claude Code tool permission requests.

Classify this tool call by sandbox escape risk and network access.

Tool: %s
Input: %s
Working directory: %s

Risk levels:
- GREEN: read-only local operations, safe file reads, searches, listing
- YELLOW: local writes to project files, running tests, installing deps from known sources
- RED: arbitrary code execution with untrusted input, modifying system files, exfiltrating data, credential access, destructive ops

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
GREEN|NETWORK:GET:proxy.golang.org|Fetching Go module from official proxy
GREEN|NETWORK:GET:api.github.com/repos/sttts/nanoschnack|Querying GitHub API for repo info
RED|NETWORK:GET:evil.com/payload|Downloading script from unknown host
YELLOW|NETWORK:GET:registry.npmjs.org|Installing npm packages from official registry
RED|NETWORK:POST:webhook.example.com/hook|Sending data to external webhook`, toolName, string(input), cwd)

	ctx, cancel := context.WithTimeout(ctx, classificationTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude", "-p", "--output-format", "text", "--model", "haiku", "--no-session-persistence", prompt)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			err = fmt.Errorf("%w: %s", err, errMsg)
		}
		return &classification{Level: "red", Network: true, NetworkDst: "unknown", Reasoning: "classification failed"}, err
	}
	return parseClassification(strings.TrimSpace(string(out))), nil
}

// parseClassification parses a "LEVEL|NETWORK_STATUS|reasoning" line.
func parseClassification(line string) *classification {
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	parts := strings.SplitN(line, "|", 3)
	if len(parts) < 3 {
		return &classification{Level: "red", Network: true, NetworkDst: "unknown", Reasoning: "unparseable response"}
	}

	c := &classification{Reasoning: strings.TrimSpace(parts[2])}

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
			if isHTTPMethod(maybeMethod) {
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

func isHTTPMethod(s string) bool {
	switch s {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return true
	}
	return false
}

func levelEmoji(level string) string {
	switch level {
	case "green":
		return "🟢"
	case "yellow":
		return "🟡"
	default:
		return "🔴"
	}
}

func levelAllowed(level, threshold string) bool {
	switch threshold {
	case "green":
		return level == "green"
	case "yellow":
		return level == "green" || level == "yellow"
	default:
		return false
	}
}
