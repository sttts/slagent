// Package perms implements an MCP stdio server for Claude Code's --permission-prompt-tool.
//
// The server runs as a subprocess (slaude _mcp-permissions) started by Claude Code.
// It communicates with the parent slaude process via a Unix socket to delegate
// permission decisions to Slack (approve/deny via reactions).
package perms

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync/atomic"
)

// RunServer runs the MCP stdio server. It reads JSON-RPC requests from stdin,
// handles MCP protocol messages, and forwards permission requests to the parent
// slaude process via the Unix socket at socketPath.
func RunServer(socketPath string) error {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	var reqID atomic.Int64

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}

		switch req.Method {
		case "initialize":
			writeResponse(req.ID, map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]interface{}{
					"tools": map[string]interface{}{},
				},
				"serverInfo": map[string]interface{}{
					"name":    "slaude-permissions",
					"version": "1.0.0",
				},
			})

		case "notifications/initialized":
			// No response needed for notifications

		case "tools/list":
			writeResponse(req.ID, map[string]interface{}{
				"tools": []interface{}{
					map[string]interface{}{
						"name":        ToolName,
						"description": "Handle permission requests for Claude Code tool usage",
						"inputSchema": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"tool_name": map[string]interface{}{
									"type":        "string",
									"description": "Name of the tool requesting permission",
								},
								"tool_use_id": map[string]interface{}{
									"type":        "string",
									"description": "Unique identifier for the tool use",
								},
								"input": map[string]interface{}{
									"type":        "object",
									"description": "Tool input parameters",
								},
							},
							"required": []string{"tool_name", "input"},
						},
					},
				},
			})

		case "tools/call":
			var params toolCallParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(req.ID, -32602, "invalid params")
				continue
			}

			// Forward to parent slaude process via Unix socket
			result, err := forwardToParent(socketPath, &params, reqID.Add(1))
			if err != nil {
				// On error, deny by default
				writeResponse(req.ID, map[string]interface{}{
					"content": []interface{}{
						map[string]interface{}{
							"type": "text",
							"text": `{"behavior":"deny","message":"` + escapeJSON(err.Error()) + `"}`,
						},
					},
				})
				continue
			}

			writeResponse(req.ID, map[string]interface{}{
				"content": []interface{}{
					map[string]interface{}{
						"type": "text",
						"text": result,
					},
				},
			})

		default:
			if req.ID != nil {
				writeError(req.ID, -32601, "method not found: "+req.Method)
			}
		}
	}

	return scanner.Err()
}

// forwardToParent sends a permission request to the parent slaude process via Unix socket
// and waits for a response.
func forwardToParent(socketPath string, params *toolCallParams, id int64) (string, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return "", fmt.Errorf("connect to slaude: %w", err)
	}
	defer conn.Close()

	// Send the permission request
	req := PermissionRequest{
		ID:       id,
		ToolName: params.Arguments.ToolName,
		Input:    params.Arguments.Input,
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return "", fmt.Errorf("send request: %w", err)
	}

	// Read the response
	var resp PermissionResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	// Format as the JSON string Claude expects
	result := map[string]interface{}{
		"behavior": resp.Behavior,
	}
	if resp.Message != "" {
		result["message"] = resp.Message
	}
	out, _ := json.Marshal(result)
	return string(out), nil
}

// ToolName is the MCP tool name registered with Claude Code.
const ToolName = "permission_prompt"

// MCPToolRef returns the full MCP tool reference for --permission-prompt-tool.
const MCPServerName = "slaude_perms"

// PermissionRequest is sent from the MCP server to the parent slaude process.
type PermissionRequest struct {
	ID       int64           `json:"id"`
	ToolName string          `json:"tool_name"`
	Input    json.RawMessage `json:"input"`
}

// PermissionResponse is sent from the parent slaude process back to the MCP server.
type PermissionResponse struct {
	Behavior string `json:"behavior"` // "allow" or "deny"
	Message  string `json:"message,omitempty"`
}

// JSON-RPC types

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type toolCallParams struct {
	Name      string        `json:"name"`
	Arguments toolCallInput `json:"arguments"`
}

type toolCallInput struct {
	ToolName  string          `json:"tool_name"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Input     json.RawMessage `json:"input"`
}

func writeResponse(id json.RawMessage, result interface{}) {
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"result":  result,
	}
	out, _ := json.Marshal(resp)
	fmt.Fprintf(os.Stdout, "%s\n", out)
}

func writeError(id json.RawMessage, code int, message string) {
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}
	out, _ := json.Marshal(resp)
	fmt.Fprintf(os.Stdout, "%s\n", out)
}

func escapeJSON(s string) string {
	b, _ := json.Marshal(s)
	// Strip the surrounding quotes
	return string(b[1 : len(b)-1])
}
