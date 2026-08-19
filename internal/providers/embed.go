package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultEmbedTimeout bounds one embedding request when the config does not set
// one. Embedding a batch of short texts is fast even locally; a minute covers a
// cold ollama model load without pinning a request forever.
const DefaultEmbedTimeout = 60 * time.Second

// EmbedConfig is everything one embedding request needs. Like TranscribeConfig
// it is built by the caller from an already-resolved provider config, so the
// credential and endpoint come from the same place a chat request's would and
// there is no second credential store to keep in sync.
type EmbedConfig struct {
	// APIBase is the provider's OpenAI-compatible base URL. Empty is refused,
	// never defaulted: dialling a default endpoint would send the operator's
	// text (and credential) to a provider they did not choose — the same rule
	// ListModels, ProbeCredential and TranscribeAudio enforce.
	APIBase string
	// APIKey may be empty: ollama is keyless and is the main local use case.
	APIKey string
	// Model is the embedding model id (e.g. nomic-embed-text).
	Model   string
	Timeout time.Duration
	// HTTPClient overrides the client, for tests. Nil uses a default.
	HTTPClient *http.Client
}

// embedResponse is the OpenAI embeddings response shape.
type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// Embed sends inputs to the provider's OpenAI-compatible POST /embeddings
// endpoint and returns one vector per input, in input order.
//
// The returned slice is ordered by the response's `index` field, not by array
// position. The OpenAI schema carries an index precisely because a provider may
// return the objects out of order, and a vector silently attached to the wrong
// input is a correctness bug no caller can detect: the wrong text is retrieved
// and everything still looks like it worked.
func Embed(ctx context.Context, cfg EmbedConfig, inputs []string) ([][]float32, Usage, error) {
	if strings.TrimSpace(cfg.APIBase) == "" {
		return nil, Usage{}, fmt.Errorf("no API base URL configured for embeddings")
	}
	if cfg.Model == "" {
		return nil, Usage{}, fmt.Errorf("no embedding model configured")
	}
	if len(inputs) == 0 {
		return nil, Usage{}, fmt.Errorf("no input to embed")
	}

	body, err := json.Marshal(map[string]any{"model": cfg.Model, "input": inputs})
	if err != nil {
		return nil, Usage{}, fmt.Errorf("build embeddings request: %w", err)
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultEmbedTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	url := strings.TrimRight(cfg.APIBase, "/") + "/embeddings"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, Usage{}, fmt.Errorf("build embeddings request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, Usage{}, fmt.Errorf("embeddings request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// The error body is bounded before it can reach a log line or an HTTP reply.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, Usage{}, fmt.Errorf("read embeddings response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := strings.TrimSpace(string(data))
		if len(snippet) > 300 {
			snippet = snippet[:300] + "..."
		}
		return nil, Usage{}, fmt.Errorf("embeddings failed: %s: %s", resp.Status, snippet)
	}

	var parsed embedResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, Usage{}, fmt.Errorf("parse embeddings response: %w", err)
	}
	if len(parsed.Data) != len(inputs) {
		return nil, Usage{}, fmt.Errorf("embeddings response has %d vectors for %d inputs", len(parsed.Data), len(inputs))
	}

	out := make([][]float32, len(inputs))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(out) {
			return nil, Usage{}, fmt.Errorf("embeddings response has out-of-range index %d", d.Index)
		}
		if out[d.Index] != nil {
			return nil, Usage{}, fmt.Errorf("embeddings response repeats index %d", d.Index)
		}
		if len(d.Embedding) == 0 {
			return nil, Usage{}, fmt.Errorf("embeddings response has an empty vector at index %d", d.Index)
		}
		out[d.Index] = d.Embedding
	}
	// Every slot is filled: the count matched, indexes are in range and no index
	// repeated, so a nil here is impossible. Checked anyway rather than reasoned
	// about, because the failure it guards is a nil vector handed to a caller.
	for i, v := range out {
		if v == nil {
			return nil, Usage{}, fmt.Errorf("embeddings response is missing index %d", i)
		}
	}

	return out, Usage{
		PromptTokens: parsed.Usage.PromptTokens,
		TotalTokens:  parsed.Usage.TotalTokens,
	}, nil
}
