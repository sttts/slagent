package slagent

import (
	"strings"
	"testing"
)

func TestMarkdownToMrkdwn(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"heading", "# Title", "*Title*"},
		{"h3", "### Sub", "*Sub*"},
		{"bold", "**bold text**", "*bold text*"},
		{"link", "[click](https://example.com)", "<https://example.com|click>"},
		{"list dash", "- item one\n- item two", "• item one\n• item two"},
		{"list star", "* item", "• item"},
		{"strikethrough", "~~removed~~", "~removed~"},
		{"indented list", "  - nested", "  • nested"},
		{"combined", "# Hello\n\n**world** and ~~old~~", "*Hello*\n\n*world* and ~old~"},
		{"no change", "plain text", "plain text"},
		{"code block preserved", "```\ncode\n```", "```\ncode\n```"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MarkdownToMrkdwn(tt.in)
			if got != tt.want {
				t.Errorf("MarkdownToMrkdwn(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSplitAtLines(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		maxLen int
		want   int // expected number of chunks
	}{
		{"short", "hello", 100, 1},
		{"exact", "hello", 5, 1},
		{"split at newline", "aaa\nbbb\nccc", 7, 2},
		{"no newline forces hard cut", strings.Repeat("x", 20), 10, 2},
		{"empty", "", 10, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := splitAtLines(tt.text, tt.maxLen)
			if len(chunks) != tt.want {
				t.Errorf("splitAtLines(%q, %d) = %d chunks, want %d", tt.text, tt.maxLen, len(chunks), tt.want)
			}

			// Verify reassembly
			joined := strings.Join(chunks, "")
			if joined != tt.text {
				t.Errorf("chunks don't reassemble: got %q, want %q", joined, tt.text)
			}

			// Verify each chunk fits
			for i, c := range chunks {
				if len(c) > tt.maxLen {
					t.Errorf("chunk %d length %d > maxLen %d", i, len(c), tt.maxLen)
				}
			}
		})
	}
}
