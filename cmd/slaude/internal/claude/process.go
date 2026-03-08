package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Process wraps a Claude Code subprocess in stream-json mode.
type Process struct {
	cmd       *exec.Cmd
	stdin     *json.Encoder
	stdinPipe interface {
		Write([]byte) (int, error)
		Close() error
	}
	scanner   *bufio.Scanner
	sessionID string
}

// inputMessage is the JSON written to Claude's stdin.
type inputMessage struct {
	Type    string      `json:"type"`
	Message userMessage `json:"message"`
}

type userMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Start spawns Claude in stream-json mode and returns a Process.
func Start(ctx context.Context, opts ...Option) (*Process, error) {
	var cfg config
	for _, o := range opts {
		o(&cfg)
	}

	// Base args: always needed for stream-json mode
	args := []string{
		"-p",
		"--output-format=stream-json",
		"--input-format=stream-json",
		"--verbose",
		"--include-partial-messages",
	}

	// Append user-provided extra args (--permission-mode, --resume, --system-prompt, etc.)
	args = append(args, cfg.extraArgs...)

	// Strip CLAUDECODE env var to allow nested invocation
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "CLAUDECODE=") {
			env = append(env, e)
		}
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Env = env
	cmd.Stderr = os.Stderr

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	scanner := bufio.NewScanner(stdoutPipe)
	// Stream-json lines can be large (e.g. system event with tools list)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	return &Process{
		cmd:       cmd,
		stdin:     json.NewEncoder(stdinPipe),
		stdinPipe: stdinPipe,
		scanner:   scanner,
	}, nil
}

// Send writes a user message to Claude's stdin.
func (p *Process) Send(content string) error {
	return p.stdin.Encode(inputMessage{
		Type:    "user",
		Message: userMessage{Role: "user", Content: content},
	})
}

// ReadEvent reads the next event from Claude's stdout.
// Returns nil, nil at EOF.
func (p *Process) ReadEvent() (*Event, error) {
	if !p.scanner.Scan() {
		if err := p.scanner.Err(); err != nil {
			return nil, fmt.Errorf("read stdout: %w", err)
		}
		return nil, nil // EOF
	}

	line := p.scanner.Bytes()
	evt, err := ParseEvent(line)
	if err != nil {
		return nil, fmt.Errorf("parse event: %w (line: %s)", err, string(line))
	}

	// Store raw JSON for debug output
	evt.RawJSON = make([]byte, len(line))
	copy(evt.RawJSON, line)

	if evt.SessionID != "" {
		p.sessionID = evt.SessionID
	}

	return evt, nil
}

// SessionID returns the session ID assigned by Claude.
func (p *Process) SessionID() string {
	return p.sessionID
}

// Wait waits for the subprocess to exit.
func (p *Process) Wait() error {
	return p.cmd.Wait()
}

// Stop closes stdin and waits for the process to exit.
func (p *Process) Stop() error {
	p.stdinPipe.Close()
	return p.cmd.Wait()
}

// Option configures a Process.
type Option func(*config)

type config struct {
	extraArgs []string
}

// WithExtraArgs appends extra arguments to the Claude command line.
func WithExtraArgs(args []string) Option {
	return func(c *config) { c.extraArgs = args }
}
