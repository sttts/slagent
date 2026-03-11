package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SessionState is persisted to disk while a slaude session is running.
type SessionState struct {
	PID         int    `json:"pid"`
	Emoji       string `json:"emoji"`
	InstanceID  string `json:"instanceId"`
	Channel     string `json:"channel"`
	ThreadURL   string `json:"threadUrl"`
	Workspace   string `json:"workspace"`
	StartedAt   int64  `json:"startedAt"` // Unix seconds
}

// sessionsDir returns the directory where session state files are stored.
func sessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".local", "share", "slaude", "sessions")
	return dir, os.MkdirAll(dir, 0o700)
}

// stateFilePath returns the path for a given PID's state file.
func stateFilePath(pid int) (string, error) {
	dir, err := sessionsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("%d.json", pid)), nil
}

// RegisterSession writes the session state to disk.
func RegisterSession(state SessionState) error {
	path, err := stateFilePath(state.PID)
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(state)
}

// UnregisterSession removes the session state file for the given PID.
func UnregisterSession(pid int) {
	path, _ := stateFilePath(pid)
	_ = os.Remove(path)
}

// ListSessions returns all persisted sessions whose process is still alive.
func ListSessions() ([]SessionState, error) {
	dir, err := sessionsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var sessions []SessionState
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var s SessionState
		if err := json.Unmarshal(data, &s); err != nil {
			continue
		}
		// Only include if process is still alive
		if processAlive(s.PID) {
			sessions = append(sessions, s)
		} else {
			// Clean up stale file
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return sessions, nil
}

// FindSession finds a session by emoji or PID string.
func FindSession(target string) (*SessionState, error) {
	sessions, err := ListSessions()
	if err != nil {
		return nil, err
	}
	// Try PID match
	if pid, err := strconv.Atoi(target); err == nil {
		for _, s := range sessions {
			s := s
			if s.PID == pid {
				return &s, nil
			}
		}
	}
	// Try emoji match (strip optional colons, case-insensitive)
	target = strings.ToLower(strings.Trim(target, ":"))
	for _, s := range sessions {
		s := s
		emoji := strings.ToLower(strings.Trim(s.Emoji, ":"))
		if emoji == target || s.InstanceID == target {
			return &s, nil
		}
	}
	return nil, fmt.Errorf("no session found for %q", target)
}

// KillSession sends SIGINT to the session with the given emoji or PID.
func KillSession(target string) error {
	s, err := FindSession(target)
	if err != nil {
		return err
	}
	proc, err := os.FindProcess(s.PID)
	if err != nil {
		return fmt.Errorf("find process %d: %w", s.PID, err)
	}
	if err := proc.Signal(syscall.SIGINT); err != nil {
		return fmt.Errorf("signal process %d: %w", s.PID, err)
	}
	return nil
}

// processAlive returns true if the given PID is still running.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

// FormatSessions returns a human-readable table of sessions.
func FormatSessions(sessions []SessionState) string {
	if len(sessions) == 0 {
		return "No active sessions."
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%-6s  %-20s  %-16s  %s\n", "PID", "EMOJI", "CHANNEL", "UPTIME")
	fmt.Fprintf(&sb, "%-6s  %-20s  %-16s  %s\n",
		"------", "--------------------", "----------------", "------")
	for _, s := range sessions {
		uptime := time.Since(time.Unix(s.StartedAt, 0)).Truncate(time.Second)
		channel := s.Channel
		if len(channel) > 16 {
			channel = channel[:13] + "..."
		}
		emoji := s.Emoji
		if s.InstanceID != "" {
			emoji = fmt.Sprintf("%s (%s)", s.Emoji, s.InstanceID)
		}
		fmt.Fprintf(&sb, "%-6d  %-20s  %-16s  %s\n", s.PID, emoji, channel, uptime)
	}
	return sb.String()
}
