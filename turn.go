package slagent

// Turn streams one agent response turn to a Slack thread.
type Turn interface {
	// Thinking appends thinking/reasoning content.
	Thinking(text string)

	// Tool reports tool activity. Status: "running", "done", "error".
	Tool(id, name, status, detail string)

	// Text appends response text (markdown).
	Text(text string)

	// Status shows a transient status line.
	Status(text string)

	// MarkQuestion marks this turn as a question. The prefix is prepended
	// and trailing "?" is replaced with " ❓" on finish.
	MarkQuestion(prefix string)

	// Finish finalizes the turn. Must be called exactly once.
	Finish() error
}

// turnWriter is the internal interface that backends implement.
type turnWriter interface {
	writeText(text string)
	writeThinking(text string)
	writeTool(id, name, status, detail string)
	writeStatus(text string)
	markQuestion(prefix string)
	finish() error
}

// turnImpl wraps a turnWriter to implement Turn.
type turnImpl struct {
	w turnWriter
}

func (t *turnImpl) Thinking(text string)                    { t.w.writeThinking(text) }
func (t *turnImpl) Tool(id, name, status, detail string)    { t.w.writeTool(id, name, status, detail) }
func (t *turnImpl) Text(text string)                        { t.w.writeText(text) }
func (t *turnImpl) Status(text string)                      { t.w.writeStatus(text) }
func (t *turnImpl) MarkQuestion(prefix string)               { t.w.markQuestion(prefix) }
func (t *turnImpl) Finish() error                           { return t.w.finish() }
