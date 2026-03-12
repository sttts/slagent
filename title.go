package slagent

import (
	"fmt"
	"strings"

	slackapi "github.com/slack-go/slack"

	"github.com/sttts/slagent/access"
)

// formatTitle builds the thread parent label reflecting access state.
//
//	Open for all:                  ":instanceID:🧵 Topic"
//	Selective (allowed users):     ":instanceID:🧵 <@U1> <@U2> Topic"
//	Selective + observe:           ":instanceID:👀🧵 <@U1> <@U2> Topic"
//	Locked (owner only):           ":instanceID:🔒🧵 Topic"
//	Locked + observe (= observe):  ":instanceID:👀🧵 Topic"
//	With bans (appended):          "... (🔒 <@U3>)"
func (t *Thread) formatTitle() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	title := t.topic
	if title == "" {
		title = "Agent session"
	}

	st := t.Controller.State()

	// Build access marker: 👀 replaces 🔒 when observe is on
	var label string
	if st.OpenAccess {
		label = fmt.Sprintf(":%s:🧵 %s", t.instanceID, title)
	} else if st.Observe {
		// Observe implies closed; 👀 replaces 🔒
		if len(st.AllowedUsers) > 0 {
			var mentions []string
			for _, u := range st.AllowedUsers {
				mentions = append(mentions, fmt.Sprintf("<@%s>", u))
			}
			label = fmt.Sprintf(":%s:👀🧵 %s %s", t.instanceID, strings.Join(mentions, " "), title)
		} else {
			label = fmt.Sprintf(":%s:👀🧵 %s", t.instanceID, title)
		}
	} else if len(st.AllowedUsers) > 0 {
		// Selective without observe
		var mentions []string
		for _, u := range st.AllowedUsers {
			mentions = append(mentions, fmt.Sprintf("<@%s>", u))
		}
		label = fmt.Sprintf(":%s:🧵 %s %s", t.instanceID, strings.Join(mentions, " "), title)
	} else {
		// Locked (no allowed users, no observe)
		label = fmt.Sprintf(":%s:🔒🧵 %s", t.instanceID, title)
	}

	// Append mode suffix (e.g. " — 📋 planning")
	if t.modeSuffix != "" {
		label += t.modeSuffix
	}

	// Append ban list
	if len(st.BannedUsers) > 0 {
		var mentions []string
		for _, u := range st.BannedUsers {
			mentions = append(mentions, fmt.Sprintf("<@%s>", u))
		}
		label += fmt.Sprintf(" (🔒 %s)", strings.Join(mentions, " "))
	}

	// t.title is for terminal display — convert shortcode to Unicode
	t.title = ShortcodesToUnicode(label)
	return label
}

// Title returns the full thread title with shortcodes converted to Unicode.
func (t *Thread) Title() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.title
}

// Topic returns the parsed topic text (without emojis, mentions, access markers).
func (t *Thread) Topic() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.topic
}

// refreshTitle re-fetches the thread parent message and re-parses the title.
// Used by joined instances to pick up access changes made by the original instance.
func (t *Thread) refreshTitle() {
	t.mu.Lock()
	threadTS := t.threadTS
	t.mu.Unlock()

	if threadTS == "" {
		return
	}
	params := &slackapi.GetConversationRepliesParameters{
		ChannelID: t.channel,
		Timestamp: threadTS,
		Limit:     1,
	}
	msgs, _, _, err := t.client.GetConversationReplies(params)
	if err == nil && len(msgs) > 0 {
		t.parseTitle(msgs[0].Text)
	}
}

// parseTitle recovers access state from a thread parent message.
// Handles both Unicode (🔒🧵) and Slack shortcode (:lock::thread:) formats.
func (t *Thread) parseTitle(text string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Normalize shortcodes to Unicode for consistent parsing
	text = ShortcodesToUnicode(text)
	t.title = text

	// Detect 👀 observe marker (👀🧵 = observe mode, replaces 🔒)
	observe := strings.Contains(text, "👀🧵")
	locked := strings.Contains(text, "🔒🧵")

	// Extract content after 🧵 (with optional space)
	if idx := strings.Index(text, "🧵 "); idx >= 0 {
		t.topic = text[idx+len("🧵 "):]
	} else if idx := strings.Index(text, "🧵"); idx >= 0 {
		t.topic = text[idx+len("🧵"):]
	}

	// Parse "(🔒 <@U3>)" — banned users (strip from title)
	bannedUsers := make(map[string]bool)
	if idx := strings.Index(t.topic, " (🔒 "); idx >= 0 {
		end := strings.Index(t.topic[idx:], ")")
		if end >= 0 {
			extractMentions(t.topic[idx:idx+end+1], bannedUsers)
			t.topic = strings.TrimSpace(t.topic[:idx] + t.topic[idx+end+1:])
		}
	}

	// Strip mode suffix (e.g. " — 📋 planning") — not part of the topic.
	if idx := strings.LastIndex(t.topic, " — 📋"); idx >= 0 {
		t.topic = t.topic[:idx]
	}

	// Build access state
	var st access.State
	st.Observe = observe
	for u := range bannedUsers {
		st.BannedUsers = append(st.BannedUsers, u)
	}

	if locked {
		st.OpenAccess = false
		t.Controller.Apply(st)
		return
	}

	// Not locked: parse leading <@...> mentions as allowed users
	allowedUsers := make(map[string]bool)
	for strings.HasPrefix(t.topic, "<@") {
		end := strings.Index(t.topic, ">")
		if end < 0 {
			break
		}
		uid := t.topic[2:end]

		// Strip display name suffix: <@U12345|sttts> → U12345
		if idx := strings.Index(uid, "|"); idx >= 0 {
			uid = uid[:idx]
		}
		allowedUsers[uid] = true
		t.topic = strings.TrimLeft(t.topic[end+1:], " ")
	}
	for u := range allowedUsers {
		st.AllowedUsers = append(st.AllowedUsers, u)
	}

	// 👀 means not open (observe replaces 🔒); otherwise open if no allowed users
	if observe {
		st.OpenAccess = false
	} else {
		st.OpenAccess = len(allowedUsers) == 0
	}
	t.Controller.Apply(st)
}

// extractMentions parses <@U...> mentions from a string into the target map.
func extractMentions(s string, target map[string]bool) {
	rest := s
	for {
		start := strings.Index(rest, "<@")
		if start < 0 {
			break
		}
		end := strings.Index(rest[start:], ">")
		if end < 0 {
			break
		}
		uid := rest[start+2 : start+end]

		// Strip display name suffix: <@U12345|sttts> → U12345
		if idx := strings.Index(uid, "|"); idx >= 0 {
			uid = uid[:idx]
		}
		target[uid] = true
		rest = rest[start+end+1:]
	}
}

// updateTitle updates the thread parent message to reflect current access state.
func (t *Thread) updateTitle() {
	t.mu.Lock()
	threadTS := t.threadTS
	t.mu.Unlock()

	if threadTS == "" {
		return
	}

	label := t.formatTitle()
	t.logSlack("updateMessage(title)", label)
	t.client.UpdateMessage(
		t.channel,
		threadTS,
		slackapi.MsgOptionBlocks(t.slagentSection(label)),
		slackapi.MsgOptionText(label, false),
	)
}

// SetModeSuffix sets a suffix appended to the thread title (e.g. " — 📋 planning")
// and updates the thread parent message. Pass "" to clear.
func (t *Thread) SetModeSuffix(suffix string) {
	t.mu.Lock()
	old := t.modeSuffix
	t.modeSuffix = suffix
	t.mu.Unlock()

	if suffix != old {
		t.updateTitle()
	}
}
