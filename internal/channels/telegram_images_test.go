package channels

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"gopkg.in/telebot.v3"
)

// fakeCtx is a telebot.Context that answers only what the media handlers use.
// Embedding the interface means any method they start calling panics loudly
// rather than silently returning a zero value.
type fakeCtx struct {
	telebot.Context
	msg *telebot.Message
}

func (f *fakeCtx) Message() *telebot.Message { return f.msg }
func (f *fakeCtx) Chat() *telebot.Chat       { return f.msg.Chat }
func (f *fakeCtx) Sender() *telebot.User     { return f.msg.Sender }
func (f *fakeCtx) Bot() *telebot.Bot         { return nil }

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{B: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func imageChannel(t *testing.T, allow []string, body []byte, downloadErr error) (*TelegramChannel, *bus.MessageBus, *int) {
	t.Helper()
	mb := bus.NewMessageBus()
	tg := NewTelegramChannel(mb, &config.TelegramConfig{Enabled: true, Token: "t", AllowFrom: allow})
	downloads := 0
	tg.download = func(*telebot.File) (io.ReadCloser, error) {
		downloads++
		if downloadErr != nil {
			return nil, downloadErr
		}
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	return tg, mb, &downloads
}

func photoCtx(senderID int64, size int) *fakeCtx {
	return &fakeCtx{msg: &telebot.Message{
		ID:     7,
		Sender: &telebot.User{ID: senderID, Username: "someone"},
		Chat:   &telebot.Chat{ID: 42},
		Photo: &telebot.Photo{
			File:    telebot.File{FileID: "f1", FileSize: int64(size)},
			Caption: "what is this?",
		},
	}}
}

// TestPhotoFromDisallowedSenderIsNeverDownloaded is the security case. The
// download carries the file id to the Bot API on a stranger's behalf and
// confirms the bot is live, so the allowlist has to be checked before it — not
// before the bus send, which is where a "check the message" ordering would put
// it.
func TestPhotoFromDisallowedSenderIsNeverDownloaded(t *testing.T) {
	tg, mb, downloads := imageChannel(t, []string{"999"}, testPNG(t), nil)

	if err := tg.handlePhoto(photoCtx(1234, 100)); err != nil {
		t.Fatalf("handlePhoto: %v", err)
	}
	if *downloads != 0 {
		t.Fatalf("a disallowed sender's photo was downloaded (%d times)", *downloads)
	}
	select {
	case m := <-mb.InboundChannel():
		t.Fatalf("a disallowed sender's message reached the bus: %+v", m)
	default:
	}
}

// TestAllowedPhotoIsAttachedAsBytes — the descriptor path is only useful if the
// bytes actually ride along with the turn that carried them.
func TestAllowedPhotoIsAttachedAsBytes(t *testing.T) {
	png := testPNG(t)
	tg, mb, downloads := imageChannel(t, []string{"1234"}, png, nil)

	if err := tg.handlePhoto(photoCtx(1234, len(png))); err != nil {
		t.Fatalf("handlePhoto: %v", err)
	}
	if *downloads != 1 {
		t.Fatalf("downloads = %d, want 1", *downloads)
	}
	select {
	case m := <-mb.InboundChannel():
		if len(m.Images) != 1 {
			t.Fatalf("want one attached image, got %d", len(m.Images))
		}
		if m.Images[0].MIME != "image/png" {
			t.Fatalf("MIME = %q, want image/png", m.Images[0].MIME)
		}
		if !bytes.Equal(m.Images[0].Data, png) {
			t.Fatal("the attached bytes are not the downloaded bytes")
		}
		if !strings.Contains(m.Content, "what is this?") {
			t.Fatalf("the caption must survive as the turn's text, got %q", m.Content)
		}
	default:
		t.Fatal("nothing reached the bus")
	}
}

// TestOversizePhotoIsRefusedWithoutDownloading — Telegram's declared size is
// enough to refuse before spending the transfer. Downloading first and
// rejecting after is the same answer at 20MB of cost.
func TestOversizePhotoIsRefusedWithoutDownloading(t *testing.T) {
	tg, mb, downloads := imageChannel(t, []string{"1234"}, testPNG(t), nil)

	if err := tg.handlePhoto(photoCtx(1234, 50<<20)); err != nil {
		t.Fatalf("handlePhoto: %v", err)
	}
	if *downloads != 0 {
		t.Fatal("an over-limit photo was downloaded anyway")
	}
	select {
	case m := <-mb.InboundChannel():
		t.Fatalf("an over-limit photo was forwarded as if it had arrived: %+v", m)
	default:
	}
}

// TestFailedDownloadIsNotForwardedAsTextOnly — the failure mode this guards is
// the quiet one: a "[Photo]" with no image gets a confident answer about
// nothing, and the user cannot tell it never arrived.
func TestFailedDownloadIsNotForwardedAsTextOnly(t *testing.T) {
	tg, mb, _ := imageChannel(t, []string{"1234"}, nil, errors.New("bot api 502"))

	if err := tg.handlePhoto(photoCtx(1234, 100)); err != nil {
		t.Fatalf("handlePhoto: %v", err)
	}
	select {
	case m := <-mb.InboundChannel():
		t.Fatalf("a failed download was forwarded to the agent anyway: %+v", m)
	default:
	}
}

// TestNonImageDocumentIsNeverDownloaded — a binary document the agent cannot
// open must not be downloaded at all. PDFs left this class in #278, so the
// example here is an Office format, which is still unsupported. Captionless it is refused honestly (no
// agent turn); with a caption the caption is forwarded, framed so the model
// knows what it cannot open.
func TestNonImageDocumentIsNeverDownloaded(t *testing.T) {
	office := func(caption string) *telebot.Message {
		return &telebot.Message{
			ID:     8,
			Sender: &telebot.User{ID: 1234},
			Chat:   &telebot.Chat{ID: 42},
			Document: &telebot.Document{
				File:     telebot.File{FileID: "d1", FileSize: 1000},
				FileName: "budget.xlsx",
				MIME:     "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
				Caption:  caption,
			},
		}
	}

	t.Run("captionless is refused, no agent turn", func(t *testing.T) {
		tg, mb, downloads := imageChannel(t, []string{"1234"}, testPNG(t), nil)
		if err := tg.handleDocument(&fakeCtx{msg: office("")}); err != nil {
			t.Fatalf("handleDocument: %v", err)
		}
		if *downloads != 0 {
			t.Fatal("a binary document was downloaded")
		}
		select {
		case m := <-mb.InboundChannel():
			t.Fatalf("a captionless binary document reached the agent as %q", m.Content)
		default:
		}
	})

	t.Run("caption is forwarded framed, still no download", func(t *testing.T) {
		tg, mb, downloads := imageChannel(t, []string{"1234"}, testPNG(t), nil)
		if err := tg.handleDocument(&fakeCtx{msg: office("summarize this")}); err != nil {
			t.Fatalf("handleDocument: %v", err)
		}
		if *downloads != 0 {
			t.Fatal("a binary document was downloaded")
		}
		select {
		case m := <-mb.InboundChannel():
			if len(m.Images) != 0 {
				t.Fatalf("an office document was attached as an image: %+v", m.Images)
			}
			if !strings.Contains(m.Content, "cannot open") || !strings.Contains(m.Content, "summarize this") {
				t.Fatalf("Content = %q, want the framed caption", m.Content)
			}
		default:
			t.Fatal("the captioned document did not reach the bus")
		}
	})
}

// TestImageDocumentIsAttachedByContentNotExtension — a document declares its
// own MIME and filename, both sender-controlled. The declaration may decide
// whether to spend a download; it may not decide what the bytes are.
func TestImageDocumentIsAttachedByContentNotExtension(t *testing.T) {
	tg, mb, _ := imageChannel(t, []string{"1234"}, []byte("plain prose, not an image at all\n"), nil)

	ctx := &fakeCtx{msg: &telebot.Message{
		ID:     9,
		Sender: &telebot.User{ID: 1234},
		Chat:   &telebot.Chat{ID: 42},
		Document: &telebot.Document{
			File:     telebot.File{FileID: "d2", FileSize: 32},
			FileName: "screenshot.png",
			MIME:     "image/png",
		},
	}}
	if err := tg.handleDocument(ctx); err != nil {
		t.Fatalf("handleDocument: %v", err)
	}
	select {
	case m := <-mb.InboundChannel():
		t.Fatalf("a text file declaring image/png was attached as an image: %+v", m)
	default:
	}
}

// docMsg builds a document message with the given name, MIME and caption.
func docMsg(name, mime, caption string, size int64) *fakeCtx {
	return &fakeCtx{msg: &telebot.Message{
		ID:     9,
		Sender: &telebot.User{ID: 1234},
		Chat:   &telebot.Chat{ID: 42},
		Document: &telebot.Document{
			File:     telebot.File{FileID: "d2", FileSize: size},
			FileName: name,
			MIME:     mime,
			Caption:  caption,
		},
	}}
}

// A text document's content is inlined into the turn — the whole point of
// sending a file is that the agent reads it, not its filename.
func TestTextDocumentContentIsInlined(t *testing.T) {
	tg, mb, downloads := imageChannel(t, []string{"1234"}, []byte("alpha,beta\n1,2\n"), nil)
	if err := tg.handleDocument(docMsg("data.csv", "text/csv", "sum it", 15)); err != nil {
		t.Fatalf("handleDocument: %v", err)
	}
	if *downloads != 1 {
		t.Fatalf("downloads = %d, want 1", *downloads)
	}
	select {
	case m := <-mb.InboundChannel():
		if !strings.Contains(m.Content, "alpha,beta\n1,2") {
			t.Fatalf("Content = %q, want the file body inlined", m.Content)
		}
		if !strings.Contains(m.Content, "data.csv") || !strings.Contains(m.Content, "sum it") {
			t.Fatalf("Content = %q, want filename and caption kept", m.Content)
		}
	default:
		t.Fatal("the text document never reached the bus")
	}
}

// An octet-stream declaration with a code extension is still text-like:
// Telegram declares most code and config files exactly that way.
func TestOctetStreamCodeFileIsInlinedByExtension(t *testing.T) {
	tg, mb, _ := imageChannel(t, []string{"1234"}, []byte("package main\n"), nil)
	if err := tg.handleDocument(docMsg("main.go", "application/octet-stream", "", 13)); err != nil {
		t.Fatalf("handleDocument: %v", err)
	}
	select {
	case m := <-mb.InboundChannel():
		if !strings.Contains(m.Content, "package main") {
			t.Fatalf("Content = %q, want the file body inlined", m.Content)
		}
	default:
		t.Fatal("the code file never reached the bus")
	}
}

// The declaration decides only whether to download; the bytes decide what
// they are. Binary content behind a .txt name is refused, not inlined.
func TestBinaryBytesBehindATextNameAreRefused(t *testing.T) {
	tg, mb, _ := imageChannel(t, []string{"1234"}, []byte{0x00, 0x01, 0xFF, 0xFE}, nil)
	if err := tg.handleDocument(docMsg("notes.txt", "text/plain", "", 4)); err != nil {
		t.Fatalf("handleDocument: %v", err)
	}
	select {
	case m := <-mb.InboundChannel():
		t.Fatalf("binary bytes reached the agent as %q", m.Content)
	default:
	}
}

// Oversized text is truncated with a visible marker, never silently cut and
// never refused — the head of a log usually answers the question.
func TestOversizedTextDocumentIsTruncatedWithAMarker(t *testing.T) {
	big := bytes.Repeat([]byte("x"), telegramTextDocMaxBytes+100)
	tg, mb, _ := imageChannel(t, []string{"1234"}, big, nil)
	if err := tg.handleDocument(docMsg("huge.log", "text/plain", "", int64(len(big)))); err != nil {
		t.Fatalf("handleDocument: %v", err)
	}
	select {
	case m := <-mb.InboundChannel():
		if !strings.Contains(m.Content, "truncated at") {
			t.Fatal("no truncation marker on an over-limit document")
		}
		if len(m.Content) > telegramTextDocMaxBytes+200 {
			t.Fatalf("content is %d bytes, cap not applied", len(m.Content))
		}
	default:
		t.Fatal("the truncated document never reached the bus")
	}
}

// An oversized *binary* file must be refused, not trimmed to nothing: an
// unbounded rune-boundary trim on invalid-UTF-8 content used to eat the whole
// buffer and inline an empty "document" carrying only the truncation marker.
func TestOversizedBinaryFileIsRefusedNotEmptied(t *testing.T) {
	big := bytes.Repeat([]byte{0xFF, 0xFE}, (telegramTextDocMaxBytes/2)+100)
	tg, mb, _ := imageChannel(t, []string{"1234"}, big, nil)
	if err := tg.handleDocument(docMsg("blob.txt", "text/plain", "", int64(len(big)))); err != nil {
		t.Fatalf("handleDocument: %v", err)
	}
	select {
	case m := <-mb.InboundChannel():
		t.Fatalf("oversized binary reached the agent as %q", m.Content)
	default:
	}
}
