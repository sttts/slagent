// Package terminal handles simple terminal output for slaude.
package terminal

import (
	"fmt"
	"io"
	"os"
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
)

// UI provides simple terminal output for slaude sessions.
type UI struct {
	w         io.Writer
	streaming bool // true while Claude is streaming text
}

// New creates a new terminal UI that writes to stdout.
func New() *UI {
	return &UI{w: os.Stdout}
}

// NewWithWriter creates a UI that writes to w (useful for testing).
func NewWithWriter(w io.Writer) *UI {
	return &UI{w: w}
}

// BannerOpts configures the banner display.
type BannerOpts struct {
	Topic     string
	Channel   string
	ThreadURL string
	JoinCmd string // command to join this thread with a new slaude instance
}

// Banner prints the session start info.
func (u *UI) Banner(opts BannerOpts) {
	fmt.Fprintf(u.w, "\n%s%s🧵 slaude%s", bold, green, reset)
	if opts.Topic != "" {
		fmt.Fprintf(u.w, " — %s", opts.Topic)
	}
	fmt.Fprintln(u.w)
	if opts.Channel != "" {
		fmt.Fprintf(u.w, "%s  💬 Channel: %s%s\n", dim, opts.Channel, reset)
	}
	if opts.ThreadURL != "" {
		fmt.Fprintf(u.w, "%s  🔗 Thread:  %s%s\n", dim, opts.ThreadURL, reset)
	}
	if opts.JoinCmd != "" {
		fmt.Fprintf(u.w, "%s  🚀 Join:    %s%s\n", dim, opts.JoinCmd, reset)
	}
	fmt.Fprintln(u.w, dim+strings.Repeat("─", 60)+reset)
	fmt.Fprintln(u.w)
}

// StartResponse begins a Claude response block.
func (u *UI) StartResponse() {
	fmt.Fprintf(u.w, "%s%s🤖 Claude:%s ", bold, green, reset)
	u.streaming = true
}

// StreamText appends streaming text from Claude. Printed inline.
func (u *UI) StreamText(text string) {
	fmt.Fprint(u.w, text)
}

// EndResponse finishes a Claude response block.
func (u *UI) EndResponse() {
	if u.streaming {
		fmt.Fprintln(u.w)
		u.streaming = false
	}
	fmt.Fprintln(u.w)
}

// ToolActivity shows a brief tool use notification.
func (u *UI) ToolActivity(summary string) {
	if u.streaming {
		fmt.Fprintln(u.w)
		u.streaming = false
	}
	fmt.Fprintf(u.w, "  %s%s%s\n", dim, summary, reset)
}

// SlackMessage shows a message received from Slack.
func (u *UI) SlackMessage(user, text string) {
	fmt.Fprintf(u.w, "  %s💬 @%s:%s %s\n", cyan, user, reset, text)
}

// HideCursor hides the terminal cursor.
func (u *UI) HideCursor() {
	fmt.Fprint(u.w, hideCursor)
}

// ShowCursor shows the terminal cursor.
func (u *UI) ShowCursor() {
	fmt.Fprint(u.w, showCursor)
}

// Info prints a dim informational line.
func (u *UI) Info(msg string) {
	fmt.Fprintf(u.w, "%s%s%s\n", dim, msg, reset)
}

// Error prints an error.
func (u *UI) Error(msg string) {
	fmt.Fprintf(u.w, "%s%s❌ Error: %s%s\n", bold, red, msg, reset)
}

// Thinking shows a thinking line with content.
func (u *UI) Thinking(text string) {
	if u.streaming {
		fmt.Fprintln(u.w)
		u.streaming = false
	}

	// Show the actual thinking content, trimmed
	line := strings.TrimRight(text, "\n")
	if line == "" {
		return
	}
	fmt.Fprintf(u.w, "  %s💭 %s%s\n", dim, line, reset)
}

