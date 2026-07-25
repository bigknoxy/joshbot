package main

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/bigknoxy/joshbot/internal/config"
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

	all := loader.List()
	if len(all) == 0 {
		fmt.Println("No skills found.")
		return nil
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })

	var pending int
	fmt.Println("Skills:")
	for _, sk := range all {
		switch {
		case sk.Bundled:
			fmt.Printf("  %-28s bundled\n", sk.Name)
		case sk.Trusted:
			fmt.Printf("  %-28s approved\n", sk.Name)
		default:
			pending++
			fmt.Printf("  %-28s AWAITING REVIEW  %s\n", sk.Name, filepath.Join(sk.Path, "SKILL.md"))
		}
	}

	if pending > 0 {
		fmt.Printf("\n%d skill(s) are not being used until you approve them.\n", pending)
		fmt.Println("Read the file, then run: joshbot skills trust <name>")
		fmt.Println("A skill's text becomes part of the agent's instructions, so review it as you would a script you are about to run.")
	}
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
