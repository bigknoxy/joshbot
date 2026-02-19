package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// BuildPrompt builds the full system prompt from workspace files and injected context.
// This follows the Python implementation's build_system_prompt function.
func BuildPrompt(workspace string, skills SkillsLoader, memory MemoryLoader) string {
	parts := []string{}

	// Core identity
	parts = append(parts, buildCoreIdentity())

	// Load identity files from workspace
	identity := loadIdentityFiles(workspace)
	for name, content := range identity {
		if content != "" {
			parts = append(parts, fmt.Sprintf("<%s>\n%s\n</%s>", name, content, name))
		}
	}

	// Memory context (always loaded)
	if memory != nil {
		memContent, err := memory.LoadMemory(context.Background())
		if err == nil && memContent != "" {
			parts = append(parts, fmt.Sprintf("<memory>\n%s\n</memory>", memContent))
		}
	}

	// Skills summary
	if skills != nil {
		skillsSummary, err := skills.LoadSummary(context.Background())
		if err == nil && skillsSummary != "" {
			parts = append(parts, fmt.Sprintf("<skills>\n%s\n</skills>", skillsSummary))
		}
	}

	// Current time
	now := time.Now().UTC().Format("2006-01-02 15:04 UTC")
	parts = append(parts, fmt.Sprintf("<current_time>%s</current_time>", now))

	return joinParts(parts)
}

// buildCoreIdentity returns the core identity prompt.
func buildCoreIdentity() string {
	return `You are joshbot, a personal AI assistant. You are helpful, capable, and proactive.

You have access to tools that let you interact with the filesystem, run shell commands, search the web, and manage your own memory and skills.

Key behaviors:
- Use your tools proactively to help the user
- Remember important information by updating your memory files
- When you learn something new or develop a useful capability, consider creating a skill for it
- Search your HISTORY.md when the user references past conversations
- Be concise but thorough in your responses
- If you're unsure about something, say so and suggest ways to find out

Memory system:
- MEMORY.md contains long-term facts about the user and context (always loaded)
- HISTORY.md is an append-only log of conversation summaries (searchable via grep)
- Use read_file and write_file to manage these files
- When conversations are consolidated, key facts go to MEMORY.md and summaries to HISTORY.md`
}

// loadIdentityFiles loads identity/bootstrap files from workspace.
func loadIdentityFiles(workspace string) map[string]string {
	files := map[string]string{
		"agents":   "AGENTS.md",
		"soul":     "SOUL.md",
		"user":     "USER.md",
		"tools":    "TOOLS.md",
		"identity": "IDENTITY.md",
	}

	result := make(map[string]string)
	for key, filename := range files {
		path := filepath.Join(workspace, filename)
		data, err := os.ReadFile(path)
		if err == nil {
			result[key] = string(data)
		}
	}

	return result
}

// joinParts joins prompt parts with double newlines.
func joinParts(parts []string) string {
	result := ""
	for i, part := range parts {
		if i > 0 {
			result += "\n\n"
		}
		result += part
	}
	return result
}

// FormatToolResult formats a tool result as a message for the LLM.
func FormatToolResult(toolCallID, name, result string) providers.Message {
	return providers.Message{
		Role:       providers.RoleTool,
		Content:    result,
		Name:       name,
		ToolCallID: toolCallID,
	}
}

// FormatAssistantToolCalls formats an assistant message with tool calls.
func FormatAssistantToolCalls(content string, toolCalls []providers.ToolCall) providers.Message {
	msg := providers.Message{
		Role:    providers.RoleAssistant,
		Content: content,
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}
	return msg
}

// LoadMemoryFile loads the MEMORY.md file from the workspace.
func LoadMemoryFile(workspace string) (string, error) {
	path := filepath.Join(workspace, "memory", "MEMORY.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("failed to read MEMORY.md: %w", err)
	}
	return string(data), nil
}

// LoadHistoryFile loads the HISTORY.md file from the workspace.
func LoadHistoryFile(workspace string) (string, error) {
	path := filepath.Join(workspace, "memory", "HISTORY.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("failed to read HISTORY.md: %w", err)
	}
	return string(data), nil
}

// FormatToolDescriptions formats tool schemas for the prompt.
func FormatToolDescriptions(tools []providers.Tool) string {
	if len(tools) == 0 {
		return ""
	}

	// Format as JSON schema for tools
	schemas, err := json.MarshalIndent(tools, "", "  ")
	if err != nil {
		return ""
	}
	return string(schemas)
}
