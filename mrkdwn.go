package slagent

import (
	"regexp"
	"strings"
)

var (
	reHeading       = regexp.MustCompile(`(?m)^(#{1,6})\s+(.+)$`)
	reBold          = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reLink          = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reListDash      = regexp.MustCompile(`(?m)^(\s*)[-*]\s+`)
	reStrikethrough = regexp.MustCompile(`~~(.+?)~~`)
)

// MarkdownToMrkdwn converts Markdown to Slack mrkdwn format.
func MarkdownToMrkdwn(text string) string {
	// Headings: # Foo → *Foo*
	text = reHeading.ReplaceAllString(text, "*$2*")

	// Bold: **foo** → *foo*
	text = reBold.ReplaceAllString(text, "*$1*")

	// Links: [text](url) → <url|text>
	text = reLink.ReplaceAllString(text, "<$2|$1>")

	// Unordered lists: - item → • item
	text = reListDash.ReplaceAllString(text, "${1}• ")

	// Strikethrough: ~~foo~~ → ~foo~
	text = reStrikethrough.ReplaceAllString(text, "~$1~")

	return text
}

// splitAtLines splits text into chunks of at most maxLen bytes at line boundaries.
func splitAtLines(text string, maxLen int) []string {
	var chunks []string
	for len(text) > 0 {
		if len(text) <= maxLen {
			chunks = append(chunks, text)
			break
		}

		// Find last newline within maxLen
		cut := strings.LastIndex(text[:maxLen], "\n")
		if cut <= 0 {
			cut = maxLen
		} else {
			cut++ // include the newline
		}
		chunks = append(chunks, text[:cut])
		text = text[cut:]
	}
	return chunks
}
