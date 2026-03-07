package slagent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"

	slackapi "github.com/slack-go/slack"
)

// mockSlack is a fake Slack API server for testing.
type mockSlack struct {
	server *httptest.Server

	mu       sync.Mutex
	messages []mockMessage          // posted messages
	tsSeq    int                    // monotonic timestamp counter
	streams  map[string]*mockStream // streamID → stream
}

type mockMessage struct {
	Channel  string
	ThreadTS string
	Text     string
	Blocks   json.RawMessage
	IsUpdate bool
	Deleted  bool
	TS       string
	User     string
	BotID    string
}

type mockStream struct {
	channel  string
	threadTS string
	chunks   []json.RawMessage
	stopped  bool
	msgTS    string
}

func newMockSlack() *mockSlack {
	m := &mockSlack{
		streams: make(map[string]*mockStream),
	}
	mux := http.NewServeMux()

	// Standard Slack API endpoints
	mux.HandleFunc("/api/chat.postMessage", m.handlePostMessage)
	mux.HandleFunc("/api/chat.update", m.handleUpdateMessage)
	mux.HandleFunc("/api/chat.delete", m.handleDeleteMessage)
	mux.HandleFunc("/api/conversations.replies", m.handleConversationReplies)
	mux.HandleFunc("/api/chat.getPermalink", m.handleGetPermalink)
	mux.HandleFunc("/api/auth.test", m.handleAuthTest)
	mux.HandleFunc("/api/users.info", m.handleUsersInfo)

	// Native streaming endpoints
	mux.HandleFunc("/api/chat.startStream", m.handleStartStream)
	mux.HandleFunc("/api/chat.appendStream", m.handleAppendStream)
	mux.HandleFunc("/api/chat.stopStream", m.handleStopStream)

	m.server = httptest.NewServer(mux)
	return m
}

func (m *mockSlack) close() {
	m.server.Close()
}

// client returns a slack-go client pointing at the mock server.
func (m *mockSlack) client() *slackapi.Client {
	return slackapi.New("xoxc-test-token", slackapi.OptionAPIURL(m.server.URL+"/api/"))
}

// botClient returns a slack-go client with a bot token.
func (m *mockSlack) botClient() *slackapi.Client {
	return slackapi.New("xoxb-test-token", slackapi.OptionAPIURL(m.server.URL+"/api/"))
}

// apiURL returns the base API URL for native turn testing.
func (m *mockSlack) apiURL() string {
	return m.server.URL + "/api/"
}

func (m *mockSlack) nextTS() string {
	m.tsSeq++
	return fmt.Sprintf("1700000%03d.000000", m.tsSeq)
}

func (m *mockSlack) postedMessages() []mockMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]mockMessage, len(m.messages))
	copy(result, m.messages)
	return result
}

// activeMessages returns non-deleted messages.
func (m *mockSlack) activeMessages() []mockMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []mockMessage
	for _, msg := range m.messages {
		if !msg.Deleted {
			result = append(result, msg)
		}
	}
	return result
}

func (m *mockSlack) streamChunks(streamID string) []json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.streams[streamID]
	if !ok {
		return nil
	}
	result := make([]json.RawMessage, len(s.chunks))
	copy(result, s.chunks)
	return result
}

func (m *mockSlack) respond(w http.ResponseWriter, data map[string]any) {
	data["ok"] = true
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (m *mockSlack) handlePostMessage(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	channel := r.FormValue("channel")
	text := r.FormValue("text")
	threadTS := r.FormValue("thread_ts")

	ts := m.nextTS()

	m.mu.Lock()
	m.messages = append(m.messages, mockMessage{
		Channel:  channel,
		ThreadTS: threadTS,
		Text:     text,
		Blocks:   json.RawMessage(r.FormValue("blocks")),
		TS:       ts,
	})
	m.mu.Unlock()

	m.respond(w, map[string]any{
		"channel": channel,
		"ts":      ts,
	})
}

func (m *mockSlack) handleUpdateMessage(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	channel := r.FormValue("channel")
	ts := r.FormValue("ts")
	text := r.FormValue("text")

	m.mu.Lock()
	for i := range m.messages {
		if m.messages[i].TS == ts && m.messages[i].Channel == channel {
			m.messages[i].Text = text
			m.messages[i].Blocks = json.RawMessage(r.FormValue("blocks"))
			m.messages[i].IsUpdate = true
			break
		}
	}
	m.mu.Unlock()

	m.respond(w, map[string]any{
		"channel": channel,
		"ts":      ts,
	})
}

func (m *mockSlack) handleDeleteMessage(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	channel := r.FormValue("channel")
	ts := r.FormValue("ts")

	m.mu.Lock()
	for i := range m.messages {
		if m.messages[i].TS == ts && m.messages[i].Channel == channel {
			m.messages[i].Deleted = true
			break
		}
	}
	m.mu.Unlock()

	m.respond(w, map[string]any{
		"channel": channel,
		"ts":      ts,
	})
}

func (m *mockSlack) handleConversationReplies(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	channel := r.FormValue("channel")
	threadTS := r.FormValue("ts")
	oldest := r.FormValue("oldest")

	m.mu.Lock()
	var msgs []map[string]any
	for _, msg := range m.messages {
		if msg.Channel != channel || msg.ThreadTS != threadTS || msg.Deleted {
			continue
		}
		if oldest != "" && msg.TS <= oldest {
			continue
		}
		entry := map[string]any{
			"ts":   msg.TS,
			"text": msg.Text,
			"user": msg.User,
		}
		if msg.BotID != "" {
			entry["bot_id"] = msg.BotID
		}
		msgs = append(msgs, entry)
	}
	m.mu.Unlock()

	m.respond(w, map[string]any{
		"messages": msgs,
	})
}

func (m *mockSlack) handleGetPermalink(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	channel := r.FormValue("channel")
	ts := r.FormValue("message_ts")
	m.respond(w, map[string]any{
		"permalink": fmt.Sprintf("https://slack.test/archives/%s/p%s", channel, ts),
	})
}

func (m *mockSlack) handleAuthTest(w http.ResponseWriter, r *http.Request) {
	m.respond(w, map[string]any{
		"user_id": "U_OWNER",
		"team_id": "T_TEST",
	})
}

func (m *mockSlack) handleUsersInfo(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	userID := r.FormValue("user")
	m.respond(w, map[string]any{
		"user": map[string]any{
			"id":        userID,
			"name":      "user-" + userID,
			"real_name": "User " + userID,
			"profile": map[string]any{
				"display_name": "User " + userID,
			},
		},
	})
}

// Native streaming handlers

func (m *mockSlack) handleStartStream(w http.ResponseWriter, r *http.Request) {
	var params map[string]any
	json.NewDecoder(r.Body).Decode(&params)

	m.mu.Lock()
	streamID := "stream-" + strconv.Itoa(m.tsSeq+1)
	msgTS := m.nextTS()
	m.streams[streamID] = &mockStream{
		channel:  params["channel"].(string),
		threadTS: fmt.Sprint(params["thread_ts"]),
		msgTS:    msgTS,
	}
	m.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ok":         true,
		"stream_id":  streamID,
		"message_ts": msgTS,
	})
}

func (m *mockSlack) handleAppendStream(w http.ResponseWriter, r *http.Request) {
	var params map[string]any
	json.NewDecoder(r.Body).Decode(&params)

	streamID, _ := params["stream_id"].(string)
	chunks, _ := params["chunks"].([]any)

	m.mu.Lock()
	s, ok := m.streams[streamID]
	if ok {
		for _, chunk := range chunks {
			data, _ := json.Marshal(chunk)
			s.chunks = append(s.chunks, data)
		}
	}
	m.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (m *mockSlack) handleStopStream(w http.ResponseWriter, r *http.Request) {
	var params map[string]any
	json.NewDecoder(r.Body).Decode(&params)

	streamID, _ := params["stream_id"].(string)

	m.mu.Lock()
	if s, ok := m.streams[streamID]; ok {
		s.stopped = true
	}
	m.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// messageText returns the mrkdwn text content of a message's blocks.
// It extracts text from section and context blocks.
func (m *mockMessage) blockText() string {
	if len(m.Blocks) == 0 {
		return m.Text
	}
	var blocks []struct {
		Type     string `json:"type"`
		Text     *struct{ Text string } `json:"text,omitempty"`
		Elements []struct {
			Text string `json:"text"`
		} `json:"elements,omitempty"`
	}
	if err := json.Unmarshal(m.Blocks, &blocks); err != nil {
		return m.Text
	}
	var parts []string
	for _, b := range blocks {
		if b.Text != nil {
			parts = append(parts, b.Text.Text)
		}
		for _, e := range b.Elements {
			parts = append(parts, e.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// injectReply adds a reply message to the mock (simulating a user typing in the thread).
func (m *mockSlack) injectReply(channel, threadTS, userID, text string) string {
	ts := m.nextTS()
	m.mu.Lock()
	m.messages = append(m.messages, mockMessage{
		Channel:  channel,
		ThreadTS: threadTS,
		Text:     text,
		User:     userID,
		TS:       ts,
	})
	m.mu.Unlock()
	return ts
}

// injectBotReply adds a bot reply message to the mock.
func (m *mockSlack) injectBotReply(channel, threadTS, botID, text string) string {
	ts := m.nextTS()
	m.mu.Lock()
	m.messages = append(m.messages, mockMessage{
		Channel:  channel,
		ThreadTS: threadTS,
		Text:     text,
		BotID:    botID,
		TS:       ts,
	})
	m.mu.Unlock()
	return ts
}
