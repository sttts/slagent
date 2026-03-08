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
```

## CLI Flags

slaude separates its own flags from Claude flags using `--`:

```bash
slaude start -c CHANNEL -- --permission-mode plan "topic"
slaude join https://team.slack.com/archives/C123/p1234567890123456 "topic"
slaude resume https://team.slack.com/archives/C123/p1234567890123456#abc123 -- --resume SESSION_ID
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
- `slaude join URL` — join an existing thread with a new slaude instance (planned)
- `slaude resume URL#instanceID -- --resume SESSION_ID` — resume an existing session (planned)
- `slaude auth` — extract credentials from Slack desktop app (default), or `--manual` to paste a token
- `slaude channels` — list accessible channels
- `slaude share FILE -c CHANNEL` — post a plan file to Slack
- `slaude status` — show workspaces and current configuration

### Thread URLs

`--resume-thread` accepts Slack permalink URLs:
```
https://workspace.slack.com/archives/CHANNEL/pTIMESTAMP#instanceID
```

The channel and thread timestamp are extracted from the URL. The `#instanceID`
fragment carries the slagent instance ID for block_id tagging on resume.

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
1. The slagent turn is finalized (text message updated to full content, activity frozen)
2. Queued Slack replies are checked and injected as the next user message
3. The session waits for more Slack feedback

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
slaude is a CLI without a persistent server.

### Approach: Slack Socket Mode

Socket Mode delivers interaction payloads over WebSocket — no public URL needed.

Requirements:
- A Slack app with Socket Mode enabled
- Bot token (`xoxb-`) for API calls
- App-level token (`xapp-`) for the WebSocket connection

Flow:
1. Claude emits ExitPlanMode → slaude posts Block Kit message with buttons
2. Owner clicks "Approve" → Slack sends interaction payload over WebSocket
3. slaude receives payload, verifies owner, sends approval to Claude stdin
4. Message updated to show "Approved by @owner"

Limitation: Only available with bot tokens (xoxb-). Session tokens (xoxc-)
fall back to text-only prompts in Slack with terminal-only interaction.

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
