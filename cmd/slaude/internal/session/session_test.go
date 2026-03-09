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

	result := interactivePrompt("AskUserQuestion", string(input), "U123")
	if result != nil {
		t.Errorf("free-text AskUserQuestion should return nil, got: %+v", result)
	}
}

func TestInteractivePromptFreeTextNoOwnerReturnsNil(t *testing.T) {
	input, _ := json.Marshal(map[string]any{
		"question": "What do you mean?",
	})

	result := interactivePrompt("AskUserQuestion", string(input), "")
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

	result := interactivePrompt("AskUserQuestion", string(input), "U123")
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

	// Must contain @mention
	if !strings.Contains(result.text, "<@U123>") {
		t.Errorf("prompt should contain mention, got: %q", result.text)
	}
}

func TestInteractivePromptExitPlanMode(t *testing.T) {
	result := interactivePrompt("ExitPlanMode", "{}", "U123")
	if result == nil {
		t.Fatal("ExitPlanMode should return a prompt")
	}
	if len(result.reactions) != 2 {
		t.Errorf("reactions = %d, want 2", len(result.reactions))
	}
	if result.reactions[0] != "white_check_mark" || result.reactions[1] != "x" {
		t.Errorf("reactions = %v, want [white_check_mark x]", result.reactions)
	}
	if !strings.Contains(result.text, "<@U123>") {
		t.Errorf("prompt should contain mention, got: %q", result.text)
	}
}

func TestInteractivePromptEnterPlanMode(t *testing.T) {
	result := interactivePrompt("EnterPlanMode", "{}", "U123")
	if result == nil {
		t.Fatal("EnterPlanMode should return a prompt")
	}
	if len(result.reactions) != 2 {
		t.Errorf("reactions = %d, want 2", len(result.reactions))
	}
}

func TestInteractivePromptUnknownToolReturnsNil(t *testing.T) {
	result := interactivePrompt("Read", `{"file_path":"main.go"}`, "U123")
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

func TestParseClassification(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantLevel  string
		wantNet    bool
		wantNetDst string
	}{
		{
			name:      "green no network",
			line:      "GREEN|NONE|Reading source file within project",
			wantLevel: "green",
		},
		{
			name:       "yellow with network",
			line:       "YELLOW|NETWORK:registry.npmjs.org|Installing npm packages",
			wantLevel:  "yellow",
			wantNet:    true,
			wantNetDst: "registry.npmjs.org",
		},
		{
			name:       "red with unknown network",
			line:       "RED|NETWORK:evil.com|Exfiltrating data",
			wantLevel:  "red",
			wantNet:    true,
			wantNetDst: "evil.com",
		},
		{
			name:      "lowercase accepted",
			line:      "green|NONE|Safe read",
			wantLevel: "green",
		},
		{
			name:       "multiline takes first",
			line:       "GREEN|NETWORK:proxy.golang.org|Fetching module\nsome extra text",
			wantLevel:  "green",
			wantNet:    true,
			wantNetDst: "proxy.golang.org",
		},
		{
			name:       "unparseable defaults to red+network",
			line:       "garbage",
			wantLevel:  "red",
			wantNet:    true,
			wantNetDst: "unknown",
		},
		{
			name:       "network with empty destination",
			line:       "YELLOW|NETWORK:|Unknown destination",
			wantLevel:  "yellow",
			wantNet:    true,
			wantNetDst: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := parseClassification(tt.line)
			if c.Level != tt.wantLevel {
				t.Errorf("Level = %q, want %q", c.Level, tt.wantLevel)
			}
			if c.Network != tt.wantNet {
				t.Errorf("Network = %v, want %v", c.Network, tt.wantNet)
			}
			if c.NetworkDst != tt.wantNetDst {
				t.Errorf("NetworkDst = %q, want %q", c.NetworkDst, tt.wantNetDst)
			}
		})
	}
}

func TestLevelAllowed(t *testing.T) {
	tests := []struct {
		level     string
		threshold string
		want      bool
	}{
		{"green", "never", false},
		{"yellow", "never", false},
		{"red", "never", false},
		{"green", "green", true},
		{"yellow", "green", false},
		{"red", "green", false},
		{"green", "yellow", true},
		{"yellow", "yellow", true},
		{"red", "yellow", false},
	}

	for _, tt := range tests {
		name := tt.level + "/" + tt.threshold
		t.Run(name, func(t *testing.T) {
			got := levelAllowed(tt.level, tt.threshold)
			if got != tt.want {
				t.Errorf("levelAllowed(%q, %q) = %v, want %v", tt.level, tt.threshold, got, tt.want)
			}
		})
	}
}

func TestParseKnownHostsFile(t *testing.T) {
	content := `# Package managers
- host: github.com
- host: api.github.com
- host: "*.googleapis.com"
- host: '*.cdn.example.com'

# Comment
- host: pypi.org
- invalid line
- host:
`
	tmp := t.TempDir()
	path := tmp + "/known-hosts.yaml"
	os.WriteFile(path, []byte(content), 0644)

	hosts, err := parseKnownHostsFile(path)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"github.com", "api.github.com", "*.googleapis.com", "*.cdn.example.com", "pypi.org"}
	if len(hosts) != len(want) {
		t.Fatalf("got %d hosts, want %d: %v", len(hosts), len(want), hosts)
	}
	for i, h := range hosts {
		if h != want[i] {
			t.Errorf("hosts[%d] = %q, want %q", i, h, want[i])
		}
	}
}

func TestKnownHostSetMatch(t *testing.T) {
	set := &knownHostSet{exact: map[string]bool{
		"github.com":     true,
		"api.github.com": true,
	}, patterns: []string{"*.googleapis.com"}}

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
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := set.match(tt.host); got != tt.want {
				t.Errorf("match(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestKnownHostSetAdd(t *testing.T) {
	set := &knownHostSet{exact: make(map[string]bool)}
	set.add("new.example.com")
	if !set.match("new.example.com") {
		t.Error("added host should match")
	}
}

func TestLoadKnownHostsDefaults(t *testing.T) {
	// When no file exists, loadKnownHosts returns built-in defaults
	set := loadKnownHosts()
	if !set.match("github.com") {
		t.Error("defaults should include github.com")
	}
	if !set.match("proxy.golang.org") {
		t.Error("defaults should include proxy.golang.org")
	}
	if set.match("evil.com") {
		t.Error("defaults should not include evil.com")
	}
}

func TestLevelEmoji(t *testing.T) {
	if e := levelEmoji("green"); e != "🟢" {
		t.Errorf("green = %q", e)
	}
	if e := levelEmoji("yellow"); e != "🟡" {
		t.Errorf("yellow = %q", e)
	}
	if e := levelEmoji("red"); e != "🔴" {
		t.Errorf("red = %q", e)
	}
	if e := levelEmoji("unknown"); e != "🔴" {
		t.Errorf("unknown = %q", e)
	}
}
