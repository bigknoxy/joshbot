package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	ctxpkg "github.com/bigknoxy/joshbot/internal/context"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/session"
	"github.com/bigknoxy/joshbot/internal/skills"
)

// ---------------------------------------------------------------------------
// compaction
// ---------------------------------------------------------------------------

// recordingArchiveSessions records every Archive call so a test can prove one
// did *not* happen. mockSessionManager alone cannot: it has no Archive.
type recordingArchiveSessions struct {
	*mockSessionManager
	mu       sync.Mutex
	archived [][]session.Message
}

func (r *recordingArchiveSessions) Archive(_ context.Context, _ string, msgs []session.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]session.Message, len(msgs))
	copy(cp, msgs)
	r.archived = append(r.archived, cp)
	return nil
}

func (r *recordingArchiveSessions) archiveCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.archived)
}

// Compaction is driven from inside the ReAct loop after a tool executes. A turn
// that answers straight from the model must therefore never compact, however
// far over the budget the history already is — and, critically, must not
// silently rewrite the session either. This pins the documented boundary; a
// refactor that hoisted the check to the top of Process would pass every
// existing compaction test (they all script tool calls) and start compacting
// on plain chat turns.
func TestCompactionDoesNotRunOnATurnWithNoToolCall(t *testing.T) {
	sessions := newMockSessionManager()
	sess := seedHistory(t, sessions, "cli:eval", 12)
	before := len(sess.Messages)

	comp := &countingCompressor{}
	// No tool calls anywhere in the script: the model just answers.
	f := newCompactionFixture(t, sessions, comp, []scriptedTurn{
		{content: "answered without tools"},
		{content: "answered without tools again"},
	})

	f.turn(t, "hello")
	f.turn(t, "hello again")

	if comp.count() != 0 {
		t.Fatalf("compressor ran %d times on tool-free turns, want 0", comp.count())
	}

	got := f.session(t)
	if len(got.Messages) <= before {
		t.Fatalf("session shrank or stalled: %d messages, want more than the seeded %d", len(got.Messages), before)
	}
	for i, m := range got.Messages {
		if m.Compaction {
			t.Fatalf("message %d is a compaction record, but no compaction should have run", i)
		}
	}
}

// applyCompaction must refuse a prefix length that does not describe the
// session it is handed. Both directions are failure modes with teeth: a
// non-positive prefix would replace nothing while inserting a summary record,
// and an over-long one indexes past the live tail. In both cases the session
// must be left exactly as it was and nothing may be archived.
func TestApplyCompaction_RefusesOutOfRangePrefix(t *testing.T) {
	for _, tc := range []struct {
		name      string
		prefixLen int
	}{
		{"zero", 0},
		{"negative", -3},
		{"past the end", 99},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions := &recordingArchiveSessions{mockSessionManager: newMockSessionManager()}
			sess := seedHistory(t, sessions.mockSessionManager, "cli:eval", 3)
			before := make([]session.Message, len(sess.Messages))
			copy(before, sess.Messages)

			a := NewAgent(config.Defaults(), &scriptedProvider{}, &mockToolExecutor{}, sessions, newMockLogger())
			a.applyCompaction(context.Background(), sess, compactionState{
				summary:   "a summary",
				prefixLen: tc.prefixLen,
				active:    true,
			})

			if len(sess.Messages) != len(before) {
				t.Fatalf("session length changed from %d to %d", len(before), len(sess.Messages))
			}
			for i := range before {
				if sess.Messages[i].Content != before[i].Content || sess.Messages[i].Compaction {
					t.Fatalf("message %d was rewritten: %+v", i, sess.Messages[i])
				}
			}
			if n := sessions.archiveCalls(); n != 0 {
				t.Fatalf("Archive called %d times for a rejected compaction, want 0", n)
			}
		})
	}
}

// The inverse of the guard: a well-formed compaction must archive the exact
// messages it removes, before they leave the live session, and leave exactly
// one compaction record at index 0. Losing the archive step turns compaction
// into deletion.
func TestApplyCompaction_ArchivesExactlyWhatItReplaces(t *testing.T) {
	sessions := &recordingArchiveSessions{mockSessionManager: newMockSessionManager()}
	sess := seedHistory(t, sessions.mockSessionManager, "cli:eval", 4)
	total := len(sess.Messages)
	prefix := total - 2

	a := NewAgent(config.Defaults(), &scriptedProvider{}, &mockToolExecutor{}, sessions, newMockLogger())
	removed := make([]session.Message, prefix)
	copy(removed, sess.Messages[:prefix])

	a.applyCompaction(context.Background(), sess, compactionState{
		summary: "SUMMARY", prefixLen: prefix, active: true,
	})

	if sessions.archiveCalls() != 1 {
		t.Fatalf("Archive called %d times, want 1", sessions.archiveCalls())
	}
	archived := sessions.archived[0]
	if len(archived) != prefix {
		t.Fatalf("archived %d messages, want the %d that were replaced", len(archived), prefix)
	}
	for i := range removed {
		if archived[i].Content != removed[i].Content {
			t.Fatalf("archived[%d] = %q, want %q", i, archived[i].Content, removed[i].Content)
		}
	}

	if got := len(sess.Messages); got != total-prefix+1 {
		t.Fatalf("session has %d messages, want %d", got, total-prefix+1)
	}
	if !sess.Messages[0].Compaction || sess.Messages[0].Content == "" {
		t.Fatalf("index 0 is not the compaction record: %+v", sess.Messages[0])
	}
	for i := 1; i < len(sess.Messages); i++ {
		if sess.Messages[i].Compaction {
			t.Fatalf("a second compaction record appeared at index %d", i)
		}
	}
}

// ---------------------------------------------------------------------------
// per-request sinks
// ---------------------------------------------------------------------------

// usageProvider answers immediately and reports token usage, so the usage sink
// has something non-zero to carry.
type usageProvider struct {
	mu    sync.Mutex
	calls int
	usage providers.Usage
}

func (p *usageProvider) Chat(_ context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()

	var tcs []providers.ToolCall
	content := "done"
	if n == 1 {
		// One tool call first, so the loop makes two provider calls and the
		// sink must fire twice rather than once per Process.
		tcs = []providers.ToolCall{toolCall("t1", "noop", `{}`)}
		content = ""
	}
	return &providers.ChatResponse{
		Choices: []providers.Choice{{Message: providers.Message{
			Role: providers.RoleAssistant, Content: content, ToolCalls: tcs,
		}}},
		Usage: p.usage,
	}, nil
}

func (p *usageProvider) ChatStream(context.Context, providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	return nil, errors.New("not supported")
}
func (p *usageProvider) Transcribe(context.Context, []byte, string) (string, error) {
	return "", errors.New("not supported")
}
func (p *usageProvider) Name() string             { return "usage" }
func (p *usageProvider) Config() providers.Config { return providers.Config{} }

// Usage must be reported once per provider call, not once per Process — the
// CLI's JSON mode sums these to report the cost of a request, and a turn that
// used three tool round-trips costs three calls' worth of tokens.
func TestUsageSink_FiresOncePerProviderCall(t *testing.T) {
	InvalidatePromptCache()
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()

	prov := &usageProvider{usage: providers.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}}
	toolLayer := &recordingTools{outputs: map[string]string{"noop": "ok"}}
	a := NewAgent(cfg, prov, toolLayer, newMockSessionManager(), newMockLogger())

	var mu sync.Mutex
	var seen []providers.Usage
	ctx := WithUsageSink(context.Background(), func(u providers.Usage) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, u)
	})

	if _, err := a.Process(ctx, bus.InboundMessage{Channel: "cli", SenderID: "u", Content: "go"}); err != nil {
		t.Fatalf("Process: %v", err)
	}

	if len(seen) != prov.calls {
		t.Fatalf("usage sink fired %d times for %d provider calls", len(seen), prov.calls)
	}
	if prov.calls < 2 {
		t.Fatalf("scenario made only %d provider calls; it must make more than one to be meaningful", prov.calls)
	}
	total := 0
	for _, u := range seen {
		total += u.TotalTokens
	}
	if total != 18*prov.calls {
		t.Fatalf("summed TotalTokens = %d, want %d", total, 18*prov.calls)
	}
}

// A Process call with no usage sink installed must not panic. The loop reaches
// usageFromContext on every provider call, so a missing nil guard there would
// be a crash on every non-CLI turn.
func TestUsageSink_AbsentIsANoOp(t *testing.T) {
	if got := UsageFromContext(context.Background()); got != nil {
		t.Fatalf("UsageFromContext on a bare context = %v, want nil", got)
	}
	if got := ProgressFromContext(context.Background()); got != nil {
		t.Fatalf("ProgressFromContext on a bare context = %v, want nil", got)
	}
}

// The three sinks share one context value. Attaching one must preserve the
// others, and attaching nil must clear only its own slot — anything else
// silently disables the CLI's tool-progress lines the moment streaming is
// wired up (or vice versa).
func TestSinks_ComposeIndependently(t *testing.T) {
	ctx := context.Background()
	ctx = WithSink(ctx, func(ToolProgressEvent) {})
	ctx = WithStreamSink(ctx, func(StreamEvent) {})
	ctx = WithUsageSink(ctx, func(providers.Usage) {})

	if progressFromContext(ctx) == nil || streamSinkFromContext(ctx) == nil || usageFromContext(ctx) == nil {
		t.Fatal("attaching three sinks lost at least one of them")
	}

	// Clearing one leaves the other two.
	cleared := WithStreamSink(ctx, nil)
	if streamSinkFromContext(cleared) != nil {
		t.Error("WithStreamSink(nil) did not clear the stream sink")
	}
	if progressFromContext(cleared) == nil {
		t.Error("WithStreamSink(nil) also cleared the progress callback")
	}
	if usageFromContext(cleared) == nil {
		t.Error("WithStreamSink(nil) also cleared the usage callback")
	}

	// The parent context must be untouched: the sink is copied, not mutated,
	// because concurrent Telegram turns derive from a shared parent.
	if streamSinkFromContext(ctx) == nil {
		t.Error("clearing a sink on a derived context mutated the parent")
	}

	cleared2 := WithUsageSink(WithSink(ctx, nil), nil)
	if progressFromContext(cleared2) != nil || usageFromContext(cleared2) != nil {
		t.Error("clearing progress and usage did not take effect")
	}
	if streamSinkFromContext(cleared2) == nil {
		t.Error("clearing progress and usage also dropped the stream sink")
	}
}

// ---------------------------------------------------------------------------
// model resolution
// ---------------------------------------------------------------------------

// legacyCfg returns a legacy (provider-centric) config: no ModelsConfig, so
// UseModelsConfig() is false and the /model path takes its other branch.
func legacyCfg(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.Model = "openrouter:base-model"
	cfg.ModelsConfig = config.ModelsConfig{}
	cfg.Providers = map[string]config.ProviderConfig{
		"openrouter": {Enabled: true, APIKey: "k", Model: "openrouter/a"},
		"groq":       {Enabled: true, APIKey: "k", Model: "groq/b"},
		"disabled":   {Enabled: false, APIKey: "k", Model: "x"},
		"keyless":    {Enabled: true, APIKey: "", Model: "y"},
	}
	return cfg
}

// /model in legacy mode lists only providers that can actually serve a
// request. Listing a disabled or keyless provider invites the user to switch
// to something that fails on the next message.
func TestModelList_LegacyHidesUnusableProviders(t *testing.T) {
	InvalidatePromptCache()
	cfg := legacyCfg(t)
	a := NewAgent(cfg, &scriptedProvider{}, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())

	resp, err := a.Process(context.Background(), cmdMsg("/model"))
	if err != nil {
		t.Fatalf("Process(/model): %v", err)
	}
	for _, want := range []string{"openrouter", "groq", "Available providers:"} {
		if !strings.Contains(resp, want) {
			t.Errorf("/model list missing %q:\n%s", want, resp)
		}
	}
	for _, unwanted := range []string{"disabled", "keyless"} {
		if strings.Contains(resp, unwanted) {
			t.Errorf("/model list offers unusable provider %q:\n%s", unwanted, resp)
		}
	}
	// The active provider is marked, and it is derived from the configured
	// default rather than hardcoded.
	if !strings.Contains(resp, "> openrouter") {
		t.Errorf("active provider not marked:\n%s", resp)
	}
}

// Legacy /model must validate the spec against the configured providers. A
// switch to an unknown or disabled provider that is accepted here is persisted
// on the session and breaks every later turn.
func TestModelSpec_LegacyRejectsUnusableSpecs(t *testing.T) {
	InvalidatePromptCache()
	cfg := legacyCfg(t)
	sessions := newMockSessionManager()
	a := NewAgent(cfg, &scriptedProvider{}, &mockToolExecutor{}, sessions, newMockLogger())

	for _, tc := range []struct {
		arg     string
		wantErr bool
	}{
		{"groq:llama-3.3", false},
		{"groq", false},
		{"disabled:whatever", true},
		{"disabled", true},
		{"nosuchprovider", true},
		{"nosuchprovider:m", true},
	} {
		resp, err := a.Process(context.Background(), cmdMsg("/model "+tc.arg))
		if err != nil {
			t.Fatalf("Process(/model %s): %v", tc.arg, err)
		}
		gotErr := strings.HasPrefix(resp, "Error:")
		if gotErr != tc.wantErr {
			t.Errorf("/model %s -> %q; wantErr=%v", tc.arg, resp, tc.wantErr)
		}
	}

	// The last accepted spec is what stuck on the session.
	sess, err := sessions.GetOrCreate(context.Background(), "cli:user")
	if err != nil {
		t.Fatal(err)
	}
	if sess.ModelOverride != "groq" {
		t.Errorf("session override = %q, want the last accepted spec %q", sess.ModelOverride, "groq")
	}
}

// A session override must reach the wire: /model fast has to change the Model
// field of the next request, not just the confirmation text. It also has to
// resolve the friendly name to the concrete model ID, or the provider 404s.
func TestSessionModelOverride_ReachesTheProviderRequest(t *testing.T) {
	InvalidatePromptCache()
	cfg := modelCentricCfg(t)
	provider := &scriptedProvider{turns: []scriptedTurn{{content: "hi"}, {content: "hi"}}}
	sessions := newMockSessionManager()
	a := NewAgent(cfg, provider, &mockToolExecutor{}, sessions, newMockLogger())

	send := func(content string) {
		t.Helper()
		if _, err := a.Process(context.Background(), bus.InboundMessage{
			Channel: "cli", SenderID: "user", Content: content,
		}); err != nil {
			t.Fatalf("Process(%q): %v", content, err)
		}
	}

	// The request carries the configured *name*; the provider registry resolves
	// it to the concrete id downstream.
	send("before switch")
	if got := provider.lastRequestModel(); got != "smart" {
		t.Fatalf("default request model = %q, want the configured default \"smart\"", got)
	}

	if _, err := a.Process(context.Background(), cmdMsg("/model fast")); err != nil {
		t.Fatalf("Process(/model fast): %v", err)
	}

	send("after switch")
	if got := provider.lastRequestModel(); got != "fast" {
		t.Fatalf("request model after /model fast = %q, want the session override \"fast\"", got)
	}
}

// CurrentModel drives the interactive status bar. It reads the CLI session, so
// a per-session override on the CLI shows, and the global default shows when
// there is none.
func TestCurrentModel_TracksTheCLISession(t *testing.T) {
	InvalidatePromptCache()
	cfg := modelCentricCfg(t)
	sessions := newMockSessionManager()
	a := NewAgent(cfg, &scriptedProvider{}, &mockToolExecutor{}, sessions, newMockLogger())

	if got := a.CurrentModel(); got != "smart" {
		t.Fatalf("CurrentModel with no override = %q, want smart", got)
	}

	sess, err := sessions.GetOrCreate(context.Background(), "cli:cli_user")
	if err != nil {
		t.Fatal(err)
	}
	sess.ModelOverride = "fast"
	if err := sessions.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	if got := a.CurrentModel(); got != "fast" {
		t.Fatalf("CurrentModel with a CLI override = %q, want fast", got)
	}

	// An override on some *other* channel must not show in the CLI status bar.
	other, err := sessions.GetOrCreate(context.Background(), "telegram:999")
	if err != nil {
		t.Fatal(err)
	}
	other.ModelOverride = "smart"
	if err := sessions.Save(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if got := a.CurrentModel(); got != "fast" {
		t.Fatalf("CurrentModel = %q; another channel's override leaked into the CLI", got)
	}
}

// resolveModelName maps a configured name to its concrete id and passes
// anything else through untouched — a bare model id must not be mangled.
func TestResolveModelName_PassesUnknownSpecsThrough(t *testing.T) {
	InvalidatePromptCache()
	a := NewAgent(modelCentricCfg(t), &scriptedProvider{}, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())

	if got := a.resolveModelName("smart"); got != "nvidia/stepfun-ai/step-3.5-flash" {
		t.Errorf("resolveModelName(smart) = %q", got)
	}
	if got := a.resolveModelName("poolside/laguna-s-2.1"); got != "poolside/laguna-s-2.1" {
		t.Errorf("resolveModelName passed-through spec = %q, want it verbatim", got)
	}

	// A legacy config resolves nothing at all.
	legacy := NewAgent(legacyCfg(t), &scriptedProvider{}, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())
	if got := legacy.resolveModelName("smart"); got != "smart" {
		t.Errorf("legacy resolveModelName = %q, want the spec verbatim", got)
	}
}

// setGlobalModel / currentGlobalModel are the runtime override the /model
// --global path sets; the runtime override must win over the config default
// for every session that has no override of its own.
func TestGlobalModelOverride_WinsOverConfigDefault(t *testing.T) {
	InvalidatePromptCache()
	a := NewAgent(modelCentricCfg(t), &scriptedProvider{}, &mockToolExecutor{}, newMockSessionManager(), newMockLogger())

	if got := a.currentGlobalModel(); got != "" {
		t.Fatalf("currentGlobalModel before any override = %q, want empty", got)
	}

	a.setGlobalModel("fast")
	if got := a.currentGlobalModel(); got != "fast" {
		t.Fatalf("currentGlobalModel = %q, want fast", got)
	}
	if got := a.resolvedModelForLocked(nil); got != "groq/llama-3.3-70b-versatile" {
		t.Fatalf("resolved model after global override = %q, want the id for fast", got)
	}

	// A session override still beats the global one.
	sess := &session.Session{ModelOverride: "smart"}
	if got := a.resolvedModelFor(sess); got != "nvidia/stepfun-ai/step-3.5-flash" {
		t.Fatalf("session override lost to the global override: %q", got)
	}
}

// ---------------------------------------------------------------------------
// /personality
// ---------------------------------------------------------------------------

// A preset name must expand to its canned instruction, a custom string must be
// kept verbatim, "none" must clear, and whatever is set must actually reach the
// system prompt on the next turn — a personality stored but never injected is
// the silent failure here.
func TestPersonalityCommand_PresetCustomClearAndInjection(t *testing.T) {
	InvalidatePromptCache()
	cfg := modelCentricCfg(t)
	provider := &scriptedProvider{turns: []scriptedTurn{{content: "ok"}, {content: "ok"}}}
	sessions := newMockSessionManager()
	a := NewAgent(cfg, provider, &mockToolExecutor{}, sessions, newMockLogger())

	run := func(cmd string) string {
		t.Helper()
		resp, err := a.Process(context.Background(), cmdMsg(cmd))
		if err != nil {
			t.Fatalf("Process(%q): %v", cmd, err)
		}
		return resp
	}

	// Unset: the help text must name every preset, so a new preset is
	// discoverable without reading the source.
	empty := run("/personality")
	for name := range personalityPresets {
		if !strings.Contains(empty, name) {
			t.Errorf("/personality help omits preset %q:\n%s", name, empty)
		}
	}

	run("/personality pirate")
	sess, err := sessions.GetOrCreate(context.Background(), "cli:user")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Personality != personalityPresets["pirate"] {
		t.Fatalf("preset not expanded: %q", sess.Personality)
	}

	// It must show up in the system prompt of the next real turn.
	if _, err := a.Process(context.Background(), bus.InboundMessage{
		Channel: "cli", SenderID: "user", Content: "hello",
	}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	sys := provider.requests[len(provider.requests)-1].Messages[0]
	if sys.Role != providers.RoleSystem {
		t.Fatalf("first message role = %v, want system", sys.Role)
	}
	if !strings.Contains(sys.Content, personalityPresets["pirate"]) {
		t.Error("the personality never reached the system prompt")
	}

	// A non-preset argument is used verbatim.
	custom := "Reply only in haiku."
	run("/personality " + custom)
	sess, _ = sessions.GetOrCreate(context.Background(), "cli:user")
	if sess.Personality != custom {
		t.Fatalf("custom personality = %q, want %q", sess.Personality, custom)
	}

	// Showing the current personality echoes it back.
	if got := run("/personality"); !strings.Contains(got, custom) {
		t.Errorf("/personality did not report the current value:\n%s", got)
	}

	// "none" clears it, case-insensitively.
	run("/personality NONE")
	sess, _ = sessions.GetOrCreate(context.Background(), "cli:user")
	if sess.Personality != "" {
		t.Fatalf("personality still set after /personality NONE: %q", sess.Personality)
	}
}

// ---------------------------------------------------------------------------
// /compact
// ---------------------------------------------------------------------------

func TestCompactCommand_Paths(t *testing.T) {
	newAgent := func(t *testing.T, sessions SessionManager, comp ContextCompressor, wired bool) *Agent {
		t.Helper()
		InvalidatePromptCache()
		cfg := config.Defaults()
		cfg.Agents.Defaults.Workspace = t.TempDir()
		cfg.Agents.Defaults.Model = "small"
		opts := []Option{}
		if wired {
			opts = append(opts,
				WithBudgetManager(ctxpkg.NewBudgetManager(ctxpkg.NewRegistry(), 3800)),
				WithContextCompressor(comp),
			)
		}
		return NewAgent(cfg, &scriptedProvider{}, &mockToolExecutor{}, sessions, newMockLogger(), opts...)
	}

	t.Run("unavailable when compression is not wired", func(t *testing.T) {
		a := newAgent(t, newMockSessionManager(), nil, false)
		resp, err := a.Process(context.Background(), cmdMsg("/compact"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(resp, "not available") {
			t.Fatalf("/compact = %q, want an unavailable message", resp)
		}
	})

	t.Run("empty session", func(t *testing.T) {
		a := newAgent(t, newMockSessionManager(), &countingCompressor{}, true)
		resp, err := a.Process(context.Background(), cmdMsg("/compact"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(resp, "Nothing to compact") {
			t.Fatalf("/compact on an empty session = %q", resp)
		}
	})

	t.Run("compressor error is reported, session untouched", func(t *testing.T) {
		sessions := newMockSessionManager()
		sess := seedHistory(t, sessions, "cli:user", 3)
		before := len(sess.Messages)
		a := newAgent(t, sessions, &countingCompressor{err: errors.New("provider down")}, true)

		resp, err := a.Process(context.Background(), cmdMsg("/compact"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(resp, "provider down") {
			t.Fatalf("/compact error = %q, want it to name the cause", resp)
		}
		if got := len(sess.Messages); got != before {
			t.Fatalf("session changed on a failed compaction: %d -> %d", before, got)
		}
	})

	t.Run("empty summary is refused", func(t *testing.T) {
		sessions := newMockSessionManager()
		sess := seedHistory(t, sessions, "cli:user", 3)
		before := len(sess.Messages)
		a := newAgent(t, sessions, &countingCompressor{summary: "   "}, true)

		resp, err := a.Process(context.Background(), cmdMsg("/compact"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(resp, "empty summary") {
			t.Fatalf("/compact = %q, want an empty-summary refusal", resp)
		}
		if got := len(sess.Messages); got != before {
			t.Fatalf("a whitespace summary replaced %d messages", before-got)
		}
	})

	t.Run("success leaves exactly one compaction record", func(t *testing.T) {
		sessions := newMockSessionManager()
		sess := seedHistory(t, sessions, "cli:user", 4)
		before := len(sess.Messages)
		a := newAgent(t, sessions, &countingCompressor{summary: "THE SUMMARY"}, true)

		resp, err := a.Process(context.Background(), cmdMsg("/compact"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(resp, "Compressed") {
			t.Fatalf("/compact = %q", resp)
		}
		got, err := sessions.GetOrCreate(context.Background(), "cli:user")
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Messages) != 1 {
			t.Fatalf("session has %d messages after compacting all %d, want 1 record", len(got.Messages), before)
		}
		if !got.Messages[0].Compaction || !strings.Contains(got.Messages[0].Content, "THE SUMMARY") {
			t.Fatalf("index 0 is not the summary record: %+v", got.Messages[0])
		}
	})
}

// ---------------------------------------------------------------------------
// images
// ---------------------------------------------------------------------------

// The session records a descriptor, never the bytes: session JSONL is exempt
// from redaction and stored images would be re-sent and re-billed on every
// later turn inside the memory window.
func TestImages_SessionStoresDescriptorAndRequestCarriesBytes(t *testing.T) {
	InvalidatePromptCache()
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	provider := &scriptedProvider{turns: []scriptedTurn{{content: "a cat"}}}
	sessions := newMockSessionManager()
	a := NewAgent(cfg, provider, &mockToolExecutor{}, sessions, newMockLogger())

	data := []byte("\x89PNG\r\n\x1a\nfake image bytes")
	img := providers.Image{MIME: "image/png", Data: data}

	if _, err := a.Process(context.Background(), bus.InboundMessage{
		Channel: "cli", SenderID: "u", Content: "what is this?", Images: []providers.Image{img},
	}); err != nil {
		t.Fatalf("Process: %v", err)
	}

	sess, err := sessions.GetOrCreate(context.Background(), "cli:u")
	if err != nil {
		t.Fatal(err)
	}
	var refs []session.ImageRef
	for _, m := range sess.Messages {
		if len(m.Images) > 0 {
			refs = append(refs, m.Images...)
		}
	}
	if len(refs) != 1 {
		t.Fatalf("session recorded %d image refs, want 1", len(refs))
	}
	if refs[0].Bytes != len(data) || refs[0].MIME != "image/png" {
		t.Errorf("ref = %+v, want %d bytes of image/png", refs[0], len(data))
	}
	if len(refs[0].SHA256) != 64 {
		t.Errorf("SHA256 = %q, want a 64-char hex digest", refs[0].SHA256)
	}

	// The bytes must be on the *request*, attached to the user message that
	// carried the caption — not in a message of their own.
	msgs := provider.requests[0].Messages
	last := msgs[len(msgs)-1]
	if last.Role != providers.RoleUser {
		t.Fatalf("last request message role = %v, want user", last.Role)
	}
	if len(last.Images) != 1 || string(last.Images[0].Data) != string(data) {
		t.Fatalf("image bytes not attached to the captioned user message: %+v", last.Images)
	}
	if last.Content != "what is this?" {
		t.Errorf("caption lost: %q", last.Content)
	}
	for i, m := range msgs[:len(msgs)-1] {
		if len(m.Images) > 0 {
			t.Errorf("images also attached to message %d (role %v)", i, m.Role)
		}
	}
}

// attachImages must drop the images rather than invent a message when the
// memory window has slid past the last user turn: an image in a message of its
// own reads as an unprompted attachment, and is a 400 on providers that
// require alternating roles.
func TestAttachImages_DropsWhenNoUserMessageRemains(t *testing.T) {
	msgs := []providers.Message{
		{Role: providers.RoleSystem, Content: "sys"},
		{Role: providers.RoleAssistant, Content: "hi"},
	}
	before := len(msgs)
	attachImages(msgs, []providers.Image{{MIME: "image/png", Data: []byte("x")}})

	if len(msgs) != before {
		t.Fatalf("attachImages appended a message: %d -> %d", before, len(msgs))
	}
	for i, m := range msgs {
		if len(m.Images) > 0 {
			t.Fatalf("images attached to message %d with role %v", i, m.Role)
		}
	}

	// It targets the *last* user message when there is more than one.
	multi := []providers.Message{
		{Role: providers.RoleUser, Content: "old"},
		{Role: providers.RoleAssistant, Content: "reply"},
		{Role: providers.RoleUser, Content: "new"},
	}
	attachImages(multi, []providers.Image{{MIME: "image/png", Data: []byte("x")}})
	if len(multi[0].Images) != 0 {
		t.Error("images attached to the earlier user message")
	}
	if len(multi[2].Images) != 1 {
		t.Error("images not attached to the most recent user message")
	}
}

func TestImageRefs_EmptyIsNil(t *testing.T) {
	if refs := imageRefs(nil); refs != nil {
		t.Fatalf("imageRefs(nil) = %v, want nil", refs)
	}
	// Distinct content must produce distinct digests: the ref is the only
	// record that an image was there at all.
	a := imageRefs([]providers.Image{{MIME: "image/png", Data: []byte("a")}})
	b := imageRefs([]providers.Image{{MIME: "image/png", Data: []byte("b")}})
	if a[0].SHA256 == b[0].SHA256 {
		t.Fatal("different image bytes produced the same SHA256")
	}
}

// ---------------------------------------------------------------------------
// context.go helpers
// ---------------------------------------------------------------------------

// A missing memory file is normal (a fresh workspace) and must not be an
// error; an unreadable one must be. Conflating them makes a permissions
// problem look like an empty memory.
func TestLoadMemoryAndHistoryFiles(t *testing.T) {
	ws := t.TempDir()

	for _, tc := range []struct {
		name string
		load func(string) (string, error)
		file string
	}{
		{"memory", LoadMemoryFile, "MEMORY.md"},
		{"history", LoadHistoryFile, "HISTORY.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.load(ws)
			if err != nil || got != "" {
				t.Fatalf("missing %s: got (%q, %v), want (\"\", nil)", tc.file, got, err)
			}

			dir := filepath.Join(ws, "memory")
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			want := "user prefers tabs"
			if err := os.WriteFile(filepath.Join(dir, tc.file), []byte(want), 0644); err != nil {
				t.Fatal(err)
			}
			got, err = tc.load(ws)
			if err != nil {
				t.Fatalf("load %s: %v", tc.file, err)
			}
			if got != want {
				t.Fatalf("%s content = %q, want %q", tc.file, got, want)
			}
		})
	}
}

func TestFormatToolDescriptions(t *testing.T) {
	if got := FormatToolDescriptions(nil); got != "" {
		t.Fatalf("FormatToolDescriptions(nil) = %q, want empty", got)
	}
	got := FormatToolDescriptions([]providers.Tool{{
		Type: "function",
		Function: providers.FunctionDefinition{
			Name:        "read_file",
			Description: "Read a file",
		},
	}})
	for _, want := range []string{"read_file", "Read a file", "function"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted schema missing %q:\n%s", want, got)
		}
	}
}

// BuildSmartPrompt must carry the user's name and a current-time marker, since
// the model has neither otherwise; and it must still contain the core identity.
func TestBuildSmartPrompt_IncludesNameAndTime(t *testing.T) {
	InvalidatePromptCache()
	ws := t.TempDir()
	got := BuildSmartPrompt(ws, nil, nil, "Josh", "what time is it")

	for _, want := range []string{"The user's name is Josh.", "<current_time>", "</current_time>", "You are joshbot"} {
		if !strings.Contains(got, want) {
			t.Errorf("BuildSmartPrompt missing %q", want)
		}
	}

	InvalidatePromptCache()
	anon := BuildSmartPrompt(ws, nil, nil, "", "")
	if strings.Contains(anon, "The user's name is") {
		t.Error("BuildSmartPrompt invented a name line for an anonymous user")
	}
}

// ---------------------------------------------------------------------------
// skill auto-detection
// ---------------------------------------------------------------------------

// The end of the detection pipeline: a turn that used a tool and whose user
// message asks for a skill must produce a SKILL.md on disk. Every stage is
// real here (detector, extractor, loader) — the only double is the provider
// that writes the SKILL.md body. A break anywhere in the chain leaves the
// feature silently doing nothing, which no unit test of a single stage catches.
func TestAfterReActDetection_WritesASkillEndToEnd(t *testing.T) {
	InvalidatePromptCache()
	ws := t.TempDir()
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = ws

	skillDoc := "---\nname: demo-skill\ndescription: does the demo\ntags: [\"demo\"]\n---\n\nSteps here.\n"
	provider := &scriptedProvider{turns: []scriptedTurn{
		{toolCalls: []providers.ToolCall{toolCall("c1", "noop", `{}`)}},
		{content: "all done"},
		{content: skillDoc}, // consumed by the extractor
	}}

	loader, err := skills.NewLoader(ws)
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}

	a := NewAgent(cfg, provider, &recordingTools{outputs: map[string]string{"noop": "ok"}},
		newMockSessionManager(), newMockLogger(),
		WithSkillDetector(skills.NewSkillDetector()),
		WithExtractor(skills.NewExtractor(provider, "small")),
		WithSkillLoader(loader),
	)

	// "create a skill" is what pushes the detector's confidence over its
	// threshold with a single tool call.
	if _, err := a.Process(context.Background(), bus.InboundMessage{
		Channel: "cli", SenderID: "u", Content: "create a skill for this workflow",
	}); err != nil {
		t.Fatalf("Process: %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(ws, "skills", "*", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("found %d generated SKILL.md files, want 1 (%v)", len(matches), matches)
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(body)) != strings.TrimSpace(skillDoc) {
		t.Fatalf("SKILL.md content = %q, want the extractor's output", body)
	}
	if filepath.Base(filepath.Dir(matches[0])) == "" {
		t.Fatal("skill written without a name directory")
	}
}

// Detection must not fire on a turn that used no tools: there is no procedure
// to capture, and creating a skill from a plain chat turn fills the workspace
// with noise the model then loads on every request.
func TestAfterReActDetection_NoToolsNoSkill(t *testing.T) {
	InvalidatePromptCache()
	ws := t.TempDir()
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = ws

	provider := &scriptedProvider{turns: []scriptedTurn{{content: "sure"}}}
	loader, err := skills.NewLoader(ws)
	if err != nil {
		t.Fatal(err)
	}
	a := NewAgent(cfg, provider, &mockToolExecutor{}, newMockSessionManager(), newMockLogger(),
		WithSkillDetector(skills.NewSkillDetector()),
		WithExtractor(skills.NewExtractor(provider, "small")),
		WithSkillLoader(loader),
	)

	if _, err := a.Process(context.Background(), bus.InboundMessage{
		Channel: "cli", SenderID: "u", Content: "create a skill for this workflow",
	}); err != nil {
		t.Fatalf("Process: %v", err)
	}

	matches, _ := filepath.Glob(filepath.Join(ws, "skills", "*", "SKILL.md"))
	if len(matches) != 0 {
		t.Fatalf("a skill was created from a tool-free turn: %v", matches)
	}
}

// The options that inject collaborators all guard against nil. Passing nil
// must leave the agent's existing collaborator in place rather than installing
// a nil that panics on the next turn.
func TestOptions_NilInjectionsAreIgnored(t *testing.T) {
	InvalidatePromptCache()
	cfg := config.Defaults()
	cfg.Agents.Defaults.Workspace = t.TempDir()

	mem := &mockMemoryLoader{memoryFn: func(context.Context) (string, error) { return "seeded", nil }}
	sk := &mockSkillsLoader{summaryFn: func(context.Context) (string, error) { return "seeded skills", nil }}

	a := NewAgent(cfg, &scriptedProvider{turns: []scriptedTurn{{content: "ok"}}}, &mockToolExecutor{},
		newMockSessionManager(), newMockLogger(),
		WithMemoryLoader(mem),
		WithSkillsLoader(sk),
		// Every nil below must be a no-op, not an assignment.
		WithMemoryLoader(nil),
		WithSkillsLoader(nil),
		WithSkillDetector(nil),
		WithExtractor(nil),
		WithSkillLoader(nil),
		WithMaxIterations(0),
	)

	if a.memory != MemoryLoader(mem) {
		t.Error("WithMemoryLoader(nil) overwrote the injected memory loader")
	}
	if a.skills != SkillsLoader(sk) {
		t.Error("WithSkillsLoader(nil) overwrote the injected skills loader")
	}
	if a.maxIterations <= 0 {
		t.Errorf("WithMaxIterations(0) left maxIterations = %d", a.maxIterations)
	}

	// And the agent still runs.
	if _, err := a.Process(context.Background(), bus.InboundMessage{
		Channel: "cli", SenderID: "u", Content: "hi",
	}); err != nil {
		t.Fatalf("Process after nil injections: %v", err)
	}
}
