package credential

import "fmt"

// Result holds the complete extraction result.
type Result struct {
	Workspaces []Workspace
	Cookie     string // xoxd-... cookie, shared across all workspaces
}

// Extract finds Slack's data directories and extracts tokens + cookie.
func Extract() (*Result, error) {
	paths := findSlackPaths()
	if len(paths) == 0 {
		return nil, fmt.Errorf("Slack desktop app not found (checked macOS and Linux paths)")
	}

	var lastErr error
	for _, p := range paths {
		workspaces, err := extractTokens(p.LevelDB)
		if err != nil {
			lastErr = fmt.Errorf("extract tokens from %s: %w", p.LevelDB, err)
			continue
		}

		cookie, err := extractCookie(p.Cookies, p.IsSnap)
		if err != nil {
			lastErr = fmt.Errorf("extract cookie from %s: %w", p.Cookies, err)
			continue
		}

		return &Result{
			Workspaces: workspaces,
			Cookie:     cookie,
		}, nil
	}

	return nil, fmt.Errorf("extraction failed: %w", lastErr)
}
