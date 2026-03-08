# slagent

> **Experimental** — this project is under active development. APIs, CLI flags, and behavior may change without notice. Use at your own risk.

Go library for streaming agent sessions to Slack threads.

## Library

slagent provides `Thread`, `Turn`, and dual-backend streaming (native `xoxb-` / compat `xoxc-`) for building Slack-integrated agent UIs.

```go
import "github.com/sttts/slagent"

client := slagent.NewSlackClient(token, cookie)
thread := slagent.NewThread(client, token, channelID, slagent.WithOwner(userID))
url, _ := thread.Start("My agent session")

turn := thread.NewTurn()
turn.Thinking("analyzing...")
turn.Tool("t1", "Read", slagent.ToolRunning, "main.go")
turn.Tool("t1", "Read", slagent.ToolDone, "main.go")
turn.Text("Here is the result.")
turn.Finish()
```

Packages:
- `slagent` — Thread, Turn, reply polling, markdown→mrkdwn
- `credential` — Load/Save Slack credentials, extract from desktop app
- `channel` — Resolve channel names/users, list channels

## slaude — Claude Code ↔ Slack bridge

Wraps any Claude Code session and mirrors it to a Slack thread. Works mid-session via `--resume`, not just for starting new sessions.

### Build

```bash
go build -o slaude ./cmd/slaude/
```

### Auth

```bash
slaude auth              # extract from local Slack app (default)
slaude auth --manual     # paste a token manually
```

### Usage

```bash
# Start a session mirrored to Slack
slaude -c CHANNEL -- --permission-mode plan "design the API"

# Resume an existing Claude session
slaude -c CHANNEL --resume-thread 1234567890.123456 -- --resume SESSION_ID

# DM a user
slaude -u alice -- "review this PR"

# Local only (no Slack)
slaude -- "quick question"
```

Everything after `--` is passed through to the Claude subprocess. This means slaude doesn't need to know about every Claude flag — you control `--permission-mode`, `--resume`, `--system-prompt`, etc. directly.

### slaude flags

| Flag | Description |
|------|-------------|
| `-c, --channel` | Slack channel name or ID |
| `-u, --user` | Slack user(s) for DM |
| `--resume-thread` | Slack thread TS to resume |
| `--debug` | Write debug logs |
| `[topic...]` | Positional topic arg |

### Subcommands

- `slaude auth` — set up Slack credentials
- `slaude channels` — list accessible channels
- `slaude share FILE -c CHANNEL` — post a plan file to Slack
- `slaude status` — show current configuration

## slagent-demo

Demo CLI for testing slagent Slack UI features without Claude.

```bash
go build -o slagent-demo ./cmd/slagent-demo/
slagent-demo -c CHANNEL full      # combined demo
slagent-demo -c CHANNEL thinking  # thinking animation
slagent-demo -c CHANNEL tools     # tool activity
```

## Authentication

slaude supports three token types:

### Session token (recommended)

Extract from your local Slack desktop app — no admin approval needed:

```bash
slaude auth --extract
```

Reads the `xoxc-` session token and `xoxd-` cookie from Slack's local storage. On macOS you'll see a keychain access prompt.

### Bot token (xoxb-)

Create a Slack app at https://api.slack.com/apps with scopes: `chat:write`, `channels:history`, `groups:history`, `channels:read`, `groups:read`, `users:read`.

### User token (xoxp-)

Same app setup as bot tokens, using User Token Scopes.

## Platform Support

| Platform | Token extraction | Session mirroring |
|----------|-----------------|-------------------|
| macOS    | Yes             | Yes               |
| Linux    | Yes             | Yes               |
| Windows  | No              | Yes (manual auth) |
