package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
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

// Header values the Copilot API requires. It rejects requests that do not
// identify themselves as a known Copilot integration.
const (
	copilotIntegrationID = "vscode-chat"
	copilotEditorVersion = "vscode/1.99.3"
	copilotPluginVersion = "copilot-chat/0.26.7"
	copilotAPIVersion    = "2025-04-01"
)

type CopilotProvider struct {
	cfg         providers.Config
	accessToken string
	client      *http.Client

	mu         sync.Mutex
	apiToken   string
	apiExpires int64
}

// ensureAPIToken returns a valid Copilot API token, exchanging the GitHub OAuth
// token for one when the cached token is missing or within a minute of expiry.
func (p *CopilotProvider) ensureAPIToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.apiToken != "" && (p.apiExpires == 0 || time.Now().Unix() < p.apiExpires-60) {
		return p.apiToken, nil
	}

	tok, err := ExchangeCopilotToken(ctx, p.accessToken)
	if err != nil {
		return "", err
	}
	p.apiToken = tok.Token
	p.apiExpires = tok.ExpiresAt
	return p.apiToken, nil
}

// setCopilotHeaders applies the headers every Copilot API request needs.
func setCopilotHeaders(h http.Header, apiToken string) {
	h.Set("Authorization", "Bearer "+apiToken)
	h.Set("Copilot-Integration-Id", copilotIntegrationID)
	h.Set("Editor-Version", copilotEditorVersion)
	h.Set("Editor-Plugin-Version", copilotPluginVersion)
	h.Set("Openai-Intent", "conversation-panel")
	h.Set("X-Github-Api-Version", copilotAPIVersion)
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	h.Set("User-Agent", "joshbot/"+Version)
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

	apiToken, err := p.ensureAPIToken(ctx)
	if err != nil {
		return nil, err
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

	setCopilotHeaders(httpReq.Header, apiToken)
	httpReq.Header.Set("X-Initiator", "agent")

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
		// A rejected credential may just be a stale cached exchange; drop it so
		// the next call re-exchanges instead of replaying a dead token.
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			p.mu.Lock()
			p.apiToken = ""
			p.apiExpires = 0
			p.mu.Unlock()
		}
		return nil, p.parseError(respBody, resp.StatusCode)
	}

	var chatResp providers.ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &chatResp, nil
}

func (p *CopilotProvider) ChatStream(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	return nil, fmt.Errorf("github-copilot: %w", providers.ErrStreamingUnsupported)
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

type copilotCatalogModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListModels returns the model ids the Copilot API will accept. accessToken is
// the GitHub OAuth token; it is exchanged for a Copilot token here, because the
// OAuth token alone is not accepted by api.githubcopilot.com.
func ListModels(accessToken string) ([]string, error) {
	ctx := context.Background()
	apiToken, err := ExchangeCopilotToken(ctx, accessToken)
	if err != nil {
		return nil, err
	}

	url := strings.TrimRight(CopilotAPIURL, "/") + "/models"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	setCopilotHeaders(httpReq.Header, apiToken.Token)

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

	var list struct {
		Data []copilotCatalogModel `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	result := make([]string, len(list.Data))
	for i, m := range list.Data {
		result[i] = m.ID
	}

	return result, nil
}

var _ providers.Provider = (*CopilotProvider)(nil)
