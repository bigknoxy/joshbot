package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// DefaultTranscribeTimeout bounds one transcription request when the config
// does not set one. Transcription of a few minutes of audio typically takes
// seconds; a minute covers a slow provider without pinning a turn forever.
const DefaultTranscribeTimeout = 60 * time.Second

// TranscribeConfig is everything one transcription request needs. It is built
// by the caller from an already-resolved provider config — the credential and
// endpoint come from the same place a chat request's would, so there is no
// second credential store to keep in sync.
type TranscribeConfig struct {
	// APIBase is the provider's OpenAI-compatible base URL. Empty is refused,
	// never defaulted: dialling a default endpoint would send the audio (and
	// the credential) to a provider the operator did not choose — the same
	// rule ListModels and ProbeCredential enforce.
	APIBase string
	APIKey  string
	// Model is the transcription model id (e.g. whisper-large-v3-turbo).
	Model   string
	Timeout time.Duration
	// HTTPClient overrides the client, for tests. Nil uses a default.
	HTTPClient *http.Client
}

// TranscribeAudio sends one audio attachment to the provider's
// OpenAI-compatible POST /audio/transcriptions endpoint and returns the
// transcript text. Telegram voice notes are OGG/Opus, which OpenAI and Groq
// both accept directly — no local decoding.
func TranscribeAudio(ctx context.Context, cfg TranscribeConfig, audio []byte, filename string) (string, error) {
	if strings.TrimSpace(cfg.APIBase) == "" {
		return "", fmt.Errorf("no API base URL configured for transcription")
	}
	if cfg.Model == "" {
		return "", fmt.Errorf("no transcription model configured")
	}
	if len(audio) == 0 {
		return "", fmt.Errorf("empty audio")
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("build transcription request: %w", err)
	}
	if _, err := part.Write(audio); err != nil {
		return "", fmt.Errorf("build transcription request: %w", err)
	}
	if err := w.WriteField("model", cfg.Model); err != nil {
		return "", fmt.Errorf("build transcription request: %w", err)
	}
	if err := w.WriteField("response_format", "json"); err != nil {
		return "", fmt.Errorf("build transcription request: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("build transcription request: %w", err)
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTranscribeTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	url := strings.TrimRight(cfg.APIBase, "/") + "/audio/transcriptions"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, &body)
	if err != nil {
		return "", fmt.Errorf("build transcription request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("transcription request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// The error body is bounded before it can reach a log line or a chat.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read transcription response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := strings.TrimSpace(string(data))
		if len(snippet) > 300 {
			snippet = snippet[:300] + "..."
		}
		return "", fmt.Errorf("transcription failed: %s: %s", resp.Status, snippet)
	}

	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", fmt.Errorf("parse transcription response: %w", err)
	}
	if strings.TrimSpace(parsed.Text) == "" {
		return "", fmt.Errorf("the transcription came back empty")
	}
	return strings.TrimSpace(parsed.Text), nil
}
