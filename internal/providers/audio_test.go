package providers

import (
	"bytes"
	"strings"
	"testing"
)

// TestSniffAudioAcceptsEachContainer pins the accept set. It matters because
// SniffAudio is the only thing standing between a caller-written filename and an
// upload billed to the operator's transcription provider: a container that stops
// being recognized is refused at the API with a 400 that looks like the caller's
// fault.
func TestSniffAudioAcceptsEachContainer(t *testing.T) {
	pad := func(b []byte) []byte {
		for len(b) < 16 {
			b = append(b, 0)
		}
		return b
	}
	for name, tc := range map[string]struct {
		data []byte
		want string
	}{
		"flac":           {pad([]byte("fLaC\x00\x00\x00\x22")), "audio/flac"},
		"ogg":            {pad([]byte("OggS\x00\x02\x00\x00")), "audio/ogg"},
		"wav":            {pad(append([]byte("RIFF"), []byte("\x24\x00\x00\x00WAVE")...)), "audio/wav"},
		"mp4":            {pad(append([]byte{0, 0, 0, 0x20}, []byte("ftypM4A ")...)), "audio/mp4"},
		"webm":           {pad([]byte{0x1A, 0x45, 0xDF, 0xA3, 1, 2, 3, 4}), "audio/webm"},
		"mp3 with id3":   {pad([]byte("ID3\x04\x00\x00\x00\x00")), "audio/mpeg"},
		"mp3 frame sync": {pad([]byte{0xFF, 0xFB, 0x90, 0x44, 0, 0, 0, 0}), "audio/mpeg"},
		"mp3 mpeg2 sync": {pad([]byte{0xFF, 0xF3, 0x40, 0xC4, 0, 0, 0, 0}), "audio/mpeg"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := SniffAudio(tc.data)
			if err != nil {
				t.Fatalf("SniffAudio: %v", err)
			}
			if got != tc.want {
				t.Fatalf("SniffAudio = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSniffAudioRefusesNonAudio is the security half. A caller can name a part
// "voice.mp3" and declare audio/mpeg; only the bytes are evidence. Each case
// here is something a naive check would wave through.
func TestSniffAudioRefusesNonAudio(t *testing.T) {
	for name, data := range map[string][]byte{
		"prose":         []byte("this is not audio, it is a long text file pretending to be one"),
		"json":          []byte(`{"file":"voice.mp3","really":"no"}`),
		"png":           append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, bytes.Repeat([]byte{0}, 16)...),
		"riff not wave": append([]byte("RIFF\x24\x00\x00\x00AVI "), bytes.Repeat([]byte{0}, 8)...),
		"too short":     []byte("fLaC"),
		"empty":         nil,
		"almost sync":   append([]byte{0xFF, 0x1B}, bytes.Repeat([]byte{0}, 16)...),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := SniffAudio(data)
			if err == nil {
				t.Fatalf("SniffAudio accepted %q as %q", name, got)
			}
		})
	}
}

// TestSniffAudioErrorNamesTheAcceptedSet keeps the refusal actionable: a caller
// who sends a wrong container needs to learn which ones work without reading the
// source.
func TestSniffAudioErrorNamesTheAcceptedSet(t *testing.T) {
	_, err := SniffAudio([]byte("plain text that is definitely long enough"))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"flac", "mp3", "ogg", "wav", "webm"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q", err, want)
		}
	}
}
