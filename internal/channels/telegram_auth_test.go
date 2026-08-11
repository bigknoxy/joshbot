package channels

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/telebot.v3"
)

// The plain-text path is the one an unauthorized user actually reaches, and it
// is where the allowlist decision is made for ordinary chat. Only the command
// paths (/new, unknown-command) had denial tests; handleMessage's own check was
// unpinned, so deleting it — or inverting it during a refactor — would have let
// any Telegram user drive the agent with no test failing.
//
// Assert the denial, not the permission: nothing reaches the bus, and the user
// is told they are not authorized.
func TestTelegramChannel_UnauthorizedTextNeverReachesTheBus(t *testing.T) {
	srv := newFakeTelegramServer(t)
	bot := srv.bot(t)
	tg := newTestTelegramChannel("12345")
	tg.mu.Lock()
	tg.bot = bot
	tg.mu.Unlock()

	ctx := bot.NewContext(telebot.Update{Message: &telebot.Message{
		ID:     1,
		Text:   "what is the admin password",
		Chat:   &telebot.Chat{ID: 1},
		Sender: &telebot.User{ID: 999, Username: "mallory", FirstName: "12345"},
	}})

	if err := tg.handleMessage(ctx); err != nil {
		t.Fatalf("handleMessage returned %v", err)
	}

	select {
	case msg := <-tg.bus.InboundChannel():
		t.Fatalf("an unauthorized user's message reached the agent: %q", msg.Content)
	case <-time.After(100 * time.Millisecond):
	}

	texts := srv.texts()
	if len(texts) != 1 || !strings.Contains(strings.ToLower(texts[0]), "not authorized") {
		t.Fatalf("expected a single refusal telling the user they are not authorized, got %v", texts)
	}
}

// The allowlist entry here is all digits; the sender's FIRST NAME is that same
// string while their numeric ID is different. Display names are chosen by the
// attacker, so a numeric entry matching one is an authentication bypass. This
// pins the shape rule on the path that carries ordinary messages — the existing
// shape tests call IsAllowed directly, which would keep passing if handleMessage
// were changed to compare against something else.
func TestTelegramChannel_HandleMessageNumericEntryDoesNotMatchDisplayName(t *testing.T) {
	srv := newFakeTelegramServer(t)
	bot := srv.bot(t)
	tg := newTestTelegramChannel("777")
	tg.mu.Lock()
	tg.bot = bot
	tg.mu.Unlock()

	ctx := bot.NewContext(telebot.Update{Message: &telebot.Message{
		ID:     1,
		Text:   "hello",
		Chat:   &telebot.Chat{ID: 1},
		Sender: &telebot.User{ID: 42, Username: "777", FirstName: "777", LastName: "777"},
	}})

	if err := tg.handleMessage(ctx); err != nil {
		t.Fatalf("handleMessage returned %v", err)
	}

	select {
	case msg := <-tg.bus.InboundChannel():
		t.Fatalf("a display name spoofing a numeric allowlist entry was authenticated: %q", msg.Content)
	case <-time.After(100 * time.Millisecond):
	}
}

// A refusal must not be a typing indicator: startTyping runs a goroutine that
// re-sends a chat action every 4s until stopped, and Send is what stops it. A
// denied user never produces a reply, so starting typing before the allowlist
// check would leak a goroutine per rejected message — an unauthenticated remote
// party controlling how many. Assert the denial leaves no typing goroutine.
func TestTelegramChannel_DeniedMessageStartsNoTypingGoroutine(t *testing.T) {
	srv := newFakeTelegramServer(t)
	bot := srv.bot(t)
	tg := newTestTelegramChannel("12345")
	tg.mu.Lock()
	tg.bot = bot
	tg.mu.Unlock()

	ctx := bot.NewContext(telebot.Update{Message: &telebot.Message{
		ID:     1,
		Text:   "hello",
		Chat:   &telebot.Chat{ID: 1},
		Sender: &telebot.User{ID: 999},
	}})
	if err := tg.handleMessage(ctx); err != nil {
		t.Fatalf("handleMessage returned %v", err)
	}

	tg.mu.Lock()
	n := len(tg.typingStop)
	tg.mu.Unlock()
	if n != 0 {
		t.Fatalf("a denied message left %d typing goroutine(s) running", n)
	}
}
