package channels

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/bigknoxy/joshbot/internal/log"
	"github.com/bigknoxy/joshbot/internal/providers"
	"gopkg.in/telebot.v3"
)

// telegramTextDocMaxBytes bounds how much of a text document is inlined into
// the agent turn. The content lands verbatim in the prompt (and the session),
// so the cap is a token budget, not a memory one; anything past it is
// truncated with a visible marker rather than refused, because the head of a
// log or CSV usually answers the question.
const telegramTextDocMaxBytes = 64 * 1024

// textDocMIMEs are the declared MIME types worth spending a download on as
// text. The declaration is not trusted as text-ness — readTextDocument checks
// the bytes — it only decides whether to download at all, the same rule the
// image path applies.
var textDocMIMEs = map[string]bool{
	"application/json":       true,
	"application/xml":        true,
	"application/x-yaml":     true,
	"application/yaml":       true,
	"application/toml":       true,
	"application/javascript": true,
	"application/x-sh":       true,
	"application/sql":        true,
}

// textDocExtensions recognises text-like files Telegram declares as
// application/octet-stream, which it does for most code and config files.
var textDocExtensions = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".csv": true, ".tsv": true,
	".json": true, ".yaml": true, ".yml": true, ".toml": true, ".xml": true,
	".log": true, ".ini": true, ".conf": true, ".env": true, ".sql": true,
	".go": true, ".py": true, ".js": true, ".ts": true, ".sh": true,
	".rb": true, ".rs": true, ".java": true, ".c": true, ".h": true,
	".cpp": true, ".html": true, ".css": true, ".diff": true, ".patch": true,
}

// isTextLikeDocument reports whether a document claims to be text, by MIME
// type or (for the octet-stream default) by filename extension.
func isTextLikeDocument(mime, name string) bool {
	if strings.HasPrefix(mime, "text/") || textDocMIMEs[mime] {
		return true
	}
	return textDocExtensions[strings.ToLower(filepath.Ext(name))]
}

// readTextDocument downloads one Telegram document and validates it as text:
// valid UTF-8 with no NUL bytes. The caller must have run the allowlist check
// first, for the same reason as fetchImage. sizeHint only refuses an
// obviously enormous file before the transfer; the read is capped regardless.
func (t *TelegramChannel) readTextDocument(doc *telebot.Document, sizeHint int64) (string, error) {
	// A hard refusal ceiling well above the inline cap: a 5MB log is worth
	// downloading 64KB of; a 2GB archive claiming .txt is not.
	const maxWorthDownloading = 8 << 20
	if sizeHint > maxWorthDownloading {
		return "", fmt.Errorf("file is %d bytes — too large to read; send an excerpt", sizeHint)
	}

	open := t.download
	if open == nil {
		if t.bot == nil {
			return "", fmt.Errorf("no bot available to download %s", doc.FileName)
		}
		open = t.bot.File
	}
	rc, err := open(&doc.File)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", doc.FileName, err)
	}
	defer func() { _ = rc.Close() }()

	// One byte past the cap distinguishes "at the limit" from "over it".
	data, err := io.ReadAll(io.LimitReader(rc, telegramTextDocMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", doc.FileName, err)
	}

	truncated := len(data) > telegramTextDocMaxBytes
	if truncated {
		data = data[:telegramTextDocMaxBytes]
		// Do not cut a rune in half at the boundary — but back off at most
		// UTFMax-1 bytes. An unbounded trim on a binary file (invalid UTF-8
		// throughout) would eat the whole buffer and pass the text check
		// below vacuously, inlining nothing but the truncation marker.
		for i := 0; i < utf8.UTFMax-1 && len(data) > 0 && !utf8.Valid(data); i++ {
			data = data[:len(data)-1]
		}
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return "", fmt.Errorf("%s is not a text file", doc.FileName)
	}

	text := string(data)
	if truncated {
		text += fmt.Sprintf("\n[... truncated at %d bytes]", telegramTextDocMaxBytes)
	}
	return text, nil
}

// attachTextDocument reads a text document for an inbound message and reports
// whether the message should still be forwarded — the same contract as
// attachImage, for the same reason: a document that silently degrades to its
// filename gets a confident answer about a file nobody opened.
func (t *TelegramChannel) attachTextDocument(ctx telebot.Context, doc *telebot.Document) (string, bool) {
	text, err := t.readTextDocument(doc, int64(doc.FileSize))
	if err == nil {
		return text, true
	}

	log.Error("failed to read telegram document", "file", doc.FileName, "error", err)
	t.stopTyping(ctx.Chat())
	if b := ctx.Bot(); b != nil {
		if _, serr := b.Send(ctx.Sender(), "I couldn't read that file: "+err.Error()); serr != nil {
			log.Error("failed to report document error", "error", serr)
		}
	}
	return "", false
}

// telegramBotDownloadMaxBytes is the Telegram Bot API's own ceiling on what a
// bot may download (getFile / file download is documented as "at most 20MB in
// size"). It is recorded here so the effective cap below is visibly the smaller
// of joshbot's limit and Telegram's, rather than a number that happens to work.
const telegramBotDownloadMaxBytes = 20 * 1000 * 1000

// telegramDocumentMaxLoad is the effective per-document cap: the smaller of
// joshbot's own limit and Telegram's. joshbot's is the smaller of the two, and
// the compile-time assertion below keeps that true — converting a negative
// difference to uint is a compile error, so raising MaxDocumentBytes past
// Telegram's ceiling fails the build rather than producing downloads Telegram
// will refuse.
const telegramDocumentMaxLoad = providers.MaxDocumentBytes

const _ = uint(telegramBotDownloadMaxBytes - telegramDocumentMaxLoad)

// telegramDocumentMaxDownload reads one byte past the cap, which is what
// distinguishes "at the limit" from "over it", exactly as on the image path.
const telegramDocumentMaxDownload = telegramDocumentMaxLoad + 1

// readPDFDocument downloads one Telegram document and validates it as a PDF.
//
// The caller must have run the allowlist check first, for the same reason as
// fetchImage: this issues a Bot API request carrying the file id on the
// sender's behalf and confirms the bot is live.
//
// sizeHint is Telegram's declared FileSize. It refuses an obviously over-limit
// file before any transfer; it is never trusted as the real size, which is
// measured from the bytes actually read. And the declared MIME type decided
// only that this path was worth taking — providers.NewDocument sniffs the
// bytes, so a PNG declared application/pdf is refused here rather than sent.
func (t *TelegramChannel) readPDFDocument(doc *telebot.Document, sizeHint int64) (providers.Document, error) {
	if sizeHint > telegramDocumentMaxLoad {
		return providers.Document{}, fmt.Errorf("document is %d bytes, over the %d byte limit",
			sizeHint, telegramDocumentMaxLoad)
	}

	open := t.download
	if open == nil {
		if t.bot == nil {
			return providers.Document{}, fmt.Errorf("no bot available to download %s", doc.FileName)
		}
		open = t.bot.File
	}
	rc, err := open(&doc.File)
	if err != nil {
		return providers.Document{}, fmt.Errorf("download %s: %w", doc.FileName, err)
	}
	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(io.LimitReader(rc, int64(telegramDocumentMaxDownload)))
	if err != nil {
		return providers.Document{}, fmt.Errorf("read %s: %w", doc.FileName, err)
	}
	return providers.NewDocument(doc.FileName, data)
}

// attachPDFDocument reads a PDF for an inbound message and reports whether the
// message should still be forwarded — the same (value, bool) contract as
// attachTextDocument and attachImage, for the same reason: a document that
// silently degrades to "[Document: report.pdf]" gets a confident answer about a
// file nobody opened.
func (t *TelegramChannel) attachPDFDocument(ctx telebot.Context, doc *telebot.Document) (providers.Document, bool) {
	pdf, err := t.readPDFDocument(doc, int64(doc.FileSize))
	if err == nil {
		return pdf, true
	}

	log.Error("failed to read telegram pdf", "file", doc.FileName, "error", err)
	t.stopTyping(ctx.Chat())
	if b := ctx.Bot(); b != nil {
		if _, serr := b.Send(ctx.Sender(), "I couldn't read that file: "+err.Error()); serr != nil {
			log.Error("failed to report document error", "error", serr)
		}
	}
	return providers.Document{}, false
}

// isPDFDocument reports whether a document claims to be a PDF, by declared MIME
// type or (for the application/octet-stream default Telegram often sends) by
// filename extension. The claim decides only whether the download is worth
// spending — providers.NewDocument sniffs the bytes for the real type.
func isPDFDocument(mime, name string) bool {
	if providers.IsSupportedDocumentMIME(mime) {
		return true
	}
	return strings.EqualFold(filepath.Ext(name), ".pdf")
}
