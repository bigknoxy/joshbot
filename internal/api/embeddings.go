package api

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
)

// maxEmbeddingInputs caps how many texts one request may embed. The cap exists
// because the upstream call is billed and slow in proportion to the batch, and
// because a limit that is checked after the work has run has not limited
// anything — so it is enforced before the provider is dialled.
const maxEmbeddingInputs = 128

// maxEmbeddingInputBytes caps one input. The whole body is already bounded by
// MaxRequestBytes; this stops a single 1 MiB blob being forwarded to the
// operator's provider as one enormous billed request.
const maxEmbeddingInputBytes = 64 << 10

// handleEmbeddings serves POST /v1/embeddings.
//
// Like /v1/audio/transcriptions and unlike /v1/chat/completions this is not the
// agent: no ReAct loop, no session, no memory. It is a thin authenticated front
// for the embedding provider the operator configured under `embeddings`.
func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "", "Method not allowed; use POST.")
		return
	}
	// Nil rather than a stub, so the 501 says which config key turns it on. A
	// 404 would read as "joshbot cannot do this" when in fact it is one config
	// key away.
	if s.embedder == nil {
		writeError(w, http.StatusNotImplemented, "invalid_request_error", "embeddings_not_configured",
			"Embeddings are not configured. Set embeddings.provider (and embeddings.model) in config.json.")
		return
	}

	// No exemption from MaxRequestBytes: an embeddings request is text, like a
	// chat turn, not an upload.
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
	var req embeddingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "", "Invalid JSON body.")
		return
	}
	// req.Model is accepted and ignored, mirroring the chat route's
	// agent-as-model rule: the configured embeddings.model is what is used, so a
	// client hardcoded to text-embedding-ada-002 keeps working.

	inputs, err := parseEmbeddingInput(req.Input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "", err.Error())
		return
	}

	format := strings.ToLower(strings.TrimSpace(req.EncodingFormat))
	if format == "" {
		format = "float"
	}
	if format != "float" && format != "base64" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "",
			fmt.Sprintf("Unsupported encoding_format %q; use \"float\" or \"base64\".", req.EncodingFormat))
		return
	}

	vectors, u, err := s.embedder(r.Context(), inputs)
	if err != nil {
		// The provider's error carries the operator's upstream endpoint and can
		// carry their key; an API caller is authenticated but is not the
		// operator.
		writeUpstreamError(w, http.StatusBadGateway, "api_error", "", err.Error())
		return
	}
	if len(vectors) != len(inputs) {
		writeError(w, http.StatusBadGateway, "api_error", "",
			"The embedding provider returned the wrong number of vectors.")
		return
	}

	resp := embeddingsResponse{Object: "list", Model: ModelID, Usage: usageFrom(u)}
	resp.Data = make([]embeddingObject, len(vectors))
	for i, v := range vectors {
		obj := embeddingObject{Object: "embedding", Index: i}
		if format == "base64" {
			obj.Embedding = encodeFloat32Base64(v)
		} else {
			obj.Embedding = v
		}
		resp.Data[i] = obj
	}
	writeJSON(w, http.StatusOK, resp)
}

// parseEmbeddingInput reads OpenAI's `input`, which is a bare string or an
// array of strings depending on the SDK. Both shapes are real traffic, so both
// are accepted; the caps are enforced here, before any upstream call.
func parseEmbeddingInput(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("missing \"input\" field")
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return validateEmbeddingInputs([]string{one})
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, fmt.Errorf("\"input\" must be a string or an array of strings")
	}
	return validateEmbeddingInputs(many)
}

func validateEmbeddingInputs(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("\"input\" is empty")
	}
	if len(in) > maxEmbeddingInputs {
		return nil, fmt.Errorf("too many inputs: %d, the limit is %d", len(in), maxEmbeddingInputs)
	}
	for i, s := range in {
		if s == "" {
			return nil, fmt.Errorf("input %d is empty", i)
		}
		if len(s) > maxEmbeddingInputBytes {
			return nil, fmt.Errorf("input %d is too large: %d bytes, the limit is %d", i, len(s), maxEmbeddingInputBytes)
		}
	}
	return in, nil
}

// encodeFloat32Base64 renders a vector the way OpenAI's base64 encoding_format
// does: the raw float32s, little-endian, base64'd. numpy.frombuffer(dtype
// float32) on the decoded bytes is what every client does with it, so the byte
// order is part of the contract and not an implementation detail.
func encodeFloat32Base64(v []float32) string {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return base64.StdEncoding.EncodeToString(b)
}
