package providers

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
)

// Image is one image attached to a user message.
//
// It holds raw bytes rather than a URL on purpose: joshbot's images arrive from
// a Telegram download or a local file, never from a public address the provider
// could fetch, so a URL would only ever be a data: URL. Keeping the decoded
// bytes means the size limits below are checked against what was actually
// received rather than against a base64 expansion of it, and it keeps the
// base64 encoding at the edge — one place, immediately before serialisation.
type Image struct {
	// MIME is the type detected by sniffing the content. It is never taken
	// from a filename: an attacker-supplied name is not evidence of anything,
	// and a mislabelled attachment reaches the provider as a 400 that reads
	// like a model problem.
	MIME string
	// Data is the raw, undecoded image.
	Data []byte
}

// Image limits. These are deliberately explicit constants rather than config:
// they exist to keep a single message from being unsendable or from costing an
// unbounded amount, and a provider's own limit is lower than anything an
// operator would plausibly raise these to.
const (
	// MaxImageBytes caps one image. Base64 inflates by 4/3, so 5 MiB of image
	// is roughly 6.7 MiB on the wire.
	MaxImageBytes = 5 << 20
	// MaxTotalImageBytes caps every image in one request together, so a
	// message carrying many just-under-limit images cannot add up to a payload
	// no provider will accept.
	MaxTotalImageBytes = 20 << 20
)

// supportedImageMIME is the set the OpenAI-compatible image_url part accepts.
// A type outside it is refused locally rather than sent, because a provider
// rejects it as an opaque 400 with no indication that the attachment was the
// cause.
var supportedImageMIME = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// SupportedImageMIMETypes lists the accepted types, sorted, for error messages
// and documentation.
func SupportedImageMIMETypes() []string {
	return []string{"image/gif", "image/jpeg", "image/png", "image/webp"}
}

// NewImage builds an Image from raw bytes, detecting the type by content.
//
// The label is used only in the error message — it is what the operator called
// the thing (a filename, "telegram photo") and never influences the detected
// type. Detection uses http.DetectContentType, which reads magic bytes, so a
// PNG named .txt is accepted as a PNG and a text file named .png is refused.
func NewImage(label string, data []byte) (Image, error) {
	if len(data) == 0 {
		return Image{}, fmt.Errorf("image %s is empty", describeImage(label))
	}
	if len(data) > MaxImageBytes {
		return Image{}, fmt.Errorf("image %s is %s, over the %s per-image limit",
			describeImage(label), humanBytes(len(data)), humanBytes(MaxImageBytes))
	}
	mime := sniffImageMIME(data)
	if !supportedImageMIME[mime] {
		return Image{}, fmt.Errorf("image %s is %s, which is not a supported image type; supported: %s",
			describeImage(label), mime, strings.Join(SupportedImageMIMETypes(), ", "))
	}
	return Image{MIME: mime, Data: data}, nil
}

// sniffImageMIME detects the type from the content alone. DetectContentType
// returns a charset parameter for text, which is noise in an error message, so
// the parameters are dropped.
func sniffImageMIME(data []byte) string {
	mime := http.DetectContentType(data)
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	return mime
}

// DataURL renders the image as the data: URL an OpenAI-compatible image_url
// part expects.
func (im Image) DataURL() string {
	return "data:" + im.MIME + ";base64," + base64.StdEncoding.EncodeToString(im.Data)
}

// String keeps image bytes out of logs. The %v of a struct holding a []byte
// prints every byte, so a single logged request would bury a log file under a
// megabyte of decimal numbers — and the image itself is user content that has
// no business being written to disk unredacted.
func (im Image) String() string {
	return fmt.Sprintf("image(%s, %s)", im.MIME, humanBytes(len(im.Data)))
}

// ValidateImages enforces the total-payload limit across a whole request. The
// per-image limit is checked at construction; this is the one that can only be
// known once all the attachments are together.
func ValidateImages(images []Image) error {
	total := 0
	for _, im := range images {
		total += len(im.Data)
	}
	if total > MaxTotalImageBytes {
		return fmt.Errorf("attached images total %s, over the %s limit for one message",
			humanBytes(total), humanBytes(MaxTotalImageBytes))
	}
	return nil
}

func describeImage(label string) string {
	if strings.TrimSpace(label) == "" {
		return "attachment"
	}
	return fmt.Sprintf("%q", label)
}

// humanBytes formats a size the way the limit is written, so an error can be
// compared to the constant it names without arithmetic.
func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// IsSupportedImageMIME reports whether a declared content type is one joshbot
// can send. It exists for channels deciding whether an attachment is worth
// downloading at all; the declared type is never trusted as the real one —
// NewImage sniffs the bytes.
func IsSupportedImageMIME(mime string) bool {
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = mime[:i]
	}
	return supportedImageMIME[strings.ToLower(strings.TrimSpace(mime))]
}
