package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sttts/slagent/cmd/slaude/internal/perms"
)

// questionTimeout is how long to wait for AskUserQuestion answers.
const questionTimeout = 10 * time.Minute

// askQuestion holds state for a single question being polled via Slack reactions.
type askQuestion struct {
	text        string       // question text
	options     []askOption
	multiSelect bool
	msgTS       string       // Slack message timestamp
	reactions   []string     // reaction names (number reactions + optional submit + cancel)
	selected    map[int]bool // selected option indices (for multi-select toggling)
	answered    bool         // single-select: has selection; multi-select: submitted
	answerIdx   int          // single-select: selected index (-1 = none)
}

// askOption holds a single option in a question.
type askOption struct {
	Label       string
	Description string
}

// pollQuestionResult is the outcome of polling a single question's reactions.
type pollQuestionResult int

const (
	pollNoChange  pollQuestionResult = iota
	pollChanged                      // selection changed, not yet answered
	pollAnswered                     // question is now answered
	pollCancelled                    // ❌ was clicked
)

// handleAskUserQuestion processes AskUserQuestion by posting per-question Slack
// messages with reactions and polling for answers.
func (s *Session) handleAskUserQuestion(req *perms.PermissionRequest) *perms.PermissionResponse {
	var input map[string]interface{}
	if err := json.Unmarshal(req.Input, &input); err != nil {
		return &perms.PermissionResponse{Behavior: "allow"}
	}

	// Parse questions array
	rawQuestions, ok := input["questions"]
	if !ok {
		// No questions format — auto-approve (free-text handled in readTurn)
		return &perms.PermissionResponse{Behavior: "allow"}
	}
	questionsArr, ok := rawQuestions.([]interface{})
	if !ok || len(questionsArr) == 0 {
		return &perms.PermissionResponse{Behavior: "allow"}
	}

	emoji := ""
	thinkingEmoji := ":claude:"
	ownerMention := ""
	if s.thread != nil {
		emoji = s.thread.Emoji()
		thinkingEmoji = s.thread.ThinkingEmoji()
		if ownerID := s.thread.OwnerID(); ownerID != "" {
			ownerMention = fmt.Sprintf(" <@%s>", ownerID)
		}
	}

	// Build question structs and post messages
	var questions []*askQuestion
	for _, qRaw := range questionsArr {
		qMap, ok := qRaw.(map[string]interface{})
		if !ok {
			continue
		}
		qText, _ := qMap["question"].(string)
		multiSelect, _ := qMap["multiSelect"].(bool)
		optsRaw, _ := qMap["options"].([]interface{})
		if len(optsRaw) == 0 {
			continue
		}

		q := &askQuestion{
			text:        qText,
			multiSelect: multiSelect,
			selected:    make(map[int]bool),
			answerIdx:   -1,
		}

		for _, optRaw := range optsRaw {
			opt, ok := optRaw.(map[string]interface{})
			if !ok {
				continue
			}
			label, _ := opt["label"].(string)
			desc, _ := opt["description"].(string)
			q.options = append(q.options, askOption{Label: label, Description: desc})
		}

		// Build reactions: number reactions + optional submit + cancel
		for i := range q.options {
			if i >= len(numberReactions) {
				break
			}
			q.reactions = append(q.reactions, numberReactions[i])
		}
		if multiSelect {
			q.reactions = append(q.reactions, "white_check_mark")
		}
		q.reactions = append(q.reactions, "x")

		// Post question message
		text := s.renderQuestion(q, emoji, thinkingEmoji, ownerMention)
		msgTS, err := s.thread.PostPrompt(text, q.reactions)
		if err != nil {
			s.ui.Error(fmt.Sprintf("failed to post question: %v", err))
			return &perms.PermissionResponse{Behavior: "allow"}
		}
		q.msgTS = msgTS
		questions = append(questions, q)
	}

	if len(questions) == 0 {
		return &perms.PermissionResponse{Behavior: "allow"}
	}

	// Show in terminal
	s.ui.ToolActivity("  ❓ AskUserQuestion — awaiting answers in Slack...")

	// Poll for answers
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	deadline := time.Now().Add(questionTimeout)

	for time.Now().Before(deadline) {
		select {
		case <-s.ctx.Done():
			for _, q := range questions {
				s.finalizeQuestion(q, emoji, ownerMention, true)
			}
			return &perms.PermissionResponse{Behavior: "deny", Message: "session cancelled"}
		case <-ticker.C:
		}

		cancelled := false
		changed := false
		for _, q := range questions {
			if q.answered {
				continue
			}
			switch s.pollQuestion(q) {
			case pollCancelled:
				cancelled = true
			case pollAnswered:
				changed = true
			case pollChanged:
				changed = true
			}
			if cancelled {
				break
			}
		}

		if cancelled {
			for _, q := range questions {
				s.finalizeQuestion(q, emoji, ownerMention, true)
			}
			s.ui.ToolActivity("  ❌ AskUserQuestion — cancelled")
			return &perms.PermissionResponse{Behavior: "deny", Message: "cancelled by user"}
		}

		if !changed {
			continue
		}

		// Check if all answered
		allDone := true
		for _, q := range questions {
			if !q.answered {
				allDone = false
				break
			}
		}

		if allDone {
			// Last click completed all — finalize all, remove all reactions
			for _, q := range questions {
				s.finalizeQuestion(q, emoji, ownerMention, false)
			}
			break
		}

		// Not all done — update each question's display
		for _, q := range questions {
			if q.answered {
				// Answered: remove ✅/❌, keep only number reactions in order
				s.thread.RemoveAllReactions(q.msgTS, q.reactions)
				for _, r := range numberOnlyReactions(q) {
					s.thread.AddReaction(q.msgTS, r)
				}
				text := s.renderQuestionFinal(q, emoji, ownerMention, false)
				s.thread.UpdateMessage(q.msgTS, text)
			} else {
				// Not answered: rebuild all reactions in order, update text
				s.thread.RemoveAllReactions(q.msgTS, q.reactions)
				for _, r := range q.reactions {
					s.thread.AddReaction(q.msgTS, r)
				}
				text := s.renderQuestion(q, emoji, thinkingEmoji, ownerMention)
				s.thread.UpdateMessage(q.msgTS, text)
			}
		}
	}

	// Check for timeout
	allDone := true
	for _, q := range questions {
		if !q.answered {
			allDone = false
			break
		}
	}
	if !allDone {
		for _, q := range questions {
			s.finalizeQuestion(q, emoji, ownerMention, true)
		}
		s.ui.ToolActivity("  ⏰ AskUserQuestion — timed out")
		return &perms.PermissionResponse{Behavior: "deny", Message: "question timed out"}
	}

	// Build answers map
	answers := make(map[string]string)
	for _, q := range questions {
		if q.multiSelect {
			var labels []string
			for i, opt := range q.options {
				if q.selected[i] {
					labels = append(labels, opt.Label)
				}
			}
			answers[q.text] = strings.Join(labels, ", ")
		} else if q.answerIdx >= 0 && q.answerIdx < len(q.options) {
			answers[q.text] = q.options[q.answerIdx].Label
		}
	}

	// Build updatedInput with answers
	input["answers"] = answers
	updatedInput, _ := json.Marshal(input)

	s.ui.ToolActivity("  ✅ AskUserQuestion — answered")
	return &perms.PermissionResponse{Behavior: "allow", UpdatedInput: updatedInput}
}

// renderQuestion builds the Slack mrkdwn text for a question (with thinking emoji).
func (s *Session) renderQuestion(q *askQuestion, emoji, thinkingEmoji, ownerMention string) string {
	var lines []string
	lines = append(lines, fmt.Sprintf("%s%s%s", emoji, thinkingEmoji, ownerMention))
	lines = append(lines, fmt.Sprintf("> *%s*", q.text))
	for i, opt := range q.options {
		if i >= len(numberReactions) {
			break
		}
		lines = append(lines, renderOptionLine(i, opt, q.answerIdx == i && !q.multiSelect, q.multiSelect && q.selected[i]))
	}
	return strings.Join(lines, "\n")
}

// renderQuestionFinal builds the Slack mrkdwn text without thinking emoji.
func (s *Session) renderQuestionFinal(q *askQuestion, emoji, ownerMention string, cancelled bool) string {
	var lines []string
	if cancelled {
		lines = append(lines, fmt.Sprintf("%s%s ❌", emoji, ownerMention))
	} else {
		lines = append(lines, fmt.Sprintf("%s%s", emoji, ownerMention))
	}
	lines = append(lines, fmt.Sprintf("> *%s*", q.text))
	for i, opt := range q.options {
		if i >= len(numberReactions) {
			break
		}
		lines = append(lines, renderOptionLine(i, opt, q.answerIdx == i && !q.multiSelect, q.multiSelect && q.selected[i]))
	}
	return strings.Join(lines, "\n")
}

// renderOptionLine formats a single option line with optional 👉 marker.
func renderOptionLine(idx int, opt askOption, singleSelected, multiSelected bool) string {
	marker := " "
	if singleSelected || multiSelected {
		marker = "👉"
	}
	if opt.Description != "" {
		return fmt.Sprintf("> %s %s *%s* — %s", numberEmoji(idx), marker, opt.Label, opt.Description)
	}
	return fmt.Sprintf("> %s %s *%s*", numberEmoji(idx), marker, opt.Label)
}

// pollQuestion checks reactions for one question and updates its state.
// Does NOT modify reactions or message text — the caller handles that.
func (s *Session) pollQuestion(q *askQuestion) pollQuestionResult {
	item, err := s.thread.GetReactions(q.msgTS)
	if err != nil {
		return pollNoChange
	}

	ownerID := s.thread.OwnerID()

	// Build map of which reactions the owner is still present on
	ownerPresent := make(map[string]bool)
	for _, r := range item {
		for _, u := range r.Users {
			if u == ownerID {
				ownerPresent[r.Name] = true
				break
			}
		}
	}

	// Check for cancel (owner or non-owner)
	for _, r := range item {
		if r.Name != "x" {
			continue
		}
		if !ownerPresent["x"] {
			return pollCancelled
		}
		for _, u := range r.Users {
			if u != ownerID {
				return pollCancelled
			}
		}
	}

	// Check number reactions
	for i := range q.options {
		if i >= len(numberReactions) {
			break
		}
		if !ownerPresent[numberReactions[i]] {
			if q.multiSelect {
				q.selected[i] = !q.selected[i]
				return pollChanged
			}
			q.answerIdx = i
			q.answered = true
			return pollAnswered
		}
	}

	// Check submit for multi-select
	if q.multiSelect && !ownerPresent["white_check_mark"] {
		hasSelection := false
		for _, sel := range q.selected {
			if sel {
				hasSelection = true
				break
			}
		}
		if hasSelection {
			q.answered = true
			return pollAnswered
		}
		return pollChanged
	}

	return pollNoChange
}

// numberOnlyReactions returns only the number reactions (no submit/cancel).
func numberOnlyReactions(q *askQuestion) []string {
	var nums []string
	for i := range q.options {
		if i >= len(numberReactions) {
			break
		}
		nums = append(nums, numberReactions[i])
	}
	return nums
}

// finalizeQuestion updates the message text (remove thinking emoji) and removes all reactions.
func (s *Session) finalizeQuestion(q *askQuestion, emoji, ownerMention string, cancelled bool) {
	if q.msgTS == "" {
		return
	}
	text := s.renderQuestionFinal(q, emoji, ownerMention, cancelled)
	s.thread.UpdateMessage(q.msgTS, text)
	s.thread.RemoveAllReactions(q.msgTS, q.reactions)
}
