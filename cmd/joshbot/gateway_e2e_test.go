package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/agent"
	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/channels"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/session"
	"github.com/bigknoxy/joshbot/internal/tools"
)

// End-to-end gateway test (audit item: runGateway wiring had 0% coverage).
//
// This drives the exact production composition: a fake Telegram Bot API
// (httptest) feeds one update through the real TelegramChannel poller, onto
// the real bus, through gatewayHandler wired by buildGatewayDeps — the same
// function runGateway subscribes with — into a real Agent running a scripted
// provider with real session files, and back out through the real
// TelegramStreamer to the fake API's wire. Every gotcha of the "reply lost vs
// duplicated vs cross-delivered" class lives in this wiring, and before this
// test none of it was executed outside production.

// fakeBotAPI records every Bot API method call and serves canned responses.
type fakeBotAPI struct {
	mu      sync.Mutex
	calls   map[string][]map[string]any // method -> decoded request bodies
	updates []string                    // JSON updates served once, in order
	nextID  int
	// finals is what each sent message currently displays: sendMessage
	// creates an entry under its allocated id, editMessageText overwrites it.
	// This is the reader's view of the chat, which is what loss-vs-duplication
	// assertions must run against — a raw call count cannot tell an edit from
	// a second message.
	finals map[int]string
}

func newFakeBotAPI() *fakeBotAPI {
	return &fakeBotAPI{calls: make(map[string][]map[string]any), finals: make(map[int]string)}
}

func (f *fakeBotAPI) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		method := parts[len(parts)-1]

		body := map[string]any{}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		f.mu.Lock()
		f.calls[method] = append(f.calls[method], body)
		var pending string
		if method == "getUpdates" && len(f.updates) > 0 {
			pending = f.updates[0]
			f.updates = f.updates[1:]
		}
		f.nextID++
		id := f.nextID
		if text, ok := body["text"].(string); ok {
			switch method {
			case "sendMessage":
				f.finals[id] = text
			case "editMessageText":
				// telebot sends message_id as a string in this form.
				var mid int
				switch v := body["message_id"].(type) {
				case string:
					fmt.Sscanf(v, "%d", &mid)
				case float64:
					mid = int(v)
				}
				if mid != 0 {
					f.finals[mid] = text
				}
			}
		}
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":42,"is_bot":true,"first_name":"joshbot","username":"joshbot_test_bot"}}`)
		case "getUpdates":
			if pending != "" {
				fmt.Fprintf(w, `{"ok":true,"result":[%s]}`, pending)
				return
			}
			// Idle long poll: a short pause keeps the poller from spinning.
			time.Sleep(50 * time.Millisecond)
			fmt.Fprint(w, `{"ok":true,"result":[]}`)
		case "sendMessage", "editMessageText":
			chat := int64(99)
			fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"chat":{"id":%d,"type":"private"},"text":"x"}}`, id, chat)
		default:
			// sendChatAction, setMyCommands, deleteMyCommands, close, ...
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	})
}

func (f *fakeBotAPI) bodies(method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.calls[method]...)
}

// chatState returns the final text of every message in the chat, in id order.
func (f *fakeBotAPI) chatState() map[int]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[int]string, len(f.finals))
	for k, v := range f.finals {
		out[k] = v
	}
	return out
}

// scriptedStreamProvider streams a fixed reply as deltas, with a usage frame.
// interDelta spaces the deltas out so the turn outlives the streamer's edit
// throttle — without it the whole reply arrives inside one throttle window,
// no interim message ever reaches the wire, and the duplication assertions
// have nothing to bite on.
type scriptedStreamProvider struct {
	reply      string
	interDelta time.Duration
}

func (p *scriptedStreamProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	return &providers.ChatResponse{
		Choices: []providers.Choice{{Message: providers.Message{Role: providers.RoleAssistant, Content: p.reply}, FinishReason: "stop"}},
		Usage:   providers.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}, nil
}

func (p *scriptedStreamProvider) ChatStream(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	ch := make(chan providers.StreamChunk, 8)
	go func() {
		defer close(ch)
		half := len(p.reply) / 2
		for i, part := range []string{p.reply[:half], p.reply[half:]} {
			if i > 0 && p.interDelta > 0 {
				time.Sleep(p.interDelta)
			}
			ch <- providers.StreamChunk{Choices: []providers.StreamChoice{{Delta: providers.Message{Role: providers.RoleAssistant, Content: part}}}}
		}
		ch <- providers.StreamChunk{Choices: []providers.StreamChoice{{FinishReason: "stop"}}}
		ch <- providers.StreamChunk{Usage: &providers.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}}
	}()
	return ch, nil
}

func (p *scriptedStreamProvider) Transcribe(context.Context, []byte, string) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (p *scriptedStreamProvider) Name() string             { return "scripted" }
func (p *scriptedStreamProvider) Config() providers.Config { return providers.Config{} }

// noTools satisfies agent.ToolExecutor with an empty tool set.
type noTools struct{}

func (noTools) Execute(context.Context, string, map[string]any) (string, error) {
	return "", fmt.Errorf("no tools registered")
}
func (noTools) ExecuteWithContext(context.Context, string, map[string]any, string, string, func(tools.AsyncResult)) (tools.ToolResult, bool) {
	return tools.ToolResult{}, false
}
func (noTools) GetSchemas() []providers.Tool { return nil }

func TestGatewayEndToEnd_TelegramUpdateToWire(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end gateway test")
	}

	api := newFakeBotAPI()
	// One inbound message from allowlisted user 99 in private chat 99.
	api.updates = []string{`{"update_id":1,"message":{"message_id":7,"date":1700000000,"text":"say something bold","from":{"id":99,"is_bot":false,"first_name":"Josh","username":"josh"},"chat":{"id":99,"type":"private"}}}`}
	srv := httptest.NewServer(api.handler())
	defer srv.Close()

	// Real components, production wiring.
	msgBus := bus.NewMessageBus()
	tgChannel := channels.NewTelegramChannel(msgBus, &config.TelegramConfig{
		Enabled:   true,
		Token:     "1234567890:ABCDEFGHIJKLMNOPQRSTUVWXYZabcde",
		AllowFrom: []string{"99"},
		APIURL:    srv.URL,
	})

	sessions, err := session.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	cfg := config.Defaults()
	cfg.Agents.Defaults.Streaming = true
	// One keyed provider so /model has something to list; the scripted
	// provider below answers regardless of the configured name.
	cfg.Providers["scripted"] = config.ProviderConfig{Enabled: true, APIKey: "k", Model: "scripted-1"}
	const reply = "Here is **bold** text"
	agentInstance := agent.NewAgent(cfg, &scriptedStreamProvider{reply: reply, interDelta: 4 * time.Second}, noTools{}, sessions, nil)
	sender := tools.NewBusMessageSender(msgBus)

	// The production wiring for the inline controls too: the /model and
	// /personality pickers (#313) and the Stop button (#310), through the
	// same adapters runGateway uses.
	picker, err := tgChannel.NewPicker(pickerBackend{agentInstance})
	if err != nil {
		t.Fatalf("NewPicker: %v", err)
	}
	stops, err := tgChannel.NewStopCoordinator()
	if err != nil {
		t.Fatalf("NewStopCoordinator: %v", err)
	}
	msgBus.Subscribe("all", gatewayHandler(buildGatewayDeps(
		msgBus, agentInstance.Process, sender, tgChannel, cfg.Agents.Defaults.Streaming, nil, picker, stops)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	msgBus.Start()
	if err := tgChannel.Start(ctx); err != nil {
		t.Fatalf("telegram start: %v", err)
	}
	defer func() { _ = tgChannel.Stop() }()

	// The final edit carries the reply converted to HTML (#315): the streamer
	// authors Markdown and converts at the wire, with parse_mode HTML.
	wantHTML := "Here is <b>bold</b> text"
	deadline := time.Now().Add(15 * time.Second)
	var finalSeen bool
	for time.Now().Before(deadline) {
		for _, b := range append(api.bodies("editMessageText"), api.bodies("sendMessage")...) {
			if text, _ := b["text"].(string); text == wantHTML {
				if pm, _ := b["parse_mode"].(string); pm == "HTML" {
					finalSeen = true
				}
			}
		}
		if finalSeen {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !finalSeen {
		t.Fatalf("the HTML-converted reply never reached the wire.\nsendMessage: %v\neditMessageText: %v",
			api.bodies("sendMessage"), api.bodies("editMessageText"))
	}

	// What the reader sees, judged on the chat's *final state*: exactly one
	// message, holding exactly the complete reply. A raw call count cannot
	// catch the real-world duplication shape — an interim message left
	// dangling with partial text while the bus fallback posts the full answer
	// as a second message — but the final-state view fails it: two messages
	// carrying reply content is duplication, zero is loss, and a message whose
	// final text is a partial is a stuck stream.
	time.Sleep(300 * time.Millisecond) // allow any (wrong) duplicate publish to land
	var replyMessages []string
	for _, text := range api.chatState() {
		if strings.Contains(text, "bold") {
			replyMessages = append(replyMessages, text)
		}
	}
	if len(replyMessages) != 1 || replyMessages[0] != wantHTML {
		t.Errorf("chat final state must be exactly one message holding the complete reply, got %d: %q",
			len(replyMessages), replyMessages)
	}

	// The Stop button rode the interim edits and is gone from the final
	// one: a keyboard left on a finished message is a button into nothing.
	var interimWithStop, finalWithStop int
	for _, b := range append(api.bodies("sendMessage"), api.bodies("editMessageText")...) {
		rm := fmt.Sprint(b["reply_markup"])
		text, _ := b["text"].(string)
		hasStop := strings.Contains(rm, "⏹ Stop")
		switch {
		case text == wantHTML && hasStop:
			finalWithStop++
		case text != wantHTML && hasStop:
			interimWithStop++
		}
	}
	if interimWithStop == 0 || finalWithStop != 0 {
		t.Errorf("Stop button: on %d interim edits (want >0), on %d final edits (want 0)", interimWithStop, finalWithStop)
	}

	// The picker adapter hands the agent's choices to the channel unchanged.
	bare := bus.InboundMessage{Channel: "telegram", SenderID: "telegram_99", Content: "/model"}
	models, err := pickerBackend{agentInstance}.ModelChoices(ctx, bare)
	if err != nil || len(models) == 0 {
		t.Errorf("pickerBackend.ModelChoices = %v, %v", models, err)
	}
	personas, err := pickerBackend{agentInstance}.PersonalityChoices(ctx, bare)
	if err != nil || len(personas) == 0 || personas[len(personas)-1].Spec != "none" {
		t.Errorf("pickerBackend.PersonalityChoices = %v, %v", personas, err)
	}
	if out, err := (pickerBackend{agentInstance}).Process(ctx, bare); err != nil || !strings.Contains(out, "Current model") {
		t.Errorf("pickerBackend.Process(/model) = %q, %v", out, err)
	}
	if kb := picker.Keyboard(ctx, bare); kb == nil {
		t.Error("the wired picker should build a keyboard for a bare /model")
	}

	// The turn persisted to a real session file keyed telegram:telegram_99.
	sessDir := sessions.SessionsDir()
	var found bool
	entries, _ := os.ReadDir(sessDir)
	for _, e := range entries {
		if strings.Contains(e.Name(), "telegram_99") && strings.HasSuffix(e.Name(), ".jsonl") {
			data, _ := os.ReadFile(filepath.Join(sessDir, e.Name()))
			if strings.Contains(string(data), "say something bold") && strings.Contains(string(data), reply) {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("session file with the turn not found in %s (entries: %v)", sessDir, entries)
	}
}
