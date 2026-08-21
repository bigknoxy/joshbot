package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/redact"
)

// Logger is a simple logger interface for providers.
type Logger interface {
	Debug(msg string, args ...interface{})
	Info(msg string, args ...interface{})
	Warn(msg string, args ...interface{})
	Error(msg string, args ...interface{})
}

// DefaultLogger is a no-op logger.
type DefaultLogger struct{}

func (d *DefaultLogger) Debug(msg string, args ...interface{}) {}
func (d *DefaultLogger) Info(msg string, args ...interface{})  {}
func (d *DefaultLogger) Warn(msg string, args ...interface{})  {}
func (d *DefaultLogger) Error(msg string, args ...interface{}) {}

// LiteLLMProvider implements the Provider interface using LiteLLM proxy.
type LiteLLMProvider struct {
	cfg    Config
	client *http.Client
	logger Logger
	apiKey atomic.Value // stores string, thread-safe API key access
}

// NewLiteLLMProvider creates a new LiteLLM provider with the given configuration.
func NewLiteLLMProvider(cfg Config) *LiteLLMProvider {
	return NewLiteLLMProviderWithLogger(cfg, &DefaultLogger{})
}

// NewLiteLLMProviderWithLogger creates a new LiteLLM provider with a custom logger.
func NewLiteLLMProviderWithLogger(cfg Config, logger Logger) *LiteLLMProvider {
	if cfg.Timeout == 0 {
		cfg.Timeout = 120 * time.Second
	}

	p := &LiteLLMProvider{
		client: &http.Client{
			Timeout: cfg.Timeout,
		},
		logger: logger,
	}
	p.apiKey.Store(cfg.APIKey)
	p.cfg = cfg
	p.cfg.APIKey = "" // clear so readers use atomic only
	return p
}

// NewProviderFromResolvedModel creates a provider from a resolved model config.
func NewProviderFromResolvedModel(resolved config.ResolvedModelConfig, logger Logger) *LiteLLMProvider {
	maxTokens := resolved.MaxTokens
	if maxTokens <= 0 {
		maxTokens = config.DefaultMaxTokens
	}

	cfg := Config{
		APIKey:             resolved.APIKey,
		APIBase:            resolved.APIBase,
		Model:              resolved.ModelID,
		MaxTokens:          maxTokens,
		ExtraHeaders:       resolved.Extra,
		ExtraBody:          resolved.ExtraBody,
		Timeout:            120 * time.Second,
		DisableStreamUsage: resolved.DisableStreamUsage,
	}

	if logger == nil {
		logger = &DefaultLogger{}
	}

	return NewLiteLLMProviderWithLogger(cfg, logger)
}

// Name returns the name of the provider.
func (p *LiteLLMProvider) Name() string {
	return "litellm"
}

// getAPIKey returns the current API key atomically.
func (p *LiteLLMProvider) getAPIKey() string {
	if v := p.apiKey.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// SetAPIKey updates the API key atomically.
func (p *LiteLLMProvider) SetAPIKey(key string) {
	p.apiKey.Store(key)
}

// Config returns the current provider configuration.
func (p *LiteLLMProvider) Config() Config {
	cfg := p.cfg
	cfg.APIKey = p.getAPIKey()
	return cfg
}

// newFallbackError creates a FallbackError for network errors.
func (p *LiteLLMProvider) newFallbackError(err error, model string) error {
	return &FallbackError{
		StatusCode: 0,
		Message:    err.Error(),
		Provider:   p.Name(),
		Model:      model,
		Cause:      err,
	}
}

// marshalBody marshals the request body, merging ExtraBody fields if configured.
func (p *LiteLLMProvider) marshalBody(req ChatRequest) ([]byte, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if len(p.cfg.ExtraBody) == 0 {
		return body, nil
	}
	var base map[string]any
	if err := json.Unmarshal(body, &base); err != nil {
		return nil, err
	}
	for k, v := range p.cfg.ExtraBody {
		base[k] = v
	}
	return json.Marshal(base)
}

// Chat sends a chat request and returns a chat response.
func (p *LiteLLMProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	// Use default model if not specified
	if req.Model == "" {
		req.Model = p.cfg.Model
	}
	req.Model = config.StripProviderPrefix(req.Model)

	// Set defaults from config
	if req.MaxTokens == 0 && p.cfg.MaxTokens > 0 {
		req.MaxTokens = p.cfg.MaxTokens
	}
	if req.Temperature == 0 && p.cfg.Temperature > 0 {
		req.Temperature = p.cfg.Temperature
	}

	// Build the request URL
	apiBase := p.cfg.APIBase
	if apiBase == "" {
		apiBase = "https://openrouter.ai/api/v1"
	}
	url := strings.TrimRight(apiBase, "/") + "/chat/completions"

	// Marshal the request body
	body, err := p.marshalBody(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create the HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")
	if key := p.getAPIKey(); key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
	httpReq.Header.Set("Accept", "application/json")

	// Add extra headers
	for k, v := range p.cfg.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}

	p.logger.Debug("Sending chat request", "model", req.Model, "url", url)

	// Send the request
	resp, err := p.client.Do(httpReq)
	if err != nil {
		// Wrap network errors in FallbackError to trigger fallback
		return nil, p.newFallbackError(err, req.Model)
	}
	defer resp.Body.Close()

	// DEBUG: Log HTTP response details
	p.logger.Debug("HTTP response received", "status", resp.StatusCode, "model", req.Model)

	// Read the response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		return nil, p.parseError(respBody, resp.StatusCode, resp.Header.Get("Retry-After"))
	}

	// Parse the response
	var chatResp ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	p.logger.Debug("Received chat response", "choices", len(chatResp.Choices), "usage", chatResp.Usage)

	// DEBUG: Log parsed response details
	if len(chatResp.Choices) > 0 {
		p.logger.Debug("Parsed LLM response", "content_length", len(chatResp.Choices[0].Message.Content), "content_preview", truncate(chatResp.Choices[0].Message.Content, 200), "tool_calls", len(chatResp.Choices[0].Message.ToolCalls))
	}
	return &chatResp, nil
}

// ChatStream sends a chat request and returns a channel of stream chunks.
func (p *LiteLLMProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	// Use default model if not specified
	if req.Model == "" {
		req.Model = p.cfg.Model
	}
	req.Model = config.StripProviderPrefix(req.Model)

	// Set defaults from config
	if req.MaxTokens == 0 && p.cfg.MaxTokens > 0 {
		req.MaxTokens = p.cfg.MaxTokens
	}
	if req.Temperature == 0 && p.cfg.Temperature > 0 {
		req.Temperature = p.cfg.Temperature
	}

	// Enable streaming
	req.Stream = true

	// Ask for the usage-bearing final chunk, or every streaming turn reports
	// zero tokens to anything billing or budgeting off the API (#301).
	// providers.<name>.disable_stream_usage opts an endpoint out if it
	// rejects the field.
	if !p.cfg.DisableStreamUsage {
		req.StreamOptions = &StreamOptions{IncludeUsage: true}
	}

	// Build the request URL
	apiBase := p.cfg.APIBase
	if apiBase == "" {
		apiBase = "https://openrouter.ai/api/v1"
	}
	url := strings.TrimRight(apiBase, "/") + "/chat/completions"

	// Marshal the request body
	body, err := p.marshalBody(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create the HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")
	if key := p.getAPIKey(); key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	// Add extra headers
	for k, v := range p.cfg.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}

	p.logger.Debug("Starting stream", "model", req.Model, "url", url)

	// Send the request
	resp, err := p.client.Do(httpReq)
	if err != nil {
		// Wrap network errors in FallbackError to trigger fallback
		return nil, p.newFallbackError(err, req.Model)
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, p.parseError(respBody, resp.StatusCode, resp.Header.Get("Retry-After"))
	}

	// Create the channel
	ch := make(chan StreamChunk, 10)

	// Start the streaming goroutine
	go p.streamReader(ctx, resp.Body, ch)

	return ch, nil
}

// streamReader reads streaming response chunks from the body and sends them to the channel.
func (p *LiteLLMProvider) streamReader(ctx context.Context, body io.Reader, ch chan<- StreamChunk) {
	defer close(ch)
	defer p.logger.Debug("Stream closed")

	reader := bufio.NewReader(body)

	for {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			p.logger.Debug("Stream cancelled", "error", ctx.Err())
			return
		default:
		}

		// Read a line (SSE format)
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			p.logger.Error("Failed to read stream line", "error", err)
			continue
		}

		// Skip empty lines and comment lines
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		// Remove "data: " prefix
		if strings.HasPrefix(line, "data: ") {
			line = strings.TrimPrefix(line, "data: ")
		}

		// Check for [DONE] signal
		if line == "[DONE]" {
			break
		}

		// Parse the JSON chunk
		var chunk StreamChunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			p.logger.Error("Failed to decode stream chunk", "error", err, "line", line)
			continue
		}

		// Skip empty chunks — except the usage frame, which by contract has
		// zero choices and arrives last (#301).
		if len(chunk.Choices) == 0 && chunk.Usage == nil {
			continue
		}

		// Send the chunk
		select {
		case ch <- chunk:
		case <-ctx.Done():
			return
		}
	}
}

// Transcribe transcribes audio data using the audio transcription endpoint.
func (p *LiteLLMProvider) Transcribe(ctx context.Context, audioData []byte, prompt string) (string, error) {
	// Build the request URL
	apiBase := p.cfg.APIBase
	if apiBase == "" {
		apiBase = "https://openrouter.ai/api/v1"
	}
	url := strings.TrimRight(apiBase, "/") + "/audio/transcriptions"

	// Note: Real implementation would use multipart form upload
	// For simplicity, returning an error indicating this needs implementation
	_ = url
	_ = prompt

	p.logger.Warn("Transcribe not fully implemented - requires multipart form upload")

	return "", fmt.Errorf("transcribe not implemented: requires multipart form upload")
}

// parseError parses an error response from the API and returns a FallbackError
// for errors that should trigger fallback (rate limits, server errors, etc.)
// retryAfter is the upstream Retry-After header value, empty when absent.
func (p *LiteLLMProvider) parseError(body []byte, statusCode int, retryAfter string) error {
	// Try to parse as an OpenAI-style error response
	var errResp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}

	errMsg := "unknown error"
	if err := json.Unmarshal(body, &errResp); err == nil {
		if errResp.Error.Message != "" {
			errMsg = errResp.Error.Message
		}
	} else {
		// Fallback to raw body if not JSON
		errMsg = string(body)
	}

	// The provider's error body is outside our control and routinely quotes the
	// request back, credential included. It reaches the user as reply text, so
	// it is redacted here rather than relying on the log writer.
	errMsg = redact.String(errMsg)

	// Determine if this error should trigger fallback
	shouldFallback := isFallbackStatusCode(statusCode)

	// Create the fallback error with structured information
	fallbackErr := &FallbackError{
		StatusCode: statusCode,
		Message:    errMsg,
		Provider:   p.Name(),
		Model:      p.cfg.Model,
		RetryAfter: ParseRetryAfter(retryAfter),
	}

	// If it's a fallback error, return the FallbackError type
	// Otherwise, return a plain error that won't trigger fallback
	if shouldFallback {
		return fallbackErr
	}

	// Return a non-fallback error for client errors (400, 401, 403, etc.)
	return fmt.Errorf("API error (%d): %s", statusCode, errMsg)
}

// ListModels fetches available models from an OpenAI-compatible API.
func ListModels(cfg Config) ([]string, error) {
	// No silent default: an empty base used to fall back to OpenRouter, so a
	// credential for some other (or unknown) provider was "checked" against
	// openrouter.ai and reported as validated. Refusing here is what lets
	// callers say "could not verify" instead of claiming a check they never ran.
	apiBase := cfg.APIBase
	if apiBase == "" {
		return nil, fmt.Errorf("no API base URL configured")
	}
	url := strings.TrimRight(apiBase, "/") + "/models"

	httpReq, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	httpReq.Header.Set("Accept", "application/json")

	for k, v := range cfg.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	models := make([]string, len(result.Data))
	for i, m := range result.Data {
		models[i] = m.ID
	}

	return models, nil
}

// truncate truncates a string to maxLen and adds "..." if truncated
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
