<p align="center">
  <img src="contrib/logo-v2.png" width="50%" alt="slagent logo">
</p>

# slagent

> [!CAUTION]
> **Experimental** — slaude exposes your Claude Code session to Slack. Likely insecure. Use at your own risk.

**slagent** is a Go library for streaming agent sessions to Slack threads. **slaude** is a CLI built on slagent that mirrors [Claude Code](https://docs.anthropic.com/en/docs/claude-code) sessions to Slack — so your team can watch, comment, and steer from Slack while Claude works.

## Quick Start — slaude

### Install

```bash
# Homebrew
brew tap sttts/slagent https://github.com/sttts/slagent
brew install sttts/slagent/slaude

# or go install
go install github.com/sttts/slagent/cmd/slaude@latest

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

# Resume a previous session (URL with cursor from exit output)
slaude resume https://team.slack.com/archives/C123/p1234567890#fox@1700000005.000000 -- --resume SESSION_ID

# DM a user
slaude start -u alice -- "review this PR"

# No channel? Interactive picker shows available channels
slaude start -- "refactor the auth module"
```

### Commands

| Command | Description |
|---------|-------------|
| `slaude start -c CHANNEL` | Start a new Slack thread with a Claude session |
| `slaude join URL [topic]` | Join an existing thread with a new agent instance |
| `slaude resume URL#id[@ts]` | Resume a previous session in a Slack thread |
| `slaude auth` | Set up Slack credentials |
| `slaude channels` | List accessible channels |
| `slaude share FILE -c CHANNEL` | Post a plan file to Slack |
| `slaude status` | Show current configuration |

Everything after `--` is passed through to the Claude subprocess. This means slaude doesn't need to know about every Claude flag — you control `--permission-mode`, `--resume`, `--system-prompt`, etc. directly.

### Multi-Instance Threads

Multiple slaude instances can share a Slack thread. Each instance gets a unique identity emoji (e.g. 🦊, 🐶). To address a specific instance, use `:shortcode::` prefix:

```
:fox_face:: focus on the auth module     →  addressed to 🦊 (others see it but ignore)
:fox_face:: /compact                     →  /command sent exclusively to 🦊
Messages without prefix                  →  broadcast to all instances
```

Regular messages with `:shortcode::` are delivered to all instances, but the system prompt tells non-targeted instances to ignore them. Commands (`/something`) are instance-exclusive — only the targeted instance receives them.

**Important:** The colon after the emoji (`🦊:`) is required. Type `:fox_face::` in Slack (which renders as `🦊:`). Without the trailing colon, slaude will post a hint with the correct syntax.

### Thread Access Control

Threads are locked to the owner by default. Use `/open` and `/lock` to control access (via `:shortcode::` targeting):

| Command | Effect |
|---------|--------|
| `:fox_face:: /open` | Open thread for everyone |
| `:fox_face:: /open <@U1> <@U2>` | Allow specific users (additive) |
| `:fox_face:: /lock` | Lock to owner only (resets all) |
| `:fox_face:: /lock <@U1>` | Ban specific users |
| `:fox_face:: /close` | Alias for `/lock` |

Start a thread open for all with `--open`:

```bash
slaude start --open -c CHANNEL -- "design the API"
```

Thread title reflects access state:
- `🔒🧵 Topic` — locked (owner only)
- `🧵 Topic` — open for all
- `🧵 @user1 @user2 Topic` — open for specific users
- `🧵 Topic (🔒 @user)` — with banned users

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
| Linux    | Untested        | Untested          |
| Windows  | No              | Untested          |

Only macOS is actively tested. Linux and Windows might work — PRs welcome.
