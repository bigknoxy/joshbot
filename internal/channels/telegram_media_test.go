package channels

import (
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	telebot "gopkg.in/telebot.v3"
)

// mediaChannel returns a channel wired to a fake notifier so the typing
// keep-alive every media handler starts never touches the network, and with a
// long interval so no test waits on a tick.
func mediaChannel(t *testing.T, allow ...string) *TelegramChannel {
	t.Helper()
	tg := newTestTelegramChannel(allow...)
	tg.mu.Lock()
	tg.notifier = &fakeNotifier{}
	tg.typingInterval = time.Hour
	tg.mu.Unlock()
	t.Cleanup(func() { _ = tg.Stop() })
	return tg
}

// mediaMessage builds a message of the requested kind from one sender. The
// sender's FIRST NAME is deliberately the digit string "4242": every media
// handler is checked against an allowlist that contains that same digit string
// as a numeric entry, so a handler that compares a numeric entry against a
// display name is an authentication bypass and must not match.
func mediaMessage(kind string, senderID int64) *telebot.Message {
	msg := &telebot.Message{
		ID:     11,
		Chat:   &telebot.Chat{ID: 500},
		Sender: &telebot.User{ID: senderID, Username: "someone", FirstName: "4242", LastName: "Impostor"},
	}
	switch kind {
	case "voice":
		msg.Voice = &telebot.Voice{File: telebot.File{FileID: "v1"}, Duration: 3, MIME: "audio/ogg", Caption: "listen"}
	case "audio":
		msg.Audio = &telebot.Audio{File: telebot.File{FileID: "a1"}, Title: "song", Performer: "band", Caption: "track"}
	case "video":
		msg.Video = &telebot.Video{File: telebot.File{FileID: "vid1"}, Width: 2, Height: 3, Caption: "clip"}
	case "sticker":
		msg.Sticker = &telebot.Sticker{File: telebot.File{FileID: "s1"}, Emoji: "🙂", SetName: "pack"}
	case "edited":
		msg.Text = "corrected text"
	}
	return msg
}

func mediaHandler(tg *TelegramChannel, kind string) func(telebot.Context) error {
	switch kind {
	case "voice":
		return tg.handleVoice
	case "audio":
		return tg.handleAudio
	case "video":
		return tg.handleVideo
	case "sticker":
		return tg.handleSticker
	case "edited":
		return tg.handleEdited
	}
	return nil
}

// TestTelegramMediaHandlersForwardToTheBus pins what each media handler puts on
// the bus. The agent only ever sees this projection of the Telegram update, so
// a handler that stopped tagging media_type — or dropped the caption — would
// silently change what the model is asked about with nothing else failing.
func TestTelegramMediaHandlersForwardToTheBus(t *testing.T) {
	cases := []struct {
		kind        string
		wantContent string
		wantFileID  string
	}{
		{kind: "voice", wantContent: "[The user sent a voice message you cannot hear. Its caption]: listen", wantFileID: "v1"},
		{kind: "audio", wantContent: "[The user sent an audio file you cannot hear (song). Its caption]: track", wantFileID: "a1"},
		{kind: "video", wantContent: "[The user sent a video you cannot watch. Its caption]: clip", wantFileID: "vid1"},
		{kind: "edited", wantContent: "[Edited]: corrected text"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			tg := mediaChannel(t, "1234")
			ctx := &fakeCtx{msg: mediaMessage(tc.kind, 1234)}

			if err := mediaHandler(tg, tc.kind)(ctx); err != nil {
				t.Fatalf("handler returned %v", err)
			}

			select {
			case got := <-tg.bus.InboundChannel():
				if got.Content != tc.wantContent {
					t.Errorf("Content = %q, want %q", got.Content, tc.wantContent)
				}
				if got.Metadata["media_type"] != tc.kind {
					t.Errorf("media_type = %v, want %q", got.Metadata["media_type"], tc.kind)
				}
				if got.SenderID != "telegram_1234" {
					t.Errorf("SenderID = %q, want telegram_1234", got.SenderID)
				}
				if got.Metadata["chat_id"] != int64(500) {
					t.Errorf("chat_id = %v, want 500", got.Metadata["chat_id"])
				}
				if tc.wantFileID != "" && got.Metadata["file_id"] != tc.wantFileID {
					t.Errorf("file_id = %v, want %q", got.Metadata["file_id"], tc.wantFileID)
				}
				if len(got.Images) != 0 {
					t.Errorf("non-image media must not carry image bytes, got %d", len(got.Images))
				}
			case <-time.After(200 * time.Millisecond):
				t.Fatal("nothing reached the bus")
			}
		})
	}
}

// TestTelegramMediaHandlersRefuseDisallowedSenders is the security case for the
// media paths. Every handler gates on the allowlist before it does anything,
// and the entry here is all digits while the stranger's *first name* is that
// same string: display names are attacker-chosen, so a numeric entry matching
// one would let anyone drive the agent. Only IsAllowed is tested for that shape
// rule elsewhere — these handlers would keep passing if they compared against
// something else.
func TestTelegramMediaHandlersRefuseDisallowedSenders(t *testing.T) {
	for _, kind := range []string{"voice", "audio", "video", "sticker", "edited"} {
		t.Run(kind, func(t *testing.T) {
			tg := mediaChannel(t, "4242")
			// Sender ID 9999 is not on the list; the first name "4242" is.
			ctx := &fakeCtx{msg: mediaMessage(kind, 9999)}

			if err := mediaHandler(tg, kind)(ctx); err != nil {
				t.Fatalf("handler returned %v", err)
			}

			select {
			case got := <-tg.bus.InboundChannel():
				t.Fatalf("a numeric allowlist entry matched an attacker-chosen display name; "+
					"%s from user 9999 reached the agent as %q", kind, got.Content)
			case <-time.After(100 * time.Millisecond):
			}

			tg.mu.RLock()
			typing := len(tg.typingStop)
			tg.mu.RUnlock()
			if typing != 0 {
				t.Fatalf("a refused %s still started a typing keep-alive, confirming the bot is live to a stranger", kind)
			}
		})
	}
}

// The bus being full must not lose the update silently in a way that panics or
// blocks: the handler logs and returns cleanly.
func TestTelegramMediaHandlerSurvivesAFullBus(t *testing.T) {
	tg := mediaChannel(t, "1234")
	for tg.bus.Send(bus.InboundMessage{Content: "filler", Channel: "telegram"}) {
		// fill the inbound queue
	}

	if err := tg.handleVoice(&fakeCtx{msg: mediaMessage("voice", 1234)}); err != nil {
		t.Fatalf("handleVoice returned %v when the bus was full: %v", err, err)
	}
}

// TestTelegramHandleUnsupportedTellsTheUser pins that an unsupported media type
// gets an answer rather than silence — and that a stranger gets nothing at all,
// which is also how the bot avoids confirming it is live.
func TestTelegramHandleUnsupportedTellsTheUser(t *testing.T) {
	srv := newFakeTelegramServer(t)
	bot := srv.bot(t)

	tg := mediaChannel(t, "1234")
	tg.mu.Lock()
	tg.bot = bot
	tg.mu.Unlock()

	allowed := bot.NewContext(telebot.Update{Message: &telebot.Message{
		ID: 1, Chat: &telebot.Chat{ID: 1},
		Sender: &telebot.User{ID: 1234, Username: "josh"},
	}})
	if err := tg.handleUnsupported(allowed, "venue"); err != nil {
		t.Fatalf("handleUnsupported: %v", err)
	}
	texts := srv.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "venue") {
		t.Fatalf("expected one reply naming the unsupported type, got %v", texts)
	}

	stranger := bot.NewContext(telebot.Update{Message: &telebot.Message{
		ID: 2, Chat: &telebot.Chat{ID: 2},
		Sender: &telebot.User{ID: 9999, Username: "mallory", FirstName: "1234"},
	}})
	if err := tg.handleUnsupported(stranger, "location"); err != nil {
		t.Fatalf("handleUnsupported for a stranger: %v", err)
	}
	if got := srv.texts(); len(got) != 1 {
		t.Fatalf("a disallowed sender was answered: %v", got)
	}
}

// TestTelegramHandleStartMatchesTheCommandMenu pins the two things about /start
// that drift: it must answer at all, and its listing must name every command in
// botCommands. botCommands is the single source for the Telegram menu and the
// unknown-command fallback, so a command added there but missing from the help
// text is invisible to users who read /help.
func TestTelegramHandleStartMatchesTheCommandMenu(t *testing.T) {
	srv := newFakeTelegramServer(t)
	bot := srv.bot(t)

	tg := mediaChannel(t, "1234")
	tg.mu.Lock()
	tg.bot = bot
	tg.mu.Unlock()

	ctx := bot.NewContext(telebot.Update{Message: &telebot.Message{
		ID: 1, Chat: &telebot.Chat{ID: 1},
		Sender: &telebot.User{ID: 1234, Username: "josh"},
	}})
	if err := tg.handleStart(ctx); err != nil {
		t.Fatalf("handleStart: %v", err)
	}

	texts := srv.texts()
	if len(texts) != 1 {
		t.Fatalf("expected exactly one help message, got %v", texts)
	}
	for _, cmd := range botCommands {
		if !strings.Contains(texts[0], "/"+cmd.Text) {
			t.Errorf("/%s is in the Telegram command menu but not in the help text", cmd.Text)
		}
	}
	if modes := srv.modes(); len(modes) != 1 || !strings.EqualFold(modes[0], string(telebot.ModeMarkdown)) {
		t.Errorf("help text should be sent as Markdown, parse modes = %v", modes)
	}
}

// TestTelegramHandleCallbackAnswersAndForwards pins both halves of a button
// press: Telegram requires the callback query to be answered or the client
// spins forever, and the payload must reach the agent.
func TestTelegramHandleCallbackAnswersAndForwards(t *testing.T) {
	srv := newFakeTelegramServer(t)
	bot := srv.bot(t)

	tg := mediaChannel(t, "1234")
	tg.mu.Lock()
	tg.bot = bot
	tg.mu.Unlock()

	cb := &telebot.Callback{
		ID:      "cb1",
		Sender:  &telebot.User{ID: 1234, Username: "josh"},
		Message: &telebot.Message{ID: 8, Chat: &telebot.Chat{ID: 77}},
		Data:    "pick:2",
	}
	if err := tg.handleCallback(bot.NewContext(telebot.Update{Callback: cb})); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}

	select {
	case got := <-tg.bus.InboundChannel():
		if got.Content != "[Callback: pick:2]" {
			t.Errorf("Content = %q, want [Callback: pick:2]", got.Content)
		}
		if got.Metadata["callback_data"] != "pick:2" || got.Metadata["media_type"] != "callback" {
			t.Errorf("callback metadata lost: %+v", got.Metadata)
		}
		if got.Metadata["chat_id"] != int64(77) {
			t.Errorf("chat_id = %v, want 77", got.Metadata["chat_id"])
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("the button press never reached the agent")
	}

	// A stranger's press must not reach the agent.
	strangerCB := &telebot.Callback{
		ID:      "cb2",
		Sender:  &telebot.User{ID: 9999, Username: "mallory", FirstName: "1234"},
		Message: &telebot.Message{ID: 9, Chat: &telebot.Chat{ID: 78}},
		Data:    "pick:3",
	}
	if err := tg.handleCallback(bot.NewContext(telebot.Update{Callback: strangerCB})); err != nil {
		t.Fatalf("handleCallback for a stranger: %v", err)
	}
	select {
	case got := <-tg.bus.InboundChannel():
		t.Fatalf("a disallowed sender's button press reached the agent: %q", got.Content)
	case <-time.After(100 * time.Millisecond):
	}
}

// A captionless voice/audio/video carries nothing the agent can perceive, so
// the channel answers honestly itself — threaded, no agent turn — instead of
// forwarding a placeholder the model would answer confidently about nothing.
// A sticker is ignored outright: it accompanies conversation, it is not a
// question, and it used to spend a full LLM turn on a confused reply.
func TestImperceptibleMediaGetsAnHonestReplyNotAnAgentTurn(t *testing.T) {
	cases := []struct {
		kind     string
		wantNote string
	}{
		{kind: "voice", wantNote: "can't listen to voice messages"},
		{kind: "audio", wantNote: "can't listen to audio"},
		{kind: "video", wantNote: "can't watch videos"},
		{kind: "sticker", wantNote: ""}, // silent ignore
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			srv := newFakeTelegramServer(t)
			bot := srv.bot(t)
			tg := mediaChannel(t, "1234")
			tg.mu.Lock()
			tg.bot = bot
			tg.mu.Unlock()

			msg := mediaMessage(tc.kind, 1234)
			switch tc.kind {
			case "voice":
				msg.Voice.Caption = ""
			case "audio":
				msg.Audio.Caption = ""
			case "video":
				msg.Video.Caption = ""
			}
			if err := mediaHandler(tg, tc.kind)(bot.NewContext(telebot.Update{Message: msg})); err != nil {
				t.Fatalf("handler: %v", err)
			}

			select {
			case got := <-tg.bus.InboundChannel():
				t.Fatalf("an imperceptible %s reached the agent as %q", tc.kind, got.Content)
			case <-time.After(100 * time.Millisecond):
			}

			texts := srv.texts()
			if tc.wantNote == "" {
				if len(texts) != 0 {
					t.Fatalf("a sticker was answered: %v", texts)
				}
				return
			}
			if len(texts) != 1 || !strings.Contains(texts[0], tc.wantNote) {
				t.Fatalf("honest reply = %v, want one message containing %q", texts, tc.wantNote)
			}
		})
	}
}
