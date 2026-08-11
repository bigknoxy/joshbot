package main

import (
	"net/url"
	"os"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/output"
)

// profileFlag is the --profile selector, shared by every command that starts an
// agent. It is defined once so the three commands cannot drift in usage text or
// in whether the flag exists at all.
func profileFlag() cli.Flag {
	return &cli.StringFlag{
		Name:  "profile",
		Usage: "Named profile to use for this run (overrides default_profile)",
	}
}

// applyProfile selects and installs the profile for a run.
//
// It is called immediately after the config is loaded and before any component
// is built, because that is what makes an unknown, disabled, or
// missing-credential profile a startup error rather than a provider error
// mid-conversation.
func applyProfile(c *cli.Context, cfg *config.Config) error {
	return cfg.ApplyProfile(cfg.SelectProfile(c.String("profile")))
}

// profilesCommand builds the read-only `joshbot profiles` command group.
func profilesCommand() *cli.Command {
	return &cli.Command{
		Name:  "profiles",
		Usage: "Inspect named model profiles",
		Description: "Profiles are named provider/model/endpoint setups you switch between with\n" +
			"--profile. A profile never stores a credential: it names the environment\n" +
			"variable holding one, and a profile carrying a raw api_key is refused when\n" +
			"the config is loaded.",
		Subcommands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List configured profiles and where each would send requests",
				// Accepted here too so `profiles list --profile x` previews
				// which profile that run would select, rather than rejecting a
				// flag every neighbouring command takes.
				Flags:  []cli.Flag{profileFlag()},
				Action: withJSONErrors(runProfilesList),
			},
		},
	}
}

// endpointHost reduces an api_base to its host.
//
// The listing shows a host rather than the configured URL because a URL may
// carry userinfo ("https://user:pass@host/v1"), which is a credential, and this
// command's contract is that nothing it prints is sensitive.
func endpointHost(apiBase string) string {
	apiBase = strings.TrimSpace(apiBase)
	if apiBase == "" {
		return ""
	}
	u, err := url.Parse(apiBase)
	if err != nil || u.Host == "" {
		// Unparseable: report nothing rather than echo a string that might be
		// anything at all.
		return ""
	}
	return u.Host
}

// runProfilesList reports the configured profiles.
func runProfilesList(c *cli.Context) error {
	format, err := outputFormat(c)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(c.Path("config"))
	if err != nil {
		return err
	}

	selected := cfg.SelectProfile(c.String("profile"))

	entries := make([]output.Profile, 0, len(cfg.Profiles))
	for _, name := range cfg.ProfileNames() {
		p := cfg.Profiles[name]
		entries = append(entries, output.Profile{
			Name:          name,
			Description:   p.Description,
			Provider:      p.Provider,
			Model:         p.ProfileModelID(),
			Endpoint:      endpointHost(p.APIBase),
			CredentialEnv: p.APIKeyEnv,
			CredentialSet: p.APIKeyEnv != "" && os.Getenv(p.APIKeyEnv) != "",
			Disabled:      p.Disabled,
			Active:        name == selected,
			Default:       name == cfg.DefaultProfile,
		})
	}

	doc := output.NewProfiles(entries, cfg.DefaultProfile)
	if format == output.JSON {
		return output.WriteJSON(jsonWriter(), doc)
	}
	output.RenderProfilesText(reportWriter(), doc)
	return nil
}
