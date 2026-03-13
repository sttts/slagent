package session

import (
	"context"
	"encoding/json"

	"github.com/sttts/slagent/cmd/slaude/shared/classify"
)

// classification is a thin alias for the shared Classification type.
type classification = classify.Classification

// classifyPermission delegates to the shared classify package.
func classifyPermission(ctx context.Context, toolName string, input json.RawMessage) (*classification, error) {
	return classify.Classify(ctx, toolName, input)
}

func levelEmoji(level string) string {
	return classify.LevelEmoji(level)
}

func levelAllowed(level, threshold string) bool {
	return classify.LevelAllowed(level, threshold)
}
