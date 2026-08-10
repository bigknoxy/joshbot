package main

import (
	"fmt"
	"path/filepath"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/output"
	"github.com/bigknoxy/joshbot/internal/skills"
	"github.com/urfave/cli/v2"
)

// Operator-facing commands for skill provenance.
//
// A workspace SKILL.md becomes part of the agent's standing instructions, so
// approving one is a security decision and belongs to a person, not to the
// agent. Nothing the model can reach calls into these commands.

// skillsLoaderForCLI builds a loader wired to the trust store, without
// starting the rest of the application.
func skillsLoaderForCLI(c *cli.Context) (*skills.Loader, error) {
	cfg, err := loadConfig(c.Path("config"))
	if err != nil {
		return nil, err
	}

	loader, err := skills.NewLoader(cfg.Agents.Defaults.Workspace)
	if err != nil {
		return nil, fmt.Errorf("failed to init skills loader: %w", err)
	}

	store, err := skills.LoadTrustStore(skills.DefaultTrustStorePath(config.DefaultHome))
	if err != nil {
		return nil, err
	}
	loader.SetTrustStore(store)

	if err := loader.Discover(); err != nil {
		return nil, err
	}
	return loader, nil
}

func runSkillsList(c *cli.Context) error {
	loader, err := skillsLoaderForCLI(c)
	if err != nil {
		return err
	}

	format, err := outputFormat(c)
	if err != nil {
		return err
	}

	entries := make([]output.Skill, 0, len(loader.List()))
	for _, sk := range loader.List() {
		switch {
		case sk.Bundled:
			entries = append(entries, output.Skill{Name: sk.Name, State: output.SkillBundled})
		case sk.Trusted:
			entries = append(entries, output.Skill{Name: sk.Name, State: output.SkillApproved})
		default:
			entries = append(entries, output.Skill{
				Name:  sk.Name,
				State: output.SkillPending,
				Path:  filepath.Join(sk.Path, "SKILL.md"),
			})
		}
	}
	doc := output.NewSkills(entries)

	out := reportWriter()
	if format == output.JSON {
		return output.WriteJSON(out, doc)
	}
	output.RenderSkillsText(out, doc)
	return nil
}

func runSkillsTrust(c *cli.Context) error {
	loader, err := skillsLoaderForCLI(c)
	if err != nil {
		return err
	}

	if c.Bool("all") {
		pending := loader.Untrusted()
		if len(pending) == 0 {
			fmt.Println("No skills are awaiting review.")
			return nil
		}
		for _, sk := range pending {
			if err := loader.Trust(sk.Name); err != nil {
				return fmt.Errorf("approve %q: %w", sk.Name, err)
			}
			fmt.Printf("Approved %s\n", sk.Name)
		}
		fmt.Printf("\n%d skill(s) approved.\n", len(pending))
		return nil
	}

	name := c.Args().First()
	if name == "" {
		return fmt.Errorf("give a skill name, or --all to approve every pending skill")
	}
	if err := loader.Trust(name); err != nil {
		return err
	}
	fmt.Printf("Approved %s\n", name)
	return nil
}

func runSkillsUntrust(c *cli.Context) error {
	loader, err := skillsLoaderForCLI(c)
	if err != nil {
		return err
	}

	name := c.Args().First()
	if name == "" {
		return fmt.Errorf("give a skill name to revoke")
	}
	if err := loader.Untrust(name); err != nil {
		return err
	}
	fmt.Printf("Revoked %s; it will not be used until approved again.\n", name)
	return nil
}

// pendingSkillNames returns skills awaiting review, for the status output.
//
// Failures are swallowed on purpose: this is a diagnostic line, and a status
// command that errors out because the skills directory is unreadable would be
// less useful than one that omits a line.
func pendingSkillNames(cfg *config.Config) []string {
	loader, err := skills.NewLoader(cfg.Agents.Defaults.Workspace)
	if err != nil {
		return nil
	}
	store, err := skills.LoadTrustStore(skills.DefaultTrustStorePath(config.DefaultHome))
	if err != nil {
		return nil
	}
	loader.SetTrustStore(store)
	if err := loader.Discover(); err != nil {
		return nil
	}

	var names []string
	for _, sk := range loader.Untrusted() {
		names = append(names, sk.Name)
	}
	return names
}
