package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// audioFormField is the multipart field OpenAI's transcription API carries the
// audio in.
const audioFormField = "file"

// handleTranscriptions serves POST /v1/audio/transcriptions.
//
// Unlike /v1/chat/completions this is not the agent: there is no ReAct loop, no
// session and no memory. It is a thin, authenticated front for the transcriber
// the operator already configured under `stt`, so a client that speaks the
// OpenAI audio API can reach the same provider joshbot uses for Telegram voice
// notes without a second credential store.
func (s *Server) handleTranscriptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "", "Method not allowed; use POST.")
		return
	}
	// Nil rather than a stub, so the 501 says which config key turns it on.
	// Answering 200 with an empty transcript would be the silent-failure
	// anti-pattern: the caller cannot tell "no speech" from "not configured".
	if s.transcriber == nil {
		writeError(w, http.StatusNotImplemented, "invalid_request_error", "transcription_not_configured",
			"Transcription is not configured. Set stt.provider (and stt.model) in config.json.")
		return
	}

	// The audio cap is deliberately not MaxRequestBytes: a chat turn is a
	// message and an upload is an upload. MaxBytesReader bounds the whole
	// request so multipart headers cannot be used to smuggle past the file cap.
	r.Body = http.MaxBytesReader(w, r.Body, providers.MaxAudioBytes+multipartOverheadBytes)

	audio, filename, format, err := readAudioUpload(r)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "audio_too_large",
				fmt.Sprintf("Audio is too large; the limit is %d bytes.", providers.MaxAudioBytes))
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request_error", "", err.Error())
		return
	}

	// Content decides the type. The filename and the part's Content-Type are
	// both written by the caller, so neither is evidence of anything.
	if _, err := providers.SniffAudio(audio); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "unsupported_audio", err.Error())
		return
	}

	text, err := s.transcriber(r.Context(), audio, filename)
	if err != nil {
		// The provider's error carries the operator's upstream endpoint and can
		// carry their key; an API caller is authenticated but is not the
		// operator.
		writeUpstreamError(w, http.StatusBadGateway, "api_error", "", err.Error())
		return
	}

	if format == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, text)
		return
	}
	writeJSON(w, http.StatusOK, transcriptionResponse{Text: text})
}

// multipartOverheadBytes is the slack allowed for multipart boundaries, part
// headers and the other form fields on top of the audio itself. Without it a
// file of exactly MaxAudioBytes could not be uploaded at all, because its
// envelope pushes the request over.
const multipartOverheadBytes = 64 << 10

// readAudioUpload pulls the audio bytes and the sibling form fields out of a
// multipart body.
//
// It streams through r.MultipartReader rather than calling ParseMultipartForm,
// which spills anything over its memory budget to temp files on disk: an
// authenticated caller could otherwise fill the operator's disk with uploads
// that are never transcribed, and the audio would touch disk unredacted on its
// way to a provider.
func readAudioUpload(r *http.Request) (audio []byte, filename, format string, err error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, "", "", fmt.Errorf("expected a multipart/form-data body with a %q field: %w", audioFormField, err)
	}
	for {
		part, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			return nil, "", "", perr
		}
		switch part.FormName() {
		case audioFormField:
			if audio != nil {
				_ = part.Close()
				return nil, "", "", fmt.Errorf("more than one %q field", audioFormField)
			}
			// LimitReader at cap+1 so a file exactly at the cap is accepted and
			// one byte over is refused as over-limit rather than silently
			// truncated and transcribed as a corrupt file.
			data, rerr := io.ReadAll(io.LimitReader(part, providers.MaxAudioBytes+1))
			_ = part.Close()
			if rerr != nil {
				return nil, "", "", rerr
			}
			if len(data) > providers.MaxAudioBytes {
				return nil, "", "", &http.MaxBytesError{Limit: providers.MaxAudioBytes}
			}
			audio = data
			filename = part.FileName()
		case "response_format":
			v, rerr := readSmallPart(part)
			if rerr != nil {
				return nil, "", "", rerr
			}
			format = strings.ToLower(strings.TrimSpace(v))
		default:
			// model, language, prompt, temperature: accepted and ignored, for
			// the same drop-in reason /v1/chat/completions ignores `model`.
			// The transcription model comes from stt.model, not the caller.
			if _, rerr := readSmallPart(part); rerr != nil {
				return nil, "", "", rerr
			}
		}
	}
	if audio == nil {
		return nil, "", "", fmt.Errorf("missing %q field", audioFormField)
	}
	if len(audio) == 0 {
		return nil, "", "", fmt.Errorf("the %q field is empty", audioFormField)
	}
	if strings.TrimSpace(filename) == "" {
		// The provider keys format detection off the filename extension, so an
		// upload with none still needs one. The bytes are sniffed either way.
		filename = "audio.ogg"
	}
	return audio, filename, format, nil
}

// readSmallPart drains a non-file form field under a small cap, so a caller
// cannot stream gigabytes into a field joshbot only ignores.
func readSmallPart(part io.ReadCloser) (string, error) {
	defer func() { _ = part.Close() }()
	v, err := io.ReadAll(io.LimitReader(part, 4<<10))
	if err != nil {
		return "", err
	}
	return string(v), nil
}
