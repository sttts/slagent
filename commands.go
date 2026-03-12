package slagent

import (
	"fmt"
	"strings"
)

// handleCommand processes /open, /lock, /close, /observe, and /help commands.
// Returns (handled, feedback): handled is true for known commands,
// feedback is a status message to post in the thread.
func (t *Thread) handleCommand(userID, cmd string) (bool, string) {
	cmd = strings.TrimSpace(cmd)
	if strings.HasPrefix(cmd, "/help") {
		return true, t.helpText()
	}

	handled, feedback := t.HandleCommand(userID, cmd)
	if !handled {
		return false, ""
	}

	// Update thread parent to reflect new access state (only if we created the thread)
	if !t.joined {
		t.updateTitle()
	}
	return true, feedback
}

// parseInstancePrefix checks if text starts with a :shortcode: prefix targeting an instance.
// Accepts any combination of trailing colons and spaces after the shortcode:
//   - ":fox_face:: msg"  — double colon
//   - ":fox_face: msg"   — single colon (space)
//   - ":fox_face: : msg" — single colon, space, colon
//   - ":fox_face:msg"    — no space
//
// Returns the shortcode (instance ID), remaining text, and whether a prefix was found.
func parseInstancePrefix(text string) (instanceID, rest string, targeted bool) {
	if !strings.HasPrefix(text, ":") {
		return "", text, false
	}

	// Find the closing colon of the shortcode
	end := strings.Index(text[1:], ":")
	if end < 0 {
		return "", text, false
	}
	shortcode := text[1 : end+1]

	// Verify it's a known identity emoji
	if _, ok := identityEmojis[shortcode]; !ok {
		return "", text, false
	}

	// Consume any trailing combination of spaces and colons after ":shortcode:"
	rest = text[end+2:]
	rest = strings.TrimLeft(rest, " :")
	return shortcode, rest, true
}

// ParseMessage extracts the target instance and cleaned text from a Slack message.
// Strips leading @mentions, then checks for :shortcode: prefix.
func ParseMessage(text string) (instanceID, cleaned string, targeted bool) {
	return parseMessage(text)
}

func parseMessage(text string) (instanceID, cleaned string, targeted bool) {
	s := text

	// Strip leading @mentions (Slack format: <@U123>)
	for strings.HasPrefix(s, "<@") {
		if idx := strings.Index(s, ">"); idx >= 0 {
			s = strings.TrimLeft(s[idx+1:], " ")
		} else {
			break
		}
	}
	return parseInstancePrefix(s)
}

// helpText returns the help message for the thread.
func (t *Thread) helpText() string {
	emoji := t.emoji
	id := t.instanceID
	return fmt.Sprintf(""+
		"*slaude — thread commands*\n"+
		"\n"+
		"*Targeting* (type `:%s:` in Slack, renders as %s)\n"+
		"  `:%s: message` — address this instance\n"+
		"  `:%s: /command` — send command exclusively to this instance\n"+
		"  Messages without prefix are broadcast to all instances.\n"+
		"\n"+
		"*Access control* (owner only)\n"+
		"  `:%s: /open` — open thread for everyone\n"+
		"  `:%s: /open @user` — allow specific users\n"+
		"  `:%s: /lock` — lock to owner only\n"+
		"  `:%s: /lock @user` — ban specific users\n"+
		"  `:%s: /close` — alias for /lock\n"+
		"  `:%s: /observe` — toggle observe mode (read all, respond to owner)\n"+
		"\n"+
		"*Session* (owner only)\n"+
		"  `:%s: /sandbox` — toggle sandbox on/off (restarts session)\n"+
		"\n"+
		"*Control*\n"+
		"  `stop` — interrupt current turn (all instances, anyone)\n"+
		"  `:%s: stop` — interrupt this instance only\n"+
		"  `quit` — terminate session (owner only)\n"+
		"  `:%s: quit` — terminate this instance only\n"+
		"  `:%s: /help` — show this help\n"+
		"  Other `/commands` are forwarded to Claude.",
		id, emoji,
		id, id, id, id, id, id, id, id, id, id, id, id)
}
