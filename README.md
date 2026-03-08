<p align="center">
  <img src="contrib/logo.png" alt="slagent logo">
</p>

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
# Start a new thread mirrored to Slack
slaude start -c CHANNEL -- --permission-mode plan "design the API"

# Join an existing Slack thread (new slaude instance)
slaude join https://team.slack.com/archives/C123/p1234567890 "topic"

# Resume a previous session (same instance ID)
slaude resume https://team.slack.com/archives/C123/p1234567890#abc123 -- --resume SESSION_ID

# DM a user
slaude start -u alice -- "review this PR"

# Local only (no Slack)
slaude start -- "quick question"
```

Everything after `--` is passed through to the Claude subprocess. This means slaude doesn't need to know about every Claude flag — you control `--permission-mode`, `--resume`, `--system-prompt`, etc. directly.

### Commands

- `slaude start` — start a new Slack thread with a Claude session
- `slaude join URL` — join an existing thread with a new slaude instance
- `slaude resume URL#id` — resume an existing session in a Slack thread
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
