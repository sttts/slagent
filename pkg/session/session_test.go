package session

import (
	"encoding/json"
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
