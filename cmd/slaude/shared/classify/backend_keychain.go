package classify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const anthropicAPIURL = "https://api.anthropic.com/v1/messages"

// keychainBackend extracts the OAuth token from macOS Keychain
// and calls the Anthropic API directly.
type keychainBackend struct {
	mu          sync.Mutex
	cachedToken string
}

func (b *keychainBackend) Name() string { return "api" }

// token extracts the OAuth access token from macOS Keychain.
func (b *keychainBackend) token() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.cachedToken != "" {
		return b.cachedToken, nil
	}

	out, err := exec.Command("security", "find-generic-password", "-s", "Claude Code-credentials", "-w").Output()
	if err != nil {
		return "", fmt.Errorf("keychain lookup failed: %w", err)
	}

	raw := bytes.TrimSpace(out)

	// Try JSON format: {"claudeAiOauth":{"accessToken":"...","expiresAt":EPOCH_MS,...}}
	var wrapper struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(raw, &wrapper) == nil && wrapper.ClaudeAiOauth.AccessToken != "" {
		if wrapper.ClaudeAiOauth.ExpiresAt > 0 && time.Now().UnixMilli() > wrapper.ClaudeAiOauth.ExpiresAt {
			return "", fmt.Errorf("OAuth token expired")
		}
		b.cachedToken = wrapper.ClaudeAiOauth.AccessToken
		return b.cachedToken, nil
	}

	// Legacy format: {"accessToken":"...","expiresAt":"ISO8601"}
	var legacy struct {
		AccessToken string `json:"accessToken"`
		ExpiresAt   string `json:"expiresAt"`
	}
	if json.Unmarshal(raw, &legacy) == nil && legacy.AccessToken != "" {
		if legacy.ExpiresAt != "" {
			if t, err := time.Parse(time.RFC3339, legacy.ExpiresAt); err == nil && time.Now().After(t) {
				return "", fmt.Errorf("OAuth token expired at %s", legacy.ExpiresAt)
			}
		}
		b.cachedToken = legacy.AccessToken
		return b.cachedToken, nil
	}

	// Fall back to raw token string (API key)
	tok := string(raw)
	if !strings.HasPrefix(tok, "sk-ant-") {
		return "", fmt.Errorf("unrecognized token format in keychain")
	}
	b.cachedToken = tok
	return b.cachedToken, nil
}

// apiRequest is the Anthropic messages API request body.
type apiRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	Messages  []apiMessage `json:"messages"`
}

type apiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// apiResponse is the Anthropic messages API response body.
type apiResponse struct {
	Content []apiContent `json:"content"`
	Error   *apiError    `json:"error,omitempty"`
}

type apiContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (b *keychainBackend) Complete(ctx context.Context, prompt string) (string, error) {
	tok, err := b.token()
	if err != nil {
		return "", err
	}

	reqBody := apiRequest{
		Model:     "claude-haiku-4-5-20251001",
		MaxTokens: 256,
		Messages:  []apiMessage{{Role: "user", Content: prompt}},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", anthropicAPIURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", tok)
	req.Header.Set("Anthropic-Version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// Handle token expiry — clear cache and return error so caller can retry/fallback
	if resp.StatusCode == 401 {
		b.mu.Lock()
		b.cachedToken = ""
		b.mu.Unlock()
		return "", fmt.Errorf("API returned 401: %s", strings.TrimSpace(string(respBody)))
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var apiResp apiResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return "", fmt.Errorf("parsing API response: %w", err)
	}
	if apiResp.Error != nil {
		return "", fmt.Errorf("API error: %s: %s", apiResp.Error.Type, apiResp.Error.Message)
	}
	if len(apiResp.Content) == 0 {
		return "", fmt.Errorf("empty response from API")
	}
	return strings.TrimSpace(apiResp.Content[0].Text), nil
}
