package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bigknoxy/joshbot/internal/config"
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

	return &LiteLLMProvider{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
		},
		logger: logger,
	}
}

// NewProviderFromResolvedModel creates a provider from a resolved model config.
func NewProviderFromResolvedModel(resolved config.ResolvedModelConfig, logger Logger) *LiteLLMProvider {
	maxTokens := resolved.MaxTokens
	if maxTokens <= 0 {
		maxTokens = config.DefaultMaxTokens
	}

	cfg := Config{
		APIKey:       resolved.APIKey,
		APIBase:      resolved.APIBase,
		Model:        resolved.ModelID,
		MaxTokens:    maxTokens,
		ExtraHeaders: resolved.Extra,
		Timeout:      120 * time.Second,
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

// Config returns the current provider configuration.
func (p *LiteLLMProvider) Config() Config {
	return p.cfg
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

// Chat sends a chat request and returns a chat response.
func (p *LiteLLMProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	// Use default model if not specified
	if req.Model == "" {
		req.Model = p.cfg.Model
	}

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
	body, err := json.Marshal(req)
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
	if p.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
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
		return nil, p.parseError(respBody, resp.StatusCode)
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

	// Set defaults from config
	if req.MaxTokens == 0 && p.cfg.MaxTokens > 0 {
		req.MaxTokens = p.cfg.MaxTokens
	}
	if req.Temperature == 0 && p.cfg.Temperature > 0 {
		req.Temperature = p.cfg.Temperature
	}

	// Enable streaming
	req.Stream = true

	// Build the request URL
	apiBase := p.cfg.APIBase
	if apiBase == "" {
		apiBase = "https://openrouter.ai/api/v1"
	}
	url := strings.TrimRight(apiBase, "/") + "/chat/completions"

	// Marshal the request body
	body, err := json.Marshal(req)
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
	if p.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
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
		return nil, p.parseError(respBody, resp.StatusCode)
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

		// Skip empty chunks
		if len(chunk.Choices) == 0 {
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

	// Create multipart form body
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	// Add model field
	if err := writer.WriteField("model", "whisper-1"); err != nil {
		return "", fmt.Errorf("failed to write model field: %w", err)
	}

	// Add prompt field if provided
	if prompt != "" {
		if err := writer.WriteField("prompt", prompt); err != nil {
			return "", fmt.Errorf("failed to write prompt field: %w", err)
		}
	}

	// Add audio file
	part, err := writer.CreateFormFile("file", "audio.ogg")
	if err != nil {
		return "", fmt.Errorf("failed to create form file: %w", err)
	}
	if _, err := part.Write(audioData); err != nil {
		return "", fmt.Errorf("failed to write audio data: %w", err)
	}

	// Close the writer to set the boundary
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("failed to close multipart writer: %w", err)
	}

	// Create the HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", url, &body)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)

	// Send the request
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("transcription request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check for error response
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("transcription failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	// Parse the response
	var result struct {
		Text string `json:"text"`
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("failed to parse transcription response: %w", err)
	}

	p.logger.Debug("Audio transcribed", "length", len(result.Text))

	return result.Text, nil
}

// parseError parses an error response from the API and returns a FallbackError
// for errors that should trigger fallback (rate limits, server errors, etc.)
func (p *LiteLLMProvider) parseError(body []byte, statusCode int) error {
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

	// Determine if this error should trigger fallback
	shouldFallback := isFallbackStatusCode(statusCode)

	// Create the fallback error with structured information
	fallbackErr := &FallbackError{
		StatusCode: statusCode,
		Message:    errMsg,
		Provider:   p.Name(),
		Model:      p.cfg.Model,
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
	apiBase := cfg.APIBase
	if apiBase == "" {
		apiBase = "https://openrouter.ai/api/v1"
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

// LiteLLMProviderWithTools extends LiteLLMProvider with tool execution capabilities.
type LiteLLMProviderWithTools struct {
	*LiteLLMProvider
	mu           sync.Mutex
	toolExecutor func(ctx context.Context, req ToolCallRequest) (*ToolCallResponse, error)
}

// NewLiteLLMProviderWithTools creates a new LiteLLM provider with tool execution support.
func NewLiteLLMProviderWithTools(cfg Config, executor func(ctx context.Context, req ToolCallRequest) (*ToolCallResponse, error)) *LiteLLMProviderWithTools {
	return NewLiteLLMProviderWithToolsAndLogger(cfg, executor, &DefaultLogger{})
}

// NewLiteLLMProviderWithToolsAndLogger creates a new LiteLLM provider with tool execution support and custom logger.
func NewLiteLLMProviderWithToolsAndLogger(cfg Config, executor func(ctx context.Context, req ToolCallRequest) (*ToolCallResponse, error), logger Logger) *LiteLLMProviderWithTools {
	return &LiteLLMProviderWithTools{
		LiteLLMProvider: NewLiteLLMProviderWithLogger(cfg, logger),
		toolExecutor:    executor,
	}
}

// ExecuteTool executes a tool call and returns the result.
func (p *LiteLLMProviderWithTools) ExecuteTool(ctx context.Context, req ToolCallRequest) (*ToolCallResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.toolExecutor == nil {
		return &ToolCallResponse{
			ToolCallID: req.ToolCallID,
			Error:      "no tool executor configured",
			IsError:    true,
		}, nil
	}

	return p.toolExecutor(ctx, req)
}

// ChatWithTools executes a chat with automatic tool calling.
// It continues calling tools until the model returns a final response.
func (p *LiteLLMProviderWithTools) ChatWithTools(ctx context.Context, req ChatRequest, maxIterations int) (*ChatResponse, error) {
	if maxIterations <= 0 {
		maxIterations = 20
	}

	// Store tool results
	toolResults := make([]Message, 0, maxIterations)

	for iteration := 0; iteration < maxIterations; iteration++ {
		// Add previous tool results to messages
		if len(toolResults) > 0 {
			req.Messages = append(req.Messages, toolResults...)
			toolResults = toolResults[:0]
		}

		// Send the chat request
		resp, err := p.Chat(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("chat request failed: %w", err)
		}

		// Check if we got a valid response
		if len(resp.Choices) == 0 {
			return resp, nil
		}

		choice := resp.Choices[0]
		message := choice.Message

		// Check if the model made tool calls
		if len(message.ToolCalls) == 0 {
			// No tool calls - this is the final response
			return resp, nil
		}

		p.logger.Info("Executing tool calls",
			"count", len(message.ToolCalls),
			"iteration", iteration+1)

		// Execute each tool call
		for _, tc := range message.ToolCalls {
			// Parse the arguments
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				toolResults = append(toolResults, Message{
					Role:       RoleTool,
					ToolCallID: tc.ID,
					Content:    fmt.Sprintf("Error: failed to parse arguments: %v", err),
					Name:       tc.Function.Name,
				})
				continue
			}

			// Execute the tool
			toolReq := ToolCallRequest{
				ToolCallID:   tc.ID,
				FunctionName: tc.Function.Name,
				Arguments:    args,
			}

			toolResp, err := p.ExecuteTool(ctx, toolReq)
			if err != nil {
				toolResults = append(toolResults, Message{
					Role:       RoleTool,
					ToolCallID: tc.ID,
					Content:    fmt.Sprintf("Error: %v", err),
					Name:       tc.Function.Name,
				})
				continue
			}

			// Add the tool result
			content := toolResp.Content
			if toolResp.Error != "" {
				content = fmt.Sprintf("Error: %s", toolResp.Error)
			}

			toolResults = append(toolResults, Message{
				Role:       RoleTool,
				ToolCallID: tc.ID,
				Content:    content,
				Name:       tc.Function.Name,
			})
		}

		// Continue to next iteration
	}

	// Reached max iterations - return the last response
	p.logger.Warn("Max tool iterations reached", "max", maxIterations)

	// Create a final message indicating max iterations reached
	finalReq := req
	finalReq.Messages = append(finalReq.Messages, toolResults...)
	finalReq.Tools = nil // Remove tools to prevent further calls
	return p.Chat(ctx, finalReq)
}

// ParseToolArguments parses JSON arguments from a tool call into a typed struct.
func ParseToolArguments[T any](args string) (*T, error) {
	var result T
	if err := json.Unmarshal([]byte(args), &result); err != nil {
		return nil, fmt.Errorf("failed to parse arguments: %w", err)
	}
	return &result, nil
}

// truncate truncates a string to maxLen and adds "..." if truncated
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
