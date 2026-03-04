# Plan: MCP Integration

**Status:** Not Started  
**Priority:** Medium  
**Estimated Effort:** 12-16 hours (~500 LOC)  
**Impact:** High (access to external tool ecosystem)  
**Risk:** Medium (new dependency, external process management)

---

## Goal

Integrate Model Context Protocol (MCP) support into joshbot, enabling access to external tool servers without writing Go code. This allows users to leverage community-contributed tools for databases, APIs, cloud services, and more.

---

## What is MCP?

Model Context Protocol is an open standard for connecting AI assistants to external tools and data sources. Think of it as "USB for AI tools" - a standardized interface that works across different AI systems.

**Key Benefits:**
- 50+ community servers available
- No code needed to add new tools
- Standard interface across AI platforms
- Self-describing tools with JSON Schema
- Sandboxed execution

**Example MCP Servers:**
- `mcp-server-postgres` - PostgreSQL database queries
- `mcp-server-github` - GitHub API integration
- `mcp-server-filesystem` - File system operations
- `mcp-server-puppeteer` - Web scraping/screenshots
- `mcp-server-slack` - Slack messaging

---

## Implementation Design

### Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                         joshbot Agent                            │
│  ┌─────────────────────────────────────────────────────────────┐│
│  │                      Tool Registry                           ││
│  │  ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌──────────────────┐  ││
│  │  │ shell   │ │ web     │ │ filesys │ │ MCP Tool Wrapper │  ││
│  │  └─────────┘ └─────────┘ └─────────┘ └────────┬─────────┘  ││
│  └────────────────────────────────────────────────┼────────────┘│
└───────────────────────────────────────────────────┼─────────────┘
                                                    │
                                                    ▼
┌─────────────────────────────────────────────────────────────────┐
│                         MCP Manager                               │
│  ┌─────────────────────────────────────────────────────────────┐│
│  │                    Server Connections                        ││
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐       ││
│  │  │ postgres │ │ github   │ │ puppeteer│ │ custom   │       ││
│  │  │ (stdio)  │ │ (stdio)  │ │ (stdio)  │ │ (http)   │       ││
│  │  └──────────┘ └──────────┘ └──────────┘ └──────────┘       ││
│  └─────────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────────┘
```

### Components

1. **MCPManager** - Manages connections to MCP servers
2. **MCPTool** - Wrapper that exposes MCP tools as joshbot tools
3. **MCPConfig** - Configuration for MCP servers in config.json

---

## Step-by-Step Implementation

### Step 1: Add MCP dependency

```bash
go get github.com/modelcontextprotocol/go-sdk/mcp@latest
```

Update `go.mod`:
```go
require (
    github.com/modelcontextprotocol/go-sdk v1.3.0
)
```

### Step 2: Define MCP configuration types

**File:** `internal/config/config.go`

Add MCP config after ToolsConfig:

```go
// MCPServerConfig defines configuration for a single MCP server.
type MCPServerConfig struct {
    Command string            `mapstructure:"command" json:"command" yaml:"command"`           // Command to run (e.g., "mcp-server-postgres")
    Args    []string          `mapstructure:"args" json:"args,omitempty" yaml:"args,omitempty"` // Command arguments
    Env     map[string]string `mapstructure:"env" json:"env,omitempty" yaml:"env,omitempty"`    // Environment variables
    Type    string            `mapstructure:"type" json:"type,omitempty" yaml:"type,omitempty"` // "stdio" (default) or "http"
    URL     string            `mapstructure:"url" json:"url,omitempty" yaml:"url,omitempty"`    // URL for HTTP servers
    Enabled bool              `mapstructure:"enabled" json:"enabled,omitempty" yaml:"enabled,omitempty"`
}

// MCPConfig holds MCP server configurations.
type MCPConfig struct {
    Servers map[string]MCPServerConfig `mapstructure:"servers" json:"servers" yaml:"servers"`
}
```

Update ToolsConfig:
```go
// ToolsConfig holds tools configuration.
type ToolsConfig struct {
    Web                    WebToolsConfig          `mapstructure:"web" json:"web" yaml:"web"`
    Exec                   ExecConfig              `mapstructure:"exec" json:"exec" yaml:"exec"`
    RestrictToWorkspace    bool                    `mapstructure:"restrict_to_workspace" json:"restrict_to_workspace" yaml:"restrict_to_workspace"`
    ShellAllowList         []string                `mapstructure:"shell_allow_list" json:"shell_allow_list" yaml:"shell_allow_list"`
    FilesystemAllowedPaths []string                `mapstructure:"filesystem_allowed_paths" json:"filesystem_allowed_paths" yaml:"filesystem_allowed_paths"`
    ToolOutputMaxChars     int                     `mapstructure:"tool_output_max_chars" json:"tool_output_max_chars" yaml:"tool_output_max_chars"`
    MCP                    MCPConfig               `mapstructure:"mcp" json:"mcp,omitempty" yaml:"mcp,omitempty"` // NEW
}
```

Update Defaults():
```go
func Defaults() *Config {
    return &Config{
        // ... existing defaults ...
        Tools: ToolsConfig{
            // ... existing fields ...
            MCP: MCPConfig{
                Servers: map[string]MCPServerConfig{},
            },
        },
        // ...
    }
}
```

### Step 3: Create MCP Manager

**File:** `internal/mcp/manager.go`

```go
// Package mcp provides MCP (Model Context Protocol) integration for joshbot.
package mcp

import (
    "context"
    "fmt"
    "os"
    "os/exec"
    "sync"
    "time"
    
    "github.com/bigknoxy/joshbot/internal/config"
    "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerConnection represents an active MCP server connection.
type ServerConnection struct {
    Name    string
    Session *mcp.ClientSession
    Tools   []*mcp.Tool
    Info    *mcp.ServerInfo
}

// Manager manages MCP server connections and tool discovery.
type Manager struct {
    mu       sync.RWMutex
    servers  map[string]*ServerConnection
    logger   interface{ Info(msg string, args ...any); Error(msg string, args ...any) }
}

// NewManager creates a new MCP manager.
func NewManager(logger interface{ Info(msg string, args ...any); Error(msg string, args ...any) }) *Manager {
    return &Manager{
        servers: make(map[string]*ServerConnection),
        logger:  logger,
    }
}

// LoadFromConfig loads and connects to MCP servers from configuration.
func (m *Manager) LoadFromConfig(ctx context.Context, cfg config.MCPConfig) error {
    for name, serverCfg := range cfg.Servers {
        if !serverCfg.Enabled {
            continue
        }
        
        if err := m.Connect(ctx, name, serverCfg); err != nil {
            if m.logger != nil {
                m.logger.Error("Failed to connect to MCP server", "name", name, "error", err)
            }
            continue
        }
        
        if m.logger != nil {
            m.logger.Info("Connected to MCP server", "name", name)
        }
    }
    
    return nil
}

// Connect establishes a connection to an MCP server.
func (m *Manager) Connect(ctx context.Context, name string, cfg config.MCPServerConfig) error {
    m.mu.Lock()
    defer m.mu.Unlock()
    
    // Check if already connected
    if _, exists := m.servers[name]; exists {
        return fmt.Errorf("server %s already connected", name)
    }
    
    // Create client
    client := mcp.NewClient(&mcp.Implementation{
        Name:    "joshbot",
        Version: "1.0.0",
    }, nil)
    
    // Create transport based on type
    var transport mcp.Transport
    var err error
    
    switch cfg.Type {
    case "http", "sse":
        // HTTP/SSE transport
        transport = &mcp.StreamableClientTransport{
            Endpoint: cfg.URL,
        }
    default:
        // Stdio transport (default)
        cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
        
        // Set environment variables
        if len(cfg.Env) > 0 {
            cmd.Env = os.Environ()
            for k, v := range cfg.Env {
                cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
            }
        }
        
        transport = &mcp.CommandTransport{Command: cmd}
    }
    
    // Connect with timeout
    connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
    defer cancel()
    
    session, err := client.Connect(connectCtx, transport, nil)
    if err != nil {
        return fmt.Errorf("connect failed: %w", err)
    }
    
    // Get server info
    info, err := session.ServerInfo(ctx)
    if err != nil {
        session.Close()
        return fmt.Errorf("get server info failed: %w", err)
    }
    
    // List available tools
    tools, err := session.ListTools(ctx)
    if err != nil {
        session.Close()
        return fmt.Errorf("list tools failed: %w", err)
    }
    
    // Store connection
    m.servers[name] = &ServerConnection{
        Name:    name,
        Session: session,
        Tools:   tools,
        Info:    info,
    }
    
    return nil
}

// Disconnect closes a connection to an MCP server.
func (m *Manager) Disconnect(name string) error {
    m.mu.Lock()
    defer m.mu.Unlock()
    
    conn, exists := m.servers[name]
    if !exists {
        return fmt.Errorf("server %s not connected", name)
    }
    
    if err := conn.Session.Close(); err != nil {
        return err
    }
    
    delete(m.servers, name)
    return nil
}

// GetServer returns a server connection by name.
func (m *Manager) GetServer(name string) (*ServerConnection, bool) {
    m.mu.RLock()
    defer m.mu.RUnlock()
    
    conn, ok := m.servers[name]
    return conn, ok
}

// ListServers returns all connected server names.
func (m *Manager) ListServers() []string {
    m.mu.RLock()
    defer m.mu.RUnlock()
    
    names := make([]string, 0, len(m.servers))
    for name := range m.servers {
        names = append(names, name)
    }
    return names
}

// GetAllTools returns all tools from all connected servers.
func (m *Manager) GetAllTools() []*MCPToolInfo {
    m.mu.RLock()
    defer m.mu.RUnlock()
    
    var tools []*MCPToolInfo
    for serverName, conn := range m.servers {
        for _, tool := range conn.Tools {
            tools = append(tools, &MCPToolInfo{
                ServerName:  serverName,
                ToolName:    tool.Name,
                Description: tool.Description,
                Schema:      tool.InputSchema,
            })
        }
    }
    return tools
}

// ExecuteTool executes a tool on an MCP server.
func (m *Manager) ExecuteTool(ctx context.Context, serverName, toolName string, args map[string]any) (*ToolResult, error) {
    m.mu.RLock()
    conn, exists := m.servers[serverName]
    m.mu.RUnlock()
    
    if !exists {
        return nil, fmt.Errorf("server %s not connected", serverName)
    }
    
    // Call the tool
    params := &mcp.CallToolParams{
        Name:      toolName,
        Arguments: args,
    }
    
    result, err := conn.Session.CallTool(ctx, params)
    if err != nil {
        return nil, fmt.Errorf("tool call failed: %w", err)
    }
    
    // Parse result
    var output string
    for _, content := range result.Content {
        if text, ok := content.(*mcp.TextContent); ok {
            output += text.Text
        }
    }
    
    return &ToolResult{
        Output:  output,
        IsError: result.IsError,
    }, nil
}

// Close closes all server connections.
func (m *Manager) Close() error {
    m.mu.Lock()
    defer m.mu.Unlock()
    
    var errs []error
    for name, conn := range m.servers {
        if err := conn.Session.Close(); err != nil {
            errs = append(errs, fmt.Errorf("close %s: %w", name, err))
        }
    }
    
    m.servers = make(map[string]*ServerConnection)
    
    if len(errs) > 0 {
        return fmt.Errorf("errors closing servers: %v", errs)
    }
    return nil
}

// MCPToolInfo describes an MCP tool.
type MCPToolInfo struct {
    ServerName  string
    ToolName    string
    Description string
    Schema      *mcp.Schema
}

// ToolResult is the result of an MCP tool execution.
type ToolResult struct {
    Output  string
    IsError bool
}
```

### Step 4: Create MCP Tool wrapper

**File:** `internal/mcp/tool.go`

```go
package mcp

import (
    "context"
    "encoding/json"
    "fmt"
    
    "github.com/bigknoxy/joshbot/internal/tools"
    "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPTool wraps an MCP tool as a joshbot tool.
type MCPTool struct {
    manager     *Manager
    serverName  string
    toolInfo    *MCPToolInfo
}

// NewMCPTool creates a new MCP tool wrapper.
func NewMCPTool(manager *Manager, serverName string, toolInfo *MCPToolInfo) *MCPTool {
    return &MCPTool{
        manager:    manager,
        serverName: serverName,
        toolInfo:   toolInfo,
    }
}

// Name returns the tool name (prefixed with server name).
func (t *MCPTool) Name() string {
    return fmt.Sprintf("mcp_%s_%s", t.serverName, t.toolInfo.ToolName)
}

// Description returns the tool description.
func (t *MCPTool) Description() string {
    return fmt.Sprintf("[MCP/%s] %s", t.serverName, t.toolInfo.Description)
}

// Parameters returns the tool parameters from JSON schema.
func (t *MCPTool) Parameters() []tools.Parameter {
    if t.toolInfo.Schema == nil {
        return nil
    }
    
    return schemaToParameters(t.toolInfo.Schema)
}

// Execute runs the MCP tool.
func (t *MCPTool) Execute(ctx interface{}, args map[string]any) tools.ToolResult {
    // Type assert context
    var execCtx context.Context
    switch v := ctx.(type) {
    case context.Context:
        execCtx = v
    default:
        execCtx = context.Background()
    }
    
    result, err := t.manager.ExecuteTool(execCtx, t.serverName, t.toolInfo.ToolName, args)
    if err != nil {
        return tools.ToolResult{Error: err}
    }
    
    if result.IsError {
        return tools.ToolResult{
            Output: fmt.Sprintf("Tool error: %s", result.Output),
        }
    }
    
    return tools.ToolResult{Output: result.Output}
}

// schemaToParameters converts MCP JSON Schema to joshbot parameters.
func schemaToParameters(schema *mcp.Schema) []tools.Parameter {
    if schema == nil || schema.Properties == nil {
        return nil
    }
    
    var params []tools.Parameter
    
    for name, prop := range schema.Properties {
        param := tools.Parameter{
            Name:     name,
            Required: isRequired(schema.Required, name),
        }
        
        // Map JSON Schema type to ParameterType
        if propMap, ok := prop.(map[string]any); ok {
            if typ, ok := propMap["type"].(string); ok {
                param.Type = jsonTypeToParamType(typ)
            }
            if desc, ok := propMap["description"].(string); ok {
                param.Description = desc
            }
            if def, ok := propMap["default"]; ok {
                param.Default = def
            }
        }
        
        params = append(params, param)
    }
    
    return params
}

// isRequired checks if a field is in the required list.
func isRequired(required []string, name string) bool {
    for _, r := range required {
        if r == name {
            return true
        }
    }
    return false
}

// jsonTypeToParamType maps JSON Schema types to joshbot parameter types.
func jsonTypeToParamType(t string) tools.ParameterType {
    switch t {
    case "string":
        return tools.ParamString
    case "integer":
        return tools.ParamInteger
    case "number":
        return tools.ParamNumber
    case "boolean":
        return tools.ParamBoolean
    case "array":
        return tools.ParamArray
    case "object":
        return tools.ParamObject
    default:
        return tools.ParamString
    }
}
```

### Step 5: Register MCP tools with joshbot

**File:** `internal/agent/agent.go`

Update agent initialization to load MCP tools:

```go
import (
    "github.com/bigknoxy/joshbot/internal/mcp"
)

// Agent represents the core AI agent.
type Agent struct {
    // ... existing fields ...
    
    // mcpManager manages MCP server connections
    mcpManager *mcp.Manager
}

// New creates a new Agent.
func New(cfg *config.Config, opts ...Option) (*Agent, error) {
    // ... existing initialization ...
    
    // Create MCP manager
    mcpMgr := mcp.NewManager(logger)
    
    // Load MCP servers from config
    if err := mcpMgr.LoadFromConfig(context.Background(), cfg.Tools.MCP); err != nil {
        logger.Warn("Failed to load some MCP servers", "error", err)
    }
    
    // Create tool registry with MCP tools
    registry := tools.NewRegistry()
    
    // Register built-in tools
    // ... existing tool registration ...
    
    // Register MCP tools
    for _, toolInfo := range mcpMgr.GetAllTools() {
        mcpTool := mcp.NewMCPTool(mcpMgr, toolInfo.ServerName, toolInfo)
        if err := registry.Register(mcpTool); err != nil {
            logger.Warn("Failed to register MCP tool", 
                "server", toolInfo.ServerName,
                "tool", toolInfo.ToolName,
                "error", err)
        } else {
            logger.Info("Registered MCP tool",
                "server", toolInfo.ServerName,
                "tool", toolInfo.ToolName)
        }
    }
    
    agent := &Agent{
        // ... existing fields ...
        mcpManager: mcpMgr,
    }
    
    return agent, nil
}

// Close cleans up agent resources.
func (a *Agent) Close() error {
    // Close MCP connections
    if a.mcpManager != nil {
        if err := a.mcpManager.Close(); err != nil {
            a.logger.Error("Failed to close MCP manager", "error", err)
        }
    }
    
    // ... other cleanup ...
    
    return nil
}
```

### Step 6: Add example configuration

**File:** `docs/MCP-EXAMPLES.md`

```markdown
# MCP Configuration Examples

## PostgreSQL Database

```json
{
  "tools": {
    "mcp": {
      "servers": {
        "postgres": {
          "command": "mcp-server-postgres",
          "args": ["postgres://user:pass@localhost/mydb"],
          "enabled": true
        }
      }
    }
  }
}
```

Usage:
```
User: "Show me the top 10 users by activity"
joshbot: [uses mcp_postgres_query tool]
```

## GitHub Integration

```json
{
  "tools": {
    "mcp": {
      "servers": {
        "github": {
          "command": "mcp-server-github",
          "env": {
            "GITHUB_TOKEN": "ghp_xxx"
          },
          "enabled": true
        }
      }
    }
  }
}
```

Usage:
```
User: "Create an issue in my repo about the bug"
joshbot: [uses mcp_github_create_issue tool]
```

## Web Scraping (Puppeteer)

```json
{
  "tools": {
    "mcp": {
      "servers": {
        "puppeteer": {
          "command": "mcp-server-puppeteer",
          "enabled": true
        }
      }
    }
  }
}
```

Usage:
```
User: "Take a screenshot of example.com"
joshbot: [uses mcp_puppeteer_screenshot tool]
```

## Multiple Servers

```json
{
  "tools": {
    "mcp": {
      "servers": {
        "postgres": {
          "command": "mcp-server-postgres",
          "args": ["postgres://localhost/mydb"],
          "enabled": true
        },
        "github": {
          "command": "mcp-server-github",
          "env": {"GITHUB_TOKEN": "ghp_xxx"},
          "enabled": true
        },
        "slack": {
          "command": "mcp-server-slack",
          "env": {"SLACK_TOKEN": "xoxb-xxx"},
          "enabled": false
        }
      }
    }
  }
}
```

## Environment Variables

```bash
# MCP servers can be configured via environment variables
export JOSHBOT_TOOLS__MCP__SERVERS__GITHUB__COMMAND=mcp-server-github
export JOSHBOT_TOOLS__MCP__SERVERS__GITHUB__ENABLED=true
```

## Installing MCP Servers

Most MCP servers are distributed as npm packages:

```bash
# PostgreSQL
npm install -g @modelcontextprotocol/server-postgres

# GitHub
npm install -g @modelcontextprotocol/server-github

# Filesystem
npm install -g @modelcontextprotocol/server-filesystem

# Puppeteer
npm install -g @modelcontextprotocol/server-puppeteer
```

Some are available as Python packages:

```bash
# Brave Search
pip install mcp-server-brave-search

# SQLite
pip install mcp-server-sqlite
```

Check the [MCP Servers Directory](https://github.com/modelcontextprotocol/servers) for more.
```

### Step 7: Add tests

**File:** `internal/mcp/manager_test.go`

```go
package mcp

import (
    "context"
    "testing"
    "time"
    
    "github.com/bigknoxy/joshbot/internal/config"
)

// mockLogger is a test logger
type mockLogger struct{}

func (m *mockLogger) Info(msg string, args ...any)  {}
func (m *mockLogger) Error(msg string, args ...any) {}

func TestManager_LoadFromConfig(t *testing.T) {
    mgr := NewManager(&mockLogger{})
    
    // Test with empty config
    err := mgr.LoadFromConfig(context.Background(), config.MCPConfig{})
    if err != nil {
        t.Errorf("Empty config should not error: %v", err)
    }
    
    // Test with disabled server
    err = mgr.LoadFromConfig(context.Background(), config.MCPConfig{
        Servers: map[string]config.MCPServerConfig{
            "disabled": {
                Command: "nonexistent",
                Enabled: false,
            },
        },
    })
    if err != nil {
        t.Errorf("Disabled server should not error: %v", err)
    }
}

func TestManager_ListServers(t *testing.T) {
    mgr := NewManager(&mockLogger{})
    
    servers := mgr.ListServers()
    if len(servers) != 0 {
        t.Errorf("Expected empty servers, got %d", len(servers))
    }
}

func TestManager_GetServer_NotFound(t *testing.T) {
    mgr := NewManager(&mockLogger{})
    
    _, ok := mgr.GetServer("nonexistent")
    if ok {
        t.Error("Should not find nonexistent server")
    }
}

func TestManager_Disconnect_NotFound(t *testing.T) {
    mgr := NewManager(&mockLogger{})
    
    err := mgr.Disconnect("nonexistent")
    if err == nil {
        t.Error("Should error on nonexistent server")
    }
}

func TestManager_Close(t *testing.T) {
    mgr := NewManager(&mockLogger{})
    
    err := mgr.Close()
    if err != nil {
        t.Errorf("Close should not error: %v", err)
    }
}
```

**File:** `internal/mcp/tool_test.go`

```go
package mcp

import (
    "testing"
    
    "github.com/bigknoxy/joshbot/internal/tools"
)

func TestMCPTool_Name(t *testing.T) {
    tool := NewMCPTool(nil, "testserver", &MCPToolInfo{
        ToolName: "mytool",
    })
    
    name := tool.Name()
    expected := "mcp_testserver_mytool"
    if name != expected {
        t.Errorf("Name() = %q, want %q", name, expected)
    }
}

func TestMCPTool_Description(t *testing.T) {
    tool := NewMCPTool(nil, "github", &MCPToolInfo{
        ToolName:    "create_issue",
        Description: "Create a new issue",
    })
    
    desc := tool.Description()
    expected := "[MCP/github] Create a new issue"
    if desc != expected {
        t.Errorf("Description() = %q, want %q", desc, expected)
    }
}

func TestSchemaToParameters(t *testing.T) {
    // This would require importing mcp types, simplified for example
    params := []tools.Parameter{
        {Name: "repo", Type: tools.ParamString, Required: true},
        {Name: "title", Type: tools.ParamString, Required: true},
        {Name: "body", Type: tools.ParamString, Required: false},
    }
    
    if len(params) != 3 {
        t.Errorf("Expected 3 parameters, got %d", len(params))
    }
    
    if !params[0].Required {
        t.Error("First param should be required")
    }
    
    if params[2].Required {
        t.Error("Third param should not be required")
    }
}
```

---

## Verification Steps

1. **Install dependencies:**
   ```bash
   go get github.com/modelcontextprotocol/go-sdk/mcp@latest
   go mod tidy
   ```

2. **Build and test:**
   ```bash
   go build ./...
   go test ./internal/mcp/... -v
   go test ./internal/agent/... -v
   ```

3. **Install an MCP server for testing:**
   ```bash
   npm install -g @modelcontextprotocol/server-filesystem
   ```

4. **Configure MCP server:**
   ```bash
   cat > ~/.joshbot/config.json << 'EOF'
   {
     "schema_version": 5,
     "tools": {
       "mcp": {
         "servers": {
           "fs": {
             "command": "mcp-server-filesystem",
             "args": ["/tmp/test-mcp"],
             "enabled": true
           }
         }
       }
     }
   }
   EOF
   ```

5. **Test MCP tool:**
   ```bash
   joshbot agent -m "List the available MCP tools"
   joshbot agent -m "Use the MCP filesystem tool to list /tmp/test-mcp"
   ```

---

## Potential Issues

1. **Server startup time**: MCP servers may take time to start. Add connection timeout.

2. **Process management**: Stdio servers run as child processes. Ensure proper cleanup on shutdown.

3. **Error handling**: External servers may crash or hang. Add health checks and reconnection logic.

4. **Security**: MCP servers have full access to their environment. Review server trustworthiness.

5. **Resource usage**: Each MCP server = additional process. Monitor memory usage.

---

## Future Enhancements

1. **Auto-discovery**: Scan for installed MCP servers automatically
2. **Health monitoring**: Periodic ping to check server health
3. **Reconnection**: Auto-restart crashed servers
4. **Permissions**: Fine-grained control over which tools are exposed
5. **Tool filtering**: Whitelist/blacklist specific tools from servers

---

## Files Changed

| File | Changes |
|------|---------|
| `go.mod` | Add MCP SDK dependency |
| `internal/config/config.go` | Add MCPConfig, MCPServerConfig types |
| `internal/mcp/manager.go` | New file - MCP connection manager |
| `internal/mcp/tool.go` | New file - MCP tool wrapper |
| `internal/mcp/manager_test.go` | New file - Manager tests |
| `internal/mcp/tool_test.go` | New file - Tool tests |
| `internal/agent/agent.go` | Integrate MCP manager |
| `docs/MCP-EXAMPLES.md` | New file - Configuration examples |

---

## Completion Checklist

- [ ] Added MCP SDK dependency
- [ ] Created MCPConfig types
- [ ] Implemented Manager with Connect/Disconnect
- [ ] Implemented tool discovery
- [ ] Implemented ExecuteTool
- [ ] Created MCPTool wrapper
- [ ] Implemented schema conversion
- [ ] Integrated with agent
- [ ] Added unit tests
- [ ] Added configuration examples
- [ ] Verified build passes
- [ ] Verified tests pass
- [ ] Tested with real MCP server

---

## Progress Log

| Date | Status | Notes |
|------|--------|-------|
| 2026-03-03 | Not Started | Plan created |
