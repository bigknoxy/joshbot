package channels

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	telebot "gopkg.in/telebot.v3"
)

func newTestTelegramChannel(allowFrom ...string) *TelegramChannel {
	return NewTelegramChannel(bus.NewMessageBus(), &config.TelegramConfig{
		Enabled:   true,
		Token:     "test-token",
		AllowFrom: allowFrom,
	})
}

func TestTelegramChannel_ConvertToInboundMessage(t *testing.T) {
	tg := newTestTelegramChannel()

	msg := &telebot.Message{
		ID:     42,
		Text:   "hello there",
		Chat:   &telebot.Chat{ID: 999},
		Sender: &telebot.User{ID: 123, Username: "josh", FirstName: "Josh", LastName: "Knox"},
	}

	got := tg.convertToInboundMessage(msg)

	if got.SenderID != "telegram_123" {
		t.Errorf("SenderID = %q, want telegram_123", got.SenderID)
	}
	if got.Content != "hello there" {
		t.Errorf("Content = %q, want %q", got.Content, "hello there")
	}
	if got.Channel != "telegram" {
		t.Errorf("Channel = %q, want telegram", got.Channel)
	}
	if got.Timestamp.IsZero() {
		t.Error("Timestamp should be set")
	}

	want := map[string]any{
		"message_id": 42,
		"chat_id":    int64(999),
		"username":   "josh",
		"first_name": "Josh",
		"last_name":  "Knox",
		"is_command": false,
	}
	for k, v := range want {
		if got.Metadata[k] != v {
			t.Errorf("Metadata[%q] = %v (%T), want %v (%T)", k, got.Metadata[k], got.Metadata[k], v, v)
		}
	}
}

func TestTelegramChannel_ConvertToInboundMessage_DetectsCommand(t *testing.T) {
	tg := newTestTelegramChannel()

	msg := &telebot.Message{
		ID:     1,
		Text:   "/help",
		Chat:   &telebot.Chat{ID: 5},
		Sender: &telebot.User{ID: 7},
	}

	got := tg.convertToInboundMessage(msg)
	if got.Metadata["is_command"] != true {
		t.Error("expected is_command to be true for a message starting with /")
	}
}

// Every send path must fail cleanly rather than panic when Start has not run.
func TestTelegramChannel_SendPathsWithoutBot(t *testing.T) {
	tg := newTestTelegramChannel()
	recipient := telebot.ChatID(1)

	t.Run("Send", func(t *testing.T) {
		err := tg.Send(bus.OutboundMessage{Channel: "telegram", ChannelID: "1", Content: "hi"})
		if err == nil {
			t.Error("expected an error when the bot is not initialized")
		}
	})

	t.Run("SendPhoto", func(t *testing.T) {
		if err := tg.SendPhoto(recipient, &telebot.Photo{}, "caption", nil); err == nil {
			t.Error("expected an error when the bot is not initialized")
		}
	})

	t.Run("SendDocument", func(t *testing.T) {
		if err := tg.SendDocument(recipient, &telebot.Document{}, "caption", nil); err == nil {
			t.Error("expected an error when the bot is not initialized")
		}
	})

	t.Run("EditMessage", func(t *testing.T) {
		if err := tg.EditMessage(recipient, 5, "new text", nil); err == nil {
			t.Error("expected an error when the bot is not initialized")
		}
	})

	// These return nothing; the assertion is that they do not panic.
	t.Run("sendTyping", func(t *testing.T) {
		tg.sendTyping(recipient)
	})

	t.Run("downloadFile", func(t *testing.T) {
		tg.downloadFile(telebot.File{FileID: "abc", UniqueID: "u1"}, "photo", 1, 2)
	})
}

func TestTelegramChannel_StopWhenNotRunning(t *testing.T) {
	tg := newTestTelegramChannel()
	if err := tg.Stop(); err != nil {
		t.Errorf("Stop on a non-running channel should be a no-op, got %v", err)
	}
	// Calling Stop again must stay safe.
	if err := tg.Stop(); err != nil {
		t.Errorf("second Stop should also be a no-op, got %v", err)
	}
}

func TestTelegramChannel_StopWhenRunning(t *testing.T) {
	tg := newTestTelegramChannel()

	tg.mu.Lock()
	tg.running = true
	tg.mu.Unlock()

	if err := tg.Stop(); err != nil {
		t.Fatalf("Stop returned %v", err)
	}

	tg.mu.RLock()
	running := tg.running
	tg.mu.RUnlock()
	if running {
		t.Error("running should be false after Stop")
	}

	select {
	case <-tg.stopCh:
	default:
		t.Error("stopCh should be closed after Stop")
	}
}

// TestTelegramChannel_StopStopsPollerGoroutine exercises the real
// Start-shaped path: it hands a live *telebot.Bot polling a fake Telegram
// API server to runBot (the same way Start() does), then calls Stop() and
// asserts the goroutine blocked inside runBot (via bot.Start()) actually
// returns.
//
// bot.Close() (the Telegram Bot API "close" session-teardown RPC) never
// touches the internal channel that unblocks bot.Start()'s event loop, so
// runBot leaks the polling goroutine forever when Stop() calls it. Only
// bot.Stop() does. Before the fix, this test times out waiting for runBot
// to return.
func TestTelegramChannel_StopStopsPollerGoroutine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer srv.Close()

	bot, err := telebot.NewBot(telebot.Settings{
		Token:   "test-token",
		URL:     srv.URL,
		Poller:  &telebot.LongPoller{Timeout: 10 * time.Millisecond},
		Offline: true, // skip the getMe() network call NewBot would otherwise make
	})
	if err != nil {
		t.Fatalf("failed to create test bot: %v", err)
	}

	tg := newTestTelegramChannel()
	tg.mu.Lock()
	tg.running = true
	tg.bot = bot
	tg.mu.Unlock()

	done := make(chan struct{})
	go func() {
		tg.runBot(context.Background(), bot)
		close(done)
	}()

	// Give the poller goroutine time to actually start polling the fake
	// server before we ask it to stop.
	time.Sleep(150 * time.Millisecond)

	if err := tg.Stop(); err != nil {
		t.Fatalf("Stop returned %v", err)
	}

	select {
	case <-done:
		// runBot returned: bot.Start() unblocked and the poller goroutine exited.
	case <-time.After(2 * time.Second):
		t.Fatal("runBot did not return after Stop(); the underlying poller goroutine leaked")
	}
}

func TestTelegramChannel_ConsumeOutboundStopsOnContextCancel(t *testing.T) {
	tg := newTestTelegramChannel()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		tg.consumeOutbound(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consumeOutbound did not return after the context was cancelled")
	}
}

func TestTelegramChannel_ConsumeOutboundStopsOnStopCh(t *testing.T) {
	tg := newTestTelegramChannel()

	done := make(chan struct{})
	go func() {
		tg.consumeOutbound(context.Background())
		close(done)
	}()

	close(tg.stopCh)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consumeOutbound did not return after stopCh was closed")
	}
}

// A send failure must be logged and the consumer must keep running, otherwise
// one bad message would silently kill the outbound path.
func TestTelegramChannel_ConsumeOutboundSurvivesSendFailure(t *testing.T) {
	mb := bus.NewMessageBus()
	tg := NewTelegramChannel(mb, &config.TelegramConfig{Enabled: true, Token: "t"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		tg.consumeOutbound(ctx)
		close(done)
	}()

	// The bot is nil, so Send fails for this message.
	mb.OutboundChan() <- bus.OutboundMessage{Channel: "telegram", ChannelID: "1", Content: "fails"}
	// A message for another channel is ignored entirely.
	mb.OutboundChan() <- bus.OutboundMessage{Channel: "cli", ChannelID: "1", Content: "ignored"}
	// "all" is treated as addressed to this channel.
	mb.OutboundChan() <- bus.OutboundMessage{Channel: "all", ChannelID: "1", Content: "broadcast"}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consumeOutbound did not return after the context was cancelled")
	}
}

func TestTelegramChannel_BuildReplyMarkup_InlineButtonTypes(t *testing.T) {
	tg := newTestTelegramChannel()

	markup := map[string]any{
		"inline_keyboard": [][]map[string]any{
			{
				{"text": "Open", "url": "https://example.com"},
				{"text": "Press", "callback_data": "cb:1"},
			},
			{
				{"text": "Second row"},
			},
		},
	}

	rm := tg.buildReplyMarkup(markup)
	if len(rm.InlineKeyboard) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rm.InlineKeyboard))
	}
	if len(rm.InlineKeyboard[0]) != 2 {
		t.Fatalf("expected 2 buttons in the first row, got %d", len(rm.InlineKeyboard[0]))
	}
	if rm.InlineKeyboard[0][0].URL != "https://example.com" {
		t.Errorf("URL = %q, want https://example.com", rm.InlineKeyboard[0][0].URL)
	}
	if rm.InlineKeyboard[0][1].Data != "cb:1" {
		t.Errorf("Data = %q, want cb:1", rm.InlineKeyboard[0][1].Data)
	}
	if rm.InlineKeyboard[1][0].Text != "Second row" {
		t.Errorf("Text = %q, want %q", rm.InlineKeyboard[1][0].Text, "Second row")
	}
}

func TestTelegramChannel_BuildReplyMarkup_ReplyKeyboard(t *testing.T) {
	tg := newTestTelegramChannel()

	markup := map[string]any{
		"keyboard": [][]map[string]any{
			{
				{"text": "Share contact", "request_contact": true},
				{"text": "Share location", "request_location": true},
				{"text": "Plain"},
			},
		},
		"resize_keyboard":   true,
		"one_time_keyboard": true,
		"selective":         true,
	}

	rm := tg.buildReplyMarkup(markup)
	if len(rm.ReplyKeyboard) != 1 || len(rm.ReplyKeyboard[0]) != 3 {
		t.Fatalf("unexpected reply keyboard shape: %+v", rm.ReplyKeyboard)
	}
	if !rm.ReplyKeyboard[0][0].Contact {
		t.Error("expected the first button to request a contact")
	}
	if !rm.ReplyKeyboard[0][1].Location {
		t.Error("expected the second button to request a location")
	}
	if rm.ReplyKeyboard[0][2].Contact || rm.ReplyKeyboard[0][2].Location {
		t.Error("the plain button should request neither contact nor location")
	}
	if !rm.ResizeKeyboard || !rm.OneTimeKeyboard || !rm.Selective {
		t.Errorf("keyboard flags not applied: %+v", rm)
	}
}

func TestTelegramChannel_BuildReplyMarkup_IgnoresWrongTypes(t *testing.T) {
	tg := newTestTelegramChannel()

	// Values of the wrong type must be skipped rather than panic.
	rm := tg.buildReplyMarkup(map[string]any{
		"inline_keyboard":   "not a keyboard",
		"keyboard":          42,
		"resize_keyboard":   "yes",
		"one_time_keyboard": nil,
		"selective":         0,
	})

	if rm == nil {
		t.Fatal("expected a non-nil markup")
	}
	if len(rm.InlineKeyboard) != 0 || len(rm.ReplyKeyboard) != 0 {
		t.Errorf("expected empty keyboards, got %+v", rm)
	}
	if rm.ResizeKeyboard || rm.OneTimeKeyboard || rm.Selective {
		t.Error("flags should stay false when the values are not booleans")
	}
}

func TestTelegramChannel_IsAllowedVariants(t *testing.T) {
	t.Run("empty allowlist permits everyone", func(t *testing.T) {
		tg := newTestTelegramChannel()
		if !tg.IsAllowed(1, "anyone", "Any", "One") {
			t.Error("expected an empty allowlist to permit everyone")
		}
	})

	t.Run("matches username case-insensitively and ignores @", func(t *testing.T) {
		tg := newTestTelegramChannel("@Josh")
		if !tg.IsAllowed(1, "josh", "", "") {
			t.Error("expected @Josh to match the username josh")
		}
		if tg.IsAllowed(2, "someone", "", "") {
			t.Error("expected an unlisted username to be rejected")
		}
	})

	t.Run("matches first and last name", func(t *testing.T) {
		tg := newTestTelegramChannel("Josh Knox")
		if !tg.IsAllowed(1, "", "Josh", "Knox") {
			t.Error("expected the full name to match")
		}
		if !tg.IsAllowed(1, "", "josh", "knox") {
			t.Error("expected the name match to be case-insensitive")
		}
		if tg.IsAllowed(1, "", "Josh", "") {
			t.Error("expected a first name alone not to match a full-name entry")
		}
	})

	// The README tells users to put their numeric Telegram ID in allow_from,
	// and it is the only identifier a user cannot change, so it must match.
	t.Run("matches numeric user id", func(t *testing.T) {
		tg := newTestTelegramChannel("12345")
		if !tg.IsAllowed(12345, "", "", "") {
			t.Error("expected a numeric user ID in allow_from to match")
		}
		if !tg.IsAllowed(12345, "someoneelse", "Other", "Name") {
			t.Error("expected the ID to match regardless of the display name")
		}
		if tg.IsAllowed(999, "", "", "") {
			t.Error("expected a different user ID to be rejected")
		}
	})

	t.Run("mixed id and username entries", func(t *testing.T) {
		tg := newTestTelegramChannel("12345", "@josh")
		if !tg.IsAllowed(12345, "", "", "") {
			t.Error("expected the numeric entry to match")
		}
		if !tg.IsAllowed(777, "josh", "", "") {
			t.Error("expected the username entry to match")
		}
		if tg.IsAllowed(777, "mallory", "", "") {
			t.Error("expected an unlisted user to be rejected")
		}
	})
}

func TestValidateTokenEmpty(t *testing.T) {
	if err := ValidateToken(""); err == nil {
		t.Error("expected an empty token to be rejected")
	}
}

// splitMessage must close and reopen a fenced code block across a split so
// neither half is sent to Telegram with unbalanced fences.
func TestSplitMessage_ReopensCodeFenceAcrossSplit(t *testing.T) {
	content := "```go\n" + strings.Repeat("x\n", 200) + "```"
	parts := splitMessage(content, 100)

	if len(parts) < 2 {
		t.Fatalf("expected the content to be split, got %d part(s)", len(parts))
	}
	for i, p := range parts {
		if strings.Count(p, "```")%2 != 0 {
			t.Errorf("part %d has unbalanced code fences:\n%s", i, p)
		}
	}
}

// Regression: content opening a code fence with no newline inside the first
// chunk used to make the remainder grow by exactly as much as the split
// consumed, so splitMessage looped forever and hung the outbound consumer.
func TestSplitMessage_TerminatesOnUnbalancedFence(t *testing.T) {
	inputs := []string{
		"```" + strings.Repeat("a", 500),
		"```\n" + strings.Repeat("a", 500),
		"```\n\n" + strings.Repeat("a", 500),
		"a```" + strings.Repeat("b", 300),
		strings.Repeat("`", 50) + strings.Repeat("c", 300),
	}

	for _, in := range inputs {
		done := make(chan []string, 1)
		go func(s string) { done <- splitMessage(s, 100) }(in)

		select {
		case parts := <-done:
			if len(parts) < 2 {
				t.Errorf("expected %d bytes to be split, got %d part(s)", len(in), len(parts))
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("splitMessage did not terminate for input of %d bytes", len(in))
		}
	}
}

// Every part must fit the limit. Telegram rejects an oversized message
// outright rather than truncating it, so a part of maxLen+4 fails the whole
// send.
func TestSplitMessage_EveryPartFitsTheLimit(t *testing.T) {
	cases := []struct {
		name    string
		content string
		maxLen  int
	}{
		{"unclosed fence, no newlines", "```" + strings.Repeat("a", 500), 100},
		{"fenced block with newlines", "```go\n" + strings.Repeat("x\n", 200) + "```", 100},
		{"long plain text", strings.Repeat("word ", 500), 100},
		{"at telegram's real limit", "```" + strings.Repeat("a", 5000), TelegramMaxMessageLen},
		{"many short fences", strings.Repeat("```\n", 200), 100},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parts := splitMessage(tc.content, tc.maxLen)
			for i, p := range parts {
				if len(p) > tc.maxLen {
					t.Errorf("part %d is %d bytes, over the %d byte limit", i, len(p), tc.maxLen)
				}
			}
		})
	}
}

// A hard split must not cut a multibyte character in half.
func TestSplitMessage_PreservesUTF8(t *testing.T) {
	cases := map[string]string{
		"japanese":   strings.Repeat("漢", 3000),
		"emoji":      strings.Repeat("🙂", 2000),
		"mixed":      strings.Repeat("aé漢🙂", 1000),
		"in a fence": "```\n" + strings.Repeat("漢", 3000),
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			for _, maxLen := range []int{97, 100, 4096} {
				for i, p := range splitMessage(content, maxLen) {
					if !utf8.ValidString(p) {
						t.Errorf("maxLen=%d part %d is not valid UTF-8", maxLen, i)
					}
				}
			}
		})
	}
}

// The two termination guards are individually mutation-undetectable by a
// "does it finish" test — removing either one alone still terminates. This
// asserts the exact split points, so weakening either guard fails here rather
// than silently halving the safety margin.
func TestSplitMessage_ExactSplitPoints(t *testing.T) {
	cases := []struct {
		name    string
		content string
		maxLen  int
		want    []string
	}{
		{
			// No newline to split on, so every boundary is a hard split with
			// the fence closed and reopened around it.
			name:    "unclosed fence without newlines",
			content: "```" + strings.Repeat("a", 60),
			maxLen:  20,
			want: []string{
				"```aaaaaaaaaaaaa\n```",
				"```\naaaaaaaaaaaa\n```",
				"```\naaaaaaaaaaaa\n```",
				"```\naaaaaaaaaaaa\n```",
				"```\naaaaaaaaaaa",
			},
		},
		{
			// Newlines are preferred split points when they are far enough in
			// to leave room for the reopening fence.
			name:    "unclosed fence with newlines",
			content: "```\n" + strings.Repeat("ab\n", 12),
			maxLen:  20,
			want: []string{
				"```\nab\nab\nab\nab\n```",
				"```\n\nab\nab\nab\n```",
				"```\n\nab\nab\nab\nab\nab\n",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parts := splitMessage(tc.content, tc.maxLen)
			if len(parts) != len(tc.want) {
				t.Fatalf("got %d parts %q, want %d parts %q", len(parts), parts, len(tc.want), tc.want)
			}
			for i := range tc.want {
				if parts[i] != tc.want[i] {
					t.Errorf("part %d = %q, want %q", i, parts[i], tc.want[i])
				}
			}
		})
	}
}

// Rejoining the parts must reproduce the original once the inserted fences
// are removed — no content may be silently dropped.
func TestSplitMessage_PreservesContent(t *testing.T) {
	content := "```go\n" + strings.Repeat("line of code\n", 400) + "```"
	parts := splitMessage(content, 200)

	rejoined := strings.Join(parts, "")
	stripped := strings.ReplaceAll(rejoined, "\n``````\n", "")
	if stripped != content {
		t.Errorf("content was not preserved across the split:\ngot  %d bytes\nwant %d bytes", len(stripped), len(content))
	}
}

func TestSplitMessage_DegenerateLimits(t *testing.T) {
	for _, maxLen := range []int{-1, 0} {
		if parts := splitMessage("some content", maxLen); len(parts) != 1 {
			t.Errorf("maxLen=%d: expected the content returned whole, got %d parts", maxLen, len(parts))
		}
	}

	// Tiny limits must still terminate and still make progress.
	for _, maxLen := range []int{1, 2, 3, 4, 5, 8} {
		done := make(chan []string, 1)
		go func(m int) { done <- splitMessage("````"+strings.Repeat("a", 20), m) }(maxLen)
		select {
		case parts := <-done:
			if len(parts) == 0 {
				t.Errorf("maxLen=%d produced no parts", maxLen)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("maxLen=%d did not terminate", maxLen)
		}
	}
}
