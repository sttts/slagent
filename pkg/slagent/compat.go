package slagent

import (
	"fmt"
	"strings"
	"sync"
	"time"

	slackapi "github.com/slack-go/slack"
)

const (
	maxBlockTextLen  = 3000
	maxDisplayLines  = 6
	compatThrottleMs = 1000
)

// compatTurn implements turnWriter using postMessage/update for session/user tokens.
// Thinking + tools + status share a single "activity" message (≤6 lines, updated in-place).
// Text streams in a separate message (last 6 lines while streaming, full text on finish).
// No messages are ever deleted.
type compatTurn struct {
	api      *slackapi.Client
	channel  string
	threadTS string
	posted   func(ts string)

	// Unified activity message (thinking + tools + status)
	thinkBuf   strings.Builder // accumulated thinking text
	activities []string        // discrete lines: tools, status
	toolIndex  map[string]int  // tool ID → index in activities
	activityTS    string          // single message timestamp
	actUpdate     time.Time       // throttle
	activityTimer *time.Timer    // debounce timer for activity flush

	// Text streaming
	textBuf    strings.Builder
	textTS     string
	textUpdate time.Time
	textTimer  *time.Timer // debounce timer for text flush

	mu sync.Mutex
}

func newCompatTurn(api *slackapi.Client, channel, threadTS string, posted func(string)) *compatTurn {
	return &compatTurn{
		api:       api,
		channel:   channel,
		threadTS:  threadTS,
		posted:    posted,
		toolIndex: make(map[string]int),
	}
}

// textMsgOpts returns message options for a text message in a code block with 🤖 prefix.
// Uses plain text (no blocks) to avoid section block padding.
// Embedded triple-backtick fences are replaced with ''' to avoid breaking the outer block.
func textMsgOpts(display string) slackapi.MsgOption {
	escaped := strings.ReplaceAll(display, "```", "'''")
	return slackapi.MsgOptionText("🤖\n```\n"+escaped+"\n```", false)
}

// renderActivity builds the activity message content from thinking + activity lines,
// keeping at most maxDisplayLines. Must be called with lock held.
func (c *compatTurn) renderActivity() string {
	var lines []string

	// Thinking lines
	if c.thinkBuf.Len() > 0 {
		lines = append(lines, "💭 _thinking..._")
		thinkText := c.thinkBuf.String()
		if len(thinkText) > 500 {
			thinkText = "…" + thinkText[len(thinkText)-499:]
		}
		for _, l := range strings.Split(thinkText, "\n") {
			lines = append(lines, "  "+l)
		}
	}

	// Activity lines (tools, status)
	lines = append(lines, c.activities...)

	// Keep last maxDisplayLines
	if len(lines) > maxDisplayLines {
		lines = lines[len(lines)-maxDisplayLines:]
	}

	return strings.Join(lines, "\n")
}

// flushActivity posts or updates the unified activity message. Must be called with lock held.
func (c *compatTurn) flushActivity() {
	// Throttle to 1/sec
	if c.activityTS != "" && time.Since(c.actUpdate) < time.Duration(compatThrottleMs)*time.Millisecond {
		c.scheduleActivityFlush()
		return
	}

	c.stopActivityTimer()
	c.postActivity()
}

// postActivity posts or updates the activity message. Must be called with lock held.
func (c *compatTurn) postActivity() {
	display := c.renderActivity()
	if display == "" {
		return
	}

	ctx := slackapi.NewContextBlock("",
		slackapi.NewTextBlockObject("mrkdwn", display, false, false),
	)

	if c.activityTS == "" {
		_, ts, err := c.api.PostMessage(
			c.channel,
			slackapi.MsgOptionBlocks(ctx),
			slackapi.MsgOptionText("activity", false),
			slackapi.MsgOptionTS(c.threadTS),
		)
		if err == nil {
			c.activityTS = ts
			c.posted(ts)
		}
	} else {
		c.api.UpdateMessage(
			c.channel,
			c.activityTS,
			slackapi.MsgOptionBlocks(ctx),
			slackapi.MsgOptionText("activity", false),
		)
	}
	c.actUpdate = time.Now()
}

// scheduleActivityFlush starts a debounce timer for activity. Must be called with lock held.
func (c *compatTurn) scheduleActivityFlush() {
	if c.activityTimer != nil {
		return
	}
	c.activityTimer = time.AfterFunc(time.Duration(compatThrottleMs)*time.Millisecond, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.activityTimer = nil
		c.postActivity()
	})
}

// stopActivityTimer cancels any pending activity debounce timer. Must be called with lock held.
func (c *compatTurn) stopActivityTimer() {
	if c.activityTimer != nil {
		c.activityTimer.Stop()
		c.activityTimer = nil
	}
}

// forceFlushText updates the text message with current buffer content,
// bypassing throttle. Must be called with lock held.
func (c *compatTurn) forceFlushText() {
	c.stopTimer()
	if c.textBuf.Len() == 0 {
		return
	}
	c.postText()
}

func (c *compatTurn) writeThinking(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Flush any pending text before first activity
	c.forceFlushText()

	c.thinkBuf.WriteString(text)
	c.flushActivity()
}

func (c *compatTurn) writeTool(id, name, status, detail string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Flush any pending text before first activity
	c.forceFlushText()

	summary := name
	if detail != "" {
		summary += ": " + detail
	}

	// Single icon: status for done/error, tool icon for running
	var icon string
	switch status {
	case ToolDone:
		icon = "✅"
	case ToolError:
		icon = "❌"
	default:
		switch name {
		case "Read":
			icon = "📄"
		case "Glob", "Grep":
			icon = "🔍"
		case "Bash":
			icon = "💻"
		case "Agent":
			icon = "🤖"
		case "Edit", "Write":
			icon = "✏️"
		case "WebSearch", "WebFetch":
			icon = "🌐"
		default:
			icon = "🔧"
		}
	}

	line := fmt.Sprintf("%s %s", icon, summary)

	// Update existing line or append new one
	if idx, ok := c.toolIndex[id]; ok {
		c.activities[idx] = line
	} else {
		c.toolIndex[id] = len(c.activities)
		c.activities = append(c.activities, line)
	}
	c.flushActivity()
}

func (c *compatTurn) writeStatus(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if text == "" {
		return
	}

	c.activities = append(c.activities, fmt.Sprintf("⏳ %s", text))
	c.flushActivity()
}

func (c *compatTurn) writeText(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.textBuf.WriteString(text)

	// Throttle updates to 1/sec
	if c.textTS != "" && time.Since(c.textUpdate) < time.Duration(compatThrottleMs)*time.Millisecond {
		// Schedule a debounce flush: if no further event within 1s, flush
		c.scheduleFlush()
		return
	}

	c.stopTimer()
	c.postText()
}

// postText posts or updates the text message with current buffer content.
// Must be called with lock held.
func (c *compatTurn) postText() {
	full := c.textBuf.String()
	display := lastNLines(full, maxDisplayLines)
	opt := textMsgOpts(display)

	if c.textTS == "" {
		_, ts, err := c.api.PostMessage(
			c.channel,
			opt,
			slackapi.MsgOptionTS(c.threadTS),
		)
		if err == nil {
			c.textTS = ts
			c.posted(ts)
		}
	} else {
		c.api.UpdateMessage(
			c.channel,
			c.textTS,
			opt,
		)
	}
	c.textUpdate = time.Now()
}

// scheduleFlush starts a debounce timer that flushes text after 1s.
// Must be called with lock held.
func (c *compatTurn) scheduleFlush() {
	if c.textTimer != nil {
		return // already scheduled
	}
	c.textTimer = time.AfterFunc(time.Duration(compatThrottleMs)*time.Millisecond, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.textTimer = nil
		c.postText()
	})
}

// stopTimer cancels any pending debounce timer. Must be called with lock held.
func (c *compatTurn) stopTimer() {
	if c.textTimer != nil {
		c.textTimer.Stop()
		c.textTimer = nil
	}
}

// finish freezes the activity message and updates the text message to the full final response.
// No messages are deleted.
func (c *compatTurn) finish() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Cancel debounce timers
	c.stopTimer()
	c.stopActivityTimer()

	// Final flush of activity (frozen as-is, no deletion)
	c.postActivity()
	if c.activityTS != "" {
		display := c.renderActivity()
		ctx := slackapi.NewContextBlock("",
			slackapi.NewTextBlockObject("mrkdwn", display, false, false),
		)
		c.api.UpdateMessage(
			c.channel,
			c.activityTS,
			slackapi.MsgOptionBlocks(ctx),
			slackapi.MsgOptionText("activity", false),
		)
	}

	// Update text message to full final response
	finalText := c.textBuf.String()
	if finalText == "" {
		return nil
	}

	// Update existing text message with full content
	if c.textTS != "" {
		c.api.UpdateMessage(
			c.channel,
			c.textTS,
			textMsgOpts(finalText),
		)
	} else {
		_, ts, err := c.api.PostMessage(
			c.channel,
			textMsgOpts(finalText),
			slackapi.MsgOptionTS(c.threadTS),
		)
		if err != nil {
			return err
		}
		c.posted(ts)
	}

	return nil
}

// lastNLines returns the last n lines of text.
func lastNLines(text string, n int) string {
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
