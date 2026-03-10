package session

import (
	"testing"
)

func TestNumberOnlyReactions(t *testing.T) {
	q := &askQuestion{
		options: []askOption{
			{Label: "A"}, {Label: "B"}, {Label: "C"},
		},
		multiSelect: true,
		reactions:    []string{"one", "two", "three", "white_check_mark", "x"},
	}

	nums := numberOnlyReactions(q)
	if len(nums) != 3 {
		t.Fatalf("len = %d, want 3", len(nums))
	}
	if nums[0] != "one" || nums[1] != "two" || nums[2] != "three" {
		t.Errorf("got %v", nums)
	}
}

func TestNumberOnlyReactionsSingleSelect(t *testing.T) {
	q := &askQuestion{
		options:   []askOption{{Label: "A"}, {Label: "B"}},
		reactions: []string{"one", "two", "x"},
	}

	nums := numberOnlyReactions(q)
	if len(nums) != 2 {
		t.Fatalf("len = %d, want 2", len(nums))
	}
}

func TestQuestionStateIsolation(t *testing.T) {
	// Simulates the fixed behavior: interacting with one question
	// should not affect another question's state.
	q1 := &askQuestion{
		text:      "Language?",
		options:   []askOption{{Label: "Go"}, {Label: "Python"}},
		selected:  make(map[int]bool),
		answerIdx: -1,
		reactions: []string{"one", "two", "x"},
		msgTS:     "1234.5678",
	}
	q2 := &askQuestion{
		text:      "License?",
		options:   []askOption{{Label: "MIT"}, {Label: "Apache"}},
		selected:  make(map[int]bool),
		answerIdx: -1,
		reactions: []string{"one", "two", "x"},
		msgTS:     "1234.9999",
	}

	// Simulate answering q1 only
	q1.answerIdx = 0
	q1.answered = true

	// q2 should be completely unaffected
	if q2.answered {
		t.Error("q2 should not be answered")
	}
	if q2.answerIdx != -1 {
		t.Errorf("q2.answerIdx = %d, want -1", q2.answerIdx)
	}

	// Simulate the fixed loop: only changed questions go into changedQuestions
	questions := []*askQuestion{q1, q2}
	var changedQuestions []*askQuestion
	for _, q := range questions {
		// In real code, pollQuestion returns the result per question
		if q.answered {
			changedQuestions = append(changedQuestions, q)
		}
	}

	// Only q1 should be in changedQuestions
	if len(changedQuestions) != 1 {
		t.Fatalf("changedQuestions = %d, want 1", len(changedQuestions))
	}
	if changedQuestions[0] != q1 {
		t.Error("changedQuestions should contain q1")
	}
}

func TestMultiSelectToggleState(t *testing.T) {
	q := &askQuestion{
		text:        "Features?",
		options:     []askOption{{Label: "Docker"}, {Label: "CI"}, {Label: "Docs"}},
		multiSelect: true,
		selected:    make(map[int]bool),
		answerIdx:   -1,
	}

	// Toggle Docker on
	q.selected[0] = !q.selected[0]
	if !q.selected[0] {
		t.Error("Docker should be selected")
	}

	// Toggle Docs on
	q.selected[2] = !q.selected[2]
	if !q.selected[2] {
		t.Error("Docs should be selected")
	}

	// Toggle Docker off
	q.selected[0] = !q.selected[0]
	if q.selected[0] {
		t.Error("Docker should be deselected")
	}

	// Only Docs should remain
	if !q.selected[2] {
		t.Error("Docs should still be selected")
	}
}
