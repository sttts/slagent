package perms

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Handler processes a permission request and returns a decision.
type Handler func(req *PermissionRequest) *PermissionResponse

// Listener accepts permission requests from the MCP server subprocess
// over a Unix socket and delegates them to a Handler.
type Listener struct {
	socketPath string
	listener   net.Listener
	handler    Handler
	wg         sync.WaitGroup
}

// NewListener creates a Unix socket listener at a temp path.
// Call Start() to begin accepting connections.
func NewListener(handler Handler) (*Listener, error) {
	dir := os.TempDir()
	socketPath := filepath.Join(dir, fmt.Sprintf("slaude-perms-%d.sock", os.Getpid()))

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", socketPath, err)
	}

	return &Listener{
		socketPath: socketPath,
		listener:   ln,
		handler:    handler,
	}, nil
}

// SocketPath returns the Unix socket path for the MCP server to connect to.
func (l *Listener) SocketPath() string {
	return l.socketPath
}

// MCPConfigFile writes the MCP config to a temp file and returns the path.
// The caller should defer os.Remove on the returned path.
func (l *Listener) MCPConfigFile(slaudeBinary string) (string, error) {
	cfg := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			MCPServerName: map[string]interface{}{
				"type":    "stdio",
				"command": slaudeBinary,
				"args":    []string{"_mcp-permissions", "--socket", l.socketPath},
			},
		},
	}
	out, _ := json.Marshal(cfg)

	f, err := os.CreateTemp("", "slaude-mcp-*.json")
	if err != nil {
		return "", fmt.Errorf("create mcp config: %w", err)
	}
	if _, err := f.Write(out); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", fmt.Errorf("write mcp config: %w", err)
	}
	f.Close()
	return f.Name(), nil
}

// PermissionToolRef returns the --permission-prompt-tool value.
func PermissionToolRef() string {
	return fmt.Sprintf("mcp__%s__%s", MCPServerName, ToolName)
}

// Start begins accepting connections in a goroutine.
func (l *Listener) Start() {
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		for {
			conn, err := l.listener.Accept()
			if err != nil {
				return // listener closed
			}
			l.wg.Add(1)
			go func() {
				defer l.wg.Done()
				l.handleConn(conn)
			}()
		}
	}()
}

// Stop closes the listener and cleans up the socket file.
func (l *Listener) Stop() {
	l.listener.Close()
	l.wg.Wait()
	os.Remove(l.socketPath)
}

func (l *Listener) handleConn(conn net.Conn) {
	defer conn.Close()

	// Set a generous read deadline — permission decisions can take minutes
	conn.SetReadDeadline(time.Now().Add(10 * time.Minute))

	var req PermissionRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		resp := PermissionResponse{Behavior: "deny", Message: "invalid request"}
		json.NewEncoder(conn).Encode(resp)
		return
	}

	// Delegate to handler (this blocks until Slack reaction or timeout)
	resp := l.handler(&req)
	json.NewEncoder(conn).Encode(resp)
}
