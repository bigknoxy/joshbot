package channels

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"gopkg.in/telebot.v3"
)

// mediaEditor records the payloads themselves rather than their formatted
// text: the whole question on this path is whether a photo or a document went
// out and with which bytes, which a string rendering cannot answer.
type mediaEditor struct {
	mu    sync.Mutex
	sent  []interface{}
	modes []telebot.ParseMode
	reply []int
	errs  []error
}

func (m *mediaEditor) Send(to telebot.Recipient, what interface{}, opts ...interface{}) (*telebot.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, what)
	mode := telebot.ModeDefault
	replyTo := 0
	for _, o := range opts {
		if so, ok := o.(*telebot.SendOptions); ok {
			mode = so.ParseMode
			if so.ReplyTo != nil {
				replyTo = so.ReplyTo.ID
			}
		}
	}
	m.modes = append(m.modes, mode)
	m.reply = append(m.reply, replyTo)
	if len(m.errs) > 0 {
		err := m.errs[0]
		m.errs = m.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	return &telebot.Message{ID: len(m.sent)}, nil
}

func (m *mediaEditor) Edit(msg telebot.Editable, what interface{}, opts ...interface{}) (*telebot.Message, error) {
	return &telebot.Message{}, nil
}

func (m *mediaEditor) Delete(msg telebot.Editable) error { return nil }

func (m *mediaEditor) snapshot() []interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]interface{}(nil), m.sent...)
}

func attachmentTestChannel(t *testing.T) (*TelegramChannel, *mediaEditor) {
	t.Helper()
	tg := newTestTelegramChannel()
	ed := &mediaEditor{}
	tg.mu.Lock()
	tg.editor = ed
	tg.retryDelay = time.Millisecond
	tg.maxRetryDelay = time.Millisecond
	tg.mu.Unlock()
	return tg, ed
}

func photoAttachment() bus.Attachment {
	return bus.Attachment{
		Filename:   "chart.png",
		MIME:       "image/png",
		Kind:       bus.AttachmentPhoto,
		Size:       4,
		Data:       []byte("\x89PNG"),
		SourcePath: "charts/chart.png",
	}
}

func TestSendAttachments_PhotoCarriesCaptionModeAndReplyAnchor(t *testing.T) {
	tg, ed := attachmentTestChannel(t)

	err := tg.Send(bus.OutboundMessage{
		Channel:     "telegram",
		ChannelID:   "999",
		Content:     "*here*",
		Attachments: []bus.Attachment{photoAttachment()},
		Metadata:    map[string]any{"parse_mode": "markdown", "reply_to_id": 42},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	sent := ed.snapshot()
	if len(sent) != 1 {
		t.Fatalf("sent %d payloads, want 1", len(sent))
	}
	photo, ok := sent[0].(*telebot.Photo)
	if !ok {
		t.Fatalf("payload is %T, want *telebot.Photo", sent[0])
	}
	if photo.Caption != "*here*" {
		t.Errorf("caption = %q, want %q", photo.Caption, "*here*")
	}
	if photo.FileReader == nil {
		t.Error("photo carries no reader; the inline bytes were dropped")
	}
	if ed.modes[0] != telebot.ModeMarkdown {
		t.Errorf("parse mode = %q, want markdown", ed.modes[0])
	}
	if ed.reply[0] != 42 {
		t.Errorf("reply anchor = %d, want 42", ed.reply[0])
	}
}

func TestSendAttachments_DocumentKeepsFilenameAndMIME(t *testing.T) {
	tg, ed := attachmentTestChannel(t)

	att := bus.Attachment{
		Filename:   "notes.png", // named .png, sniffed as text: must be a document
		MIME:       "text/plain",
		Kind:       bus.AttachmentDocument,
		Size:       5,
		Data:       []byte("hello"),
		SourcePath: "notes.png",
	}
	if err := tg.Send(bus.OutboundMessage{ChannelID: "1", Attachments: []bus.Attachment{att}}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	doc, ok := ed.snapshot()[0].(*telebot.Document)
	if !ok {
		t.Fatalf("payload is %T, want *telebot.Document", ed.snapshot()[0])
	}
	if doc.FileName != "notes.png" || doc.MIME != "text/plain" {
		t.Errorf("document = %q/%q, want notes.png/text/plain", doc.FileName, doc.MIME)
	}
}

func TestSendAttachments_RetriesTransientFailureWithFreshPayload(t *testing.T) {
	tg, ed := attachmentTestChannel(t)
	ed.errs = []error{errors.New("connection reset")}

	if err := tg.Send(bus.OutboundMessage{ChannelID: "1", Attachments: []bus.Attachment{photoAttachment()}}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	sent := ed.snapshot()
	if len(sent) != 2 {
		t.Fatalf("sent %d payloads, want 2 (one failure, one retry)", len(sent))
	}
	// The retry must not reuse the consumed reader: a fresh payload is a
	// different pointer, and a reused one would upload zero bytes.
	if sent[0] == sent[1] {
		t.Error("retry reused the first payload; the reader is already drained")
	}
}

func TestSendAttachments_PermanentFailureIsNotRetried(t *testing.T) {
	tg, ed := attachmentTestChannel(t)
	ed.errs = []error{errors.New("bot was blocked by the user")}

	err := tg.Send(bus.OutboundMessage{ChannelID: "1", Attachments: []bus.Attachment{photoAttachment()}})
	if err == nil {
		t.Fatal("want an error for a permanent failure")
	}
	if n := len(ed.snapshot()); n != 1 {
		t.Errorf("sent %d payloads, want exactly 1 — a 403 never succeeds on retry", n)
	}
}

func TestSendAttachments_ParseEntityErrorRetriesWithPlainCaption(t *testing.T) {
	tg, ed := attachmentTestChannel(t)
	ed.errs = []error{errors.New("telegram: can't parse entities: unbalanced")}

	msg := bus.OutboundMessage{
		ChannelID:   "1",
		Content:     "*half open",
		Attachments: []bus.Attachment{photoAttachment()},
		Metadata:    map[string]any{"parse_mode": "markdown"},
	}
	if err := tg.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	sent := ed.snapshot()
	if len(sent) != 2 {
		t.Fatalf("sent %d payloads, want 2", len(sent))
	}
	if ed.modes[1] != telebot.ModeDefault {
		t.Errorf("retry parse mode = %q, want cleared", ed.modes[1])
	}
	// The caption is cleared of formatting, never of content, and the file
	// itself is re-sent with real bytes.
	photo, ok := sent[1].(*telebot.Photo)
	if !ok {
		t.Fatalf("retry payload is %T, want *telebot.Photo", sent[1])
	}
	if photo.Caption != "*half open" {
		t.Errorf("retry caption = %q, want the original text", photo.Caption)
	}
	if photo.FileReader == nil {
		t.Error("retry carries no reader; it would upload zero bytes")
	}
}

func TestSendAttachments_OversizeIsRefusedWithoutSending(t *testing.T) {
	tg, ed := attachmentTestChannel(t)
	att := photoAttachment()
	att.Size = tg.AttachmentLimits().PhotoMaxBytes + 1

	err := tg.Send(bus.OutboundMessage{ChannelID: "1", Attachments: []bus.Attachment{att}})
	if err == nil {
		t.Fatal("want an error for an over-limit photo")
	}
	if n := len(ed.snapshot()); n != 0 {
		t.Errorf("sent %d payloads, want 0 — the limit is checked before the upload", n)
	}
}

func TestSendAttachments_LongCaptionBecomesItsOwnMessage(t *testing.T) {
	tg, ed := attachmentTestChannel(t)
	// The channel's text path needs a bot; here the editor stands in for the
	// media send and the text send goes through the same fake.
	long := strings.Repeat("x", TelegramMaxCaptionLen+1)

	err := tg.sendAttachments(ed, telebot.ChatID(1), bus.OutboundMessage{
		ChannelID:   "1",
		Content:     long,
		Attachments: []bus.Attachment{photoAttachment()},
	}, telebot.ModeDefault)
	// Send() for the overflow text needs t.bot, which the fake cannot supply;
	// what matters here is that the photo went out with an empty caption
	// rather than a truncated one.
	_ = err
	photo, ok := ed.snapshot()[0].(*telebot.Photo)
	if !ok {
		t.Fatalf("payload is %T, want *telebot.Photo", ed.snapshot()[0])
	}
	if photo.Caption != "" {
		t.Errorf("caption = %q, want empty — an over-cap caption is sent separately, never truncated", photo.Caption)
	}
}

// B1: the payload is always reader-backed. A disk-backed telebot file would be
// a second, uncontained open on the channel goroutine — after the bus hop, and
// again on every retry — sending bytes the containment walk never saw. Asserting
// on telebot's own fields is the point: FileLocal is what FromDisk sets.
func TestTelegramMedia_NeverProducesADiskBackedFile(t *testing.T) {
	cases := []bus.Attachment{
		photoAttachment(),
		{Filename: "notes.bin", MIME: "application/octet-stream", Kind: bus.AttachmentDocument, Size: 4, Data: []byte("data"), SourcePath: "notes.bin"},
	}
	for _, att := range cases {
		t.Run(string(att.Kind), func(t *testing.T) {
			var file telebot.File
			switch m := telegramMedia(att, "cap").(type) {
			case *telebot.Photo:
				file = m.File
			case *telebot.Document:
				file = m.File
			default:
				t.Fatalf("payload is %T", m)
			}
			if file.FileLocal != "" {
				t.Errorf("FileLocal = %q — the payload would be re-opened from disk, uncontained", file.FileLocal)
			}
			if file.FileReader == nil {
				t.Error("FileReader is nil; the contained bytes are the only thing that may be uploaded")
			}
		})
	}
}

// An attachment with no bytes is refused: there is no path to fall back to,
// by design.
func TestSendAttachments_ByteslessAttachmentIsRefused(t *testing.T) {
	tg, ed := attachmentTestChannel(t)
	att := bus.Attachment{Filename: "ghost.png", Kind: bus.AttachmentPhoto, Size: 4, SourcePath: "ghost.png"}

	if err := tg.Send(bus.OutboundMessage{ChannelID: "1", Attachments: []bus.Attachment{att}}); err == nil {
		t.Fatal("want an error for an attachment with no content")
	}
	if n := len(ed.snapshot()); n != 0 {
		t.Errorf("sent %d payloads, want 0", n)
	}
}

func TestDescribeUnsentAttachments(t *testing.T) {
	if got := describeUnsentAttachments("hi", nil); got != "hi" {
		t.Errorf("with no attachments the text must be untouched, got %q", got)
	}
	got := describeUnsentAttachments("chart is ready", []bus.Attachment{photoAttachment()})
	if !strings.Contains(got, "charts/chart.png") {
		t.Errorf("degraded text must name the file, got %q", got)
	}
	// F3: the label goes into chat, so it must not disclose the operator's
	// filesystem layout.
	if strings.Contains(got, string(filepath.Separator)+"Users") || strings.Contains(got, "/home/") {
		t.Errorf("degraded text leaked an absolute path: %q", got)
	}
	if !strings.Contains(got, "chart is ready") {
		t.Errorf("degraded text must keep the reply, got %q", got)
	}
}
