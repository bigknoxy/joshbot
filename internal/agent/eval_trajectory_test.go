package agent

import (
	"errors"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/session"
)

// Behavioural evals for the ReAct loop. Each drives the real loop through a
// scripted model and asserts on the trajectory — what the loop sent, what it
// dispatched, what it persisted — rather than on the final string.
//
// Every eval ends with assertProtocolInvariants so that the wire-format rules
// are enforced on every scenario, present and future.

func TestEval_SingleToolRoundTrip(t *testing.T) {
	res := runTrajectoryEval(t, trajectoryScenario{
		name:        "single tool round trip",
		userMessage: "list the files",
		turns: []scriptedTurn{
			{toolCalls: []providers.ToolCall{toolCall("call_a", "shell", `{"command":"ls"}`)}},
			{content: "There are three files."},
		},
		toolOutputs: map[string]string{"shell": "a.go b.go c.go"},
	})
	assertProtocolInvariants(t, res)

	if res.err != nil {
		t.Fatalf("Process returned %v", res.err)
	}
	if len(res.requests) != 2 {
		t.Fatalf("expected 2 model calls, got %d", len(res.requests))
	}

	// The tool must actually be dispatched, with the arguments parsed out of
	// the model's JSON rather than passed through raw.
	inv := res.invocations
	if len(inv) != 1 {
		t.Fatalf("expected 1 tool invocation, got %d", len(inv))
	}
	if inv[0].name != "shell" {
		t.Errorf("tool name = %q, want shell", inv[0].name)
	}
	if got := inv[0].args["command"]; got != "ls" {
		t.Errorf("command arg = %v, want ls", got)
	}

	// The whole point of the loop: the tool's output has to reach the model on
	// the next turn. If this regresses the agent still replies, just blindly.
	second := res.requestMessages(1)
	result, ok := toolResultFor(second, "call_a")
	if !ok {
		t.Fatal("second model call carried no result for call_a")
	}
	if !strings.Contains(result, "a.go b.go c.go") {
		t.Errorf("tool output did not reach the model; got %q", result)
	}

	if res.final != "There are three files." {
		t.Errorf("final = %q", res.final)
	}
}

func TestEval_ParallelToolCallsAllAnswered(t *testing.T) {
	res := runTrajectoryEval(t, trajectoryScenario{
		name: "two tool calls in one turn",
		turns: []scriptedTurn{
			{toolCalls: []providers.ToolCall{
				toolCall("call_1", "shell", `{"command":"date"}`),
				toolCall("call_2", "filesystem", `{"path":"/tmp"}`),
			}},
			{content: "done"},
		},
		toolOutputs: map[string]string{"shell": "Tuesday", "filesystem": "empty"},
	})
	assertProtocolInvariants(t, res)

	if len(res.invocations) != 2 {
		t.Fatalf("expected both tools to be dispatched, got %d", len(res.invocations))
	}

	second := res.requestMessages(1)
	for _, id := range []string{"call_1", "call_2"} {
		if _, ok := toolResultFor(second, id); !ok {
			t.Errorf("no result returned for %s", id)
		}
	}
	if n := countRole(second, providers.RoleTool); n != 2 {
		t.Errorf("expected 2 tool messages in the follow-up request, got %d", n)
	}
}

func TestEval_MalformedToolArgumentsAreReportedNotDispatched(t *testing.T) {
	res := runTrajectoryEval(t, trajectoryScenario{
		name: "model emits invalid JSON arguments",
		turns: []scriptedTurn{
			{toolCalls: []providers.ToolCall{toolCall("call_bad", "shell", `{"command": `)}},
			{content: "I could not parse that."},
		},
	})
	assertProtocolInvariants(t, res)

	if res.err != nil {
		t.Fatalf("malformed arguments should not fail the run, got %v", res.err)
	}
	// A tool must not run on arguments that did not parse.
	if len(res.invocations) != 0 {
		t.Errorf("expected no tool dispatch, got %v", res.invocations)
	}

	// The model has to be told, otherwise it retries the same broken call.
	second := res.requestMessages(1)
	result, ok := toolResultFor(second, "call_bad")
	if !ok {
		t.Fatal("no tool message answered the malformed call")
	}
	if !strings.Contains(strings.ToLower(result), "invalid arguments") {
		t.Errorf("model was not told the arguments were invalid; got %q", result)
	}
}

func TestEval_ToolErrorReachesModel(t *testing.T) {
	res := runTrajectoryEval(t, trajectoryScenario{
		name: "tool returns an error",
		turns: []scriptedTurn{
			{toolCalls: []providers.ToolCall{toolCall("call_e", "shell", `{"command":"rm -rf /"}`)}},
			{content: "That was blocked."},
		},
		toolErrors: map[string]error{"shell": errors.New("command denied: dangerous pattern")},
	})
	assertProtocolInvariants(t, res)

	second := res.requestMessages(1)
	result, ok := toolResultFor(second, "call_e")
	if !ok {
		t.Fatal("no tool message answered the failing call")
	}
	// The reason has to survive, not be flattened to a generic failure. A
	// denied command the model cannot see the reason for gets retried forever.
	if !strings.Contains(result, "command denied") {
		t.Errorf("error detail lost on the way to the model; got %q", result)
	}
}

// This is the regression guard for the defect this harness was built to find:
// once history exceeded the token budget, observation masking rebuilt messages
// without ToolCalls or ToolCallID, so the loop sent tool messages that answered
// nothing. Providers reject that with a 400, meaning long conversations broke
// while short ones stayed fine.
func TestEval_ContextMaskingPreservesToolLinkage(t *testing.T) {
	history := chatHistory(1, 400)
	history = append(history, toolExchange("call_old", "shell", "file listing "+padding(400))...)
	history = append(history, chatHistory(8, 400)...)

	res := runTrajectoryEval(t, trajectoryScenario{
		name:        "long history forces observation masking",
		history:     history,
		tightBudget: true,
		turns:       []scriptedTurn{{content: "ok"}},
	})
	assertProtocolInvariants(t, res)

	first := res.requestMessages(0)
	if len(first) == 0 {
		t.Fatal("no request recorded")
	}
	// Masking must actually have run, or this eval proves nothing.
	if countRole(first, providers.RoleTool) == 0 {
		t.Fatal("expected the seeded tool exchange to survive into the request")
	}
	var masked bool
	for _, m := range first {
		if strings.Contains(m.Content, "[Tool output truncated]") {
			masked = true
		}
	}
	if !masked {
		t.Fatal("observation masking did not run; this scenario no longer exercises the path it guards")
	}
}

// The memory window slices history to a fixed length. A cut landing between an
// assistant tool call and its result leaves the result orphaned at the head of
// the conversation, which is the same 400 by a different route.
func TestEval_MemoryWindowDoesNotOrphanToolResults(t *testing.T) {
	history := []session.Message{
		{Role: session.RoleUser, Content: "run it"},
	}
	history = append(history, toolExchange("call_w", "shell", "output")...)

	res := runTrajectoryEval(t, trajectoryScenario{
		name:         "memory window cuts between a call and its result",
		history:      history,
		memoryWindow: 2,
		turns:        []scriptedTurn{{content: "ok"}},
	})
	assertProtocolInvariants(t, res)

	first := res.requestMessages(0)
	if len(first) > 4 {
		t.Errorf("memory window did not apply; %d messages sent", len(first))
	}
}

func TestEval_MaxIterationsBoundsModelCalls(t *testing.T) {
	// A model that calls a tool forever must be stopped by the iteration cap.
	turns := make([]scriptedTurn, 10)
	for i := range turns {
		turns[i] = scriptedTurn{toolCalls: []providers.ToolCall{toolCall("call_loop", "shell", `{"command":"true"}`)}}
	}

	res := runTrajectoryEval(t, trajectoryScenario{
		name:          "runaway tool loop",
		turns:         turns,
		maxIterations: 3,
	})
	assertProtocolInvariants(t, res)

	if len(res.requests) != 3 {
		t.Errorf("expected exactly 3 model calls under a cap of 3, got %d", len(res.requests))
	}
	if len(res.invocations) != 3 {
		t.Errorf("expected 3 tool dispatches, got %d", len(res.invocations))
	}
	if res.final == "" {
		t.Error("hitting the cap should still return something to the user")
	}
}

func TestEval_SessionRecordsFullTrajectory(t *testing.T) {
	res := runTrajectoryEval(t, trajectoryScenario{
		name:        "trajectory is persisted",
		userMessage: "check the time",
		turns: []scriptedTurn{
			{toolCalls: []providers.ToolCall{toolCall("call_s", "shell", `{"command":"date"}`)}},
			{content: "It is Tuesday."},
		},
		toolOutputs: map[string]string{"shell": "Tuesday"},
	})
	assertProtocolInvariants(t, res)

	// The session is what a restart reloads. If the tool linkage is not
	// persisted, the next turn rebuilds a malformed conversation from disk.
	var sawCall, sawResult bool
	for _, m := range res.sess.Messages {
		for _, tc := range m.ToolCalls {
			if tc.ID == "call_s" && tc.Name == "shell" {
				sawCall = true
			}
		}
		if m.Role == session.RoleTool && m.ToolCallID == "call_s" {
			sawResult = true
			if !strings.Contains(m.Content, "Tuesday") {
				t.Errorf("persisted tool result = %q", m.Content)
			}
		}
	}
	if !sawCall {
		t.Error("session did not record the assistant tool call with its id")
	}
	if !sawResult {
		t.Error("session did not record the tool result against its call id")
	}
}

func TestEval_ProviderFailureAndEmptyResponse(t *testing.T) {
	// Process deliberately turns a loop failure into a user-facing string with a
	// nil error, so the channel always has something to print. The contract the
	// eval guards is that the cause is not swallowed: a silent success-looking
	// reply on a provider outage is the regression to catch.
	t.Run("provider error is reported to the user", func(t *testing.T) {
		res := runTrajectoryEval(t, trajectoryScenario{
			turns: []scriptedTurn{{err: errors.New("upstream 503")}},
		})
		assertProtocolInvariants(t, res)
		if res.err != nil {
			t.Fatalf("Process should absorb the error, got %v", res.err)
		}
		if !strings.Contains(res.final, "upstream 503") {
			t.Errorf("the failure cause never reached the user; reply was %q", res.final)
		}
	})

	t.Run("empty choices yields a usable reply", func(t *testing.T) {
		res := runTrajectoryEval(t, trajectoryScenario{
			turns: []scriptedTurn{{noChoices: true}},
		})
		assertProtocolInvariants(t, res)
		if res.err != nil {
			t.Fatalf("an empty response should not be an error, got %v", res.err)
		}
		if strings.TrimSpace(res.final) == "" {
			t.Error("expected a fallback reply rather than an empty string")
		}
	})
}
