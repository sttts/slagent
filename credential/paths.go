package credential

import (
	"os"
	"path/filepath"
	"runtime"
)

// slackPaths holds candidate paths for Slack's local data.
type slackPaths struct {
	LevelDB string // directory containing LevelDB files
	Cookies string // path to Cookies SQLite database
}

// findSlackPaths returns all existing Slack data directories on this system.
func findSlackPaths() []slackPaths {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	var candidates []slackPaths
	switch runtime.GOOS {
	case "darwin":
		candidates = []slackPaths{
			// App Store version (sandboxed)
			{
				LevelDB: filepath.Join(home, "Library", "Containers", "com.tinyspeck.slackmacgap",
					"Data", "Library", "Application Support", "Slack", "Local Storage", "leveldb"),
				Cookies: filepath.Join(home, "Library", "Containers", "com.tinyspeck.slackmacgap",
					"Data", "Library", "Application Support", "Slack", "Cookies"),
			},
			// Direct download version
			{
				LevelDB: filepath.Join(home, "Library", "Application Support", "Slack", "Local Storage", "leveldb"),
				Cookies: filepath.Join(home, "Library", "Application Support", "Slack", "Cookies"),
			},
		}
	case "linux":
		candidates = []slackPaths{
			{
				LevelDB: filepath.Join(home, ".config", "Slack", "Local Storage", "leveldb"),
				Cookies: filepath.Join(home, ".config", "Slack", "Cookies"),
			},
		}
	}

	// Filter to paths that exist
	var result []slackPaths
	for _, c := range candidates {
		if dirExists(c.LevelDB) && fileExists(c.Cookies) {
			result = append(result, c)
		}
	}
	return result
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
