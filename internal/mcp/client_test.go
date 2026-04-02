package mcp

import (
	"testing"
)

func TestNewServer(t *testing.T) {
	s := NewServer("test-server", "http://localhost:8080", map[string]string{
		"Authorization": "Bearer token",
	})

	if s.Name != "test-server" {
		t.Errorf("expected name 'test-server', got %s", s.Name)
	}
	if s.URL != "http://localhost:8080" {
		t.Errorf("expected URL 'http://localhost:8080', got %s", s.URL)
	}
	if s.Headers["Authorization"] != "Bearer token" {
		t.Errorf("expected Authorization header, got %s", s.Headers["Authorization"])
	}
	if s.client == nil {
		t.Error("expected http.Client to be initialized")
	}
}

func TestGetTools_Empty(t *testing.T) {
	s := NewServer("test", "http://localhost:8080", nil)
	tools := s.GetTools()
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestManagerNewManager(t *testing.T) {
	mgr := NewManager()
	if mgr == nil {
		t.Fatal("expected non-nil manager")
	}
}

func TestManagerServerNames(t *testing.T) {
	mgr := NewManager()
	// Add server directly to avoid network call
	mgr.servers = append(mgr.servers, NewServer("a", "http://a", nil))
	mgr.servers = append(mgr.servers, NewServer("b", "http://b", nil))

	names := mgr.ServerNames()
	if len(names) != 2 {
		t.Errorf("expected 2 server names, got %d", len(names))
	}
}

func TestManagerGetAllTools(t *testing.T) {
	mgr := NewManager()
	s := NewServer("test", "http://test", nil)
	s.Tools = []MCPTool{
		{Name: "tool1", Description: "First tool"},
		{Name: "tool2", Description: "Second tool"},
	}
	mgr.servers = append(mgr.servers, s)

	tools := mgr.GetAllTools()
	if len(tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(tools))
	}
}

func TestManagerCallToolNotFound(t *testing.T) {
	mgr := NewManager()
	_, err := mgr.CallTool(nil, "nonexistent_tool", nil)
	if err == nil {
		t.Error("expected error for nonexistent tool")
	}
}

func TestJSONRPCRequestIDType(t *testing.T) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      42,
		Method:  "test",
	}
	if req.ID != 42 {
		t.Errorf("expected ID=42, got %d", req.ID)
	}
}

func TestServerNextIDAtomic(t *testing.T) {
	s := NewServer("test", "http://test", nil)

	id1 := s.nextID.Add(1)
	id2 := s.nextID.Add(1)
	id3 := s.nextID.Add(1)

	if id1 >= id2 || id2 >= id3 {
		t.Errorf("expected monotonically increasing IDs: %d, %d, %d", id1, id2, id3)
	}
}
