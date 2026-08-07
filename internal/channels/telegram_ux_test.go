package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	telebot "gopkg.in/telebot.v3"
)

// fakeNotifier records typing actions and command registrations without any
// network access.
type fakeNotifier struct {
	mu       sync.Mutex
	actions  []string
	cmdCalls [][]interface{}
	cmdErr   error
}

func (f *fakeNotifier) Notify(to telebot.Recipient, action telebot.ChatAction, threadID ...int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, fmt.Sprintf("%s:%s", to.Recipient(), action))
	return nil
}

func (f *fakeNotifier) SetCommands(opts ...interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmdCalls = append(f.cmdCalls, opts)
	return f.cmdErr
}

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.actions)
}

// Telegram clears a chat action after 5 seconds, so a single sendChatAction
// leaves the user staring at a dead chat during a long ReAct turn. The
// keep-alive must re-send until it is explicitly stopped.
func TestTelegramChannel_TypingKeepAliveRepeats(t *testing.T) {
	tg := newTestTelegramChannel()
	fake := &fakeNotifier{}
	tg.mu.Lock()
	tg.notifier = fake
	tg.typingInterval = 10 * time.Millisecond
	tg.mu.Unlock()

	tg.startTyping(telebot.ChatID(42))

	deadline := time.After(2 * time.Second)
	for fake.count() < 3 {
		select {
		case <-deadline:
			t.Fatalf("typing keep-alive sent only %d actions; expected repeats", fake.count())
		case <-time.After(5 * time.Millisecond):
		}
	}

	tg.stopTyping(telebot.ChatID(42))
	time.Sleep(50 * time.Millisecond)
	after := fake.count()
	time.Sleep(80 * time.Millisecond)
	if fake.count() != after {
		t.Fatalf("typing kept firing after stopTyping: %d -> %d", after, fake.count())
	}
}

func TestTelegramChannel_TypingUsesTypingAction(t *testing.T) {
	tg := newTestTelegramChannel()
	fake := &fakeNotifier{}
	tg.mu.Lock()
	tg.notifier = fake
	tg.typingInterval = time.Hour
	tg.mu.Unlock()

	tg.startTyping(telebot.ChatID(7))
	defer tg.stopTyping(telebot.ChatID(7))

	deadline := time.After(2 * time.Second)
	for fake.count() < 1 {
		select {
		case <-deadline:
			t.Fatal("no typing action was sent")
		case <-time.After(5 * time.Millisecond):
		}
	}

	fake.mu.Lock()
	got := fake.actions[0]
	fake.mu.Unlock()
	if got != "7:"+string(telebot.Typing) {
		t.Fatalf("expected typing action for chat 7, got %q", got)
	}
}

// The keep-alive must not outlive the channel even if the reply never arrives.
func TestTelegramChannel_TypingStopsOnChannelShutdown(t *testing.T) {
	tg := newTestTelegramChannel()
	fake := &fakeNotifier{}
	tg.mu.Lock()
	tg.notifier = fake
	tg.typingInterval = 5 * time.Millisecond
	tg.mu.Unlock()

	tg.startTyping(telebot.ChatID(1))
	time.Sleep(30 * time.Millisecond)

	close(tg.stopCh)
	time.Sleep(50 * time.Millisecond)
	after := fake.count()
	time.Sleep(80 * time.Millisecond)
	if fake.count() != after {
		t.Fatalf("typing kept firing after the channel shut down: %d -> %d", after, fake.count())
	}
}

func TestTelegramChannel_StopTypingUnknownChatIsNoop(t *testing.T) {
	tg := newTestTelegramChannel()
	// Must not panic on a chat that never started a keep-alive.
	tg.stopTyping(telebot.ChatID(999))
	tg.stopTyping(nil)
}

// Concurrent chats each get their own keep-alive; stopping one must not stop
// the other.
func TestTelegramChannel_TypingIsPerChat(t *testing.T) {
	tg := newTestTelegramChannel()
	fake := &fakeNotifier{}
	tg.mu.Lock()
	tg.notifier = fake
	tg.typingInterval = 5 * time.Millisecond
	tg.mu.Unlock()

	tg.startTyping(telebot.ChatID(1))
	tg.startTyping(telebot.ChatID(2))
	defer tg.stopTyping(telebot.ChatID(2))

	seen := func(prefix string) bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		for _, a := range fake.actions {
			if strings.HasPrefix(a, prefix) {
				return true
			}
		}
		return false
	}

	deadline := time.After(2 * time.Second)
	for !seen("1:") || !seen("2:") {
		select {
		case <-deadline:
			t.Fatal("both chats should have received typing actions")
		case <-time.After(5 * time.Millisecond):
		}
	}

	tg.stopTyping(telebot.ChatID(1))
	time.Sleep(40 * time.Millisecond)

	countFor := func(prefix string) int {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		n := 0
		for _, a := range fake.actions {
			if strings.HasPrefix(a, prefix) {
				n++
			}
		}
		return n
	}
	one, two := countFor("1:"), countFor("2:")
	time.Sleep(80 * time.Millisecond)
	if countFor("1:") != one {
		t.Fatal("chat 1 kept typing after being stopped")
	}
	if countFor("2:") <= two {
		t.Fatal("chat 2 stopped typing when chat 1 was stopped")
	}
}

// fakeTelegramServer answers the handful of Bot API calls these tests make and
// records the text and parse_mode of every sendMessage. If rejectParseMode is
// set, any sendMessage carrying that parse_mode gets Telegram's real
// can't-parse-entities error body instead of success — this is what a
// malformed Markdown reply from the LLM looks like in production.
type fakeTelegramServer struct {
	*httptest.Server
	mu              sync.Mutex
	sent            []string
	parseModes      []string
	rejectParseMode string
	// rejectAll answers every sendMessage with the parse-entity error,
	// whatever parse mode it carried. Needed to exercise the guard that stops
	// a plain-text send from attempting a pointless "fallback" to plain text.
	rejectAll bool
	// attempts counts every sendMessage request, including rejected ones;
	// `sent` only records the ones that succeeded.
	attempts int
}

func newFakeTelegramServer(t *testing.T) *fakeTelegramServer {
	t.Helper()
	f := &fakeTelegramServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			var payload struct {
				Text      string `json:"text"`
				ParseMode string `json:"parse_mode"`
			}
			_ = json.Unmarshal(body, &payload)

			f.mu.Lock()
			reject := f.rejectParseMode
			rejectAll := f.rejectAll
			f.attempts++
			f.mu.Unlock()

			if rejectAll || (reject != "" && strings.EqualFold(payload.ParseMode, reject)) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities: Character '_' is reserved and must be escaped with the preceding '\\'"}`))
				return
			}

			f.mu.Lock()
			f.sent = append(f.sent, payload.Text)
			f.parseModes = append(f.parseModes, payload.ParseMode)
			f.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":1}}}`))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeTelegramServer) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

func (f *fakeTelegramServer) attemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

func (f *fakeTelegramServer) modes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.parseModes...)
}

func (f *fakeTelegramServer) bot(t *testing.T) *telebot.Bot {
	t.Helper()
	bot, err := telebot.NewBot(telebot.Settings{
		Token:   "test-token",
		URL:     f.URL,
		Poller:  &telebot.LongPoller{Timeout: 10 * time.Millisecond},
		Offline: true,
	})
	if err != nil {
		t.Fatalf("failed to create test bot: %v", err)
	}
	return bot
}

// Sending the reply must end the keep-alive, otherwise it runs until shutdown.
func TestTelegramChannel_SendStopsTyping(t *testing.T) {
	srv := newFakeTelegramServer(t)
	tg := newTestTelegramChannel()
	fake := &fakeNotifier{}
	tg.mu.Lock()
	tg.bot = srv.bot(t)
	tg.notifier = fake
	tg.typingInterval = 5 * time.Millisecond
	tg.mu.Unlock()

	tg.startTyping(telebot.ChatID(1))
	time.Sleep(30 * time.Millisecond)

	if err := tg.Send(bus.OutboundMessage{Channel: "telegram", ChannelID: "1", Content: "hello"}); err != nil {
		t.Fatalf("Send returned %v", err)
	}

	time.Sleep(40 * time.Millisecond)
	after := fake.count()
	time.Sleep(80 * time.Millisecond)
	if fake.count() != after {
		t.Fatalf("typing kept firing after the reply was sent: %d -> %d", after, fake.count())
	}
}

// This is the core bug: a Markdown-formatted reply containing an unescaped
// entity (very common in LLM output — underscores, asterisks, stray
// backticks) must not vanish. Telegram rejects it with 400 can't-parse-
// entities; Send must retry once as plain text rather than dropping the
// message.
func TestTelegramChannel_SendFallsBackToPlainTextOnParseEntityError(t *testing.T) {
	srv := newFakeTelegramServer(t)
	srv.rejectParseMode = "Markdown"
	tg := newTestTelegramChannel()
	tg.mu.Lock()
	tg.bot = srv.bot(t)
	tg.notifier = &fakeNotifier{}
	tg.mu.Unlock()

	content := "unescaped _ entity"
	err := tg.Send(bus.OutboundMessage{
		Channel:   "telegram",
		ChannelID: "1",
		Content:   content,
		Metadata:  map[string]any{"parse_mode": "markdown"},
	})
	if err != nil {
		t.Fatalf("Send returned %v; the message must never be lost to a formatting error", err)
	}

	texts := srv.texts()
	if len(texts) != 1 || texts[0] != content {
		t.Fatalf("expected the plain-text fallback to deliver the content, got %v", texts)
	}
	modes := srv.modes()
	if len(modes) != 1 || modes[0] != "" {
		t.Fatalf("expected the fallback send to use no parse_mode, got %v", modes)
	}
}

// The fallback exists to strip formatting. A send that carried no parse mode
// has nothing to strip, so it must NOT make a second identical attempt --
// that would hand one failure a free retry outside the maxRetries budget.
//
// This is the guard `partOpts.ParseMode != telebot.ModeDefault`. Without this
// test that guard can be deleted and every other test still passes.
func TestTelegramChannel_PlainTextSendDoesNotAttemptFallback(t *testing.T) {
	srv := newFakeTelegramServer(t)
	srv.rejectAll = true // parse-entity error even for a plain-text send
	tg := newTestTelegramChannel()
	tg.mu.Lock()
	tg.bot = srv.bot(t)
	tg.notifier = &fakeNotifier{}
	tg.maxRetries = 1
	tg.retryDelay = time.Millisecond
	tg.mu.Unlock()

	// No parse_mode metadata, so the send goes out as plain text.
	err := tg.Send(bus.OutboundMessage{Channel: "telegram", ChannelID: "1", Content: "plain text"})
	if err == nil {
		t.Fatal("expected the send to fail; the fake server rejects everything")
	}
	if got := srv.attemptCount(); got != 1 {
		t.Errorf("plain-text send made %d attempts, want 1 — the fallback fired "+
			"despite there being no formatting to remove", got)
	}
}

// A successful plain send is the control: no rejection, no fallback, one call.
func TestTelegramChannel_PlainTextSendSucceedsUntouched(t *testing.T) {
	srv := newFakeTelegramServer(t)
	tg := newTestTelegramChannel()
	tg.mu.Lock()
	tg.bot = srv.bot(t)
	tg.notifier = &fakeNotifier{}
	tg.maxRetries = 1
	tg.mu.Unlock()

	if err := tg.Send(bus.OutboundMessage{Channel: "telegram", ChannelID: "1", Content: "plain text"}); err != nil {
		t.Fatalf("Send returned %v", err)
	}
	if got := srv.attemptCount(); got != 1 {
		t.Errorf("made %d attempts for a successful plain send, want 1", got)
	}
}

// splitMessage produces multiple parts for long content; every part must get
// its own fallback, not just the first.
func TestTelegramChannel_SendFallsBackOnEveryPartOfASplitMessage(t *testing.T) {
	srv := newFakeTelegramServer(t)
	srv.rejectParseMode = "Markdown"
	tg := newTestTelegramChannel()
	tg.mu.Lock()
	tg.bot = srv.bot(t)
	tg.notifier = &fakeNotifier{}
	tg.mu.Unlock()

	// Build content long enough that splitMessage produces at least two parts.
	content := strings.Repeat("a_b ", 2000)
	err := tg.Send(bus.OutboundMessage{
		Channel:   "telegram",
		ChannelID: "1",
		Content:   content,
		Metadata:  map[string]any{"parse_mode": "markdown"},
	})
	if err != nil {
		t.Fatalf("Send returned %v", err)
	}

	texts := srv.texts()
	if len(texts) < 2 {
		t.Fatalf("expected the long message to be split into multiple parts, got %d", len(texts))
	}
	modes := srv.modes()
	for i, m := range modes {
		if m != "" {
			t.Fatalf("part %d was delivered with parse_mode %q; every part must fall back to plain text", i, m)
		}
	}
}

// A non-formatting failure (e.g. a genuine network error, or another 400)
// must retry/fail normally and must not be silently downgraded to plain
// text — only the parse-entity failure mode gets the fallback.
func TestTelegramChannel_SendDoesNotFallBackOnUnrelatedError(t *testing.T) {
	var mu sync.Mutex
	sendCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			mu.Lock()
			sendCount++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":1}}}`))
	}))
	defer srv.Close()

	tg := newTestTelegramChannel()
	tg.mu.Lock()
	bot, err := telebot.NewBot(telebot.Settings{
		Token:   "test-token",
		URL:     srv.URL,
		Poller:  &telebot.LongPoller{Timeout: 10 * time.Millisecond},
		Offline: true,
	})
	if err != nil {
		t.Fatalf("failed to create test bot: %v", err)
	}
	tg.bot = bot
	tg.notifier = &fakeNotifier{}
	tg.maxRetries = 1
	tg.mu.Unlock()

	sendErr := tg.Send(bus.OutboundMessage{
		Channel:   "telegram",
		ChannelID: "1",
		Content:   "hello",
		Metadata:  map[string]any{"parse_mode": "markdown"},
	})
	if sendErr == nil {
		t.Fatal("expected Send to return an error for an unrelated 400, not silently downgrade")
	}
	if !strings.Contains(sendErr.Error(), "chat not found") {
		t.Fatalf("expected the original error to be preserved, got %v", sendErr)
	}

	// maxRetries=1 means exactly one sendMessage attempt; a fallback attempt
	// for a non-formatting error would show up as a second call.
	mu.Lock()
	got := sendCount
	mu.Unlock()
	if got != 1 {
		t.Fatalf("expected exactly 1 sendMessage call for an unrelated error, got %d (fallback must only trigger on parse-entity errors)", got)
	}
}

// The command menu must be registered with Telegram, scoped to private chats,
// and must list exactly the commands that have handlers.
func TestTelegramChannel_RegisterCommands(t *testing.T) {
	tg := newTestTelegramChannel()
	fake := &fakeNotifier{}

	if err := tg.registerCommands(fake); err != nil {
		t.Fatalf("registerCommands returned %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.cmdCalls) != 1 {
		t.Fatalf("expected exactly one SetCommands call, got %d", len(fake.cmdCalls))
	}

	var cmds []telebot.Command
	var scope *telebot.CommandScope
	for _, opt := range fake.cmdCalls[0] {
		switch v := opt.(type) {
		case []telebot.Command:
			cmds = v
		case telebot.CommandScope:
			s := v
			scope = &s
		}
	}
	if scope == nil || scope.Type != telebot.CommandScopeAllPrivateChats {
		t.Fatalf("commands must be scoped to all private chats, got %+v", scope)
	}

	got := make([]string, 0, len(cmds))
	for _, c := range cmds {
		if c.Description == "" {
			t.Errorf("command %q has no description", c.Text)
		}
		got = append(got, c.Text)
	}
	want := []string{"start", "help", "new"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("registered commands = %v, want %v", got, want)
	}
}

// Registration has to be wired into the startup path, not merely available.
func TestTelegramChannel_CreateBotRegistersCommands(t *testing.T) {
	var mu sync.Mutex
	var setCommandsBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/setMyCommands") {
			mu.Lock()
			setCommandsBody = string(body)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer srv.Close()

	tg := newTestTelegramChannel()
	tg.apiURL = srv.URL
	tg.offline = true

	if _, err := tg.createBot(context.Background()); err != nil {
		t.Fatalf("createBot returned %v", err)
	}

	mu.Lock()
	got := setCommandsBody
	mu.Unlock()
	if got == "" {
		t.Fatal("createBot never called setMyCommands, so the menu is invisible in Telegram")
	}
	for _, want := range []string{`"start"`, `"help"`, `"new"`, "all_private_chats"} {
		if !strings.Contains(got, want) {
			t.Fatalf("setMyCommands payload missing %s: %s", want, got)
		}
	}
}

// A failure to register the menu is not fatal — the bot must still run.
func TestTelegramChannel_RegisterCommandsFailureIsNotFatal(t *testing.T) {
	tg := newTestTelegramChannel()
	fake := &fakeNotifier{cmdErr: fmt.Errorf("telegram is down")}

	if err := tg.registerCommands(fake); err == nil {
		t.Fatal("registerCommands should surface the underlying error to its caller")
	}

	// The startup path swallows the error (logs and continues) so a Telegram
	// outage cannot stop the bot from running.
	tg.registerCommandsBestEffort(fake)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.cmdCalls) != 2 {
		t.Fatalf("registration should have been attempted twice, got %d", len(fake.cmdCalls))
	}
}

// An unknown command used to be swallowed silently, leaving the user with no
// reply at all.
func TestTelegramChannel_UnknownCommandGetsReply(t *testing.T) {
	srv := newFakeTelegramServer(t)
	bot := srv.bot(t)
	// The unknown-command reply is only sent to an allowlisted sender.
	tg := newTestTelegramChannel("josh")
	tg.mu.Lock()
	tg.bot = bot
	tg.mu.Unlock()

	ctx := bot.NewContext(telebot.Update{Message: &telebot.Message{
		ID:     1,
		Text:   "/nwe",
		Chat:   &telebot.Chat{ID: 1},
		Sender: &telebot.User{ID: 1, Username: "josh"},
	}})

	if err := tg.handleMessage(ctx); err != nil {
		t.Fatalf("handleMessage returned %v", err)
	}

	texts := srv.texts()
	if len(texts) != 1 {
		t.Fatalf("expected one reply to an unknown command, got %v", texts)
	}
	if !strings.Contains(strings.ToLower(texts[0]), "unknown command") {
		t.Fatalf("reply should name the problem, got %q", texts[0])
	}
	for _, c := range []string{"/help", "/new", "/start"} {
		if !strings.Contains(texts[0], c) {
			t.Fatalf("reply should list %s, got %q", c, texts[0])
		}
	}
}

// Known commands are handled by their own telebot handlers; handleMessage must
// keep leaving them alone.
func TestTelegramChannel_KnownCommandNotAnsweredByFallback(t *testing.T) {
	srv := newFakeTelegramServer(t)
	bot := srv.bot(t)
	tg := newTestTelegramChannel()
	tg.mu.Lock()
	tg.bot = bot
	tg.mu.Unlock()

	for _, cmd := range []string{"/start", "/help", "/new", "/help@joshbot", "/new extra args"} {
		ctx := bot.NewContext(telebot.Update{Message: &telebot.Message{
			ID:     1,
			Text:   cmd,
			Chat:   &telebot.Chat{ID: 1},
			Sender: &telebot.User{ID: 1},
		}})
		if err := tg.handleMessage(ctx); err != nil {
			t.Fatalf("handleMessage(%q) returned %v", cmd, err)
		}
	}

	if texts := srv.texts(); len(texts) != 0 {
		t.Fatalf("known commands must not trigger the unknown-command fallback, got %v", texts)
	}
}

// The unknown-command fallback must not answer users outside the allowlist.
func TestTelegramChannel_UnknownCommandRespectsAllowlist(t *testing.T) {
	srv := newFakeTelegramServer(t)
	bot := srv.bot(t)
	tg := newTestTelegramChannel("12345")
	tg.mu.Lock()
	tg.bot = bot
	tg.mu.Unlock()

	ctx := bot.NewContext(telebot.Update{Message: &telebot.Message{
		ID:     1,
		Text:   "/nwe",
		Chat:   &telebot.Chat{ID: 1},
		Sender: &telebot.User{ID: 999, Username: "mallory"},
	}})

	if err := tg.handleMessage(ctx); err != nil {
		t.Fatalf("handleMessage returned %v", err)
	}
	for _, txt := range srv.texts() {
		if strings.Contains(strings.ToLower(txt), "unknown command") {
			t.Fatalf("an unauthorized user must not get the command list, got %q", txt)
		}
	}
}

// A keep-alive must not run forever. If the agent turn dies without producing
// a reply, nothing calls stopTyping — without a cap the goroutine would keep
// calling the Bot API every few seconds for the life of the process, burning
// rate limit and showing the user a permanent "typing…".
func TestTelegramChannel_TypingKeepAliveStopsAtMaxDuration(t *testing.T) {
	tg := newTestTelegramChannel()
	fake := &fakeNotifier{}
	tg.mu.Lock()
	tg.notifier = fake
	tg.typingInterval = 5 * time.Millisecond
	tg.typingMaxDuration = 40 * time.Millisecond
	tg.mu.Unlock()

	tg.startTyping(telebot.ChatID(42))

	// Well past the cap, but stopTyping is deliberately never called.
	time.Sleep(300 * time.Millisecond)
	settled := fake.count()

	time.Sleep(150 * time.Millisecond)
	if grew := fake.count(); grew != settled {
		t.Errorf("keep-alive still running past its max duration: %d then %d", settled, grew)
	}

	// The entry must be cleared too, or this chat could never type again.
	tg.mu.Lock()
	_, present := tg.typingStop[telegramRecipientKeyForTest(telebot.ChatID(42))]
	tg.mu.Unlock()
	if present {
		t.Error("expired keep-alive left its map entry behind")
	}
}

func telegramRecipientKeyForTest(r telebot.Recipient) string {
	k, _ := recipientKey(r)
	return k
}
