package providers

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
)

// Document is one non-image file attached to a user message.
//
// It is the exact sibling of Image and exists for the same reasons: joshbot's
// documents arrive from a Telegram download or a local file, never from a
// public address a provider could fetch, so keeping the decoded bytes means the
// limits below are checked against what was actually received rather than
// against a base64 expansion of it, and the encoding happens once, immediately
// before serialisation.
type Document struct {
	// Label is what the sender called it — a filename, usually. It is
	// untrusted text: it rides the wire as the part's filename (providers use
	// it only as a display hint) and is never evidence of the content type.
	Label string
	// MIME is the type detected by sniffing the content. It is never taken
	// from a filename or a declared content type: a mislabelled attachment
	// reaches the provider as a 400 that reads like a model problem.
	MIME string
	// Data is the raw, undecoded document.
	Data []byte
}

// Document limits. Like the image limits these are explicit constants rather
// than config: they keep one message from being unsendable or from costing an
// unbounded amount, and every provider's own limit is lower than anything an
// operator would plausibly raise these to.
const (
	// MaxDocumentBytes caps one document. Base64 inflates by 4/3, so 8 MiB of
	// PDF is roughly 10.7 MiB on the wire.
	MaxDocumentBytes = 8 << 20
	// MaxTotalDocumentBytes caps every document in one request together. 16
	// MiB of PDF is about 21.3 MiB base64, which leaves headroom under
	// Anthropic's documented 32 MB maximum request size.
	MaxTotalDocumentBytes = 16 << 20
)

// MIMEPDF is the one document type joshbot can currently send. Office formats
// (docx/xlsx/pptx) are zipped XML that no provider accepts as a document part,
// so they stay refused honestly rather than uploaded to be rejected.
const MIMEPDF = "application/pdf"

// SupportedDocumentMIMETypes lists the accepted types, sorted, for error
// messages and documentation.
func SupportedDocumentMIMETypes() []string {
	return []string{MIMEPDF}
}

// pdfMagic is the signature every standard PDF starts with. http.DetectContentType
// recognises it too, but sniffing it directly keeps the rule visible and does
// not depend on that table gaining or losing entries.
var pdfMagic = []byte("%PDF-")

// NewDocument builds a Document from raw bytes, detecting the type by content.
//
// The label is used in the error message and as the wire filename; it never
// influences the detected type. A PDF named .png is accepted as a PDF and a PNG
// named .pdf is refused — content decides the type, never the label.
func NewDocument(label string, data []byte) (Document, error) {
	if len(data) == 0 {
		return Document{}, fmt.Errorf("document %s is empty", describeImage(label))
	}
	if len(data) > MaxDocumentBytes {
		return Document{}, fmt.Errorf("document %s is %s, over the %s per-document limit",
			describeImage(label), humanBytes(len(data)), humanBytes(MaxDocumentBytes))
	}
	if !bytes.HasPrefix(data, pdfMagic) {
		return Document{}, fmt.Errorf("document %s is %s, which is not a supported document type; supported: %s",
			describeImage(label), sniffImageMIME(data), strings.Join(SupportedDocumentMIMETypes(), ", "))
	}
	return Document{Label: label, MIME: MIMEPDF, Data: data}, nil
}

// IsSupportedDocumentMIME reports whether a declared content type is one
// joshbot can send. It exists for channels deciding whether an attachment is
// worth downloading at all; the declared type is never trusted as the real one
// — NewDocument sniffs the bytes.
func IsSupportedDocumentMIME(mime string) bool {
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = mime[:i]
	}
	return strings.ToLower(strings.TrimSpace(mime)) == MIMEPDF
}

// DataURL renders the document as the data: URL an OpenAI-compatible file part
// expects in its file_data field.
func (d Document) DataURL() string {
	return "data:" + d.MIME + ";base64," + base64.StdEncoding.EncodeToString(d.Data)
}

// Filename is what rides the wire as the part's filename. Providers use it as a
// display hint only, but an empty one is rejected by some, so a document with
// no label gets a generic name rather than none.
func (d Document) Filename() string {
	name := strings.TrimSpace(d.Label)
	if name == "" {
		return "document.pdf"
	}
	return name
}

// String keeps document bytes out of logs, for the same reason Image.String
// does: the %v of a struct holding a []byte prints every byte, and the document
// itself is user content with no business being written to disk unredacted.
func (d Document) String() string {
	return fmt.Sprintf("document(%s, %s)", d.MIME, humanBytes(len(d.Data)))
}

// ValidateDocuments enforces the total-payload limit across a whole request.
// The per-document limit is checked at construction; this is the one that can
// only be known once all the attachments are together.
func ValidateDocuments(docs []Document) error {
	total := 0
	for _, d := range docs {
		total += len(d.Data)
	}
	if total > MaxTotalDocumentBytes {
		return fmt.Errorf("attached documents total %s, over the %s limit for one message",
			humanBytes(total), humanBytes(MaxTotalDocumentBytes))
	}
	return nil
}
