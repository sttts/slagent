package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sttts/slagent/cmd/slaude/internal/perms"
)

// knownDest is a known-safe network destination.
type knownDest struct {
	Host    string          // exact host or glob pattern (e.g. "*.github.com")
	Path    string          // optional URL path glob (e.g. "/repos/**"); empty = any path
	Methods map[string]bool // optional allowed HTTP methods (e.g. GET, HEAD); nil = any method
}

// knownHostSet holds known-safe network destinations for auto-approve.
type knownHostSet struct {
	dests []knownDest
}

// match returns true if host matches a known destination (any path, any method).
func (k *knownHostSet) match(host string) bool {
	return k.matchRequest(host, "", "")
}

// matchRequest returns true if host + URL path + method matches a known destination.
// Empty urlPath or method means "any".
func (k *knownHostSet) matchRequest(host, urlPath, method string) bool {
	for _, d := range k.dests {
		if !matchHostPattern(d.Host, host) && d.Host != host {
			continue
		}
		if d.Path != "" && (urlPath == "" || !matchPathPattern(d.Path, urlPath)) {
			continue
		}
		if d.Methods != nil {
			if method == "" || !d.Methods[strings.ToUpper(method)] {
				continue
			}
		}
		return true
	}
	return false
}

// matchHostPattern matches a host against a DNS-aware glob pattern.
//   - "*" matches exactly one DNS label (no dots)
//   - "**" matches one or more DNS labels
func matchHostPattern(pattern, host string) bool {
	return matchParts(strings.Split(pattern, "."), strings.Split(host, "."))
}

// matchParts recursively matches pattern parts against host parts.
func matchParts(pparts, hparts []string) bool {
	for len(pparts) > 0 && len(hparts) > 0 {
		p := pparts[0]
		if p == "**" {
			rest := pparts[1:]
			for i := 1; i <= len(hparts)-len(rest); i++ {
				if matchParts(rest, hparts[i:]) {
					return true
				}
			}
			return false
		}
		if p != "*" && p != hparts[0] {
			return false
		}
		pparts = pparts[1:]
		hparts = hparts[1:]
	}
	return len(pparts) == 0 && len(hparts) == 0
}

// matchPathPattern matches a URL path against a glob pattern using "/" as separator.
func matchPathPattern(pattern, urlPath string) bool {
	return matchParts(splitPath(pattern), splitPath(urlPath))
}

// splitPath splits a path into segments, stripping leading/trailing slashes.
func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// add adds an exact host to the set.
func (k *knownHostSet) add(host string) {
	k.dests = append(k.dests, knownDest{Host: host})
}

// readOnly is the default method set: GET and HEAD only.
var readOnly = map[string]bool{"GET": true, "HEAD": true}

// defaultKnownDests are used when no known-hosts.yaml exists.
var defaultKnownDests = []knownDest{
	{Host: "github.com", Methods: readOnly},
	{Host: "api.github.com", Methods: readOnly},
	{Host: "raw.githubusercontent.com", Methods: readOnly},
	{Host: "proxy.golang.org", Methods: readOnly},
	{Host: "sum.golang.org", Methods: readOnly},
	{Host: "registry.npmjs.org", Methods: readOnly},
	{Host: "pypi.org", Methods: readOnly},
	{Host: "files.pythonhosted.org", Methods: readOnly},
	{Host: "rubygems.org", Methods: readOnly},
	{Host: "crates.io", Methods: readOnly},
	{Host: "static.crates.io", Methods: readOnly},
}

// knownHostsPaths returns candidate paths for known-hosts.yaml.
func knownHostsPaths() []string {
	var paths []string
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "slagent", "known-hosts.yaml"))
	}
	return paths
}

// loadKnownHosts loads the known host set from ~/.config/slagent/known-hosts.yaml,
// falling back to built-in defaults if the file doesn't exist.
func loadKnownHosts() *knownHostSet {
	set := &knownHostSet{}
	for _, p := range knownHostsPaths() {
		if dests, err := parseKnownHostsFile(p); err == nil {
			set.dests = dests
			return set
		}
	}
	set.dests = append(set.dests, defaultKnownDests...)
	return set
}

// parseKnownHostsFile reads a known-hosts.yaml file.
func parseKnownHostsFile(filePath string) ([]knownDest, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var dests []knownDest
	var current *knownDest
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "- host:") {
			if current != nil {
				if current.Methods == nil {
					current.Methods = map[string]bool{"GET": true, "HEAD": true}
				}
				dests = append(dests, *current)
			}
			value := unquote(strings.TrimSpace(strings.TrimPrefix(line, "- host:")))
			if value != "" {
				current = &knownDest{Host: value}
			} else {
				current = nil
			}
			continue
		}

		if strings.HasPrefix(line, "path:") && current != nil {
			current.Path = unquote(strings.TrimSpace(strings.TrimPrefix(line, "path:")))
			continue
		}

		if strings.HasPrefix(line, "methods:") && current != nil {
			raw := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "methods:")), "[]")
			current.Methods = make(map[string]bool)
			for _, m := range strings.Split(raw, ",") {
				m = strings.TrimSpace(m)
				if m != "" {
					current.Methods[strings.ToUpper(m)] = true
				}
			}
			continue
		}
	}

	if current != nil {
		if current.Methods == nil {
			current.Methods = map[string]bool{"GET": true, "HEAD": true}
		}
		dests = append(dests, *current)
	}
	return dests, scanner.Err()
}

// unquote strips surrounding single or double quotes from a YAML value.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// workspaceConfig holds per-workspace settings from ~/.config/slagent/config.yaml.
type workspaceConfig struct {
	ThinkingEmoji               string
	DangerousAutoApprove        string
	DangerousAutoApproveNetwork string
}

// loadWorkspaceConfig loads workspace-specific settings from config.yaml.
func loadWorkspaceConfig(workspace string) workspaceConfig {
	home, err := os.UserHomeDir()
	if err != nil {
		return workspaceConfig{}
	}
	return parseConfigFile(filepath.Join(home, ".config", "slagent", "config.yaml"), workspace)
}

// parseConfigFile reads a config.yaml and returns settings for the given workspace.
func parseConfigFile(filePath, workspace string) workspaceConfig {
	f, err := os.Open(filePath)
	if err != nil {
		return workspaceConfig{}
	}
	defer f.Close()

	var cfg workspaceConfig
	var currentWorkspace string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trimmed == "workspaces:" {
			continue
		}
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && strings.HasSuffix(trimmed, ":") {
			currentWorkspace = strings.TrimSuffix(trimmed, ":")
			continue
		}
		if strings.HasPrefix(line, "    ") && currentWorkspace == workspace {
			if strings.HasPrefix(trimmed, "thinking-emoji:") {
				cfg.ThinkingEmoji = unquote(strings.TrimSpace(strings.TrimPrefix(trimmed, "thinking-emoji:")))
			} else if strings.HasPrefix(trimmed, "dangerous-auto-approve-network:") {
				cfg.DangerousAutoApproveNetwork = unquote(strings.TrimSpace(strings.TrimPrefix(trimmed, "dangerous-auto-approve-network:")))
			} else if strings.HasPrefix(trimmed, "dangerous-auto-approve:") {
				cfg.DangerousAutoApprove = unquote(strings.TrimSpace(strings.TrimPrefix(trimmed, "dangerous-auto-approve:")))
			}
		}
	}
	return cfg
}

// autoApproveSummary returns a human-readable summary of the auto-approve policy.
func autoApproveSummary(level, network string) string {
	if level == "" {
		level = "never"
	}
	if network == "" {
		network = "never"
	}
	if level == "never" && network == "never" {
		return ""
	}
	var parts []string
	switch level {
	case "green":
		parts = append(parts, "green (read-only)")
	case "yellow":
		parts = append(parts, "green+yellow (local ops)")
	}
	switch network {
	case "known":
		parts = append(parts, "known hosts")
	case "any":
		parts = append(parts, "any network")
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ")
}

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
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	deadline := time.Now().Add(permissionTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-s.ctx.Done():
			s.thread.DeleteMessage(msgTS)
			return &perms.PermissionResponse{Behavior: "deny", Message: "session cancelled"}
		case <-ticker.C:
		}
		selected, err := s.thread.PollReaction(msgTS, reactions)
		if err != nil {
			continue
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
	}

	s.thread.DeleteMessage(msgTS)
	s.ui.ToolActivity(fmt.Sprintf("  ⏰ Timed out: %s: %s", req.ToolName, detail))
	return &perms.PermissionResponse{Behavior: "deny", Message: "permission request timed out"}
}

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
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	deadline := time.Now().Add(permissionTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-s.ctx.Done():
			s.thread.DeleteMessage(msgTS)
			return
		case <-ticker.C:
		}
		selected, err := s.thread.PollReaction(msgTS, reactions)
		if err != nil {
			continue
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
	}

	s.thread.DeleteMessage(msgTS)
	s.ui.ToolActivity(fmt.Sprintf("  ⏰ Timed out: %s plan mode", label))
}
