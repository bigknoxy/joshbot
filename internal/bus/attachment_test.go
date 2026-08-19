package bus

import "testing"

func TestAttachmentLimits_WithDefaultsFillsOnlyZeroFields(t *testing.T) {
	got := AttachmentLimits{PhotoMaxBytes: 5}.WithDefaults()
	if got.PhotoMaxBytes != 5 {
		t.Errorf("PhotoMaxBytes = %d, want the caller's override kept", got.PhotoMaxBytes)
	}
	if got.DocumentMaxBytes != DefaultDocumentMaxBytes {
		t.Errorf("DocumentMaxBytes = %d, want the default", got.DocumentMaxBytes)
	}
}

func TestDefaultAttachmentLimitsAreNonZero(t *testing.T) {
	l := DefaultAttachmentLimits()
	if l.PhotoMaxBytes <= 0 || l.DocumentMaxBytes <= 0 {
		t.Fatalf("a zero default would refuse every file: %+v", l)
	}
	if l.PhotoMaxBytes > l.DocumentMaxBytes {
		t.Errorf("photo limit %d exceeds the document limit %d; an oversize image could not degrade to a document",
			l.PhotoMaxBytes, l.DocumentMaxBytes)
	}
}

// The attachment must survive the bus as a typed value: an outbound message
// carrying one has to be distinguishable from one that never had a file.
func TestOutboundMessageCarriesAttachments(t *testing.T) {
	msg := OutboundMessage{Content: "caption", Attachments: []Attachment{{Filename: "a.png", Kind: AttachmentPhoto, Size: 3}}}
	if len(msg.Attachments) != 1 || msg.Attachments[0].Kind != AttachmentPhoto {
		t.Fatalf("attachments did not survive: %+v", msg.Attachments)
	}
	if (OutboundMessage{Content: "x"}).Attachments != nil {
		t.Error("a plain text message must carry no attachments")
	}
}
