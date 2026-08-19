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
// Data is always populated for a valid attachment, and it is the only source of
// outbound bytes. There is deliberately no second route: the bytes are read once
// through the workspace containment walk that validated the path, and everything
// downstream sends exactly those bytes. An earlier design carried an absolute
// path for larger files and let the channel re-open it, which was CVE-shaped —
// the contained walk validated one file while an uncontained os.Open on the
// channel goroutine, after a bus hop and again on every retry, sent whatever the
// leaf pointed at by then.
type Attachment struct {
	Filename string
	MIME     string
	Kind     AttachmentKind
	Size     int64
	Data     []byte
	// SourcePath is a workspace-relative label for humans — what a channel with
	// no native attachment support names instead of silently dropping the file.
	// Nothing opens it, ever. It is relative rather than absolute both because
	// re-opening it is not a thing any code may do, and because it is printed
	// into chat, where an absolute path discloses the operator's home directory.
	SourcePath string
}

// String keeps attachment bytes out of logs, the way providers.Image does.
func (a Attachment) String() string {
	return fmt.Sprintf("attachment(%s, %s, %s, %d B)", a.Filename, a.Kind, a.MIME, a.Size)
}

// Default outbound attachment sizes, in bytes. They are reachable through
// AttachmentLimits rather than referenced directly at each use site so a future
// per-channel or per-api_url override (issue #280, a self-hosted Bot API server
// raises them) has one place to land.
const (
	DefaultPhotoMaxBytes int64 = 10 << 20
	// DefaultDocumentMaxBytes is 10 MiB, well under Telegram's own 50 MiB
	// ceiling for a document. That is deliberate and is a memory bound, not a
	// protocol bound: every outbound byte must come from the single contained
	// read, so the whole payload is held in memory from the tool call until the
	// channel finishes uploading it. Raising this under issue #280 raises peak
	// memory use with it — that is the tradeoff taken knowingly, in exchange for
	// there being no second, uncontained way to put bytes on the wire.
	DefaultDocumentMaxBytes int64 = 10 << 20
)

// AttachmentLimits bounds what may be sent out. A zero field means "use the
// default", so a partially-filled override is still usable.
type AttachmentLimits struct {
	PhotoMaxBytes    int64
	DocumentMaxBytes int64
}

// DefaultAttachmentLimits returns the built-in limits.
func DefaultAttachmentLimits() AttachmentLimits {
	return AttachmentLimits{
		PhotoMaxBytes:    DefaultPhotoMaxBytes,
		DocumentMaxBytes: DefaultDocumentMaxBytes,
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
	return l
}
