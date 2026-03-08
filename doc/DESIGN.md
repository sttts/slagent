# slagent Design Document

## Overview

slagent is a Go library for streaming agent sessions to Slack threads. The `slaude` CLI wraps Claude Code and mirrors its output to Slack, letting remote team members follow along and provide feedback in real-time. The local developer runs a terminal session while Claude's responses, thinking state, and tool activity are mirrored to a Slack thread. Thread replies from teammates are injected back into Claude's conversation.

## Architecture

```
┌─────────────┐     stream-json      ┌──────────────┐     Slack API      ┌───────────┐
│ Claude Code │ ──────────────────> │    slaude    │ ────────────────> │   Slack   │
│ (subprocess)│ <────────────────── │              │ <──────────────── │  thread   │
└─────────────┘     stdin (prompts)  │  ┌────────┐ │     poll replies  └───────────┘
                                     │  │terminal│ │
                                     │  └────────┘ │
                                     └──────────────┘
```

### Package Structure

```
slagent.go                        Package root: token types, Thread options, client factory
thread.go                         Thread lifecycle: Start, Resume, NewTurn, Post*, Replies
turn.go                           Turn interface + turnImpl delegation wrapper
compat.go                         Compat backend (xoxc-/xoxp-): postMessage/update
native.go                         Native backend (xoxb-): startStream/appendStream/stopStream
reply.go                          Thread reply polling + authorization
mrkdwn.go                         Markdown-to-mrkdwn converter, splitAtLines

credential/                       Slack credentials and token extraction
  credential.go                     Credentials struct, Load, Save, Path
  extract.go                        Top-level Extract() orchestrator
  paths.go                          Platform-specific Slack data directory detection
  leveldb.go                        LevelDB token extraction
  cookie.go                         SQLite cookie database reading
  decrypt.go                        Chromium cookie decryption (AES-CBC, PBKDF2)

channel/                          Channel and user resolution
  channel.go                        Client, ResolveByName, ResolveUser, List

cmd/slaude/                       CLI entry point, command routing
  main.go
cmd/slaude/internal/
  session/session.go                Session orchestration (wires claude + terminal + slagent)
  claude/process.go                 Claude Code subprocess lifecycle
  claude/events.go                  Stream-JSON event type definitions and parser
  terminal/terminal.go              Terminal UI (streaming output, tool/thinking lines)

cmd/slagent-demo/main.go         Standalone demo exercising slagent Slack UI
```

Module: `github.com/sttts/slagent`

## Claude Code Integration

### Subprocess Management

Claude Code is launched as a subprocess with base flags:
- `-p` — piped mode (reads from stdin)
- `--output-format stream-json` — structured event output
- `--input-format stream-json` — structured JSON input
- `--verbose` — required for stream-json to work
- `--include-partial-messages` — get intermediate assistant events

Additional flags are passed through from the user via `--` separator. slaude does not own `--permission-mode`, `--resume`, `--system-prompt`, etc. — these are controlled by the user directly.

The `CLAUDECODE` environment variable is unset to prevent nested-invocation detection.

### Pass-through Args

slaude separates its own flags from Claude flags using `--`:

```bash
slaude -c CHANNEL -- --permission-mode plan --resume SESSION_ID "topic"
```

The session intercepts `--system-prompt` in the pass-through args to append Slack context (feedback framing) when a Slack thread is active.

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
- `tool_use` — tool invocation (name + input JSON)
- `assistant` — complete message (fallback when no streaming occurred)
- `result` — turn boundary, signals response is complete

### Turn Boundaries

The `result` event marks the end of a turn. At this point:
1. The slagent turn is finalized (text message updated to full content, activity frozen)
2. Queued Slack replies are checked and injected as the next user message
3. The session waits for more Slack feedback

### Tool Lifecycle

Tools are tracked across their lifecycle in session.go:

1. `tool_use` event → `ToolRunning` posted to slagent, shown in terminal
2. Next `tool_use`/`system`/`result` event → previous tool marked `ToolDone`
3. `finishTool()` helper called at each boundary to close the previous tool

Tool display uses two functions:
- `formatTool()` — terminal display with emoji prefix (📄 Read, 💻 Bash, etc.)
- `toolDetail()` — raw detail string for slagent (no emoji, just file/pattern/command)

### Interactive Tools

Some tools are interactive prompts between Claude and the terminal user:
- `ExitPlanMode` — Claude requests to leave plan mode
- `EnterPlanMode` — Claude requests to enter plan mode
- `AskUserQuestion` — Claude asks a question with options

These are posted to Slack as prominent standalone messages (not activity lines)
so thread observers can see what Claude is asking for. The actual approval/response
happens at the terminal where Claude Code handles the prompt directly.

**Planned: Socket Mode integration** for Block Kit buttons that let the Slack
thread owner approve/reject directly from Slack (see "Interactive Buttons" below).

## slagent — Slack Agent Streaming

The `slagent` package provides a unified streaming interface for mirroring agent
sessions to Slack. It abstracts two backends behind a common `Turn` interface.

### Turn Interface

```go
type Turn interface {
    Thinking(text string)                    // append thinking content
    Tool(id, name, status, detail string)    // report tool activity
    Text(text string)                        // append response text
    Status(text string)                      // transient status line
    MarkQuestion(prefix string)              // mark as question turn
    Finish() error                           // finalize the turn
}
```

### Backend Selection

The backend is chosen automatically based on token prefix:

| Token    | Backend | Mechanism |
|----------|---------|-----------|
| `xoxb-`  | Native  | `chat.startStream` / `appendStream` / `stopStream` |
| `xoxc-`  | Compat  | `chat.postMessage` / `chat.update` (1/sec throttle) |
| `xoxp-`  | Compat  | same as xoxc- |

### Compat Backend (xoxc-/xoxp-)

Uses standard Slack Web API. Two message types per turn:

1. **Activity message** — single context block, updated in-place, showing thinking + tools + status (max 6 lines). Tools tracked via `toolIndex` map for in-place updates. Icons: `:claude:` when running, ✓ when done, ❌ on error.

2. **Text message** — posted with `🤖` prefix, markdown converted to mrkdwn.
   Full text is shown during streaming (no truncation).

**Throttling and debounce:**
- Updates throttled to 1/sec per message (Slack rate limit)
- `forceFlushText()` bypasses throttle when tools/thinking start (ensures text is visible before activity begins)
- Debounce timers (`time.AfterFunc`, 1s) flush remaining content after idle period

### Native Backend (xoxb-)

Uses Slack's native streaming API for real-time updates:
- `chat.startStream` — opens a stream, returns `stream_id`
- `chat.appendStream` — sends chunks (markdown_text, task_update)
- `chat.stopStream` — finalizes the stream

Text is buffered and flushed when the buffer exceeds `bufferSize` (default 256 bytes).
Thinking and tools are sent as `task_update` chunks with status tracking.

### Thread Lifecycle

```go
thread := slagent.NewThread(client, token, ch, opts...)
thread.Start("Planning: redesign auth")   // post parent message
// or
thread.Resume("1700000001.000000")         // resume existing thread

turn := thread.NewTurn()                   // start a new response turn
turn.Thinking("analyzing code...")
turn.Tool("t1", "Read", "running", "main.go")
turn.Tool("t1", "Read", "done", "main.go")
turn.Text("Here is my analysis...")
turn.Finish()

thread.Post("Status update")              // standalone message
thread.PostUser("alice", "What about X?") // user message with avatar
thread.PostMarkdown(planText)             // content in code block
```

### Reply Polling and Authorization

`thread.PollReplies()` / `thread.Replies(ctx)` poll `conversations.replies`:

- Skip parent message and already-seen messages (tracked via `lastTS`)
- Skip messages we posted (tracked via `postedTS` map)
- Skip bot messages (`BotID != ""`)
- Handle `!open` / `!close` commands (toggle open access)
- Check authorization: owner only by default, all participants if open access

User display names are resolved via `users.info` and cached.

## Terminal Output

The terminal UI (`cmd/slaude/internal/terminal`) provides simple line-based output:

- **Thinking**: each thinking delta prints on its own line: `  💭 analyzing the codebase...`
- **Tools**: each tool prints on its own line: `  📄 main.go`, `  💻 go build`
- **Text**: streamed inline after the `🤖 Claude:` prefix
- **No in-place updates**: every line is final (no cursor movement, no clearLine)

## Session Orchestration

The `session.Session` struct wires everything together:

```
Main Loop:
  1. Send initial topic to Claude (skip if --resume in pass-through args)
  2. Read turn:
     a. Stream text to terminal + slagent turn
     b. Show thinking lines in terminal + slagent turn
     c. Show tool activity in terminal + slagent turn
     d. Post interactive tools as standalone Slack messages
     e. Post code diffs (Edit/Write) as separate Slack messages
     f. Track tool lifecycle (running → done)
     g. On result: finalize turn, return
  3. Start Slack poller (background goroutine)
  4. Wait for Slack replies (blocking)
  5. Show replies in terminal, format as [Team feedback from Slack]
  6. Send to Claude via stdin
  7. Read turn (go to step 2)
```

## Interactive Buttons (planned)

Interactive tools (ExitPlanMode, AskUserQuestion, EnterPlanMode) should be shown
in Slack with Block Kit buttons that the session owner can click to approve/reject.

### Constraint

Block Kit button clicks send an interaction payload to a server endpoint.
slaude is a CLI without a persistent server. Two viable approaches:

### Approach: Slack Socket Mode

Socket Mode delivers interaction payloads over WebSocket — no public URL needed.

Requirements:
- A Slack app with Socket Mode enabled
- Bot token (`xoxb-`) for API calls
- App-level token (`xapp-`) for the WebSocket connection

Architecture:
```
┌──────────┐  WebSocket   ┌──────────┐  interaction  ┌──────────┐
│  Slack   │ ──────────> │  slaude  │ <──────────── │  User    │
│ Platform │              │SocketMode│   clicks btn  │ in Slack │
└──────────┘              │ listener │               └──────────┘
                          └──────────┘
```

Flow:
1. Claude emits ExitPlanMode → slaude posts Block Kit message with buttons
2. Owner clicks "Approve" → Slack sends interaction payload over WebSocket
3. slaude receives payload, verifies owner, sends approval to Claude stdin
4. Message updated to show "✅ Approved by @owner"

Button layout:
- ExitPlanMode: `[✅ Approve] [❌ Reject]`
- EnterPlanMode: `[✅ Approve] [❌ Reject]`
- AskUserQuestion: one button per `allowedPrompts` option

Limitation: Only available with bot tokens (xoxb-). Session tokens (xoxc-)
fall back to text-only prompts in Slack with terminal-only interaction.

## Authentication Model

Three token types are supported, stored in `~/.config/slagent/credentials.json`:

| Token Type | Prefix | How to Get | Admin Required |
|-----------|--------|------------|----------------|
| Session   | xoxc-  | `slaude auth` | No |
| Bot       | xoxb-  | Slack app OAuth | Yes |
| User      | xoxp-  | Slack app OAuth (user scopes) | Yes |

### Session Token Architecture

Session tokens (`xoxc-`) are the same tokens the Slack desktop app uses internally.
They require a companion cookie (`xoxd-`) that must be sent with every API request.

The cookie is injected via a custom `http.Client` wrapper (`cookieHTTPClient`)
passed to the slack-go library via `slack.OptionHTTPClient()`.

### Token Extraction Process

1. **Find Slack data directory** — checks platform-specific paths:
   - macOS App Store: `~/Library/Containers/com.tinyspeck.slackmacgap/.../Slack/`
   - macOS direct: `~/Library/Application Support/Slack/`
   - Linux: `~/.config/Slack/`

2. **Extract xoxc token from LevelDB** — Slack stores workspace config in a
   LevelDB database at `Local Storage/leveldb/`. The database is copied to a
   temp directory first (Slack holds the lock file). Values may have byte prefixes
   or UTF-16LE encoding that must be handled.

3. **Decrypt xoxd cookie from SQLite** — The `Cookies` file is a Chromium cookie
   database. The `d` cookie for `.slack.com` is encrypted using platform-specific
   key derivation (macOS: Keychain + PBKDF2, Linux: GNOME Keyring + PBKDF2).

## Credentials Storage

Credentials are stored in `~/.config/slagent/credentials.json` with 0600 permissions:

```json
{
  "token": "xoxc-...",
  "type": "session",
  "cookie": "xoxd-..."
}
```

## Dependencies

| Dependency | Purpose |
|---|---|
| `github.com/slack-go/slack` | Slack Web API client |
| `github.com/syndtr/goleveldb` | LevelDB reader for token extraction |
| `modernc.org/sqlite` | Pure-Go SQLite for cookie database reading |
| `golang.org/x/crypto/pbkdf2` | PBKDF2 key derivation for cookie decryption |
| `github.com/alecthomas/kong` | CLI argument parsing |

All dependencies are pure Go (no CGO required), enabling simple cross-compilation.

## Security Considerations

- **Credentials file** is written with `0600` permissions (owner read/write only)
- **Session tokens** have the same permissions as the user's Slack session — they can do anything the user can do in Slack
- **Token extraction** requires local access to the Slack app's data directory and keychain/keyring access
- **Cookie handling** keeps the `xoxd-` value in memory and on disk; it's sent only to Slack's API servers
- **No secrets in code** — all tokens come from user input or local extraction
