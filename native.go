package slagent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

const defaultSlackAPIURL = "https://slack.com/api/"

// nativeTurn implements turnWriter using Slack's native streaming API
// (chat.startStream / chat.appendStream / chat.stopStream).
// Requires a bot token (xoxb-*).
type nativeTurn struct {
	token    string
	apiURL   string // base URL for API calls (default: https://slack.com/api/)
	channel  string
	threadTS string
	convert  func(string) string
	posted   func(ts string)
	bufSize  int

	streamID string // set after startStream
	fullText strings.Builder // accumulated raw text (pre-conversion)
	flushed  int             // bytes of fullText already flushed
	thinkBuf strings.Builder // accumulated thinking text
	started  bool
	question bool   // replace trailing ? with ❓ on finish
	qPrefix  string // prepended to text on finish

	mu sync.Mutex
}

func newNativeTurn(token, apiURL, channel, threadTS string, convert func(string) string, posted func(string), bufSize int) *nativeTurn {
	if apiURL == "" {
		apiURL = defaultSlackAPIURL
	}
	return &nativeTurn{
		token:    token,
		apiURL:   apiURL,
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

	streamID, ok := resp["stream_id"].(string)
	if !ok {
		return fmt.Errorf("chat.startStream: missing stream_id in response")
	}
	n.streamID = streamID

	if ts, ok := resp["message_ts"].(string); ok {
		n.posted(ts)
	}
	n.started = true
	return nil
}

func (n *nativeTurn) writeText(text string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.fullText.WriteString(text)

	// Flush when unflushed portion exceeds threshold
	if n.fullText.Len()-n.flushed >= n.bufSize {
		n.flushText()
	}
}

// flushText converts and sends the unflushed portion of fullText.
// Converts the entire accumulated text, then sends only the new portion.
// Must be called with lock held.
func (n *nativeTurn) flushText() {
	if n.fullText.Len() == n.flushed {
		return
	}
	if err := n.startStream(); err != nil {
		return
	}

	// Convert full text to get correct cross-boundary markdown
	converted := n.convert(n.fullText.String())

	// Send only the portion after what we already flushed
	// On first flush, send everything; on subsequent, approximate the new chunk
	// by converting old prefix and taking the diff
	var chunk string
	if n.flushed == 0 {
		chunk = converted
	} else {
		oldConverted := n.convert(n.fullText.String()[:n.flushed])
		if strings.HasPrefix(converted, oldConverted) {
			chunk = converted[len(oldConverted):]
		} else {
			// Conversion changed earlier text (rare); send full reconvert
			chunk = converted
		}
	}

	if chunk != "" {
		n.callAPI("chat.appendStream", map[string]any{
			"stream_id": n.streamID,
			"channel":   n.channel,
			"chunks": []map[string]any{{
				"type":  "markdown_text",
				"value": chunk,
			}},
		})
	}
	n.flushed = n.fullText.Len()
}

func (n *nativeTurn) writeThinking(text string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.thinkBuf.WriteString(text)

	if err := n.startStream(); err != nil {
		return
	}

	// Show last 5 lines of accumulated thinking
	display := n.thinkBuf.String()
	lines := strings.Split(display, "\n")
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
	case ToolDone:
		taskStatus = "completed"
	case ToolError:
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

func (n *nativeTurn) markQuestion(prefix string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.question = true
	n.qPrefix = prefix
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

	// Question turns: prepend @mention, replace trailing ? with ❓
	if n.question && n.fullText.Len() > 0 {
		s := n.qPrefix + strings.TrimRight(n.fullText.String(), "\n ")
		n.fullText.Reset()
		if strings.HasSuffix(s, "?") {
			n.fullText.WriteString(s[:len(s)-1] + " ❓")
		} else {
			n.fullText.WriteString(s + " ❓")
		}
	}

	// Flush remaining text (may lazily start the stream)
	n.flushText()

	if !n.started {
		return nil
	}

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

	req, err := http.NewRequest("POST", n.apiURL+method, strings.NewReader(string(body)))
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
