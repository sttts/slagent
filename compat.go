package slagent

import (
	"fmt"
	"io"
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
	api           *slackapi.Client
	channel       string
	threadTS      string
	blockID       string    // slagent block_id for message tagging
	emoji         string    // identity emoji prefix for text messages
	thinkingEmoji string    // Slack shortcode for thinking/running indicator
	slackLog      io.Writer // optional Slack API logger

	// Unified activity message (thinking + tools + status)
	thinkBuf   strings.Builder // accumulated thinking text
	activities []string        // discrete lines: tools, status
	toolIndex  map[string]int  // tool ID → index in activities
	activityTS      string       // single message timestamp
	actUpdate       time.Time    // throttle
	activityTimer   *time.Timer  // debounce timer for activity flush
	activityDeleted bool         // activity was deleted by text; don't recreate

	// Text streaming — progressive message chain.
	// Frozen messages contain finalized text with block_id.
	// The last message shows a scrolling tail with streaming block_id (~).
	textBuf       strings.Builder
	textFrozenLen int         // bytes of textBuf in frozen (completed) messages
	textMsgs      []string    // timestamps: frozen messages + current streaming message
	textUpdate    time.Time
	textTimer     *time.Timer // debounce timer for text flush
	question      bool        // replace trailing ? with ❓ on finish
	qPrefix       string      // prepended to text on finish (e.g. "@user: ")
	plainText     bool        // wrap text in code block instead of mrkdwn conversion

	mu sync.Mutex
}

func newCompatTurn(api *slackapi.Client, channel, threadTS, blockID, emoji, thinkingEmoji string, slackLog io.Writer) *compatTurn {
	return &compatTurn{
		api:           api,
		channel:       channel,
		threadTS:      threadTS,
		blockID:       blockID,
		emoji:         emoji,
		thinkingEmoji: thinkingEmoji,
		slackLog:      slackLog,
		toolIndex:     make(map[string]int),
	}
}

// logSlack writes a Slack API action to the log writer. Must be called with lock held.
func (c *compatTurn) logSlack(action, content string) {
	if c.slackLog == nil {
		return
	}
	fmt.Fprintf(c.slackLog, "[slack] %s: %s\n", action, content)
}

// renderActivity builds the activity message content from thinking + activity lines,
// keeping at most maxDisplayLines. Must be called with lock held.
func (c *compatTurn) renderActivity() string {
	var lines []string

	// Thinking indicator: only shown before tools appear.
	// Once tools are active they carry their own :claude: spinner.
	if c.thinkBuf.Len() > 0 && len(c.activities) == 0 {
		lines = append(lines, c.emoji+c.thinkingEmoji)
		thinkText := c.thinkBuf.String()
		if len(thinkText) > 500 {
			thinkText = "…" + thinkText[len(thinkText)-499:]
		}
		for _, l := range strings.Split(thinkText, "\n") {
			lines = append(lines, "  "+l)
		}
	}

	// Activity lines (tools, status).
	// Only one :claude: spinner visible at a time (the last running tool),
	// unless in question mode where all spinners are shown.
	if c.question {
		lines = append(lines, c.activities...)
	} else {
		// Find the last line with a running spinner
		lastSpinner := -1
		for i := len(c.activities) - 1; i >= 0; i-- {
			if strings.HasPrefix(c.activities[i], c.thinkingEmoji+" ") {
				lastSpinner = i
				break
			}
		}

		for i, line := range c.activities {
			// Demote earlier running spinners to "⋯"
			if i != lastSpinner && strings.HasPrefix(line, c.thinkingEmoji+" ") {
				lines = append(lines, "⋯"+line[len(c.thinkingEmoji):])
			} else {
				lines = append(lines, line)
			}
		}
	}

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
	if c.activityDeleted {
		return
	}
	display := c.renderActivity()
	if display == "" {
		return
	}

	// Activity messages use ~act suffix — always skipped by all pollers
	actBlockID := c.blockID + "~act"
	ctx := slackapi.NewContextBlock(actBlockID,
		slackapi.NewTextBlockObject("mrkdwn", display, false, false),
	)

	if c.activityTS == "" {
		c.logSlack("postMessage(activity)", display)
		_, ts, err := c.api.PostMessage(
			c.channel,
			slackapi.MsgOptionBlocks(ctx),
			slackapi.MsgOptionText("activity", false),
			slackapi.MsgOptionTS(c.threadTS),
		)
		if err == nil {
			c.activityTS = ts
		}
	} else {
		c.logSlack("updateMessage(activity)", display)
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

// deleteActivity deletes the activity message and resets activity state.
// Acquires the lock.
func (c *compatTurn) deleteActivity() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleteActivityLocked()
}

// deleteActivityLocked deletes the activity message. Must be called with lock held.
func (c *compatTurn) deleteActivityLocked() {
	c.stopActivityTimer()
	if c.activityTS == "" {
		return
	}
	c.logSlack("deleteMessage(activity)", c.activityTS)
	c.api.DeleteMessage(c.channel, c.activityTS)
	c.activityTS = ""
	c.activityDeleted = true
	c.thinkBuf.Reset()
	c.activities = nil
	c.toolIndex = make(map[string]int)
}

func (c *compatTurn) writeThinking(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Flush any pending text before activity
	c.forceFlushText()
	c.activityDeleted = false // new thinking starts fresh activity

	c.thinkBuf.WriteString(text)
	c.flushActivity()
}

func (c *compatTurn) writeTool(id, name, status, detail string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Flush any pending text before activity
	c.forceFlushText()

	// New running tool (not a done/error update) starts fresh activity
	if status == ToolRunning {
		if _, exists := c.toolIndex[id]; !exists {
			c.activityDeleted = false
		}
	}

	summary := name
	if detail != "" {
		summary += ": " + detail
	}

	// :claude: while running, ✅ when done, ❌ on error
	var icon string
	switch status {
	case ToolDone:
		icon = "✓"
	case ToolError:
		icon = "❌"
	default:
		icon = c.thinkingEmoji
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

func (c *compatTurn) markQuestion(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.question = true
	c.qPrefix = prefix
}

func (c *compatTurn) setPlainText(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.plainText = on
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

	// Strip leading newlines from the first text content
	if c.textBuf.Len() == 0 {
		text = strings.TrimLeft(text, "\n")
		if text == "" {
			return
		}

		// Delete activity and post text immediately (same lock scope, minimal gap)
		c.deleteActivityLocked()
	}
	c.textBuf.WriteString(text)

	// Throttle updates to 1/sec
	if len(c.textMsgs) > 0 && time.Since(c.textUpdate) < time.Duration(compatThrottleMs)*time.Millisecond {
		// Schedule a debounce flush: if no further event within 1s, flush
		c.scheduleFlush()
		return
	}

	c.stopTimer()
	c.postText()
}

// postText posts or updates the text message with current buffer content.
const (
	// maxRawChunkLen is the raw text limit per message before formatting.
	// Formatted text (with "> " prefix, emoji, fences) must fit in maxBlockTextLen (3000).
	maxRawChunkLen = 2500

	// streamingTailLines is how many lines to show in the scrolling tail.
	streamingTailLines = 6
)

// postText progressively freezes text into messages and shows a scrolling tail.
// Each frozen message uses a section block with block_id for poller identification.
// Must be called with lock held.
func (c *compatTurn) postText() {
	full := c.textBuf.String()
	current := full[c.textFrozenLen:]

	// Freeze chunks when current portion exceeds the raw limit
	for len(current) > maxRawChunkLen {
		cut := strings.LastIndex(current[:maxRawChunkLen], "\n")
		if cut <= 0 {
			cut = maxRawChunkLen
		} else {
			cut++
		}
		frozen := current[:cut]
		c.freezeMessage(frozen)
		c.textFrozenLen += cut
		current = full[c.textFrozenLen:]
	}

	// Show the tail of the current (unfrozen) portion
	lines := strings.Split(current, "\n")
	display := current
	if len(lines) > streamingTailLines {
		display = strings.Join(lines[len(lines)-streamingTailLines:], "\n")
	}
	converted := formatText(display, c.emoji, c.plainText)

	streamBlockID := c.blockID + "~"
	section := slackapi.NewSectionBlock(
		slackapi.NewTextBlockObject("mrkdwn", converted, false, false),
		nil, nil,
	)
	section.BlockID = streamBlockID
	opts := []slackapi.MsgOption{
		slackapi.MsgOptionBlocks(section),
		slackapi.MsgOptionText(converted, false),
	}

	lastTS := c.lastTextTS()
	if lastTS == "" {
		c.logSlack("postMessage(text)", converted[:min(60, len(converted))])
		allOpts := append(opts, slackapi.MsgOptionTS(c.threadTS))
		if _, ts, err := c.api.PostMessage(c.channel, allOpts...); err == nil {
			c.textMsgs = append(c.textMsgs, ts)
		}
	} else {
		c.logSlack("updateMessage(text)", converted[:min(60, len(converted))])
		c.api.UpdateMessage(c.channel, lastTS, opts...)
	}
	c.textUpdate = time.Now()
}

// freezeMessage finalizes the current streaming message with the given text,
// then appends an empty slot so the next postText creates a new message.
func (c *compatTurn) freezeMessage(text string) {
	converted := formatText(text, c.emoji, c.plainText)
	frozenBlockID := fmt.Sprintf("%s-%d", c.blockID, len(c.textMsgs))
	section := slackapi.NewSectionBlock(
		slackapi.NewTextBlockObject("mrkdwn", converted, false, false),
		nil, nil,
	)
	section.BlockID = frozenBlockID
	opts := []slackapi.MsgOption{
		slackapi.MsgOptionBlocks(section),
		slackapi.MsgOptionText(converted, false),
	}

	lastTS := c.lastTextTS()
	if lastTS != "" {
		c.logSlack("updateMessage(text/freeze)", converted[:min(60, len(converted))])
		c.api.UpdateMessage(c.channel, lastTS, opts...)
	} else {
		c.logSlack("postMessage(text/freeze)", converted[:min(60, len(converted))])
		allOpts := append(opts, slackapi.MsgOptionTS(c.threadTS))
		if _, ts, err := c.api.PostMessage(c.channel, allOpts...); err == nil {
			c.textMsgs = append(c.textMsgs, ts)
		}
	}
	// Next postText will create a fresh message
	c.textMsgs = append(c.textMsgs, "")
}

// lastTextTS returns the timestamp of the last text message, or "".
func (c *compatTurn) lastTextTS() string {
	if len(c.textMsgs) == 0 {
		return ""
	}
	return c.textMsgs[len(c.textMsgs)-1]
}

// formatText converts raw text to the slagent blockquote convention.
// Every line starts with "> "; first line is "> :emoji: ...".
// This convention is how the poller identifies slagent messages.
func formatText(display, emoji string, plainText bool) string {
	if plainText {
		escaped := strings.ReplaceAll(display, "```", "'''")
		body := emoji + " 📋\n```\n" + escaped + "\n```"
		lines := strings.Split(body, "\n")
		for i := range lines {
			lines[i] = "> " + lines[i]
		}
		return strings.Join(lines, "\n")
	}

	body := MarkdownToMrkdwn(display)
	lines := strings.Split(body, "\n")
	lines[0] = "> " + emoji + " " + lines[0]
	for i := 1; i < len(lines); i++ {
		lines[i] = "> " + lines[i]
	}
	return strings.Join(lines, "\n")
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
func (c *compatTurn) finish() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Cancel debounce timers
	c.stopTimer()
	c.stopActivityTimer()

	// If no text and no real activity, delete the activity message (e.g. early thinking indicator)
	finalText := strings.TrimLeft(c.textBuf.String(), "\n")
	if finalText == "" && len(c.activities) == 0 && strings.TrimSpace(c.thinkBuf.String()) == "" {
		c.deleteActivityLocked()
		return nil
	}

	// Final flush of activity (frozen as-is, no deletion)
	c.postActivity()
	if c.activityTS != "" {
		display := c.renderActivity()
		actBlockID := c.blockID + "~act"
		ctx := slackapi.NewContextBlock(actBlockID,
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
	if finalText == "" {
		return nil
	}

	// Question turns: prepend @mention, replace trailing ? with ❓
	if c.question {
		if c.qPrefix != "" {
			finalText = c.qPrefix + finalText
		}
		finalText = strings.TrimRight(finalText, "\n ")
		if strings.HasSuffix(finalText, "?") {
			finalText = finalText[:len(finalText)-1] + " ❓"
		} else {
			finalText += " ❓"
		}
	}

	// Finalize the last streaming message with its remaining unfrozen content.
	// Frozen messages are already posted with their final content.
	unfrozen := finalText[c.textFrozenLen:]
	converted := formatText(unfrozen, c.emoji, c.plainText)

	finalBlockID := c.blockID
	section := slackapi.NewSectionBlock(
		slackapi.NewTextBlockObject("mrkdwn", converted, false, false),
		nil, nil,
	)
	section.BlockID = finalBlockID
	opts := []slackapi.MsgOption{
		slackapi.MsgOptionBlocks(section),
		slackapi.MsgOptionText(converted, false),
	}

	// If activity is below text, delete the first text message so it reposts after activity
	if len(c.textMsgs) > 0 && c.textMsgs[0] != "" && c.activityTS != "" && c.textMsgs[0] < c.activityTS {
		c.logSlack("deleteMessage(text/reorder)", c.textMsgs[0])
		c.api.DeleteMessage(c.channel, c.textMsgs[0])
		c.textMsgs[0] = ""
	}

	lastTS := c.lastTextTS()
	if lastTS != "" {
		c.logSlack("updateMessage(text/final)", converted[:min(60, len(converted))])
		c.api.UpdateMessage(c.channel, lastTS, opts...)
	} else {
		c.logSlack("postMessage(text/final)", converted[:min(60, len(converted))])
		allOpts := append(opts, slackapi.MsgOptionTS(c.threadTS))
		_, _, err := c.api.PostMessage(c.channel, allOpts...)
		if err != nil {
			return err
		}
	}

	return nil
}
