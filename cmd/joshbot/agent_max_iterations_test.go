package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// --max-iterations is the only bound an operator has on a ReAct loop that will
// not converge. A model that keeps asking for a tool it cannot use will call
// the provider again on every pass, and every pass is billed. If the flag is
// dropped between the CLI and the agent, nothing says so: the run just costs
// more and takes longer. So the assertion has to be the count of provider
// requests, not the presence of the flag.

// loopingChatServer always answers with a tool call, so the ReAct loop never
// converges on its own. The tool does not exist; the agent gets an error result
// back and asks again, which is exactly the runaway shape the bound exists for.
type loopingChatServer struct {
	*httptest.Server
	mu sync.Mutex
	n  int
}

func (s *loopingChatServer) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func newLoopingChatServer(t *testing.T) *loopingChatServer {
	t.Helper()
	srv := &loopingChatServer{}
	srv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		srv.mu.Lock()
		srv.n++
		srv.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		out, _ := json.Marshal(map[string]any{
			"id":     "chatcmpl-loop",
			"object": "chat.completion",
			"model":  "test-model",
			"choices": []any{map[string]any{
				"index":         0,
				"finish_reason": "tool_calls",
				"message": map[string]any{
					"role":    "assistant",
					"content": "",
					"tool_calls": []any{map[string]any{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      "no_such_tool",
							"arguments": "{}",
						},
					}},
				},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAgentMaxIterationsBoundsAProviderLoopThatWillNotConverge(t *testing.T) {
	srv := newLoopingChatServer(t)
	cfg := agentEnv(t, srv.URL+"/v1")

	// The run itself may end in an error or a give-up reply; what is under
	// test is that it ended at all, and how expensively.
	runCLI(t, "--config", cfg, "agent", "--max-iterations", "2", "-m", "hi")

	bounded := srv.calls()
	if bounded == 0 {
		t.Fatal("the provider was never called; the loop under test never ran")
	}
	// The loop runs at most maxIter passes and then makes one final call to
	// turn the exhausted state into a reply, so three is the ceiling for two.
	if bounded > 3 {
		t.Fatalf("--max-iterations 2 still made %d provider calls; the override never reached the agent", bounded)
	}
}

// And the bound must be the value passed, not a constant: a hard-coded cap
// would pass the test above while ignoring the flag entirely.
func TestAgentMaxIterationsIsTheValuePassedNotAConstant(t *testing.T) {
	low := newLoopingChatServer(t)
	runCLI(t, "--config", agentEnv(t, low.URL+"/v1"), "agent", "--max-iterations", "1", "-m", "hi")

	high := newLoopingChatServer(t)
	runCLI(t, "--config", agentEnv(t, high.URL+"/v1"), "agent", "--max-iterations", "4", "-m", "hi")

	if low.calls() >= high.calls() {
		t.Errorf("--max-iterations 1 made %d calls and 4 made %d; the value is not being honoured",
			low.calls(), high.calls())
	}
}
