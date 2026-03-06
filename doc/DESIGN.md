# pairplan Design Document

## Overview

pairplan bridges Claude Code planning sessions with Slack, letting remote team members follow along and provide feedback in real-time. The local developer runs a terminal session while Claude's responses, thinking state, and tool activity are mirrored to a Slack thread. Thread replies from teammates are injected back into Claude's conversation.

## Architecture

```
┌─────────────┐     stream-json      ┌──────────────┐     Slack API      ┌───────────┐
│ Claude Code │ ──────────────────> │   pairplan   │ ────────────────> │   Slack   │
│ (subprocess)│ <────────────────── │              │ <──────────────── │  thread   │
└─────────────┘     stdin (prompts)  │  ┌────────┐ │     poll replies  └───────────┘
                                     │  │terminal│ │
                                     │  └────────┘ │
                                     └──────────────┘
```

### Package Structure

```
cmd/pairplan/main.go        CLI entry point, command routing
pkg/claude/events.go        Stream-JSON event type definitions and parser
pkg/claude/process.go       Claude Code subprocess lifecycle
pkg/session/session.go      Session orchestration (wires claude + terminal + slack)
pkg/terminal/terminal.go    Terminal UI (prompts, streaming output)
pkg/slack/client.go         Slack API wrapper (Block Kit, live thinking, polling)
pkg/slack/extract/          Token extraction from local Slack desktop app
  extract.go                  Top-level Extract() orchestrator
  paths.go                    Platform-specific Slack data directory detection
  leveldb.go                  LevelDB token extraction
  cookie.go                   SQLite cookie database reading
  decrypt.go                  Chromium cookie decryption (AES-CBC, PBKDF2)
```

## Claude Code Integration

### Subprocess Management

Claude Code is launched as a subprocess with flags:
- `--output-format stream-json` — structured event output
- `--verbose` — required for stream-json to work
- `-p` — piped mode (reads from stdin)
- `--permission-mode plan` — default permission level

The `CLAUDE_CODE` environment variable is unset to prevent nested-invocation detection.

### Event Stream

Claude emits newline-delimited JSON events. Key event flow:

```
system → stream_event(message_start) → stream_event(content_block_start)
  → stream_event(content_block_delta: thinking_delta | text_delta)
  → stream_event(content_block_stop) → stream_event(message_stop)
  → assistant → result
```

Events are parsed into high-level `Event` structs:
- `text_delta` — streaming text content
- `thinking` — thinking phase (with accumulated text for live display)
- `tool_use` — tool invocation (name + input summary)
- `assistant` — complete message (fallback when no streaming occurred)
- `result` — turn boundary, signals response is complete

### Turn Boundaries

The `result` event marks the end of a turn. At this point:
1. The complete response text is posted to Slack
2. Queued Slack replies are checked and injected as the next user message
3. The terminal prompt is shown again

## Slack Integration

### Authentication Model

Three token types are supported, stored in `~/.config/pairplan/credentials.json`:

| Token Type | Prefix | How to Get | Admin Required |
|-----------|--------|------------|----------------|
| Session   | xoxc-  | `pairplan auth --extract` | No |
| Bot       | xoxb-  | Slack app OAuth | Yes |
| User      | xoxp-  | Slack app OAuth (user scopes) | Yes |

#### Session Token Architecture

Session tokens (`xoxc-`) are the same tokens the Slack desktop app uses internally. They require a companion cookie (`xoxd-`) that must be sent with every API request.

The cookie is injected via a custom `http.Client` wrapper:

```go
type cookieHTTPClient struct {
    inner  *http.Client
    cookie string
}

func (c *cookieHTTPClient) Do(req *http.Request) (*http.Response, error) {
    req.Header.Set("Cookie", fmt.Sprintf("d=%s", c.cookie))
    return c.inner.Do(req)
}
```

This is passed to the slack-go library via `slack.OptionHTTPClient()`.

#### Token Extraction Process

1. **Find Slack data directory** — checks platform-specific paths:
   - macOS App Store: `~/Library/Containers/com.tinyspeck.slackmacgap/.../Slack/`
   - macOS direct: `~/Library/Application Support/Slack/`
   - Linux: `~/.config/Slack/`

2. **Extract xoxc token from LevelDB** — Slack stores workspace config in a LevelDB database at `Local Storage/leveldb/`. The database is copied to a temp directory first (Slack holds the lock file). The `localConfig_v2` key contains JSON with `teams.WORKSPACE_ID.token` fields.

   Encoding challenges:
   - Values may have a 1-byte prefix (`\x00`, `\x01`, `\x02`) that must be stripped
   - Some Slack versions store values in UTF-16LE (detected by NUL byte frequency)

3. **Decrypt xoxd cookie from SQLite** — The `Cookies` file is a Chromium cookie database (SQLite). The `d` cookie for `.slack.com` is encrypted using:
   - **macOS**: Passphrase from Keychain (`security find-generic-password -s "Slack Safe Storage" -w`), PBKDF2-SHA1 with salt `saltysalt`, 1003 iterations, 16-byte key, AES-128-CBC with IV of 16 space bytes
   - **Linux**: Passphrase from GNOME Keyring (`secret-tool lookup application Slack`) or fallback `peanuts`, PBKDF2-SHA1 with 1 iteration, same AES-CBC

   The encrypted blob has a 3-byte prefix (`v10`) that is stripped before decryption. PKCS#7 padding is removed after decryption.

### Message Formatting

All messages use Slack's Block Kit for structured rendering:

| Message Type | Block Structure |
|---|---|
| Thread parent | Header block: "Planning session: {topic}" |
| Claude response (short, ≤3000 chars) | Section block with mrkdwn |
| Claude response (long, per chunk) | Section block with code block wrapping |
| User message | Context block (user name) + Section block (text) |
| Tool activity | Context block with tool summary |
| Thinking indicator | Context block, live-updated |
| Session end | Section block + Divider |

All Block Kit messages include a plain-text fallback for notifications and accessibility.

### Live Thinking Indicator

During Claude's thinking phase, a message is posted to Slack and updated in-place:

1. First `thinking` event → `chat.postMessage` with "thinking..." context block
2. Subsequent `thinking` events → `chat.update` with accumulated thinking text (throttled to 1 update/second, last 2000 chars shown)
3. First `text_delta` or `tool_use` or `result` event → `chat.delete` removes the thinking message

The `LiveThinking` struct is goroutine-safe with its own mutex.

### Auto-Split

Slack's Block Kit text limit is approximately 3000 characters per text object. Long Claude responses are handled:

- If ≤ 3000 chars: posted as a single Section block with mrkdwn formatting
- If > 3000 chars: split at line boundaries into ≤3000 char chunks, each posted as a separate message wrapped in a code block (triple backticks) to preserve formatting

The `splitAtLines()` function finds the last newline within the limit, falling back to a hard cut if no newline exists.

### Reply Polling

A background goroutine polls `conversations.replies` every 3 seconds. New messages are queued and injected at the next turn boundary.

Message filtering:
- **Bot tokens**: skip messages with a `BotID` (our bot's messages)
- **User/Session tokens**: skip messages from `ownUserID` (resolved via `auth.test` at startup)

User display names are resolved via `users.info` and cached for the session.

### Channel Support

The `channels` command lists all accessible conversations:
- Public channels (`public_channel`)
- Private channels (`private_channel`)
- Group DMs / multi-party IMs (`mpim`)
- Direct messages (`im`)

This is particularly useful for finding private group chat IDs, which aren't visible in the Slack UI's URL.

## Session Orchestration

The `session.Session` struct wires everything together:

```
Main Loop:
  1. Show terminal prompt
  2. Read user input
  3. Handle /commands (quit, status)
  4. Mirror user message to Slack (async)
  5. Send to Claude via stdin
  6. Read turn:
     a. Start LiveThinking on first thinking event
     b. Update LiveThinking with thinking text
     c. Stop LiveThinking on first text_delta
     d. Stream text to terminal
     e. Post tool activity to Slack (async)
     f. On result: post complete text to Slack, return
  7. Inject queued Slack replies (if any → send to Claude → read another turn)
  8. Loop to step 1
```

Slack operations (posting messages, tool activity) are done in goroutines to avoid blocking the terminal output.

## Credentials Storage

Credentials are stored in `~/.config/pairplan/credentials.json` with 0600 permissions:

```json
{
  "token": "xoxc-...",
  "type": "session",
  "cookie": "xoxd-..."
}
```

Backwards compatibility: if `token` is empty, the legacy `bot_token` field is used.

## Dependencies

| Dependency | Purpose |
|---|---|
| `github.com/slack-go/slack` | Slack Web API client |
| `github.com/syndtr/goleveldb` | LevelDB reader for token extraction |
| `modernc.org/sqlite` | Pure-Go SQLite for cookie database reading |
| `golang.org/x/crypto/pbkdf2` | PBKDF2 key derivation for cookie decryption |

All dependencies are pure Go (no CGO required), enabling simple cross-compilation.

## Security Considerations

- **Credentials file** is written with `0600` permissions (owner read/write only)
- **Session tokens** have the same permissions as the user's Slack session — they can do anything the user can do in Slack
- **Token extraction** requires local access to the Slack app's data directory and keychain/keyring access
- **Cookie handling** keeps the `xoxd-` value in memory and on disk; it's sent only to Slack's API servers
- **No secrets in code** — all tokens come from user input or local extraction
