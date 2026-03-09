package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/alecthomas/kong"

	"github.com/sttts/slagent"
	slackchan "github.com/sttts/slagent/channel"
	slackclient "github.com/sttts/slagent/client"
	"github.com/sttts/slagent/credential"
)

var cli struct {
	Channel      string   `short:"c" help:"Slack channel name or ID." placeholder:"CHANNEL"`
	User         []string `short:"u" help:"Slack user(s) for DM." placeholder:"USER"`
	ResumeThread string   `help:"Slack thread timestamp to resume." placeholder:"THREAD_TS"`

	Thinking  ThinkingCmd  `cmd:"" help:"Demo thinking animation: 15 lines over 5s, then final text."`
	Tools     ToolsCmd     `cmd:"" help:"Demo 8 sequential tools, each running then done."`
	Count     CountCmd     `cmd:"" help:"Stream numbers 1-20 with 500ms delays."`
	Plan      PlanCmd      `cmd:"" help:"Post a hardcoded markdown plan."`
	Tasks     TasksCmd     `cmd:"" help:"Demo task list with status updates and tools."`
	Approve   ApproveCmd   `cmd:"" help:"Demo yes/no approval prompt with reaction emojis."`
	Ask       AskCmd       `cmd:"" help:"Demo multi-choice question with number reaction emojis."`
	Full      FullCmd      `cmd:"" default:"withargs" help:"Combined demo: user msg, thinking, tools, interactive, text, finish."`
}

func main() {
	ctx := kong.Parse(&cli,
		kong.Name("slagent-demo"),
		kong.Description("Demo CLI for slagent Slack UI features."),
		kong.UsageOnError(),
	)
	if err := ctx.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// setupThread creates a slagent.Thread using shared CLI flags.
func setupThread(channel string, users []string, resumeTS string) (*slagent.Thread, error) {
	creds, err := credential.Load("")
	if err != nil {
		return nil, err
	}

	// Resolve channel/user
	sc := slackclient.New(creds.EffectiveToken(), creds.Cookie)
	ch := channel
	if len(users) > 0 {
		resolver, err := slackchan.New(sc)
		if err != nil {
			return nil, err
		}
		ch, err = resolver.ResolveUserChannel(users...)
		if err != nil {
			return nil, fmt.Errorf("resolving user: %w", err)
		}
	} else if ch != "" && !isSlackID(ch) {
		resolver, err := slackchan.New(sc)
		if err != nil {
			return nil, err
		}
		ch, err = resolver.ResolveChannelByName(strings.TrimPrefix(ch, "#"))
		if err != nil {
			return nil, fmt.Errorf("resolving channel: %w", err)
		}
	}
	if ch == "" {
		return nil, fmt.Errorf("no channel specified (-c or -u required)")
	}

	// Resolve owner for interactive prompts
	var opts []slagent.ThreadOption
	resp, err := sc.AuthTest()
	if err == nil && resp.UserID != "" {
		opts = append(opts, slagent.WithOwner(resp.UserID))
	}

	thread := slagent.NewThread(sc, ch, opts...)

	if resumeTS != "" {
		thread.Resume(resumeTS)
		fmt.Printf("Resumed thread %s\n", resumeTS)
	} else {
		url, err := thread.Start("slagent-demo")
		if err != nil {
			return nil, err
		}
		fmt.Printf("Thread: %s\n", url)
	}

	return thread, nil
}

// ThinkingCmd demos thinking animation.
type ThinkingCmd struct{}

func (cmd *ThinkingCmd) Run() error {
	thread, err := setupThread(cli.Channel, cli.User, cli.ResumeThread)
	if err != nil {
		return err
	}

	turn := thread.NewTurn()

	// 15 thinking lines over 5s
	for i := 1; i <= 15; i++ {
		turn.Thinking(fmt.Sprintf("Considering approach %d... analyzing trade-offs and implications\n", i))
		time.Sleep(333 * time.Millisecond)
	}

	turn.Text("After careful consideration, the best approach is to use a modular architecture with clear separation of concerns.")
	return turn.Finish()
}

// ToolsCmd demos sequential tool calls.
type ToolsCmd struct{}

func (cmd *ToolsCmd) Run() error {
	thread, err := setupThread(cli.Channel, cli.User, cli.ResumeThread)
	if err != nil {
		return err
	}

	turn := thread.NewTurn()

	tools := []struct{ name, detail string }{
		{"Read", "pkg/slagent/thread.go"},
		{"Glob", "**/*.go"},
		{"Grep", "func NewThread"},
		{"Bash", "go build ./..."},
		{"Edit", "cmd/main.go:42"},
		{"Write", "pkg/new_file.go"},
		{"Agent", "Exploring codebase"},
		{"WebSearch", "golang slack streaming"},
	}

	for i, t := range tools {
		id := fmt.Sprintf("tool_%d", i+1)
		turn.Tool(id, t.name, slagent.ToolRunning, t.detail)
		time.Sleep(500 * time.Millisecond)
		turn.Tool(id, t.name, slagent.ToolDone, t.detail)
	}

	turn.Text("All tools completed successfully.")
	return turn.Finish()
}

// CountCmd streams numbers 1-20.
type CountCmd struct{}

func (cmd *CountCmd) Run() error {
	thread, err := setupThread(cli.Channel, cli.User, cli.ResumeThread)
	if err != nil {
		return err
	}

	turn := thread.NewTurn()

	for i := 1; i <= 20; i++ {
		turn.Text(fmt.Sprintf("%d\n", i))
		time.Sleep(500 * time.Millisecond)
	}

	return turn.Finish()
}

// PlanCmd posts a hardcoded markdown plan.
type PlanCmd struct{}

const samplePlan = `# Implementation Plan: JWT Authentication & Error Handling

## Overview
Add JWT-based authentication with refresh token rotation, structured error
responses with request IDs, and rate limiting for auth endpoints.

## 1. Dependencies
- ` + "`github.com/golang-jwt/jwt/v5`" + ` — token signing and validation
- ` + "`golang.org/x/crypto`" + ` — bcrypt password hashing
- ` + "`github.com/google/uuid`" + ` — request ID generation

## 2. Structured Error Types (pkg/api/errors.go)
` + "```go" + `
type APIError struct {
    Code      int               ` + "`json:\"code\"`" + `
    Message   string            ` + "`json:\"message\"`" + `
    RequestID string            ` + "`json:\"request_id\"`" + `
    Details   map[string]string ` + "`json:\"details,omitempty\"`" + `
}

type ValidationError struct {
    APIError
    Fields map[string]string ` + "`json:\"fields\"`" + `
}
` + "```" + `

## 3. Auth Middleware (pkg/api/middleware.go)
- Extract Bearer token from Authorization header
- Validate JWT signature, expiration, and issuer claims
- Inject authenticated user context into request
- Return 401 with structured APIError on failure
- Respect upstream X-Request-ID header if present

## 4. JWT Service (pkg/api/jwt.go)
` + "```go" + `
type JWTService struct {
    secret     []byte
    accessTTL  time.Duration  // 15 minutes
    refreshTTL time.Duration  // 7 days
}

func (s *JWTService) GenerateTokenPair(userID string) (access, refresh string, err error)
func (s *JWTService) ValidateAccess(token string) (*Claims, error)
func (s *JWTService) RotateRefresh(oldRefresh string) (access, refresh string, err error)
` + "```" + `

## 5. Login Endpoint
` + "```go" + `
POST /api/auth/login
{ "email": "user@example.com", "password": "..." }
→ 200: { "access_token": "...", "refresh_token": "...", "expires_in": 900 }
→ 401: { "code": 401, "message": "invalid credentials", "request_id": "..." }
` + "```" + `

## 6. Token Refresh Endpoint
` + "```go" + `
POST /api/auth/refresh
Authorization: Bearer <refresh_token>
→ 200: { "access_token": "...", "refresh_token": "...", "expires_in": 900 }
→ 401: { "code": 401, "message": "refresh token expired", "request_id": "..." }
` + "```" + `

## 7. Database Migration
- Add ` + "`refresh_tokens`" + ` table: user_id, token_hash, expires_at, created_at
- Add index on token_hash for O(1) lookup
- Add index on (user_id, expires_at) for cleanup queries
- Add cleanup job: DELETE WHERE expires_at < NOW()

## 8. Rate Limiting (pkg/api/rate_limit.go)
- Token bucket per IP for /auth/login (5 req/min)
- Separate limit for /auth/refresh (10 req/min)
- Return 429 with Retry-After header

## 9. Error Recovery Middleware
- Recover from panics, log stack trace (dev mode only)
- Wrap unhandled errors in APIError with 500 status
- Always include request ID in error responses

## 10. Testing
- Unit: JWT generation, validation, expiration, rotation
- Unit: APIError serialization, ValidationError field mapping
- Unit: Rate limiter token bucket behavior
- Integration: Full login → access → refresh → access flow
- Integration: Concurrent token rotation (race condition check)
- Integration: Rate limit exhaustion and recovery
`

func (cmd *PlanCmd) Run() error {
	thread, err := setupThread(cli.Channel, cli.User, cli.ResumeThread)
	if err != nil {
		return err
	}
	return thread.PostMarkdown(samplePlan)
}

// TasksCmd demos a task list with status updates and tools.
type TasksCmd struct{}

func (cmd *TasksCmd) Run() error {
	thread, err := setupThread(cli.Channel, cli.User, cli.ResumeThread)
	if err != nil {
		return err
	}

	turn := thread.NewTurn()

	// Task 1: research
	turn.Status("Working on task 1/3: Research existing patterns")
	turn.Tool("t1", "Grep", slagent.ToolRunning, "func.*Handler")
	time.Sleep(800 * time.Millisecond)
	turn.Tool("t1", "Grep", slagent.ToolDone, "func.*Handler (12 matches)")
	turn.Tool("t2", "Read", slagent.ToolRunning, "pkg/api/handler.go")
	time.Sleep(600 * time.Millisecond)
	turn.Tool("t2", "Read", slagent.ToolDone, "pkg/api/handler.go")

	// Task 2: implement
	turn.Status("Working on task 2/3: Implement new endpoint")
	turn.Tool("t3", "Edit", slagent.ToolRunning, "pkg/api/routes.go:28")
	time.Sleep(700 * time.Millisecond)
	turn.Tool("t3", "Edit", slagent.ToolDone, "pkg/api/routes.go:28")
	turn.Tool("t4", "Write", slagent.ToolRunning, "pkg/api/auth_handler.go")
	time.Sleep(900 * time.Millisecond)
	turn.Tool("t4", "Write", slagent.ToolDone, "pkg/api/auth_handler.go")

	// Task 3: verify
	turn.Status("Working on task 3/3: Run tests")
	turn.Tool("t5", "Bash", slagent.ToolRunning, "go test ./pkg/api/...")
	time.Sleep(1200 * time.Millisecond)
	turn.Tool("t5", "Bash", slagent.ToolDone, "go test ./pkg/api/... (ok)")

	turn.Text("All 3 tasks completed:\n1. Researched existing handler patterns\n2. Added auth endpoint to routes\n3. Tests passing")
	return turn.Finish()
}

// ApproveCmd demos a yes/no approval prompt with emoji reactions.
type ApproveCmd struct{}

func (cmd *ApproveCmd) Run() error {
	thread, err := setupThread(cli.Channel, cli.User, cli.ResumeThread)
	if err != nil {
		return err
	}

	ownerID := thread.OwnerID()
	mention := ""
	if ownerID != "" {
		mention = fmt.Sprintf(" <@%s>", ownerID)
	}

	reactions := []string{"white_check_mark", "x"}
	ts, err := thread.PostPrompt(
		fmt.Sprintf("🗳️ *Claude wants to exit plan mode.*%s", mention),
		reactions,
	)
	if err != nil {
		return err
	}

	fmt.Println("Waiting for reaction (click ✅ or ❌ to respond)...")
	for {
		selected, err := thread.PollReaction(ts, reactions)
		if err != nil {
			return err
		}
		if selected != "" {
			label := "approved ✅"
			if selected == "x" {
				label = "rejected ❌"
			}
			fmt.Printf("Selection: %s (reaction: %s)\n", label, selected)
			thread.Post(fmt.Sprintf("🗳️ *Exit plan mode: %s*", label))
			return nil
		}
		time.Sleep(2 * time.Second)
	}
}

// AskCmd demos a multi-choice question with number reaction emojis.
type AskCmd struct{}

func (cmd *AskCmd) Run() error {
	thread, err := setupThread(cli.Channel, cli.User, cli.ResumeThread)
	if err != nil {
		return err
	}

	ownerID := thread.OwnerID()
	mention := ""
	if ownerID != "" {
		mention = fmt.Sprintf(" <@%s>", ownerID)
	}

	options := []string{
		"Use the existing API and extend it",
		"Build a new abstraction layer",
		"Skip for now and revisit later",
	}
	reactions := []string{"one", "two", "three"}

	var lines []string
	lines = append(lines, fmt.Sprintf("❓ *Claude asks:*%s\nWhich approach do you prefer?\n", mention))
	emojis := []string{"1️⃣", "2️⃣", "3️⃣"}
	for i, opt := range options {
		lines = append(lines, fmt.Sprintf("%s  %s", emojis[i], opt))
	}

	ts, err := thread.PostPrompt(strings.Join(lines, "\n"), reactions)
	if err != nil {
		return err
	}

	fmt.Println("Waiting for reaction (click 1️⃣, 2️⃣, or 3️⃣ to respond)...")
	for {
		selected, err := thread.PollReaction(ts, reactions)
		if err != nil {
			return err
		}
		if selected != "" {
			idx := 0
			for i, r := range reactions {
				if r == selected {
					idx = i
					break
				}
			}
			fmt.Printf("Selection: %s %s\n", emojis[idx], options[idx])
			thread.Post(fmt.Sprintf("❓ *Selected:* %s %s", emojis[idx], options[idx]))
			return nil
		}
		time.Sleep(2 * time.Second)
	}
}

// FullCmd runs the combined demo.
type FullCmd struct{}

func (cmd *FullCmd) Run() error {
	thread, err := setupThread(cli.Channel, cli.User, cli.ResumeThread)
	if err != nil {
		return err
	}

	// Gate: wait for approval before starting the demo
	ownerID := thread.OwnerID()
	mention := ""
	if ownerID != "" {
		mention = fmt.Sprintf(" <@%s>", ownerID)
	}
	startReactions := []string{"white_check_mark", "x"}
	startTS, err := thread.PostPrompt(
		fmt.Sprintf("▶️ *Ready to start demo.*%s\nClick ✅ to begin or ❌ to cancel.", mention),
		startReactions,
	)
	if err != nil {
		return err
	}

	fmt.Println("Waiting to start (click ✅ to begin)...")
	for {
		selected, pollErr := thread.PollReaction(startTS, startReactions)
		if pollErr != nil {
			return pollErr
		}
		if selected == "x" {
			fmt.Println("Cancelled.")
			return nil
		}
		if selected != "" {
			break
		}
		time.Sleep(2 * time.Second)
	}

	// Post user message
	if err := thread.PostUser("demo-user", "Can you add JWT authentication and error handling to our API server? We need proper token validation, refresh rotation, and structured error responses with request IDs."); err != nil {
		return err
	}

	// Turn 1: thinking + research
	turn := thread.NewTurn()

	thinkingLines := []string{
		"The user wants JWT authentication and structured error handling for the API.\n",
		"Let me start by understanding the current codebase structure.\n",
		"Looking at the existing handlers — they return raw errors which get swallowed by the framework.\n",
		"There's no consistent error pattern: some return 500, others panic, some silently drop errors.\n",
		"For auth, I need to decide between session-based and JWT. JWT is stateless and scales horizontally.\n",
		"Access tokens should be short-lived (15 min) with refresh tokens for rotation (7 days).\n",
		"Refresh token rotation is critical — each refresh invalidates the old token and issues a new pair.\n",
		"This prevents replay attacks if a refresh token is compromised.\n",
		"For the error types, a structured APIError with Code, Message, RequestID, and optional Details.\n",
		"ValidationError extends APIError with a Fields map for per-field frontend highlighting.\n",
		"The middleware chain should be: RequestID → RateLimit → Auth → ErrorRecovery → Handler.\n",
		"Request IDs: check for upstream X-Request-ID from nginx, generate UUID v4 if absent.\n",
		"For logging, structured JSON with request_id, path, method, status, duration.\n",
		"Stack traces only in dev mode — never leak internals in production.\n",
		"Rate limiting on auth endpoints is important to prevent brute force.\n",
		"Token bucket per IP: 5 req/min for login, 10 req/min for refresh.\n",
		"The refresh_tokens table needs cleanup: expired tokens should be purged periodically.\n",
		"I need to handle concurrent token rotation carefully — two requests with the same refresh token.\n",
		"Optimistic locking with a version column or unique constraint on token_hash should work.\n",
		"Let me now examine the existing code to plan the specific file changes.\n",
	}
	for _, line := range thinkingLines {
		turn.Thinking(line)
		time.Sleep(200 * time.Millisecond)
	}

	// Research tools
	researchTools := []struct {
		name, detail string
		sleepMS      int
	}{
		{"Glob", "pkg/api/**/*.go", 400},
		{"Grep", "func.*Handler", 600},
		{"Read", "pkg/api/handler.go", 500},
		{"Read", "pkg/api/middleware.go", 400},
		{"Read", "pkg/api/routes.go", 300},
		{"Grep", "http.Status", 500},
		{"Read", "go.mod", 300},
		{"Grep", "error.*interface", 400},
	}
	for i, t := range researchTools {
		id := fmt.Sprintf("r%d", i+1)
		turn.Tool(id, t.name, slagent.ToolRunning, t.detail)
		time.Sleep(time.Duration(t.sleepMS) * time.Millisecond)
		turn.Tool(id, t.name, slagent.ToolDone, t.detail)
	}

	turn.Text("I've analyzed the codebase. The API has 12 handlers with inconsistent error handling and no authentication. I'll write up a detailed plan.")
	turn.Finish()

	// Stream the plan line-by-line, then post the final version
	planTurn := thread.NewTurn()
	for _, line := range strings.Split(samplePlan, "\n") {
		planTurn.Text(line + "\n")
		time.Sleep(80 * time.Millisecond)
	}
	planTurn.Finish()

	// ExitPlanMode prompt — replicates Claude's actual UI
	exitOptions := []string{
		"Yes, clear context and auto-accept edits",
		"Yes, auto-accept edits",
		"Yes, manually approve edits",
	}
	exitReactions := []string{"one", "two", "three"}
	exitEmojis := []string{"1️⃣", "2️⃣", "3️⃣"}

	var promptLines []string
	promptLines = append(promptLines,
		fmt.Sprintf("🗳️ *Claude has written up a plan and is ready to execute. Would you like to proceed?*%s\n", mention),
	)
	for i, opt := range exitOptions {
		promptLines = append(promptLines, fmt.Sprintf("%s  %s", exitEmojis[i], opt))
	}
	promptLines = append(promptLines, "\n_Or reply in thread to tell Claude what to change._")

	exitTS, err := thread.PostPrompt(strings.Join(promptLines, "\n"), exitReactions)
	if err != nil {
		return err
	}

	fmt.Println("Waiting for plan approval (1️⃣ 2️⃣ 3️⃣ or reply to revise)...")
	for {
		selected, pollErr := thread.PollReaction(exitTS, exitReactions)
		if pollErr != nil {
			return pollErr
		}
		if selected != "" {
			idx := 0
			for i, r := range exitReactions {
				if r == selected {
					idx = i
					break
				}
			}
			fmt.Printf("Plan: %s %s\n", exitEmojis[idx], exitOptions[idx])
			thread.Post(fmt.Sprintf("🗳️ *Plan approved:* %s %s", exitEmojis[idx], exitOptions[idx]))
			break
		}
		time.Sleep(2 * time.Second)
	}

	// Turn 2: implementation (long, with mid-flow interactive prompts)
	turn = thread.NewTurn()

	// Phase 1: core error types + JWT + middleware
	implTools1 := []struct {
		name, detail string
		sleepMS      int
	}{
		{"Write", "pkg/api/errors.go", 800},
		{"Write", "pkg/api/jwt.go", 900},
		{"Edit", "pkg/api/middleware.go:15", 700},
		{"Edit", "pkg/api/handler.go:42", 600},
		{"Edit", "pkg/api/handler.go:87", 500},
	}
	for i, t := range implTools1 {
		id := fmt.Sprintf("i%d", i+1)
		turn.Tool(id, t.name, slagent.ToolRunning, t.detail)
		time.Sleep(time.Duration(t.sleepMS) * time.Millisecond)
		turn.Tool(id, t.name, slagent.ToolDone, t.detail)
	}

	// Mid-implementation prompt 1: rate limiting question
	turn.Finish() // finalize turn before interactive prompt
	askOptions := []string{
		"Yes, all auth endpoints",
		"No, skip rate limiting",
		"Yes, but only /login",
	}
	askReactions := []string{"one", "two", "three"}
	askEmojis := []string{"1️⃣", "2️⃣", "3️⃣"}

	var askLines []string
	askLines = append(askLines, fmt.Sprintf("❓ *Claude asks:*%s\nShould I add rate limiting to the auth endpoints?\n", mention))
	for i, opt := range askOptions {
		askLines = append(askLines, fmt.Sprintf("%s  %s", askEmojis[i], opt))
	}
	askTS, err := thread.PostPrompt(strings.Join(askLines, "\n"), askReactions)
	if err != nil {
		return err
	}

	fmt.Println("Waiting for rate limiting decision (1️⃣ 2️⃣ 3️⃣)...")
	for {
		selected, pollErr := thread.PollReaction(askTS, askReactions)
		if pollErr != nil {
			return pollErr
		}
		if selected != "" {
			idx := 0
			for i, r := range askReactions {
				if r == selected {
					idx = i
					break
				}
			}
			fmt.Printf("Rate limiting: %s %s\n", askEmojis[idx], askOptions[idx])
			thread.Post(fmt.Sprintf("❓ *Selected:* %s %s", askEmojis[idx], askOptions[idx]))
			break
		}
		time.Sleep(2 * time.Second)
	}

	// Phase 2: continue implementation
	turn = thread.NewTurn()
	implTools2 := []struct {
		name, detail string
		sleepMS      int
	}{
		{"Edit", "pkg/api/handler.go:134", 500},
		{"Edit", "pkg/api/routes.go:28", 400},
		{"Write", "pkg/api/rate_limit.go", 700},
		{"Bash", "go build ./pkg/api/...", 1000},
		{"Write", "pkg/api/errors_test.go", 900},
		{"Write", "pkg/api/jwt_test.go", 800},
	}
	for i, t := range implTools2 {
		id := fmt.Sprintf("i%d", i+6)
		turn.Tool(id, t.name, slagent.ToolRunning, t.detail)
		time.Sleep(time.Duration(t.sleepMS) * time.Millisecond)
		turn.Tool(id, t.name, slagent.ToolDone, t.detail)
	}

	// Mid-implementation prompt 2: race condition fix
	turn.Finish()
	fixReactions := []string{"white_check_mark", "x"}
	fixTS, err := thread.PostPrompt(
		fmt.Sprintf("❓ *Claude asks:*%s\nThe test suite found a race condition in concurrent token refresh. Should I fix it now or add a TODO?\n\n✅  Fix now — add mutex + retry logic\n❌  Add TODO and continue", mention),
		fixReactions,
	)
	if err != nil {
		return err
	}

	fmt.Println("Waiting for race condition decision (✅ or ❌)...")
	for {
		selected, pollErr := thread.PollReaction(fixTS, fixReactions)
		if pollErr != nil {
			return pollErr
		}
		if selected != "" {
			label := "Fix now ✅"
			if selected == "x" {
				label = "Add TODO ❌"
			}
			fmt.Printf("Race condition: %s\n", label)
			thread.Post(fmt.Sprintf("❓ *Selected:* %s", label))
			break
		}
		time.Sleep(2 * time.Second)
	}

	// Phase 3: final tools + tests
	turn = thread.NewTurn()
	implTools3 := []struct {
		name, detail string
		sleepMS      int
	}{
		{"Edit", "pkg/api/jwt.go:89", 600},
		{"Write", "pkg/api/rate_limit_test.go", 700},
		{"Bash", "go test ./pkg/api/... -v", 1500},
		{"Bash", "go vet ./pkg/api/...", 800},
	}
	for i, t := range implTools3 {
		id := fmt.Sprintf("i%d", i+12)
		turn.Tool(id, t.name, slagent.ToolRunning, t.detail)
		time.Sleep(time.Duration(t.sleepMS) * time.Millisecond)
		turn.Tool(id, t.name, slagent.ToolDone, t.detail)
	}

	// Summary text
	summaryLines := []string{
		"I've implemented the full JWT authentication and error handling plan.\n",
		"\n",
		"## Changes made\n",
		"\n",
		"**New files:**\n",
		"- `pkg/api/errors.go` — APIError and ValidationError types with request ID tracking\n",
		"- `pkg/api/jwt.go` — JWTService with token generation, validation, and refresh rotation\n",
		"- `pkg/api/rate_limit.go` — Token bucket rate limiter per IP for auth endpoints\n",
		"- `pkg/api/errors_test.go` — 23 unit tests for error serialization\n",
		"- `pkg/api/jwt_test.go` — 31 tests including concurrent rotation\n",
		"- `pkg/api/rate_limit_test.go` — 12 tests for bucket behavior and recovery\n",
		"\n",
		"**Modified files:**\n",
		"- `pkg/api/middleware.go` — Added auth, rate limit, error recovery, and request ID middleware\n",
		"- `pkg/api/handler.go` — Updated 5 handlers to return structured APIError values\n",
		"- `pkg/api/routes.go` — Wired new middleware chain and auth endpoints\n",
		"\n",
		"All 66 tests passing. `go vet` clean. The API now has proper JWT authentication ",
		"with refresh rotation, consistent structured error responses with request IDs, ",
		"and rate limiting on auth endpoints to prevent brute force attacks.\n",
	}
	for _, line := range summaryLines {
		turn.Text(line)
		time.Sleep(100 * time.Millisecond)
	}

	return turn.Finish()
}

// isSlackID returns true if s looks like a Slack channel/user ID.
func isSlackID(s string) bool {
	if len(s) < 2 {
		return false
	}
	prefix := s[0]
	return (prefix == 'C' || prefix == 'G' || prefix == 'D') && s[1] >= '0' && s[1] <= '9'
}
