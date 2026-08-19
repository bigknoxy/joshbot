package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEmbedOrdersByResponseIndex is the assertion this file exists for.
//
// The OpenAI schema carries an `index` on every embedding object precisely
// because the array may come back in any order, and a vector attached to the
// wrong input is invisible: the caller stores it, retrieves the wrong text and
// nothing ever errors. So the server here answers deliberately out of order and
// the test pins that Embed places each vector by its index, not by its position.
func TestEmbedOrdersByResponseIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reversed on purpose.
		_, _ = io.WriteString(w, `{"data":[
			{"index":2,"embedding":[3,3]},
			{"index":0,"embedding":[1,1]},
			{"index":1,"embedding":[2,2]}
		],"usage":{"prompt_tokens":9,"total_tokens":9}}`)
	}))
	defer srv.Close()

	got, u, err := Embed(context.Background(), EmbedConfig{APIBase: srv.URL, Model: "m"}, []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	want := [][]float32{{1, 1}, {2, 2}, {3, 3}}
	for i := range want {
		if len(got[i]) != 2 || got[i][0] != want[i][0] {
			t.Fatalf("vector %d = %v, want %v (response order was not honoured by index)", i, got[i], want[i])
		}
	}
	if u.PromptTokens != 9 || u.TotalTokens != 9 {
		t.Fatalf("usage = %+v, want prompt/total 9", u)
	}
}

// A malformed index must fail loudly rather than panic or silently drop a
// vector. Each of these is a shape a proxy or a non-conforming gateway can
// produce, and every one of them would otherwise mismatch text to vector.
func TestEmbedRejectsMalformedIndexes(t *testing.T) {
	cases := map[string]string{
		"index out of range": `{"data":[{"index":0,"embedding":[1]},{"index":7,"embedding":[2]}]}`,
		"repeated index":     `{"data":[{"index":0,"embedding":[1]},{"index":0,"embedding":[2]}]}`,
		"negative index":     `{"data":[{"index":-1,"embedding":[1]},{"index":1,"embedding":[2]}]}`,
		"too few vectors":    `{"data":[{"index":0,"embedding":[1]}]}`,
		"empty vector":       `{"data":[{"index":0,"embedding":[]},{"index":1,"embedding":[2]}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			defer srv.Close()
			if _, _, err := Embed(context.Background(), EmbedConfig{APIBase: srv.URL, Model: "m"}, []string{"a", "b"}); err == nil {
				t.Fatal("Embed accepted a malformed response; a mismatched vector is undetectable downstream")
			}
		})
	}
}

// An empty APIBase is refused, never defaulted: defaulting one would send the
// operator's text and credential to a provider they did not choose. Same rule
// as ListModels, ProbeCredential and TranscribeAudio.
func TestEmbedRefusesEmptyConfig(t *testing.T) {
	cases := []struct {
		name   string
		cfg    EmbedConfig
		inputs []string
		want   string
	}{
		{"no api base", EmbedConfig{Model: "m"}, []string{"a"}, "no API base URL"},
		{"blank api base", EmbedConfig{APIBase: "   ", Model: "m"}, []string{"a"}, "no API base URL"},
		{"no model", EmbedConfig{APIBase: "http://x"}, []string{"a"}, "no embedding model"},
		{"no inputs", EmbedConfig{APIBase: "http://x", Model: "m"}, nil, "no input"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Embed(context.Background(), tc.cfg, tc.inputs)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// The request must carry the model, the inputs as an array, and the bearer
// credential only when there is one — ollama is keyless and an empty bearer
// header is rejected by some gateways.
func TestEmbedRequestShape(t *testing.T) {
	for _, key := range []string{"", "k"} {
		var gotAuth string
		var body map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			_ = json.NewDecoder(r.Body).Decode(&body)
			if !strings.HasSuffix(r.URL.Path, "/embeddings") {
				t.Errorf("path = %q, want it to end in /embeddings", r.URL.Path)
			}
			_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[1]}]}`)
		}))
		// Trailing slash on the base must not double up in the URL.
		if _, _, err := Embed(context.Background(), EmbedConfig{APIBase: srv.URL + "/", Model: "m", APIKey: key}, []string{"a"}); err != nil {
			t.Fatalf("Embed: %v", err)
		}
		srv.Close()
		if body["model"] != "m" {
			t.Fatalf("model = %v", body["model"])
		}
		if _, ok := body["input"].([]any); !ok {
			t.Fatalf("input = %#v, want an array", body["input"])
		}
		if key == "" && gotAuth != "" {
			t.Fatalf("Authorization = %q for a keyless provider, want none", gotAuth)
		}
		if key != "" && gotAuth != "Bearer k" {
			t.Fatalf("Authorization = %q", gotAuth)
		}
	}
}

// A non-200 must surface the upstream status and a bounded snippet: unbounded,
// a hostile or broken endpoint writes its whole body into a log line or an HTTP
// reply.
func TestEmbedBoundsErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, strings.Repeat("x", 5000))
	}))
	defer srv.Close()
	_, _, err := Embed(context.Background(), EmbedConfig{APIBase: srv.URL, Model: "m"}, []string{"a"})
	if err == nil {
		t.Fatal("Embed accepted a 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v, want the upstream status", err)
	}
	if len(err.Error()) > 500 {
		t.Fatalf("error is %d bytes; the snippet is not bounded", len(err.Error()))
	}
}

func TestEmbedRejectsUnparseableBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "not json")
	}))
	defer srv.Close()
	if _, _, err := Embed(context.Background(), EmbedConfig{APIBase: srv.URL, Model: "m"}, []string{"a"}); err == nil {
		t.Fatal("Embed accepted a non-JSON body")
	}
}
