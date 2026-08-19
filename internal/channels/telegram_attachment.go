package channels

import (
	"bytes"
	"fmt"
	"math"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/log"
	"gopkg.in/telebot.v3"
)

// AttachmentLimits returns the outbound file sizes this channel enforces.
//
// It is a method rather than constants read at the use site so a self-hosted
// Bot API server — which raises both limits, and is what telegram.api_url
// points at (issue #280) — has one place to change. The channel re-checks the
// size even though the tool already did: the bus is a public boundary and an
// attachment could be published by anything, so the transport enforces its own
// limit rather than trusting the producer's.
func (t *TelegramChannel) AttachmentLimits() bus.AttachmentLimits {
	return bus.DefaultAttachmentLimits()
}

// sendAttachments delivers each attachment on an outbound message. The message
// Content becomes the first attachment's caption; a caption over the Bot API's
// cap is sent as its own text message afterwards rather than truncated.
func (t *TelegramChannel) sendAttachments(editor telegramEditor, recipient telebot.Recipient, msg bus.OutboundMessage, parseMode telebot.ParseMode) error {
	caption := msg.Content
	overflow := ""
	if len(caption) > TelegramMaxCaptionLen {
		overflow, caption = caption, ""
	}

	for i, att := range msg.Attachments {
		opts := &telebot.SendOptions{}
		text := ""
		if i == 0 {
			text = caption
			opts.ParseMode = parseMode
			t.applyReplyTarget(opts, msg.Metadata)
		}
		if err := t.sendOneAttachment(editor, recipient, att, text, opts); err != nil {
			return fmt.Errorf("failed to send attachment %s: %w", att.Filename, err)
		}
	}

	if overflow != "" {
		// Reuse the text path wholesale: it already splits at 4096, retries,
		// and falls back to plain text on a formatting rejection.
		textOnly := msg
		textOnly.Attachments = nil
		textOnly.Content = overflow
		return t.Send(textOnly)
	}
	return nil
}

// applyReplyTarget copies the thread anchor out of the metadata, matching what
// the text path does for its first part.
func (t *TelegramChannel) applyReplyTarget(opts *telebot.SendOptions, meta map[string]any) {
	if replyToMsg, ok := meta["reply_to_message"].(**telebot.Message); ok && replyToMsg != nil {
		opts.ReplyTo = *replyToMsg
	}
	switch id := meta["reply_to_id"].(type) {
	case int:
		if id != 0 {
			opts.ReplyTo = &telebot.Message{ID: id}
		}
	case float64:
		if id != 0 {
			opts.ReplyTo = &telebot.Message{ID: int(id)}
		}
	}
}

// sendOneAttachment sends one file, retrying transient failures the same way
// the text path does and clearing the parse mode — the caption's formatting,
// never the file — when Telegram rejects the entities.
func (t *TelegramChannel) sendOneAttachment(editor telegramEditor, recipient telebot.Recipient, att bus.Attachment, caption string, opts *telebot.SendOptions) error {
	limits := t.AttachmentLimits()
	max := limits.DocumentMaxBytes
	if att.Kind == bus.AttachmentPhoto {
		max = limits.PhotoMaxBytes
	}
	if att.Size > max {
		return fmt.Errorf("%s is %s, over the %s telegram limit for a %s",
			att.Filename, humanBytes(att.Size), humanBytes(max), att.Kind)
	}
	if len(att.Data) == 0 && att.Path == "" {
		return fmt.Errorf("attachment %s carries neither bytes nor a path", att.Filename)
	}

	var lastErr error
	delay := t.retryDelay
	for attempt := 0; attempt < t.maxRetries; attempt++ {
		// The payload is rebuilt every attempt: a File backed by an
		// io.Reader is consumed by the send, so a retry that reused it would
		// upload zero bytes and report success.
		_, err := editor.Send(recipient, telegramMedia(att, caption), opts)
		if err == nil {
			return nil
		}

		if isParseEntityError(err) && opts.ParseMode != telebot.ModeDefault {
			log.Warn("telegram rejected caption formatting, retrying attachment with a plain caption",
				"file", att.Filename, "error", err)
			opts.ParseMode = telebot.ModeDefault
			if _, fallbackErr := editor.Send(recipient, telegramMedia(att, caption), opts); fallbackErr == nil {
				return nil
			} else {
				err = fallbackErr
			}
		}

		lastErr = err
		log.Warn("failed to send telegram attachment, retrying",
			"attempt", attempt+1, "max_retries", t.maxRetries,
			"file", att.Filename, "error", err)
		if !isRetryable(err) {
			break
		}
		select {
		case <-time.After(delay):
			delay = time.Duration(math.Min(float64(delay*2), float64(t.maxRetryDelay)))
		case <-t.stopCh:
			return fmt.Errorf("stopped while retrying: %w", lastErr)
		}
	}
	return lastErr
}

// telegramMedia builds the telebot payload for one attachment. Kind was
// decided by sniffing the bytes, never the extension, so a text file named
// .png goes out as a document rather than as a photo Telegram would reject.
func telegramMedia(att bus.Attachment, caption string) interface{} {
	file := telebot.FromDisk(att.Path)
	if len(att.Data) > 0 {
		file = telebot.FromReader(bytes.NewReader(att.Data))
	}
	if att.Kind == bus.AttachmentPhoto {
		return &telebot.Photo{File: file, Caption: caption}
	}
	return &telebot.Document{
		File:     file,
		Caption:  caption,
		FileName: att.Filename,
		MIME:     att.MIME,
	}
}

// humanBytes formats a size the way the limits are written.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
