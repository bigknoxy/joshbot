package skills

import (
	"context"
	"fmt"
	"strings"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// Extractor converts a Trace into a SKILL.md by asking the LLM to produce
// a reusable procedure from the steps taken.
type Extractor struct {
	provider providers.Provider
	model    string
}

// NewExtractor builds an Extractor backed by the given LLM provider.
// model can be empty; the provider uses its default.
func NewExtractor(provider providers.Provider, model string) *Extractor {
	return &Extractor{provider: provider, model: model}
}

// Extract sends the trace (plus a list of existing skill names) to the LLM and
// returns the raw SKILL.md content (including frontmatter) or an error.
func (e *Extractor) Extract(ctx context.Context, trace Trace, existingSkills []*Skill) (string, error) {
	var toolCallsStr strings.Builder
	for i, tc := range trace.ToolCalls {
		toolCallsStr.WriteString(fmt.Sprintf("  %d. Tool: %s\n", i+1, tc.Tool))
		toolCallsStr.WriteString(fmt.Sprintf("     Args: %v\n", tc.Args))
		toolCallsStr.WriteString(fmt.Sprintf("     Result: %s\n", tc.Result))
	}

	var existingStr string
	if len(existingSkills) > 0 {
		names := make([]string, len(existingSkills))
		for i, s := range existingSkills {
			names[i] = s.Name
		}
		existingStr = "Existing skills: " + strings.Join(names, ", ") + "\n"
	}

	prompt := fmt.Sprintf(`You are a skill extraction system. Given a conversation trace, generate a reusable SKILL.md file.

The trace shows how the agent solved a user's request using tools. Create a SKILL.md that captures this procedure as a reusable skill.

User message: %s

Tool calls:
%s
Final output: %s

%s
Generate a SKILL.md with:
1. YAML frontmatter (name, description, tags, requirements)
2. Clear step-by-step body that the agent can follow

Requirements:
- name must be lowercase with hyphens (e.g., "git-commit-skill")
- description should be concise (under 200 chars)
- tags should be relevant categories (e.g., ["git", "automation"])
- requirements should list any needed binaries (e.g., ["bin:git"])
- Body should be detailed instructions the agent can follow

Output ONLY the SKILL.md content, nothing else.`,
		trace.UserMessage,
		toolCallsStr.String(),
		trace.FinalOutput,
		existingStr,
	)

	req := providers.ChatRequest{
		Messages: []providers.Message{
			{Role: providers.RoleUser, Content: prompt},
		},
		MaxTokens:   2000,
		Temperature: 0.3,
	}
	if e.model != "" {
		req.Model = e.model
	}

	resp, err := e.provider.Chat(ctx, req)
	if err != nil {
		return "", fmt.Errorf("extraction failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("extraction LLM returned no choices")
	}

	content := strings.TrimSpace(resp.Choices[0].Message.Content)
	return content, nil
}
