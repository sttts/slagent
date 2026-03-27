package session

import (
	"fmt"
	"strings"

	"github.com/sttts/slagent"
	"github.com/sttts/slagent/cmd/slaude/internal/claude"
)

// toolTracker tracks tool state for Slack turn updates during a readTurn loop.
// It owns the current Turn so that approval-boundary splits can be done in one place.
type toolTracker struct {
	turn   slagent.Turn
	thread *slagent.Thread // for creating new turns on split
	seq    int
	id     string // current tool ID ("t1", "t2", ...)
	name   string // current tool name
	detail string // current tool detail
}

// SplitTurn ends the current Slack turn and starts a fresh one.
// Used at approval boundaries (plan mode, questions) so text before and after
// the boundary appears in separate Slack messages.
func (tt *toolTracker) SplitTurn() {
	tt.Clear()
	if tt.turn != nil {
		tt.turn.DeleteActivity()
		tt.turn.Finish()
	}
	if tt.thread != nil {
		tt.turn = tt.thread.NewTurn()
	}
}

// Start finishes the previous tool (if any) and begins tracking a new one.
func (tt *toolTracker) Start(name string) {
	tt.Finish()
	tt.seq++
	tt.id = fmt.Sprintf("t%d", tt.seq)
	tt.name = name
	tt.detail = ""
	if tt.turn != nil {
		tt.turn.Tool(tt.id, name, slagent.ToolRunning, "")
	}
}

// Update sets the detail for the current tool, or starts a new one if the name differs.
func (tt *toolTracker) Update(name, detail string) {
	if tt.name == name && tt.detail == "" {
		tt.detail = detail
		return
	}
	tt.Finish()
	tt.seq++
	tt.id = fmt.Sprintf("t%d", tt.seq)
	tt.name = name
	tt.detail = detail
}

// Finish marks the current tool as done in Slack and clears the tracker.
func (tt *toolTracker) Finish() {
	if tt.id != "" && tt.turn != nil {
		tt.turn.Tool(tt.id, tt.name, slagent.ToolDone, tt.detail)
	}
	tt.id = ""
}

// Clear stops tracking the current tool without posting a Finish to Slack.
func (tt *toolTracker) Clear() {
	tt.id = ""
}

// eventOrErr holds a ReadEvent result for channel communication.
type eventOrErr struct {
	evt *claude.Event
	err error
}

// readTurn reads events from Claude until the turn ends (result event).
// If earlyTurn is non-nil, it is used instead of creating a new turn
// (allows showing thinking activity before Claude starts responding).
func (s *Session) readTurn(earlyTurn ...slagent.Turn) error {
	s.ui.StartResponse()
	var fullText strings.Builder
	hadOutput := false

	// Set up slagent turn for Slack streaming.
	// The toolTracker owns the turn so SplitTurn can replace it at approval boundaries.
	tt := &toolTracker{thread: s.thread}
	if len(earlyTurn) > 0 && earlyTurn[0] != nil {
		tt.turn = earlyTurn[0]
	} else if s.thread != nil {
		tt.turn = s.thread.NewTurn()
	}

	// Drain stop channel before starting (ignore stale signals)
	select {
	case <-s.stopNotify:
	default:
	}

	// Read events in a goroutine so we can select on stop signals
	evtCh := make(chan eventOrErr, 1)
	readNext := func() {
		evt, err := s.proc.ReadEvent()
		evtCh <- eventOrErr{evt, err}
	}
	go readNext()

	var interrupted bool
	for {
		var evt *claude.Event
		var err error

		select {
		case result := <-evtCh:
			evt, err = result.evt, result.err
		case <-s.stopNotify:
			s.proc.Interrupt()
			interrupted = true
			s.ui.Info("⏹️ Interrupted")
			if s.thread != nil {
				s.thread.Post("⏹️ Interrupted")
			}
			result := <-evtCh
			evt, err = result.evt, result.err
		}

		if err != nil || evt == nil {
			if tt.turn != nil {
				tt.turn.Finish()
			}
			s.ui.EndResponse()

			// SIGINT may kill Claude (e.g. during Bash tool execution) instead
			// of just aborting the turn. Restart with --resume so the session
			// can continue.
			if interrupted {
				if restartErr := s.restartAfterInterrupt(); restartErr != nil {
					return fmt.Errorf("restart after interrupt: %w", restartErr)
				}
				return nil
			}
			if err != nil {
				return err
			}
			return fmt.Errorf("unexpected EOF from Claude")
		}

		if s.debugLog != nil {
			fmt.Fprintf(s.debugLog, "%s\n", evt.RawJSON)
		}

		switch evt.Type {
		case "text_delta":
			hadOutput = true
			s.ui.StreamText(evt.Text)
			fullText.WriteString(evt.Text)
			if tt.turn != nil {
				tt.turn.Text(evt.Text)
			}

		case "thinking":
			s.ui.Thinking(evt.Text)
			if tt.turn != nil {
				tt.turn.Thinking(evt.Text)
			}

		case claude.TypeAssistant:
			if fullText.Len() == 0 && evt.Text != "" {
				s.ui.StreamText(evt.Text)
				fullText.WriteString(evt.Text)
				if tt.turn != nil {
					tt.turn.Text(evt.Text)
				}
			}

		case "tool_start":
			hadOutput = true
			tt.Start(evt.ToolName)
			s.ui.ToolActivity(formatToolStart(evt.ToolName))

		case "input_json_delta":
			// Streaming tool input — full input arrives with tool_use event

		case "rate_limit":
			if evt.Text != "allowed" {
				msg := "⏳ Rate limited — waiting..."
				s.ui.Info(msg)
				if tt.turn != nil {
					tt.turn.Status(msg)
				}
			}

		case "tool_use":
			s.handleToolUse(tt, evt)

		case claude.TypeResult:
			tt.Finish()
			s.ui.EndResponse()
			if tt.turn != nil {
				tt.turn.Finish()
			}

			// Track silent turns for thinking activity suppression
			if hadOutput {
				s.silentTurnsLeft = 3
			} else if s.silentTurnsLeft > 0 {
				s.silentTurnsLeft--
			}

			s.repostTodos()
			return nil

		case claude.TypeSystem:
			tt.Finish()
		}

		go readNext()
	}
}

// handleToolUse processes a tool_use event from Claude.
func (s *Session) handleToolUse(tt *toolTracker, evt *claude.Event) {
	tt.Update(evt.ToolName, toolDetail(evt.ToolName, evt.ToolInput))
	s.ui.ToolActivity(formatTool(evt.ToolName, evt.ToolInput))

	// Slack turn interaction (prompts, plan mode, questions)
	if tt.turn != nil {
		if p := interactivePrompt(evt.ToolName, evt.ToolInput, s.thread.OwnerID(), s.thread.Emoji()); p != nil {
			s.thread.PostPrompt(p.text, p.reactions)
			tt.Clear()
		} else if evt.ToolName == "EnterPlanMode" || evt.ToolName == "ExitPlanMode" {
			tt.SplitTurn()
			// Plan text streams AFTER EnterPlanMode, so the new turn is plain text.
			// After ExitPlanMode, the new turn is back to normal mrkdwn.
			if tt.turn != nil {
				tt.turn.SetPlainText(evt.ToolName == "EnterPlanMode")
			}
			s.approvePlanModeTransition(evt.ToolName == "EnterPlanMode", evt.ToolInput)
		} else if evt.ToolName == "AskUserQuestion" {
			if hasQuestionsFormat(evt.ToolInput) {
				tt.SplitTurn()
			} else {
				var prefix string
				if ownerID := s.thread.OwnerID(); ownerID != "" {
					prefix = fmt.Sprintf("<@%s>: ", ownerID)
				}
				tt.turn.MarkQuestion(prefix)
			}
			tt.Clear()
		} else {
			tt.turn.Tool(tt.id, evt.ToolName, slagent.ToolRunning, tt.detail)
		}
	}

	if evt.ToolName == "TodoWrite" {
		s.updateTodos(evt.ToolInput)
	}

	// Post code blocks for Edit/Write. ExitPlanMode plan is posted
	// after approval in approvePlanModeTransition.
	if s.thread != nil && evt.ToolName != "ExitPlanMode" {
		if block := toolCodeBlock(evt.ToolName, evt.ToolInput); block != "" {
			s.thread.Post(s.thread.Emoji() + " " + block)
		}
	}
}

// startThinking creates a new turn and shows a thinking activity immediately,
// returning the turn for use by readTurn. This gives instant feedback in Slack
// before Claude starts responding.
func (s *Session) startThinking() slagent.Turn {
	if s.thread == nil {
		return nil
	}

	// Suppress thinking activity after too many silent turns
	if s.silentTurnsLeft <= 0 {
		return nil
	}

	turn := s.thread.NewTurn()
	turn.Thinking(" ")
	return turn
}
