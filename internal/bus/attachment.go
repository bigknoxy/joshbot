package bus

import "fmt"

// AttachmentKind decides how a channel presents an outbound file: inline media
// or a file to download. It is a typed field on Attachment rather than a string
// in OutboundMessage.Metadata on purpose — a misspelled map key sends nothing
// and reports success, which is exactly the failure mode outbound media must
// not have.
type AttachmentKind string

const (
	// AttachmentPhoto is content sniffed as a supported image, small enough to
	// go out as inline media.
	AttachmentPhoto AttachmentKind = "photo"
	// AttachmentDocument is everything else: a file the recipient downloads.
	AttachmentDocument AttachmentKind = "document"
)

// Attachment is one file leaving joshbot on an outbound message.
//
// Kind and MIME are decided by sniffing the bytes, never by the filename — the
// same rule inbound images follow (providers.NewImage). A ".png" holding prose
// is a document, and a ".dat" holding a PNG is a photo.
//
// Data carries the bytes for anything at or under AttachmentLimits.InlineMaxBytes
// so the send needs no second read of a file that may have changed underneath
// it. Above that the bytes would be a large, pointless copy through the bus, so
// only Path is carried and the channel streams from disk. Path is always set:
// it is what a channel with no native attachment support names instead of
// silently dropping the file.
type Attachment struct {
	Filename string
	MIME     string
	Kind     AttachmentKind
	Size     int64
	Data     []byte
	Path     string
}

// String keeps attachment bytes out of logs, the way providers.Image does.
func (a Attachment) String() string {
	return fmt.Sprintf("attachment(%s, %s, %s, %d B)", a.Filename, a.Kind, a.MIME, a.Size)
}

// Default outbound attachment sizes, in bytes. The photo and document values
// are the Telegram Bot API's own upload limits, which are the binding
// constraint today; they are reachable through AttachmentLimits rather than
// referenced directly at each use site so a future per-channel or per-api_url
// override (issue #280, a self-hosted Bot API server raises both) has one
// place to land.
const (
	DefaultPhotoMaxBytes    int64 = 10 << 20
	DefaultDocumentMaxBytes int64 = 50 << 20
	// DefaultInlineMaxBytes is the point above which the bytes stay on disk.
	DefaultInlineMaxBytes int64 = 10 << 20
)

// AttachmentLimits bounds what may be sent out. A zero field means "use the
// default", so a partially-filled override is still usable.
type AttachmentLimits struct {
	PhotoMaxBytes    int64
	DocumentMaxBytes int64
	InlineMaxBytes   int64
}

// DefaultAttachmentLimits returns the built-in limits.
func DefaultAttachmentLimits() AttachmentLimits {
	return AttachmentLimits{
		PhotoMaxBytes:    DefaultPhotoMaxBytes,
		DocumentMaxBytes: DefaultDocumentMaxBytes,
		InlineMaxBytes:   DefaultInlineMaxBytes,
	}
}

// WithDefaults fills any unset field from the defaults.
func (l AttachmentLimits) WithDefaults() AttachmentLimits {
	d := DefaultAttachmentLimits()
	if l.PhotoMaxBytes <= 0 {
		l.PhotoMaxBytes = d.PhotoMaxBytes
	}
	if l.DocumentMaxBytes <= 0 {
		l.DocumentMaxBytes = d.DocumentMaxBytes
	}
	if l.InlineMaxBytes <= 0 {
		l.InlineMaxBytes = d.InlineMaxBytes
	}
	return l
}
