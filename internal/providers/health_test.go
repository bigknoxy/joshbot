package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// countingProvider fails a fixed number of times, then succeeds.
type countingProvider struct {
	name     string
	failWith error
	failFor  int // number of leading calls that fail
	calls    int
}

func (c *countingProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	c.calls++
	if c.calls <= c.failFor {
		return nil, c.failWith
	}
	return &ChatResponse{Model: req.Model}, nil
}

func (c *countingProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	c.calls++
	if c.calls <= c.failFor {
		return nil, c.failWith
	}
	ch := make(chan StreamChunk)
	close(ch)
	return ch, nil
}

func (c *countingProvider) Transcribe(ctx context.Context, audio []byte, prompt string) (string, error) {
	return "", nil
}
func (c *countingProvider) Name() string   { return c.name }
func (c *countingProvider) Config() Config { return Config{Model: "m-" + c.name} }

// fastClock replaces the MultiProvider's time source and sleep with
// instantaneous fakes that record what would have been waited.
type fastClock struct {
	now    time.Time
	slept  []time.Duration
	mpRefs *MultiProvider
}

func installFastClock(mp *MultiProvider) *fastClock {
	fc := &fastClock{now: time.Unix(1_700_000_000, 0)}
	mp.now = func() time.Time { return fc.now }
	mp.sleep = func(ctx context.Context, d time.Duration) error {
		fc.slept = append(fc.slept, d)
		fc.now = fc.now.Add(d)
		return ctx.Err()
	}
	fc.mpRefs = mp
	return fc
}

func TestRetrySucceedsOnSameProvider(t *testing.T) {
	p := &countingProvider{
		name:     "flaky",
		failWith: &FallbackError{StatusCode: 500, Message: "blip", Provider: "flaky"},
		failFor:  2,
	}
	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "flaky"})
	fc := installFastClock(mp)
	mp.Register("flaky", p, "model-a", 0, true)
	mp.SetMaxRetries("flaky", 2)

	resp, err := mp.Chat(context.Background(), ChatRequest{Model: "flaky"})
	if err != nil {
		t.Fatalf("Chat should succeed after retries, got %v", err)
	}
	if resp.Model != "model-a" {
		t.Errorf("resp.Model = %q, want model-a", resp.Model)
	}
	if p.calls != 3 {
		t.Errorf("provider called %d times, want 3 (1 + 2 retries)", p.calls)
	}
	if len(fc.slept) != 2 {
		t.Fatalf("slept %d times, want 2", len(fc.slept))
	}
	// Exponential: second wait strictly longer than the first (jitter is
	// additive, base doubles).
	if fc.slept[1] <= fc.slept[0] {
		t.Errorf("backoff not increasing: %v then %v", fc.slept[0], fc.slept[1])
	}
	// Success clears the health record.
	if len(mp.HealthSnapshot()) != 0 {
		t.Errorf("health should be clean after success, got %+v", mp.HealthSnapshot())
	}
}

func TestRetryHonorsRetryAfter(t *testing.T) {
	p := &countingProvider{
		name:     "limited",
		failWith: &FallbackError{StatusCode: 429, Message: "slow down", Provider: "limited", RetryAfter: 3 * time.Second},
		failFor:  1,
	}
	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "limited"})
	fc := installFastClock(mp)
	mp.Register("limited", p, "model-a", 0, true)
	mp.SetMaxRetries("limited", 1)

	if _, err := mp.Chat(context.Background(), ChatRequest{Model: "limited"}); err != nil {
		t.Fatalf("Chat should succeed on retry, got %v", err)
	}
	if len(fc.slept) != 1 || fc.slept[0] != 3*time.Second {
		t.Errorf("slept %v, want exactly the Retry-After of 3s", fc.slept)
	}
}

func TestLongRetryAfterSkipsRetryAndCoolsDown(t *testing.T) {
	primary := &countingProvider{
		name:     "primary",
		failWith: &FallbackError{StatusCode: 429, Message: "come back later", Provider: "primary", RetryAfter: 2 * time.Minute},
		failFor:  1 << 30,
	}
	backup := &countingProvider{name: "backup"}
	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "primary"})
	fc := installFastClock(mp)
	mp.Register("primary", primary, "model-a", 0, true)
	mp.Register("backup", backup, "model-b", 1, true)
	mp.SetMaxRetries("primary", 3)

	resp, err := mp.Chat(context.Background(), ChatRequest{Model: "primary"})
	if err != nil {
		t.Fatalf("Chat should fall back, got %v", err)
	}
	if resp.Model != "model-b" {
		t.Errorf("answered by %q, want the backup's model-b", resp.Model)
	}
	// The 2-minute Retry-After exceeds the in-turn cap: no sleep, one dial.
	if len(fc.slept) != 0 {
		t.Errorf("turn stalled %v, want no in-turn wait for a long Retry-After", fc.slept)
	}
	if primary.calls != 1 {
		t.Errorf("primary dialled %d times, want 1", primary.calls)
	}

	// The primary is now cooling: the next turn goes straight to the backup.
	before := primary.calls
	if _, err := mp.Chat(context.Background(), ChatRequest{Model: "primary"}); err != nil {
		t.Fatalf("second Chat failed: %v", err)
	}
	if primary.calls != before {
		t.Errorf("primary re-dialled during cooldown (%d calls)", primary.calls)
	}

	// After the window passes it is dialled again.
	fc.now = fc.now.Add(3 * time.Minute)
	if _, err := mp.Chat(context.Background(), ChatRequest{Model: "primary"}); err != nil {
		t.Fatalf("third Chat failed: %v", err)
	}
	if primary.calls != before+1 {
		t.Errorf("primary should be re-probed after cooldown, calls=%d want %d", primary.calls, before+1)
	}
}

func TestCooldownNeverEmptiesTheChain(t *testing.T) {
	only := &countingProvider{
		name:     "only",
		failWith: &FallbackError{StatusCode: 503, Message: "down", Provider: "only", RetryAfter: time.Minute},
		failFor:  1,
	}
	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "only"})
	installFastClock(mp)
	mp.Register("only", only, "model-a", 0, true)

	// First turn fails and cools the only provider down.
	if _, err := mp.Chat(context.Background(), ChatRequest{Model: "only"}); err == nil {
		t.Fatal("first Chat should fail")
	}
	// Second turn must still dial it: a cooldown deprioritizes, never drops.
	resp, err := mp.Chat(context.Background(), ChatRequest{Model: "only"})
	if err != nil {
		t.Fatalf("cooldown must not empty the chain: %v", err)
	}
	if resp.Model != "model-a" {
		t.Errorf("resp.Model = %q", resp.Model)
	}
}

func TestNonFallbackErrorIsNotRetried(t *testing.T) {
	p := &countingProvider{
		name:     "denied",
		failWith: &FallbackError{StatusCode: 401, Message: "bad key", Provider: "denied"},
		failFor:  1 << 30,
	}
	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "denied"})
	installFastClock(mp)
	mp.Register("denied", p, "model-a", 0, true)
	mp.SetMaxRetries("denied", 3)

	if _, err := mp.Chat(context.Background(), ChatRequest{Model: "denied"}); err == nil {
		t.Fatal("Chat should fail on 401")
	}
	if p.calls != 1 {
		t.Errorf("401 was retried (%d calls), must not be", p.calls)
	}
}

func TestStreamRetryOnOpenFailure(t *testing.T) {
	p := &countingProvider{
		name:     "flaky",
		failWith: &FallbackError{StatusCode: 502, Message: "bad gateway", Provider: "flaky"},
		failFor:  1,
	}
	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "flaky"})
	fc := installFastClock(mp)
	mp.Register("flaky", p, "model-a", 0, true)
	mp.SetMaxRetries("flaky", 1)

	ch, err := mp.ChatStream(context.Background(), ChatRequest{Model: "flaky"})
	if err != nil {
		t.Fatalf("ChatStream should succeed on retry, got %v", err)
	}
	if ch == nil {
		t.Fatal("nil channel")
	}
	if p.calls != 2 {
		t.Errorf("provider called %d times, want 2", p.calls)
	}
	if len(fc.slept) != 1 {
		t.Errorf("slept %d times, want 1", len(fc.slept))
	}
}

func TestHealthSnapshotReportsCooling(t *testing.T) {
	p := &countingProvider{
		name:     "sick",
		failWith: &FallbackError{StatusCode: 429, Message: "limited", Provider: "sick", RetryAfter: time.Minute},
		failFor:  1 << 30,
	}
	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "sick"})
	installFastClock(mp)
	mp.Register("sick", p, "model-a", 0, true)

	_, _ = mp.Chat(context.Background(), ChatRequest{Model: "sick"})

	infos := mp.HealthSnapshot()
	if len(infos) != 1 {
		t.Fatalf("HealthSnapshot() len = %d, want 1", len(infos))
	}
	if infos[0].Name != "sick" || infos[0].Failures != 1 {
		t.Errorf("snapshot = %+v", infos[0])
	}
	if infos[0].LastErr != "rate_limit" {
		t.Errorf("LastErr = %q, want rate_limit", infos[0].LastErr)
	}
	if infos[0].CoolUntil.IsZero() {
		t.Error("CoolUntil should be set by a Retry-After")
	}
}

func TestRepeatedFailuresCoolWithoutRetryAfter(t *testing.T) {
	p := &countingProvider{
		name:     "down",
		failWith: &FallbackError{StatusCode: 503, Message: "down", Provider: "down"},
		failFor:  1 << 30,
	}
	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "down"})
	fc := installFastClock(mp)
	mp.Register("down", p, "model-a", 0, true)

	// One failure: no cooldown yet.
	_, _ = mp.Chat(context.Background(), ChatRequest{Model: "down"})
	if infos := mp.HealthSnapshot(); len(infos) != 1 || infos[0].Failures != 1 || !infos[0].CoolUntil.IsZero() {
		t.Fatalf("one failure should record but not cool down: %+v", infos)
	}
	// Second consecutive failure crosses the threshold.
	_, _ = mp.Chat(context.Background(), ChatRequest{Model: "down"})
	infos := mp.HealthSnapshot()
	if len(infos) != 1 || infos[0].Failures != 2 {
		t.Fatalf("snapshot = %+v", infos)
	}
	if !fc.now.Before(infos[0].CoolUntil) {
		t.Error("two consecutive failures should start a cooldown")
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"120", 120 * time.Second},
		{"0", 0},
		{"-5", 0},
		{"garbage", 0},
	}
	for _, c := range cases {
		if got := ParseRetryAfter(c.in); got != c.want {
			t.Errorf("ParseRetryAfter(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	// HTTP-date form: a time in the future yields a positive duration.
	future := time.Now().Add(90 * time.Second).UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
	if got := ParseRetryAfter(future); got < 80*time.Second || got > 90*time.Second {
		t.Errorf("ParseRetryAfter(http-date) = %v, want ~90s", got)
	}
	past := time.Now().Add(-time.Hour).UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
	if got := ParseRetryAfter(past); got != 0 {
		t.Errorf("ParseRetryAfter(past date) = %v, want 0", got)
	}
}

func TestAggregateErrorStillNamesEveryProviderWithRetries(t *testing.T) {
	p1 := &countingProvider{name: "a", failWith: &FallbackError{StatusCode: 500, Message: "boom", Provider: "a"}, failFor: 1 << 30}
	p2 := &countingProvider{name: "b", failWith: &FallbackError{StatusCode: 429, Message: "limited", Provider: "b"}, failFor: 1 << 30}
	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "a"})
	installFastClock(mp)
	mp.Register("a", p1, "model-a", 0, true)
	mp.Register("b", p2, "model-b", 1, true)
	mp.SetMaxRetries("a", 1)
	mp.SetMaxRetries("b", 1)

	_, err := mp.Chat(context.Background(), ChatRequest{Model: "a"})
	if err == nil {
		t.Fatal("Chat should fail")
	}
	for _, want := range []string{"a:", "b:", "boom", "limited"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("aggregate error missing %q: %v", want, err)
		}
	}
	if p1.calls != 2 || p2.calls != 2 {
		t.Errorf("calls = %d/%d, want 2/2 (1 + 1 retry each)", p1.calls, p2.calls)
	}
}

// TestRetryAfterEndToEndOverHTTP proves the whole path: a real HTTP 429 with
// a Retry-After header, parsed by LiteLLMProvider into a FallbackError, paced
// by MultiProvider's retry, and answered on the second attempt.
func TestRetryAfterEndToEndOverHTTP(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"hello"}}],"model":"m"}`)
	}))
	defer srv.Close()

	p := NewLiteLLMProvider(Config{APIBase: srv.URL, Model: "m", APIKey: "k"})
	mp := NewMultiProvider(MultiProviderConfig{DefaultProvider: "fake"})
	fc := installFastClock(mp)
	mp.Register("fake", p, "m", 0, true)
	mp.SetMaxRetries("fake", 1)

	resp, err := mp.Chat(context.Background(), ChatRequest{Model: "fake"})
	if err != nil {
		t.Fatalf("Chat = %v", err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content != "hello" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("server hit %d times, want 2", got)
	}
	if len(fc.slept) != 1 || fc.slept[0] != 2*time.Second {
		t.Errorf("slept %v, want exactly the wire Retry-After of 2s", fc.slept)
	}
}
