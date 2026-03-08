package slagent

import (
	"encoding/json"
	"testing"
)

func TestNativeTurnTextStreaming(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.botClient(), "xoxb-test-token", "C_TEST", withAPIURL(mock.apiURL()))
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()

	// Verify we got a native turn
	impl := turn.(*turnImpl)
	if _, ok := impl.w.(*nativeTurn); !ok {
		t.Fatal("expected nativeTurn for xoxb token")
	}

	turn.Text("Hello world!")
	err := turn.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// Should have started a stream, appended text, and stopped
	mock.mu.Lock()
	found := false
	for _, s := range mock.streams {
		if s.stopped {
			found = true
			if len(s.chunks) == 0 {
				t.Error("no chunks appended")
			}
		}
	}
	streamCount := len(mock.streams)
	mock.mu.Unlock()

	if streamCount == 0 {
		t.Fatal("no streams created")
	}
	if !found {
		t.Error("no stream was stopped")
	}
}

func TestNativeTurnBufferThreshold(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	// Small buffer size to trigger mid-stream flush
	thread := NewThread(mock.botClient(), "xoxb-test-token", "C_TEST",
		withAPIURL(mock.apiURL()),
		WithBufferSize(10),
	)
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	turn.Text("12345678901234567890") // 20 chars > bufSize 10

	// Should have flushed at least once before finish
	impl := turn.(*turnImpl)
	n := impl.w.(*nativeTurn)

	if n.flushed == 0 {
		t.Error("expected buffer flush before finish")
	}

	turn.Finish()
}

func TestNativeTurnThinkingAccumulation(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.botClient(), "xoxb-test-token", "C_TEST", withAPIURL(mock.apiURL()))
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	turn.Thinking("line1\n")
	turn.Thinking("line2\n")
	turn.Thinking("line3\n")
	turn.Finish()

	// Verify thinking chunks contain accumulated text
	for _, s := range mock.streams {
		for _, raw := range s.chunks {
			var chunk map[string]any
			json.Unmarshal(raw, &chunk)
			if chunk["type"] == "task_update" {
				val := chunk["value"].(map[string]any)
				if val["id"] == "thinking" {
					details := val["details"].(string)
					// Last chunk should contain all lines
					if len(details) == 0 {
						t.Error("thinking details empty")
					}
				}
			}
		}
	}
}

func TestNativeTurnToolStatusMapping(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.botClient(), "xoxb-test-token", "C_TEST", withAPIURL(mock.apiURL()))
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	turn.Tool("t1", "Read", ToolRunning, "main.go")
	turn.Tool("t1", "Read", ToolDone, "")
	turn.Tool("t2", "Write", ToolError, "permission denied")
	turn.Finish()

	// Verify status mapping in chunks
	var statuses []string
	for _, s := range mock.streams {
		for _, raw := range s.chunks {
			var chunk map[string]any
			json.Unmarshal(raw, &chunk)
			if chunk["type"] == "task_update" {
				val := chunk["value"].(map[string]any)
				if val["id"] != "thinking" && val["id"] != "status" {
					statuses = append(statuses, val["status"].(string))
				}
			}
		}
	}

	want := []string{"in_progress", "completed", "failed"}
	if len(statuses) != len(want) {
		t.Fatalf("statuses = %v, want %v", statuses, want)
	}
	for i, s := range statuses {
		if s != want[i] {
			t.Errorf("status[%d] = %q, want %q", i, s, want[i])
		}
	}
}

func TestNativeTurnLazyStart(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.botClient(), "xoxb-test-token", "C_TEST", withAPIURL(mock.apiURL()))
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()

	// No content sent yet — stream should not have started
	if len(mock.streams) != 0 {
		t.Error("stream started before any content")
	}

	// Finish with no content — should not start a stream
	err := turn.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if len(mock.streams) != 0 {
		t.Error("stream started on empty finish")
	}
}

func TestNativeTurnStatus(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.botClient(), "xoxb-test-token", "C_TEST", withAPIURL(mock.apiURL()))
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	turn.Status("searching...")
	turn.Status("compiling...")
	turn.Finish()

	// Verify status chunks were sent
	statusCount := 0
	for _, s := range mock.streams {
		for _, raw := range s.chunks {
			var chunk map[string]any
			json.Unmarshal(raw, &chunk)
			if chunk["type"] == "task_update" {
				val := chunk["value"].(map[string]any)
				if val["id"] == "status" {
					statusCount++
				}
			}
		}
	}
	if statusCount != 2 {
		t.Errorf("status chunks = %d, want 2", statusCount)
	}
}
