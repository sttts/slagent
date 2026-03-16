package classify

import (
	"context"
	"runtime"
	"sync"
)

// Backend sends a prompt to an LLM and returns the text response.
type Backend interface {
	// Complete sends a prompt and returns the model's text response.
	Complete(ctx context.Context, prompt string) (string, error)
	// Name returns a short identifier for logging (e.g. "keychain", "cli").
	Name() string
}

var (
	defaultBackend Backend
	backendOnce    sync.Once
)

// DefaultBackend returns the auto-detected backend.
// On macOS, tries keychain first (direct API), falls back to CLI.
func DefaultBackend() Backend {
	backendOnce.Do(func() {
		if runtime.GOOS == "darwin" {
			kb := &keychainBackend{}
			if _, err := kb.token(); err == nil {
				defaultBackend = kb
				return
			}
		}
		defaultBackend = &cliBackend{}
	})
	return defaultBackend
}
