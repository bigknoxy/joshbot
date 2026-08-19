package providers

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func testPDFBytes() []byte {
	return append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), 64)...)
}

func testPNGBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{B: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// TestNewDocumentSniffsContentNotTheLabel is the acceptance criterion in both
// directions: a PDF named .png is accepted as a PDF, and a PNG named .pdf is
// refused. Only the bytes decide.
func TestNewDocumentSniffsContentNotTheLabel(t *testing.T) {
	t.Run("pdf named .png is accepted as a pdf", func(t *testing.T) {
		doc, err := NewDocument("screenshot.png", testPDFBytes())
		if err != nil {
			t.Fatalf("NewDocument: %v", err)
		}
		if doc.MIME != MIMEPDF {
			t.Fatalf("MIME = %q, want %q", doc.MIME, MIMEPDF)
		}
	})

	t.Run("png named .pdf is refused", func(t *testing.T) {
		_, err := NewDocument("report.pdf", testPNGBytes(t))
		if err == nil {
			t.Fatal("a PNG named .pdf was accepted as a document")
		}
		if !strings.Contains(err.Error(), "not a supported document type") {
			t.Fatalf("error should say why, got %q", err)
		}
	})
}

func TestNewDocumentRejectsEmptyAndOversize(t *testing.T) {
	if _, err := NewDocument("a.pdf", nil); err == nil {
		t.Fatal("empty document accepted")
	}
	big := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), MaxDocumentBytes)...)
	if _, err := NewDocument("a.pdf", big); err == nil {
		t.Fatal("oversize document accepted")
	}
}

func TestValidateDocumentsEnforcesTotal(t *testing.T) {
	one := Document{MIME: MIMEPDF, Data: bytes.Repeat([]byte("x"), MaxDocumentBytes)}
	if err := ValidateDocuments([]Document{one}); err != nil {
		t.Fatalf("a single at-limit document must pass: %v", err)
	}
	many := []Document{one, one, one}
	if err := ValidateDocuments(many); err == nil {
		t.Fatal("documents over the total limit were accepted")
	}
}

// TestDocumentStringKeepsBytesOutOfLogs pins that a formatted Document does not
// print its payload.
func TestDocumentStringKeepsBytesOutOfLogs(t *testing.T) {
	d := Document{Label: "a.pdf", MIME: MIMEPDF, Data: testPDFBytes()}
	got := d.String()
	if strings.Contains(got, "%PDF") || strings.Contains(got, "120") {
		t.Fatalf("document bytes leaked into String(): %q", got)
	}
	if !strings.Contains(got, MIMEPDF) {
		t.Fatalf("String() should name the type, got %q", got)
	}
}

// TestTextOnlyMessageSerializesByteIdentically is the guard on the riskiest
// line in the documents change: MarshalJSON's fast path now tests two slices
// instead of one, and a message with no attachments must still produce exactly
// the JSON it produced before documents existed — a content array where a
// string was expected is a 400 on every request to every provider.
//
// The expected bytes are written out literally rather than computed, so the
// test cannot drift with the implementation it is guarding.
func TestTextOnlyMessageSerializesByteIdentically(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
		want string
	}{
		{
			name: "plain user turn",
			msg:  Message{Role: RoleUser, Content: "hello"},
			want: `{"role":"user","content":"hello"}`,
		},
		{
			name: "tool result",
			msg:  Message{Role: RoleTool, Content: "ok", Name: "shell", ToolCallID: "call_1"},
			want: `{"role":"tool","content":"ok","name":"shell","tool_call_id":"call_1"}`,
		},
		{
			name: "empty content assistant turn with a tool call",
			msg: Message{Role: RoleAssistant, ToolCalls: []ToolCall{{
				ID: "call_1", Type: "function",
				Function: FunctionCall{Name: "shell", Arguments: `{"cmd":"ls"}`},
			}}},
			want: `{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"shell","arguments":"{\"cmd\":\"ls\"}"}}]}`,
		},
		{
			name: "explicitly empty attachment slices are still the fast path",
			msg:  Message{Role: RoleUser, Content: "hi", Images: []Image{}, Documents: []Document{}},
			want: `{"role":"user","content":"hi"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.msg)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("text-only serialisation changed\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

// TestMessageWithDocumentSerializesAsAFilePart pins the wire shape: OpenAI's
// Chat Completions "file" content part, a file object with filename and a
// file_data data: URL.
func TestMessageWithDocumentSerializesAsAFilePart(t *testing.T) {
	doc, err := NewDocument("report.pdf", testPDFBytes())
	if err != nil {
		t.Fatalf("NewDocument: %v", err)
	}
	raw, err := json.Marshal(Message{Role: RoleUser, Content: "summarise", Documents: []Document{doc}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			File *struct {
				Filename string `json:"filename"`
				FileData string `json:"file_data"`
			} `json:"file"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("content should be an array of parts, got %s (%v)", raw, err)
	}
	if len(got.Content) != 2 {
		t.Fatalf("want a text part and a file part, got %d: %s", len(got.Content), raw)
	}
	if got.Content[0].Type != "text" || got.Content[0].Text != "summarise" {
		t.Fatalf("first part should be the caption, got %+v", got.Content[0])
	}
	f := got.Content[1]
	if f.Type != "file" || f.File == nil {
		t.Fatalf("second part should be a file part, got %+v", f)
	}
	if f.File.Filename != "report.pdf" {
		t.Fatalf("filename = %q, want report.pdf", f.File.Filename)
	}
	if !strings.HasPrefix(f.File.FileData, "data:application/pdf;base64,") {
		t.Fatalf("file_data should be a pdf data URL, got %.40q", f.File.FileData)
	}
}

// TestMessageWithBothAttachmentKindsKeepsImagesFirst pins the part ordering so
// a message carrying a photo and a PDF is not silently reshuffled.
func TestMessageWithBothAttachmentKindsKeepsImagesFirst(t *testing.T) {
	img, err := NewImage("a.png", testPNGBytes(t))
	if err != nil {
		t.Fatalf("NewImage: %v", err)
	}
	doc, err := NewDocument("b.pdf", testPDFBytes())
	if err != nil {
		t.Fatalf("NewDocument: %v", err)
	}
	raw, err := json.Marshal(Message{Role: RoleUser, Images: []Image{img}, Documents: []Document{doc}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got struct {
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Content) != 2 || got.Content[0].Type != "image_url" || got.Content[1].Type != "file" {
		t.Fatalf("part order changed: %s", raw)
	}
}

func TestIsSupportedDocumentMIME(t *testing.T) {
	for _, ok := range []string{"application/pdf", "Application/PDF", " application/pdf ", "application/pdf; charset=binary"} {
		if !IsSupportedDocumentMIME(ok) {
			t.Errorf("IsSupportedDocumentMIME(%q) = false, want true", ok)
		}
	}
	for _, no := range []string{"", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "image/png", "text/plain"} {
		if IsSupportedDocumentMIME(no) {
			t.Errorf("IsSupportedDocumentMIME(%q) = true, want false", no)
		}
	}
}

func TestDocumentFilenameFallsBackWhenUnlabelled(t *testing.T) {
	if got := (Document{}).Filename(); got != "document.pdf" {
		t.Fatalf("Filename() = %q, want document.pdf", got)
	}
	if got := (Document{Label: "  "}).Filename(); got != "document.pdf" {
		t.Fatalf("blank label should fall back, got %q", got)
	}
}
