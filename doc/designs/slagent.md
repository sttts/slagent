# slagent Design

## Overview

slagent is a Go library for streaming agent sessions to Slack threads. It
provides a `Thread` and `Turn` abstraction over two Slack backends, letting
any agent framework mirror its output to a Slack thread in real time.

Module: `github.com/sttts/slagent`

## Package Structure

```
slagent.go          Package root: token types, Thread options, client factory
thread.go           Thread lifecycle: Start, Resume, NewTurn, Post*, Replies
turn.go             Turn interface + turnImpl delegation wrapper
compat.go           Compat backend (xoxc-/xoxp-): postMessage/update
native.go           Native backend (xoxb-): startStream/appendStream/stopStream
reply.go            Thread reply polling + authorization
mrkdwn.go           Markdown-to-mrkdwn converter, splitAtLines

credential/         Slack credentials and token extraction
  credential.go       Credentials struct, Load, Save, Path, multi-workspace store
  extract.go          Top-level Extract() orchestrator
  paths.go            Platform-specific Slack data directory detection
  leveldb.go          LevelDB token extraction (Workspace struct)
  cookie.go           SQLite cookie database reading
  decrypt.go          Chromium cookie decryption (AES-CBC, PBKDF2)

channel/            Channel and user resolution
  channel.go          Builder, Client, ResolveByName, ResolveUser, List
```

## Turn Interface

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

## Backend Selection

The backend is chosen automatically based on token prefix:

| Token    | Backend | Mechanism |
|----------|---------|-----------|
| `xoxb-`  | Native  | `chat.startStream` / `appendStream` / `stopStream` |
| `xoxc-`  | Compat  | `chat.postMessage` / `chat.update` (1/sec throttle) |
| `xoxp-`  | Compat  | same as xoxc- |

## Compat Backend (xoxc-/xoxp-)

Uses standard Slack Web API. Two message types per turn:

1. **Activity message** — single context block, updated in-place, showing
   thinking + tools + status (max 6 lines). Tools tracked via `toolIndex` map
   for in-place updates. Icons: `:claude:` when running, checkmark when done,
   X on error. **Activity is transient** — deleted when new text arrives via
   `deleteActivity()`, keeping the thread clean.

2. **Text message** — posted with robot prefix, markdown converted to mrkdwn.
   Full text is shown during streaming (no truncation).

**Throttling and debounce:**
- Updates throttled to 1/sec per message (Slack rate limit)
- `finalizeText()` flushes text with final block_id and resets state when
  tools/thinking start — ensures text is visible before activity begins and
  post-tool text creates a new message
- Debounce timers (`time.AfterFunc`, 1s) flush remaining content after idle

## Native Backend (xoxb-)

Uses Slack's native streaming API for real-time updates:
- `chat.startStream` — opens a stream, returns `stream_id`
- `chat.appendStream` — sends chunks (markdown_text, task_update)
- `chat.stopStream` — finalizes the stream

Text is buffered and flushed when the buffer exceeds `bufferSize` (default
256 bytes). Thinking and tools are sent as `task_update` chunks with status
tracking.

## Thread Lifecycle

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

thread.Post("Status update")              // standalone message (returns ts)
thread.UpdateMessage(ts, "Updated text")  // update existing message
thread.DeleteMessage(ts)                  // delete a message
thread.PostUser("alice", "What about X?") // user message with avatar
thread.PostMarkdown(planText)             // content in code block

// Interactive prompts with reaction emojis
ts, _ := thread.PostPrompt("Approve?", []string{"white_check_mark", "x"})
selected, _ := thread.PollReaction(ts, []string{"white_check_mark", "x"})
thread.FinalizeReaction(ts, selected, []string{"white_check_mark", "x"})
```

Thread parent message format: `:thread: <title>` (plain text for emoji
shortcode rendering).

## Message Identity and Stabilization

Every message posted by slagent carries a `block_id` with the prefix `slagent-{instanceID}`.
The instance ID is an 8-char random hex string, unique per slaude process, inherited on resume.

Block ID suffixes encode message state:

| Suffix | Meaning | Poller behavior |
|--------|---------|-----------------|
| (none) | Finalized text | Skip if our instance; deliver if another instance |
| `~`    | Streaming text | Always skip, don't advance cursor (re-check next poll) |
| `~act` | Activity | Always skip from all instances |

This allows multiple slaude instances in the same thread to see each other's
finalized messages as replies, while ignoring in-flight streaming and transient
activity.

## Reply Polling and Authorization

`thread.PollReplies()` / `thread.Replies(ctx)` poll `conversations.replies`:

- Skip parent message and already-seen messages (tracked via `lastTS`)
- Classify slagent blocks by kind and source instance (see above)
- Skip bot messages (`BotID != ""`)
- Parse `:shortcode::` prefix for instance targeting (see below)
- Handle `/open` / `/close` commands (toggle open access)
- Check authorization: owner only by default, all participants if open access

User display names are resolved via `users.info` and cached.

## Emoji-Prefix Instance Targeting

Messages can be targeted at a specific slaude instance using `:shortcode::` prefix:

```
:fox_face:: do this task       →  renders as  🦊: do this task
:dog:: /compact                →  renders as  🐶: /compact
<@U123> :fox_face:: hello      →  @mention + targeted
```

Parsing is done by `parseInstancePrefix()` in `thread.go`:
- Matches `:shortcode::` where shortcode is a known identity emoji
- Non-command messages are delivered to ALL instances with the original text
  (prefix included). The system prompt tells Claude to ignore messages
  prefixed with another instance's emoji.
- Commands (`/open`, `/close`, `/compact`, etc.) are instance-exclusive:
  only the targeted instance receives them.
- `/open` and `/close` are handled by slaude. Unknown `/commands` are
  forwarded to Claude via `Reply.Command`.

## Channel Package

The channel package uses a builder pattern for construction:

```go
client, err := channel.New().Build()                           // default workspace
client, err := channel.New().WithWorkspace("team.slack.com").Build()  // specific workspace
```

Provides:
- `ResolveChannelByName(name)` — look up channel by name
- `ResolveUserChannel(names...)` — open DM/group DM
- `ListChannels(progress)` — list channels with activity filtering

## Credential Storage

Credentials are stored per workspace in `~/.config/slagent/credentials.json`
with 0600 permissions:

```json
{
  "default": "myteam.slack.com",
  "workspaces": {
    "myteam.slack.com": {
      "token": "xoxc-...",
      "type": "session",
      "cookie": "xoxd-..."
    },
    "other.slack.com": {
      "token": "xoxc-...",
      "type": "session",
      "cookie": "xoxd-..."
    }
  }
}
```

Running `slaude auth` multiple times adds workspaces. The first workspace
saved becomes the default. `credential.Load(workspace)` loads a specific
workspace; empty string loads the default.

Three token types are supported:

| Token Type | Prefix | How to Get | Admin Required |
|-----------|--------|------------|----------------|
| Session   | xoxc-  | `slaude auth` | No |
| Bot       | xoxb-  | Slack app OAuth | Yes |
| User      | xoxp-  | Slack app OAuth (user scopes) | Yes |

### Token Extraction Process

1. **Find Slack data directory** — checks platform-specific paths:
   - macOS App Store: `~/Library/Containers/com.tinyspeck.slackmacgap/.../Slack/`
   - macOS direct: `~/Library/Application Support/Slack/`
   - Linux: `~/.config/Slack/`

2. **Extract xoxc token from LevelDB** — Slack stores workspace config in a
   LevelDB database at `Local Storage/leveldb/`. The database is copied to a
   temp directory first (Slack holds the lock file). Values may have byte
   prefixes or UTF-16LE encoding that must be handled.

3. **Decrypt xoxd cookie from SQLite** — The `Cookies` file is a Chromium cookie
   database. The `d` cookie for `.slack.com` is encrypted using platform-specific
   key derivation (macOS: Keychain + PBKDF2, Linux: GNOME Keyring + PBKDF2).

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
- **Session tokens** have the same permissions as the user's Slack session
- **Token extraction** requires local access to Slack's data directory and keychain/keyring
- **Cookie handling** keeps the `xoxd-` value in memory and on disk; sent only to Slack's API
- **No secrets in code** — all tokens come from user input or local extraction
