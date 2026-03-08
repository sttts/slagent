// Package claude manages Claude Code as a stream-json subprocess.
package claude

import "encoding/json"

// Event types emitted by Claude's stream-json output.
const (
	TypeSystem         = "system"
	TypeStreamEvent    = "stream_event"
	TypeAssistant      = "assistant"
	TypeResult         = "result"
	TypeRateLimitEvent = "rate_limit_event"
)

// Stream event subtypes.
const (
	EventMessageStart     = "message_start"
	EventContentBlockStart = "content_block_start"
	EventContentBlockDelta = "content_block_delta"
	EventContentBlockStop  = "content_block_stop"
	EventMessageDelta     = "message_delta"
	EventMessageStop      = "message_stop"
)

// Delta types within content_block_delta.
const (
	DeltaText      = "text_delta"
	DeltaThinking  = "thinking_delta"
	DeltaSignature = "signature_delta"
)

// RawEvent is a partially-parsed stream-json line.
type RawEvent struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Event     json.RawMessage `json:"event,omitempty"`
	Message   json.RawMessage `json:"message,omitempty"`

	// Result fields
	IsError    bool   `json:"is_error,omitempty"`
	Result     string `json:"result,omitempty"`
	NumTurns   int    `json:"num_turns,omitempty"`
	DurationMs int    `json:"duration_ms,omitempty"`
}

// StreamEventInner is the "event" object inside a stream_event.
type StreamEventInner struct {
	Type         string          `json:"type"`
	Index        int             `json:"index,omitempty"`
	Delta        json.RawMessage `json:"delta,omitempty"`
	ContentBlock json.RawMessage `json:"content_block,omitempty"`
}

// Delta is a content_block_delta payload.
type Delta struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// AssistantMessage is the "message" object inside an assistant event.
type AssistantMessage struct {
	ID      string         `json:"id"`
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// ContentBlock is a single block in an assistant message.
type ContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// Event is a high-level parsed event for consumers.
type Event struct {
	Type      string // system, text_delta, thinking, assistant, tool_use, result, etc.
	Text      string // for text_delta and assistant
	ToolName  string // for tool_use
	ToolInput string // for tool_use
	IsError   bool   // for result
	SessionID string
	Raw       *RawEvent
	RawJSON   []byte // original JSON line (set by ReadEvent)
}

// ParseEvent converts a JSON line into a high-level Event.
func ParseEvent(line []byte) (*Event, error) {
	var raw RawEvent
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}

	evt := &Event{Raw: &raw, SessionID: raw.SessionID}

	switch raw.Type {
	case TypeSystem:
		evt.Type = TypeSystem

	case TypeStreamEvent:
		var inner StreamEventInner
		if err := json.Unmarshal(raw.Event, &inner); err != nil {
			return nil, err
		}
		switch inner.Type {
		case EventContentBlockDelta:
			var d Delta
			if err := json.Unmarshal(inner.Delta, &d); err != nil {
				return nil, err
			}
			switch d.Type {
			case DeltaText:
				evt.Type = "text_delta"
				evt.Text = d.Text
			case DeltaThinking:
				evt.Type = "thinking"
				evt.Text = d.Text
			case DeltaSignature:
				evt.Type = "signature"
			default:
				evt.Type = "stream_other"
			}
		default:
			evt.Type = "stream_other"
		}

	case TypeAssistant:
		var msg AssistantMessage
		if err := json.Unmarshal(raw.Message, &msg); err != nil {
			return nil, err
		}
		// Collect text and tool uses
		var text string
		for _, block := range msg.Content {
			switch block.Type {
			case "text":
				text += block.Text
			case "tool_use":
				evt.Type = "tool_use"
				evt.ToolName = block.Name
				if block.Input != nil {
					evt.ToolInput = string(block.Input)
				}
			}
		}
		if evt.Type != "tool_use" {
			evt.Type = TypeAssistant
			evt.Text = text
		}

	case TypeResult:
		evt.Type = TypeResult
		evt.IsError = raw.IsError
		evt.Text = raw.Result

	default:
		evt.Type = raw.Type
	}

	return evt, nil
}
