<p align="center">
  <img src="contrib/logo.png" width="50%" alt="slagent logo">
</p>

# slagent

> [!CAUTION]
> **Experimental** — slaude exposes your Claude Code session to Slack. Likely insecure. Use at your own risk.

**slagent** is a Go library for streaming agent sessions to Slack threads. **slaude** is a CLI built on slagent that mirrors [Claude Code](https://docs.anthropic.com/en/docs/claude-code) sessions to Slack — so your team can watch, comment, and steer from Slack while Claude works.

## Quick Start — slaude

### Install

```bash
# Homebrew
brew install sttts/slagent/slaude

# or build from source
go build -o slaude ./cmd/slaude/
```

### Set up Slack credentials

```bash
slaude auth              # extract from local Slack app (recommended)
slaude auth --manual     # or paste a token manually
```

### Run

```bash
# Start a Claude session mirrored to a Slack channel
slaude start -c CHANNEL -- "design the API"

# With Claude flags (everything after -- goes to Claude)
slaude start -c CHANNEL -- --permission-mode plan "refactor the auth module"

# Join an existing Slack thread (new agent instance)
slaude join https://team.slack.com/archives/C123/p1234567890 "help with tests"

# Resume a previous session (same agent instance)
slaude resume https://team.slack.com/archives/C123/p1234567890#fox -- --resume SESSION_ID

# DM a user
slaude start -u alice -- "review this PR"

# Local only (no Slack)
slaude start -- "quick question"
```

### Commands

| Command | Description |
|---------|-------------|
| `slaude start -c CHANNEL` | Start a new Slack thread with a Claude session |
| `slaude join URL [topic]` | Join an existing thread with a new agent instance |
| `slaude resume URL#id` | Resume a previous session in a Slack thread |
| `slaude auth` | Set up Slack credentials |
| `slaude channels` | List accessible channels |
| `slaude share FILE -c CHANNEL` | Post a plan file to Slack |
| `slaude status` | Show current configuration |

Everything after `--` is passed through to the Claude subprocess. This means slaude doesn't need to know about every Claude flag — you control `--permission-mode`, `--resume`, `--system-prompt`, etc. directly.

## slagent Library

slagent is the Go library that slaude is built on. Use it to build your own Slack-integrated agent UIs.

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
