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
	Rules              []string      // extra classification rules appended to the prompt
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

	// Track which section we're in
	var section string // "known-hosts", "rules", or ""
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
				section = ""
				cfg.AutoApproveNetwork = Unquote(strings.TrimSpace(strings.TrimPrefix(trimmed, "auto-approve-network:")))
			} else if strings.HasPrefix(trimmed, "auto-approve:") {
				section = ""
				cfg.AutoApprove = Unquote(strings.TrimSpace(strings.TrimPrefix(trimmed, "auto-approve:")))
			} else if trimmed == "known-hosts:" {
				section = "known-hosts"
			} else if trimmed == "rules:" {
				section = "rules"
			} else {
				section = ""
			}
			continue
		}

		// Indented lines belonging to current section
		switch section {
		case "known-hosts":
			knownHostLines = append(knownHostLines, trimmed)
		case "rules":
			// Parse "- some rule text" entries
			if strings.HasPrefix(trimmed, "- ") {
				rule := Unquote(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
				if rule != "" {
					cfg.Rules = append(cfg.Rules, rule)
				}
			}
		}
	}

	// Parse collected known-hosts lines
	if len(knownHostLines) > 0 {
		content := strings.Join(knownHostLines, "\n")
		s := bufio.NewScanner(strings.NewReader(content))
		if dests, err := parseKnownHostsFromScanner(s); err == nil && len(dests) > 0 {
			cfg.KnownHosts = &KnownHostSet{Dests: dests}
		}
	}

	return cfg
}
