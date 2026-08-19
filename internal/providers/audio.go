package providers

import (
	"bytes"
	"fmt"
)

// MaxAudioBytes bounds one audio upload. It matches the 25 MiB limit OpenAI's
// transcription endpoint enforces, so joshbot refuses locally — naming the
// limit — rather than spending the upload to be told no by the provider.
const MaxAudioBytes = 25 << 20

// SniffAudio reports the container MIME type of an audio payload, or an error
// when the bytes are not one of the containers a transcription endpoint
// accepts.
//
// Content decides the type, never the name, for the same reason it does for
// images (providers.NewImage): a multipart filename and its declared
// Content-Type are both written by the caller, so a .mp3 that is really a
// 25 MiB text file would otherwise be uploaded to the operator's provider and
// billed before anything noticed. The set is the one OpenAI documents —
// flac, mp3/mpga/mpeg, mp4/m4a, ogg, wav, webm — because a container outside
// it is refused upstream anyway.
func SniffAudio(data []byte) (string, error) {
	switch {
	case len(data) < 12:
		return "", fmt.Errorf("audio is too short to identify")
	case bytes.HasPrefix(data, []byte("fLaC")):
		return "audio/flac", nil
	case bytes.HasPrefix(data, []byte("OggS")):
		return "audio/ogg", nil
	case bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE")):
		return "audio/wav", nil
	case bytes.Equal(data[4:8], []byte("ftyp")):
		return "audio/mp4", nil
	case bytes.HasPrefix(data, []byte{0x1A, 0x45, 0xDF, 0xA3}):
		return "audio/webm", nil
	case bytes.HasPrefix(data, []byte("ID3")):
		return "audio/mpeg", nil
	case data[0] == 0xFF && data[1]&0xE0 == 0xE0:
		// MPEG audio frame sync: eleven set bits. An MP3 with no ID3 tag
		// starts straight at a frame header.
		return "audio/mpeg", nil
	}
	return "", fmt.Errorf("unrecognized audio format: expected flac, mp3, mp4/m4a, mpeg, ogg, wav or webm")
}
