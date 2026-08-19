package channels

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/providers"
	"gopkg.in/telebot.v3"
)

func testPDF() []byte {
	return append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), 128)...)
}

func pdfChannel(t *testing.T, allow []string, body []byte) (*TelegramChannel, *bus.MessageBus, *int) {
	t.Helper()
	mb := bus.NewMessageBus()
	tg := NewTelegramChannel(mb, &config.TelegramConfig{Enabled: true, Token: "t", AllowFrom: allow})
	downloads := 0
	tg.download = func(*telebot.File) (io.ReadCloser, error) {
		downloads++
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	return tg, mb, &downloads
}

func docCtx(senderID int64, name, mime string, size int64) *fakeCtx {
	return &fakeCtx{msg: &telebot.Message{
		ID:     11,
		Sender: &telebot.User{ID: senderID, Username: "someone"},
		Chat:   &telebot.Chat{ID: 42},
		Document: &telebot.Document{
			File:     telebot.File{FileID: "f1", FileSize: size},
			FileName: name,
			MIME:     mime,
			Caption:  "what does this say?",
		},
	}}
}

// receiveInbound takes the one message the turn published, or fails.
func receiveInbound(t *testing.T, mb *bus.MessageBus) bus.InboundMessage {
	t.Helper()
	select {
	case m := <-mb.InboundChannel():
		return m
	default:
		t.Fatal("nothing reached the bus")
		return bus.InboundMessage{}
	}
}

// assertNoInbound pins that a refused attachment ended the turn rather than
// reaching the agent as a filename.
func assertNoInbound(t *testing.T, mb *bus.MessageBus) {
	t.Helper()
	select {
	case m := <-mb.InboundChannel():
		t.Fatalf("a refused attachment reached the agent as %q (%d documents, %d images)",
			m.Content, len(m.Documents), len(m.Images))
	default:
	}
}

// TestPDFRidesTheTurnAsADocument is acceptance criterion 1's channel half: the
// PDF reaches the bus as bytes on the turn, not as a filename.
func TestPDFRidesTheTurnAsADocument(t *testing.T) {
	body := testPDF()
	tg, mb, downloads := pdfChannel(t, []string{"1234"}, body)

	if err := tg.handleDocument(docCtx(1234, "report.pdf", "application/pdf", int64(len(body)))); err != nil {
		t.Fatalf("handleDocument: %v", err)
	}
	if *downloads != 1 {
		t.Fatalf("downloads = %d, want 1", *downloads)
	}
	msg := receiveInbound(t, mb)
	if len(msg.Documents) != 1 {
		t.Fatalf("Documents = %d, want 1", len(msg.Documents))
	}
	got := msg.Documents[0]
	if got.MIME != providers.MIMEPDF {
		t.Fatalf("MIME = %q, want %q", got.MIME, providers.MIMEPDF)
	}
	if !bytes.Equal(got.Data, body) {
		t.Fatal("the document bytes did not survive the download")
	}
	if got.Label != "report.pdf" {
		t.Fatalf("Label = %q, want report.pdf", got.Label)
	}
	if !strings.Contains(msg.Content, "what does this say?") {
		t.Fatalf("the caption must ride the turn, got %q", msg.Content)
	}
}

// TestPDFFromDisallowedSenderIsNeverDownloaded is the security case, the same
// one the image path pins: the download carries the file id to the Bot API on a
// stranger's behalf and confirms the bot is live.
func TestPDFFromDisallowedSenderIsNeverDownloaded(t *testing.T) {
	tg, _, downloads := pdfChannel(t, []string{"999"}, testPDF())
	if err := tg.handleDocument(docCtx(1234, "report.pdf", "application/pdf", 100)); err != nil {
		t.Fatalf("handleDocument: %v", err)
	}
	if *downloads != 0 {
		t.Fatalf("a disallowed sender caused %d downloads", *downloads)
	}
}

// TestDeclaredOversizePDFIsRefusedWithoutDownloading — the declared size is the
// only thing available before a transfer, so it is what refuses an obviously
// over-limit file. It is never trusted as the real size; the LimitReader below
// covers a lying declaration.
func TestDeclaredOversizePDFIsRefusedWithoutDownloading(t *testing.T) {
	tg, mb, downloads := pdfChannel(t, []string{"1234"}, testPDF())
	oversize := int64(providers.MaxDocumentBytes) + 1

	if err := tg.handleDocument(docCtx(1234, "huge.pdf", "application/pdf", oversize)); err != nil {
		t.Fatalf("handleDocument: %v", err)
	}
	if *downloads != 0 {
		t.Fatalf("an over-limit declaration still caused %d downloads", *downloads)
	}
	assertNoInbound(t, mb)
}

// TestOverLimitPDFBodyIsDistinguishableFromTruncation — reading one byte past
// the cap is what tells "at the limit" from "over it", so a file that lies
// about its size is refused rather than silently truncated into a corrupt PDF.
func TestOverLimitPDFBodyIsDistinguishableFromTruncation(t *testing.T) {
	body := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), providers.MaxDocumentBytes)...)
	tg, mb, downloads := pdfChannel(t, []string{"1234"}, body)

	// The declaration lies: it claims a small file.
	if err := tg.handleDocument(docCtx(1234, "liar.pdf", "application/pdf", 100)); err != nil {
		t.Fatalf("handleDocument: %v", err)
	}
	if *downloads != 1 {
		t.Fatalf("downloads = %d, want 1", *downloads)
	}
	assertNoInbound(t, mb)
}

// TestPDFSniffingBeatsTheDeclaredType covers both mislabelling directions at
// the channel boundary. A PNG declared application/pdf must be refused rather
// than sent, and a PDF declared image/png must not be accepted as an image.
func TestPDFSniffingBeatsTheDeclaredType(t *testing.T) {
	t.Run("png declared as pdf is refused", func(t *testing.T) {
		tg, mb, downloads := pdfChannel(t, []string{"1234"}, testPNG(t))
		if err := tg.handleDocument(docCtx(1234, "report.pdf", "application/pdf", 100)); err != nil {
			t.Fatalf("handleDocument: %v", err)
		}
		if *downloads != 1 {
			t.Fatalf("downloads = %d, want 1 (the claim earns the download; the bytes decide)", *downloads)
		}
		assertNoInbound(t, mb)
	})

	t.Run("pdf declared as png is refused rather than sent as an image", func(t *testing.T) {
		tg, mb, downloads := pdfChannel(t, []string{"1234"}, testPDF())
		if err := tg.handleDocument(docCtx(1234, "shot.png", "image/png", 100)); err != nil {
			t.Fatalf("handleDocument: %v", err)
		}
		if *downloads != 1 {
			t.Fatalf("downloads = %d, want 1", *downloads)
		}
		// The image path took it (the declaration decided that), and
		// providers.NewImage refused the bytes. The turn ends honestly rather
		// than forwarding a mislabelled attachment.
		assertNoInbound(t, mb)
	})
}

// TestOfficeDocumentRefusalListsWhatIsSupported — acceptance criterion 4. A
// docx stays refused and is never downloaded, and the refusal now says what
// does work. The captioned path is what a test can observe: fakeCtx has no bot,
// so the captionless reply goes nowhere, while the caption is forwarded framed.
func TestOfficeDocumentRefusalListsWhatIsSupported(t *testing.T) {
	tg, mb, downloads := pdfChannel(t, []string{"1234"}, testPDF())
	const docxMIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

	if err := tg.handleDocument(docCtx(1234, "budget.docx", docxMIME, 100)); err != nil {
		t.Fatalf("handleDocument: %v", err)
	}
	if *downloads != 0 {
		t.Fatal("an unsupported office format must not be downloaded")
	}
	msg := receiveInbound(t, mb)
	if len(msg.Documents) != 0 || len(msg.Images) != 0 {
		t.Fatalf("a docx was attached: %+v", msg)
	}
	for _, want := range []string{"budget.docx", "PDF", "what does this say?"} {
		if !strings.Contains(msg.Content, want) {
			t.Fatalf("the framed refusal must mention %q, got %q", want, msg.Content)
		}
	}
}

// TestTextDocumentStillInlines guards the case ordering: adding the PDF arm
// must not have shadowed the text-like arm.
func TestTextDocumentStillInlines(t *testing.T) {
	tg, mb, _ := pdfChannel(t, []string{"1234"}, []byte("hello from a log file"))
	if err := tg.handleDocument(docCtx(1234, "app.log", "application/octet-stream", 21)); err != nil {
		t.Fatalf("handleDocument: %v", err)
	}
	msg := receiveInbound(t, mb)
	if len(msg.Documents) != 0 {
		t.Fatal("a text file must be inlined, not carried as a document attachment")
	}
	if !strings.Contains(msg.Content, "hello from a log file") {
		t.Fatalf("the text was not inlined, got %q", msg.Content)
	}
}

func TestIsPDFDocument(t *testing.T) {
	cases := []struct {
		mime, name string
		want       bool
	}{
		{"application/pdf", "x.bin", true},
		{"application/octet-stream", "report.PDF", true},
		{"application/octet-stream", "report.pdf", true},
		{"application/octet-stream", "notes.txt", false},
		{"image/png", "shot.png", false},
		{"", "", false},
	}
	for _, c := range cases {
		if got := isPDFDocument(c.mime, c.name); got != c.want {
			t.Errorf("isPDFDocument(%q, %q) = %v, want %v", c.mime, c.name, got, c.want)
		}
	}
}
