package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/bigknoxy/joshbot/internal/skills"
)

// SkillRegistryTool exposes skill management (list, create, delete, get) to the agent.
type SkillRegistryTool struct {
	loader *skills.Loader
}

// NewSkillRegistryTool creates a tool that wraps a skills.Loader.
func NewSkillRegistryTool(loader *skills.Loader) *SkillRegistryTool {
	return &SkillRegistryTool{loader: loader}
}

func (t *SkillRegistryTool) Name() string { return "skill_registry" }

func (t *SkillRegistryTool) Description() string {
	return "skill_registry: list, create, delete, or get the full content of a skill in the registry. Use \"get\" to load a skill's full instructions before following them — the list summary is a one-line description only."
}

func (t *SkillRegistryTool) Parameters() []Parameter {
	return []Parameter{
		{
			Name:        "action",
			Type:        ParamString,
			Description: "Action: list, create, delete, get",
			Required:    true,
			Enum:        []string{"list", "create", "delete", "get"},
		},
		{
			Name:        "name",
			Type:        ParamString,
			Description: "Skill name (required: create/delete/get)",
		},
		{
			Name:        "content",
			Type:        ParamString,
			Description: "Full SKILL.md content (required: create)",
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
	case "get":
		return t.getSkill(ctx, args)
	default:
		return ToolResult{Error: fmt.Errorf("unknown action: %s (expected list, create, delete, or get)", action)}
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

// getSkill returns a skill's full content. This is the only working path to
// a non-Always skill's instructions: `read_file` cannot reach it, since the
// summary the model sees (LoadSummary) never carries a filesystem path, and
// a bundled skill has no real path at all — it is compiled into the binary
// via go:embed and its Path is a virtual "bundled/<name>" string. The prior
// design told the model to "use read_file to load full skill content" with
// no way to satisfy that instruction, which made every non-Always skill's
// detailed instructions effectively unreachable.
func (t *SkillRegistryTool) getSkill(ctx interface{}, args map[string]any) ToolResult {
	name, _ := args["name"].(string)
	if name == "" {
		return ToolResult{Error: fmt.Errorf("name is required for get action")}
	}
	if t.loader == nil {
		return ToolResult{Error: fmt.Errorf("skill loader not available")}
	}

	reqCtx, ok := ctx.(context.Context)
	if !ok || reqCtx == nil {
		reqCtx = context.Background()
	}

	content, err := t.loader.LoadFullSkillContent(reqCtx, name)
	if err != nil {
		return ToolResult{Error: err}
	}
	if content == "" {
		return ToolResult{Error: fmt.Errorf("skill %q not found", name)}
	}
	return ToolResult{Output: content}
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
