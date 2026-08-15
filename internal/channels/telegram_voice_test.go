package channels

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	telebot "gopkg.in/telebot.v3"
)

func voiceMsg(caption string, duration int, size int64) *telebot.Message {
	return &telebot.Message{
		ID:     12,
		Chat:   &telebot.Chat{ID: 500},
		Sender: &telebot.User{ID: 1234, Username: "josh"},
		Voice: &telebot.Voice{
			File:     telebot.File{FileID: "v9", FileSize: size},
			Duration: duration,
			MIME:     "audio/ogg",
			Caption:  caption,
		},
	}
}

// With a transcriber wired, a voice note becomes a transcribed agent turn —
// the whole point of #276 — and the caption rides along.
func TestVoiceMessageIsTranscribedIntoTheTurn(t *testing.T) {
	tg, mb, downloads := imageChannel(t, []string{"1234"}, []byte("OGGDATA"), nil)
	var gotAudio string
	tg.SetTranscriber(func(_ context.Context, audio []byte, filename string) (string, error) {
		gotAudio = filename + ":" + string(audio)
		return "buy milk on the way home", nil
	})

	if err := tg.handleVoice(&fakeCtx{msg: voiceMsg("from the car", 5, 7)}); err != nil {
		t.Fatalf("handleVoice: %v", err)
	}
	if *downloads != 1 {
		t.Fatalf("downloads = %d, want 1", *downloads)
	}
	if gotAudio != "voice.ogg:OGGDATA" {
		t.Errorf("transcriber input = %q", gotAudio)
	}
	select {
	case m := <-mb.InboundChannel():
		if !strings.Contains(m.Content, "[Voice message, transcribed]: buy milk on the way home") {
			t.Errorf("Content = %q, want the transcript", m.Content)
		}
		if !strings.Contains(m.Content, "from the car") {
			t.Errorf("Content = %q, want the caption kept", m.Content)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("the transcribed voice message never reached the bus")
	}
}

// A failed transcription ends the turn with an error in the chat — a voice
// note silently degraded to a placeholder gets a confident answer about
// content nobody heard.
func TestFailedTranscriptionEndsTheTurnNotTheHonesty(t *testing.T) {
	srv := newFakeTelegramServer(t)
	bot := srv.bot(t)
	tg, mb, _ := imageChannel(t, []string{"1234"}, []byte("OGGDATA"), nil)
	tg.mu.Lock()
	tg.bot = bot
	tg.mu.Unlock()
	tg.SetTranscriber(func(context.Context, []byte, string) (string, error) {
		return "", errors.New("provider says no")
	})

	if err := tg.handleVoice(bot.NewContext(telebot.Update{Message: voiceMsg("", 5, 7)})); err != nil {
		t.Fatalf("handleVoice: %v", err)
	}
	select {
	case m := <-mb.InboundChannel():
		t.Fatalf("a failed transcription reached the agent as %q", m.Content)
	case <-time.After(100 * time.Millisecond):
	}
	texts := srv.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "couldn't transcribe") {
		t.Fatalf("error reply = %v", texts)
	}
}

// Overlong and oversized voice notes are refused from the declared metadata,
// before any download is spent.
func TestOverlimitVoiceIsRefusedBeforeDownload(t *testing.T) {
	cases := []struct {
		name     string
		duration int
		size     int64
	}{
		{name: "too long", duration: maxVoiceDuration + 1, size: 7},
		{name: "too big", duration: 5, size: maxVoiceBytes + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFakeTelegramServer(t)
			bot := srv.bot(t)
			tg, mb, downloads := imageChannel(t, []string{"1234"}, []byte("OGGDATA"), nil)
			tg.mu.Lock()
			tg.bot = bot
			tg.mu.Unlock()
			tg.SetTranscriber(func(context.Context, []byte, string) (string, error) {
				return "should never run", nil
			})

			if err := tg.handleVoice(bot.NewContext(telebot.Update{Message: voiceMsg("", tc.duration, tc.size)})); err != nil {
				t.Fatalf("handleVoice: %v", err)
			}
			if *downloads != 0 {
				t.Fatal("an over-limit voice note was downloaded")
			}
			select {
			case m := <-mb.InboundChannel():
				t.Fatalf("an over-limit voice note reached the agent as %q", m.Content)
			case <-time.After(100 * time.Millisecond):
			}
			if texts := srv.texts(); len(texts) != 1 || !strings.Contains(texts[0], "limit") {
				t.Fatalf("refusal = %v, want one message naming the limit", texts)
			}
		})
	}
}

// Without a transcriber the honesty rules stand: captionless is refused with
// a note that transcription can be enabled, a caption is forwarded framed.
func TestVoiceWithoutTranscriberKeepsTheHonestRefusal(t *testing.T) {
	srv := newFakeTelegramServer(t)
	bot := srv.bot(t)
	tg, mb, downloads := imageChannel(t, []string{"1234"}, []byte("OGGDATA"), nil)
	tg.mu.Lock()
	tg.bot = bot
	tg.mu.Unlock()

	if err := tg.handleVoice(bot.NewContext(telebot.Update{Message: voiceMsg("", 5, 7)})); err != nil {
		t.Fatalf("handleVoice: %v", err)
	}
	if *downloads != 0 {
		t.Fatal("a voice note was downloaded with no transcriber wired")
	}
	select {
	case m := <-mb.InboundChannel():
		t.Fatalf("a captionless voice note reached the agent as %q", m.Content)
	case <-time.After(100 * time.Millisecond):
	}
	if texts := srv.texts(); len(texts) != 1 || !strings.Contains(texts[0], "stt") {
		t.Fatalf("refusal = %v, want it to name the stt config", texts)
	}

	if err := tg.handleVoice(&fakeCtx{msg: voiceMsg("call the dentist", 5, 7)}); err != nil {
		t.Fatalf("handleVoice with caption: %v", err)
	}
	select {
	case m := <-mb.InboundChannel():
		want := fmt.Sprintf("[The user sent a voice message you cannot hear. Its caption]: %s", "call the dentist")
		if m.Content != want {
			t.Errorf("Content = %q, want %q", m.Content, want)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("the captioned voice message never reached the bus")
	}
}
