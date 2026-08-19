package api

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/providers"
)

func embedServer(t *testing.T, e Embedder) *Server {
	t.Helper()
	s, err := New(&fakeAgent{}, Options{Listen: "127.0.0.1:0", APIKeys: []string{"secret"}, Embedder: e})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func postEmbeddings(t *testing.T, s *Server, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}

// stubEmbedder returns one fixed-width vector per input and records what it was
// asked for, so a test can assert the route refused before dialling upstream.
func stubEmbedder(calls *[][]string) Embedder {
	return func(_ context.Context, inputs []string) ([][]float32, providers.Usage, error) {
		if calls != nil {
			*calls = append(*calls, inputs)
		}
		out := make([][]float32, len(inputs))
		for i := range inputs {
			out[i] = []float32{float32(i), 0.5, -1.5}
		}
		return out, providers.Usage{PromptTokens: 3, TotalTokens: 3}, nil
	}
}

// The route spends the operator's upstream credential on whatever text it is
// handed, so an unauthenticated caller is a billing primitive at minimum.
func TestEmbeddingsRequiresAuth(t *testing.T) {
	var calls [][]string
	s := embedServer(t, stubEmbedder(&calls))
	for name, key := range map[string]string{"no key": "", "wrong key": "nope"} {
		t.Run(name, func(t *testing.T) {
			w := postEmbeddings(t, s, key, `{"input":"hi"}`)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
			if len(calls) != 0 {
				t.Fatal("the embedder ran for an unauthenticated request")
			}
		})
	}
}

// The 501 names the config key. A 404 would read as "joshbot cannot do this",
// and a 200 with an empty data array is indistinguishable from a working
// provider that returned nothing.
func TestEmbeddingsUnconfiguredNamesTheConfigKey(t *testing.T) {
	s := testServer(t, &fakeAgent{})
	w := postEmbeddings(t, s, "secret", `{"input":"hi"}`)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", w.Code)
	}
	if !strings.Contains(w.Body.String(), "embeddings.provider") {
		t.Fatalf("body = %q, want it to name embeddings.provider", w.Body.String())
	}
}

// `input` is a bare string in some SDKs and an array in others. Both are real
// traffic; rejecting either breaks a drop-in client.
func TestEmbeddingsAcceptsStringAndArrayInput(t *testing.T) {
	cases := map[string]struct {
		body string
		want []string
	}{
		"bare string": {`{"input":"hello"}`, []string{"hello"}},
		"array":       {`{"input":["a","b"]}`, []string{"a", "b"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var calls [][]string
			s := embedServer(t, stubEmbedder(&calls))
			w := postEmbeddings(t, s, "secret", tc.body)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			if len(calls) != 1 || len(calls[0]) != len(tc.want) || calls[0][0] != tc.want[0] {
				t.Fatalf("embedder saw %v, want %v", calls, tc.want)
			}
			var resp struct {
				Object string `json:"object"`
				Data   []struct {
					Object    string    `json:"object"`
					Index     int       `json:"index"`
					Embedding []float32 `json:"embedding"`
				} `json:"data"`
				Usage usage `json:"usage"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Object != "list" || len(resp.Data) != len(tc.want) {
				t.Fatalf("response = %+v", resp)
			}
			for i, d := range resp.Data {
				if d.Index != i || d.Object != "embedding" || len(d.Embedding) != 3 {
					t.Fatalf("data[%d] = %+v", i, d)
				}
			}
			if resp.Usage.PromptTokens != 3 {
				t.Fatalf("usage = %+v, want the provider's token counts", resp.Usage)
			}
		})
	}
}

// The caps must be enforced BEFORE the upstream call: a limit that reports an
// error after the work has run — and has been billed — has not limited
// anything. So each case asserts the embedder never ran.
func TestEmbeddingsCapsRejectBeforeCallingUpstream(t *testing.T) {
	big := strings.Repeat("x", maxEmbeddingInputBytes+1)
	many := make([]string, maxEmbeddingInputs+1)
	for i := range many {
		many[i] = "x"
	}
	manyJSON, _ := json.Marshal(many)

	cases := map[string]struct {
		body string
		want string
	}{
		"too many inputs":      {fmt.Sprintf(`{"input":%s}`, manyJSON), "too many inputs"},
		"single input too big": {fmt.Sprintf(`{"input":%q}`, big), "too large"},
		"empty array":          {`{"input":[]}`, "empty"},
		"empty string":         {`{"input":""}`, "empty"},
		"empty inside array":   {`{"input":["a",""]}`, "empty"},
		"missing input":        {`{"model":"x"}`, "input"},
		"wrong input type":     {`{"input":{"a":1}}`, "string or an array"},
		"non-string array":     {`{"input":[1,2]}`, "string or an array"},
		"invalid json":         {`{`, "Invalid JSON"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var calls [][]string
			s := embedServer(t, stubEmbedder(&calls))
			w := postEmbeddings(t, s, "secret", tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
			if len(calls) != 0 {
				t.Fatalf("the embedder ran for a request that must be refused first: %v", calls)
			}
			if !strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("body = %q, want it to contain %q", w.Body.String(), tc.want)
			}
		})
	}
}

// A batch exactly at the cap is accepted. Off-by-one on a limit that rejects is
// as much a bug as no limit at all.
func TestEmbeddingsAcceptsExactlyTheCap(t *testing.T) {
	at := make([]string, maxEmbeddingInputs)
	for i := range at {
		at[i] = "x"
	}
	body, _ := json.Marshal(map[string]any{"input": at})
	var calls [][]string
	s := embedServer(t, stubEmbedder(&calls))
	w := postEmbeddings(t, s, "secret", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d for a batch exactly at the cap: %s", w.Code, w.Body.String())
	}
	if len(calls) != 1 || len(calls[0]) != maxEmbeddingInputs {
		t.Fatalf("embedder saw %d inputs", len(calls[0]))
	}
}

// base64 is the OpenAI SDK's other encoding_format: raw little-endian float32s.
// The byte order is part of the contract — numpy.frombuffer on the decoded
// bytes is what clients do with it — so it is decoded back here rather than
// compared to a golden string.
func TestEmbeddingsBase64EncodingRoundTrips(t *testing.T) {
	s := embedServer(t, stubEmbedder(nil))
	w := postEmbeddings(t, s, "secret", `{"input":"hi","encoding_format":"base64"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []struct {
			Embedding string `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode (base64 must serialize as a JSON string, not an array): %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(resp.Data[0].Embedding)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	if len(raw) != 12 {
		t.Fatalf("decoded %d bytes, want 12 for a 3-float vector", len(raw))
	}
	want := []float32{0, 0.5, -1.5}
	for i, v := range want {
		got := math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
		if got != v {
			t.Fatalf("float %d = %v, want %v (little-endian float32 is the contract)", i, got, v)
		}
	}
}

// An unsupported encoding_format is refused rather than silently answered with
// floats: a client asking for base64 and handed an array parses garbage.
func TestEmbeddingsRejectsUnknownEncodingFormat(t *testing.T) {
	var calls [][]string
	s := embedServer(t, stubEmbedder(&calls))
	w := postEmbeddings(t, s, "secret", `{"input":"hi","encoding_format":"int8"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if len(calls) != 0 {
		t.Fatal("the embedder ran for an unsupported encoding_format")
	}
}

// `model` is accepted and ignored, mirroring the chat route: a client hardcoded
// to an OpenAI model id must keep working against the configured model.
func TestEmbeddingsIgnoresRequestedModel(t *testing.T) {
	s := embedServer(t, stubEmbedder(nil))
	w := postEmbeddings(t, s, "secret", `{"model":"text-embedding-ada-002","input":"hi"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"model":"`+ModelID+`"`) {
		t.Fatalf("body = %q, want the server's own model id", w.Body.String())
	}
}

// An upstream error is redacted before it crosses the network: it carries the
// operator's endpoint and can carry their key, and an API caller is
// authenticated but is not the operator (#238).
func TestEmbeddingsRedactsUpstreamError(t *testing.T) {
	s := embedServer(t, func(context.Context, []string) ([][]float32, providers.Usage, error) {
		return nil, providers.Usage{}, errors.New(`upstream rejected the request: {"api_key": "sk-supersecretvalue"}`)
	})
	w := postEmbeddings(t, s, "secret", `{"input":"hi"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	if strings.Contains(w.Body.String(), "sk-supersecretvalue") {
		t.Fatalf("the upstream credential reached the caller: %s", w.Body.String())
	}
}

// A provider that answers with the wrong number of vectors must be a failure,
// not a 200 whose data array silently does not line up with the inputs.
func TestEmbeddingsRejectsVectorCountMismatch(t *testing.T) {
	s := embedServer(t, func(context.Context, []string) ([][]float32, providers.Usage, error) {
		return [][]float32{{1}}, providers.Usage{}, nil
	})
	w := postEmbeddings(t, s, "secret", `{"input":["a","b"]}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
}

func TestEmbeddingsRejectsNonPost(t *testing.T) {
	s := embedServer(t, stubEmbedder(nil))
	r := httptest.NewRequest(http.MethodGet, "/v1/embeddings", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

// The body stays under the shared JSON cap — this route is text, not an upload,
// so it gets no exemption the way the audio route needed one.
func TestEmbeddingsBodyIsCappedAtMaxRequestBytes(t *testing.T) {
	var calls [][]string
	s := embedServer(t, stubEmbedder(&calls))
	huge := strings.Repeat("x", MaxRequestBytes+1024)
	w := postEmbeddings(t, s, "secret", fmt.Sprintf(`{"input":%q}`, huge))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if len(calls) != 0 {
		t.Fatal("the embedder ran for an over-cap body")
	}
}
