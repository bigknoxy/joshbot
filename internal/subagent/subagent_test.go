package subagent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/providers"
)

type truncatingMockProvider struct{}

func (m *truncatingMockProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	fullResponse := sampleGoCLI

	if req.MaxTokens > 0 {
		maxChars := req.MaxTokens * 4
		if maxChars < len(fullResponse) {
			fullResponse = fullResponse[:maxChars]
		}
	}

	return &providers.ChatResponse{
		Choices: []providers.Choice{
			{
				Message: providers.Message{
					Content: fullResponse,
				},
			},
		},
	}, nil
}

func (m *truncatingMockProvider) ChatStream(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *truncatingMockProvider) Transcribe(ctx context.Context, audioData []byte, prompt string) (string, error) {
	return "", fmt.Errorf("not implemented")
}

func (m *truncatingMockProvider) Name() string {
	return "truncating-mock"
}

func (m *truncatingMockProvider) Config() providers.Config {
	return providers.DefaultConfig()
}

var sampleGoCLI = `package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"text/tabwriter"
	"time"
)

type RepoStats struct {
	FullName    string ` + "`json:\"full_name\"`" + `
	Stars       int    ` + "`json:\"stargazers_count\"`" + `
	Forks       int    ` + "`json:\"forks_count\"`" + `
	OpenIssues  int    ` + "`json:\"open_issues_count\"`" + `
	Description string ` + "`json:\"description\"`" + `
	Language    string ` + "`json:\"language\"`" + `
	UpdatedAt   string ` + "`json:\"updated_at\"`" + `
}

type GitHubError struct {
	Message string ` + "`json:\"message\"`" + `
}

func fetchRepoStats(owner, repo, token string) (*RepoStats, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching repo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var ghErr GitHubError
		json.NewDecoder(resp.Body).Decode(&ghErr)
		return nil, fmt.Errorf("GitHub API error (%d): %s", resp.StatusCode, ghErr.Message)
	}
	var stats RepoStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	return &stats, nil
}

func formatTable(stats *RepoStats) string {
	var sb strings.Builder
	w := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Property\tValue\n")
	fmt.Fprintf(w, "-----\t-----\n")
	fmt.Fprintf(w, "Repository\t%s\n", stats.FullName)
	fmt.Fprintf(w, "Stars\t%d\n", stats.Stars)
	fmt.Fprintf(w, "Forks\t%d\n", stats.Forks)
	fmt.Fprintf(w, "Open Issues\t%d\n", stats.OpenIssues)
	fmt.Fprintf(w, "Language\t%s\n", stats.Language)
	fmt.Fprintf(w, "Description\t%s\n", stats.Description)
	fmt.Fprintf(w, "Last Updated\t%s\n", stats.UpdatedAt)
	w.Flush()
	return sb.String()
}

func main() {
	token := flag.String("token", "", "GitHub personal access token")
	flag.Parse()
	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: ghstats [--token TOKEN] owner/repo\n")
		os.Exit(1)
	}
	parts := strings.Split(args[0], "/")
	if len(parts) != 2 {
		fmt.Fprintf(os.Stderr, "Error: argument must be in owner/repo format\n")
		os.Exit(1)
	}
	stats, err := fetchRepoStats(parts[0], parts[1], *token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(formatTable(stats))
}
`

func TestSubagent_500TokenTruncation_Blocker(t *testing.T) {
	provider := &truncatingMockProvider{}

	runner := NewRunner(provider, "test-model", 500, 0.3, 30*time.Second)
	result, err := runner.Run(context.Background(), "write a Go CLI tool for GitHub stats")
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("500-token limit produces %d chars (~%d tokens)", len(result), len(result)/4)

	missing := 0
	for _, section := range []struct{ name, content string }{
		{"fetchRepoStats function", "func fetchRepoStats"},
		{"formatTable function", "func formatTable"},
		{"main() function", "func main()"},
		{"os.Exit call", "os.Exit"},
	} {
		if !strings.Contains(result, section.content) {
			missing++
			t.Logf("MISSING: %s", section.name)
		}
	}

	if missing >= 2 {
		t.Logf("BLOCKER CONFIRMED: 500-token maxTokens causes code truncation (%d/4 essential sections lost)", missing)
		return
	}

	if missing > 0 {
		t.Logf("PARTIAL TRUNCATION: 500-token limit dropped %d/4 sections", missing)
		return
	}

	t.Log("NOTE: Mock output fit within 500 tokens (unlikely with real LLM output)")
}

func TestSubagent_MaxTokens4096Sufficient(t *testing.T) {
	tokens := len(sampleGoCLI) / 4
	if tokens > 4096 {
		t.Fatalf("sample Go CLI (~%d tokens) exceeds even 4096-token limit", tokens)
	}

	t.Logf("Sample Go CLI: %d chars, ~%d tokens — fits 4096 limit", len(sampleGoCLI), tokens)

	expected := []string{
		"package main", "encoding/json", "net/http",
		"text/tabwriter", "func fetchRepoStats",
		"func formatTable", "func main()", "os.Exit",
	}
	for _, section := range expected {
		if !strings.Contains(sampleGoCLI, section) {
			t.Fatalf("expected sample to contain: %s", section)
		}
	}
}

func TestSubagent_4096TokensProven(t *testing.T) {
	provider := &truncatingMockProvider{}

	runner := NewRunner(provider, "test-model", 4096, 0.3, 30*time.Second)
	result, err := runner.Run(context.Background(), "write a Go CLI tool for GitHub stats")
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("4096-token limit produces %d chars (~%d tokens)", len(result), len(result)/4)

	if !strings.Contains(result, "func main()") {
		t.Fatal("FIX BROKEN: 4096-token limit still truncates func main()")
	}
	if !strings.Contains(result, "func fetchRepoStats") {
		t.Fatal("FIX BROKEN: 4096-token limit still truncates func fetchRepoStats")
	}
	if !strings.Contains(result, "os.Exit") {
		t.Fatal("FIX BROKEN: 4096-token limit still truncates os.Exit")
	}
	t.Log("FIX CONFIRMED: 4096-token limit preserves full Go CLI output")
}
