package integration

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/agent"
	"github.com/bigknoxy/joshbot/internal/bus"
	cfgpkg "github.com/bigknoxy/joshbot/internal/config"
	ctxpkg "github.com/bigknoxy/joshbot/internal/context"
	"github.com/bigknoxy/joshbot/internal/learning"
	"github.com/bigknoxy/joshbot/internal/memory"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/session"
)

// mockProvider records requests and returns summaries when asked.
type mockProvider struct {
	mu         sync.Mutex
	lastReq    providers.ChatRequest
	sawSummary bool
}

func (m *mockProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	m.mu.Lock()
	m.lastReq = req
	// detect if summary prompt
	for _, msg := range req.Messages {
		if strings.Contains(msg.Content, "Summarize") || strings.Contains(msg.Content, "conversation_summary") || strings.Contains(msg.Content, "<conversation_summary>") {
			m.sawSummary = true
		}
		if strings.Contains(msg.Content, "Summarize the following") {
			m.mu.Unlock()
			return &providers.ChatResponse{Choices: []providers.Choice{{Message: providers.Message{Content: "SUMMARY"}}}}, nil
		}
	}
	m.mu.Unlock()
	// default assistant reply
	return &providers.ChatResponse{Choices: []providers.Choice{{Message: providers.Message{Content: "assistant reply"}}}}, nil
}

func (m *mockProvider) ChatStream(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	return nil, nil
}
func (m *mockProvider) Transcribe(ctx context.Context, audioData []byte, prompt string) (string, error) {
	return "", nil
}
func (m *mockProvider) Name() string             { return "mock" }
func (m *mockProvider) Config() providers.Config { return providers.DefaultConfig() }

// inMemorySessionManager is a test session manager.
type inMemorySessionManager struct {
	mu    sync.Mutex
	store map[string]*session.Session
}

func newInMemSessionManager() *inMemorySessionManager {
	return &inMemorySessionManager{store: map[string]*session.Session{}}
}

func (m *inMemorySessionManager) GetOrCreate(ctx context.Context, key string) (*session.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.store[key]; ok {
		return s, nil
	}
	s := session.NewSession(key)
	m.store[key] = s
	return s, nil
}

func (m *inMemorySessionManager) Save(ctx context.Context, sess *session.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[sess.ID] = sess
	return nil
}

func (m *inMemorySessionManager) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.store, key)
	return nil
}

func TestAgent_CompressionAndConsolidation(t *testing.T) {
	ctx := context.Background()

	tmp := t.TempDir()
	cfg := cfgpkg.Defaults()
	cfg.Agents.Defaults.Workspace = tmp
	cfg.Agents.Defaults.Model = "small"
	cfg.Agents.Defaults.MaxTokens = 200

	// memory
	mem, err := memory.New(tmp)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	if err := mem.Initialize(ctx); err != nil {
		t.Fatalf("mem.Initialize: %v", err)
	}

	// provider mock
	prov := &mockProvider{}

	// budget manager with large margin to force small budget
	reg := ctxpkg.NewRegistry()
	budget := ctxpkg.NewBudgetManager(reg, 4000)
	compressor := &ctxpkg.Compressor{Provider: prov}

	// session manager
	sessMgr := newInMemSessionManager()

	// construct agent
	a := agent.NewAgent(cfg, prov, nil /*tools*/, sessMgr, nil /*logger*/, agent.WithMemoryLoader(mem), agent.WithBudgetManager(budget), agent.WithCompressor(compressor))

	// create a session with many messages to exceed budget
	// session key must match getSessionKey(msg) -> "cli:tester"
	key := "cli:tester"
	sess, _ := sessMgr.GetOrCreate(ctx, key)
	for i := 0; i < 200; i++ {
		sess.AddMessage(session.Message{Role: session.RoleUser, Content: "repeated message content to consume tokens"})
	}
	_ = sessMgr.Save(ctx, sess)

	// Call Process with an inbound message; this should trigger compression path
	msg := bus.InboundMessage{SenderID: "tester", Channel: "cli", Content: "Hello", Timestamp: time.Now()}

	// Call Process
	resp, err := a.Process(ctx, msg)
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	if resp == "" {
		t.Fatalf("expected non-empty response")
	}

	// Verify compressor triggered: mockProvider should have recorded sawSummary when it received a message containing <conversation_summary>
	prov.mu.Lock()
	saw := prov.sawSummary
	prov.mu.Unlock()
	if !saw {
		t.Fatalf("expected compressor to send summarized marker to provider; sawSummary=false")
	}

	// Now test consolidator: append history and run once
	_ = mem.AppendHistory(ctx, "Important decision: prefer X over Y")
	consolidator := learning.NewConsolidator(mem, prov, 1*time.Hour)
	if err := consolidator.RunOnce(ctx); err != nil {
		t.Fatalf("Consolidator RunOnce error: %v", err)
	}
	memText, err := mem.LoadMemory(ctx)
	if err != nil {
		t.Fatalf("LoadMemory error: %v", err)
	}
	if !strings.Contains(memText, "Consolidated Facts") {
		t.Fatalf("expected consolidated facts in MEMORY.md, got: %s", memText)
	}
}
