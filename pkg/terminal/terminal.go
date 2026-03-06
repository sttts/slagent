// Package terminal handles simple terminal output and input for pairplan.
package terminal

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Colors for terminal output.
const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	green  = "\033[32m"
	yellow = "\033[33m"
	blue   = "\033[34m"
	cyan   = "\033[36m"
	red    = "\033[31m"
)

// UI provides simple terminal output for pairplan sessions.
type UI struct {
	reader    *bufio.Reader
	streaming bool // true while Claude is streaming text
}

// New creates a new terminal UI.
func New() *UI {
	return &UI{
		reader: bufio.NewReader(os.Stdin),
	}
}

// Banner prints the session start info.
func (u *UI) Banner(topic, channel, threadURL string) {
	fmt.Printf("\n%s%s pairplan %s", bold, blue, reset)
	if topic != "" {
		fmt.Printf(" — %s", topic)
	}
	fmt.Println()
	if channel != "" {
		fmt.Printf("%sChannel: #%s%s\n", dim, channel, reset)
	}
	if threadURL != "" {
		fmt.Printf("%sThread:  %s%s\n", dim, threadURL, reset)
	}
	fmt.Println(dim + strings.Repeat("─", 60) + reset)
	fmt.Println()
}

// StartResponse begins a Claude response block.
func (u *UI) StartResponse() {
	fmt.Printf("%s%sClaude:%s ", bold, green, reset)
	u.streaming = true
}

// StreamText appends streaming text from Claude. Printed inline.
func (u *UI) StreamText(text string) {
	fmt.Print(text)
}

// EndResponse finishes a Claude response block.
func (u *UI) EndResponse() {
	if u.streaming {
		fmt.Println()
		u.streaming = false
	}
	fmt.Println()
}

// ToolActivity shows a brief tool use notification.
func (u *UI) ToolActivity(toolName, summary string) {
	fmt.Printf("  %s> %s: %s%s\n", dim, toolName, summary, reset)
}

// SlackMessage shows a message received from Slack.
func (u *UI) SlackMessage(user, text string) {
	fmt.Printf("  %s[Slack] @%s:%s %s\n", cyan, user, reset, text)
}

// UserMessage echoes the user's input.
func (u *UI) UserMessage(text string) {
	fmt.Printf("%s%sYou:%s %s\n\n", bold, yellow, reset, text)
}

// Info prints a dim informational line.
func (u *UI) Info(msg string) {
	fmt.Printf("%s%s%s\n", dim, msg, reset)
}

// Error prints an error.
func (u *UI) Error(msg string) {
	fmt.Printf("%s%sError: %s%s\n", bold, red, msg, reset)
}

// Prompt reads a line of input from the user. Returns the trimmed text
// and false on EOF.
func (u *UI) Prompt() (string, bool) {
	fmt.Printf("%s%sYou> %s", bold, blue, reset)
	line, err := u.reader.ReadString('\n')
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(line), true
}

// Thinking shows a thinking indicator.
func (u *UI) Thinking() {
	fmt.Printf("  %s(thinking...)%s\n", dim, reset)
}
