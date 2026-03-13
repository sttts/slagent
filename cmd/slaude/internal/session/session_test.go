package session

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestInteractivePromptFreeTextReturnsNil(t *testing.T) {
	// Free-text AskUserQuestion (no allowedPrompts) must return nil
	// so it's handled inline in the turn text, not as a separate prompt message.
	input, _ := json.Marshal(map[string]any{
		"question": "What do you mean by Sandbox?",
	})

	result := interactivePrompt("AskUserQuestion", string(input), "U123", "🐂")
	if result != nil {
		t.Errorf("free-text AskUserQuestion should return nil, got: %+v", result)
	}
}

func TestInteractivePromptFreeTextNoOwnerReturnsNil(t *testing.T) {
	input, _ := json.Marshal(map[string]any{
		"question": "What do you mean?",
	})

	result := interactivePrompt("AskUserQuestion", string(input), "", "")
	if result != nil {
		t.Errorf("free-text AskUserQuestion without owner should return nil, got: %+v", result)
	}
}

func TestInteractivePromptMultiChoiceReturnsPrompt(t *testing.T) {
	// Multi-choice AskUserQuestion must return a prompt with number reactions.
	input, _ := json.Marshal(map[string]any{
		"question":       "Which approach?",
		"allowedPrompts": []string{"Option A", "Option B", "Option C"},
	})

	result := interactivePrompt("AskUserQuestion", string(input), "U123", "🐂")
	if result == nil {
		t.Fatal("multi-choice AskUserQuestion should return a prompt")
	}

	// Must have numbered reactions
	if len(result.reactions) != 3 {
		t.Errorf("reactions = %d, want 3", len(result.reactions))
	}
	if result.reactions[0] != "one" || result.reactions[1] != "two" || result.reactions[2] != "three" {
		t.Errorf("reactions = %v, want [one two three]", result.reactions)
	}

	// Must contain question text and options
	if !strings.Contains(result.text, "Which approach?") {
		t.Errorf("prompt should contain question, got: %q", result.text)
	}
	if !strings.Contains(result.text, "Option A") {
		t.Errorf("prompt should contain options, got: %q", result.text)
	}

	// Must contain @mention and emoji
	if !strings.Contains(result.text, "<@U123>") {
		t.Errorf("prompt should contain mention, got: %q", result.text)
	}
	if !strings.Contains(result.text, "🐂") {
		t.Errorf("prompt should contain emoji, got: %q", result.text)
	}
}

func TestInteractivePromptQuestionsFormatReturnsNil(t *testing.T) {
	// Questions format is handled by handleAskUserQuestion, not interactivePrompt
	input, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"question": "What kind of opponent?",
				"options": []map[string]any{
					{"label": "Human vs AI", "description": "Play against computer"},
				},
				"multiSelect": false,
			},
		},
	})

	result := interactivePrompt("AskUserQuestion", string(input), "U123", "🐂")
	if result != nil {
		t.Errorf("questions format should return nil (handled by handleAskUserQuestion), got: %+v", result)
	}
}

func TestInteractivePromptExitPlanModeReturnsNil(t *testing.T) {
	// ExitPlanMode is handled by the permission system, not interactivePrompt
	result := interactivePrompt("ExitPlanMode", "{}", "U123", "🐂")
	if result != nil {
		t.Errorf("ExitPlanMode should return nil (handled by permission system), got: %+v", result)
	}
}

func TestInteractivePromptEnterPlanModeReturnsNil(t *testing.T) {
	// EnterPlanMode is handled by the permission system, not interactivePrompt
	result := interactivePrompt("EnterPlanMode", "{}", "U123", "🐂")
	if result != nil {
		t.Errorf("EnterPlanMode should return nil (handled by permission system), got: %+v", result)
	}
}

func TestInteractivePromptUnknownToolReturnsNil(t *testing.T) {
	result := interactivePrompt("Read", `{"file_path":"main.go"}`, "U123", "🐂")
	if result != nil {
		t.Errorf("unknown tool should return nil, got: %+v", result)
	}
}

func TestToolDetailToolSearch(t *testing.T) {
	input, _ := json.Marshal(map[string]any{
		"query": "select:AskUserQuestion",
	})
	d := toolDetail("ToolSearch", string(input))
	if d != "select:AskUserQuestion" {
		t.Errorf("toolDetail = %q, want %q", d, "select:AskUserQuestion")
	}
}

func TestFormatToolToolSearch(t *testing.T) {
	input, _ := json.Marshal(map[string]any{
		"query": "select:AskUserQuestion",
	})
	f := formatTool("ToolSearch", string(input))
	if f != "🔍 select:AskUserQuestion" {
		t.Errorf("formatTool = %q, want %q", f, "🔍 select:AskUserQuestion")
	}
}

func TestFormatTodos(t *testing.T) {
	tests := []struct {
		name  string
		todos []todo
		want  string
	}{
		{
			name: "mixed statuses",
			todos: []todo{
				{Content: "Set up project", Status: "completed"},
				{Content: "Write tests", Status: "in_progress"},
				{Content: "Deploy", Status: "pending"},
			},
			want: "📋 *Tasks*\n  ✅ ~Set up project~\n  ⏳ Write tests\n  ☐ Deploy",
		},
		{
			name: "all pending",
			todos: []todo{
				{Content: "Task A", Status: "pending"},
				{Content: "Task B", Status: "pending"},
			},
			want: "📋 *Tasks*\n  ☐ Task A\n  ☐ Task B",
		},
		{
			name:  "single completed",
			todos: []todo{{Content: "Done thing", Status: "completed"}},
			want:  "📋 *Tasks*\n  ✅ ~Done thing~",
		},
		{
			name:  "unknown status treated as pending",
			todos: []todo{{Content: "Mystery", Status: "unknown"}},
			want:  "📋 *Tasks*\n  ☐ Mystery",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Session{todos: tt.todos}
			got := s.formatTodos()
			if got != tt.want {
				t.Errorf("formatTodos() =\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestUpdateTodosParses(t *testing.T) {
	// updateTodos with nil thread should parse and store todos without error
	input, _ := json.Marshal(map[string]any{
		"todos": []map[string]any{
			{"content": "Write code", "status": "in_progress"},
			{"content": "Review", "status": "pending"},
		},
	})

	s := &Session{}
	s.updateTodos(string(input))

	if len(s.todos) != 2 {
		t.Fatalf("len(todos) = %d, want 2", len(s.todos))
	}
	if s.todos[0].Content != "Write code" || s.todos[0].Status != "in_progress" {
		t.Errorf("todos[0] = %+v", s.todos[0])
	}
	if s.todos[1].Content != "Review" || s.todos[1].Status != "pending" {
		t.Errorf("todos[1] = %+v", s.todos[1])
	}
}

func TestUpdateTodosEmptyClearsList(t *testing.T) {
	s := &Session{
		todos: []todo{{Content: "Old task", Status: "pending"}},
	}

	// Empty todos input clears the list
	s.updateTodos(`{"todos":[]}`)
	if len(s.todos) != 0 {
		t.Errorf("todos should be cleared, got %d items", len(s.todos))
	}
}

func TestUpdateTodosInvalidJSONClearsList(t *testing.T) {
	s := &Session{
		todos: []todo{{Content: "Old task", Status: "pending"}},
	}

	s.updateTodos("not json")
	if len(s.todos) != 0 {
		t.Errorf("invalid JSON should clear todos, got %d items", len(s.todos))
	}
}

func TestKnownHostSetMatch(t *testing.T) {
	set := &knownHostSet{Dests: []knownDest{
		{Host: "github.com"},
		{Host: "api.github.com"},
		{Host: "*.googleapis.com"},
		{Host: "**.cdn.example.com"},
	}}

	tests := []struct {
		host string
		want bool
	}{
		{"github.com", true},
		{"api.github.com", true},
		{"evil.github.com", false},
		{"storage.googleapis.com", true},
		{"googleapis.com", false},
		{"evil.com", false},

		// * matches one label only
		{"a.b.googleapis.com", false},

		// ** matches one or more labels
		{"us.cdn.example.com", true},
		{"us.east.cdn.example.com", true},
		{"cdn.example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := set.Match(tt.host); got != tt.want {
				t.Errorf("Match(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestKnownHostSetMatchRequest(t *testing.T) {
	set := &knownHostSet{Dests: []knownDest{
		{Host: "github.com"},
		{Host: "api.github.com", Path: "/repos/**", Methods: map[string]bool{"GET": true, "HEAD": true}},
		{Host: "uploads.example.com", Path: "/files/*"},
	}}

	tests := []struct {
		host, path, method string
		want               bool
	}{
		// Host-only match
		{"github.com", "/anything", "POST", true},

		// Path + method restricted
		{"api.github.com", "/repos/foo/bar", "GET", true},
		{"api.github.com", "/repos/foo/bar", "HEAD", true},
		{"api.github.com", "/repos/foo/bar", "DELETE", false},
		{"api.github.com", "/users/foo", "GET", false},

		// Path-only restriction (no method filter)
		{"uploads.example.com", "/files/image.png", "", true},
		{"uploads.example.com", "/files/a/b.png", "", false},
		{"uploads.example.com", "/other/path", "", false},
	}

	for _, tt := range tests {
		name := tt.host + tt.path + ":" + tt.method
		t.Run(name, func(t *testing.T) {
			if got := set.MatchRequest(tt.host, tt.path, tt.method); got != tt.want {
				t.Errorf("MatchRequest(%q, %q, %q) = %v, want %v", tt.host, tt.path, tt.method, got, tt.want)
			}
		})
	}
}

func TestKnownHostSetAdd(t *testing.T) {
	set := &knownHostSet{}
	set.Add("new.example.com")
	if !set.Match("new.example.com") {
		t.Error("added host should match")
	}
}

func TestLoadKnownHostsDefaults(t *testing.T) {
	set := loadKnownHosts()

	// Defaults require GET/HEAD method
	if !set.MatchRequest("github.com", "", "GET") {
		t.Error("defaults should include github.com GET")
	}
	if !set.MatchRequest("proxy.golang.org", "", "HEAD") {
		t.Error("defaults should include proxy.golang.org HEAD")
	}
	if set.MatchRequest("github.com", "", "POST") {
		t.Error("defaults should not allow github.com POST")
	}
	if set.MatchRequest("evil.com", "", "GET") {
		t.Error("defaults should not include evil.com")
	}

	// Match() without method should not match method-restricted entries
	if set.Match("github.com") {
		t.Error("Match() without method should not match method-restricted defaults")
	}
}

func TestParseConfigFile(t *testing.T) {
	content := `# slagent config
workspaces:
  nvidia.enterprise.slack.com:
    thinking-emoji: ":claude-thinking:"
  myteam.slack.com:
    thinking-emoji: ":claude:"
`
	tmp := t.TempDir()
	path := tmp + "/config.yaml"
	os.WriteFile(path, []byte(content), 0644)

	// Match first workspace
	cfg := parseConfigFile(path, "nvidia.enterprise.slack.com")
	if cfg.ThinkingEmoji != ":claude-thinking:" {
		t.Errorf("ThinkingEmoji = %q, want %q", cfg.ThinkingEmoji, ":claude-thinking:")
	}

	// Match second workspace
	cfg = parseConfigFile(path, "myteam.slack.com")
	if cfg.ThinkingEmoji != ":claude:" {
		t.Errorf("ThinkingEmoji = %q, want %q", cfg.ThinkingEmoji, ":claude:")
	}

	// No match
	cfg = parseConfigFile(path, "other.slack.com")
	if cfg.ThinkingEmoji != "" {
		t.Errorf("ThinkingEmoji = %q, want empty", cfg.ThinkingEmoji)
	}

	// Missing file
	cfg = parseConfigFile(tmp+"/nonexistent.yaml", "nvidia.enterprise.slack.com")
	if cfg.ThinkingEmoji != "" {
		t.Errorf("ThinkingEmoji = %q, want empty for missing file", cfg.ThinkingEmoji)
	}
}

func TestRenderQuestionSingleSelect(t *testing.T) {
	q := &askQuestion{
		text: "What kind of opponent?",
		options: []askOption{
			{Label: "Human vs AI", Description: "Play against computer"},
			{Label: "Human vs Human", Description: "Two players"},
			{Label: "Both modes", Description: "Let player choose"},
		},
		selected:  make(map[int]bool),
		answerIdx: -1,
	}
	s := &Session{}

	// No selection — should have thinking emoji, no 👉
	text := s.renderQuestion(q, "🐂", ":claude:", " <@U123>")
	if !strings.Contains(text, "🐂:claude: <@U123>") {
		t.Errorf("should have emoji+thinking+mention, got: %q", text)
	}
	if strings.Contains(text, "👉") {
		t.Errorf("should not have 👉 yet, got: %q", text)
	}

	// Select option 1 — should show 👉 on Human vs Human
	q.answerIdx = 1
	q.answered = true
	text = s.renderQuestion(q, "🐂", ":claude:", " <@U123>")
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "Human vs Human") && !strings.Contains(line, "👉") {
			t.Errorf("selected option should have 👉, got: %q", line)
		}
		if strings.Contains(line, "Human vs AI") && strings.Contains(line, "👉") {
			t.Errorf("unselected option should not have 👉, got: %q", line)
		}
	}
}

func TestRenderQuestionMultiSelect(t *testing.T) {
	q := &askQuestion{
		text: "Which features?",
		options: []askOption{
			{Label: "Auth"}, {Label: "Logging"}, {Label: "Cache"},
		},
		multiSelect: true,
		selected:    map[int]bool{0: true, 2: true},
		answerIdx:   -1,
	}
	s := &Session{}

	text := s.renderQuestion(q, "🐂", ":claude:", " <@U123>")
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "*Auth*") && !strings.Contains(line, "👉") {
			t.Errorf("Auth should have 👉, got: %q", line)
		}
		if strings.Contains(line, "*Logging*") && strings.Contains(line, "👉") {
			t.Errorf("Logging should not have 👉, got: %q", line)
		}
		if strings.Contains(line, "*Cache*") && !strings.Contains(line, "👉") {
			t.Errorf("Cache should have 👉, got: %q", line)
		}
	}
}

func TestRenderQuestionFinalRemovesThinkingEmoji(t *testing.T) {
	q := &askQuestion{
		text:      "Pick one",
		options:   []askOption{{Label: "A"}, {Label: "B"}},
		selected:  make(map[int]bool),
		answerIdx: 0,
	}
	s := &Session{}

	pending := s.renderQuestion(q, "🐂", ":claude:", " <@U123>")
	if !strings.Contains(pending, ":claude:") {
		t.Errorf("pending should have thinking emoji, got: %q", pending)
	}

	final := s.renderQuestionFinal(q, "🐂", " <@U123>", false)
	if strings.Contains(final, ":claude:") {
		t.Errorf("final should not have thinking emoji, got: %q", final)
	}
	if !strings.HasPrefix(final, "🐂 <@U123>") {
		t.Errorf("final should start with emoji+mention, got: %q", final)
	}
}

func TestRenderQuestionFinalCancelled(t *testing.T) {
	q := &askQuestion{
		text:      "Pick one",
		options:   []askOption{{Label: "A"}},
		selected:  make(map[int]bool),
		answerIdx: -1,
	}
	s := &Session{}

	text := s.renderQuestionFinal(q, "🐂", " <@U123>", true)
	if !strings.Contains(text, "❌") {
		t.Errorf("cancelled should have ❌, got: %q", text)
	}
	if strings.Contains(text, ":claude:") {
		t.Errorf("cancelled should not have thinking emoji, got: %q", text)
	}
}

func TestSingleSelectSwitchRendering(t *testing.T) {
	q := &askQuestion{
		text:      "Pick",
		options:   []askOption{{Label: "A"}, {Label: "B"}, {Label: "C"}},
		selected:  make(map[int]bool),
		answerIdx: -1,
	}
	s := &Session{}

	// Select A — 👉 on A only
	q.answerIdx = 0
	q.answered = true
	text := s.renderQuestion(q, "🐂", ":claude:", "")
	if strings.Count(text, "👉") != 1 {
		t.Errorf("should have exactly 1 marker, got: %q", text)
	}

	// Switch to C — 👉 moves from A to C
	q.answerIdx = 2
	text = s.renderQuestion(q, "🐂", ":claude:", "")
	if strings.Count(text, "👉") != 1 {
		t.Errorf("should have exactly 1 marker after switch, got: %q", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "*A*") && strings.Contains(line, "👉") {
			t.Errorf("A should not have 👉 after switch, got: %q", line)
		}
		if strings.Contains(line, "*C*") && !strings.Contains(line, "👉") {
			t.Errorf("C should have 👉 after switch, got: %q", line)
		}
	}
}

func TestMultiSelectToggleRendering(t *testing.T) {
	q := &askQuestion{
		text:        "Pick",
		options:     []askOption{{Label: "A"}, {Label: "B"}, {Label: "C"}},
		multiSelect: true,
		selected:    make(map[int]bool),
		answerIdx:   -1,
	}
	s := &Session{}

	// No selection — no 👉
	text := s.renderQuestion(q, "🐂", ":claude:", "")
	if strings.Count(text, "👉") != 0 {
		t.Errorf("no selection should have 0 markers, got: %q", text)
	}

	// Select A and C — 2 markers
	q.selected[0] = true
	q.selected[2] = true
	text = s.renderQuestion(q, "🐂", ":claude:", "")
	if strings.Count(text, "👉") != 2 {
		t.Errorf("two selections should have 2 markers, got: %q", text)
	}

	// Toggle A off — back to 1 marker on C
	q.selected[0] = false
	text = s.renderQuestion(q, "🐂", ":claude:", "")
	if strings.Count(text, "👉") != 1 {
		t.Errorf("one selection should have 1 marker, got: %q", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "*C*") && !strings.Contains(line, "👉") {
			t.Errorf("C should still have 👉, got: %q", line)
		}
		if strings.Contains(line, "*A*") && strings.Contains(line, "👉") {
			t.Errorf("A should not have 👉 after toggle off, got: %q", line)
		}
	}
}

