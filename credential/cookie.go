package credential

import (
	"database/sql"
	"fmt"
	"io"
	"os"

	_ "modernc.org/sqlite"
)

// extractCookie reads and decrypts the 'd' cookie from Slack's Cookies SQLite DB.
func extractCookie(cookiesPath string, isSnap bool) (string, error) {
	// Copy to temp file (Slack may hold a WAL lock)
	tmpFile, err := os.CreateTemp("", "slagent-cookies-*.sqlite")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	if err := copyFile(cookiesPath, tmpPath); err != nil {
		return "", fmt.Errorf("copy cookies db: %w", err)
	}

	// Also copy WAL and SHM files if they exist
	for _, suffix := range []string{"-wal", "-shm"} {
		src := cookiesPath + suffix
		if _, err := os.Stat(src); err == nil {
			dst := tmpPath + suffix
			copyFile(src, dst)
			defer os.Remove(dst)
		}
	}

	db, err := sql.Open("sqlite", tmpPath)
	if err != nil {
		return "", fmt.Errorf("open cookies db: %w", err)
	}
	defer db.Close()

	var encrypted []byte
	err = db.QueryRow(
		"SELECT encrypted_value FROM cookies WHERE host_key = '.slack.com' AND name = 'd'",
	).Scan(&encrypted)
	if err != nil {
		return "", fmt.Errorf("query cookie: %w", err)
	}

	if len(encrypted) == 0 {
		return "", fmt.Errorf("empty encrypted cookie value")
	}

	// Check for unencrypted value first
	var plainValue string
	_ = db.QueryRow(
		"SELECT value FROM cookies WHERE host_key = '.slack.com' AND name = 'd'",
	).Scan(&plainValue)
	if plainValue != "" {
		return plainValue, nil
	}

	return decryptCookieValue(encrypted, isSnap)
}

// copyFileForCookie is a helper that copies a file for cookie extraction.
// Re-uses the copyFile from leveldb.go since they're in the same package.
func copyFileReader(src string) ([]byte, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
