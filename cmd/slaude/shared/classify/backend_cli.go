package classify

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// cliBackend shells out to `claude -p --model haiku` for classification.
type cliBackend struct{}

func (b *cliBackend) Name() string { return "cli" }

func (b *cliBackend) Complete(ctx context.Context, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, "claude", "-p", "--output-format", "text", "--model", "haiku", "--no-session-persistence", prompt)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			err = fmt.Errorf("%w: %s", err, errMsg)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
