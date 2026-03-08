package slagent

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sttts/pairplan/pkg/terminal"
)

// turnEvent represents a single event from the Claude stream-JSON protocol,
// mapped to the Turn method calls that readTurn would make.
type turnEvent struct {
	kind   string // "text", "thinking", "tool", "tool_done", "tool_error", "status", "question"
	text   string // text content or tool name
	detail string // tool detail or question prefix
}

// formatToolForTerminal mirrors session.go's formatTool for the test harness.
func formatToolForTerminal(name, detail string) string {
	icons := map[string]string{
		"Read": "📄", "Glob": "🔍", "Grep": "🔍", "Bash": "💻",
		"Agent": "🤖", "Edit": "✏️", "Write": "✏️",
		"WebSearch": "🔎", "WebFetch": "🌐",
	}
	icon := icons[name]
	if icon == "" {
		icon = "🔧"
	}
	if detail != "" {
		return icon + " " + name + ": " + detail
	}
	return icon + " " + name
}

func TestEventSequences(t *testing.T) {
	tests := []struct {
		name   string
		events []turnEvent

		// Slack expectations (text message)
		wantSlack       string // substring in Slack text message
		wantSlackPrefix string // Slack text message starts with
		wantSlackSuffix string // Slack text message ends with
		wantSlackNoText bool   // no Slack text message

		// Slack expectations (activity message)
		wantSlackActivity string // substring in Slack activity message

		// Terminal expectations (ANSI stripped)
		wantTerminal []string // substrings that must appear in terminal output (in order)
	}{
		{
			name: "pure text response",
			events: []turnEvent{
				{kind: "text", text: "Hello "},
				{kind: "text", text: "world!"},
			},
			wantSlackPrefix: "🤖 Hello",
			wantSlack:       "world!",
			wantTerminal:    []string{"🤖 Claude:", "Hello ", "world!"},
		},
		{
			name: "text with markdown converted to mrkdwn",
			events: []turnEvent{
				{kind: "text", text: "# Plan\n**bold** text"},
			},
			wantSlack:    "*bold*", // **bold** → *bold*
			wantTerminal: []string{"# Plan", "**bold** text"},
		},
		{
			name: "thinking then text",
			events: []turnEvent{
				{kind: "thinking", text: "Let me analyze..."},
				{kind: "text", text: "Here is my analysis."},
			},
			wantSlack:         "analysis",
			wantSlackActivity: ":claude:",
			wantTerminal:      []string{"💭 Let me analyze...", "Here is my analysis."},
		},
		{
			name: "tool running then done then text",
			events: []turnEvent{
				{kind: "tool", text: "Read", detail: "main.go"},
				{kind: "tool_done", text: "Read", detail: "main.go"},
				{kind: "text", text: "I read the file."},
			},
			wantSlack:         "I read the file",
			wantSlackActivity: "✓ Read: main.go",
			wantTerminal:      []string{"📄 Read: main.go", "I read the file"},
		},
		{
			name: "multiple tools then text",
			events: []turnEvent{
				{kind: "tool", text: "Read", detail: "a.go"},
				{kind: "tool_done", text: "Read", detail: "a.go"},
				{kind: "tool", text: "Grep", detail: "pattern"},
				{kind: "tool_done", text: "Grep", detail: "pattern"},
				{kind: "text", text: "Found the code."},
			},
			wantSlack:         "Found the code",
			wantSlackActivity: "✓ Grep: pattern",
			wantTerminal:      []string{"📄 Read: a.go", "🔍 Grep: pattern", "Found the code"},
		},
		{
			name: "text before tool (text flushed before activity)",
			events: []turnEvent{
				{kind: "text", text: "Let me check. "},
				{kind: "tool", text: "Read", detail: "main.go"},
				{kind: "tool_done", text: "Read", detail: "main.go"},
				{kind: "text", text: "Done."},
			},
			wantSlack:         "Done",
			wantSlackActivity: "✓ Read",
			wantTerminal:      []string{"Let me check.", "📄 Read: main.go", "Done."},
		},
		{
			name: "free-text question: text then AskUserQuestion",
			events: []turnEvent{
				{kind: "text", text: "What do you mean by Sandbox?"},
				{kind: "question", text: "<@U123>: "},
			},
			wantSlackPrefix: "🤖 <@U123>: ",
			wantSlack:       "Sandbox",
			wantSlackSuffix: " ❓",
			wantTerminal:    []string{"What do you mean by Sandbox?"},
		},
		{
			name: "free-text question: text without trailing ?",
			events: []turnEvent{
				{kind: "text", text: "Please tell me more."},
				{kind: "question", text: "<@U123>: "},
			},
			wantSlackPrefix: "🤖 <@U123>: ",
			wantSlackSuffix: " ❓",
			wantTerminal:    []string{"Please tell me more."},
		},
		{
			name: "free-text question: multi-line text",
			events: []turnEvent{
				{kind: "text", text: "I have a few questions:\n- What is the scope?\n- What is the timeline?"},
				{kind: "question", text: "<@U123>: "},
			},
			wantSlackPrefix: "🤖 <@U123>: ",
			wantSlack:       "scope",
			wantSlackSuffix: " ❓",
			wantTerminal:    []string{"scope", "timeline"},
		},
		{
			name: "free-text question: no owner",
			events: []turnEvent{
				{kind: "text", text: "What do you want?"},
				{kind: "question", text: ""},
			},
			wantSlackPrefix: "🤖 What",
			wantSlackSuffix: " ❓",
			wantTerminal:    []string{"What do you want?"},
		},
		{
			name: "thinking then tool then question",
			events: []turnEvent{
				{kind: "thinking", text: "analyzing..."},
				{kind: "tool", text: "Read", detail: "go.mod"},
				{kind: "tool_done", text: "Read", detail: "go.mod"},
				{kind: "text", text: "Could you clarify the requirements?"},
				{kind: "question", text: "<@U123>: "},
			},
			wantSlackPrefix:   "🤖 <@U123>: ",
			wantSlackSuffix:   " ❓",
			wantSlackActivity: "✓ Read",
			wantTerminal:      []string{"💭 analyzing...", "📄 Read: go.mod", "clarify the requirements"},
		},
		{
			name: "only thinking, no text",
			events: []turnEvent{
				{kind: "thinking", text: "pondering..."},
			},
			wantSlackNoText:   true,
			wantSlackActivity: ":claude:",
			wantTerminal:      []string{"💭 pondering..."},
		},
		{
			name: "tool error",
			events: []turnEvent{
				{kind: "tool", text: "Bash", detail: "go build"},
				{kind: "tool_error", text: "Bash", detail: "go build"},
				{kind: "text", text: "Build failed."},
			},
			wantSlack:         "Build failed",
			wantSlackActivity: "❌ Bash",
			wantTerminal:      []string{"💻 Bash: go build", "Build failed"},
		},
		{
			name: "status line in activity",
			events: []turnEvent{
				{kind: "status", text: "compiling..."},
				{kind: "text", text: "Done."},
			},
			wantSlack:         "Done",
			wantSlackActivity: "⏳ compiling",
			wantTerminal:      []string{"Done."},
		},
		{
			name: "question replaces only last ?",
			events: []turnEvent{
				{kind: "text", text: "Do you want A? Or B?"},
				{kind: "question", text: "<@U123>: "},
			},
			wantSlack:       "want A?",
			wantSlackSuffix: "Or B ❓",
			wantTerminal:    []string{"Do you want A? Or B?"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up Slack mock
			mock := newMockSlack()
			defer mock.close()

			thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
			thread.Resume("1700000001.000000")
			turn := thread.NewTurn()

			// Set up terminal UI with captured output
			var termBuf bytes.Buffer
			ui := terminal.NewWithWriter(&termBuf)
			ui.StartResponse()

			toolSeq := 0

			// Replay events through both Slack turn and terminal UI
			for _, ev := range tt.events {
				switch ev.kind {
				case "text":
					ui.StreamText(ev.text)
					turn.Text(ev.text)
				case "thinking":
					ui.Thinking(ev.text)
					turn.Thinking(ev.text)
				case "tool":
					toolSeq++
					ui.ToolActivity(formatToolForTerminal(ev.text, ev.detail))
					turn.Tool(toolID(toolSeq), ev.text, ToolRunning, ev.detail)
				case "tool_done":
					turn.Tool(toolID(toolSeq), ev.text, ToolDone, ev.detail)
				case "tool_error":
					turn.Tool(toolID(toolSeq), ev.text, ToolError, ev.detail)
				case "status":
					turn.Status(ev.text)
				case "question":
					turn.MarkQuestion(ev.text)
				}
			}

			ui.EndResponse()
			turn.Finish()

			// === Check Slack output ===
			active := mock.activeMessages()
			var textMsg, activityMsg *mockMessage
			for i, m := range active {
				if m.Text == "activity" {
					activityMsg = &active[i]
				} else {
					textMsg = &active[i]
				}
			}

			if tt.wantSlackNoText {
				if textMsg != nil {
					t.Errorf("slack: expected no text message, got: %q", textMsg.blockText())
				}
			} else if tt.wantSlack != "" || tt.wantSlackPrefix != "" || tt.wantSlackSuffix != "" {
				if textMsg == nil {
					t.Fatal("slack: expected text message, got none")
				}
				content := textMsg.blockText()

				if tt.wantSlack != "" && !strings.Contains(content, tt.wantSlack) {
					t.Errorf("slack: text should contain %q, got: %q", tt.wantSlack, content)
				}
				if tt.wantSlackPrefix != "" && !strings.HasPrefix(content, tt.wantSlackPrefix) {
					t.Errorf("slack: text should start with %q, got: %q", tt.wantSlackPrefix, content)
				}
				if tt.wantSlackSuffix != "" && !strings.HasSuffix(content, tt.wantSlackSuffix) {
					t.Errorf("slack: text should end with %q, got: %q", tt.wantSlackSuffix, content)
				}
			}

			if tt.wantSlackActivity != "" {
				if activityMsg == nil {
					t.Fatal("slack: expected activity message, got none")
				}
				content := activityMsg.blockText()
				if !strings.Contains(content, tt.wantSlackActivity) {
					t.Errorf("slack: activity should contain %q, got: %q", tt.wantSlackActivity, content)
				}
			}

			// === Check terminal output ===
			termOut := stripANSI(termBuf.String())
			for _, want := range tt.wantTerminal {
				if !strings.Contains(termOut, want) {
					t.Errorf("terminal: should contain %q, got: %q", want, termOut)
				}
			}
		})
	}
}

// stripANSI removes ANSI escape sequences from a string.
func stripANSI(s string) string {
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\033' && i+1 < len(s) && s[i+1] == '[' {
			// Skip until the final byte of the escape sequence
			j := i + 2
			for j < len(s) && s[j] >= 0x20 && s[j] <= 0x3F {
				j++
			}
			if j < len(s) {
				j++ // skip final byte
			}
			i = j
		} else {
			out.WriteByte(s[i])
			i++
		}
	}
	return out.String()
}

func toolID(seq int) string {
	return "t" + string(rune('0'+seq))
}
