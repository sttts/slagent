package slagent

import (
	"strings"
	"testing"
	"time"
)

func TestCompatTurnTextStreaming(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	turn.Text("Hello ")
	turn.Text("world!")
	err := turn.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// After finish: text message updated to full content, not deleted
	active := mock.activeMessages()
	if len(active) == 0 {
		t.Fatal("no active messages after Finish")
	}

	// No messages should be deleted
	for _, m := range mock.postedMessages() {
		if m.Deleted {
			t.Error("no messages should be deleted")
		}
	}
}

func TestCompatTurnThinkingNotDeleted(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	turn.Thinking("Let me think about this...")
	turn.Thinking("\nMore thoughts")
	turn.Finish()

	// Activity message should still exist (not deleted)
	active := mock.activeMessages()
	found := false
	for _, m := range active {
		if m.Text == "activity" {
			found = true
		}
	}
	if !found {
		t.Error("activity message should persist after finish")
	}

	// No deletions
	for _, m := range mock.postedMessages() {
		if m.Deleted {
			t.Error("no messages should be deleted")
		}
	}
}

func TestCompatTurnUnifiedActivity(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	turn.Thinking("analyzing code")
	turn.Tool("t1", "Read", "running", "main.go")
	turn.Tool("t2", "Grep", "running", "pattern")
	turn.Status("compiling...")
	turn.Finish()

	// All activity should be in ONE message (same TS)
	active := mock.activeMessages()
	activityCount := 0
	for _, m := range active {
		if m.Text == "activity" {
			activityCount++
		}
	}
	if activityCount != 1 {
		t.Errorf("expected 1 activity message, got %d", activityCount)
	}

	// No deletions
	for _, m := range mock.postedMessages() {
		if m.Deleted {
			t.Error("no messages should be deleted")
		}
	}
}

func TestCompatTurnToolIcons(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	impl := turn.(*turnImpl)
	w := impl.w.(*compatTurn)

	turn.Tool("t1", "Read", "running", "main.go")

	w.mu.Lock()
	display := w.renderActivity()
	w.mu.Unlock()

	if !strings.Contains(display, ":claude:") {
		t.Error("running tool should use :claude: icon")
	}
}

func TestCompatTurnToolError(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	impl := turn.(*turnImpl)
	w := impl.w.(*compatTurn)

	turn.Tool("t1", "Bash", ToolError, "go build")

	w.mu.Lock()
	display := w.renderActivity()
	w.mu.Unlock()

	if !strings.Contains(display, "❌") {
		t.Error("error tool should show ❌")
	}
}

func TestCompatTurnEmptyFinish(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	// Finish with no content should not error
	turn := thread.NewTurn()
	err := turn.Finish()
	if err != nil {
		t.Fatalf("empty Finish: %v", err)
	}
}

func TestCompatActivityMaxLines(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	impl := turn.(*turnImpl)
	w := impl.w.(*compatTurn)

	// Add more than maxDisplayLines activities
	for i := 0; i < 10; i++ {
		turn.Tool("t"+string(rune('0'+i)), "Tool", "done", "")
	}

	w.mu.Lock()
	display := w.renderActivity()
	w.mu.Unlock()

	lines := strings.Split(display, "\n")
	if len(lines) > maxDisplayLines {
		t.Errorf("activity lines = %d, want <= %d", len(lines), maxDisplayLines)
	}
}

func TestCompatFinishUpdatesTextMessage(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	turn.Text("line 1\n")
	turn.Text("line 2\n")
	turn.Text("line 3\n")
	err := turn.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// The text message should exist and be updated (not deleted + re-posted)
	active := mock.activeMessages()
	textMsgFound := false
	for _, m := range active {
		if m.Text != "activity" && m.Text != "" && m.IsUpdate {
			textMsgFound = true
		}
	}
	if !textMsgFound {
		t.Error("text message should be updated on finish")
	}
}

func TestCompatTextFlushedBeforeTool(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()

	// Stream text character-by-character (like real deltas).
	// First char creates the message, rest are throttled (within 1s).
	// Without finalizeText, the message would show only "G".
	fullText := "Good — I have a thorough understanding of the codebase now."
	for _, ch := range fullText {
		turn.Text(string(ch))
	}
	turn.Tool("t1", "Read", ToolRunning, "main.go")

	// Find the text message (not the activity message)
	active := mock.activeMessages()
	var textMsg *mockMessage
	for i, m := range active {
		if m.Text != "activity" {
			textMsg = &active[i]
			break
		}
	}
	if textMsg == nil {
		t.Fatal("no text message found")
	}

	content := textMsg.blockText()
	if !strings.Contains(content, "thorough understanding") {
		t.Errorf("text message should contain full text before tool starts, got: %q", content)
	}

	turn.Finish()
}

func TestCompatTextFlushedBeforeThinking(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()

	// Stream text character-by-character, then start thinking
	fullText := "Let me analyze this carefully."
	for _, ch := range fullText {
		turn.Text(string(ch))
	}
	turn.Thinking("Considering the architecture...")

	active := mock.activeMessages()
	var textMsg *mockMessage
	for i, m := range active {
		if m.Text != "activity" {
			textMsg = &active[i]
			break
		}
	}
	if textMsg == nil {
		t.Fatal("no text message found")
	}

	content := textMsg.blockText()
	if !strings.Contains(content, "analyze this carefully") {
		t.Errorf("text message should contain full text before thinking starts, got: %q", content)
	}

	turn.Finish()
}

func TestCompatTextHasInlineEmojiPrefix(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST", WithInstanceID("dog"))
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	turn.Text("Hello world")
	turn.Finish()

	// Find the text message
	active := mock.activeMessages()
	var textMsg *mockMessage
	for i, m := range active {
		if m.Text != "activity" {
			textMsg = &active[i]
			break
		}
	}
	if textMsg == nil {
		t.Fatal("no text message found")
	}

	content := textMsg.blockText()

	// Must start with identity emoji inline (not on a separate line)
	if !strings.HasPrefix(content, "🐶 ") {
		t.Errorf("text message should start with '🐶 ' (dog emoji), got: %q", content)
	}

	// Must contain the text (converted via MarkdownToMrkdwn)
	if !strings.Contains(content, "Hello world") {
		t.Errorf("text message should contain 'Hello world', got: %q", content)
	}
}

func TestCompatTextMarkdownConverted(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	turn.Text("# Heading\n**bold** and [link](http://example.com)")
	turn.Finish()

	active := mock.activeMessages()
	var textMsg *mockMessage
	for i, m := range active {
		if m.Text != "activity" {
			textMsg = &active[i]
			break
		}
	}
	if textMsg == nil {
		t.Fatal("no text message found")
	}

	content := textMsg.blockText()

	// Markdown headings → bold, **bold** → *bold*, links → Slack format
	if !strings.Contains(content, "*Heading*") {
		t.Errorf("heading should be converted to bold, got: %q", content)
	}
	if !strings.Contains(content, "<http://example.com|link>") {
		t.Errorf("link should be converted to Slack format, got: %q", content)
	}
}

func TestCompatToolSingleIcon(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	impl := turn.(*turnImpl)
	w := impl.w.(*compatTurn)

	// Running tool should show tool icon only
	turn.Tool("t1", "Read", ToolRunning, "main.go")

	w.mu.Lock()
	display := w.renderActivity()
	w.mu.Unlock()

	if !strings.Contains(display, ":claude: Read: main.go") {
		t.Errorf("running tool should show :claude:, got: %q", display)
	}
	if strings.Contains(display, "✓") || strings.Contains(display, "⏳") {
		t.Error("running tool should not have status marker")
	}
}

func TestCompatToolDoneUpdatesInPlace(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	impl := turn.(*turnImpl)
	w := impl.w.(*compatTurn)

	// Add tool as running, then mark done
	turn.Tool("t1", "Read", ToolRunning, "main.go")
	turn.Tool("t1", "Read", ToolDone, "main.go")

	w.mu.Lock()
	display := w.renderActivity()
	lineCount := len(w.activities)
	w.mu.Unlock()

	// Should have exactly 1 activity line (updated in place, not appended)
	if lineCount != 1 {
		t.Errorf("expected 1 activity line, got %d", lineCount)
	}

	// Done tool should show ✅, not tool icon
	if !strings.Contains(display, "✓ Read: main.go") {
		t.Errorf("done tool should show ✅, got: %q", display)
	}
	if strings.Contains(display, ":claude:") {
		t.Error("done tool should not show :claude:")
	}
}

func TestCompatToolSequenceNoDoubleIcons(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	impl := turn.(*turnImpl)
	w := impl.w.(*compatTurn)

	// Simulate a sequence: Read running → done, Grep running → done
	turn.Tool("t1", "Read", ToolRunning, "file.go")
	turn.Tool("t1", "Read", ToolDone, "file.go")
	turn.Tool("t2", "Grep", ToolRunning, "pattern")
	turn.Tool("t2", "Grep", ToolDone, "pattern")

	w.mu.Lock()
	display := w.renderActivity()
	lineCount := len(w.activities)
	w.mu.Unlock()

	// Should have 2 lines (one per tool ID)
	if lineCount != 2 {
		t.Errorf("expected 2 activity lines, got %d", lineCount)
	}

	// Each line should have exactly one icon (✅ or :claude: or ❌)
	for _, line := range strings.Split(display, "\n") {
		iconCount := strings.Count(line, "✓") + strings.Count(line, ":claude:") +
			strings.Count(line, "❌")
		if iconCount != 1 {
			t.Errorf("line should have exactly 1 icon, got %d: %q", iconCount, line)
		}
	}
}

func TestCompatTextDebounceFlushed(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()

	// Stream text char-by-char: first char posts, rest are throttled
	fullText := "Good — I have a thorough understanding."
	for _, ch := range fullText {
		turn.Text(string(ch))
	}

	// At this point, text message has only "G" (or partial), rest is throttled.
	// Wait for debounce timer to fire (1s + margin).
	time.Sleep(1200 * time.Millisecond)

	// After debounce, the text message should have the full content
	active := mock.activeMessages()
	var textMsg *mockMessage
	for i, m := range active {
		if m.Text != "activity" {
			textMsg = &active[i]
			break
		}
	}
	if textMsg == nil {
		t.Fatal("no text message found")
	}

	content := textMsg.blockText()
	if !strings.Contains(content, "thorough understanding") {
		t.Errorf("debounce should have flushed full text, got: %q", content)
	}

	turn.Finish()
}

func TestCompatActivityDebounceFlushed(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()

	// First tool posts activity, rapid subsequent tools are throttled
	turn.Tool("t1", "Read", ToolRunning, "file1.go")
	turn.Tool("t1", "Read", ToolDone, "file1.go")
	turn.Tool("t2", "Read", ToolRunning, "file2.go")
	turn.Tool("t2", "Read", ToolDone, "file2.go")
	turn.Tool("t3", "Read", ToolRunning, "file3.go")

	// Wait for debounce
	time.Sleep(1200 * time.Millisecond)

	impl := turn.(*turnImpl)
	w := impl.w.(*compatTurn)

	// Find the activity message
	active := mock.activeMessages()
	var activityMsg *mockMessage
	for i, m := range active {
		if m.Text == "activity" {
			activityMsg = &active[i]
		}
	}
	if activityMsg == nil {
		t.Fatal("no activity message found")
	}

	// Check the activity content has all tools
	content := activityMsg.blockText()
	_ = w // accessed above for display check

	if !strings.Contains(content, "file3.go") {
		t.Errorf("debounce should have flushed latest tool, got: %q", content)
	}

	turn.Finish()
}

func TestCompatMarkQuestionReplacesTrailingQuestionMark(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST", WithInstanceID("cat"))
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	turn.Text("What do you mean by Sandbox?")
	turn.MarkQuestion("<@U123>: ")
	turn.Finish()

	// Find the text message
	active := mock.activeMessages()
	var textMsg *mockMessage
	for i, m := range active {
		if m.Text != "activity" {
			textMsg = &active[i]
			break
		}
	}
	if textMsg == nil {
		t.Fatal("no text message found")
	}

	content := textMsg.blockText()

	// Must end with ❓, not ?
	if strings.Contains(content, "Sandbox?") {
		t.Errorf("trailing ? should be replaced with ❓, got: %q", content)
	}
	if !strings.Contains(content, "Sandbox ❓") {
		t.Errorf("should end with ' ❓', got: %q", content)
	}

	// Must have @mention inline with identity emoji — prefix prepended at finish
	if !strings.HasPrefix(content, "🐱 <@U123>: ") {
		t.Errorf("should start with '🐱 <@U123>: ', got: %q", content)
	}
}

func TestCompatMarkQuestionAppendsWhenNoQuestionMark(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	turn.Text("Please clarify what you mean.")
	turn.MarkQuestion("<@U123>: ")
	turn.Finish()

	active := mock.activeMessages()
	var textMsg *mockMessage
	for i, m := range active {
		if m.Text != "activity" {
			textMsg = &active[i]
			break
		}
	}
	if textMsg == nil {
		t.Fatal("no text message found")
	}

	content := textMsg.blockText()

	// No trailing ?, so ❓ should be appended
	if !strings.HasSuffix(content, " ❓") {
		t.Errorf("should end with ' ❓', got: %q", content)
	}
}

func TestCompatNoMarkQuestionKeepsQuestionMark(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	// Regular turn (not a question) — should NOT modify trailing ?
	turn := thread.NewTurn()
	turn.Text("Should we proceed?")
	turn.Finish()

	active := mock.activeMessages()
	var textMsg *mockMessage
	for i, m := range active {
		if m.Text != "activity" {
			textMsg = &active[i]
			break
		}
	}
	if textMsg == nil {
		t.Fatal("no text message found")
	}

	content := textMsg.blockText()

	// Without MarkQuestion(), the ? stays as-is
	if !strings.Contains(content, "proceed?") {
		t.Errorf("regular turn should keep ?, got: %q", content)
	}
	if strings.Contains(content, "❓") {
		t.Errorf("regular turn should not have ❓, got: %q", content)
	}
}

func TestCompatMarkQuestionMultipleQuestionMarks(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	turn.Text("What do you want? Could you clarify?")
	turn.MarkQuestion("<@U123>: ")
	turn.Finish()

	active := mock.activeMessages()
	var textMsg *mockMessage
	for i, m := range active {
		if m.Text != "activity" {
			textMsg = &active[i]
			break
		}
	}
	if textMsg == nil {
		t.Fatal("no text message found")
	}

	content := textMsg.blockText()

	// Only the LAST ? is replaced
	if !strings.Contains(content, "want?") {
		t.Errorf("first ? should be kept, got: %q", content)
	}
	if !strings.Contains(content, "clarify ❓") {
		t.Errorf("last ? should be replaced with ❓, got: %q", content)
	}
}

func TestCompatActivityDeletedWhenTextFollows(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	turn.Tool("t1", "Read", ToolRunning, "main.go")
	turn.Tool("t1", "Read", ToolDone, "main.go")

	// Activity message should exist before text
	active := mock.activeMessages()
	activityCount := 0
	for _, m := range active {
		if m.Text == "activity" {
			activityCount++
		}
	}
	if activityCount != 1 {
		t.Fatalf("expected 1 activity message before text, got %d", activityCount)
	}

	// Text arrives — activity should be deleted
	turn.Text("I read the file.")
	turn.Finish()

	active = mock.activeMessages()
	for _, m := range active {
		if m.Text == "activity" {
			t.Error("activity message should be deleted when text follows")
		}
	}

	// Text message should exist
	textFound := false
	for _, m := range active {
		if m.Text != "activity" && strings.Contains(m.blockText(), "I read the file") {
			textFound = true
		}
	}
	if !textFound {
		t.Error("text message should exist after activity deletion")
	}
}

func TestCompatActivityPersistsWithoutText(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	// Tool-only turn (no text follows)
	turn := thread.NewTurn()
	turn.Tool("t1", "Read", ToolRunning, "main.go")
	turn.Tool("t1", "Read", ToolDone, "main.go")
	turn.Finish()

	// Activity should persist (no text to delete it)
	active := mock.activeMessages()
	activityFound := false
	for _, m := range active {
		if m.Text == "activity" {
			activityFound = true
		}
	}
	if !activityFound {
		t.Error("activity should persist when no text follows")
	}

	// No deletions
	for _, m := range mock.postedMessages() {
		if m.Deleted {
			t.Error("no messages should be deleted in tool-only turn")
		}
	}
}

func TestCompatActivityNotRecreatedAfterDeletion(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	turn.Tool("t1", "Read", ToolRunning, "main.go")

	// Text arrives — deletes activity
	turn.Text("Done.")

	// ToolDone update should NOT recreate activity
	turn.Tool("t1", "Read", ToolDone, "main.go")
	turn.Finish()

	active := mock.activeMessages()
	for _, m := range active {
		if m.Text == "activity" {
			t.Error("activity should not be recreated after deletion by text")
		}
	}
}

func TestCompatNewToolAfterTextStartsFreshActivity(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()

	// First tool → activity posted
	turn.Tool("t1", "Read", ToolRunning, "main.go")

	// Text → activity deleted
	turn.Text("Read the file.")

	// New tool (different ID) → fresh activity should be created
	turn.Tool("t2", "Grep", ToolRunning, "pattern")
	turn.Finish()

	active := mock.activeMessages()
	activityFound := false
	for _, m := range active {
		if m.Text == "activity" {
			activityFound = true
			content := m.blockText()
			if !strings.Contains(content, "Grep") {
				t.Errorf("new activity should show Grep, got: %q", content)
			}
		}
	}
	if !activityFound {
		t.Error("new tool after text should start fresh activity")
	}
}

func TestCompatThinkingThenTextDeletesActivity(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()
	turn.Thinking("analyzing...")
	turn.Text("Here is my analysis.")
	turn.Finish()

	active := mock.activeMessages()
	for _, m := range active {
		if m.Text == "activity" {
			t.Error("activity (thinking) should be deleted when text follows")
		}
	}
}

func TestCompatFinishFullText(t *testing.T) {
	mock := newMockSlack()
	defer mock.close()

	thread := NewThread(mock.client(), "xoxc-test", "C_TEST")
	thread.Resume("1700000001.000000")

	turn := thread.NewTurn()

	// Stream many text chunks (simulating character-by-character deltas)
	fullText := "Good — I have a thorough understanding of the codebase now. Let me design the implementation."
	for _, ch := range fullText {
		turn.Text(string(ch))
	}
	turn.Finish()

	// After finish, text message must contain the full text
	active := mock.activeMessages()
	var textMsg *mockMessage
	for i, m := range active {
		if m.Text != "activity" {
			textMsg = &active[i]
			break
		}
	}
	if textMsg == nil {
		t.Fatal("no text message found")
	}

	content := textMsg.blockText()
	if !strings.Contains(content, "thorough understanding") {
		t.Errorf("finished text should contain full text, got: %q", content)
	}
	if !strings.Contains(content, "design the implementation") {
		t.Errorf("finished text should contain ending, got: %q", content)
	}
}
