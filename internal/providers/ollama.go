package providers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/log"
)

const (
	DefaultOllamaBaseURL = "http://localhost:11434"
	ollamaBaseURLEnvVar  = "OLLAMA_BASE_URL"
)

type OllamaClient struct {
	BaseURL string
	client  *http.Client
}

type ModelInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type listModelsResponse struct {
	Models []ModelInfo `json:"models"`
}

func NewOllamaClient(baseURL string) *OllamaClient {
	if baseURL == "" {
		baseURL = GetDefaultAPIBase()
	}

	return &OllamaClient{
		BaseURL: baseURL,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func GetDefaultAPIBase() string {
	if baseURL := os.Getenv(ollamaBaseURLEnvVar); baseURL != "" {
		return baseURL
	}
	return DefaultOllamaBaseURL
}

func (c *OllamaClient) ListModels() ([]ModelInfo, error) {
	url := strings.TrimRight(c.BaseURL, "/") + "/api/tags"

	httpReq, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Accept", "application/json")

	log.Debug("Fetching Ollama models", "url", url)

	resp, err := c.client.Do(httpReq)
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

	var result listModelsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	log.Debug("Retrieved Ollama models", "count", len(result.Models))

	return result.Models, nil
}
