package session

import (
	"github.com/sttts/slagent/cmd/slaude/shared/classify"
)

// knownDest is a thin alias for the shared KnownDest type.
type knownDest = classify.KnownDest

// knownHostSet is a thin alias for the shared KnownHostSet type.
type knownHostSet = classify.KnownHostSet

// loadKnownHosts delegates to the shared classify package.
func loadKnownHosts() *knownHostSet {
	return classify.LoadKnownHosts()
}

// unquote delegates to the shared classify package.
func unquote(s string) string {
	return classify.Unquote(s)
}
