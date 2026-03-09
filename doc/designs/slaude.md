# slaude Design

## Overview

slaude is a CLI that wraps Claude Code and mirrors its output to a Slack
thread. It uses the slagent library for Slack streaming. The local developer
runs a terminal session while Claude's responses, thinking state, and tool
activity are mirrored to a Slack thread. Thread replies from teammates are
injected back into Claude's conversation.

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

## Package Structure

```
cmd/slaude/                       CLI entry point, command routing
  main.go
cmd/slaude/internal/
  session/session.go              Session orchestration (wires claude + terminal + slagent)
  claude/process.go               Claude Code subprocess lifecycle
  claude/events.go                Stream-JSON event type definitions and parser
  terminal/terminal.go            Terminal UI (streaming output, tool/thinking lines)
  perms/server.go                 MCP stdio server for permission prompts
  perms/listener.go               Unix socket listener in parent slaude process
```

## CLI Flags

slaude separates its own flags from Claude flags using `--`:

```bash
slaude start -c CHANNEL -- --permission-mode plan "topic"
slaude join https://team.slack.com/archives/C123/p1234567890123456 "topic"
slaude resume https://team.slack.com/archives/C123/p1234567890123456#abc123@1700000005.000000 -- --resume SESSION_ID
```

### slaude flags

| Flag | Description |
|------|-------------|
| `-c, --channel` | Slack channel name or ID (start only) |
| `-u, --user` | Slack user(s) for DM (start only) |
| `-w, --workspace` | Slack workspace URL (uses default if omitted) |
| `--debug` | Write debug logs |
| `[topic...]` | Positional topic arg |

Everything after `--` is passed through to the Claude subprocess. slaude does
not own `--permission-mode`, `--resume`, `--system-prompt`, etc. — these are
controlled by the user directly.

### Subcommands

- `slaude start` — start a new session mirrored to Slack
- `slaude join URL` — join an existing thread with a new slaude instance
- `slaude resume URL#instanceID@lastTS -- --resume SESSION_ID` — resume an existing session
- `slaude auth` — extract credentials from Slack desktop app (default), or `--manual` to paste a token
- `slaude channels` — list accessible channels
- `slaude share FILE -c CHANNEL` — post a plan file to Slack
- `slaude status` — show workspaces and current configuration

### Thread URLs

`join` and `resume` accept Slack permalink URLs:
```
https://workspace.slack.com/archives/CHANNEL/pTIMESTAMP#instanceID[@lastTS]
```

The channel and thread timestamp are extracted from the URL. The `#instanceID`
fragment carries the slagent instance ID for block_id tagging on resume.
The optional `@lastTS` suffix is a cursor: the timestamp of the last seen
message. On resume, all messages up to this point are skipped, avoiding
re-processing of old commands and feedback.

## Auth

`slaude auth` extracts tokens from the Slack desktop app. When multiple
workspaces are found, the user picks one. Running auth multiple times adds
more workspaces to `~/.config/slagent/credentials.json`. The first workspace
saved becomes the default; use `-w` to select a different one.

```bash
slaude auth              # extract from local Slack app (default)
slaude auth --manual     # paste a token manually
```

One session always targets one workspace, selected by `-w` or the default.

## Claude Code Integration

### Subprocess Management

Claude Code is launched as a subprocess with base flags:
- `-p` — piped mode (reads from stdin)
- `--output-format stream-json` — structured event output
- `--input-format stream-json` — structured JSON input
- `--verbose` — required for stream-json to work
- `--include-partial-messages` — get intermediate assistant events

Additional flags are passed through from the user via `--` separator.
The `CLAUDECODE` environment variable is unset to prevent nested-invocation
detection.

### System Prompt Injection

The session intercepts `--system-prompt` in the pass-through args to append
Slack context when a Slack thread is active:

```go
slackCtx := "\n\nYour session is mirrored to a Slack thread. " +
    "Messages prefixed with [Team feedback from Slack] contain input from " +
    "team members watching the thread."
```

If `--system-prompt` is already in the pass-through args, the context is
appended. Otherwise a new `--system-prompt` arg is added.

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
1. The slagent turn is finalized (text message updated to full content)
2. Tasks message is reposted to stay near the bottom of the thread
3. Queued Slack replies are checked and injected as the next user message
4. The session waits for more Slack feedback

### Early Thinking

When forwarding a Slack message to Claude, the session creates a turn and
posts an `{emoji}:claude:` activity immediately — before Claude starts
responding. This gives instant visual feedback in Slack. The same turn is
then passed to `readTurn()` so Claude's actual response replaces the
thinking activity. If no real content follows (no text, no tools, no
substantive thinking), the activity is deleted on finish.

Messages addressed to other instances (`:other_emoji::`) skip this since
Claude will silently ignore them anyway.

### Tool Lifecycle

Tools are tracked across their lifecycle in session.go:

1. `tool_use` event → `ToolRunning` posted to slagent, shown in terminal
2. Next `tool_use`/`system`/`result` event → previous tool marked `ToolDone`
3. `finishTool()` helper called at each boundary to close the previous tool

Tool display uses two functions:
- `formatTool()` — terminal display with emoji prefix
- `toolDetail()` — raw detail string for slagent (no emoji, just file/pattern/command)

### Interactive Tools

Some tools are interactive prompts between Claude and the terminal user:
- `ExitPlanMode` — Claude requests to leave plan mode
- `EnterPlanMode` — Claude requests to enter plan mode
- `AskUserQuestion` — Claude asks a question with options

These are posted to Slack as prominent standalone messages (not activity lines)
so thread observers can see what Claude is asking for.

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
     g. Intercept TodoWrite → update tasks message in Slack
     h. On result: finalize turn, repost tasks, return
  3. Start Slack poller (background goroutine)
  4. Wait for Slack replies (blocking)
  5. Separate commands from feedback:
     a. Commands (`Reply.Command`): sent directly to Claude, each as its own turn
     b. Feedback (`Reply.Text`): wrapped in [Team feedback from Slack], sent as one message
  6. Read turn (go to step 2)
```

## Permission Approval via MCP

Claude Code's `--permission-prompt-tool` delegates permission decisions to an
MCP tool. slaude uses this to let the Slack thread owner approve or deny tools.

### Architecture

```
Claude Code                    slaude (parent)              Slack
    │                              │                          │
    ├─ needs permission ──────────>│                          │
    │  (MCP tool call)             │                          │
    │                              ├─ post ✅❌ prompt ──────>│
    │                              │                          │
    │                              │<── poll owner reaction ──│
    │                              │                          │
    │<── allow/deny ──────────────│                          │
    │                              ├─ delete prompt ─────────>│
```

### Components

- **MCP stdio server** (`perms/server.go`): Hidden `_mcp-permissions`
  subcommand. Claude launches it as an MCP subprocess. Handles JSON-RPC 2.0
  (initialize, tools/list, tools/call). Forwards permission requests to the
  parent slaude process via Unix socket.

- **Unix socket listener** (`perms/listener.go`): Runs in the parent slaude
  process. Accepts connections from the MCP server. Delegates to a `Handler`
  function that posts the prompt to Slack, polls for reaction, and returns
  allow/deny.

### MCP Response Format

Allow response (updatedInput is **required** — Claude validates as union type):
```json
{"behavior": "allow", "updatedInput": {"command": "ls", "description": "..."}}
```

Deny response (message is **required**):
```json
{"behavior": "deny", "message": "denied by owner"}
```

### Config

`--mcp-config` expects a **file path**, not inline JSON. The listener writes
config to a temp file via `MCPConfigFile()`. The tool reference is
`mcp__slaude_perms__permission_prompt`.

## Task Tracking in Slack

Claude's `TodoWrite` tool_use events are intercepted to mirror the task list
in the Slack thread as a persistent message.

### Flow

1. `tool_use` with `ToolName == "TodoWrite"` → `updateTodos()` parses input
2. Tasks rendered as mrkdwn: `📋 *Tasks*\n  ☐ pending\n  ⏳ in_progress\n  ✅ ~completed~`
3. First call posts a new message; subsequent calls update in place
4. After each turn (`result` event), the tasks message is delete+reposted
   to keep it near the bottom of the thread

### Thread Message Ordering

Messages at the bottom of the thread follow this order:
1. **Text message** — agent response (activity is transient, deleted when text arrives)
2. **Tasks message** — persistent TODO list (only shown when tasks exist)
3. **Question/prompt** — interactive prompt with reaction emojis (optional)

## Interactive Prompts

Interactive tools (ExitPlanMode, AskUserQuestion, EnterPlanMode) are posted
to Slack with reaction emojis that the session owner clicks to respond.

### Multi-choice (AskUserQuestion with allowedPrompts)

Posted as a numbered list with emoji reactions (`:one:`, `:two:`, etc.).
Owner clicks a reaction to select. `FinalizeReaction` cleans up: re-adds
the selected reaction and removes the rest.

### Binary prompts (ExitPlanMode, EnterPlanMode)

Posted with `:white_check_mark:` and `:x:` reactions. Owner clicks to
approve or reject.

### Free-text questions (AskUserQuestion without allowedPrompts)

Not posted as a separate prompt. The question text is streamed as the
turn's text message with `MarkQuestion(prefix)` adding `@mention` and
trailing `❓`.

## Emoji-Prefix Instance Targeting

When multiple slaude instances share a thread, messages can be directed to a
specific instance using the `:shortcode::` prefix (renders as `🦊:` in Slack).

### Format

```
:fox_face:: do this task         →  addressed to fox (all see it, others ignore via prompt)
:dog:: /compact                  →  /compact sent exclusively to dog instance
<@U123> :fox_face:: hello        →  @mention + addressed to fox
regular message                  →  broadcast to all instances
```

### Parsing

Handled by `parseInstancePrefix()` in slagent's `thread.go`:
1. Strip leading `<@...>` mentions
2. Match `:shortcode::` where shortcode is a known identity emoji
3. If the text starts with `/`, it's a command — instance-exclusive:
   - Only the targeted instance receives it
   - `/open`, `/close`: handled by slaude (thread access control)
   - Unknown `/commands`: forwarded to Claude via `Reply.Command`
4. Non-command messages are delivered to ALL instances with the original
   text (prefix included). The system prompt tells Claude to ignore
   messages prefixed with another instance's emoji.
5. `mistargeted()` detects wrong syntax (`:fox_face: /cmd` with single
   colon, or `🦊 /cmd` with Unicode emoji) and posts a hint suggesting
   the correct `::` syntax. Only triggers for `/commands`.

### Session Handling

In `session.go`, replies are split into commands and feedback:
- Commands are sent directly to Claude stdin (one turn per command)
- Regular feedback is batched as `[Team feedback from Slack]`

The system prompt tells Claude about the `:shortcode::` convention:
messages with its own emoji should be acted on, messages with another
instance's emoji should generally be ignored.

## Thread Access Control

Thread access is managed through `/open` and `/lock` commands via emoji-prefix
targeting (`:shortcode:: /open`). Access state is reflected in the thread title.

### Commands

| Command | Effect |
|---------|--------|
| `/open` | Open for everyone |
| `/open <@U1> <@U2>` | Allow specific users (additive) |
| `/lock` | Lock to owner only (clears all) |
| `/lock <@U1>` | Ban specific users |
| `/close` | Alias for `/lock` |

### Title Format

The thread parent message reflects the access state:
- `🔒🧵 Topic` — locked (owner only)
- `🧵 Topic` — open for all
- `🧵 <@U1> <@U2> Topic` — open for specific users
- `🧵 Topic (🔒 <@U3>)` — open but with banned users

On `Resume()`, the title is parsed to recover the access state.

### Rules

- Only the owner can execute access commands
- Owner can never be banned
- `/open <@U>` unbans a previously banned user
- `/lock <@U>` removes from the allowed list
- Other slaude instances are subject to the same access rules

## Dependencies

| Dependency | Purpose |
|---|---|
| `github.com/alecthomas/kong` | CLI argument parsing |

Plus the slagent library and its transitive dependencies.

## Platform Support

| Platform | Token extraction | Session mirroring |
|----------|-----------------|-------------------|
| macOS    | Yes             | Yes               |
| Linux    | Yes             | Yes               |
| Windows  | No              | Yes (manual auth) |
