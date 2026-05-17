package tools

import (
	"fmt"
	"strings"

	"github.com/bigknoxy/joshbot/internal/skills"
)

// SkillRegistryTool exposes skill management (list, create, delete) to the agent.
type SkillRegistryTool struct {
	loader *skills.Loader
}

// NewSkillRegistryTool creates a tool that wraps a skills.Loader.
func NewSkillRegistryTool(loader *skills.Loader) *SkillRegistryTool {
	return &SkillRegistryTool{loader: loader}
}

func (t *SkillRegistryTool) Name() string { return "skill_registry" }

func (t *SkillRegistryTool) Description() string {
	return "List, create, or delete skills in the skill registry. Actions: list (list all skills), create (create a new skill with YAML frontmatter + body), delete (delete a skill by name)."
}

func (t *SkillRegistryTool) Parameters() []Parameter {
	return []Parameter{
		{
			Name:        "action",
			Type:        ParamString,
			Description: "Action to perform: list, create, delete",
			Required:    true,
			Enum:        []string{"list", "create", "delete"},
		},
		{
			Name:        "name",
			Type:        ParamString,
			Description: "Skill name (required for create/delete)",
		},
		{
			Name:        "content",
			Type:        ParamString,
			Description: "Full SKILL.md content including frontmatter and body (required for create)",
		},
	}
}

func (t *SkillRegistryTool) Execute(ctx interface{}, args map[string]any) ToolResult {
	action, _ := args["action"].(string)

	switch action {
	case "list":
		return t.listSkills()
	case "create":
		return t.createSkill(args)
	case "delete":
		return t.deleteSkill(args)
	default:
		return ToolResult{Error: fmt.Errorf("unknown action: %s (expected list, create, or delete)", action)}
	}
}

func (t *SkillRegistryTool) listSkills() ToolResult {
	if t.loader == nil {
		return ToolResult{Error: fmt.Errorf("skill loader not available")}
	}

	skillsList := t.loader.List()
	if len(skillsList) == 0 {
		return ToolResult{Output: "No skills registered."}
	}

	var b strings.Builder
	for _, sk := range skillsList {
		avail := ""
		if !sk.Available() {
			avail = " (unavailable)"
		}
		fmt.Fprintf(&b, "- %s: %s%s\n", sk.Name, sk.Description, avail)
	}
	return ToolResult{Output: b.String()}
}

func (t *SkillRegistryTool) createSkill(args map[string]any) ToolResult {
	name, _ := args["name"].(string)
	content, _ := args["content"].(string)

	if name == "" {
		return ToolResult{Error: fmt.Errorf("name is required for create action")}
	}
	if content == "" {
		return ToolResult{Error: fmt.Errorf("content is required for create action")}
	}

	if t.loader == nil {
		return ToolResult{Error: fmt.Errorf("skill loader not available")}
	}

	// Check for existing skill with same name
	if existing := t.loader.GetSkill(name); existing != nil {
		return ToolResult{Error: fmt.Errorf("skill %q already exists", name)}
	}

	// Validate content against existing skills
	existingSkills := t.loader.List()
	if err := skills.ValidateSkill(content, existingSkills); err != nil {
		return ToolResult{Error: fmt.Errorf("skill validation failed: %w", err)}
	}

	if err := t.loader.Create(name, content); err != nil {
		return ToolResult{Error: fmt.Errorf("failed to create skill: %w", err)}
	}

	return ToolResult{Output: fmt.Sprintf("Skill %q created successfully.", name)}
}

func (t *SkillRegistryTool) deleteSkill(args map[string]any) ToolResult {
	name, _ := args["name"].(string)
	if name == "" {
		return ToolResult{Error: fmt.Errorf("name is required for delete action")}
	}

	if t.loader == nil {
		return ToolResult{Error: fmt.Errorf("skill loader not available")}
	}

	if err := t.loader.Delete(name); err != nil {
		return ToolResult{Error: fmt.Errorf("failed to delete skill: %w", err)}
	}

	return ToolResult{Output: fmt.Sprintf("Skill %q deleted.", name)}
}
