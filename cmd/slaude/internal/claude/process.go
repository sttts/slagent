package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

// ringBuffer is a fixed-capacity circular buffer that keeps the last N bytes
// written to it. It implements io.Writer.
type ringBuffer struct {
	buf  []byte
	size int
	pos  int
	full bool
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{buf: make([]byte, size), size: size}
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n >= r.size {
		// Data larger than buffer — just keep the tail.
		copy(r.buf, p[n-r.size:])
		r.pos = 0
		r.full = true
		return n, nil
	}
	// Write may wrap around.
	if r.pos+n <= r.size {
		copy(r.buf[r.pos:], p)
		r.pos += n
	} else {
		// Wrapping — buffer is now full.
		first := r.size - r.pos
		copy(r.buf[r.pos:], p[:first])
		copy(r.buf, p[first:])
		r.pos = n - first
		r.full = true
	}
	return n, nil
}

func (r *ringBuffer) String() string {
	if !r.full {
		return string(r.buf[:r.pos])
	}
	// Reconstruct: from pos to end, then from start to pos.
	return string(r.buf[r.pos:]) + string(r.buf[:r.pos])
}

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
	stderrBuf *ringBuffer // captures last 10KB of stderr for error reporting
	waited    sync.Once     // guards cmd.Wait to prevent double-wait
	waitErr   error         // result of cmd.Wait
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

	// Always capture the tail of stderr in a ring buffer for error reporting.
	// Also tee to the configured stderr destination (os.Stderr by default).
	stderrBuf := newRingBuffer(10 * 1024) // 10KB
	stderrDest := io.Writer(os.Stderr)
	if cfg.stderr != nil {
		stderrDest = cfg.stderr
	}
	cmd.Stderr = io.MultiWriter(stderrDest, stderrBuf)

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
		stderrBuf: stderrBuf,
	}, nil
}

// Send writes a user message to Claude's stdin.
func (p *Process) Send(content string) error {
	if err := p.stdin.Encode(inputMessage{
		Type:    "user",
		Message: userMessage{Role: "user", Content: content},
	}); err != nil {
		return p.wrapError(err)
	}
	return nil
}

// ReadEvent reads the next event from Claude's stdout.
// Returns nil, nil at EOF.
func (p *Process) ReadEvent() (*Event, error) {
	if !p.scanner.Scan() {
		if err := p.scanner.Err(); err != nil {
			return nil, p.wrapError(fmt.Errorf("read stdout: %w", err))
		}
		// EOF — check if the process died unexpectedly.
		if err := p.wrapError(nil); err != nil {
			return nil, err
		}
		return nil, nil // clean EOF
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
	p.waited.Do(func() { p.waitErr = p.cmd.Wait() })
	return p.waitErr
}

// Interrupt sends SIGINT to the Claude process, causing it to abort the current
// turn and emit a result event (like pressing Escape in the terminal).
func (p *Process) Interrupt() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Signal(syscall.SIGINT)
}

// Stop closes stdin and waits for the process to exit.
func (p *Process) Stop() error {
	p.stdinPipe.Close()
	return p.Wait()
}

// wrapError enriches an error with stderr output and exit code when the
// claude process has already exited. If origErr is nil and the process
// exited cleanly, it returns nil.
func (p *Process) wrapError(origErr error) error {
	waitErr := p.Wait()
	if waitErr == nil {
		return origErr // process exited cleanly
	}

	// Process died — build a descriptive error from stderr.
	stderr := strings.TrimSpace(p.stderrBuf.String())

	// Keep only the last few lines of stderr to avoid overwhelming output.
	if lines := strings.Split(stderr, "\n"); len(lines) > 10 {
		stderr = strings.Join(lines[len(lines)-10:], "\n")
	}

	if stderr != "" {
		return fmt.Errorf("claude process exited (%v):\n%s", waitErr, stderr)
	}
	if origErr != nil {
		return fmt.Errorf("claude process exited (%v): %w", waitErr, origErr)
	}
	return fmt.Errorf("claude process exited (%v)", waitErr)
}

// Option configures a Process.
type Option func(*config)

type config struct {
	extraArgs []string
	stderr    io.Writer
}

// WithExtraArgs appends extra arguments to the Claude command line.
func WithExtraArgs(args []string) Option {
	return func(c *config) { c.extraArgs = args }
}

// WithStderr sets the stderr writer for the Claude subprocess.
func WithStderr(w io.Writer) Option {
	return func(c *config) { c.stderr = w }
}
