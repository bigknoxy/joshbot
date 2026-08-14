package channels

import (
	"context"
	"fmt"
	"io"

	"github.com/bigknoxy/joshbot/internal/log"
	"gopkg.in/telebot.v3"
)

// maxVoiceBytes bounds a voice-note download. The public Bot API refuses bot
// downloads over 20MB anyway, so anything larger is refused from the declared
// size before spending any transfer.
const maxVoiceBytes = 20 << 20

// maxVoiceDuration bounds what is worth transcribing, from Telegram's
// declared duration. Ten minutes of speech is far past a chat question, and
// the cap is cheap insurance on per-minute transcription pricing.
const maxVoiceDuration = 600 // seconds

// SetTranscriber wires voice-message transcription. Call once at setup,
// before Start — the field is read without the lock on the message path.
// A nil transcriber (the default) leaves the honest-refusal behaviour.
func (t *TelegramChannel) SetTranscriber(fn func(ctx context.Context, audio []byte, filename string) (string, error)) {
	t.transcriber = fn
}

// readVoice downloads one voice note, bounded. The caller must have run the
// allowlist check first, for the same reason as fetchImage: the download
// carries a file id to the Bot API on the sender's behalf.
func (t *TelegramChannel) readVoice(voice *telebot.Voice) ([]byte, error) {
	if voice.Duration > maxVoiceDuration {
		return nil, fmt.Errorf("the voice message is %d seconds long — over the %d second limit; send a shorter one", voice.Duration, maxVoiceDuration)
	}
	if voice.FileSize > maxVoiceBytes {
		return nil, fmt.Errorf("the voice message is %d bytes — over the %d byte limit", voice.FileSize, maxVoiceBytes)
	}

	open := t.download
	if open == nil {
		if t.bot == nil {
			return nil, fmt.Errorf("no bot available to download the voice message")
		}
		open = t.bot.File
	}
	rc, err := open(&voice.File)
	if err != nil {
		return nil, fmt.Errorf("download voice message: %w", err)
	}
	defer func() { _ = rc.Close() }()

	// One byte past the cap distinguishes over-limit from at-limit.
	data, err := io.ReadAll(io.LimitReader(rc, maxVoiceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read voice message: %w", err)
	}
	if len(data) > maxVoiceBytes {
		return nil, fmt.Errorf("the voice message is over the %d byte limit", maxVoiceBytes)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("the voice message download was empty")
	}
	return data, nil
}

// attachVoiceTranscript downloads and transcribes one voice note and reports
// whether the message should still be forwarded — the same fail-loud contract
// as attachImage: a voice note that silently degraded to a placeholder would
// get a confident answer about content nobody heard, so a failure is answered
// in the chat and the turn ends there. Telegram voice is OGG/Opus; the
// filename's extension tells the transcription endpoint the container.
func (t *TelegramChannel) attachVoiceTranscript(ctx telebot.Context, voice *telebot.Voice) (string, bool) {
	text, err := t.transcribeVoice(voice)
	if err == nil {
		return text, true
	}

	log.Error("failed to transcribe telegram voice message", "error", err)
	t.stopTyping(ctx.Chat())
	if b := ctx.Bot(); b != nil {
		if _, serr := b.Send(ctx.Sender(), "I couldn't transcribe that voice message: "+err.Error()); serr != nil {
			log.Error("failed to report voice error", "error", serr)
		}
	}
	return "", false
}

func (t *TelegramChannel) transcribeVoice(voice *telebot.Voice) (string, error) {
	audio, err := t.readVoice(voice)
	if err != nil {
		return "", err
	}
	// The transcriber owns its own request timeout (config stt.timeout), so
	// Background is deliberate: a handler has no request context to inherit.
	return t.transcriber(context.Background(), audio, "voice.ogg")
}
