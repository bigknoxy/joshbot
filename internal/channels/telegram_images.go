package channels

import (
	"fmt"
	"io"

	"github.com/bigknoxy/joshbot/internal/log"
	"github.com/bigknoxy/joshbot/internal/providers"
	"gopkg.in/telebot.v3"
)

// telegramImageMaxDownload bounds what is read off the wire for one attachment.
// It is deliberately a little larger than the per-image limit so an over-limit
// file is recognised as over-limit rather than truncated into something that
// sniffs as a valid but corrupt image.
const telegramImageMaxDownload = providers.MaxImageBytes + 1

// fetchImage downloads one Telegram file and validates it as an image.
//
// The caller must have run the allowlist check first: this issues a network
// request to the Bot API carrying the file id, so calling it for a sender the
// operator never allowed both leaks that the bot is live and does work on an
// unauthorised user's behalf.
//
// sizeHint is Telegram's declared FileSize. It is used only to refuse an
// obviously over-limit file before downloading it; it is never trusted as the
// real size, which is measured from the bytes actually read.
func (t *TelegramChannel) fetchImage(file telebot.File, label string, sizeHint int64) (providers.Image, error) {
	if sizeHint > providers.MaxImageBytes {
		return providers.Image{}, fmt.Errorf("image is %d bytes, over the %d byte limit", sizeHint, providers.MaxImageBytes)
	}

	open := t.download
	if open == nil {
		if t.bot == nil {
			return providers.Image{}, fmt.Errorf("no bot available to download %s", label)
		}
		open = t.bot.File
	}
	rc, err := open(&file)
	if err != nil {
		return providers.Image{}, fmt.Errorf("download %s: %w", label, err)
	}
	defer func() { _ = rc.Close() }()

	// LimitReader caps memory regardless of what Telegram declared; reading one
	// byte past the limit is what distinguishes "at the limit" from "over it".
	data, err := io.ReadAll(io.LimitReader(rc, telegramImageMaxDownload))
	if err != nil {
		return providers.Image{}, fmt.Errorf("read %s: %w", label, err)
	}
	return providers.NewImage(label, data)
}

// attachImage downloads an image for an inbound message and reports whether the
// message should still be forwarded.
//
// A failure is answered in the chat rather than swallowed: an image that
// silently became a text-only "[Photo]" gets a confident answer about nothing,
// which is worse than an error. The turn ends there — there is no useful
// conversation to have about an attachment that never arrived.
func (t *TelegramChannel) attachImage(ctx telebot.Context, file telebot.File, label string, sizeHint int64) (providers.Image, bool) {
	img, err := t.fetchImage(file, label, sizeHint)
	if err == nil {
		return img, true
	}

	log.Error("failed to attach telegram image", "label", label, "error", err)
	t.stopTyping(ctx.Chat())
	if b := ctx.Bot(); b != nil {
		if _, serr := b.Send(ctx.Sender(), "I couldn't read that image: "+err.Error()); serr != nil {
			log.Error("failed to report image error", "error", serr)
		}
	}
	return providers.Image{}, false
}
