---
name: skill-creator
description: "Meta-skill for creating new skills that extend joshbot's capabilities"
always: false
tags: [meta, self-improvement]
---

# Skill Creator

You can create new skills to extend your own capabilities. Skills are markdown files that teach you how to do specific things.

## Creating a New Skill

1. Create a directory: `workspace/skills/{skill-name}/`
2. Create the skill file: `workspace/skills/{skill-name}/SKILL.md`
3. Optionally create supporting directories:
   - `scripts/` - executable scripts the skill uses
   - `references/` - reference documentation
   - `assets/` - templates, configs, or other files

## SKILL.md Format

```yaml
---
name: skill-name
description: "One-line description of what this skill does"
always: false  # Set to true if this should always be loaded into context
requirements: [bin:some-binary, env:SOME_ENV_VAR]  # Optional dependencies
tags: [category1, category2]
---

# Skill Name

Detailed instructions for how to use this skill.
Include examples, best practices, and common patterns.
```

## Guidelines for Creating Skills

1. **Keep skills focused** - one skill per capability
2. **Write clear instructions** - you'll be reading these later
3. **Include examples** - concrete examples help you use the skill correctly
4. **Declare requirements** - if the skill needs a binary (like `git`) or env var, declare it
5. **Use `always: true` sparingly** - only for skills that should always be in context
6. **Test the skill** - try using it after creation to make sure it works

## When to Create Skills

- When you find yourself repeatedly doing the same type of task
- When the user teaches you a specific workflow
- When you discover a useful pattern that should be remembered
- When integrating with a new tool or service

## Workspace Skills Override Bundled

If you create a workspace skill with the same name as a bundled skill, your workspace version takes priority. This lets you customize built-in behaviors.
