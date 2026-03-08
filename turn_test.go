package slagent

import (
	"testing"
)

// fakeTurnWriter records all calls for verification.
type fakeTurnWriter struct {
	texts     []string
	thinks    []string
	tools     []toolCall
	statuses  []string
	question  bool
	qPrefix   string
	finished  bool
}

type toolCall struct {
	id, name, status, detail string
}

func (f *fakeTurnWriter) writeText(text string)                        { f.texts = append(f.texts, text) }
func (f *fakeTurnWriter) writeThinking(text string)                    { f.thinks = append(f.thinks, text) }
func (f *fakeTurnWriter) writeTool(id, name, status, detail string)    { f.tools = append(f.tools, toolCall{id, name, status, detail}) }
func (f *fakeTurnWriter) writeStatus(text string)                      { f.statuses = append(f.statuses, text) }
func (f *fakeTurnWriter) markQuestion(prefix string)                    { f.question = true; f.qPrefix = prefix }
func (f *fakeTurnWriter) finish() error                                { f.finished = true; return nil }

func TestTurnImplDelegation(t *testing.T) {
	w := &fakeTurnWriter{}
	turn := &turnImpl{w: w}

	turn.Thinking("think1")
	turn.Thinking("think2")
	turn.Tool("t1", "Read", "running", "main.go")
	turn.Tool("t1", "Read", "done", "")
	turn.Text("hello ")
	turn.Text("world")
	turn.Status("compiling...")
	turn.Finish()

	if len(w.thinks) != 2 {
		t.Errorf("thinks = %d, want 2", len(w.thinks))
	}
	if len(w.tools) != 2 {
		t.Errorf("tools = %d, want 2", len(w.tools))
	}
	if len(w.texts) != 2 {
		t.Errorf("texts = %d, want 2", len(w.texts))
	}
	if len(w.statuses) != 1 {
		t.Errorf("statuses = %d, want 1", len(w.statuses))
	}
	if !w.finished {
		t.Error("finish was not called")
	}

	// Verify tool status transitions
	if w.tools[0].status != "running" {
		t.Errorf("tool[0].status = %q, want running", w.tools[0].status)
	}
	if w.tools[1].status != "done" {
		t.Errorf("tool[1].status = %q, want done", w.tools[1].status)
	}
}
