package slagent

import (
	"fmt"
	"strings"
	"sync"
	"time"

	slackapi "github.com/slack-go/slack"
)

const (
	maxBlockTextLen = 3000
	maxToolHistory  = 5
)

// compatTurn implements turnWriter using postMessage/update for session/user tokens.
type compatTurn struct {
	api      *slackapi.Client
	channel  string
	threadTS string
	convert  func(string) string // markdown → mrkdwn
	posted   func(ts string)     // callback to track posted messages

	// Text streaming state
	textBuf    strings.Builder
	textTS     string // timestamp of the streaming text message
	textUpdate time.Time

	// Thinking state
	thinkBuf strings.Builder
	thinkTS  string
	thinkUpdate time.Time

	// Tool state
	tools  []toolEntry
	toolTS string

	// Status state
	statusTS string

	mu sync.Mutex
}

type toolEntry struct {
	id, name, status, detail string
}

func newCompatTurn(api *slackapi.Client, channel, threadTS string, convert func(string) string, posted func(string)) *compatTurn {
	return &compatTurn{
		api:      api,
		channel:  channel,
		threadTS: threadTS,
		convert:  convert,
		posted:   posted,
	}
}

func (c *compatTurn) writeText(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.textBuf.WriteString(text)

	// Throttle updates to 1/sec
	if c.textTS != "" && time.Since(c.textUpdate) < time.Second {
		return
	}

	// Show last ~2800 chars as mrkdwn
	display := c.textBuf.String()
	if len(display) > 2800 {
		display = "…" + display[len(display)-2799:]
	}
	display = c.convert(display)

	section := slackapi.NewSectionBlock(
		slackapi.NewTextBlockObject("mrkdwn", display, false, false),
		nil, nil,
	)

	if c.textTS == "" {
		_, ts, err := c.api.PostMessage(
			c.channel,
			slackapi.MsgOptionBlocks(section),
			slackapi.MsgOptionText(display, false),
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
			slackapi.MsgOptionBlocks(section),
			slackapi.MsgOptionText(display, false),
		)
	}
	c.textUpdate = time.Now()
}

func (c *compatTurn) writeThinking(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.thinkBuf.WriteString(text)

	// Throttle to 1/sec
	if c.thinkTS != "" && time.Since(c.thinkUpdate) < time.Second {
		return
	}

	// Show last ~500 chars, last 5 lines in code block
	display := c.thinkBuf.String()
	if len(display) > 500 {
		display = "…" + display[len(display)-499:]
	}
	lines := strings.Split(display, "\n")
	if len(lines) > 5 {
		lines = append([]string{"…"}, lines[len(lines)-5:]...)
	}

	content := fmt.Sprintf("💭 _thinking..._\n```%s```", strings.Join(lines, "\n"))
	ctx := slackapi.NewContextBlock("",
		slackapi.NewTextBlockObject("mrkdwn", content, false, false),
	)

	if c.thinkTS == "" {
		_, ts, err := c.api.PostMessage(
			c.channel,
			slackapi.MsgOptionBlocks(ctx),
			slackapi.MsgOptionText("thinking...", false),
			slackapi.MsgOptionTS(c.threadTS),
		)
		if err == nil {
			c.thinkTS = ts
			c.posted(ts)
		}
	} else {
		c.api.UpdateMessage(
			c.channel,
			c.thinkTS,
			slackapi.MsgOptionBlocks(ctx),
			slackapi.MsgOptionText("thinking...", false),
		)
	}
	c.thinkUpdate = time.Now()
}

func (c *compatTurn) writeTool(id, name, status, detail string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Update or add tool entry
	found := false
	for i := range c.tools {
		if c.tools[i].id == id {
			c.tools[i] = toolEntry{id, name, status, detail}
			found = true
			break
		}
	}
	if !found {
		c.tools = append(c.tools, toolEntry{id, name, status, detail})
	}

	// Keep last maxToolHistory entries
	if len(c.tools) > maxToolHistory {
		c.tools = c.tools[len(c.tools)-maxToolHistory:]
	}

	// Build display with scrolling history
	var lines []string
	for i, t := range c.tools {
		summary := t.name
		if t.detail != "" {
			summary += ": " + t.detail
		}
		if t.status == ToolError {
			summary += " ❌"
		}
		if i == len(c.tools)-1 {
			lines = append(lines, fmt.Sprintf("🔧 %s", summary))
		} else {
			lines = append(lines, fmt.Sprintf("      %s", summary))
		}
	}
	display := strings.Join(lines, "\n")

	ctx := slackapi.NewContextBlock("",
		slackapi.NewTextBlockObject("mrkdwn", display, false, false),
	)

	if c.toolTS == "" {
		_, ts, err := c.api.PostMessage(
			c.channel,
			slackapi.MsgOptionBlocks(ctx),
			slackapi.MsgOptionText(display, false),
			slackapi.MsgOptionTS(c.threadTS),
		)
		if err == nil {
			c.toolTS = ts
			c.posted(ts)
		}
	} else {
		c.api.UpdateMessage(
			c.channel,
			c.toolTS,
			slackapi.MsgOptionBlocks(ctx),
			slackapi.MsgOptionText(display, false),
		)
	}
}

func (c *compatTurn) writeStatus(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Delete previous status message
	if c.statusTS != "" {
		c.api.DeleteMessage(c.channel, c.statusTS)
		c.statusTS = ""
	}

	if text == "" {
		return
	}

	ctx := slackapi.NewContextBlock("",
		slackapi.NewTextBlockObject("mrkdwn", fmt.Sprintf("⏳ %s", text), false, false),
	)
	_, ts, err := c.api.PostMessage(
		c.channel,
		slackapi.MsgOptionBlocks(ctx),
		slackapi.MsgOptionText(text, false),
		slackapi.MsgOptionTS(c.threadTS),
	)
	if err == nil {
		c.statusTS = ts
		c.posted(ts)
	}
}

func (c *compatTurn) finish() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Delete transient messages
	if c.thinkTS != "" {
		c.api.DeleteMessage(c.channel, c.thinkTS)
	}
	if c.toolTS != "" {
		c.api.DeleteMessage(c.channel, c.toolTS)
	}
	if c.statusTS != "" {
		c.api.DeleteMessage(c.channel, c.statusTS)
	}

	// Finalize text: delete streaming message, post properly split final response
	finalText := c.textBuf.String()
	if c.textTS != "" {
		c.api.DeleteMessage(c.channel, c.textTS)
	}

	if finalText != "" {
		mrkdwn := c.convert(finalText)
		chunks := splitAtLines(mrkdwn, maxBlockTextLen)
		for _, chunk := range chunks {
			section := slackapi.NewSectionBlock(
				slackapi.NewTextBlockObject("mrkdwn", chunk, false, false),
				nil, nil,
			)
			_, ts, err := c.api.PostMessage(
				c.channel,
				slackapi.MsgOptionBlocks(section),
				slackapi.MsgOptionText(chunk, false),
				slackapi.MsgOptionTS(c.threadTS),
			)
			if err != nil {
				return err
			}
			c.posted(ts)
		}
	}

	return nil
}
