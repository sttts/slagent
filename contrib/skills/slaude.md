# Skill: slaude — Slack-Mirrored Claude Code Sessions

## When to Use

Use this skill when asked to start, stop, join, or manage Claude Code sessions mirrored to Slack via `slaude`. Typical triggers:
- "Start a Claude session in #channel"
- "Mirror this to Slack"
- "Join that Slack thread with a new agent"
- "Resume the session"
- "Stop/quit the session"
- "Kill this session" / "End the session" / "Stop yourself"
- "List running sessions" / "What sessions are active"

## Prerequisites

- `slaude` binary must be installed and in PATH (or built locally from the slagent repo)
- Slack credentials configured via `slaude auth`
- Target Slack channel must be accessible to the authenticated user

Verify setup:
```bash
slaude status
```

## Starting a Session

Start a new Claude Code session mirrored to a Slack thread:

```bash
slaude start -c CHANNEL -- "your prompt here"
```

**Key flags:**
| Flag | Description |
|------|-------------|
| `-c CHANNEL` | Slack channel name or ID (required unless using `-u`) |
| `-u USER` | DM a user instead of posting to a channel |
| `--locked` | Lock to owner only (default for `start`) |
| `--observe` | Observe mode: read all messages, only respond to authorized users |
| `--open` | Open for all thread participants |
| `--debug` | Write raw JSON events for troubleshooting |
| `--no-bye` | Skip goodbye message on exit |
| `--dangerous-auto-approve green` | Auto-approve read-only operations |
| `--dangerous-auto-approve yellow` | Auto-approve local writes too |
| `--dangerous-auto-approve-network known` | Auto-approve network to known hosts (GitHub, npm, Go proxy, etc.) |
| `--dangerous-auto-approve-network any` | Auto-approve all network access |

**Access mode flags** (`--locked`, `--observe`, `--open`) are mutually exclusive.
When no flag is given — interactive terminal prompts `Closed, oBserve, or open? [cBo]`; non-interactive: `start` defaults to `--locked`, `join`/`resume` default to `--observe`.

Everything after `--` passes directly to Claude Code. Use this for Claude-specific flags:
```bash
slaude start -c CHANNEL -- --permission-mode plan "design the API"
slaude start -c CHANNEL -- --model sonnet "quick fix"
```

**Process management:** `slaude start` runs as a foreground process. It blocks until the session ends. To manage it as a sub-agent:
- The process PID can be captured: `slaude start ... & echo $!`
- Send `SIGINT` to gracefully stop: `kill -INT $PID`
- The process exits with code 0 on normal completion

## Session Management

### List running sessions

```bash
slaude ps
```

Prints a table of all active slaude sessions on this machine:

```
PID     EMOJI                 CHANNEL           UPTIME
------  --------------------  ----------------  ------
12345   :fox_face: (fox)      #engineering      5m30s
67890   :dog:                 #backend          1m12s
```

### Kill a session

```bash
slaude kill fox           # by emoji shortcode
slaude kill :fox_face:    # by emoji with colons
slaude kill 12345         # by PID
```

Sends SIGINT to the target session. First SIGINT interrupts the current Claude turn; the session then waits for new input or exits gracefully.

**Self-termination:** A running slaude session can kill itself by running `slaude kill` with its own emoji or PID. This is useful when Claude is asked to "stop", "quit", or "end the session" and wants to act autonomously:

```bash
# Claude finds its own session and kills it
MY_EMOJI=$(slaude ps | awk 'NR>2 && $2 ~ /fox/ {print $2}')
slaude kill "$MY_EMOJI"
```

Or more directly, if you know the emoji:
```bash
slaude kill fox
```

## Stopping a Session

There are four ways to stop a running session:

### 1. From Slack (recommended)
Type in the Slack thread:
- `quit` — ends the session (owner only)
- `stop` — interrupts the current turn but keeps the session alive (anyone)
- `:emoji:: quit` — quit a specific instance in multi-agent threads

### 2. Via slaude kill (for self-termination or remote stop)
```bash
slaude kill fox       # stop session with emoji "fox"
slaude kill 12345     # stop session by PID
```
Use `slaude ps` first to find the target emoji or PID.

### 3. Via signal
Send SIGINT directly to the slaude process:
```bash
kill -INT $PID
```
First SIGINT interrupts the current Claude turn. The session then waits for new input or exits.

### 4. Close stdin
If running interactively, Ctrl+C or closing stdin will end the session.

## Joining an Existing Thread

Add a new agent instance to an existing Slack thread:
```bash
slaude join https://team.slack.com/archives/C123/p1234567890 "help with tests"
```
- Each instance gets a unique emoji identity (e.g. fox, dog, koala)
- Defaults to `--observe` (non-interactive): reads all messages, responds only to authorized users
- Use `--locked` to start locked to owner only; `--open` to open for everyone

## Resuming a Session

Resume a previously exited session:
```bash
slaude resume https://team.slack.com/archives/C123/p1234567890#fox@1700000005.000000 -- --resume SESSION_ID
```
The resume URL (including `#instanceID@lastTS` fragment) and session ID are printed when a session exits.

## Multi-Instance Threads

Multiple slaude instances can share one Slack thread. Each gets a unique emoji.

**Addressing instances from Slack:**
- `:fox_face:: do something` — addressed to the fox instance only
- `:dog:: /compact` — send a command to the dog instance only
- Messages without prefix — broadcast to all instances

**Important:** The trailing colon after the emoji is required. Type `:shortcode::` in Slack.

## Thread Access Control

| Command | Effect |
|---------|--------|
| `:emoji:: /open` | Open thread for everyone |
| `:emoji:: /open @user1 @user2` | Allow specific users (additive) |
| `:emoji:: /lock` | Lock to owner only (resets all, disables observe) |
| `:emoji:: /lock @user` | Ban a specific user |
| `:emoji:: /observe` | Toggle observe mode on/off |

**Observe mode** — an orthogonal flag on top of locked/open/selective access:
- **Off** (default): non-authorized messages filtered out, agent ignores them
- **On**: all messages delivered for passive learning; agent still only responds to authorized users; non-authorized users get an ephemeral "not authorized" if they try to interact directly

Thread title encodes access state:
- `🔒🧵 Topic` — locked (owner only)
- `👀🧵 Topic` — observe (locked + reading all)
- `🧵 Topic` — open for all
- `🧵 @user1 @user2 Topic` — selective (specific users)
- `👀🧵 @user1 @user2 Topic` — selective + observe

`/lock` and `/open` (without args) disable observe. Each instance manages access independently — `/open` and `/lock` on a joined/resumed instance only affect that instance's in-memory state.

## Sub-Agent Management (for OpenClaw)

When managing slaude sessions as sub-agents from OpenClaw:

### Lifecycle Pattern

```bash
# 1. Start session as background process, capture PID
slaude start -c "$CHANNEL" --open \
  --dangerous-auto-approve green \
  --dangerous-auto-approve-network known \
  -- "$PROMPT" &
SLAUDE_PID=$!

# 2. Monitor — process exits when session completes
wait $SLAUDE_PID
EXIT_CODE=$?

# 3. Or stop early via signal
kill -INT $SLAUDE_PID
```

### Key Integration Points

1. **No external API** — slaude is process-local. Control is via:
   - Slack messages in the thread (`quit`, `stop`)
   - Unix signals to the process (`SIGINT`)
   - `slaude kill <emoji|PID>` — from the orchestrator or from within the session itself

2. **Session output** — On exit, slaude prints resume info to stderr:
   ```
   To resume: slaude resume URL#id@ts -- --resume SESSION_ID
   ```
   Capture stderr to extract session ID and resume URL for later resumption.

3. **Thread URL** — Printed to stderr on startup. Capture to track which Slack thread maps to which sub-agent.

4. **Multiple agents per thread** — Use `slaude join` to add more agents to the same thread. Each gets an independent emoji identity.

5. **Auto-approve for unattended operation** — For sub-agents running without human oversight, consider:
   ```bash
   --dangerous-auto-approve yellow \
   --dangerous-auto-approve-network known
   ```
   This auto-approves local reads/writes and known-host network access. Unknown hosts and destructive operations still require Slack approval.

### Orchestration Example

```bash
# Start agent A on backend work
slaude start -c engineering --open -- "implement the API endpoint for /users" &
PID_A=$!

# Start agent B on frontend work in the same thread
THREAD_URL="https://team.slack.com/archives/C123/p1234567890"
slaude join "$THREAD_URL" "build the React component for user list" &
PID_B=$!

# Wait for both to finish
wait $PID_A $PID_B
```

### Health Checking

```bash
# Check if session is still running (via slaude ps)
slaude ps

# Or check by PID directly
kill -0 $SLAUDE_PID 2>/dev/null && echo "running" || echo "exited"
```

## Troubleshooting

| Issue | Fix |
|-------|-----|
| `slaude: command not found` | Install via `brew install sttts/slagent/slaude` or `go install github.com/sttts/slagent/cmd/slaude@latest` |
| No channels listed | Run `slaude auth` to set up credentials |
| Permission requests stuck | Check Slack thread for ✅/❌ reaction prompts |
| Wrong emoji syntax | Use `:shortcode::` (two colons after shortcode), not Unicode emoji |
| Session won't quit | Only the session owner can `quit`. Others can only `stop` (interrupt). |
