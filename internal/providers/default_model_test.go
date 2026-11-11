package providers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// retiredNIMModels lists NVIDIA NIM model IDs confirmed dead against
// https://integrate.api.nvidia.com/v1 — each answered HTTP 410 Gone ("the
// model has reached its end of life") on the date recorded beside it.
//
// This list is historical, not exhaustive: NVIDIA retires models without
// notice, so a default can rot without appearing here. That is what the live
// check below is for. This list exists to fail loudly and specifically when
// someone reinstates a default we already know is dead — which has happened
// twice — and to keep the failure legible instead of a bare "410 Gone" on a
// user's first message.
var retiredNIMModels = map[string]string{
	"moonshotai/kimi-k2-thinking":        "2026-08",
	"deepseek-ai/deepseek-v4-flash-0731": "2026-11",
}

// TestNVIDIADefaultModelIsNotRetired is the offline half of the guard on the
// NVIDIA default model. It runs in ordinary CI with no network and no key.
func TestNVIDIADefaultModelIsNotRetired(t *testing.T) {
	def := GetDefaultModel("nvidia")
	if def == "" {
		t.Fatal("nvidia has no default model; a fresh install would have nothing to fall back to")
	}
	if retiredOn, dead := retiredNIMModels[def]; dead {
		t.Errorf("the NVIDIA default model is %q, a model confirmed retired (HTTP 410 Gone) in %s; "+
			"a fresh nvidia install cannot make its first LLM call — pick a live model and verify it "+
			"answers a tool-calling request before changing it", def, retiredOn)
	}
}

// TestNVIDIADefaultModelIsLive is the live half of the guard: it asks
// NVIDIA's catalog whether the default model still exists, and asks the model
// itself to return a tool call. Both matter — a model can be in the catalog
// and still not support function calling, and a default that cannot call
// tools breaks every agent turn, which is worse than a model that is merely
// slow.
//
// It is opt-in because the free tier is rate-limited and ordinary CI must not
// depend on it. The scheduled workflow .github/workflows/live-eval.yml sets
// JOSHBOT_LIVE_MODEL_CHECK=1 and supplies NVIDIA_API_KEY.
func TestNVIDIADefaultModelIsLive(t *testing.T) {
	if os.Getenv("JOSHBOT_LIVE_MODEL_CHECK") != "1" {
		t.Skip("set JOSHBOT_LIVE_MODEL_CHECK=1 (with NVIDIA_API_KEY) to check the NVIDIA default against the live catalog")
	}
	key := os.Getenv("NVIDIA_API_KEY")
	if key == "" {
		t.Skip("NVIDIA_API_KEY is not set")
	}

	def := GetDefaultModel("nvidia")
	if def == "" {
		t.Fatal("nvidia has no default model")
	}
	base := GetDefaultAPIBaseFor("nvidia")
	if base == "" {
		t.Fatal("nvidia has no default API base")
	}
	client := &http.Client{Timeout: 60 * time.Second}

	t.Run("catalog", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, base+"/models", nil)
		if err != nil {
			t.Fatalf("build catalog request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("catalog request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("catalog returned %s", resp.Status)
		}
		var payload struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			t.Fatalf("decode catalog: %v", err)
		}
		for _, m := range payload.Data {
			if m.ID == def {
				return
			}
		}
		t.Errorf("the NVIDIA default model %q is not in the live catalog; a fresh install cannot make its first LLM call", def)
	})

	t.Run("tool_calling", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{
			"model": def,
			"messages": []map[string]string{
				{"role": "user", "content": "What is the weather in Paris? Use the tool."},
			},
			"tools": []map[string]any{{
				"type": "function",
				"function": map[string]any{
					"name":        "get_weather",
					"description": "Get the weather for a city",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"city": map[string]string{"type": "string"},
						},
						"required": []string{"city"},
					},
				},
			}},
			"max_tokens": 256,
		})
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		req, err := http.NewRequest(http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("build chat request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("chat request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("the NVIDIA default model %q answered %s; a fresh install cannot make its first LLM call", def, resp.Status)
		}
		var payload struct {
			Choices []struct {
				Message struct {
					ToolCalls []json.RawMessage `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			t.Fatalf("decode chat response: %v", err)
		}
		for _, choice := range payload.Choices {
			if len(choice.Message.ToolCalls) > 0 {
				return
			}
		}
		t.Errorf("the NVIDIA default model %q did not return a tool call; a default that cannot call tools "+
			"breaks every agent turn", def)
	})
}

// TestProviderDefaultsAreNotBlank guards the shape of the other defaults too:
// an empty default is not a rotted one, but it is the same class of "a fresh
// install has nothing to fall back to".
func TestProviderDefaultsAreNotBlank(t *testing.T) {
	for _, name := range []string{"openrouter", "nvidia", "groq", "ollama", "anthropic", "poolside"} {
		if GetDefaultModel(name) == "" {
			t.Errorf("provider %q has no default model", name)
		}
	}
}

// TestNVIDIADefaultModelNameShape keeps the default in the "nvidia/<model>"
// form the catalog uses. A bare model ID would 404 against the NVIDIA
// endpoint, which is the same first-call failure wearing a different hat.
func TestNVIDIADefaultModelNameShape(t *testing.T) {
	def := GetDefaultModel("nvidia")
	if !strings.HasPrefix(def, "nvidia/") {
		t.Errorf("the NVIDIA default model %q is not in the nvidia/<model> form the NVIDIA endpoint expects", def)
	}
}
