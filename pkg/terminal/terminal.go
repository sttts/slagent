// Package terminal handles simple terminal output and input for pairplan.
package terminal

import (
	"fmt"
	"strings"
)

// ANSI escape sequences.
const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	green  = "\033[32m"
	cyan   = "\033[36m"
	red    = "\033[31m"

	hideCursor = "\033[?25l"
	showCursor = "\033[?25h"
	clearLine  = "\r\033[K"
)

// UI provides simple terminal output for pairplan sessions.
type UI struct {
	streaming  bool // true while Claude is streaming text
	onToolLine bool // true when cursor is on an in-place tool activity line
}

// New creates a new terminal UI.
func New() *UI {
	return &UI{}
}

// Banner prints the session start info.
func (u *UI) Banner(topic, channel, threadURL string) {
	fmt.Printf("\n%s%s🧵 pairplan %s", bold, green, reset)
	if topic != "" {
		fmt.Printf(" — %s", topic)
	}
	fmt.Println()
	if channel != "" {
		fmt.Printf("%s  💬 Channel: %s%s\n", dim, channel, reset)
	}
	if threadURL != "" {
		fmt.Printf("%s  🔗 Thread:  %s%s\n", dim, threadURL, reset)
	}
	fmt.Println(dim + strings.Repeat("─", 60) + reset)
	fmt.Println()
}

// StartResponse begins a Claude response block.
func (u *UI) StartResponse() {
	u.clearToolLine()
	fmt.Printf("%s%s🤖 Claude:%s ", bold, green, reset)
	u.streaming = true
}

// StreamText appends streaming text from Claude. Printed inline.
func (u *UI) StreamText(text string) {
	u.clearToolLine()
	fmt.Print(text)
}

// EndResponse finishes a Claude response block.
func (u *UI) EndResponse() {
	u.clearToolLine()
	if u.streaming {
		fmt.Println()
		u.streaming = false
	}
	fmt.Println()
}

// ToolActivity shows a brief tool use notification, updating in-place.
func (u *UI) ToolActivity(summary string) {
	fmt.Printf("%s  %s%s%s", clearLine, dim, summary, reset)
	u.onToolLine = true
}

// clearToolLine clears the in-place tool activity line if active.
func (u *UI) clearToolLine() {
	if u.onToolLine {
		fmt.Print(clearLine)
		u.onToolLine = false
	}
}

// SlackMessage shows a message received from Slack.
func (u *UI) SlackMessage(user, text string) {
	u.clearToolLine()
	fmt.Printf("  %s💬 @%s:%s %s\n", cyan, user, reset, text)
}

// HideCursor hides the terminal cursor.
func (u *UI) HideCursor() {
	fmt.Print(hideCursor)
}

// ShowCursor shows the terminal cursor.
func (u *UI) ShowCursor() {
	fmt.Print(showCursor)
}

// Info prints a dim informational line.
func (u *UI) Info(msg string) {
	u.clearToolLine()
	fmt.Printf("%s%s%s\n", dim, msg, reset)
}

// Error prints an error.
func (u *UI) Error(msg string) {
	u.clearToolLine()
	fmt.Printf("%s%s❌ Error: %s%s\n", bold, red, msg, reset)
}

// Thinking shows a thinking indicator, updating in-place.
func (u *UI) Thinking() {
	fmt.Printf("%s  %s💭 thinking...%s", clearLine, dim, reset)
	u.onToolLine = true
}
