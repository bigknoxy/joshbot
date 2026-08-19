package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// oggBytes is a minimal payload that SniffAudio accepts. The endpoint sniffs
// content, so tests cannot get away with naming a part "x.ogg".
func oggBytes(n int) []byte {
	b := append([]byte("OggS\x00\x02\x00\x00"), bytes.Repeat([]byte{0}, 8)...)
	if n > len(b) {
		b = append(b, bytes.Repeat([]byte{0x41}, n-len(b))...)
	}
	return b
}

// audioForm builds a multipart body. field/filename are caller-controlled on the
// wire, so tests drive them directly.
func audioForm(t *testing.T, field, filename string, data []byte, extra map[string]string) (string, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if field != "" {
		part, err := mw.CreateFormFile(field, filename)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatalf("write part: %v", err)
		}
	}
	for k, v := range extra {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return mw.FormDataContentType(), buf.Bytes()
}

// transcribeServer builds a server with a transcriber wired. testServer
// deliberately does not, so the 501 path has its own coverage.
func transcribeServer(t *testing.T, tr Transcriber) *Server {
	t.Helper()
	s, err := New(&fakeAgent{}, Options{Listen: "127.0.0.1:0", APIKeys: []string{"secret"}, Transcriber: tr})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func postAudio(t *testing.T, s *Server, key, ctype string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(body))
	r.Header.Set("Content-Type", ctype)
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}

// TestTranscriptionsRequiresAuth is the assertion that matters most. The route
// spends the operator's upstream credential on whatever bytes it is handed, so
// an unauthenticated caller is a billing primitive at minimum.
func TestTranscriptionsRequiresAuth(t *testing.T) {
	called := false
	s := transcribeServer(t, func(context.Context, []byte, string) (string, error) {
		called = true
		return "hi", nil
	})
	ctype, body := audioForm(t, audioFormField, "a.ogg", oggBytes(32), nil)
	for name, key := range map[string]string{"no key": "", "wrong key": "nope"} {
		t.Run(name, func(t *testing.T) {
			w := postAudio(t, s, key, ctype, body)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
			if called {
				t.Fatal("the transcriber ran for an unauthenticated request")
			}
		})
	}
}

// TestTranscriptionsUnconfiguredNamesTheConfigKey pins the 501. Answering 200
// with an empty transcript would leave the caller unable to tell "no speech"
// from "never configured", and a 404 would read as a joshbot that cannot do this
// at all.
func TestTranscriptionsUnconfiguredNamesTheConfigKey(t *testing.T) {
	s := testServer(t, &fakeAgent{})
	ctype, body := audioForm(t, audioFormField, "a.ogg", oggBytes(32), nil)
	w := postAudio(t, s, "secret", ctype, body)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", w.Code)
	}
	if !strings.Contains(w.Body.String(), "stt.provider") {
		t.Fatalf("the 501 does not name the config key: %s", w.Body.String())
	}
}

// TestTranscriptionsRejectsGET keeps the route POST-only: a GET carrying no body
// would otherwise reach the multipart reader and report a confusing parse error.
func TestTranscriptionsRejectsGET(t *testing.T) {
	s := transcribeServer(t, func(context.Context, []byte, string) (string, error) { return "hi", nil })
	r := httptest.NewRequest(http.MethodGet, "/v1/audio/transcriptions", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

// TestTranscriptionsHappyPath checks the response shape and that the audio and a
// usable filename reach the provider: providers.TranscribeAudio keys format
// detection off the extension, so a dropped filename is a live failure.
func TestTranscriptionsHappyPath(t *testing.T) {
	var gotAudio []byte
	var gotName string
	s := transcribeServer(t, func(_ context.Context, audio []byte, filename string) (string, error) {
		gotAudio, gotName = audio, filename
		return "hello there", nil
	})
	want := oggBytes(64)
	ctype, body := audioForm(t, audioFormField, "voice.ogg", want, map[string]string{"model": "whisper-1", "language": "en"})
	w := postAudio(t, s, "secret", ctype, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !bytes.Equal(gotAudio, want) {
		t.Fatalf("the provider got %d bytes, want %d", len(gotAudio), len(want))
	}
	if gotName != "voice.ogg" {
		t.Fatalf("filename = %q, want voice.ogg", gotName)
	}
	if got := strings.TrimSpace(w.Body.String()); got != `{"text":"hello there"}` {
		t.Fatalf("body = %s", got)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
}

// TestTranscriptionsResponseFormatText covers the other documented format. A
// client asking for text and receiving JSON gets a transcript with quotes and a
// JSON envelope baked into it.
func TestTranscriptionsResponseFormatText(t *testing.T) {
	s := transcribeServer(t, func(context.Context, []byte, string) (string, error) { return "hello there", nil })
	ctype, body := audioForm(t, audioFormField, "voice.ogg", oggBytes(32), map[string]string{"response_format": "TEXT"})
	w := postAudio(t, s, "secret", ctype, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != "hello there" {
		t.Fatalf("body = %q", w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
}

// TestTranscriptionsRefusesNonAudioBeforeSpending is the content-decides-the-type
// rule at the boundary: a text file named .mp3 must never reach the provider.
func TestTranscriptionsRefusesNonAudioBeforeSpending(t *testing.T) {
	called := false
	s := transcribeServer(t, func(context.Context, []byte, string) (string, error) {
		called = true
		return "", nil
	})
	ctype, body := audioForm(t, audioFormField, "voice.mp3", []byte("not audio at all, just prose"), nil)
	w := postAudio(t, s, "secret", ctype, body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if called {
		t.Fatal("the transcriber ran on a non-audio upload")
	}
}

// TestTranscriptionsRefusesOverLimit pins the cap. Without it an authenticated
// caller can stream unbounded bytes into the operator's provider; the transcriber
// must not run, because the point is to refuse before spending.
func TestTranscriptionsRefusesOverLimit(t *testing.T) {
	called := false
	s := transcribeServer(t, func(context.Context, []byte, string) (string, error) {
		called = true
		return "", nil
	})
	ctype, body := audioForm(t, audioFormField, "voice.ogg", oggBytes(providers.MaxAudioBytes+1), nil)
	w := postAudio(t, s, "secret", ctype, body)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", w.Code, w.Body.String())
	}
	if called {
		t.Fatal("the transcriber ran on an over-limit upload")
	}
}

// TestTranscriptionsRejectsMalformedForms covers the shapes a client gets wrong.
// Each must be a 400 naming the problem rather than a panic or a 502 blaming the
// provider.
func TestTranscriptionsRejectsMalformedForms(t *testing.T) {
	s := transcribeServer(t, func(context.Context, []byte, string) (string, error) { return "hi", nil })

	t.Run("missing file field", func(t *testing.T) {
		ctype, body := audioForm(t, "", "", nil, map[string]string{"model": "whisper-1"})
		w := postAudio(t, s, "secret", ctype, body)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), audioFormField) {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
	})
	t.Run("empty file field", func(t *testing.T) {
		ctype, body := audioForm(t, audioFormField, "voice.ogg", nil, nil)
		w := postAudio(t, s, "secret", ctype, body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
	})
	t.Run("not multipart", func(t *testing.T) {
		w := postAudio(t, s, "secret", "application/json", []byte(`{"file":"voice.ogg"}`))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
	})
}

// TestTranscriptionsRedactsUpstreamErrors is the #238 origin split. The provider
// error carries the operator's endpoint and can carry their key; an API caller is
// authenticated but is not the operator.
func TestTranscriptionsRedactsUpstreamErrors(t *testing.T) {
	const secret = "sk-live-abcdefghijklmnopqrstuvwxyz012345"
	s := transcribeServer(t, func(context.Context, []byte, string) (string, error) {
		return "", fmt.Errorf("transcription failed: 401 authorization: Bearer %s", secret)
	})
	ctype, body := audioForm(t, audioFormField, "voice.ogg", oggBytes(32), nil)
	w := postAudio(t, s, "secret", ctype, body)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Fatalf("the upstream credential reached the client: %s", w.Body.String())
	}
}

// TestReadAudioUploadDefaultsAFilename pins the fallback. TranscribeAudio picks
// the format from the extension, so an upload with no filename would otherwise be
// refused upstream with an error the caller cannot act on.
func TestReadAudioUploadDefaultsAFilename(t *testing.T) {
	ctype, body := audioForm(t, audioFormField, "", oggBytes(32), nil)
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(body))
	r.Header.Set("Content-Type", ctype)
	_, filename, _, err := readAudioUpload(r)
	if err != nil {
		t.Fatalf("readAudioUpload: %v", err)
	}
	if !strings.Contains(filename, ".") {
		t.Fatalf("filename = %q, want one carrying an extension", filename)
	}
}

// TestReadAudioUploadRefusesTwoFileParts stops the last-part-wins ambiguity: a
// caller could otherwise pair a benign sniffed part with a second one and leave
// which gets transcribed up to iteration order.
func TestReadAudioUploadRefusesTwoFileParts(t *testing.T) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for i := 0; i < 2; i++ {
		part, err := mw.CreateFormFile(audioFormField, fmt.Sprintf("a%d.ogg", i))
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := part.Write(oggBytes(32)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(buf.Bytes()))
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if _, _, _, err := readAudioUpload(r); err == nil {
		t.Fatal("readAudioUpload accepted two file parts")
	} else if !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestTranscriptionsPropagatesRequestCancellation keeps a disconnected client
// from holding a provider call open.
func TestTranscriptionsPropagatesRequestCancellation(t *testing.T) {
	s := transcribeServer(t, func(ctx context.Context, _ []byte, _ string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "", errors.New("expected a cancelled context")
	})
	ctype, body := audioForm(t, audioFormField, "voice.ogg", oggBytes(32), nil)
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(body))
	r.Header.Set("Content-Type", ctype)
	r.Header.Set("Authorization", "Bearer secret")
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r.WithContext(ctx))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "expected a cancelled context") {
		t.Fatal("the request context did not reach the transcriber")
	}
}
