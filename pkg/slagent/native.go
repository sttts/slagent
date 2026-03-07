package slagent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	slackapi "github.com/slack-go/slack"
)

// nativeTurn implements turnWriter using Slack's native streaming API
// (chat.startStream / chat.appendStream / chat.stopStream).
// Requires a bot token (xoxb-*).
type nativeTurn struct {
	api      *slackapi.Client
	token    string
	channel  string
	threadTS string
	convert  func(string) string
	posted   func(ts string)
	bufSize  int

	streamID string // set after startStream
	textBuf  strings.Builder
	started  bool

	mu sync.Mutex
}

func newNativeTurn(api *slackapi.Client, token, channel, threadTS string, convert func(string) string, posted func(string), bufSize int) *nativeTurn {
	return &nativeTurn{
		api:      api,
		token:    token,
		channel:  channel,
		threadTS: threadTS,
		convert:  convert,
		posted:   posted,
		bufSize:  bufSize,
	}
}

// startStream lazily starts the stream on first content.
func (n *nativeTurn) startStream() error {
	if n.started {
		return nil
	}

	resp, err := n.callAPI("chat.startStream", map[string]any{
		"channel":           n.channel,
		"thread_ts":         n.threadTS,
		"task_display_mode": "timeline",
	})
	if err != nil {
		return fmt.Errorf("chat.startStream: %w", err)
	}
	n.streamID = resp["stream_id"].(string)
	if ts, ok := resp["message_ts"].(string); ok {
		n.posted(ts)
	}
	n.started = true
	return nil
}

func (n *nativeTurn) writeText(text string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.textBuf.WriteString(text)

	// Flush when buffer exceeds threshold
	if n.textBuf.Len() >= n.bufSize {
		n.flushText()
	}
}

// flushText sends buffered text as a markdown_text chunk. Must be called with lock held.
func (n *nativeTurn) flushText() {
	if n.textBuf.Len() == 0 {
		return
	}
	if err := n.startStream(); err != nil {
		return
	}

	n.callAPI("chat.appendStream", map[string]any{
		"stream_id": n.streamID,
		"channel":   n.channel,
		"chunks": []map[string]any{{
			"type":  "markdown_text",
			"value": n.convert(n.textBuf.String()),
		}},
	})
	n.textBuf.Reset()
}

func (n *nativeTurn) writeThinking(text string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if err := n.startStream(); err != nil {
		return
	}

	// Show last 5 lines
	lines := strings.Split(text, "\n")
	if len(lines) > 5 {
		lines = append([]string{"…"}, lines[len(lines)-5:]...)
	}

	n.callAPI("chat.appendStream", map[string]any{
		"stream_id": n.streamID,
		"channel":   n.channel,
		"chunks": []map[string]any{{
			"type": "task_update",
			"value": map[string]any{
				"id":      "thinking",
				"status":  "in_progress",
				"details": strings.Join(lines, "\n"),
			},
		}},
	})
}

func (n *nativeTurn) writeTool(id, name, status, detail string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if err := n.startStream(); err != nil {
		return
	}

	taskStatus := "in_progress"
	switch status {
	case "done":
		taskStatus = "completed"
	case "error":
		taskStatus = "failed"
	}

	details := name
	if detail != "" {
		details += ": " + detail
	}

	n.callAPI("chat.appendStream", map[string]any{
		"stream_id": n.streamID,
		"channel":   n.channel,
		"chunks": []map[string]any{{
			"type": "task_update",
			"value": map[string]any{
				"id":      id,
				"status":  taskStatus,
				"details": details,
			},
		}},
	})
}

func (n *nativeTurn) writeStatus(text string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if err := n.startStream(); err != nil {
		return
	}

	n.callAPI("chat.appendStream", map[string]any{
		"stream_id": n.streamID,
		"channel":   n.channel,
		"chunks": []map[string]any{{
			"type": "task_update",
			"value": map[string]any{
				"id":      "status",
				"status":  "in_progress",
				"details": text,
			},
		}},
	})
}

func (n *nativeTurn) finish() error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if !n.started {
		// Nothing was streamed
		return nil
	}

	// Flush remaining text
	n.flushText()

	_, err := n.callAPI("chat.stopStream", map[string]any{
		"stream_id": n.streamID,
		"channel":   n.channel,
	})
	return err
}

// callAPI calls a Slack API method with JSON body.
func (n *nativeTurn) callAPI(method string, params map[string]any) (map[string]any, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", "https://slack.com/api/"+method, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+n.token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if ok, _ := result["ok"].(bool); !ok {
		errMsg, _ := result["error"].(string)
		return result, fmt.Errorf("%s: %s", method, errMsg)
	}
	return result, nil
}

// isNativeToken returns true if the token supports native streaming.
func isNativeToken(token string) bool {
	return strings.HasPrefix(token, "xoxb-")
}
