package classify

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Config holds shared classifier settings from ~/.config/slagent/classifier.yaml.
type Config struct {
	AutoApprove        string        // "never", "green", "yellow"
	AutoApproveNetwork string        // "never", "known", "any"
	KnownHosts         *KnownHostSet // nil means use defaults
}

// LoadConfig reads classifier settings from ~/.config/slagent/classifier.yaml.
// Falls back to defaults (auto-approve=never, auto-approve-network=never, default known hosts).
func LoadConfig() Config {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{AutoApprove: "never", AutoApproveNetwork: "never"}
	}
	return ParseConfigFile(filepath.Join(home, ".config", "slagent", "classifier.yaml"))
}

// ParseConfigFile reads a classifier.yaml and returns the config.
func ParseConfigFile(filePath string) Config {
	cfg := Config{AutoApprove: "never", AutoApproveNetwork: "never"}

	f, err := os.Open(filePath)
	if err != nil {
		return cfg
	}
	defer f.Close()

	// Track whether we've entered the known-hosts section
	var inKnownHosts bool
	var knownHostLines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Top-level keys (no indentation)
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			if strings.HasPrefix(trimmed, "auto-approve-network:") {
				inKnownHosts = false
				cfg.AutoApproveNetwork = Unquote(strings.TrimSpace(strings.TrimPrefix(trimmed, "auto-approve-network:")))
			} else if strings.HasPrefix(trimmed, "auto-approve:") {
				inKnownHosts = false
				cfg.AutoApprove = Unquote(strings.TrimSpace(strings.TrimPrefix(trimmed, "auto-approve:")))
			} else if trimmed == "known-hosts:" {
				inKnownHosts = true
			} else {
				inKnownHosts = false
			}
			continue
		}

		// Indented lines belonging to known-hosts
		if inKnownHosts {
			knownHostLines = append(knownHostLines, trimmed)
		}
	}

	// Parse collected known-hosts lines
	if len(knownHostLines) > 0 {
		// Re-parse as if it were a standalone known-hosts file
		content := strings.Join(knownHostLines, "\n")
		s := bufio.NewScanner(strings.NewReader(content))
		if dests, err := parseKnownHostsFromScanner(s); err == nil && len(dests) > 0 {
			cfg.KnownHosts = &KnownHostSet{Dests: dests}
		}
	}

	return cfg
}
