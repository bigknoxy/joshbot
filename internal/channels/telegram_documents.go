package channels

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/bigknoxy/joshbot/internal/log"
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
		// Do not cut a rune in half at the boundary.
		for len(data) > 0 && !utf8.Valid(data) {
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
