package session

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

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
