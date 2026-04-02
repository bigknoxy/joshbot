// Package mcp provides an MCP (Model Context Protocol) HTTP client
// for connecting to MCP servers and discovering/calling tools.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// MCPTool represents a tool discovered from an MCP server.
type MCPTool struct {
	ServerName  string
	Name        string
	Description string
	InputSchema json.RawMessage
}

// MCPResult represents the result of calling an MCP tool.
type MCPResult struct {
	Content []MCPContent `json:"content"`
	IsError bool         `json:"isError"`
}

// MCPContent represents a content item in an MCP result.
type MCPContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// Server represents a connected MCP server.
type Server struct {
	Name      string
	URL       string
	Tools     []MCPTool
	Headers   map[string]string
	client    *http.Client
	sessionID string
	nextID    atomic.Int64
	mu        sync.RWMutex
}

// NewServer creates a new MCP server connection.
func NewServer(name, url string, headers map[string]string) *Server {
	return &Server{
		Name:    name,
		URL:     url,
		Headers: headers,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Connect initializes the MCP session and discovers available tools.
func (s *Server) Connect(ctx context.Context) error {
	// Send initialize request
	initReq := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      s.nextID.Add(1),
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "joshbot",
				"version": "1.18.0",
			},
		},
	}

	resp, err := s.sendRequest(ctx, initReq)
	if err != nil {
		return fmt.Errorf("MCP initialize failed: %w", err)
	}

	// Extract session ID if provided (for streamable HTTP)
	if sessionID := resp.Header.Get("Mcp-Session-Id"); sessionID != "" {
		s.mu.Lock()
		s.sessionID = sessionID
		s.mu.Unlock()
	}

	// Send initialized notification
	notif := jsonRPCNotification{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	if err := s.sendNotification(ctx, notif); err != nil {
		// Notification failure is non-fatal
		_ = err
	}

	// Discover tools
	if err := s.discoverTools(ctx); err != nil {
		return fmt.Errorf("MCP discover tools failed: %w", err)
	}

	return nil
}

// discoverTools fetches available tools from the MCP server.
func (s *Server) discoverTools(ctx context.Context) error {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      s.nextID.Add(1),
		Method:  "tools/list",
		Params:  map[string]any{},
	}

	respBody, err := s.sendRequestJSON(ctx, req)
	if err != nil {
		return err
	}

	var result struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("failed to parse tools list: %w", err)
	}

	s.Tools = make([]MCPTool, 0, len(result.Tools))
	for _, t := range result.Tools {
		s.Tools = append(s.Tools, MCPTool{
			ServerName:  s.Name,
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}

	return nil
}

// CallTool calls a tool on the MCP server.
func (s *Server) CallTool(ctx context.Context, toolName string, args map[string]any) (*MCPResult, error) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      s.nextID.Add(1),
		Method:  "tools/call",
		Params: map[string]any{
			"name":      toolName,
			"arguments": args,
		},
	}

	respBody, err := s.sendRequestJSON(ctx, req)
	if err != nil {
		return nil, err
	}

	var result MCPResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse tool result: %w", err)
	}

	return &result, nil
}

// GetTools returns the discovered tools.
func (s *Server) GetTools() []MCPTool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Tools
}

// sendRequest sends a JSON-RPC request and returns the HTTP response.
func (s *Server) sendRequest(ctx context.Context, req jsonRPCRequest) (*http.Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", s.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	s.mu.RLock()
	for k, v := range s.Headers {
		httpReq.Header.Set(k, v)
	}
	if s.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", s.sessionID)
	}
	s.mu.RUnlock()

	return s.client.Do(httpReq)
}

// sendRequestJSON sends a JSON-RPC request and returns the parsed result.
func (s *Server) sendRequestJSON(ctx context.Context, req jsonRPCRequest) (json.RawMessage, error) {
	resp, err := s.sendRequest(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("MCP server error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var jsonResp jsonRPCResponse
	if err := json.Unmarshal(respBody, &jsonResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if jsonResp.Error != nil {
		return nil, fmt.Errorf("MCP error: %s (code: %d)", jsonResp.Error.Message, jsonResp.Error.Code)
	}

	return jsonResp.Result, nil
}

// sendNotification sends a JSON-RPC notification (no response expected).
func (s *Server) sendNotification(ctx context.Context, notif jsonRPCNotification) error {
	body, err := json.Marshal(notif)
	if err != nil {
		return fmt.Errorf("failed to marshal notification: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", s.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create notification request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	s.mu.RLock()
	for k, v := range s.Headers {
		httpReq.Header.Set(k, v)
	}
	s.mu.RUnlock()

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// JSON-RPC types

type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonRPCNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Manager manages multiple MCP server connections.
type Manager struct {
	servers []*Server
	mu      sync.RWMutex
}

// NewManager creates a new MCP manager.
func NewManager() *Manager {
	return &Manager{}
}

// AddServer adds and connects to an MCP server.
func (m *Manager) AddServer(ctx context.Context, name, url string, headers map[string]string) error {
	server := NewServer(name, url, headers)
	if err := server.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect to MCP server %s: %w", name, err)
	}

	m.mu.Lock()
	m.servers = append(m.servers, server)
	m.mu.Unlock()

	return nil
}

// GetAllTools returns tools from all connected servers.
func (m *Manager) GetAllTools() []MCPTool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var all []MCPTool
	for _, s := range m.servers {
		all = append(all, s.GetTools()...)
	}
	return all
}

// CallTool finds and calls a tool by its qualified name (server_toolName).
func (m *Manager) CallTool(ctx context.Context, qualifiedName string, args map[string]any) (*MCPResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, server := range m.servers {
		for _, tool := range server.GetTools() {
			if tool.ServerName+"_"+tool.Name == qualifiedName {
				return server.CallTool(ctx, tool.Name, args)
			}
		}
	}

	return nil, fmt.Errorf("tool not found: %s", qualifiedName)
}

// ServerNames returns the names of all connected servers.
func (m *Manager) ServerNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, len(m.servers))
	for i, s := range m.servers {
		names[i] = s.Name
	}
	return names
}
