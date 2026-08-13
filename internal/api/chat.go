package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bigknoxy/joshbot/internal/agent"
	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/log"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/session"
)

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "", "Method not allowed; use POST.")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "",
			fmt.Sprintf("Could not parse request body: %v", err))
		return
	}

	prompt, err := lastUserMessage(req.Messages)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "", err.Error())
		return
	}

	sender, err := senderID(req.User)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "", err.Error())
		return
	}

	msg := bus.InboundMessage{
		SenderID:  sender,
		Content:   prompt,
		Channel:   ChannelName,
		Timestamp: time.Now(),
	}

	if req.Stream {
		s.streamCompletion(w, r, msg)
		return
	}
	s.completion(w, r, msg)
}

// lastUserMessage picks the turn joshbot will answer.
//
// Only the final user message is used. joshbot is a stateful assistant that
// keeps its own session, memory and skills per caller, so replaying a client's
// accumulated transcript would double the history rather than supply it. That is
// the agent-as-model bargain: the caller sends a turn, joshbot supplies the
// context.
//
// A client-supplied system message is deliberately dropped rather than merged.
// joshbot's system prompt carries its tool contract and safety framing, and this
// endpoint hands callers the shell and filesystem tools — letting a request
// rewrite that prompt would be an authenticated prompt-injection channel
// straight into tool execution.
func lastUserMessage(messages []chatMessage) (string, error) {
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.EqualFold(messages[i].Role, "user") {
			if content := strings.TrimSpace(messages[i].Content); content != "" {
				return content, nil
			}
			return "", fmt.Errorf("the last 'user' message has empty content")
		}
	}
	return "", fmt.Errorf("'messages' must contain at least one message with role 'user'")
}

// senderID validates the caller-supplied identity before it becomes half of a
// session key.
//
// The value is attacker-controlled and joshbot builds a session file path from
// the session key, so it goes through the same validation every other
// path-building entry point uses. A colon is refused on top of that: the session
// key is "channel:senderID", so a sender containing one could name a session
// belonging to another channel.
func senderID(user string) (string, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return DefaultUser, nil
	}
	if strings.Contains(user, ":") {
		return "", fmt.Errorf("'user' must not contain ':'")
	}
	if err := session.ValidateSessionID(user); err != nil {
		return "", fmt.Errorf("'user' is not a valid identifier: %w", err)
	}
	// ValidateSessionID stops traversal but says nothing about length or shape,
	// and this value becomes a filename: "<channel>:<user>.jsonl". Anything past
	// NAME_MAX (255 on APFS and ext4) makes every turn for that caller fail to
	// persist, with the failure surfacing as a session error rather than as the
	// bad request it is. The charset is narrowed for a second reason: a name
	// carrying a newline or an RTL override renders deceptively in
	// `joshbot sessions`, where an operator reads it to decide what to prune.
	if len(user) > MaxUserLength {
		return "", fmt.Errorf("'user' must be at most %d characters", MaxUserLength)
	}
	for _, r := range user {
		if !isUserRune(r) {
			return "", fmt.Errorf("'user' may contain only letters, digits, '.', '_' and '-'")
		}
	}
	return user, nil
}

// MaxUserLength bounds the caller-supplied identity. It is well under NAME_MAX
// once the channel prefix and the ".jsonl" suffix are added.
const MaxUserLength = 64

func isUserRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '.' || r == '_' || r == '-':
		return true
	}
	return false
}

// completion answers a non-streaming request.
func (s *Server) completion(w http.ResponseWriter, r *http.Request, msg bus.InboundMessage) {
	var (
		mu    sync.Mutex
		total providers.Usage
	)
	ctx := agent.WithUsageSink(r.Context(), func(u providers.Usage) {
		// The sink fires once per provider call, and one turn makes several
		// when tools are involved, so the totals accumulate rather than
		// overwrite. The lock is defensive rather than currently load-bearing:
		// today Process calls the sink inline on this handler's own goroutine,
		// but nothing in the sink contract promises that, and a provider that
		// later reports usage from a reader goroutine would otherwise race.
		mu.Lock()
		defer mu.Unlock()
		total.PromptTokens += u.PromptTokens
		total.CompletionTokens += u.CompletionTokens
		total.TotalTokens += u.TotalTokens
	})

	response, err := s.agent.Process(ctx, msg)
	if err == nil {
		// Process reports LLM failures as reply text with a nil error, because
		// a chat channel has to show the user something. Answering 200 with
		// that string would put a provider outage in the assistant's mouth and
		// give the caller no way to tell it from a real answer.
		err = agent.ReplyError(response)
	}
	if err != nil {
		log.Warn("API completion failed", "error", err)
		writeError(w, http.StatusBadGateway, "api_error", "", err.Error())
		return
	}

	mu.Lock()
	u := usageFrom(total)
	mu.Unlock()

	writeJSON(w, http.StatusOK, chatResponse{
		ID:      newID(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   ModelID,
		Choices: []chatChoice{{
			Index:        0,
			Message:      chatMessage{Role: "assistant", Content: response},
			FinishReason: "stop",
		}},
		Usage: u,
	})
}

// streamCompletion answers a streaming request as server-sent events.
func (s *Server) streamCompletion(w http.ResponseWriter, r *http.Request, msg bus.InboundMessage) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "api_error", "", "Streaming is not supported by this server.")
		return
	}

	id := newID()
	created := time.Now().Unix()

	var (
		mu      sync.Mutex
		total   providers.Usage
		sent    strings.Builder
		started bool
	)

	// send writes one SSE frame. Callers hold mu: the stream sink and the final
	// frame both write to the same ResponseWriter, and today both run on this
	// handler's goroutine, so the lock is a guard against a future sink that
	// does not — not a race being fixed here. http.ResponseWriter is not safe
	// for concurrent use, so the guard is cheap next to what it prevents.
	send := func(c chunk) {
		payload, err := json.Marshal(c)
		if err != nil {
			log.Debug("API stream frame marshal failed", "error", err)
			return
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			log.Debug("API stream write failed", "error", err)
			return
		}
		flusher.Flush()
	}

	frame := func(d delta, finish string) chunk {
		return chunk{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   ModelID,
			Choices: []chunkChoice{{Index: 0, Delta: d, FinishReason: finish}},
		}
	}

	ctx := agent.WithUsageSink(r.Context(), func(u providers.Usage) {
		mu.Lock()
		defer mu.Unlock()
		total.PromptTokens += u.PromptTokens
		total.CompletionTokens += u.CompletionTokens
		total.TotalTokens += u.TotalTokens
	})
	ctx = agent.WithStreamSink(ctx, func(ev agent.StreamEvent) {
		if ev.Delta == "" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if !started {
			// The headers go out on the first delta, not before, so a failure
			// that happens before any text can still be reported as an HTTP
			// status the client will surface as an error.
			writeSSEHeaders(w)
			send(frame(delta{Role: "assistant"}, ""))
			started = true
		}
		sent.WriteString(ev.Delta)
		send(frame(delta{Content: ev.Delta}, ""))
	})

	response, err := s.agent.Process(ctx, msg)
	if err == nil {
		err = agent.ReplyError(response)
	}

	mu.Lock()
	defer mu.Unlock()

	if err != nil {
		log.Warn("API stream failed", "error", err)
		if !started {
			// Nothing has been written, so a real status code is still
			// possible and is far more useful to a client than a 200 whose
			// body happens to contain an error.
			writeError(w, http.StatusBadGateway, "api_error", "", err.Error())
			return
		}
		// Mid-stream the status is already 200. OpenAI clients surface an
		// error object embedded in the stream, so report it there and stop
		// rather than closing the connection silently on a partial answer.
		payload, mErr := json.Marshal(errorEnvelope{Error: errorBody{Message: err.Error(), Type: "api_error"}})
		if mErr == nil {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}

	if !started {
		// A turn can finish without the stream sink firing — a provider with
		// no streaming endpoint falls back to Chat, and a slash command never
		// reaches a provider at all. Emitting the whole answer here is what
		// keeps those turns from returning an empty stream.
		writeSSEHeaders(w)
		send(frame(delta{Role: "assistant"}, ""))
		if response != "" {
			send(frame(delta{Content: response}, ""))
		}
	} else if streamed := sent.String(); streamed != response {
		// The sink carries the model's raw deltas, but Process returns the
		// finished reply, which may differ — a final tool round adds text the
		// stream never saw. The comparison is on content, not byte count: the
		// two are only related when the stream is a genuine prefix, and slicing
		// the response by the number of streamed bytes would cut mid-character
		// or mid-word whenever it is not.
		if remainder, ok := strings.CutPrefix(response, streamed); ok {
			if remainder != "" {
				send(frame(delta{Content: remainder}, ""))
			}
		}
		// When the stream is not a prefix of the final reply, nothing more is
		// sent: the deltas already on screen are the model's own words, and
		// replaying the whole reply after them would show the answer twice.
	}

	u := usageFrom(total)
	final := frame(delta{}, "stop")
	final.Usage = &u
	send(final)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func writeSSEHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// Proxies that buffer would defeat streaming entirely.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}
