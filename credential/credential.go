// Package credential manages Slack credentials for slagent.
package credential

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Credentials holds the stored Slack token.
type Credentials struct {
	Token    string `json:"token,omitempty"`
	Type     string `json:"type,omitempty"`      // "bot", "user", or "session"
	Cookie   string `json:"cookie,omitempty"` // xoxd-... for xoxc session tokens
}

// EffectiveToken returns the token.
func (c *Credentials) EffectiveToken() string {
	return c.Token
}

// EffectiveType returns the token type, inferring from prefix if not set.
func (c *Credentials) EffectiveType() string {
	if c.Type != "" {
		return c.Type
	}
	token := c.EffectiveToken()
	switch {
	case strings.HasPrefix(token, "xoxp-"):
		return "user"
	case strings.HasPrefix(token, "xoxc-"):
		return "session"
	default:
		return "bot"
	}
}

// Path returns the path to the credentials file.
func Path() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "slagent", "credentials.json")
}

// Load reads stored credentials.
func Load() (*Credentials, error) {
	data, err := os.ReadFile(Path())
	if err != nil {
		return nil, fmt.Errorf("no credentials found (run 'slaude auth'): %w", err)
	}
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	if creds.EffectiveToken() == "" {
		return nil, fmt.Errorf("empty token (run 'slaude auth')")
	}
	return &creds, nil
}

// Save writes credentials to disk.
func Save(creds *Credentials) error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
