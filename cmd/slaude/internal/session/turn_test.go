package session

import (
	"testing"

	"github.com/sttts/slagent"
)

// mockTurn records Tool calls for verifying toolTracker behavior.
type mockTurn struct {
	calls []toolCall
}

type toolCall struct {
	id, name, status, detail string
}

func (m *mockTurn) Tool(id, name, status, detail string) {
	m.calls = append(m.calls, toolCall{id, name, status, detail})
}

func (m *mockTurn) Thinking(string)      {}
func (m *mockTurn) Text(string)           {}
func (m *mockTurn) Status(string)         {}
func (m *mockTurn) MarkQuestion(string)   {}
func (m *mockTurn) DeleteActivity()       {}
func (m *mockTurn) SetPlainText(bool)     {}
func (m *mockTurn) Finish() error         { return nil }

func TestToolTrackerStartFinish(t *testing.T) {
	m := &mockTurn{}
	tt := &toolTracker{turn: m}

	tt.Start("Read")
	if tt.id != "t1" || tt.name != "Read" || tt.seq != 1 {
		t.Errorf("after Start: id=%q name=%q seq=%d", tt.id, tt.name, tt.seq)
	}
	if len(m.calls) != 1 || m.calls[0].status != slagent.ToolRunning {
		t.Errorf("Start should emit running call, got %+v", m.calls)
	}

	tt.Finish()
	if tt.id != "" {
		t.Errorf("after Finish: id should be empty, got %q", tt.id)
	}
	if len(m.calls) != 2 || m.calls[1].status != slagent.ToolDone {
		t.Errorf("Finish should emit done call, got %+v", m.calls)
	}
	if m.calls[1].id != "t1" || m.calls[1].name != "Read" {
		t.Errorf("Finish should use same id/name, got %+v", m.calls[1])
	}
}

func TestToolTrackerStartFinishesPrevious(t *testing.T) {
	m := &mockTurn{}
	tt := &toolTracker{turn: m}

	tt.Start("Read")
	tt.Start("Write")

	// Should have: Read running, Read done, Write running
	if len(m.calls) != 3 {
		t.Fatalf("expected 3 calls, got %d: %+v", len(m.calls), m.calls)
	}
	if m.calls[0].name != "Read" || m.calls[0].status != slagent.ToolRunning {
		t.Errorf("calls[0] = %+v", m.calls[0])
	}
	if m.calls[1].name != "Read" || m.calls[1].status != slagent.ToolDone {
		t.Errorf("calls[1] = %+v", m.calls[1])
	}
	if m.calls[2].name != "Write" || m.calls[2].status != slagent.ToolRunning {
		t.Errorf("calls[2] = %+v", m.calls[2])
	}
	if tt.seq != 2 || tt.id != "t2" {
		t.Errorf("seq=%d id=%q, want seq=2 id=t2", tt.seq, tt.id)
	}
}

func TestToolTrackerUpdateSameName(t *testing.T) {
	m := &mockTurn{}
	tt := &toolTracker{turn: m}

	tt.Start("Read")
	tt.Update("Read", "main.go")

	// Update with same name and empty detail just sets detail, no new calls
	if tt.detail != "main.go" {
		t.Errorf("detail = %q, want main.go", tt.detail)
	}
	if tt.id != "t1" {
		t.Errorf("id should still be t1, got %q", tt.id)
	}
	// Only the Start call
	if len(m.calls) != 1 {
		t.Errorf("expected 1 call, got %d: %+v", len(m.calls), m.calls)
	}
}

func TestToolTrackerUpdateDifferentName(t *testing.T) {
	m := &mockTurn{}
	tt := &toolTracker{turn: m}

	tt.Start("Read")
	tt.Update("Read", "main.go")
	tt.Update("Write", "output.go")

	// Second Update with different name finishes Read and starts Write
	if tt.name != "Write" || tt.detail != "output.go" || tt.id != "t2" {
		t.Errorf("after different-name Update: name=%q detail=%q id=%q", tt.name, tt.detail, tt.id)
	}
	// Calls: Read running, Read done
	if len(m.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d: %+v", len(m.calls), m.calls)
	}
	if m.calls[1].name != "Read" || m.calls[1].status != slagent.ToolDone || m.calls[1].detail != "main.go" {
		t.Errorf("calls[1] = %+v", m.calls[1])
	}
}

func TestToolTrackerClear(t *testing.T) {
	m := &mockTurn{}
	tt := &toolTracker{turn: m}

	tt.Start("AskUserQuestion")
	tt.Clear()

	if tt.id != "" {
		t.Errorf("after Clear: id should be empty, got %q", tt.id)
	}

	// Finish after Clear should be a no-op (no done call)
	tt.Finish()
	if len(m.calls) != 1 {
		t.Errorf("Finish after Clear should not emit, got %d calls: %+v", len(m.calls), m.calls)
	}
}

func TestToolTrackerNilTurn(t *testing.T) {
	// With nil turn (no Slack), tracking still works but no calls are made
	tt := &toolTracker{turn: nil}

	tt.Start("Read")
	if tt.id != "t1" || tt.name != "Read" {
		t.Errorf("Start should still track state: id=%q name=%q", tt.id, tt.name)
	}

	tt.Finish()
	if tt.id != "" {
		t.Errorf("Finish should clear id even without turn")
	}
}

func TestToolTrackerFinishIncludesDetail(t *testing.T) {
	m := &mockTurn{}
	tt := &toolTracker{turn: m}

	tt.Start("Read")
	tt.Update("Read", "session.go")
	tt.Finish()

	// Finish should include the detail from Update
	if len(m.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(m.calls))
	}
	if m.calls[1].detail != "session.go" {
		t.Errorf("Finish detail = %q, want session.go", m.calls[1].detail)
	}
}

func TestSilentTurnsCountdown(t *testing.T) {
	s := &Session{silentTurnsLeft: 3}

	// Initial state: 3 turns allowed
	if s.silentTurnsLeft != 3 {
		t.Fatalf("initial = %d, want 3", s.silentTurnsLeft)
	}

	// Simulate silent turn: no output
	s.silentTurnsLeft--
	if s.silentTurnsLeft != 2 {
		t.Errorf("after 1 silent: %d, want 2", s.silentTurnsLeft)
	}

	s.silentTurnsLeft--
	s.silentTurnsLeft--
	if s.silentTurnsLeft != 0 {
		t.Errorf("after 3 silent: %d, want 0", s.silentTurnsLeft)
	}

	// Output resets to 3
	s.silentTurnsLeft = 3
	if s.silentTurnsLeft != 3 {
		t.Errorf("after reset: %d, want 3", s.silentTurnsLeft)
	}
}

func TestStartThinkingSuppressedWhenSilent(t *testing.T) {
	// Without a thread, startThinking always returns nil
	s := &Session{silentTurnsLeft: 0}
	turn := s.startThinking()
	if turn != nil {
		t.Error("startThinking with no thread should return nil")
	}

	// With silentTurnsLeft > 0 but no thread, still nil
	s.silentTurnsLeft = 3
	turn = s.startThinking()
	if turn != nil {
		t.Error("startThinking with no thread should return nil even with turns left")
	}
}

func TestStartThinkingCounterIntegration(t *testing.T) {
	s := &Session{silentTurnsLeft: 3}

	// Simulate 3 silent turns
	for i := 0; i < 3; i++ {
		if s.silentTurnsLeft <= 0 {
			t.Fatalf("should still have turns left at iteration %d", i)
		}
		s.silentTurnsLeft--
	}

	// Now suppressed
	if s.silentTurnsLeft > 0 {
		t.Error("should be suppressed after 3 silent turns")
	}

	// Output resets
	s.silentTurnsLeft = 3
	if s.silentTurnsLeft != 3 {
		t.Error("reset should restore to 3")
	}
}
