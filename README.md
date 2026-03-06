# pairplan

Mirror Claude Code planning sessions to Slack threads. Your team follows along in Slack while you drive the planning session locally.

## Quick Start

```bash
# Build
go build -o pairplan ./cmd/pairplan/

# Extract credentials from your local Slack app (no Slack app installation needed)
pairplan auth --extract

# List channels to find your group chat ID
pairplan channels

# Start a planning session mirrored to Slack
pairplan start --channel C01234ABCDE --topic "API redesign"
```

## Authentication

pairplan supports three token types:

### Session token (recommended, no admin approval needed)

Extract directly from your local Slack desktop app:

```bash
pairplan auth --extract
```

This reads the `xoxc-` session token and encrypted `xoxd-` cookie from Slack's local storage. On macOS you'll see a keychain access prompt — allow it so pairplan can decrypt the cookie.

Requirements:
- Slack desktop app installed and signed in
- macOS or Linux

### Bot token (xoxb-)

Requires creating a Slack app at https://api.slack.com/apps with scopes: `chat:write`, `channels:history`, `groups:history`, `channels:read`, `groups:read`, `users:read`.

```bash
pairplan auth
# paste your xoxb-... token
```

### User OAuth token (xoxp-)

Same app setup as bot tokens, but using User Token Scopes. Also requires app installation to the workspace.

```bash
pairplan auth
# paste your xoxp-... token
```

## Commands

### `pairplan start`

Start an interactive planning session with Claude Code.

```bash
pairplan start [--channel C] [--topic "description"]
```

- `--channel`, `-c` — Slack channel ID to mirror to (omit for local-only)
- `--topic`, `-t` — Planning topic (shown in Slack thread header)
- `--permission-mode` — Claude permission mode (default: `plan`)

During a session:
- Type your messages to interact with Claude
- Team members reply in the Slack thread — their feedback is injected into the conversation
- Claude's thinking is shown live in Slack (updated every second, deleted when done)
- Long responses are auto-split into multiple messages
- Ctrl-D or Ctrl-C to end

### `pairplan auth`

Set up Slack credentials.

```bash
pairplan auth              # paste a token manually
pairplan auth --extract    # extract from local Slack app
```

### `pairplan channels`

List accessible Slack channels with their IDs. Useful for finding private group chat IDs.

```bash
pairplan channels
```

Output:
```
ID              TYPE      NAME
──────────────────────────────────────────────────
C01234ABCDE     channel   general
G09876FGHIJ     group     secret-project
D05555KLMNO     mpim      mpdm-alice--bob--you-1
```

### `pairplan share`

Post a plan file to a Slack channel for review.

```bash
pairplan share plan.md --channel C01234ABCDE
```

### `pairplan status`

Show current configuration (token type, truncated token).

## Slack Features

- **Block Kit formatting** — messages use Slack's Block Kit for clean rendering
- **Live thinking indicator** — shows Claude's thinking process in real-time, auto-deletes when done
- **Auto-split** — responses over 3000 chars are split at line boundaries into code-block chunks
- **Thread replies** — team members reply in the Slack thread, feedback is injected at turn boundaries
- **Private groups** — works with private channels, group DMs (mpim), and direct messages

## How It Works

pairplan runs Claude Code as a subprocess in `--output-format stream-json` mode, parses the event stream, displays output in the terminal, and mirrors everything to a Slack thread. Team feedback from Slack is polled every 3 seconds and injected into Claude's conversation at turn boundaries.

## Platform Support

| Platform | Token extraction | Session mirroring |
|----------|-----------------|-------------------|
| macOS    | Yes             | Yes               |
| Linux    | Yes             | Yes               |
| Windows  | No              | Yes (manual auth) |
