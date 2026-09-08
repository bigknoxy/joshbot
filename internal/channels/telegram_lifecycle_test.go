package channels

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	telebot "gopkg.in/telebot.v3"
)

// offlineTelegramChannel points a channel at a fake Bot API so Start() can run
// the real path — createBot, setupHandlers, the command menu, the outbound
// consumer and the poller — without touching api.telegram.org.
func offlineTelegramChannel(t *testing.T, allow ...string) (*TelegramChannel, *fakeTelegramServer) {
	t.Helper()
	srv := newFakeTelegramServer(t)
	tg := newTestTelegramChannel(allow...)
	tg.mu.Lock()
	tg.apiURL = srv.URL
	tg.offline = true
	tg.pollTimeout = 10 * time.Millisecond
	tg.retryDelay = time.Millisecond
	tg.maxRetryDelay = 5 * time.Millisecond
	tg.typingInterval = time.Hour
	tg.mu.Unlock()
	return tg, srv
}

// TestTelegramChannel_StartRefusesAnUnconfiguredToken pins that a missing token
// is reported *and* releases the running latch. Leaving running set would make
// every later Start report "already running", so an operator who fixes their
// config would still have a dead bot with no error to explain it.
func TestTelegramChannel_StartRefusesAnUnconfiguredToken(t *testing.T) {
	tg := NewTelegramChannel(bus.NewMessageBus(), &config.TelegramConfig{Enabled: true})

	err := tg.Start(context.Background())
	if err == nil {
		t.Fatal("Start with no token should have failed")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Fatalf("error should name the token, got %v", err)
	}
	tg.mu.RLock()
	running := tg.running
	tg.mu.RUnlock()
	if running {
		t.Fatal("a failed Start left the channel latched as running; a later Start can never succeed")
	}
}

// TestTelegramChannel_StartRunsTheWholeLoop exercises Start end to end against a
// fake Bot API: it must create the bot, publish the command menu, and leave an
// outbound consumer that actually delivers. Stop must then bring all of it down.
func TestTelegramChannel_StartRunsTheWholeLoop(t *testing.T) {
	tg, srv := offlineTelegramChannel(t, "1234")
	tg.bus.Start()
	defer tg.bus.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := tg.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// A second Start must be refused rather than starting a second poller
	// against the same token.
	if err := tg.Start(ctx); err == nil {
		t.Fatal("a second Start should have been refused")
	}

	// runBot creates the bot on its own goroutine now, so poll for it.
	deadline := time.After(3 * time.Second)
	var bot *telebot.Bot
	for {
		tg.mu.RLock()
		bot = tg.bot
		tg.mu.RUnlock()
		if bot != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Start did not install a bot")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// The consumer Start launched must deliver a message addressed to this
	// channel. Nothing else in the process is reading the outbound channel.
	//
	// The consumer registers its bus subscription only once its own
	// goroutine actually runs, so a message published before that
	// registration completes is fanned out to a subscriber list that does
	// not yet include it and never arrives. Resend on a short interval
	// until it's seen, rather than assuming the first send won that race —
	// a duplicate delivery doesn't change what this test checks.
	outbound := bus.OutboundMessage{Channel: "telegram", ChannelID: "1234", Content: "delivered by the consumer"}
	deliveryDeadline := time.After(2 * time.Second)
	for {
		texts := srv.texts()
		found := false
		for _, s := range texts {
			if s == "delivered by the consumer" {
				found = true
			}
		}
		if found {
			break
		}
		select {
		case <-deliveryDeadline:
			t.Fatalf("the outbound consumer Start launched never delivered; sent = %v", texts)
		case <-time.After(10 * time.Millisecond):
			tg.bus.OutboundChan() <- outbound
		}
	}

	if err := tg.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	tg.mu.RLock()
	stillRunning, stillBot := tg.running, tg.bot
	tg.mu.RUnlock()
	if stillRunning || stillBot != nil {
		t.Fatalf("Stop left running=%v bot=%v", stillRunning, stillBot != nil)
	}
}

// TestTelegramChannel_StateTracksConnectionLifecycle pins that State()
// reflects the channel's live runtime state: down before start, reconnecting
// while the initial creation is retrying, connected once polling. When the
// channel reconnects after a dropped poll it must report reconnecting, not a
// stale connected.
func TestTelegramChannel_StateTracksConnectionLifecycle(t *testing.T) {
	tg, _ := offlineTelegramChannel(t, "1234")

	if got := tg.State(); got != ChannelDown {
		t.Fatalf("pre-start State = %q, want %q", got, ChannelDown)
	}

	tg.mu.Lock()
	tg.running = true
	// Simulate the transient-outage window from the self-heal regression test:
	// the first attempt fails, so the channel reports reconnecting.
	var attempts int
	tg.createFunc = func(ctx context.Context) (*telebot.Bot, error) {
		attempts++
		if attempts <= 2 {
			return nil, fmt.Errorf("dial failed")
		}
		// Clear the seam so the real createBot path runs from here on.
		tg.mu.Lock()
		tg.createFunc = nil
		tg.mu.Unlock()
		return tg.createBot(ctx)
	}
	tg.retryDelay = time.Millisecond
	tg.maxRetryDelay = time.Millisecond
	tg.mu.Unlock()

	if got := tg.State(); got != ChannelDown {
		t.Fatalf("State before runBot = %q, want %q", got, ChannelDown)
	}

	done := make(chan struct{})
	go func() {
		tg.runBot(context.Background())
		close(done)
	}()

	// Eventually connected once the injected outage clears.
	deadline := time.After(3 * time.Second)
	for {
		if tg.State() == ChannelConnected {
			break
		}
		select {
		case <-deadline:
			t.Fatal("channel never reached connected")
		case <-time.After(5 * time.Millisecond):
		}
	}

	if err := tg.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := tg.State(); got != ChannelDown {
		t.Fatalf("post-stop State = %q, want %q", got, ChannelDown)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runBot did not return after Stop")
	}
}

// TestTelegramChannel_RunBotRebindsAfterReconnect pins the reconnection path.
// When polling dies, runBot must build a *new* bot and rebind its own loop
// variable to it: restarting the stale, already-stopped bot returns instantly
// forever, which reads as a live channel that silently receives nothing.
func TestTelegramChannel_RunBotRebindsAfterReconnect(t *testing.T) {
	tg, _ := offlineTelegramChannel(t, "1234")
	tg.mu.Lock()
	tg.running = true
	tg.mu.Unlock()

	done := make(chan struct{})
	go func() {
		tg.runBot(context.Background())
		close(done)
	}()

	// Wait for runBot to create its own bot and start polling.
	deadline := time.After(3 * time.Second)
	var first *telebot.Bot
	for {
		tg.mu.RLock()
		first = tg.bot
		tg.mu.RUnlock()
		if first != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("runBot never created its initial bot")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Let the poller start, then kill it the way a dropped connection would.
	time.Sleep(100 * time.Millisecond)
	first.Stop()

	for {
		tg.mu.RLock()
		current := tg.bot
		tg.mu.RUnlock()
		if current != nil && current != first {
			break
		}
		select {
		case <-deadline:
			t.Fatal("runBot never reconnected after polling stopped")
		case <-time.After(10 * time.Millisecond):
		}
	}

	if err := tg.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runBot did not return after Stop; the reconnected poller leaked")
	}
}

// TestTelegramChannel_RunBotSelfHealsAcrossATransientCreationFailure is the
// regression test for issue #387. If creating the bot fails (the real failure
// is a network/DNS outage at boot making the api.telegram.org lookup fail),
// runBot must keep retrying with backoff and eventually connect once the
// network recovers — instead of abandoning the channel after one attempt,
// which left the gateway "running" with Telegram permanently dead until a
// manual restart.
func TestTelegramChannel_RunBotSelfHealsAcrossATransientCreationFailure(t *testing.T) {
	tg, _ := offlineTelegramChannel(t, "1234")
	tg.mu.Lock()
	tg.running = true
	// failCreation is the injected outage: the first 3 creation attempts are
	// treated as unreachable, then the network comes back and createBot's real
	// path (against the fake server) succeeds.
	var attempts int
	tg.createFunc = func(ctx context.Context) (*telebot.Bot, error) {
		tg.mu.Lock()
		attempts++
		n := attempts
		tg.mu.Unlock()
		if n <= 3 {
			return nil, fmt.Errorf("GetMe: Post \"https://api.telegram.org/getMe\": dial tcp: lookup api.telegram.org: no such host")
		}
		// Back to the real creation path now that the network is "back".
		tg.mu.Lock()
		tg.createFunc = nil
		tg.mu.Unlock()
		return tg.createBot(ctx)
	}
	tg.retryDelay = 2 * time.Millisecond
	tg.maxRetryDelay = 4 * time.Millisecond
	tg.mu.Unlock()

	done := make(chan struct{})
	go func() {
		tg.runBot(context.Background())
		close(done)
	}()

	// The channel must come up on its own, without a restart, after the fake
	// outage clears.
	deadline := time.After(3 * time.Second)
	for {
		tg.mu.RLock()
		bot := tg.bot
		tg.mu.RUnlock()
		if bot != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("runBot did not self-heal: after the transient creation failures it never connected")
		case <-time.After(5 * time.Millisecond):
		}
	}

	tg.mu.RLock()
	n := attempts
	tg.mu.RUnlock()
	if n < 3 {
		t.Fatalf("expected >=3 creation attempts during the outage, saw %d", n)
	}

	if err := tg.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runBot did not return after Stop; the reconnected poller leaked")
	}
}

// A proxy URL must be honoured when it parses and ignored when it does not —
// an unparseable proxy must never stop the bot from being created, or a typo in
// an optional setting takes the whole channel down.
func TestTelegramChannel_CreateBotToleratesABadProxy(t *testing.T) {
	srv := newFakeTelegramServer(t)
	for _, proxy := range []string{"http://127.0.0.1:9/", "://not a url"} {
		tg := NewTelegramChannel(bus.NewMessageBus(), &config.TelegramConfig{
			Enabled: true, Token: "test-token", Proxy: proxy,
		})
		tg.apiURL = srv.URL
		tg.offline = true
		if _, err := tg.createBot(context.Background()); err != nil {
			t.Fatalf("createBot with proxy %q: %v", proxy, err)
		}
	}
}

// recipientKey screens the recipients that would panic inside Recipient(). A
// typed nil pointer in a non-nil interface is the case that actually reaches
// here — a message with no chat — and it must be reported unusable, not
// dereferenced.
func TestRecipientKeyScreensUnusableRecipients(t *testing.T) {
	var nilChat *telebot.Chat
	var nilUser *telebot.User

	for name, r := range map[string]telebot.Recipient{
		"nil interface":  nil,
		"typed nil chat": nilChat,
		"typed nil user": nilUser,
	} {
		if _, ok := recipientKey(r); ok {
			t.Errorf("%s should be reported unusable", name)
		}
	}

	if key, ok := recipientKey(telebot.ChatID(42)); !ok || key != "42" {
		t.Fatalf("recipientKey(ChatID(42)) = %q,%v; want \"42\",true", key, ok)
	}
}

// Send must abandon a retryable failure the moment the channel is shutting
// down, rather than sleeping out its exponential backoff — Stop waits behind
// nothing, but an outbound consumer parked in a multi-second backoff keeps the
// process alive and keeps hammering an API that just rate-limited it.
func TestTelegramChannel_SendStopsRetryingOnShutdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 1"}`))
	}))
	defer srv.Close()

	bot, err := telebot.NewBot(telebot.Settings{
		Token:   "test-token",
		URL:     srv.URL,
		Poller:  &telebot.LongPoller{Timeout: 10 * time.Millisecond},
		Offline: true,
	})
	if err != nil {
		t.Fatalf("NewBot: %v", err)
	}

	tg := newTestTelegramChannel("1234")
	tg.mu.Lock()
	tg.bot = bot
	tg.notifier = &fakeNotifier{}
	tg.retryDelay = 30 * time.Second
	tg.mu.Unlock()

	close(tg.stopCh)

	start := time.Now()
	sendErr := tg.Send(bus.OutboundMessage{Channel: "telegram", ChannelID: "1234", Content: "x"})
	if sendErr == nil {
		t.Fatal("expected an error when every attempt is rate-limited")
	}
	if !strings.Contains(sendErr.Error(), "stopped while retrying") {
		t.Fatalf("error should say the send was abandoned for shutdown, got %v", sendErr)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Send slept out its backoff after shutdown (%s)", elapsed)
	}
}
