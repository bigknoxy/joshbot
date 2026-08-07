// Package mcp implements a minimal client for the Model Context Protocol (MCP)
// over the stdio transport: JSON-RPC 2.0 messages exchanged, one per line, with
// the stdin/stdout of a spawned server process.
//
// The package is deliberately free of any dependency on joshbot's tool registry
// so that the registry can wrap it without an import cycle. It speaks the
// protocol and manages process lifecycle; adapting a discovered MCP tool to the
// agent's Tool interface happens in the tools package.
//
// Scope: stdio transport only (what Gemini CLI and Goose use). HTTP/SSE
// transports are intentionally not implemented — see SECURITY.md for the trust
// model and the package README for what was deferred.
package mcp

import "encoding/json"

// protocolVersion is the MCP revision this client advertises during the
// initialize handshake. Servers that speak a newer revision are expected to
// respond with their own version; we do not renegotiate.
const protocolVersion = "2024-11-05"

// jsonrpcVersion is the fixed JSON-RPC version string every message carries.
const jsonrpcVersion = "2.0"

// rpcRequest is an outgoing JSON-RPC request. ID is omitted for notifications.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcResponse is an incoming JSON-RPC response or notification. A message with
// a nil ID is a notification and is ignored by the call dispatcher.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is the JSON-RPC error object.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Message
}

// initializeParams is sent as the params of the initialize request.
type initializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      clientInfo     `json:"clientInfo"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ToolInfo describes a tool advertised by an MCP server via tools/list.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// listToolsResult is the result of a tools/list call.
type listToolsResult struct {
	Tools []ToolInfo `json:"tools"`
}

// callToolParams is the params of a tools/call request.
type callToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// callToolResult is the result of a tools/call request. Content is a list of
// typed blocks; this client renders the text blocks and reports isError.
type callToolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
