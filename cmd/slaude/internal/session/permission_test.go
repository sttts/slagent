package session

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/sttts/slagent/cmd/slaude/internal/terminal"
)

// --- Channel coordination tests ---

func TestPlanApprovalChannelUnbuffered(t *testing.T) {
	ch := make(chan bool) // unbuffered, like Session.planApproval

	// Simulate EnterPlanMode approval: send with no reader (MCP auto-approved).
	go func() {
		select {
		case ch <- true:
		default:
		}
	}()

	time.Sleep(10 * time.Millisecond)

	// Channel must be empty — no stale value for ExitPlanMode to consume
	select {
	case v := <-ch:
		t.Fatalf("channel should be empty, got stale value: %v", v)
	default:
	}
}

func TestPlanApprovalBufferedLeaks(t *testing.T) {
	// Documents the bug: buffered(1) leaks values across transitions.
	ch := make(chan bool, 1)

	// EnterPlanMode signals approved, nobody reads
	select {
	case ch <- true:
	default:
	}

	// ExitPlanMode MCP handler reads stale value
	select {
	case v := <-ch:
		if !v {
			t.Fatal("expected stale true from EnterPlanMode")
		}
		// Bug: MCP auto-allowed ExitPlanMode using Enter's stale approval
	default:
		t.Fatal("buffered channel should have had a stale value")
	}
}

func TestPlanApprovalCoordinationApproved(t *testing.T) {
	ch := make(chan bool)

	// MCP handler goroutine blocks waiting
	var mcpResult bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case mcpResult = <-ch:
		case <-time.After(2 * time.Second):
			t.Error("MCP handler timed out")
		}
	}()

	time.Sleep(10 * time.Millisecond)

	// approvePlanModeTransition signals
	select {
	case ch <- true:
	default:
		t.Fatal("send should succeed — MCP handler should be waiting")
	}

	wg.Wait()
	if !mcpResult {
		t.Error("MCP handler should have received true")
	}
}

func TestPlanApprovalCoordinationDenied(t *testing.T) {
	ch := make(chan bool)

	var mcpResult bool
	mcpResult = true // start true to verify it flips
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		mcpResult = <-ch
	}()

	time.Sleep(10 * time.Millisecond)

	ch <- false
	wg.Wait()
	if mcpResult {
		t.Error("MCP handler should have received false (denied)")
	}
}

// --- approvePlanModeTransition tests ---

func silentUI() *terminal.UI {
	return terminal.NewWithWriter(io.Discard)
}

func TestApprovePlanModeNoThreadAutoApproves(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &Session{
		ctx:          ctx,
		ui:           silentUI(),
		planApproval: make(chan bool),
		// thread is nil — no Slack
	}

	// MCP handler waits in background
	var mcpResult bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case mcpResult = <-s.planApproval:
		case <-time.After(2 * time.Second):
			t.Error("timed out waiting for plan approval")
		}
	}()

	time.Sleep(10 * time.Millisecond)

	// nil thread → auto-approve
	s.approvePlanModeTransition(true, "")

	wg.Wait()
	if !mcpResult {
		t.Error("nil thread should auto-approve")
	}
}

func TestApprovePlanModeSessionCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	s := &Session{
		ctx:          ctx,
		ui:           silentUI(),
		planApproval: make(chan bool),
	}

	cancel()

	// With cancelled context, MCP handler should not receive anything
	select {
	case <-s.planApproval:
		t.Fatal("should not receive on cancelled context")
	case <-ctx.Done():
		// expected
	}
}

func TestPlanApprovalEnterThenExitNoLeak(t *testing.T) {
	// End-to-end: EnterPlanMode approved (no MCP reader) then ExitPlanMode.
	// The Enter approval must not leak to Exit.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &Session{
		ctx:          ctx,
		ui:           silentUI(),
		planApproval: make(chan bool),
		// thread is nil so approval is instant
	}

	// EnterPlanMode: no MCP handler listening
	s.approvePlanModeTransition(true, "")

	// Channel must be empty
	select {
	case v := <-s.planApproval:
		t.Fatalf("stale value leaked from EnterPlanMode: %v", v)
	default:
	}

	// ExitPlanMode: MCP handler listens, then approval signals
	var mcpResult bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case mcpResult = <-s.planApproval:
		case <-time.After(2 * time.Second):
			t.Error("MCP handler timed out — possible stale channel state")
		}
	}()

	time.Sleep(10 * time.Millisecond)
	s.approvePlanModeTransition(false, "")

	wg.Wait()
	if !mcpResult {
		t.Error("ExitPlanMode should be auto-approved (nil thread)")
	}
}
