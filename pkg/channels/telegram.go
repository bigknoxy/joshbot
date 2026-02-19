package channels

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"runtime"
)

// OutboundMessage represents a message to be sent via a channel.
type OutboundMessage struct {
	Text     string
	Metadata map[string]string
}

// SendTelegram enqueues/sends a Telegram message with instrumentation.
// It logs length and SHA256 hex digest (not the content) and attaches a trace_id.
// If the message text is empty, the send is skipped but instrumentation is logged.
func SendTelegram(msg *OutboundMessage) error {
	if msg.Metadata == nil {
		msg.Metadata = make(map[string]string)
	}

	// compute length and sha256 digest
	length := len(msg.Text)
	h := sha256.Sum256([]byte(msg.Text))
	digest := hex.EncodeToString(h[:])

	// generate trace id
	traceID, err := uuidV4()
	if err != nil {
		return fmt.Errorf("failed to generate trace id: %w", err)
	}
	msg.Metadata["trace_id"] = traceID

	// origin (file:line) if possible
	var origin string
	if pc, file, line, ok := runtime.Caller(1); ok {
		fn := ""
		if f := runtime.FuncForPC(pc); f != nil {
			fn = f.Name()
		}
		origin = fmt.Sprintf("%s:%d (%s)", file, line, fn)
	} else {
		origin = "unknown"
	}

	// log instrumentation (do not log full content)
	log.Printf("telegram: trace_id=%s length=%d sha256=%s origin=%s", traceID, length, digest, origin)

	if length == 0 {
		// skip sending empty messages
		log.Printf("telegram: trace_id=%s skipping send because message is empty", traceID)
		return nil
	}

	// Simulate send (no external network call). Mark as sent in metadata.
	msg.Metadata["sent"] = "true"
	log.Printf("telegram: trace_id=%s sent message (len=%d)", traceID, length)
	return nil
}

// uuidV4 returns a RFC4122 version 4 UUID using crypto/rand.
func uuidV4() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// set version (4) and variant bits per RFC4122
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	// format
	uuid := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	return uuid, nil
}
