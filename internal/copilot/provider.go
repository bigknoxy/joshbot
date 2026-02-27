package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bigknoxy/joshbot/internal/providers"
)

func init() {
	providers.RegisterProviderWithInfo("github-copilot", providers.ProviderInfo{
		Factory: func(cfg providers.Config) (providers.Provider, error) {
			if cfg.APIKey == "" {
				return nil, fmt.Errorf("github-copilot requires authentication. Run: joshbot auth github-copilot")
			}
			return NewCopilotProvider(cfg, cfg.APIKey), nil
		},
		DefaultModel: "gpt-4o",
		DisplayName:  "GitHub Copilot",
		Description:  "GitHub Copilot (requires OAuth authentication)",
	})
}

const copilotModel = "gpt-4o"

type CopilotProvider struct {
	cfg         providers.Config
	accessToken string
	client      *http.Client
}

func NewCopilotProvider(cfg providers.Config, accessToken string) *CopilotProvider {
	if cfg.Timeout == 0 {
		cfg.Timeout = 120 * time.Second
	}
	if cfg.Model == "" {
		cfg.Model = copilotModel
	}

	return &CopilotProvider{
		cfg:         cfg,
		accessToken: accessToken,
		client: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

func (p *CopilotProvider) Name() string {
	return "github-copilot"
}

func (p *CopilotProvider) Config() providers.Config {
	return p.cfg
}

func (p *CopilotProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	if req.Model == "" {
		req.Model = p.cfg.Model
	}

	if req.MaxTokens == 0 && p.cfg.MaxTokens > 0 {
		req.MaxTokens = p.cfg.MaxTokens
	}
	if req.Temperature == 0 && p.cfg.Temperature > 0 {
		req.Temperature = p.cfg.Temperature
	}

	url := strings.TrimRight(CopilotAPIURL, "/") + "/chat/completions"

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+p.accessToken)
	httpReq.Header.Set("Openai-Intent", "conversation-edits")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "joshbot/"+Version)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, p.parseError(respBody, resp.StatusCode)
	}

	var chatResp providers.ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &chatResp, nil
}

func (p *CopilotProvider) ChatStream(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	return nil, fmt.Errorf("streaming not yet implemented")
}

func (p *CopilotProvider) Transcribe(ctx context.Context, audioData []byte, prompt string) (string, error) {
	_ = ctx
	_ = audioData
	_ = prompt
	return "", fmt.Errorf("transcribe not supported by GitHub Copilot")
}

func (p *CopilotProvider) parseError(body []byte, statusCode int) error {
	if statusCode == http.StatusForbidden {
		return fmt.Errorf("GitHub Copilot authentication expired. Run: joshbot auth github-copilot")
	}

	var errResp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &errResp); err == nil {
		if errResp.Error.Message != "" {
			return fmt.Errorf("API error (%d): %s (type: %s, code: %s)",
				statusCode, errResp.Error.Message, errResp.Error.Type, errResp.Error.Code)
		}
	}

	return fmt.Errorf("API request failed with status %d: %s", statusCode, string(body))
}

const copilotCatalogURL = "https://models.github.ai/catalog/models"

type copilotCatalogModel struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Publisher    string   `json:"publisher"`
	Summary      string   `json:"summary"`
	Capabilities []string `json:"capabilities"`
}

func ListModels(accessToken string) ([]string, error) {
	httpReq, err := http.NewRequest(http.MethodGet, copilotCatalogURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+accessToken)
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	httpReq.Header.Set("User-Agent", "joshbot/"+Version)

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

	var models []copilotCatalogModel
	if err := json.Unmarshal(body, &models); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	result := make([]string, len(models))
	for i, m := range models {
		result[i] = m.ID
	}

	return result, nil
}

var _ providers.Provider = (*CopilotProvider)(nil)
