package providers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The request must be a well-formed multipart POST to /audio/transcriptions
// carrying the audio, the model and the bearer key — the OpenAI-compatible
// shape Groq and OpenAI both accept.
func TestTranscribeAudioSendsAWellFormedRequest(t *testing.T) {
	var gotPath, gotAuth, gotModel, gotFile string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		gotModel = r.FormValue("model")
		f, hdr, err := r.FormFile("file")
		if err != nil {
			t.Errorf("file part: %v", err)
		} else {
			data, _ := io.ReadAll(f)
			gotFile = hdr.Filename + ":" + string(data)
			_ = f.Close()
		}
		_, _ = w.Write([]byte(`{"text": "  hello from the voice note  "}`))
	}))
	defer srv.Close()

	text, err := TranscribeAudio(context.Background(), TranscribeConfig{
		APIBase: srv.URL + "/v1/",
		APIKey:  "k-123",
		Model:   "whisper-large-v3-turbo",
	}, []byte("OGGDATA"), "voice.ogg")
	if err != nil {
		t.Fatalf("TranscribeAudio: %v", err)
	}
	if text != "hello from the voice note" {
		t.Errorf("text = %q, want the trimmed transcript", text)
	}
	if gotPath != "/v1/audio/transcriptions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer k-123" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotModel != "whisper-large-v3-turbo" {
		t.Errorf("model = %q", gotModel)
	}
	if gotFile != "voice.ogg:OGGDATA" {
		t.Errorf("file = %q", gotFile)
	}
}

// A non-200 is an error carrying a bounded body snippet, an empty transcript
// is an error rather than an empty success, and an empty APIBase is refused
// rather than defaulted — dialling a default endpoint would send the audio
// and the credential to a provider the operator did not choose.
func TestTranscribeAudioFailureModes(t *testing.T) {
	t.Run("empty api base refused", func(t *testing.T) {
		_, err := TranscribeAudio(context.Background(), TranscribeConfig{Model: "m"}, []byte("x"), "v.ogg")
		if err == nil || !strings.Contains(err.Error(), "no API base") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("http error surfaces status and snippet", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"bad key"}`))
		}))
		defer srv.Close()
		_, err := TranscribeAudio(context.Background(), TranscribeConfig{APIBase: srv.URL, Model: "m"}, []byte("x"), "v.ogg")
		if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "bad key") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("empty transcript is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"text": "   "}`))
		}))
		defer srv.Close()
		_, err := TranscribeAudio(context.Background(), TranscribeConfig{APIBase: srv.URL, Model: "m"}, []byte("x"), "v.ogg")
		if err == nil || !strings.Contains(err.Error(), "empty") {
			t.Fatalf("err = %v", err)
		}
	})
}
