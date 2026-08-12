// Package main is the entry point for the joshbot CLI.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/muesli/termenv"
	"golang.org/x/term"

	"github.com/bigknoxy/joshbot/internal/agent"
	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/channels"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/configure"
	ctxpkg "github.com/bigknoxy/joshbot/internal/context"
	"github.com/bigknoxy/joshbot/internal/copilot"
	"github.com/bigknoxy/joshbot/internal/cron"
	"github.com/bigknoxy/joshbot/internal/heartbeat"
	"github.com/bigknoxy/joshbot/internal/learning"
	"github.com/bigknoxy/joshbot/internal/log"
	"github.com/bigknoxy/joshbot/internal/mcp"
	"github.com/bigknoxy/joshbot/internal/memory"
	"github.com/bigknoxy/joshbot/internal/output"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/redact"
	"github.com/bigknoxy/joshbot/internal/service"
	"github.com/bigknoxy/joshbot/internal/session"
	"github.com/bigknoxy/joshbot/internal/skills"
	"github.com/bigknoxy/joshbot/internal/subagent"
	"github.com/bigknoxy/joshbot/internal/tools"
	"github.com/charmbracelet/lipgloss"
	"github.com/urfave/cli/v2"
)

// Exit codes form joshbot's machine-readable contract for scripted and
// agentic consumers (issue #148). They are stable: 0 success, 1 general
// failure, 2 authentication/credential problem, 3 invalid usage/validation,
// 4 confirmation required (a destructive action needs --force or a prompt).
const (
	exitOK           = 0
	exitGeneral      = 1
	exitAuth         = 2
	exitValidation   = 3
	exitConfirmation = 4
)

// exitError wraps an error with a specific process exit code. main() unwraps
// it to choose the code; any other error maps to exitGeneral. It also carries
// an optional remediation string surfaced in JSON error output.
type exitError struct {
	code        int
	err         error
	remediation string
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// newExitError builds an exitError with a code and optional remediation.
func newExitError(code int, remediation string, err error) *exitError {
	return &exitError{code: code, err: err, remediation: remediation}
}

// exitErrorf is a convenience constructor for a formatted exitError with no
// remediation.
func exitErrorf(code int, format string, args ...any) *exitError {
	return &exitError{code: code, err: fmt.Errorf(format, args...)}
}

// codeForError maps an error to its process exit code, honouring an
// exitError anywhere in the chain and defaulting to exitGeneral.
func codeForError(err error) int {
	if err == nil {
		return exitOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return exitGeneral
}

// remediationForError returns the remediation hint from an exitError in the
// chain, or "" if none.
func remediationForError(err error) string {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.remediation
	}
	return ""
}

// runningContext describes how joshbot is running.
type runningContext struct {
	IsService bool
	IsDocker  bool
	IsGoRun   bool
}

// runningFromGoRun reports whether exePath is the throwaway binary `go run`
// builds, rather than an installed joshbot.
//
// It matches the go-build cache and nothing else. Three call sites used to
// also reject any path containing "/tmp/", which is not a property of `go run`:
// it made a joshbot installed anywhere under /tmp permanently unable to update
// or uninstall itself, and reported the cause as `go run`, which was neither
// true nor actionable.
func runningFromGoRun(exePath string) bool {
	return strings.Contains(exePath, "go-build")
}

// detectRunningContext determines how joshbot is currently running.
func detectRunningContext() runningContext {
	ctx := runningContext{}

	// Check for go run
	exePath, _ := osExecutable()
	if runningFromGoRun(exePath) {
		ctx.IsGoRun = true
		return ctx
	}

	// Check for Docker
	if _, err := os.Stat("/.dockerenv"); err == nil {
		ctx.IsDocker = true
	}

	// Check for service installation
	svc, err := newServiceManager(service.Config{Name: "joshbot"})
	if err == nil && svc.IsInstalled() {
		status, _ := svc.Status()
		if status.Running {
			ctx.IsService = true
		}
	}

	return ctx
}

// Version is set at build time via -ldflags.
var Version = "dev"

// toolExecutorAdapter wraps tools.Registry to satisfy the subagent.ToolExecutor
// interface, converting between tools.ToolResult/tools.AsyncResult and the
// subagent's local types to avoid an import cycle.
type toolExecutorAdapter struct {
	registry *tools.Registry
}

func (a *toolExecutorAdapter) GetSchemas() []providers.Tool {
	return a.registry.GetSchemas()
}

func (a *toolExecutorAdapter) ExecuteWithContext(ctx context.Context, name string, args map[string]any, channel, channelID string, callback func(subagent.AsyncResult)) (subagent.ToolResult, bool) {
	toolsResult, isAsync := a.registry.ExecuteWithContext(ctx, name, args, channel, channelID, func(ar tools.AsyncResult) {
		if callback != nil {
			callback(subagent.AsyncResult{
				ToolName: ar.ToolName,
				Args:     ar.Args,
				Output:   ar.Output,
				Error:    ar.Error,
				Metadata: ar.Metadata,
				Channel:  ar.Channel,
				ChatID:   ar.ChatID,
			})
		}
	})
	return subagent.ToolResult{
		Output: toolsResult.Output,
		Error:  toolsResult.Error,
	}, isAsync
}

func main() {
	// The sandbox helper is handled before the CLI framework starts.
	//
	// It re-execs this binary to confine a single shell command: the helper
	// process restricts itself with Landlock — which is irreversible and
	// inherited by everything it spawns — runs the command, and exits. Doing
	// that inside joshbot's own long-lived process would permanently sandbox
	// the agent. See internal/tools/sandbox_helper.go.
	//
	// It is intentionally not a registered command: it takes no user-facing
	// arguments, and setting up logging or config first would be wasted work
	// in a process that exists to run one command and die.
	if len(os.Args) > 1 && os.Args[1] == tools.SandboxHelperArg {
		code, err := tools.RunSandboxHelper(os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
		os.Exit(code)
	}

	if err := runApp(); err != nil {
		// A command that already emitted a machine-readable JSON error to
		// stderr sets jsonErrorEmitted so we don't also print a plain-text
		// "Error:" line and double-report. The exit code still applies.
		if !jsonErrorEmitted {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
		os.Exit(codeForError(err))
	}
}

// jsonErrorEmitted is set by the JSON output modes when they have already
// written a structured error to stderr, so main() suppresses the plain-text
// duplicate while still applying the exit code.
var jsonErrorEmitted bool

func runApp() error {
	// Setup global logger configuration
	loggerCfg := log.DefaultConfig()
	loggerCfg.Prefix = "joshbot"

	if err := log.Init(loggerCfg); err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}

	return newApp().Run(os.Args)
}

// newApp builds the CLI. It is separate from runApp so the command surface —
// every command's flags, actions and help text — can be inspected without
// initialising the logger or reading os.Args.
func newApp() *cli.App {
	app := &cli.App{
		Name:                 "joshbot",
		Version:              Version,
		Usage:                "A lightweight personal AI assistant with self-learning and long-term memory",
		EnableBashCompletion: true,
		Flags: []cli.Flag{
			&cli.PathFlag{
				Name:        "config",
				Usage:       "Path to config file",
				DefaultText: "~/.joshbot/config.json",
			},
			&cli.BoolFlag{
				Name:        "verbose",
				Aliases:     []string{"vv"},
				Usage:       "Enable verbose logging",
				Destination: new(bool),
			},
			&cli.BoolFlag{
				Name:        "debug",
				Usage:       "Enable debug logging (more detailed than verbose)",
				Destination: new(bool),
			},
			&cli.StringFlag{
				// Global, not per-command: a script wrapping joshbot sets it
				// once. Only the read-only reporting commands honour it today
				// (status, preflight, skills list, auth status,
				// configure --list); `agent` keeps its own --output-format,
				// which is a superset carrying stream-json as well.
				Name:  "output",
				Usage: "Output format for reporting commands: " + strings.Join(output.Formats, ", "),
				Value: string(output.Text),
			},
			&cli.BoolFlag{
				Name:  "no-color",
				Usage: "Disable ANSI colour in all output",
			},
			&cli.StringFlag{
				Name:  "log-level",
				Usage: "Log level: debug, info, warn, error (default info)",
			},
		},
		Commands: []*cli.Command{
			{
				Name:  "agent",
				Usage: "Start joshbot in interactive CLI mode",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "model",
						Usage: "Model to use (overrides config)",
					},
					profileFlag(),
					&cli.StringFlag{
						Name:  "output-format",
						Usage: "Output format: text, json, or stream-json (json modes are non-interactive and require --message)",
						Value: "text",
					},
					&cli.StringFlag{
						Name:    "resume",
						Aliases: []string{"session"},
						Usage:   "Resume a prior session by its id (from a previous json result)",
					},
					&cli.StringFlag{
						Name:    "message",
						Aliases: []string{"m"},
						Usage:   "Send a single message and exit (non-interactive mode)",
					},
					&cli.StringSliceFlag{
						Name:  "image",
						Usage: "Attach an image file to the message (repeatable; requires a vision-capable model)",
					},
					&cli.BoolFlag{
						Name:  "debug",
						Usage: "Enable debug logging",
					},
					&cli.IntFlag{
						Name:  "max-iterations",
						Usage: "Override the ReAct loop iteration limit (default: 50)",
						Value: 0, // 0 means use config default
					},
				},
				Action: runAgent,
			},
			{
				Name:  "gateway",
				Usage: "Start joshbot gateway (Telegram + all channels)",
				Flags: []cli.Flag{
					profileFlag(),
					&cli.BoolFlag{
						Name:  "debug",
						Usage: "Enable debug logging",
					},
				},
				Action: runGateway,
			},
			{
				Name:  "onboard",
				Usage: "First-time setup wizard",
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:  "force",
						Usage: "Start fresh without prompting (backs up existing)",
					},
					&cli.BoolFlag{
						Name:  "keep-data",
						Usage: "Reconfigure while preserving all existing data",
					},
					&cli.StringFlag{
						Name:    "model",
						Aliases: []string{"m"},
						Usage:   "Model to use (overrides config)",
					},
					&cli.StringFlag{
						Name:  "provider",
						Usage: "Provider to configure non-interactively (openrouter, openai, anthropic, poolside, nvidia, groq, ollama, azure, custom, litellm, github-copilot)",
					},
					&cli.StringFlag{
						Name:  "api-key",
						Usage: "API key for the provider (non-interactive; falls back to JOSHBOT_PROVIDERS__<PROVIDER>__API_KEY)",
					},
					&cli.StringFlag{
						Name:  "api-base",
						Usage: "API base URL for the provider (required for azure/custom)",
					},
				},
				Action: runOnboard,
			},
			{
				Name:   "status",
				Usage:  "Show configuration and status",
				Action: withJSONErrors(runStatus),
			},
			{
				Name:  "preflight",
				Usage: "Check the config would actually work, without calling any provider",
				Description: "Resolves the config the way the agent does and reports what would be used:\n" +
					"provider, the exact model ID sent on the wire, the API host, and whether a\n" +
					"credential is present and where it came from. Never prints the credential and\n" +
					"never contacts a provider. Exits non-zero when joshbot would not start.",
				Flags:  []cli.Flag{profileFlag()},
				Action: withJSONErrors(runPreflight),
			},
			mcpCommand(),
			profilesCommand(),
			{
				Name:  "skills",
				Usage: "Review and approve workspace skills",
				Description: "Workspace skills become part of the agent's instructions, so they are\n" +
					"inert until you approve them. Approval is bound to the file's contents:\n" +
					"editing an approved skill revokes it until you approve it again.",
				Subcommands: []*cli.Command{
					{
						Name:   "list",
						Usage:  "List skills and whether they are approved",
						Action: withJSONErrors(runSkillsList),
					},
					{
						Name:      "trust",
						Usage:     "Approve a skill after reviewing it (use --all to approve every pending skill)",
						ArgsUsage: "[skill name]",
						Flags: []cli.Flag{
							&cli.BoolFlag{
								Name:  "all",
								Usage: "Approve every skill currently awaiting review",
							},
						},
						Action: runSkillsTrust,
					},
					{
						Name:      "untrust",
						Usage:     "Revoke approval for a skill",
						ArgsUsage: "<skill name>",
						Action:    runSkillsUntrust,
					},
				},
			},
			sessionsCommand(),
			{
				Name:    "configure",
				Aliases: []string{"config"},
				Usage:   "Configure LLM providers and settings",
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:  "list",
						Usage: "List configured providers",
					},
					&cli.StringFlag{
						Name:  "provider",
						Usage: "Provider to configure (openrouter, openai, anthropic, poolside, nvidia, groq, ollama, azure, custom, litellm, github-copilot)",
					},
					&cli.StringFlag{
						Name:  "api-key",
						Usage: "API key for the provider",
					},
					&cli.StringFlag{
						Name:  "api-base",
						Usage: "API base URL for the provider",
					},
					&cli.StringFlag{
						Name:  "model",
						Usage: "Model to use for the provider",
					},
					&cli.StringFlag{
						Name:  "set-default",
						Usage: "Set a provider as the default",
					},
					&cli.StringFlag{
						Name:  "remove",
						Usage: "Remove a configured provider",
					},
				},
				Action: withJSONErrors(runConfigure),
			},
			{
				Name:   "update",
				Usage:  "Update joshbot to the latest version",
				Action: runUpdate,
			},
			{
				Name:   "version",
				Usage:  "Show joshbot version",
				Action: runVersion,
			},
			{
				Name:  "uninstall",
				Usage: "Uninstall joshbot and optionally remove configuration",
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:  "force",
						Usage: "Skip confirmation prompts",
					},
					&cli.BoolFlag{
						Name:  "keep-config",
						Usage: "Keep configuration directory",
					},
				},
				Action: runUninstall,
			},
			{
				Name:  "service",
				Usage: "Manage joshbot as a system service",
				Subcommands: []*cli.Command{
					{
						Name:   "install",
						Usage:  "Install joshbot as a system service",
						Action: runServiceInstall,
					},
					{
						Name:   "uninstall",
						Usage:  "Uninstall the joshbot system service",
						Action: runServiceUninstall,
					},
					{
						Name:   "status",
						Usage:  "Check joshbot service status",
						Action: runServiceStatus,
					},
				},
			},
			{
				Name:  "auth",
				Usage: "Manage OAuth authentication for providers",
				Subcommands: []*cli.Command{
					{
						Name:   "github-copilot",
						Usage:  "Authenticate with GitHub Copilot",
						Action: runAuthCopilot,
						Flags: []cli.Flag{
							&cli.BoolFlag{
								Name:  "force",
								Usage: "Re-authenticate even if a token is already stored",
							},
						},
					},
					{
						Name:   "status",
						Usage:  "Show authentication status for all providers",
						Action: withJSONErrors(runAuthStatus),
					},
				},
			},
		},
		Before: func(c *cli.Context) error {
			// Explicit --log-level wins; --verbose/--debug are shorthands for
			// debug. Precedence: flags > env > project config > user config.
			if lvl := c.String("log-level"); lvl != "" {
				parsed, err := parseLogLevel(lvl)
				if err != nil {
					return newExitError(exitValidation, "valid levels: debug, info, warn, error", err)
				}
				log.SetLevel(parsed)
			} else if c.Bool("verbose") || c.Bool("debug") {
				log.SetLevel(log.DebugLevel)
			}
			if c.Bool("no-color") {
				applyNoColor()
			}
			return nil
		},
	}

	return app
}

// explicitConfigPath reports the path the user actually chose with --config,
// or "" when they left the flag alone.
//
// "~/.joshbot/config.json" is compared literally because that is the flag's
// DefaultText: an untilded string the shell never expands, so it means "the
// user did not choose a path" rather than an actual location.
func explicitConfigPath(cfgPath string) string {
	if cfgPath == "" || cfgPath == "~/.joshbot/config.json" {
		return ""
	}
	return cfgPath
}

// loadConfig loads configuration from file or environment.
func loadConfig(cfgPath string) (*config.Config, error) {
	var cfg *config.Config
	var err error

	// An explicit --config names a file, and everything derived from the home
	// follows it. The previous version used only the directory, discarding the
	// file name, and restored the global afterwards — so joshbot read one file
	// and wrote another. See internal/config/path.go.
	//
	// "~/.joshbot/config.json" is compared literally because that is the flag's
	// DefaultText: an untilded string the shell never expands, so it means "the
	// user did not choose a path" rather than an actual location.
	if explicitConfigPath(cfgPath) != "" {
		cfg, err = config.LoadFrom(cfgPath)
	} else {
		cfg, err = config.Load()
	}

	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	return cfg, nil
}

// setupComponents initializes all required components.
// registerProviders populates mp from cfg: the model-centric list when one is
// configured, the legacy provider map otherwise.
//
// It is a function rather than inline in setupComponents because the configure
// tool's hot reload has to do exactly the same thing. That reload used to carry
// its own copy which reconstructed only openrouter and nvidia, so a config
// change made through the agent silently dropped groq, poolside, ollama,
// github-copilot and custom from the running process until the next restart —
// with no error anywhere, because Clear() had already removed them.
func registerProviders(cfg *config.Config, multiProvider *providers.MultiProvider) error {
	// Check if using new model-centric config
	if cfg.UseModelsConfig() {
		// Use new model-centric configuration
		log.Info("Using model-centric configuration")

		resolvedModels := cfg.GetAllModelConfigs()
		for i, resolved := range resolvedModels {
			llmProvider := providers.NewProviderFromResolvedModel(resolved, &providers.DefaultLogger{})
			var provider providers.Provider = llmProvider
			if len(resolved.APIKeys) > 1 {
				pool := providers.NewAPIKeyPool(resolved.APIKeys, 24*time.Hour, 3)
				provider = providers.NewKeyRotatingProvider(llmProvider, pool)
				log.Info("Wrapped provider with key rotation", "name", resolved.Name, "keys", len(resolved.APIKeys))
			}
			multiProvider.Register(resolved.Name, provider, resolved.ModelID, i, true)
			log.Info("Registered model", "name", resolved.Name, "model", resolved.ModelID, "provider", resolved.Provider, "api_key_len", len(resolved.APIKey))
		}

		if len(resolvedModels) == 0 {
			return fmt.Errorf("no models configured")
		}
	} else {
		// Use legacy provider configuration
		if cfg.ProviderDefaults.Default == "" {
			multiProvider.SetDefault("openrouter")
		}

		// Register OpenRouter (if configured and enabled)
		if p, ok := cfg.Providers["openrouter"]; ok && p.APIKey != "" && p.Enabled {
			openrouterProvider, err := providers.GetProvider("openrouter", providers.Config{
				APIKey:       p.APIKey,
				APIBase:      p.APIBase,
				ExtraHeaders: p.ExtraHeaders,
				Model:        cfg.Agents.Defaults.Model,
				MaxTokens:    cfg.Agents.Defaults.MaxTokens,
				Temperature:  cfg.Agents.Defaults.Temperature,
			})
			if err != nil {
				log.Warn("Failed to create OpenRouter provider", "error", err)
			} else {
				multiProvider.Register("openrouter", openrouterProvider, cfg.Agents.Defaults.Model, 0, p.Enabled)
			}
		}

		// Register NVIDIA NIM (if configured) - first fallback
		if p, ok := cfg.Providers["nvidia"]; ok && p.APIKey != "" && p.Enabled {
			nvidiaProvider, err := providers.GetProvider("nvidia", providers.Config{
				APIKey:       p.APIKey,
				APIBase:      p.APIBase,
				ExtraHeaders: p.ExtraHeaders,
				Model:        p.Model,
			})
			if err != nil {
				log.Warn("Failed to create NVIDIA provider", "error", err)
			} else {
				priority := 1
				if idx := indexOf(cfg.ProviderDefaults.FallbackOrder, "nvidia"); idx >= 0 {
					priority = idx + 1
				}
				model := p.Model
				if model == "" {
					model = cfg.Agents.Defaults.Model
				}
				multiProvider.Register("nvidia", nvidiaProvider, model, priority, p.Enabled)
			}
		}

		// Register Groq (if configured)
		if p, ok := cfg.Providers["groq"]; ok && p.APIKey != "" && p.Enabled {
			groqProvider, err := providers.GetProvider("groq", providers.Config{
				APIKey:       p.APIKey,
				APIBase:      p.APIBase,
				ExtraHeaders: p.ExtraHeaders,
			})
			if err != nil {
				log.Warn("Failed to create Groq provider", "error", err)
			} else {
				priority := len(cfg.ProviderDefaults.FallbackOrder) + 1
				if idx := indexOf(cfg.ProviderDefaults.FallbackOrder, "groq"); idx >= 0 {
					priority = idx + 1
				}
				multiProvider.Register("groq", groqProvider, "", priority, p.Enabled)
			}
		}

		// Register Poolside (if configured)
		if p, ok := cfg.Providers["poolside"]; ok && p.APIKey != "" && p.Enabled {
			poolsideProvider, err := providers.GetProvider("poolside", providers.Config{
				APIKey:       p.APIKey,
				APIBase:      p.APIBase,
				ExtraHeaders: p.ExtraHeaders,
				Model:        p.Model,
			})
			if err != nil {
				log.Warn("Failed to create Poolside provider", "error", err)
			} else {
				priority := len(cfg.ProviderDefaults.FallbackOrder) + 1
				if idx := indexOf(cfg.ProviderDefaults.FallbackOrder, "poolside"); idx >= 0 {
					priority = idx + 1
				}
				model := p.Model
				if model == "" {
					model = providers.GetDefaultModel("poolside")
				}
				multiProvider.Register("poolside", poolsideProvider, model, priority, p.Enabled)
			}
		}

		// Register Ollama (if configured)
		if p, ok := cfg.Providers["ollama"]; ok && p.Enabled {
			apiBase := p.APIBase
			if apiBase == "" {
				apiBase = providers.GetDefaultAPIBase()
			}
			timeout := p.Timeout
			if timeout == 0 {
				timeout = 300 * time.Second
			}
			ollamaProvider, err := providers.GetProvider("ollama", providers.Config{
				APIBase:      apiBase,
				ExtraHeaders: p.ExtraHeaders,
				Timeout:      timeout,
				Model:        p.Model,
			})
			if err != nil {
				log.Warn("Failed to create Ollama provider", "error", err)
			} else {
				priority := len(cfg.ProviderDefaults.FallbackOrder) + 1
				if idx := indexOf(cfg.ProviderDefaults.FallbackOrder, "ollama"); idx >= 0 {
					priority = idx + 1
				}
				model := p.Model
				if model == "" {
					model = providers.GetDefaultModel("ollama")
				}
				multiProvider.Register("ollama", ollamaProvider, model, priority, p.Enabled)
			}
		}

		// Register GitHub Copilot (if configured)
		if p, ok := cfg.Providers["github-copilot"]; ok && p.Enabled {
			homeDir, _ := copilot.GetHomeDir()
			token, err := copilot.LoadToken(homeDir)
			if err != nil || token == nil || token.AccessToken == "" {
				log.Warn("GitHub Copilot not authenticated", "error", err)
				log.Warn("Run: joshbot auth github-copilot")
			} else {
				copilotCfg := providers.Config{
					APIKey:      token.AccessToken,
					Model:       cfg.Agents.Defaults.Model,
					MaxTokens:   cfg.Agents.Defaults.MaxTokens,
					Temperature: cfg.Agents.Defaults.Temperature,
				}
				if p.Model != "" {
					copilotCfg.Model = p.Model
				}
				if copilotCfg.Model == "" {
					copilotCfg.Model = "gpt-4o"
				}
				copilotProvider, err := providers.GetProvider("github-copilot", copilotCfg)
				if err != nil {
					log.Warn("Failed to create GitHub Copilot provider", "error", err)
				} else {
					priority := len(cfg.ProviderDefaults.FallbackOrder) + 1
					for i, name := range cfg.ProviderDefaults.FallbackOrder {
						if name == "github-copilot" {
							priority = i + 1
							break
						}
					}
					multiProvider.Register("github-copilot", copilotProvider, copilotCfg.Model, priority, p.Enabled)
					log.Info("Registered provider", "name", "github-copilot", "priority", priority)
				}
			}
		}

		// Register Custom OpenAI-compatible (if configured)
		if p, ok := cfg.Providers["custom"]; ok && p.APIKey != "" && p.Enabled {
			customProvider, err := providers.GetProvider("custom", providers.Config{
				APIKey:       p.APIKey,
				APIBase:      p.APIBase,
				ExtraHeaders: p.ExtraHeaders,
				Model:        p.Model,
			})
			if err != nil {
				log.Warn("Failed to create custom provider", "error", err)
			} else {
				priority := len(cfg.ProviderDefaults.FallbackOrder) + 1
				if idx := indexOf(cfg.ProviderDefaults.FallbackOrder, "custom"); idx >= 0 {
					priority = idx + 1
				}
				model := p.Model
				if model == "" {
					model = p.Model
				}
				multiProvider.Register("custom", customProvider, model, priority, p.Enabled)
				log.Info("Registered custom provider", "api_base", p.APIBase)
			}
		}

		// Fail fast if the legacy provider map produced zero usable providers,
		// rather than deferring to an opaque "no providers configured" error
		// the first time Chat() is called. Distinguishes an empty config from
		// providers present but none enabled (issue #71).
		if len(multiProvider.GetProviderNames()) == 0 {
			return noProvidersRegisteredError(cfg.Providers)
		}
	}
	return nil
}

func setupComponents(cfg *config.Config) (*bus.MessageBus, providers.Provider, *session.Manager, *agent.Agent, *tools.Registry, *tools.BusMessageSender, error) {
	// Ensure directories exist
	if err := cfg.EnsureDirs(); err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("failed to create directories: %w", err)
	}

	// Initialize memory manager
	memoryManager, err := memory.New(cfg.Agents.Defaults.Workspace, memory.WithMaxSize(cfg.Agents.Defaults.MaxMemorySize))
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("failed to init memory manager: %w", err)
	}
	if err := memoryManager.Initialize(context.Background()); err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("failed to initialize memory files: %w", err)
	}

	// Initialize skills loader
	skillsLoader, err := skills.NewLoader(cfg.Agents.Defaults.Workspace)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("failed to init skills loader: %w", err)
	}
	// Workspace skills become part of the agent's standing instructions, so
	// they are gated on operator approval. See internal/skills/trust.go.
	skillsTrust, err := skills.LoadTrustStore(skills.DefaultTrustStorePath(config.DefaultHome))
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("failed to load skills trust store: %w", err)
	}
	skillsLoader.SetTrustStore(skillsTrust)

	// Discover skills now so agent has summaries available
	_ = skillsLoader.Discover()

	// Withheld skills must be announced. An operator who upgraded and found
	// their skills quietly stopped working would reasonably call that a bug.
	if pending := skillsLoader.Untrusted(); len(pending) > 0 {
		names := make([]string, 0, len(pending))
		for _, sk := range pending {
			names = append(names, sk.Name)
		}
		log.Warn("Skills are awaiting review and are not in use",
			"skills", strings.Join(names, ", "),
			"approve_with", "joshbot skills trust <name>  (or --all)")
	}

	// Initialize message bus
	msgBus := bus.NewMessageBus()

	// Create BusMessageSender for tools that need to send messages
	messageSender := tools.NewBusMessageSender(msgBus)

	// Get logger
	logger := log.Get()

	// Create MultiProvider
	multiProvider := providers.NewMultiProvider(providers.MultiProviderConfig{
		DefaultProvider: cfg.ProviderDefaults.Default,
		Logger:          &providers.DefaultLogger{},
	})

	if err := registerProviders(cfg, multiProvider); err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}

	// Initialize session manager
	sessionMgr, err := session.NewManager(cfg.SessionsDir())
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("failed to create session manager: %w", err)
	}

	// Build context budgeting/compression components
	registry := ctxpkg.NewRegistry()
	budget := ctxpkg.NewBudgetManager(registry, 100)
	compressor := &ctxpkg.Compressor{Provider: multiProvider}

	// Resolve the shell sandbox setting. A value we do not recognise is an
	// error rather than a silent fallback to "off": an operator who typed it
	// wrong would otherwise believe commands were contained when they were not.
	sandboxMode, ok := tools.ParseSandboxMode(cfg.Tools.ShellSandbox)
	if !ok {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf(
			"tools.shell_sandbox has unknown value %q; use \"off\" or \"workspace\"", cfg.Tools.ShellSandbox)
	}
	if sandboxMode != tools.SandboxOff {
		if !tools.SandboxAvailable() {
			return nil, nil, nil, nil, nil, nil, fmt.Errorf(
				"tools.shell_sandbox is %q but %s; set it to \"off\" to run without containment",
				sandboxMode, tools.SandboxDescription())
		}
		if !tools.SandboxSupported() {
			return nil, nil, nil, nil, nil, nil, fmt.Errorf(
				"tools.shell_sandbox is %q but the running kernel does not provide %s; "+
					"set it to \"off\" to run without containment", sandboxMode, tools.SandboxDescription())
		}
		log.Info("Shell sandbox enabled", "mode", sandboxMode,
			"mechanism", tools.SandboxDescription(), "network", cfg.Tools.ShellSandboxAllowNetwork)
	}

	// Resolve the shell approval gate. Same rule as the sandbox: an
	// unrecognised value is a startup error, because an operator who typed
	// "interactve" would otherwise believe every command was being confirmed
	// while none of them were.
	approvalMode, ok := tools.ParseApprovalMode(cfg.Tools.ShellApproval)
	if !ok {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf(
			"tools.shell_approval has unknown value %q; use \"off\", \"interactive\" or \"always\"",
			cfg.Tools.ShellApproval)
	}
	if approvalMode != tools.ApprovalOff {
		log.Info("Shell approval gate enabled", "mode", approvalMode)
	}
	// Read by runAgentLoop, which installs the terminal approver. Nothing
	// else installs one, so every non-interactive entry point — the gateway,
	// cron, the heartbeat — leaves the request with no approver and the gate
	// denies rather than blocks.
	shellApprovalMode = approvalMode

	// Create tools registry with defaults
	// The cron service is built before the registry so the cron tool can be
	// registered against it. It is started further down with the other
	// background services.
	cronSvc := cron.NewService(msgBus, cfg.Agents.Defaults.Workspace)

	toolsRegistry := tools.RegistryWithDefaults(
		cfg.Agents.Defaults.Workspace,
		cfg.Tools.RestrictToWorkspace,
		cfg.Tools.Exec.Timeout,
		0, // webTimeout - not configurable in config yet
		messageSender,
		cfg.Tools.ShellAllowList,
		cfg.Tools.FilesystemAllowedPaths,
		skillsLoader,
		tools.WithShellSandbox(sandboxMode, cfg.Tools.ShellSandboxAllowNetwork),
		tools.WithShellApproval(approvalMode),
		tools.WithCronService(cronSvc, defaultReminderChannel(cfg)),
	)

	// Connect any configured MCP servers and register their tools. Fail-soft by
	// design: a server that will not start is logged and skipped, never a
	// startup abort — MCP is additive and must not be able to break the agent.
	// The spawned processes are owned by a package-level manager reaped by
	// closeMCPServers, which every long-lived entry point defers.
	registerMCPServers(context.Background(), toolsRegistry, cfg.MCP)

	// Create function to reload providers from config (for config tool hot-reload)
	reloadProviders := func() error {
		multiProvider.Clear()
		return registerProviders(cfg, multiProvider)
	}

	// Register config tool
	toolsRegistry.Register(tools.NewConfigureTool(cfg, reloadProviders))

	// Create async callback channel and start processor for background task notifications
	asyncCallbackCh := make(chan tools.AsyncResult, 100)
	toolsRegistry.SetAsyncCallback(asyncCallbackCh)
	toolsRegistry.Register(tools.NewMemorySearchTool(memoryManager))

	// Create subagent runner for parallel and chain execution tools
	agentModel := cfg.Agents.Defaults.Model
	if cfg.UseModelsConfig() {
		agentModel = cfg.ModelsConfig.Agent.Model
	}
	subagentRunner := subagent.NewRunner(multiProvider, agentModel,
		subagent.WithMaxTokens(4096),
		subagent.WithTemperature(0.3),
		subagent.WithTimeout(60*time.Second),
		subagent.WithTools(&toolExecutorAdapter{registry: toolsRegistry}),
	)
	toolsRegistry.Register(tools.NewParallelSubagentTool(subagentRunner))
	toolsRegistry.Register(tools.NewChainExecutionTool(subagentRunner))

	// Create subagent config manager for agent profile discovery
	agentConfigDir := filepath.Join(config.DefaultHome, "agents")
	if err := os.MkdirAll(agentConfigDir, 0750); err == nil {
		agentCfgMgr, cfgErr := tools.NewSubagentConfigManager(agentConfigDir)
		if cfgErr == nil {
			if discErr := agentCfgMgr.Discover(); discErr != nil {
				log.Warn("Subagent config discovery failed", "error", discErr)
			}
			toolsRegistry.Register(tools.NewSubagentConfigTool(agentCfgMgr))
		}
	}

	go publishAsyncResults(asyncCallbackCh, msgBus)

	// Create skill self-creation components (Milestone 2)
	skillDetector := skills.NewSkillDetector()
	skillExtractor := skills.NewExtractor(multiProvider, agentModel)

	// Enable async support in the registry
	toolsRegistry.SetAsyncCallback(asyncCallbackCh)
	agentInstance := agent.NewAgent(
		cfg,
		multiProvider,
		toolsRegistry,
		sessionMgr,
		logger,
		agent.WithMemoryLoader(memoryManager),
		agent.WithHistoryAppender(memoryManager),
		agent.WithSkillsLoader(skillsLoader),
		agent.WithSkillDetector(skillDetector),
		agent.WithExtractor(skillExtractor),
		agent.WithSkillLoader(skillsLoader),
		agent.WithBudgetManager(budget),
		agent.WithCompressor(compressor),
	)

	// Start background services (best-effort)
	cronSvc.Start()
	hb := heartbeat.NewService(msgBus, cfg.Agents.Defaults.Workspace)
	hb.SetInterval(cfg.HeartbeatInterval())
	// Route heartbeat tasks to the same channel scheduled reminders use, and
	// resolve that channel's stored chat ID so results reach a real recipient
	// instead of failing with "no valid recipient".
	hb.SetChannel(defaultReminderChannel(cfg))
	hb.SetChatIDResolver(messageSender.GetChatID)
	hb.Start()

	// Start consolidator (self-learning memory consolidation)
	consolidator := learning.NewConsolidator(memoryManager, multiProvider, 10*time.Minute)
	consolidator.Start()

	logger.Info("Background services started", "cron_jobs_file", cfg.Agents.Defaults.Workspace)

	return msgBus, multiProvider, sessionMgr, agentInstance, toolsRegistry, messageSender, nil
}

// mcpMu guards mcpManager, which holds the MCP server processes spawned during
// setup. It is a package var rather than a seventh return value from
// setupComponents because the manager is process-scoped: exactly one setup runs
// per invocation, and the only thing a caller ever does with it is reap it on
// the way out.
var (
	mcpMu      sync.Mutex
	mcpManager *mcp.Manager
)

// registerMCPServers connects the enabled MCP servers and registers their tools
// on reg. It never returns an error: MCP is additive, so a server that fails to
// start is logged and skipped inside RegisterMCPTools rather than aborting
// startup. The resulting manager is stashed for closeMCPServers.
// A server whose advertised tool list has not been approved contributes no
// tools; a trust store that cannot be read is fatal to MCP registration and
// nothing else, because the alternative — carrying on with a nil store — would
// silently disable every server with no explanation an operator could act on.
// asyncMaxOutput caps how much of a background task's output is repeated back
// to the user. Telegram hard-fails over 4096 bytes and the notification is
// unsolicited, so a long build log has to be cut rather than sent whole.
const asyncMaxOutput = 2000

// asyncResultMessage renders the notification for a finished background task.
// A failed task must say so — an error rendered as a success line is a task the
// user believes completed — and an over-long output must be visibly truncated
// rather than silently cut, or the user reads a half-sentence as the whole
// answer.
func asyncResultMessage(result tools.AsyncResult) string {
	if result.Error != nil {
		return fmt.Sprintf("❌ Background task failed (%s): %v", result.ToolName, result.Error)
	}
	output := result.Output
	if len(output) > asyncMaxOutput {
		output = output[:asyncMaxOutput] + "... (truncated)"
	}
	return fmt.Sprintf("✅ Background task completed (%s):\n%s", result.ToolName, output)
}

// publishAsyncResults forwards finished background tasks to the bus until the
// channel closes. The result carries the channel and chat it belongs to, and it
// has to be routed back to that one: a notification published against the wrong
// chat is delivered to a user who never started the task.
func publishAsyncResults(ch <-chan tools.AsyncResult, msgBus *bus.MessageBus) {
	for result := range ch {
		msgBus.Publish(bus.OutboundMessage{
			Channel:   result.Channel,
			ChannelID: result.ChatID,
			Content:   asyncResultMessage(result),
		})
	}
}

func registerMCPServers(ctx context.Context, reg *tools.Registry, cfg config.MCPConfig) {
	trust, err := mcp.LoadTrustStore(mcp.DefaultTrustStorePath(config.DefaultHome))
	if err != nil {
		log.Warn("mcp: could not read the approval store, no MCP tools will be used", "error", err)
		return
	}
	mgr := tools.RegisterMCPTools(ctx, reg, cfg, trust)
	if mgr == nil {
		return
	}
	mcpMu.Lock()
	prev := mcpManager
	mcpManager = mgr
	mcpMu.Unlock()
	if prev != nil {
		prev.Close()
	}
}

// closeMCPServers reaps every spawned MCP server process. Safe to call when no
// servers were configured, and safe to call twice.
func closeMCPServers() {
	mcpMu.Lock()
	mgr := mcpManager
	mcpManager = nil
	mcpMu.Unlock()
	if mgr != nil {
		mgr.Close()
	}
}

// defaultReminderChannel picks where a scheduled reminder goes when the agent
// does not name a channel. A reminder delivered to a CLI session nobody is
// sitting at is lost, so a configured Telegram channel wins.
func defaultReminderChannel(cfg *config.Config) string {
	if cfg != nil && cfg.Channels.Telegram.Enabled {
		return "telegram"
	}
	if cfg != nil && cfg.Channels.Discord.Enabled {
		return "discord"
	}
	return "cli"
}

// indexOf returns the index of needle in haystack, or -1 if not found.
func indexOf(haystack []string, needle string) int {
	for i, s := range haystack {
		if s == needle {
			return i
		}
	}
	return -1
}

// setupGracefulShutdown sets up signal handling for graceful shutdown.
func setupGracefulShutdown(ctx context.Context, cancel context.CancelFunc, done chan<- struct{}) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	go func() {
		first := true
		for sig := range sigChan {
			switch sig {
			case syscall.SIGHUP:
				log.Warn("Received SIGHUP signal, gracefully restarting...", "signal", sig)
				continue
			}

			if first {
				log.Warn("Received signal, shutting down...", "signal", sig)
				first = false
				cancel()
				close(done)
				continue
			}

			// signal.Notify disables Go's default termination for every signal
			// it registers, so if this goroutine handled only one the process
			// would become unkillable by anything short of SIGKILL. A second
			// signal means the operator asked twice: leave immediately.
			log.Warn("Received second signal, exiting immediately", "signal", sig)
			os.Exit(130)
		}
	}()
}

// runAgent executes the agent (interactive CLI) mode.
// applyMaxIterationsOverride applies the --max-iterations CLI override to the
// agent. It is nil-safe: runAgent calls it only after setupComponents has
// succeeded, but a nil agent must not panic — a setup failure is reported as
// an error, not a crash.
func applyMaxIterationsOverride(agentInstance *agent.Agent, maxIter int) {
	if agentInstance == nil {
		log.Warn("Cannot apply --max-iterations: agent not initialized")
		return
	}
	agentInstance.SetMaxIterations(maxIter)
	log.Info("Overriding max iterations from CLI", "max_iterations", maxIter)
}

func runAgent(c *cli.Context) error {
	cfg, err := loadConfig(c.Path("config"))
	if err != nil {
		return err
	}
	// Before any component is built, so a bad --profile is a startup error.
	if err := applyProfile(c, cfg); err != nil {
		return err
	}

	// Validate --output-format before doing any work so a typo fails fast
	// with a validation exit code rather than after model setup.
	format := c.String("output-format")
	switch format {
	case "", "text":
		format = "text"
	case "json", "stream-json":
	default:
		return newExitError(exitValidation, "use one of: text, json, stream-json",
			fmt.Errorf("invalid --output-format %q", format))
	}
	jsonMode := format != "text"

	// HARD RULE: in JSON modes stdout carries data only. Route every log line
	// (including setup diagnostics below) to stderr. Independent of TTY
	// detection — the flag alone selects it.
	if jsonMode {
		log.Get().Logger.SetOutput(os.Stderr)
		if c.String("message") == "" {
			// Every other JSON-mode failure writes a {"type":"error",...}
			// document to stderr. Returning bare here left a wrapper with a
			// non-zero exit and an empty error channel, indistinguishable
			// from a crash, and inconsistent with the sibling --image path
			// two blocks down (issue #220).
			err := newExitError(exitValidation, "json output modes are non-interactive; pass -m/--message",
				fmt.Errorf("--output-format %s requires --message", format))
			emitJSONError(os.Stderr, "", err)
			return err
		}
	}

	// Check for either legacy providers or new model-centric config
	if !cfg.UseModelsConfig() && len(cfg.Providers) == 0 {
		err := newExitError(exitAuth, "run 'joshbot onboard' to configure a provider",
			fmt.Errorf("no providers configured"))
		if jsonMode {
			emitJSONError(os.Stderr, "", err)
		}
		return err
	}

	// Override model from CLI flag if provided (works for both config formats)
	if modelFlag := c.String("model"); modelFlag != "" {
		if cfg.UseModelsConfig() {
			// Check if the flag is a model name or model ID
			if _, ok := cfg.GetModel(modelFlag); ok {
				cfg.ModelsConfig.Agent.Model = modelFlag
			} else {
				// Try to find by model ID
				for _, m := range cfg.ModelsConfig.Models {
					if m.Model == modelFlag || config.StripProviderPrefix(m.Model) == modelFlag {
						cfg.ModelsConfig.Agent.Model = m.Name
						break
					}
				}
			}
		}
		cfg.Agents.Defaults.Model = modelFlag
	}

	// Get model name for logging
	modelName := cfg.Agents.Defaults.Model
	if cfg.UseModelsConfig() {
		modelName = cfg.ModelsConfig.Agent.Model
	}

	log.Info("Starting agent mode", "model", modelName)

	// Setup components
	_, _, _, agentInstance, toolsRegistry, messageSender, err := setupComponents(cfg)
	defer closeMCPServers()
	if err != nil {
		// HARD RULE: in JSON modes a setup failure must still be well-formed —
		// a machine-readable error on stderr, not a plain-text line.
		if jsonMode {
			emitJSONError(os.Stderr, "", err)
		}
		return err
	}

	// Apply --max-iterations CLI override if provided (0 means use config
	// default). This runs only after setupComponents succeeded, so
	// agentInstance is guaranteed non-nil.
	if maxIter := c.Int("max-iterations"); maxIter > 0 {
		applyMaxIterationsOverride(agentInstance, maxIter)
	}

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Start async callback printer for CLI mode. In JSON modes stdout is
	// reserved for data, so background-task notices go to stderr instead.
	asyncOut := io.Writer(os.Stdout)
	if jsonMode {
		asyncOut = os.Stderr
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case result := <-toolsRegistry.GetAsyncCallbackChannel():
				var msg string
				if result.Error != nil {
					msg = fmt.Sprintf("\n❌ Background task failed (%s): %v\n> ", result.ToolName, result.Error)
				} else {
					output := result.Output
					if len(output) > 500 {
						output = output[:500] + "... (truncated)"
					}
					msg = fmt.Sprintf("\n✅ Background task completed (%s):\n%s\n> ", result.ToolName, output)
				}
				fmt.Fprint(asyncOut, msg)
			}
		}
	}()

	// Attachments are read before the turn starts so a bad path fails fast,
	// naming the path, instead of surfacing mid-conversation.
	images, imgErr := loadImageFlags(c.StringSlice("image"))
	if imgErr != nil {
		err := newExitError(exitValidation, "check the --image paths", imgErr)
		if jsonMode {
			emitJSONError(os.Stderr, "", err)
		}
		return err
	}
	if len(images) > 0 && c.String("message") == "" {
		err := newExitError(exitValidation, "pass -m/--message with --image",
			fmt.Errorf("--image requires --message"))
		if jsonMode {
			emitJSONError(os.Stderr, "", err)
		}
		return err
	}

	// Non-interactive JSON modes: machine-readable single turn.
	if jsonMode {
		err := runAgentJSON(ctx, agentInstance, c.String("message"), format, c.String("resume"), os.Stdout, os.Stderr, messageSender, images...)
		time.Sleep(2 * time.Second) // let async callbacks drain to stderr
		return err
	}

	// Non-interactive text mode: send single message and exit.
	if message := c.String("message"); message != "" {
		err := runAgentSingleMessage(ctx, agentInstance, message, c.String("resume"), os.Stdout, messageSender, images...)
		// Wait a bit for async callbacks
		time.Sleep(2 * time.Second)
		return err
	}

	done := make(chan struct{})
	setupGracefulShutdown(ctx, cancel, done)

	if err := runAgentLoop(ctx, cancel, done, os.Stdin, os.Stdout, agentInstance, messageSender, cfg.Agents.Defaults.Streaming); err != nil {
		return err
	}
	return nil
}

type agentProcessor interface {
	Process(context.Context, bus.InboundMessage) (string, error)
}

// progressCapable is implemented by test mocks (e.g. mockProgressAgent in
// main_test.go) that still use the old SetProgressCallback wiring. The real
// *agent.Agent no longer implements this — it receives its sink per-request
// via the context (agent.WithSink), which is concurrency-safe. The type
// assertion is checked so that mocks continue to work unmodified while the
// real Agent gets its sink through the context path below.
type progressCapable interface {
	SetProgressCallback(agent.ProgressFunc)
}

// modelReporter is implemented by the real *agent.Agent so the TUI line editor
// can show the session's current model in the prompt. It is checked as an
// optional capability, so mocks without CurrentModel still work.
type modelReporter interface {
	CurrentModel() string
}

// cliCommandNames are the slash commands offered by the TUI editor's Tab
// completion. They mirror what the agent's /help lists for CLI sessions plus
// the CLI-only commands the buffered prompt still supports (/clear, /history).
var cliCommandNames = []string{"start", "new", "status", "model", "personality", "compact", "help", "clear", "history", "exit"}

// isTTY reports whether w is connected to an interactive terminal. It is a
// variable (not a plain function) so tests can inject deterministic
// TTY-ness instead of depending on whatever terminal (or lack of one) the
// test process happens to run under.
//
// In production, only *os.File can be a terminal; io.Writer implementations
// used by tests (bytes.Buffer, io.Discard, custom mocks) never satisfy the
// *os.File type assertion and so are correctly treated as non-TTY without
// any special-casing.
var isTTY = func(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}

// cliProgress renders tool-call visibility lines and a spinner/elapsed-time
// indicator for the interactive CLI loop. All output goes through a single
// mutex so the spinner goroutine and synchronous tool-progress callbacks
// (invoked from within agentInstance.Process) never interleave mid-line.
//
// It is only ever constructed when output is a real TTY (see isTTY) — never
// print decorative output (spinner, \r, ANSI clear codes) to a non-terminal,
// since piped/non-interactive output must stay clean and parseable.
type cliProgress struct {
	mu         sync.Mutex
	out        io.Writer
	spinCancel chan struct{}
	spinDone   chan struct{}
	// streamed reports whether the stream sink delivered any text this turn.
	streamed bool
}

func newCLIProgress(out io.Writer) *cliProgress {
	return &cliProgress{out: out}
}

const clearLine = "\r\033[K"

// onToolEvent is the agent.ProgressFunc wired into the Agent. It clears the
// spinner line and prints a start/completion line for the tool call.
func (p *cliProgress) onToolEvent(e agent.ToolProgressEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprint(p.out, clearLine)
	switch e.Phase {
	case agent.ToolProgressStart:
		label := e.Tool
		if e.Summary != "" {
			label = fmt.Sprintf("%s(%s)", e.Tool, e.Summary)
		}
		fmt.Fprintf(p.out, "⏺ %s\n", label)
	case agent.ToolProgressDone:
		status := "ok"
		if e.Err != nil {
			status = "error"
		}
		fmt.Fprintf(p.out, "⎿ %s (%.1fs)\n", status, e.Elapsed.Seconds())
	}
}

// onStreamEvent is the agent.StreamSink wired into the Agent when streaming
// is enabled. It writes incremental text deltas directly to the output,
// stopping the spinner on the first delta so the spinner's \r does not fight
// the streamed text.
func (p *cliProgress) onStreamEvent(e agent.StreamEvent) {
	// Stop the spinner on the first delta — once text starts flowing, the
	// spinner is wrong (it implies "still thinking" over partial output).
	//
	// Waiting for the goroutine must happen outside p.mu. The spinner takes
	// that same lock to draw a frame, so holding it across `<-spinDone`
	// deadlocked whenever a tick landed in the window: the spinner blocked in
	// Lock and so never returned to its select to see spinCancel. That is why
	// stopSpinner does the close-and-wait outside the lock too.
	p.stopSpinner()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.streamed = true
	fmt.Fprint(p.out, e.Delta)
}

// takeSpinner detaches the running spinner's channels under the lock, so that
// exactly one caller ever closes spinCancel.
func (p *cliProgress) takeSpinner() (chan struct{}, chan struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cancel, done := p.spinCancel, p.spinDone
	p.spinCancel, p.spinDone = nil, nil
	return cancel, done
}

// beginTurn resets the per-turn state. `streamed` decides whether the caller
// still has to print the response, so a stale value from the previous turn
// would swallow this turn's answer.
func (p *cliProgress) beginTurn() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.streamed = false
}

// didStream reports whether any text reached the terminal through the stream
// sink during this turn.
func (p *cliProgress) didStream() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.streamed
}

var spinnerFrames = [...]string{"|", "/", "-", "\\"}

// startSpinner begins printing an elapsed-time spinner on a single line,
// updated in place via \r, until stopSpinner is called. It must be
// followed by exactly one stopSpinner call; the spinner goroutine exits as
// soon as stopSpinner signals it, so it never leaks or blocks shutdown.
func (p *cliProgress) startSpinner() {
	p.mu.Lock()
	p.spinCancel = make(chan struct{})
	p.spinDone = make(chan struct{})
	cancel, done := p.spinCancel, p.spinDone
	p.mu.Unlock()
	go func() {
		defer close(done)
		ticker := time.NewTicker(150 * time.Millisecond)
		defer ticker.Stop()
		start := time.Now()
		i := 0
		for {
			select {
			case <-cancel:
				return
			case <-ticker.C:
				p.mu.Lock()
				fmt.Fprintf(p.out, "\r%s thinking... (%.1fs)", spinnerFrames[i%len(spinnerFrames)], time.Since(start).Seconds())
				p.mu.Unlock()
				i++
			}
		}
	}()
}

// stopSpinner cancels the spinner goroutine, waits for it to exit, and
// clears the spinner line so subsequent output starts on a clean line.
func (p *cliProgress) stopSpinner() {
	cancel, done := p.takeSpinner()
	if cancel == nil {
		return
	}
	close(cancel)
	<-done
	p.mu.Lock()
	fmt.Fprint(p.out, clearLine)
	p.mu.Unlock()
}

func runAgentLoop(ctx context.Context, cancel context.CancelFunc, done <-chan struct{}, input io.Reader, output io.Writer, agentInstance agentProcessor, messageSender *tools.BusMessageSender, streaming bool) error {
	// Set chat ID for CLI mode so message tools can send messages proactively
	if messageSender != nil {
		messageSender.SetChatID("cli", "cli_user")
	}

	// Tool-call visibility and the spinner are strictly opt-in: only wired
	// up when output is a real terminal, so piped/non-interactive output
	// (e.g. a script driving `joshbot agent` over a pipe) never sees
	// decorative ANSI/\r content.
	var progress *cliProgress
	if isTTY(output) {
		progress = newCLIProgress(output)
		if pc, ok := agentInstance.(progressCapable); ok {
			pc.SetProgressCallback(progress.onToolEvent)
			defer pc.SetProgressCallback(nil)
		}
	}

	// The approval gate needs somewhere to ask, so it is installed only for a
	// real terminal. Everywhere else the request carries no approver and
	// tools.ApproverFromContext denies — which is the point: an unattended
	// turn must not be able to wait on an answer that is never coming.
	var approver *cliApprover
	if shellApprovalMode != tools.ApprovalOff && isTTY(output) {
		approver = newCLIApprover(output, input, false, shellApprovalMode)
	}
	// raw is decided below, when the line editor puts the terminal into raw
	// mode; a raw terminal delivers the keystroke with no trailing newline to
	// discard, and draining one there would eat the next character typed.

	fmt.Fprintln(output, "joshbot agent mode. Type 'exit' to quit.")

	// The line editor replaces the plain "> " prompt only when input is a real
	// terminal. Tests inject isTTY but pass bytes.Buffer / blockingReader as
	// input, which never satisfy the *os.File assertion, so this branch is not
	// taken in unit tests and the buffered path below is exercised instead.
	var editor *lineEditor
	var oldTermState *term.State
	if f, ok := input.(*os.File); ok && isTTY(output) && isatty.IsTerminal(f.Fd()) {
		editor = newLineEditor(output, nil, cliCommandNames)
		editor.reader = newOSKeyReader(int(f.Fd()))
		w, h := terminalSize(int(f.Fd()))
		editor.width, editor.height = w, h
		if mr, ok := agentInstance.(modelReporter); ok {
			editor.setPromptFn(func() string {
				return buildEditorPrompt(mr.CurrentModel())
			})
		}
		var err error
		oldTermState, err = makeRaw(int(f.Fd()))
		if err != nil {
			return fmt.Errorf("failed to enter terminal raw mode: %w", err)
		}
		if approver != nil {
			approver.raw = true
		}
		defer restoreTerminal(int(f.Fd()), oldTermState)
		defer editor.close()
	}

	// Reading happens on its own goroutine so a blocked read cannot make the
	// loop deaf to shutdown. Checking `done` only between reads meant a signal
	// arriving while sitting at the prompt was never observed, and the process
	// could only be killed with SIGKILL (issue #104). The editor path reads
	// through ReadLine, which selects on ctx.Done() the same way, and must NOT
	// share the descriptor with this bufio goroutine — two readers on the same
	// terminal would steal each other's keystrokes — so it is only created in
	// the buffered (non-editor) path.
	type readResult struct {
		line string
		err  error
	}
	var lines <-chan readResult
	if editor == nil {
		reader := bufio.NewReader(input)
		ch := make(chan readResult)
		lines = ch
		go func() {
			defer close(ch)
			for {
				line, err := reader.ReadString('\n')
				select {
				case ch <- readResult{line: line, err: err}:
				case <-ctx.Done():
					return
				}
				if err != nil {
					return
				}
			}
		}()
	}

	for {
		var line string
		var readErr error

		if editor != nil {
			line, readErr = editor.ReadLine(ctx)
			if readErr == io.EOF {
				cancel()
				return nil
			}
			if readErr != nil {
				if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
					log.Info("Agent shutdown complete")
					return nil
				}
				return fmt.Errorf("failed to read input: %w", readErr)
			}
		} else {
			fmt.Fprint(output, "> ")

			select {
			case <-done:
				log.Info("Agent shutdown complete")
				return nil
			case <-ctx.Done():
				log.Info("Agent shutdown complete")
				return nil
			case res, ok := <-lines:
				if !ok {
					// The reader goroutine is finished; nothing more can arrive.
					cancel()
					return nil
				}
				line, readErr = res.line, res.err
			}

			if readErr != nil && readErr != io.EOF {
				return fmt.Errorf("failed to read input: %w", readErr)
			}
			if readErr == io.EOF && strings.TrimSpace(line) == "" {
				cancel()
				return nil
			}
		}

		inputLine := strings.TrimSpace(line)
		if inputLine == "" {
			if readErr == io.EOF {
				cancel()
				return nil
			}
			continue
		}

		if strings.EqualFold(inputLine, "exit") {
			cancel()
			return nil
		}

		msg := bus.InboundMessage{
			SenderID:  "cli_user",
			Content:   inputLine,
			Channel:   "cli",
			Timestamp: time.Now(),
			Metadata: map[string]any{
				"username": "user",
			},
		}

		if progress != nil {
			progress.beginTurn()
			progress.startSpinner()
		}
		// Attach the per-request progress sink to the context so the real
		// *agent.Agent receives it via progressFromContext (concurrency-safe,
		// no shared mutable state on Agent). Mocks that still implement
		// progressCapable receive it via SetProgressCallback above.
		//
		// When streaming is enabled and output is a TTY, also attach a
		// stream sink so text deltas are written incrementally. The spinner
		// is stopped on the first delta by onStreamEvent.
		processCtx := ctx
		if progress != nil {
			processCtx = agent.WithSink(ctx, progress.onToolEvent)
		}
		if approver != nil {
			processCtx = tools.WithApprover(processCtx, approver)
		}
		if streaming && progress != nil {
			processCtx = agent.WithStreamSink(processCtx, progress.onStreamEvent)
		}
		response, procErr := agentInstance.Process(processCtx, msg)
		if progress != nil {
			progress.stopSpinner()
		}
		if procErr != nil {
			log.Error("Agent error", "error", procErr)
			fmt.Fprintf(output, "Error: %v\n", procErr)
			continue
		}

		// Print the response unless the stream sink already delivered it.
		//
		// The condition is "text was actually streamed this turn", not
		// "streaming is configured": several Process paths return without
		// streaming anything — a slash command, a session-load failure, the
		// timeout message, a stream that failed to open — and testing the
		// config instead printed two blank lines and nothing else.
		if progress != nil && progress.didStream() {
			fmt.Fprint(output, "\n\n")
		} else {
			fmt.Fprintf(output, "\n%s\n\n", strings.TrimSpace(response))
		}

		if readErr == io.EOF {
			cancel()
			return nil
		}
	}
}

// parseLogLevel converts a textual log level to a log.Level. It accepts the
// four charmbracelet levels plus common aliases.
func parseLogLevel(s string) (log.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return log.DebugLevel, nil
	case "info", "":
		return log.InfoLevel, nil
	case "warn", "warning":
		return log.WarnLevel, nil
	case "error", "err":
		return log.ErrorLevel, nil
	default:
		return log.InfoLevel, fmt.Errorf("unknown log level %q", s)
	}
}

// applyNoColor strips ANSI colour from the logger and from lipgloss-rendered
// output. Honoured everywhere colour is emitted (issue #148).
func applyNoColor() {
	log.Get().Logger.SetColorProfile(termenv.Ascii)
	lipgloss.SetColorProfile(termenv.Ascii)
}

// headlessSession resolves the (channel, senderID) pair that getSessionKey in
// the agent turns into a session id. A resume id of the form "channel:sender"
// is split on its first colon; a bare id is treated as a sender under the
// "cli" channel; empty resumes to the default headless session.
func headlessSession(resume string) (channel, sender string) {
	resume = strings.TrimSpace(resume)
	if resume == "" {
		return "cli", "cli_user"
	}
	if i := strings.IndexByte(resume, ':'); i >= 0 {
		ch, sn := resume[:i], resume[i+1:]
		if ch == "" || sn == "" {
			return "cli", strings.TrimPrefix(resume, ":")
		}
		return ch, sn
	}
	return "cli", resume
}

// jsonUsage is the token-usage block emitted in JSON output. Cost is omitted
// (null) because joshbot has no per-model pricing table; consumers compute
// cost from the token counts and their own rate card.
type jsonUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// jsonToolCall records a tool invocation for the JSON result.
type jsonToolCall struct {
	Tool    string `json:"tool"`
	Summary string `json:"summary,omitempty"`
}

// jsonResult is the single document emitted on stdout in --output-format json,
// and the terminal line in stream-json.
type jsonResult struct {
	Type      string         `json:"type"` // always "result"
	SessionID string         `json:"session_id"`
	Result    string         `json:"result"`
	IsError   bool           `json:"is_error"`
	Usage     jsonUsage      `json:"usage"`
	CostUSD   *float64       `json:"cost_usd"` // null: no pricing table
	ToolCalls []jsonToolCall `json:"tool_calls"`
}

// jsonErrorDoc is the structured error emitted on stderr in JSON modes.
type jsonErrorDoc struct {
	Type        string `json:"type"` // always "error"
	Error       string `json:"error"`
	Code        int    `json:"code"`
	Remediation string `json:"remediation,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
}

// emitJSONError writes a machine-readable error to stderr and records that a
// JSON error was emitted so main() does not also print a plain-text line.
func emitJSONError(w io.Writer, sessionID string, err error) {
	doc := jsonErrorDoc{
		Type:        "error",
		Error:       err.Error(),
		Code:        codeForError(err),
		Remediation: remediationForError(err),
		SessionID:   sessionID,
	}
	b, _ := json.Marshal(doc)
	fmt.Fprintln(w, string(b))
	jsonErrorEmitted = true
}

// runAgentJSON runs a single headless turn and emits machine-readable output.
// stdout carries data ONLY: the streamed events (stream-json) and the final
// result document. Every diagnostic goes to stderr. This is the single most
// important property for agent consumers (issue #144).
//
// It is deliberately independent of isTTY: JSON output is selected by the
// flag, never inferred from whether stdout is a terminal.
func runAgentJSON(ctx context.Context, agentInstance agentProcessor, message, format, resume string, stdout, stderr io.Writer, messageSender *tools.BusMessageSender, images ...providers.Image) error {
	channel, sender := headlessSession(resume)
	sessionID := channel + ":" + sender

	if messageSender != nil {
		messageSender.SetChatID(channel, sender)
	}

	stream := format == "stream-json"

	var (
		mu        sync.Mutex
		usage     jsonUsage
		toolCalls []jsonToolCall
	)
	enc := json.NewEncoder(stdout)

	// Capture tool calls (and, in stream-json, emit them live) via the
	// per-request progress sink already used by the interactive CLI.
	progressCtx := agent.WithSink(ctx, func(e agent.ToolProgressEvent) {
		mu.Lock()
		defer mu.Unlock()
		if e.Phase == agent.ToolProgressStart {
			toolCalls = append(toolCalls, jsonToolCall{Tool: e.Tool, Summary: e.Summary})
		}
		if stream {
			evt := map[string]any{"tool": e.Tool, "summary": e.Summary}
			if e.Phase == agent.ToolProgressStart {
				evt["type"] = "tool_start"
			} else {
				evt["type"] = "tool_done"
				evt["elapsed_seconds"] = e.Elapsed.Seconds()
				if e.Err != nil {
					evt["error"] = e.Err.Error()
				}
			}
			_ = enc.Encode(evt) // one JSON object per line (NDJSON)
		}
	})
	// Accumulate token usage across ReAct iterations.
	fullCtx := agent.WithUsageSink(progressCtx, func(u providers.Usage) {
		mu.Lock()
		defer mu.Unlock()
		usage.PromptTokens += u.PromptTokens
		usage.CompletionTokens += u.CompletionTokens
		usage.TotalTokens += u.TotalTokens
	})

	msg := bus.InboundMessage{
		SenderID:  sender,
		Content:   message,
		Channel:   channel,
		Timestamp: time.Now(),
		Metadata:  map[string]any{"username": "user"},
		Images:    images,
	}

	response, procErr := agentInstance.Process(fullCtx, msg)
	if procErr != nil {
		err := exitErrorf(exitGeneral, "failed to process message: %w", procErr)
		emitJSONError(stderr, sessionID, err)
		return err
	}

	// A turn the agent could not complete comes back as reply text with a nil
	// error (it answers a chat channel). The result document must report that
	// as is_error, and the process must exit non-zero — a consumer that only
	// reads stdout would otherwise treat the failure as an answer.
	replyErr := agentReplyError(response)

	mu.Lock()
	defer mu.Unlock()
	if toolCalls == nil {
		toolCalls = []jsonToolCall{}
	}
	res := jsonResult{
		Type:      "result",
		SessionID: sessionID,
		Result:    strings.TrimSpace(response),
		IsError:   replyErr != nil,
		Usage:     usage,
		CostUSD:   nil,
		ToolCalls: toolCalls,
	}
	if err := enc.Encode(res); err != nil {
		return exitErrorf(exitGeneral, "failed to encode result: %w", err)
	}
	if replyErr != nil {
		err := exitErrorf(exitGeneral, "failed to process message: %w", replyErr)
		emitJSONError(stderr, sessionID, err)
		return err
	}
	return nil
}

// runAgentSingleMessage sends a single message and prints the response. When
// resume is non-empty it continues that prior session; otherwise it uses the
// default headless session (cli:cli_user), preserving existing behaviour.
func runAgentSingleMessage(ctx context.Context, agentInstance agentProcessor, message, resume string, output io.Writer, messageSender *tools.BusMessageSender, images ...providers.Image) error {
	channel, sender := headlessSession(resume)
	// Set chat ID for CLI mode so message tools work
	if messageSender != nil {
		messageSender.SetChatID(channel, sender)
	}

	msg := bus.InboundMessage{
		SenderID:  sender,
		Content:   message,
		Channel:   channel,
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"username": "user",
		},
		Images: images,
	}

	response, err := agentInstance.Process(ctx, msg)
	if err == nil {
		err = agentReplyError(response)
	}
	if err != nil {
		return exitErrorf(exitGeneral, "failed to process message: %w", err)
	}

	fmt.Fprintln(output, strings.TrimSpace(response))
	return nil
}

// agentReplyPrefix is the in-band failure report the ReAct loop returns when a
// turn could not be completed (see internal/agent).
const agentReplyPrefix = "Error processing request: "

// agentReplyError turns that in-band report back into an error. The agent
// answers a chat channel, so it reports provider failures as reply text with a
// nil error; a non-interactive CLI must not exit 0 over one — a script piping
// `joshbot agent -m` cannot otherwise tell a real answer from an unreachable
// provider.
func agentReplyError(response string) error {
	trimmed := strings.TrimSpace(response)
	if !strings.HasPrefix(trimmed, agentReplyPrefix) {
		return nil
	}
	return errors.New(strings.TrimSpace(strings.TrimPrefix(trimmed, agentReplyPrefix)))
}

func runVersion(c *cli.Context) error {
	fmt.Printf("joshbot version %s\n", Version)
	return nil
}

// runUpdate checks for updates and installs the latest version of joshbot.
func runUpdate(c *cli.Context) error {
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════╗")
	fmt.Println("║           Update joshbot                 ║")
	fmt.Println("╚═══════════════════════════════════════════╝")
	fmt.Println()

	// 1. Get current version
	currentVersion := getVersion()
	fmt.Printf("Current version: %s\n", currentVersion)

	// 2. Get latest stable release from GitHub API
	fmt.Println("Checking for updates...")
	latestVersion, err := getLatestVersion()
	if err != nil {
		fmt.Printf("Error checking for updates: %v\n", err)
		fmt.Println("You can manually download from: https://github.com/bigknoxy/joshbot/releases")
		return nil
	}

	fmt.Printf("Latest stable release: %s\n", latestVersion)

	// 3. Compare versions
	cmp := compareVersions(currentVersion, latestVersion)
	if cmp >= 0 {
		fmt.Printf("Already up to date (%s)\n", currentVersion)
		return nil
	}

	// 4. Detect running context before any state changes
	runCtx := detectRunningContext()

	// 5. Download new binary
	fmt.Println()
	fmt.Println("Downloading update...")

	// Get current binary path
	exePath, err := getBinaryPath()
	if err != nil {
		return fmt.Errorf("could not determine executable path: %w", err)
	}

	// Check if running from source.
	//
	// Match only the go-build cache, which is where `go run` puts its
	// throwaway binary. An earlier version also rejected any path containing
	// "/tmp/", which is not a property of `go run` at all: it made a joshbot
	// installed anywhere under /tmp permanently unable to update, and told the
	// user the reason was `go run`.
	if runningFromGoRun(exePath) {
		return cli.Exit("Cannot update when running from source with 'go run'.\n"+
			"Install joshbot first (e.g. 'go install', or the one-line installer),\n"+
			"then run 'joshbot update' from the installed binary.", 1)
	}

	// Download to a temp file
	tmpDir := filepath.Dir(exePath)
	tmpFile := filepath.Join(tmpDir, ".joshbot_new")

	// Build download URL
	extension := ""
	if runtime.GOOS == "windows" {
		extension = ".exe"
	}
	downloadURL := fmt.Sprintf(
		"%s/%s/joshbot_%s_%s_%s%s",
		releaseDownloadBase, latestVersion, latestVersion, runtime.GOOS, runtime.GOARCH, extension,
	)

	if err := downloadBinary(downloadURL, tmpFile); err != nil {
		fmt.Printf("Error downloading: %v\n", err)
		fmt.Println("You can manually download from: https://github.com/bigknoxy/joshbot/releases")
		return nil
	}

	// 5. Make temp binary executable
	if err := os.Chmod(tmpFile, 0755); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("failed to make binary executable: %w", err)
	}

	// 6. Atomic replacement
	// First, try a simple rename (works if same filesystem and we have permissions)
	backupFile := exePath + ".bak"

	// Backup current binary
	if err := os.Rename(exePath, backupFile); err != nil {
		// If rename fails (e.g., different filesystem), try copying
		if copyErr := copyFile(tmpFile, exePath); copyErr != nil {
			os.Remove(tmpFile)
			return fmt.Errorf("failed to replace binary: %w", err)
		}
		// Clean up temp file after copy
		os.Remove(tmpFile)
	} else {
		// Rename succeeded - now rename temp to final location
		if err := os.Rename(tmpFile, exePath); err != nil {
			// Restore backup
			os.Rename(backupFile, exePath)
			os.Remove(tmpFile)
			return fmt.Errorf("failed to install update: %w", err)
		}
		// Remove backup
		os.Remove(backupFile)
	}

	fmt.Printf("Updated joshbot %s → %s\n", currentVersion, latestVersion)
	fmt.Println()

	// Auto-restart after successful update
	if runCtx.IsDocker {
		fmt.Println("Update complete. Restart your Docker container to use the new version.")
		return nil
	}

	if runCtx.IsService {
		svc, err := newServiceManager(service.Config{
			Name:        "joshbot",
			DisplayName: "Joshbot AI Assistant",
			Description: "Personal AI assistant with Telegram integration",
		})
		if err == nil {
			fmt.Println("Restarting joshbot service...")
			if err := svc.Restart(); err != nil {
				fmt.Printf("Warning: Could not restart service: %v\n", err)
				fmt.Println("Please restart manually: systemctl restart joshbot")
				return nil
			}
			fmt.Println("Service restarted successfully!")
			return nil
		}
	}

	// Interactive restart via exec
	fmt.Println("Restarting joshbot...")
	args := os.Args[1:]
	err = execSelf(exePath, append([]string{exePath}, args...), os.Environ())
	if err != nil {
		fmt.Printf("Warning: Could not auto-restart: %v\n", err)
		fmt.Println("Please restart joshbot manually.")
	}

	return nil
}

// getVersion returns the current version string.
func getVersion() string {
	if Version == "dev" {
		return "dev"
	}
	// Ensure version has v prefix
	if !strings.HasPrefix(Version, "v") {
		return "v" + Version
	}
	return Version
}

// GitHubRelease represents a GitHub release response.
type GitHubRelease struct {
	TagName string `json:"tag_name"`
}

// getLatestVersion fetches the latest stable release tag from GitHub API.
// releaseAPIURL is where the update check asks for the newest release. It is a
// package var rather than a constant for the same reason isTTY is: it is the
// one thing standing between this function and the network, and a test that
// cannot substitute it cannot cover the parse, status and empty-tag paths at
// all. Tests point it at an httptest server; nothing in production writes it.
var releaseAPIURL = "https://api.github.com/repos/bigknoxy/joshbot/releases/latest"

// releaseDownloadBase is the other half of that seam: where the new binary is
// fetched from. execSelf is the last statement of a successful update and
// replaces the process image, so a test that cannot substitute it can never
// reach the binary-replacement code it guards — which is the part that can
// leave an operator with no working joshbot at all. Nothing in production
// writes either.
var (
	releaseDownloadBase = "https://github.com/bigknoxy/joshbot/releases/download"
	execSelf            = syscall.Exec
)

func getLatestVersion() (string, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	req, err := http.NewRequest("GET", releaseAPIURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "joshbot-update-check")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if release.TagName == "" {
		return "", fmt.Errorf("no release tag found")
	}

	return release.TagName, nil
}

// compareVersions compares two semantic version strings.
// Returns -1 if v1 < v2, 0 if v1 == v2, 1 if v1 > v2.
// Only compares stable releases (ignores prerelease suffixes like -beta, -rc).
func compareVersions(v1, v2 string) int {
	// Normalize versions - strip v prefix
	v1 = strings.TrimPrefix(v1, "v")
	v2 = strings.TrimPrefix(v2, "v")

	// Strip prerelease suffixes for comparison
	v1 = stripPrerelease(v1)
	v2 = stripPrerelease(v2)

	v1Parts := strings.Split(v1, ".")
	v2Parts := strings.Split(v2, ".")

	// Compare major, minor, patch
	for i := 0; i < 3; i++ {
		var n1, n2 int
		if i < len(v1Parts) {
			n1, _ = strconv.Atoi(v1Parts[i])
		}
		if i < len(v2Parts) {
			n2, _ = strconv.Atoi(v2Parts[i])
		}

		if n1 < n2 {
			return -1
		}
		if n1 > n2 {
			return 1
		}
	}

	return 0
}

// stripPrerelease removes prerelease suffixes like -beta, -rc, -alpha.
func stripPrerelease(v string) string {
	if idx := strings.Index(v, "-"); idx != -1 {
		return v[:idx]
	}
	return v
}

// downloadBinary downloads a file from URL to destPath.
func downloadBinary(url, destPath string) error {
	client := &http.Client{
		Timeout: 60 * time.Second,
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "joshbot-update")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("release not found for this platform/architecture")
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	// Create temp file
	tmpFile, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer tmpFile.Close()

	// Copy response body to file
	_, err = io.Copy(tmpFile, resp.Body)
	if err != nil {
		os.Remove(destPath)
		return fmt.Errorf("failed to save downloaded file: %w", err)
	}

	return nil
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		return err
	}

	return dstFile.Sync()
}

// getBinaryPath returns the path to the current executable.
func getBinaryPath() (string, error) {
	exePath, err := osExecutable()
	if err != nil {
		return "", err
	}

	// Resolve symlinks
	realPath, err := filepath.EvalSymlinks(exePath)
	if err != nil {
		return exePath, nil
	}

	return realPath, nil
}

// osExecutable and newServiceManager are the two things standing between this
// package and the host: the path of the running binary (which uninstall and
// update both replace or delete) and the real launchd/systemd manager. They
// are package vars for the same reason isTTY is — a test that cannot
// substitute them can only cover the early bail-outs, and the delete and
// daemon-install paths are exactly the parts worth pinning. Every
// service.NewManager call site goes through newServiceManager; a new one that
// calls the package function directly is untestable by construction. Tests
// point osExecutable at a temp file, so the removal is real but harmless.
// Nothing in production writes either.
var (
	osExecutable      = os.Executable
	newServiceManager = service.NewManager
)

// runUninstall uninstalls joshbot and optionally removes configuration.
func runUninstall(c *cli.Context) error {
	// Find the binary location
	exePath, err := osExecutable()
	if err != nil {
		return fmt.Errorf("could not determine executable path: %w", err)
	}

	// Check if running from source.
	//
	// Match only the go-build cache, which is where `go run` puts its
	// throwaway binary — the same fix `joshbot update` needed. Also rejecting
	// any path containing "/tmp/" is not a property of `go run`: it made a
	// joshbot installed anywhere under /tmp permanently unable to uninstall,
	// and told the user the reason was `go run`.
	if runningFromGoRun(exePath) {
		return cli.Exit("Cannot uninstall when running from source with 'go run'.\n"+
			"Install joshbot first (e.g. 'go install', or the one-line installer),\n"+
			"then run 'joshbot uninstall' from the installed binary.", 1)
	}

	// Resolve symlinks to get the real path
	realPath, err := filepath.EvalSymlinks(exePath)
	if err != nil {
		realPath = exePath
	}

	// Check if the binary exists
	if _, err := os.Stat(realPath); os.IsNotExist(err) {
		return fmt.Errorf("binary not found at %s", realPath)
	}

	// Get absolute path for display
	absPath, err := filepath.Abs(realPath)
	if err != nil {
		absPath = realPath
	}

	// Show what will be removed
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════╗")
	fmt.Println("║           Uninstall joshbot               ║")
	fmt.Println("╚═══════════════════════════════════════════╝")
	fmt.Println()
	fmt.Printf("Binary to remove: %s\n", redact.HomePath(absPath))

	// Determine config directory
	configDir := config.DefaultHome
	configExists := false
	if _, err := os.Stat(configDir); err == nil {
		configExists = true
	}

	if configExists && !c.Bool("keep-config") {
		fmt.Printf("Config to remove: %s\n", redact.HomePath(configDir))
	} else if configExists && c.Bool("keep-config") {
		fmt.Printf("Config (kept):    %s\n", redact.HomePath(configDir))
	} else {
		fmt.Println("Config:           (not found)")
	}
	fmt.Println()

	// Check for installed service
	svcCfg := service.Config{
		Name:        "joshbot",
		DisplayName: "Joshbot AI Assistant",
		Description: "Personal AI assistant with Telegram integration",
		ExecPath:    absPath,
	}

	svc, svcErr := newServiceManager(svcCfg)
	serviceUninstalled := false

	if svcErr == nil && svc.IsInstalled() {
		fmt.Printf("Service detected:  joshbot (%s)\n", svc.Name())
		fmt.Println()

		// Prompt for service uninstall (default yes since binary is being removed)
		uninstallService := true
		if !c.Bool("force") {
			fmt.Print("Uninstall service? (Y/n): ")
			var response string
			fmt.Scanln(&response)
			uninstallService = strings.ToLower(response) != "n"
		}

		if uninstallService {
			fmt.Printf("Uninstalling service (%s)...\n", svc.Name())
			result, err := svc.Uninstall()
			if err != nil {
				fmt.Printf("Warning: Failed to uninstall service: %v\n", err)
				fmt.Println("You may need to uninstall it manually.")
			} else {
				serviceUninstalled = true
				fmt.Println(result.Message)
			}
		}
		fmt.Println()
	}

	// Check if running from the directory being removed - warn user
	dirToRemove := filepath.Dir(absPath)
	currentDir, err := os.Getwd()
	if err == nil {
		if strings.HasPrefix(currentDir, dirToRemove) {
			fmt.Println("Warning: You are running joshbot from within the directory that will be removed.")
			fmt.Println("The uninstall may fail or leave the binary in an inconsistent state.")
			fmt.Println()
		}
	}

	// Prompt for binary removal confirmation (unless --force)
	if !c.Bool("force") {
		fmt.Print("Remove joshbot binary? (y/N): ")
		var response string
		fmt.Scanln(&response)
		if strings.ToLower(response) != "y" {
			fmt.Println("Uninstall cancelled.")
			return nil
		}
	}

	// Remove the binary
	fmt.Printf("Removing binary: %s\n", redact.HomePath(absPath))
	if err := os.Remove(absPath); err != nil {
		return fmt.Errorf("failed to remove binary: %w", err)
	}

	// Prompt for config removal (unless --keep-config or --force)
	removeConfig := false
	if configExists && !c.Bool("keep-config") {
		if !c.Bool("force") {
			fmt.Print("Remove configuration directory (~/.joshbot)? (y/N): ")
			var response string
			fmt.Scanln(&response)
			removeConfig = strings.ToLower(response) == "y"
		} else {
			removeConfig = true
		}

		if removeConfig {
			fmt.Printf("Removing config: %s\n", redact.HomePath(configDir))
			if err := os.RemoveAll(configDir); err != nil {
				fmt.Printf("Warning: Failed to remove config directory: %v\n", err)
				fmt.Println("You may need to remove it manually.")
			}
		}
	}

	// Show success message
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════╗")
	fmt.Println("║           Uninstallation complete!         ║")
	fmt.Println("╚═══════════════════════════════════════════╝")
	fmt.Println()
	fmt.Println("Removed:")
	fmt.Printf("  - Binary: %s\n", redact.HomePath(absPath))
	if removeConfig {
		fmt.Printf("  - Config: %s\n", redact.HomePath(configDir))
	}
	if serviceUninstalled {
		fmt.Println("  - Service: joshbot")
	}
	fmt.Println()
	fmt.Println("Thank you for using joshbot!")

	return nil
}

// runGateway executes the gateway (Telegram + channels) mode.
// gatewayStreamer is the part of *channels.TelegramStreamer the gateway
// handler uses. It is an interface so the suppression rule below — the one
// that decides whether the bus copy of a reply is sent — can be tested without
// a Telegram bot.
type gatewayStreamer interface {
	Delta(text string)
	Finish(procErr error) bool
}

// noStreamer stands in when the turn is not being streamed. Finish reporting
// false is what routes the reply back through the bus.
type noStreamer struct{}

func (noStreamer) Delta(string)      {}
func (noStreamer) Finish(error) bool { return false }

// gatewayDeps is everything gatewayHandler needs from the running gateway.
// Pulling them out as functions is what makes the handler testable at all:
// the real ones are a message bus, an agent and a Telegram bot, none of which
// a unit test can stand up.
type gatewayDeps struct {
	publish   func(bus.OutboundMessage) bool
	process   func(context.Context, bus.InboundMessage) (string, error)
	setChatID func(channel, chatID string)
	// newStreamer returns nil when this turn must not be streamed.
	newStreamer func(msg bus.InboundMessage) gatewayStreamer
}

// gatewayHandler is the bus subscription runGateway installs: one inbound
// message in, at most one outbound message out. It is separated from
// runGateway because runGateway cannot be run in a test — it dials providers,
// opens a Telegram long poll and blocks on a signal — while every rule that
// decides what the user actually sees lives here.
func gatewayHandler(d gatewayDeps) func(context.Context, bus.InboundMessage) {
	return func(ctx context.Context, msg bus.InboundMessage) {
		log.Debug("bus handler invoked", "channel", msg.Channel, "sender", msg.SenderID)
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic in agent handler",
					"panic", fmt.Sprintf("%v", r),
					"stack", string(debug.Stack()),
				)
				outbound := bus.OutboundMessage{
					Content:   "I encountered an internal error while processing your request. Please try again.",
					Channel:   msg.Channel,
					ChannelID: getChannelID(msg),
					Timestamp: time.Now(),
				}
				d.publish(outbound)
			}
		}()

		// Store the chat ID for this channel to enable proactive messaging
		d.setChatID(msg.Channel, getChannelID(msg))

		log.Debug("Processing message",
			"channel", msg.Channel,
			"content", msg.Content,
		)
		// Stream the reply into the chat when streaming is on, editing one
		// message as text arrives instead of waiting for the whole turn.
		//
		// The streamer is per-request and rides the context, like the
		// tool-progress sink: the handler runs concurrently for every chat,
		// and a shared streamer would edit one conversation's message with
		// another's text. Heartbeat turns are excluded because their reply is
		// suppressed when nothing needs attention — streaming one would put
		// "HEARTBEAT_OK" in the chat, which is the exact output the
		// suppression exists to hide.
		procCtx := ctx
		// noStreamer, not a nil interface: Finish must return false rather
		// than panic when this turn is not streamed.
		var streamer gatewayStreamer = noStreamer{}
		if s := d.newStreamer(msg); s != nil {
			streamer = s
			procCtx = agent.WithStreamSink(procCtx, func(e agent.StreamEvent) {
				streamer.Delta(e.Delta)
			})
		}

		response, err := d.process(procCtx, msg)
		if err != nil {
			log.Error("Agent error", "error", err)
			// Partial text is already in the chat, so the error belongs on the
			// end of it rather than in a second message contradicting the
			// first. Finish reports false when nothing was delivered, and the
			// ordinary error reply is sent instead.
			if streamer.Finish(err) {
				return
			}
			// Send error response
			outbound := bus.OutboundMessage{
				Content:   fmt.Sprintf("Error: %v", err),
				Channel:   msg.Channel,
				ChannelID: getChannelID(msg),
				Timestamp: time.Now(),
			}
			d.publish(outbound)
			return
		}

		// Heartbeat messages are proactive background checks: when the agent
		// decides nothing needs the user's attention it replies HEARTBEAT_OK (see
		// the heartbeat completion contract), and that reply is suppressed rather
		// than delivered to the chat. An empty reply is treated the same way.
		if msg.SenderID == "heartbeat" {
			trimmed := strings.TrimSpace(response)
			if trimmed == "" || strings.HasPrefix(strings.ToUpper(trimmed), "HEARTBEAT_OK") {
				log.Debug("Heartbeat produced no actionable output; suppressing reply", "task", msg.Content)
				return
			}
		}

		// The streamed message is the reply. Publishing it again would post
		// the whole answer a second time under the incremental one, so the
		// bus path runs only when nothing was streamed — the same condition
		// the interactive CLI uses (didStream), for the same reason: several
		// Process paths return without streaming anything.
		// Process reports LLM failures in band, as reply text with a nil error
		// (see agentReplyError). A turn that streamed some text and then hit one
		// would otherwise end silently: the partial text is on screen, Finish
		// sees nothing wrong, and the publish that would have carried the error
		// is suppressed. Translating it back gets the reason appended to what
		// the user is already looking at.
		if streamer.Finish(agentReplyError(response)) {
			log.Info("Streamed outbound message", "channel", msg.Channel, "response_len", len(response))
			return
		}

		// Send response back to the appropriate channel
		channelID := getChannelID(msg)
		log.Info("Publishing outbound message", "channel", msg.Channel, "channelID", channelID, "response_len", len(response))
		outbound := bus.OutboundMessage{
			Content:   response,
			Channel:   msg.Channel,
			ChannelID: channelID,
			SenderID:  msg.SenderID,
			Timestamp: time.Now(),
		}
		d.publish(outbound)
	}
}

func runGateway(c *cli.Context) error {
	cfg, err := loadConfig(c.Path("config"))
	if err != nil {
		return err
	}
	if err := applyProfile(c, cfg); err != nil {
		return err
	}

	// Check for either legacy providers or new model-centric config
	if !cfg.UseModelsConfig() && len(cfg.Providers) == 0 {
		return fmt.Errorf("no providers configured. Run 'joshbot onboard' first")
	}

	// Get model name for logging
	modelName := cfg.Agents.Defaults.Model
	if cfg.UseModelsConfig() {
		modelName = cfg.ModelsConfig.Agent.Model
	}

	log.Info("Starting gateway mode",
		"model", modelName,
		"telegram", cfg.Channels.Telegram.Enabled,
		"discord", cfg.Channels.Discord.Enabled,
	)

	// Setup components
	msgBus, _, _, agentInstance, _, sender, err := setupComponents(cfg)
	defer closeMCPServers()
	if err != nil {
		return err
	}

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	setupGracefulShutdown(ctx, cancel, done)

	// Start message bus
	msgBus.Start()

	// The Telegram channel is created before the subscription that uses it,
	// not after: the bus handler runs on its own goroutine, and assigning to a
	// variable it has already captured is a data race even though no message
	// can arrive before Start.
	var tgChannel *channels.TelegramChannel
	if cfg.Channels.Telegram.Enabled && cfg.Channels.Telegram.Token != "" {
		tgChannel = channels.NewTelegramChannel(msgBus, &cfg.Channels.Telegram)
	}
	streaming := cfg.Agents.Defaults.Streaming

	// Subscribe agent to all channels. The handler itself is gatewayHandler,
	// which is where every decision worth testing lives; runGateway only owns
	// the network and process lifecycle around it.
	msgBus.Subscribe("all", gatewayHandler(gatewayDeps{
		publish: msgBus.Publish,
		process: agentInstance.Process,
		setChatID: func(channel, chatID string) {
			if sender != nil {
				sender.SetChatID(channel, chatID)
			}
		},
		newStreamer: func(msg bus.InboundMessage) gatewayStreamer {
			if !shouldStream(streaming, tgChannel != nil, msg) {
				return nil
			}
			// A typed nil must not be returned as a non-nil interface: the
			// handler tests the interface, not the pointer.
			s := tgChannel.NewStreamer(getChannelID(msg))
			if s == nil {
				return nil
			}
			return s
		},
	}))

	// Start Telegram channel if enabled
	if tgChannel != nil {
		if err := tgChannel.Start(ctx); err != nil {
			log.Error("Failed to start Telegram channel", "error", err)
		} else {
			log.Info("Telegram channel started")
		}
	}

	// Start Discord channel if enabled
	var discordChannel *channels.DiscordChannel
	if cfg.Channels.Discord.Enabled && cfg.Channels.Discord.Token != "" {
		discordChannel = channels.NewDiscordChannel(msgBus, &cfg.Channels.Discord)
		if err := discordChannel.Start(ctx); err != nil {
			log.Error("Failed to start Discord channel", "error", err)
		} else {
			log.Info("Discord channel started")
		}
	}

	// Print startup banner
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════╗")
	fmt.Println("║         joshbot gateway running           ║")
	fmt.Printf("║  Model: %-34s ║\n", cfg.Agents.Defaults.Model)
	fmt.Printf("║  Telegram: %-30s ║\n", boolToEnabled(cfg.Channels.Telegram.Enabled))
	fmt.Printf("║  Discord: %-31s ║\n", boolToEnabled(cfg.Channels.Discord.Enabled))
	fmt.Println("║                                           ║")
	fmt.Println("║  Press Ctrl+C to stop                     ║")
	fmt.Println("╚═══════════════════════════════════════════╝")
	fmt.Println()

	// Wait for shutdown
	<-done

	// Stop Telegram channel
	if tgChannel != nil {
		tgChannel.Stop()
	}

	// Stop Discord channel
	if discordChannel != nil {
		discordChannel.Stop()
	}

	log.Info("Gateway stopped")
	return nil
}

// shouldStream decides whether a turn gets incremental Telegram edits. Every
// condition here is load-bearing and none of them fails loudly if dropped:
// streaming a Discord or CLI turn reaches for a Telegram streamer that cannot
// exist, and streaming a heartbeat edits a message into a chat the operator
// never asked anything in — the heartbeat's own reply is usually suppressed
// entirely, so the stream would be the only thing they ever see of it.
func shouldStream(streaming, haveTelegram bool, msg bus.InboundMessage) bool {
	return streaming && haveTelegram && msg.Channel == "telegram" && msg.SenderID != "heartbeat"
}

// getChannelID extracts the chat ID from message metadata.
func getChannelID(msg bus.InboundMessage) string {
	if chatID, ok := msg.Metadata["chat_id"]; ok {
		switch v := chatID.(type) {
		case string:
			return v
		case int64:
			return fmt.Sprintf("%d", v)
		case float64:
			return fmt.Sprintf("%.0f", v)
		case int:
			return fmt.Sprintf("%d", v)
		}
	}
	return ""
}

// runOnboard executes the first-time setup wizard.
func runOnboard(c *cli.Context) error {
	force := c.Bool("force")
	keepData := c.Bool("keep-data")
	modelFlag := c.String("model")
	providerFlag := c.String("provider")
	apiKeyFlag := c.String("api-key")
	apiBaseFlag := c.String("api-base")

	// Anchor before anything reads or writes the home: onboarding inspects the
	// install, backs it up and rewrites it, all from homeDir. Anchoring later
	// meant --config selected where the new config.json was written while the
	// backup and the deletion still hit the real ~/.joshbot (issue #97).
	if p := explicitConfigPath(c.Path("config")); p != "" {
		if err := config.UseConfigFile(p); err != nil {
			return err
		}
	}
	homeDir := config.DefaultHome

	// An unknown --provider used to be accepted, "validated" and written out as
	// a config nothing can dial. Reject it before anything is touched.
	if providerFlag != "" && !isSupportedProvider(providerFlag) {
		return fmt.Errorf("unknown provider %q (supported: %s)",
			providerFlag, strings.Join(configure.SupportedProviders(), ", "))
	}

	// Welcome banner
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════╗")
	fmt.Println("║       Welcome to joshbot!                 ║")
	fmt.Println("║  Let's get you set up.                    ║")
	fmt.Println("╚═══════════════════════════════════════════╝")
	fmt.Println()

	// Check for existing installation
	configExists, workspaceExists, _ := checkExistingInstall(homeDir)
	hasExisting := configExists || workspaceExists

	// Track whether we should skip file creation
	skipFileCreation := false

	// Load existing config for reconfiguration mode
	var existingCfg *config.Config
	if hasExisting && (keepData || force) {
		// Try to load existing config for defaults
		var err error
		existingCfg, err = loadConfig(c.Path("config"))
		if err != nil {
			log.Warn("Failed to load existing config, will use defaults", "error", err)
		}
	}

	if hasExisting {
		if force {
			// --force: backup and continue with full onboarding (no prompts)
			fmt.Println("Existing installation found. Backing up...")
			backupPath, err := backupExisting(homeDir)
			if err != nil {
				return fmt.Errorf("failed to backup existing installation: %w", err)
			}
			fmt.Printf("Backed up to: %s\n", redact.HomePath(backupPath))
			fmt.Println()
		} else if keepData {
			// --keep-data: skip file creation, just run prompts
			skipFileCreation = true
			fmt.Println("Keeping existing data, will reconfigure...")
			fmt.Println()
		} else {
			// Interactive mode: show two-choice menu
			fmt.Println("╔═══════════════════════════════════════════╗")
			fmt.Println("║        Existing Installation Found        ║")
			fmt.Println("╚═══════════════════════════════════════════╝")
			fmt.Println()

			// Display existing files with status
			fmt.Printf("  Config:     %s %s\n", redact.HomePath(filepath.Join(homeDir, "config.json")), statusBool(configExists))
			fmt.Printf("  Workspace:  %s %s\n", redact.HomePath(filepath.Join(homeDir, "workspace/")), statusBool(workspaceExists))
			memoryPath := filepath.Join(homeDir, "workspace", "memory")
			if _, err := os.Stat(memoryPath); err == nil {
				fmt.Printf("  Memory:     %s %s\n", redact.HomePath(memoryPath), statusBool(true))
			}
			fmt.Println()

			fmt.Println("  [1] Keep existing data and reconfigure")
			fmt.Println("  [2] Delete and start fresh (backup created)")
			fmt.Println()
			fmt.Print("  Choose [1-2] (default: 1): ")

			var choice string
			fmt.Scanln(&choice)
			fmt.Println()

			// The menu says "(default: 1)", so a bare Enter — which Scanln
			// leaves as an empty string — must keep the data. It used to fall
			// through to the else and move the whole install aside, which is
			// the one outcome the operator was told they were declining.
			if choice := strings.TrimSpace(choice); choice == "" || choice == "1" {
				// Keep existing data: load config and run prompts with defaults
				skipFileCreation = true
				var err error
				existingCfg, err = loadConfig(c.Path("config"))
				if err != nil {
					log.Warn("Failed to load existing config, will use defaults", "error", err)
				}
			} else {
				// Delete and start fresh: backup then continue
				fmt.Println("Backing up existing installation...")
				backupPath, err := backupExisting(homeDir)
				if err != nil {
					return fmt.Errorf("failed to backup existing installation: %w", err)
				}
				fmt.Printf("Backed up to: %s\n", redact.HomePath(backupPath))
				fmt.Println()
			}
		}
	}

	// Run prompts (skip if --force)
	var apiKey, personalityChoice, model, userName string
	var provider string
	var soulContent string
	var telegramConfig *config.TelegramConfig

	if force {
		// Use defaults for non-interactive setup
		personalityChoice = "2" // Friendly
		soulContent = getPersonalitySoul(personalityChoice)
		// Provider precedence: --provider flag, then existing config, then default.
		if providerFlag != "" {
			provider = providerFlag
		} else if existingCfg != nil && len(existingCfg.Providers) > 0 {
			for p := range existingCfg.Providers {
				provider = p
				break
			}
		}
		if provider == "" {
			provider = "openrouter"
		}
		// The model must follow the chosen provider: config.DefaultModel is an
		// OpenRouter id, so `--force --provider ollama` used to write a model
		// the selected provider cannot serve.
		switch {
		case modelFlag != "":
			model = modelFlag
		case providers.GetDefaultModel(provider) != "":
			model = providers.GetDefaultModel(provider)
		default:
			model = config.DefaultModel
		}
		// API key precedence: --api-key flag, then env, then existing config.
		// --force must never block on stdin, so promptProviderAPIKey is not used:
		// it reads stdin, so with a terminal attached it hung forever, and with
		// stdin closed it silently saved a config with no provider configured.
		apiKey = strings.TrimSpace(apiKeyFlag)
		if apiKey == "" {
			apiKey = providerAPIKeyFromEnv(provider)
		}
		if apiKey == "" && existingCfg != nil {
			if p, ok := existingCfg.Providers[provider]; ok && p.APIKey != "" {
				apiKey = p.APIKey
			}
		}
	} else {
		// Interactive prompts - pass existing config for defaults. Flags, when
		// supplied, pre-fill the corresponding answer instead of prompting.
		if providerFlag != "" {
			provider = providerFlag
		} else {
			provider = selectProvider(existingCfg)
		}
		if apiKeyFlag != "" {
			apiKey = strings.TrimSpace(apiKeyFlag)
		} else {
			var err error
			apiKey, err = promptProviderAPIKey(provider, existingCfg)
			if err != nil {
				return err
			}
		}
		personalityChoice = selectPersonality(existingCfg)
		soulContent = getPersonalitySoul(personalityChoice)
		userName = promptUserName(existingCfg)
		model = selectModel(existingCfg, provider, modelFlag)
		telegramConfig = setupTelegram(existingCfg)
	}

	// Build config
	cfg := config.Defaults()
	providerConfigured := false
	if apiKey != "" || provider == "ollama" || provider == "github-copilot" {
		// Get provider's default model as fallback
		defaultModel := providers.GetDefaultModel(provider)
		if defaultModel == "" {
			defaultModel = "openrouter/free"
		}
		// Use selected model, or fall back to provider default
		if model == "" {
			model = defaultModel
		}
		// Build provider config
		providerCfg := config.ProviderConfig{
			APIKey:  apiKey,
			APIBase: strings.TrimSpace(apiBaseFlag),
			Enabled: true,
			Model:   model,
		}
		// For Ollama, set appropriate defaults
		if provider == "ollama" {
			providerCfg.Timeout = 300 * time.Second
		}
		cfg.Providers = map[string]config.ProviderConfig{
			provider: providerCfg,
		}
		cfg.ProviderDefaults.Default = provider
		providerConfigured = true
	}

	// A non-interactive --force run that ends up with no usable provider must
	// fail loudly instead of printing "Setup complete!" over an unusable config
	// — otherwise the next `joshbot agent` tells the user to onboard again, the
	// exact loop reported in issue #142. config.Defaults() seeds a stub
	// "openrouter" entry with no key, so length alone is not a usable signal;
	// providerConfigured tracks whether a real credential (or a keyless local
	// provider like ollama) was actually wired up.
	cfg.Agents.Defaults.Model = model
	if userName != "" {
		cfg.User.Name = userName
	}
	if telegramConfig != nil {
		cfg.Channels.Telegram = *telegramConfig
	}

	// Ensure directories and save config
	if err := cfg.EnsureDirs(); err != nil {
		return fmt.Errorf("failed to create directories: %w", err)
	}

	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// Validate the credential now, so a bad key is reported here rather than
	// surfacing as a raw provider 401 on the user's first chat (issue #160).
	// A failure is reported, not fatal: it may be an offline onboard or a
	// transient network error, and the config is already written.
	if apiKey != "" {
		cfgr := configure.New(cfg)
		if err := cfgr.ValidateProviderCredentials(provider); err != nil {
			fmt.Printf("\n  ⚠ Could not validate %s credentials: %v\n", configure.GetProviderDisplayName(provider), err)
			if url := providerKeyURL(provider); url != "" {
				fmt.Printf("    Check your key at: %s\n", url)
			}
		} else {
			fmt.Printf("\n  ✓ %s credentials validated\n", configure.GetProviderDisplayName(provider))
		}
	}

	// Only create workspace files if NOT keeping existing data
	if !skipFileCreation {
		if err := createWorkspaceFiles(cfg, soulContent); err != nil {
			return err
		}
	}

	// Step 6: Service install
	var installService bool
	if force {
		installService = false
	} else {
		installService = promptServiceInstall()
	}
	if installService {
		if err := doServiceInstall(); err != nil {
			fmt.Printf("Warning: Could not install service: %v\n", err)
			fmt.Println("You can run 'joshbot service install' manually later.")
			if err := promptCronStartupFallback(); err != nil {
				fmt.Printf("Warning: Could not configure cron startup fallback: %v\n", err)
			}
		}
	}

	configPath := filepath.Join(homeDir, "config.json")
	wsDir := cfg.WorkspaceDir()

	// The config and workspace scaffold above are written unconditionally so a
	// caller that intends to supply a credential separately still gets a usable
	// tree. But a run with no usable provider must not print "Setup
	// complete!" and exit 0 — that is the loop reported in issue #142, where the
	// next `joshbot agent` just tells the user to onboard again.
	//
	// The interactive path gets the same treatment: with stdin closed every
	// prompt reads EOF and takes its default, which produced a config with an
	// empty provider entry, "Setup complete!" and exit 0.
	if !providerConfigured {
		cmd := "onboard"
		if force {
			cmd = "onboard --force"
		}
		return fmt.Errorf("%s did not configure any provider: pass --provider <name> --api-key <key>, "+
			"or set JOSHBOT_PROVIDERS__%s__API_KEY (supported: %s)",
			cmd,
			strings.ToUpper(strings.ReplaceAll(provider, "-", "_")),
			strings.Join(configure.SupportedProviders(), ", "))
	}

	// Print completion banner

	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════╗")
	fmt.Println("║           Setup complete!                  ║")
	fmt.Println("╚═══════════════════════════════════════════╝")
	fmt.Println()
	fmt.Println("  Config:")
	// Same treatment as `joshbot status`: the home directory carries the
	// account name, and the setup summary is the first output a new user
	// pastes into an issue when something did not work.
	fmt.Printf("    %s\n", redact.HomePath(configPath))
	fmt.Printf("    %s\n", redact.HomePath(wsDir))
	fmt.Println()
	fmt.Println("  What's next?")
	fmt.Println()
	fmt.Println("   1. Test your setup:")
	fmt.Println("      $ joshbot agent -m \"hello\"")
	fmt.Println()
	fmt.Println("   2. Start an interactive chat session:")
	fmt.Println("      $ joshbot agent")
	fmt.Println()
	if telegramConfig != nil && telegramConfig.Enabled {
		fmt.Println("   3. Run joshbot with Telegram (background + all channels):")
		fmt.Println("      $ joshbot gateway")
		fmt.Println()
		fmt.Println("   4. Check your configuration:")
		fmt.Println("      $ joshbot status")
		fmt.Println()
		fmt.Println("   5. Rerun this setup anytime:")
		fmt.Println("      $ joshbot onboard")
	} else {
		fmt.Println("   3. Check your configuration:")
		fmt.Println("      $ joshbot status")
		fmt.Println()
		fmt.Println("   4. Rerun this setup anytime:")
		fmt.Println("      $ joshbot onboard")
	}
	fmt.Println()
	fmt.Println("  Need help?")
	fmt.Println("    Edit ~/.joshbot/config.json to customize settings.")
	fmt.Println("    Run joshbot with --debug flag for verbose logging.")

	return nil
}

// selectProvider prompts the user to choose an LLM provider.
func selectProvider(existingCfg *config.Config) string {
	fmt.Println("\n[Step 1] LLM Provider")
	fmt.Println("Choose your LLM provider:")

	// Use provider registry for display names and descriptions
	providerList := []string{"nvidia", "openrouter", "groq", "ollama", "github-copilot", "poolside"}
	for i, key := range providerList {
		displayName := providers.GetProviderDisplayName(key)
		desc := providers.GetProviderDescription(key)

		prefix := "  "
		suffix := ""
		if key == "nvidia" {
			prefix = "  ✅"
			suffix = " — recommended for new users"
		}
		if desc != "" {
			fmt.Printf("%s %d. %s (%s%s)\n", prefix, i+1, displayName, desc, suffix)
		} else {
			fmt.Printf("%s %d. %s\n", prefix, i+1, displayName)
		}
	}

	// Show current default if exists
	if existingCfg != nil && existingCfg.ProviderDefaults.Default != "" {
		fmt.Printf("\nCurrent provider: %s\n", providers.GetProviderDisplayName(existingCfg.ProviderDefaults.Default))
	}

	fmt.Print("\nChoice [1]: ")
	var choice string
	fmt.Scanln(&choice)
	if choice == "" {
		choice = "1"
	}

	switch choice {
	case "1":
		return "nvidia"
	case "2":
		return "openrouter"
	case "3":
		return "groq"
	case "4":
		return "ollama"
	case "5":
		return "github-copilot"
	case "6":
		return "poolside"
	default:
		return "nvidia"
	}
}

// providerKeyURL returns the URL where a provider's API key can be obtained, or
// "" if there is no well-known page.
func providerKeyURL(provider string) string {
	switch provider {
	case "nvidia":
		return "https://build.nvidia.com"
	case "openrouter":
		return "https://openrouter.ai/keys"
	case "groq":
		return "https://console.groq.com/keys"
	case "poolside":
		return "https://poolside.ai"
	case "openai":
		return "https://platform.openai.com/api-keys"
	case "anthropic":
		return "https://console.anthropic.com/settings/keys"
	default:
		return ""
	}
}

// providerAPIKeyFromEnv reads a provider's API key from the environment,
// accepting both the canonical nested form
// (JOSHBOT_PROVIDERS__<PROVIDER>__API_KEY) and the shorthand
// (JOSHBOT_<PROVIDER>_API_KEY). The provider key is upper-cased with hyphens
// mapped to underscores (github-copilot -> GITHUB_COPILOT).
func providerAPIKeyFromEnv(provider string) string {
	up := strings.ToUpper(strings.ReplaceAll(provider, "-", "_"))
	if v := os.Getenv("JOSHBOT_PROVIDERS__" + up + "__API_KEY"); v != "" {
		return strings.TrimSpace(v)
	}
	if v := os.Getenv("JOSHBOT_" + up + "_API_KEY"); v != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

// promptProviderAPIKey prompts for the API key based on the selected provider.
func promptProviderAPIKey(provider string, existingCfg *config.Config) (string, error) {
	var keyName string
	keyURL := providerKeyURL(provider)
	switch provider {
	case "nvidia":
		keyName = "NVIDIA API key"
	case "openrouter":
		keyName = "OpenRouter API key"
	case "groq":
		keyName = "Groq API key"
	case "poolside":
		keyName = "Poolside API key"
	case "ollama":
		fmt.Println("\nOllama runs locally - no API key needed.")
		return "", nil
	case "github-copilot":
		fmt.Println("\nGitHub Copilot uses OAuth authentication.")
		fmt.Println("You will be shown a URL and code to authorize.")
		fmt.Println()

		ctx := context.Background()
		if _, err := copilot.RunDeviceFlow(ctx); err != nil {
			return "", fmt.Errorf("OAuth failed: %w", err)
		}

		fmt.Println("\nSuccessfully authenticated with GitHub Copilot!")
		fmt.Println("Token saved to ~/.joshbot/auth.json")
		return "", nil
	}

	if keyName == "" {
		keyName = configure.GetProviderDisplayName(provider) + " API key"
	}
	if keyURL != "" {
		fmt.Printf("\nGet your %s at: %s\n", keyName, keyURL)
	}

	// Show existing key if available
	var currentKey string
	if existingCfg != nil {
		if p, ok := existingCfg.Providers[provider]; ok && p.APIKey != "" {
			currentKey = p.APIKey
			fmt.Printf("Current API key: %s\n", configure.MaskAPIKey(p.APIKey))
			fmt.Print("Enter new API key (or press Enter to keep current): ")
		} else {
			fmt.Printf("Enter your %s (or press Enter to skip): ", keyName)
		}
	} else {
		fmt.Printf("Enter your %s (or press Enter to skip): ", keyName)
	}

	var apiKey string
	fmt.Scanln(&apiKey)
	apiKey = strings.TrimSpace(apiKey)

	// Pressing Enter means "keep the current key", not "clear the provider".
	// The caller in runOnboard treats an empty key as "no provider configured"
	// and falls back to the default (disabled) openrouter entry, silently
	// dropping the provider that was already working — the field report that
	// introduced this fix: reconfiguring an existing NVIDIA install with
	// Enter-to-keep saved a config with no enabled provider at all.
	if apiKey == "" && currentKey != "" {
		apiKey = currentKey
	}
	return apiKey, nil
}

// selectPersonality prompts the user to choose a personality and returns the choice.
func selectPersonality(existingCfg *config.Config) string {
	fmt.Println("\n[Step 2] Personality")
	fmt.Println("Choose joshbot's personality:")
	fmt.Println("  1. Professional - Concise, task-focused, minimal small talk")
	fmt.Println("  2. Friendly - Warm, conversational, uses humor")
	fmt.Println("  3. Sarcastic - Witty, dry humor, still helpful underneath")
	fmt.Println("  4. Minimal - Extremely terse, just the facts")
	fmt.Println("  5. Custom - Write your own SOUL.md")

	// Default to "2" (Friendly) - personality isn't stored in config
	defaultChoice := "2"

	fmt.Printf("Choose personality (1-5) [%s]: ", defaultChoice)
	var personalityChoice string
	fmt.Scanln(&personalityChoice)
	if personalityChoice == "" {
		personalityChoice = defaultChoice
	}

	// Show a sample response to help them know what to expect
	showPersonalityPreview(personalityChoice)

	return personalityChoice
}

// promptUserName prompts the user for their name.
func promptUserName(existingCfg *config.Config) string {
	fmt.Println("\n[Step 3] Personalization")

	// Show existing name if available
	var defaultName string
	if existingCfg != nil && existingCfg.User.Name != "" {
		defaultName = existingCfg.User.Name
		fmt.Printf("Current name: %s\n", defaultName)
		fmt.Print("Enter your name (or press Enter to keep current): ")
	} else {
		fmt.Print("What should I call you? (optional, press Enter to skip): ")
	}

	var name string
	fmt.Scanln(&name)
	return strings.TrimSpace(name)
}

// modelHelp returns a brief description of what a model is good for, by provider.
func modelHelp(provider string) string {
	switch provider {
	case "nvidia":
		return "Good for complex reasoning, coding, and analysis tasks"
	case "openrouter":
		return "OpenRouter's free tier — good for testing and light use"
	case "groq":
		return "Fast responses, great for chat and quick iterations"
	case "ollama":
		return "Local model, good balance of speed and capability"
	case "poolside":
		return "AI for software development, optimized for coding tasks"
	case "github-copilot":
		return "GitHub's Copilot models, optimized for coding"
	default:
		return "Your chosen model for all conversations"
	}
}

// selectModel prompts the user to select a model and returns the choice.
func selectModel(existingCfg *config.Config, provider string, modelFlag string) string {
	// Get provider's default model, fall back to config default
	defaultModel := providers.GetDefaultModel(provider)
	if defaultModel == "" {
		defaultModel = config.DefaultModel
	}

	// CLI flag has highest priority
	if modelFlag != "" {
		defaultModel = modelFlag
	} else if existingCfg != nil && existingCfg.Agents.Defaults.Model != "" {
		// Use existing model as default if available
		defaultModel = existingCfg.Agents.Defaults.Model
	}

	displayName := providers.GetProviderDisplayName(provider)
	modelDesc := modelHelp(provider)

	// showModelPrompt displays the model selection prompt and reads input.
	showModelPrompt := func() string {
		fmt.Printf("  %s default: %s\n", displayName, defaultModel)
		fmt.Printf("  └ %s\n", modelDesc)
		fmt.Printf("\nModel name [%s] (press Enter to accept): ", defaultModel)
		var model string
		fmt.Scanln(&model)
		model = strings.TrimSpace(model)
		if model == "" {
			model = defaultModel
		}
		return model
	}

	// For GitHub Copilot, fetch models from the catalog
	if provider == "github-copilot" {
		homeDir, _ := copilot.GetHomeDir()
		token, err := copilot.LoadToken(homeDir)
		if err == nil && token != nil && token.AccessToken != "" {
			fmt.Println("\n[Step 4] Model")
			fmt.Println("Fetching available models from GitHub Copilot...")
			models, err := copilot.ListModels(token.AccessToken)
			if err != nil {
				fmt.Printf("Could not fetch models: %v\n", err)
				fmt.Println()
				return showModelPrompt()
			}
			if len(models) > 0 {
				// Check if existing config has a saved model
				if existingCfg != nil {
					if p, ok := existingCfg.Providers[provider]; ok && p.Model != "" {
						defaultModel = p.Model
					}
				}
				return promptModelSelection(models, defaultModel)
			}
			fmt.Println("Could not fetch models, using default.")
			fmt.Println()
			return showModelPrompt()
		}
		fmt.Println("Not authenticated with GitHub Copilot, using default.")
		fmt.Println()
		return showModelPrompt()
	}

	fmt.Println("\n[Step 4] Model")
	fmt.Printf("  The model powers all of joshbot's responses. Each provider has a\n")
	fmt.Printf("  recommended default that balances speed, quality, and cost.\n")
	fmt.Println()
	fmt.Printf("  You can change this later in config.json or with the --model flag.\n")
	fmt.Println()
	return showModelPrompt()
}

func setupTelegram(existingCfg *config.Config) *config.TelegramConfig {
	fmt.Println("\n[Step 5] Telegram Setup")

	// Check if Telegram is already configured
	existingToken := ""
	existingEnabled := false
	existingAllowFrom := []string{}
	if existingCfg != nil {
		existingEnabled = existingCfg.Channels.Telegram.Enabled
		existingToken = existingCfg.Channels.Telegram.Token
		existingAllowFrom = existingCfg.Channels.Telegram.AllowFrom
	}

	if existingEnabled && existingToken != "" {
		// Already configured - ask if they want to keep or change
		maskedToken := maskToken(existingToken)
		fmt.Printf("Telegram is currently configured.\n")
		fmt.Printf("Current bot token: %s\n", maskedToken)
		fmt.Println()
		fmt.Println("  1. Keep current token")
		fmt.Println("  2. Change token")
		fmt.Println("  3. Disable Telegram")
		fmt.Println()
		fmt.Printf("Choice [1]: ")

		var choice string
		fmt.Scanln(&choice)
		fmt.Println()

		if choice == "3" {
			fmt.Println("Telegram disabled.")
			return &config.TelegramConfig{
				Enabled:   false,
				Token:     "",
				AllowFrom: []string{},
			}
		}

		if choice == "1" || choice == "" {
			// Keep existing token
			fmt.Println("Keeping current Telegram configuration.")
			return &config.TelegramConfig{
				Enabled:   true,
				Token:     existingToken,
				AllowFrom: existingAllowFrom,
			}
		}
		// choice == "2" - proceed to get new token
	} else {
		// Not configured yet
		fmt.Println("Would you like to set up Telegram for joshbot?")
		fmt.Println("This allows you to chat with joshbot via Telegram.")
		fmt.Println()
		fmt.Println("  1. Yes, set up Telegram")
		fmt.Println("  2. No, skip for now")
		fmt.Println()
		fmt.Printf("Choice [2]: ")

		var choice string
		fmt.Scanln(&choice)

		if choice != "1" {
			fmt.Println("\nSkipping Telegram setup. You can configure it later by editing:")
			fmt.Printf("  %s\n", filepath.Join(config.DefaultHome, "config.json"))
			return nil
		}
	}

	// Get new token
	fmt.Println("\n" + strings.Repeat("─", 45))
	fmt.Println("Telegram Bot Setup")
	fmt.Println(strings.Repeat("─", 45))
	fmt.Println()
	fmt.Println("To create a Telegram bot:")
	fmt.Println()
	fmt.Println("  1. Open Telegram and search for @BotFather")
	fmt.Println("  2. Send the command: /newbot")
	fmt.Println("  3. Follow the prompts to name your bot")
	fmt.Println("  4. BotFather will give you a token (keep it secret!)")
	fmt.Println()
	fmt.Println("Enter your bot token when ready.")
	fmt.Println("(Type 'cancel' to abort)")
	fmt.Println()

	// The token is entered once; if validation fails the user gets one more
	// chance to correct a typo before we fall back to preserving a working
	// existing token (or disabling Telegram on a fresh install). The network
	// itself is already retried inside ValidateToken, so this loop is about
	// giving the human a second attempt, not about masking a dead network.
	for prompt := 1; prompt <= 2; prompt++ {
		fmt.Printf("Bot token: ")

		var token string
		fmt.Scanln(&token)

		if token == "cancel" || token == "" {
			if existingEnabled && existingToken != "" {
				// Aborting a token change must not disconnect a working bot:
				// returning nil here would make runOnboard save the config with
				// Telegram disabled.
				fmt.Println("\nTelegram setup cancelled. Keeping the existing Telegram configuration.")
				return &config.TelegramConfig{
					Enabled:   true,
					Token:     existingToken,
					AllowFrom: existingAllowFrom,
				}
			}
			fmt.Println("\nTelegram setup cancelled.")
			return nil
		}

		// Sanitize token: strip control characters and escape sequences
		token = strings.TrimSpace(sanitizeToken(token))

		fmt.Println("\nValidating token...")
		if err := validateTelegramToken(token); err != nil {
			fmt.Printf("Token validation failed: %v\n", err)
			if channels.IsNetworkError(err) {
				fmt.Println("This looks like a network problem — joshbot could not reach the Telegram API.")
				fmt.Println("Check your internet connection and proxy settings.")
			} else {
				fmt.Println("The token was rejected by Telegram, or is not in the expected <numeric-id>:<secret> format.")
				fmt.Println("Double-check it was copied in full.")
			}
			if prompt == 2 {
				return telegramValidationFailed(existingEnabled, existingToken, existingAllowFrom)
			}
			fmt.Println("Please check your token and try again.")
			fmt.Println()
			continue
		}
		fmt.Println("Token validated successfully!")

		fmt.Println("\nAllowed usernames (optional)")
		fmt.Println("Restrict bot access to specific Telegram usernames.")
		fmt.Println("Leave empty to allow anyone to use the bot.")
		fmt.Println()

		// Show existing allow from as default
		defaultUsernames := strings.Join(existingAllowFrom, ", ")
		fmt.Printf("Usernames (comma-separated) [current: %s]: ", defaultUsernames)

		usernamesRaw := readLine()

		var allowFrom []string
		// Use existing if no new input
		if usernamesRaw == "" && len(existingAllowFrom) > 0 {
			allowFrom = existingAllowFrom
		} else if usernamesRaw != "" {
			for _, u := range strings.Split(usernamesRaw, ",") {
				u = strings.TrimSpace(u)
				if u != "" {
					if !strings.HasPrefix(u, "@") {
						u = "@" + u
					}
					allowFrom = append(allowFrom, u)
				}
			}
		}

		fmt.Println("\nTelegram configured!")

		return &config.TelegramConfig{
			Enabled:   true,
			Token:     token,
			AllowFrom: allowFrom,
		}
	}
	return telegramValidationFailed(existingEnabled, existingToken, existingAllowFrom)
}

// validateTelegramToken is the network boundary for setupTelegram's token
// validation. It is a package var so tests can stub it and exercise the wizard
// flow without touching the real Telegram API.
var validateTelegramToken = channels.ValidateToken

// readLine reads a single line from stdin, spaces included. fmt.Scanln stops at
// the first space, which truncates a comma-separated username list typed as
// "@alice, bob" down to just "@alice,". It reads one byte at a time so it never
// buffers ahead into input a later prompt is waiting for.
func readLine() string {
	var sb strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(buf)
		if n == 0 || err != nil {
			break
		}
		if buf[0] == '\n' {
			break
		}
		sb.WriteByte(buf[0])
	}
	return strings.TrimRight(sb.String(), "\r")
}

// telegramValidationFailed decides what to do when a freshly-entered token
// could not be validated after both attempts. An existing working token is
// preserved — a transient network failure must not silently disconnect a live
// bot, which is what happened when the old code returned nil and the config
// was saved with Telegram disabled. On a fresh install Telegram is left
// disabled with a clear message instead.
func telegramValidationFailed(existingEnabled bool, existingToken string, existingAllowFrom []string) *config.TelegramConfig {
	if existingEnabled && existingToken != "" {
		fmt.Println("\nKeeping the existing Telegram configuration: the new token could not be validated.")
		return &config.TelegramConfig{
			Enabled:   true,
			Token:     existingToken,
			AllowFrom: existingAllowFrom,
		}
	}
	fmt.Println("\nTelegram setup skipped: no token could be validated.")
	return &config.TelegramConfig{
		Enabled:   false,
		Token:     "",
		AllowFrom: []string{},
	}
}

func promptServiceInstall() bool {
	fmt.Println("\n[Step 6] Service Installation")
	fmt.Println("Install joshbot as a background service?")
	fmt.Println()
	fmt.Println("This allows joshbot to:")
	fmt.Println("  - Start automatically on boot")
	fmt.Println("  - Run in the background continuously")
	fmt.Println("  - Be managed with: joshbot service start/stop/status")
	fmt.Println()
	fmt.Println("  1. Yes, install as service")
	fmt.Println("  2. No, I'll run it manually")
	fmt.Println()
	fmt.Printf("Choice [2]: ")

	var choice string
	fmt.Scanln(&choice)

	return choice == "1"
}

func promptCronStartupFallback() error {
	if runtime.GOOS != "linux" {
		return nil
	}

	fmt.Println("\nAutomatic startup fallback")
	fmt.Println("I can install a cron @reboot entry to start joshbot on boot.")
	fmt.Println("  1. Yes, install cron startup fallback")
	fmt.Println("  2. No, I will configure startup manually")
	fmt.Printf("Choice [2]: ")

	var choice string
	fmt.Scanln(&choice)
	if choice != "1" {
		return nil
	}

	if err := installCronStartupEntry(); err != nil {
		return err
	}

	fmt.Println("Cron startup fallback installed.")
	return nil
}

func installCronStartupEntry() error {
	if _, err := exec.LookPath("crontab"); err != nil {
		return fmt.Errorf("crontab not found")
	}

	execPath, err := osExecutable()
	if err != nil {
		return fmt.Errorf("failed to detect executable path: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to detect home directory: %w", err)
	}

	logPath := filepath.Join(home, ".joshbot", "logs", "gateway.log")
	// 0700, like every other directory under ~/.joshbot: the gateway log
	// carries conversation content and tool output.
	if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	entry := fmt.Sprintf("@reboot %s gateway >> %s 2>&1", execPath, logPath)

	existing, err := exec.Command("crontab", "-l").CombinedOutput()
	existingText := strings.TrimSpace(string(existing))
	if err != nil && existingText != "" && !strings.Contains(existingText, "no crontab for") {
		return fmt.Errorf("failed to read existing crontab: %w", err)
	}

	if strings.Contains(existingText, entry) {
		return nil
	}

	var newCron string
	if existingText == "" || strings.Contains(existingText, "no crontab for") {
		newCron = entry + "\n"
	} else {
		newCron = existingText + "\n" + entry + "\n"
	}

	cmd := exec.Command("crontab", "-")
	cmd.Stdin = strings.NewReader(newCron)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to install cron entry: %w: %s", err, strings.TrimSpace(string(out)))
	}

	return nil
}

func doServiceInstall() error {
	svc, err := newServiceManager(service.Config{
		Name:        "joshbot",
		DisplayName: "joshbot AI Assistant",
		Description: "Personal AI assistant with Telegram integration",
	})
	if err != nil {
		return err
	}

	fmt.Println("\nInstalling service...")
	result, err := svc.Install()
	if err != nil {
		return err
	}

	fmt.Println("Service installed successfully!")
	if result.Message != "" {
		fmt.Printf("  %s\n", result.Message)
	}

	fmt.Println("\nStarting service...")
	if err := svc.Start(); err != nil {
		fmt.Printf("Warning: Could not start service: %v\n", err)
		fmt.Println("Try: joshbot service start")
	} else {
		fmt.Println("Service started!")
	}

	if result.LogPath != "" {
		fmt.Printf("\nLogs: %s\n", result.LogPath)
	}

	return nil
}

// createWorkspaceFiles creates the workspace files (SOUL.md, USER.md, etc.)
// and memory files in the workspace directory.
func createWorkspaceFiles(cfg *config.Config, soulContent string) error {
	wsDir := cfg.WorkspaceDir()

	// SOUL.md - write the personality content
	soulPath := filepath.Join(wsDir, "SOUL.md")
	if _, err := os.Stat(soulPath); os.IsNotExist(err) {
		if err := os.WriteFile(soulPath, []byte(soulContent), 0644); err != nil {
			return fmt.Errorf("failed to write SOUL.md: %w", err)
		}
	}

	// USER.md
	userContent := `# User Profile

## About You
- Name: (your name)
- Location: (your location)
- Timezone: (your timezone)

## Preferences
- (add your preferences here)

## Current Projects
- (what are you working on?)

## Notes
- (anything else joshbot should know)
`
	if err := os.WriteFile(filepath.Join(wsDir, "USER.md"), []byte(userContent), 0644); err != nil {
		return fmt.Errorf("failed to write USER.md: %w", err)
	}

	// AGENTS.md
	agentsContent := `# Agent Instructions

## General Guidelines
- Be helpful and proactive
- Use tools to verify information when possible
- Keep responses appropriately detailed
- Remember context from previous conversations using the memory system
- Create skills when you learn new capabilities

## Tool Usage
- Prefer reading files before editing them
- Use shell commands carefully (safety guards are active)
- Search the web when you need current information
- Update memory when you learn something important about the user
`
	if err := os.WriteFile(filepath.Join(wsDir, "AGENTS.md"), []byte(agentsContent), 0644); err != nil {
		return fmt.Errorf("failed to write AGENTS.md: %w", err)
	}

	// IDENTITY.md
	identityContent := `# Identity

I am joshbot, a personal AI assistant.
I am always learning and improving through conversations.
I remember important information across sessions.
I can create new skills to extend my capabilities.
`
	if err := os.WriteFile(filepath.Join(wsDir, "IDENTITY.md"), []byte(identityContent), 0644); err != nil {
		return fmt.Errorf("failed to write IDENTITY.md: %w", err)
	}

	// Initialize memory files
	memDir := filepath.Join(wsDir, "memory")
	if err := os.MkdirAll(memDir, 0700); err != nil {
		return fmt.Errorf("failed to create memory directory: %w", err)
	}

	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("# Memory\n\nImportant information about the user:\n"), 0644); err != nil {
		return fmt.Errorf("failed to write MEMORY.md: %w", err)
	}

	if err := os.WriteFile(filepath.Join(memDir, "HISTORY.md"), []byte("# History\n\n"), 0644); err != nil {
		return fmt.Errorf("failed to write HISTORY.md: %w", err)
	}

	return nil
}

// runPreflight reports whether joshbot would start, and with what, without
// dialling anything.
//
// It uses LoadStrictFrom rather than loadConfig on purpose: Load replaces an
// unusable config with defaults, which would have this command cheerfully
// describe a config the operator never wrote while the file in front of them is
// the broken one.
func runPreflight(c *cli.Context) error {
	format, err := outputFormat(c)
	if err != nil {
		return err
	}

	cfg, loadErr := config.LoadStrictFrom(c.Path("config"))
	if cfg == nil {
		return loadErr
	}

	configErr := ""
	if loadErr != nil {
		configErr = loadErr.Error()
	}
	// A profile failure is reported through the document rather than returned,
	// because describing why the run would not start is exactly this command's
	// job — returning here would print a bare error and no report at all. Only
	// attempted when the config itself loaded; applying a profile on top of a
	// broken config would report the profile as the cause of a problem it did
	// not create.
	if loadErr == nil {
		if err := applyProfile(c, cfg); err != nil && configErr == "" {
			configErr = err.Error()
		}
	}
	// NewPreflight redacts the free-text fields as it builds the document, so
	// this output is safe to paste into an issue in either format.
	doc := output.NewPreflight(config.Preflight(cfg), configErr)

	if format == output.JSON {
		if err := output.WriteJSON(jsonWriter(), doc); err != nil {
			return err
		}
		if doc.OK {
			return nil
		}
		// Same contract as the text path: the document above is the whole
		// report, so exit non-zero without printing a second diagnosis.
		return cli.Exit("", 1)
	}

	// Same redaction as `joshbot status`: this output exists to be pasted into
	// an issue, so a home directory (which carries the account name) and any
	// credential-shaped value are stripped first.
	output.RenderPreflightText(reportWriter(), doc)

	if doc.OK {
		return nil
	}

	problem, _ := doc.FirstProblem()
	if problem == "" && loadErr != nil {
		// A config the resolver could not even reach: the load error is the
		// whole diagnosis, and reporting nothing here would exit non-zero with
		// no reason attached.
		return loadErr
	}
	// The "NOT OK — ..." line is printed by RenderPreflightText above.
	// cli.Exit rather than a plain error: the message is already printed above
	// in full, and urfave/cli would otherwise print it a second time.
	return cli.Exit("", 1)
}

// runStatus displays the current configuration and status.
func runStatus(c *cli.Context) error {
	format, err := outputFormat(c)
	if err != nil {
		return err
	}

	cfg, err := loadConfig(c.Path("config"))
	if err != nil {
		return err
	}

	configExists := false
	configPath := filepath.Join(config.DefaultHome, "config.json")
	if _, err := os.Stat(configPath); err == nil {
		configExists = true
	}

	wsExists := false
	if _, err := os.Stat(cfg.WorkspaceDir()); err == nil {
		wsExists = true
	}

	// Check memory files
	memoryPath := filepath.Join(cfg.WorkspaceDir(), "memory", "MEMORY.md")
	historyPath := filepath.Join(cfg.WorkspaceDir(), "memory", "HISTORY.md")

	var memorySize, historySize int64
	if memStats, err := os.Stat(memoryPath); err == nil {
		memorySize = memStats.Size()
	}
	if histStats, err := os.Stat(historyPath); err == nil {
		historySize = histStats.Size()
	}

	// Paths are stripped of the home directory here rather than relying on the
	// output writer: the JSON form is not written through the redactor (see the
	// internal/output package comment), so a raw path would leak the account
	// name into a document meant to be pasted into an issue.
	doc := output.Status{
		SchemaVersion:       output.SchemaVersion,
		Version:             Version,
		ConfigPath:          redact.HomePath(configPath),
		ConfigExists:        configExists,
		Workspace:           redact.HomePath(cfg.WorkspaceDir()),
		WorkspaceExists:     wsExists,
		SessionsDir:         redact.HomePath(cfg.SessionsDir()),
		ConfigFormat:        output.FormatLegacy,
		Model:               cfg.Agents.Defaults.Model,
		MaxTokens:           cfg.Agents.Defaults.MaxTokens,
		Temperature:         cfg.Agents.Defaults.Temperature,
		MemoryWindow:        cfg.Agents.Defaults.MemoryWindow,
		TelegramEnabled:     cfg.Channels.Telegram.Enabled,
		WorkspaceRestricted: cfg.Tools.RestrictToWorkspace,
		PendingSkills:       pendingSkillNames(cfg),
		MemoryBytes:         memorySize,
		HistoryBytes:        historySize,
	}
	if cfg.UseModelsConfig() {
		doc.ConfigFormat = output.FormatModelCentric
		doc.Model = cfg.ModelsConfig.Agent.Model
		doc.Fallback = cfg.ModelsConfig.Agent.Fallback
		for _, m := range cfg.ModelsConfig.Models {
			if !m.Disabled {
				doc.Models = append(doc.Models, m.Name)
			}
		}
	} else {
		doc.Providers = providerStatuses(cfg.Providers)
	}

	if format == output.JSON {
		return output.WriteJSON(jsonWriter(), doc)
	}

	output.RenderStatusText(reportWriter(), doc)
	return nil
}

// providerStatuses maps the legacy providers map onto the reporting struct,
// mirroring the registration gates in setupComponents so status never claims a
// provider is configured when it is actually inert. See issue #71.
func providerStatuses(providersCfg map[string]config.ProviderConfig) []output.ProviderStatus {
	names := make([]string, 0, len(providersCfg))
	for name := range providersCfg {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]output.ProviderStatus, 0, len(names))
	for _, name := range names {
		p := providersCfg[name]
		switch {
		case !p.Enabled:
			out = append(out, output.ProviderStatus{Name: name, Reason: output.ReasonNotEnabled})
		case providerRequiresAPIKey(name) && p.APIKey == "":
			out = append(out, output.ProviderStatus{Name: name, Reason: output.ReasonNoAPIKey})
		default:
			out = append(out, output.ProviderStatus{Name: name, Usable: true})
		}
	}
	return out
}

// runConfigure handles the configure command.
func runConfigure(c *cli.Context) error {
	// Via loadConfig, not config.Load: otherwise --config selects the file
	// this command reads for display but not the one it writes to.
	cfg, err := loadConfig(c.Path("config"))
	if err != nil {
		return err
	}

	conf := configure.New(cfg)

	if c.Bool("list") {
		format, err := outputFormat(c)
		if err != nil {
			return err
		}
		return listProviders(cfg, format)
	}

	if provider := c.String("remove"); provider != "" {
		if err := conf.RemoveProvider(provider); err != nil {
			return err
		}
		fmt.Printf("Removed provider %q.\n", provider)
		return flushConfig(cfg)
	}

	if c.IsSet("provider") {
		opts := configure.ProviderOptions{
			Name:   c.String("provider"),
			APIKey: c.String("api-key"),
			Model:  c.String("model"),
		}
		if c.IsSet("api-base") {
			opts.APIBase = c.String("api-base")
		}
		if err := conf.ConfigureProvider(opts); err != nil {
			return err
		}
		// Report the model that will actually be used. When --model is omitted
		// the provider entry is left empty and the effective model comes from
		// the agent defaults or the provider's registered default; printing the
		// empty provider field made a working setup look broken.
		effectiveModel := cfg.Providers[opts.Name].Model
		if effectiveModel == "" {
			effectiveModel = cfg.Agents.Defaults.Model
		}
		if effectiveModel == "" {
			effectiveModel = providers.GetDefaultModel(opts.Name)
		}
		fmt.Printf("Provider %q configured with model %q.\n", opts.Name, effectiveModel)
	}

	if provider := c.String("set-default"); provider != "" {
		if err := conf.SetDefault(provider); err != nil {
			return err
		}
		fmt.Printf("Default provider set to %q with model %q.\n", provider, cfg.Agents.Defaults.Model)
	}

	if c.IsSet("provider") || c.IsSet("set-default") {
		return flushConfig(cfg)
	}

	return runConfigureWizard(cfg)
}

func flushConfig(cfg *config.Config) error {
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Println("Configuration saved.")
	return nil
}

// listProviders displays the configured providers.
func listProviders(cfg *config.Config, format output.Format) error {
	names := []string{"nvidia", "openrouter", "groq", "ollama", "github-copilot", "poolside"}
	defaultProvider := cfg.ProviderDefaults.Default

	doc := output.Providers{
		SchemaVersion: output.SchemaVersion,
		Default:       defaultProvider,
		Providers:     make([]output.ConfiguredProvider, 0, len(names)),
	}
	for _, name := range names {
		p, exists := cfg.Providers[name]
		status := output.ProviderNotConfigured

		if exists && p.Enabled {
			switch {
			case name == "github-copilot":
				if copilotAuthenticated() {
					status = output.ProviderAuthenticated
				} else {
					status = output.ProviderOAuthRequired
				}
			case p.APIKey != "":
				status = output.ProviderConfigured
			}
		}

		doc.Providers = append(doc.Providers, output.ConfiguredProvider{
			Name:      name,
			Status:    status,
			IsDefault: name == defaultProvider,
		})
	}

	out := reportWriter()
	if format == output.JSON {
		return output.WriteJSON(out, doc)
	}
	output.RenderProvidersText(out, doc)
	return nil
}

// copilotAuthenticated reports whether a usable GitHub Copilot token is on
// disk. Presence only — the token itself never leaves this function.
func copilotAuthenticated() bool {
	homeDir, err := copilot.GetHomeDir()
	if err != nil {
		return false
	}
	token, err := copilot.LoadToken(homeDir)
	return err == nil && token != nil && token.AccessToken != ""
}

// runConfigureWizard runs the interactive provider configuration wizard.
func runConfigureWizard(cfg *config.Config) error {
	providers := []string{"nvidia", "openrouter", "groq", "ollama", "github-copilot", "poolside"}

	for {
		// Display current state
		fmt.Println()
		fmt.Println("╔═══════════════════════════════════════════╗")
		fmt.Println("║        Configure LLM Providers           ║")
		fmt.Println("╚═══════════════════════════════════════════╝")
		fmt.Println()
		fmt.Println("Current providers:")

		defaultProvider := cfg.ProviderDefaults.Default
		for _, name := range providers {
			p, exists := cfg.Providers[name]
			isDefault := name == defaultProvider
			icon := "○"
			status := "not configured"

			if exists && p.Enabled {
				if name == "github-copilot" {
					homeDir, _ := copilot.GetHomeDir()
					token, err := copilot.LoadToken(homeDir)
					if err == nil && token != nil && token.AccessToken != "" {
						icon = "✓"
						status = "authenticated"
					} else {
						icon = "○"
						status = "OAuth required"
					}
				} else if p.APIKey != "" {
					icon = "✓"
					status = "configured"
				}
			}
			if isDefault {
				status += " (default)"
			}

			fmt.Printf("  %s %s (%s)\n", icon, configure.GetProviderDisplayName(name), status)
		}

		fmt.Println()
		fmt.Println("What would you like to do?")
		fmt.Println("  1. Configure NVIDIA NIM")
		fmt.Println("  2. Configure OpenRouter")
		fmt.Println("  3. Configure Groq")
		fmt.Println("  4. Configure Ollama")
		fmt.Println("  5. Configure GitHub Copilot")
		fmt.Println("  6. Configure Poolside")
		fmt.Println("  7. Set default provider")
		fmt.Println("  8. Configure fallback order")
		fmt.Println("  9. Done")
		fmt.Println()

		fmt.Print("Choice [9]: ")

		// On EOF (closed stdin) Scanln leaves choice empty, which takes the
		// default and saves-and-exits — the wizard must never spin on
		// "Invalid choice" against a stdin that will never produce one.
		var choice string
		fmt.Scanln(&choice)
		if choice == "" {
			choice = "9"
		}

		switch choice {
		case "1":
			cfg = configureProvider(cfg, "nvidia")
		case "2":
			cfg = configureProvider(cfg, "openrouter")
		case "3":
			cfg = configureProvider(cfg, "groq")
		case "4":
			cfg = configureProvider(cfg, "ollama")
		case "5":
			cfg = configureProvider(cfg, "github-copilot")
		case "6":
			cfg = configureProvider(cfg, "poolside")
		case "7":
			cfg = setDefaultProvider(cfg)
		case "8":
			cfg = configureFallbackOrder(cfg)
		case "9":
			// Save and exit
			if err := config.Save(cfg); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}
			fmt.Println("\nConfiguration saved.")
			return nil
		default:
			fmt.Println("Invalid choice. Please try again.")
		}
	}
}

// configureProvider prompts the user to configure a specific provider.
func configureProvider(cfg *config.Config, provider string) *config.Config {
	// Initialize providers map if needed
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]config.ProviderConfig)
	}

	p, exists := cfg.Providers[provider]

	fmt.Printf("\n=== Configure %s ===\n", configure.GetProviderDisplayName(provider))
	fmt.Println()

	// Get API key (skip for OAuth-based providers)
	var apiKey string
	if provider != "github-copilot" {
		fmt.Print("API key")
		if exists && p.APIKey != "" {
			fmt.Printf(" [%s]", configure.MaskAPIKey(p.APIKey))
		}
		fmt.Print(": ")

		fmt.Scanln(&apiKey)
		apiKey = strings.TrimSpace(apiKey)

		// If user entered something, use it; otherwise keep existing
		if apiKey != "" {
			p.APIKey = apiKey
		}
	}

	// Get API base URL (different for each provider)
	var apiBase string
	switch provider {
	case "openrouter":
		if exists && p.APIBase != "" {
			fmt.Printf("API base URL [%s]: ", p.APIBase)
		} else {
			fmt.Print("API base URL [https://openrouter.ai/api/v1]: ")
		}
		fmt.Scanln(&apiBase)
		if apiBase == "" {
			if p.APIBase == "" {
				apiBase = "https://openrouter.ai/api/v1"
			} else {
				apiBase = p.APIBase
			}
		}
		p.APIBase = strings.TrimSpace(apiBase)

		defaultModel := providers.GetDefaultModel("openrouter")
		if exists && p.Model != "" {
			defaultModel = p.Model
		}
		models, err := providers.ListModels(providers.Config{
			APIKey:  p.APIKey,
			APIBase: p.APIBase,
		})
		if err != nil {
			fmt.Printf("\nCould not fetch models: %v\n", err)
			fmt.Printf("Model (default: %s): ", defaultModel)
		} else if len(models) > 0 {
			selected := promptModelSelection(models, defaultModel)
			p.Model = selected
		} else {
			fmt.Printf("Model (default: %s): ", defaultModel)
		}
		var modelInput string
		fmt.Scanln(&modelInput)
		if modelInput == "" && p.Model == "" {
			p.Model = defaultModel
		} else if modelInput != "" {
			p.Model = strings.TrimSpace(modelInput)
		}
	case "nvidia":
		if exists && p.APIBase != "" {
			fmt.Printf("API base URL [%s]: ", p.APIBase)
		} else {
			fmt.Print("API base URL [https://integrate.api.nvidia.com/v1]: ")
		}
		fmt.Scanln(&apiBase)
		if apiBase == "" {
			if p.APIBase == "" {
				apiBase = "https://integrate.api.nvidia.com/v1"
			} else {
				apiBase = p.APIBase
			}
		}
		p.APIBase = strings.TrimSpace(apiBase)

		fmt.Println("\nNote: NVIDIA NIM does not support model listing via API.")
		fmt.Printf("Available models: https://docs.nvidia.com/nim/large-language-models/1.15.0/models.html\n")
		defaultModel := providers.GetDefaultModel("nvidia")
		if exists && p.Model != "" {
			defaultModel = p.Model
		}
		fmt.Printf("Model (default: %s): ", defaultModel)
		var modelInput string
		fmt.Scanln(&modelInput)
		if modelInput == "" {
			modelInput = defaultModel
		}
		p.Model = strings.TrimSpace(modelInput)
	case "groq":
		if exists && p.APIBase != "" {
			fmt.Printf("API base URL [%s]: ", p.APIBase)
		} else {
			fmt.Print("API base URL [https://api.groq.com/openai/v1]: ")
		}
		fmt.Scanln(&apiBase)
		if apiBase == "" {
			if p.APIBase == "" {
				apiBase = "https://api.groq.com/openai/v1"
			} else {
				apiBase = p.APIBase
			}
		}
		p.APIBase = strings.TrimSpace(apiBase)

		defaultModel := providers.GetDefaultModel("groq")
		if exists && p.Model != "" {
			defaultModel = p.Model
		}
		models, err := providers.ListModels(providers.Config{
			APIKey:  p.APIKey,
			APIBase: p.APIBase,
		})
		if err != nil {
			fmt.Printf("\nCould not fetch models: %v\n", err)
			fmt.Printf("Model (default: %s): ", defaultModel)
		} else if len(models) > 0 {
			selected := promptModelSelection(models, defaultModel)
			p.Model = selected
		} else {
			fmt.Printf("Model (default: %s): ", defaultModel)
		}
		var modelInput string
		fmt.Scanln(&modelInput)
		if modelInput == "" && p.Model == "" {
			p.Model = defaultModel
		} else if modelInput != "" {
			p.Model = strings.TrimSpace(modelInput)
		}
	case "poolside":
		// Taken from the registry, not written out again here: the previous
		// hardcoded default was "https://api.poolside.ai/v1", a host that does
		// not resolve, so the wizard handed every user a broken config.
		poolsideBase := providers.GetDefaultAPIBaseFor("poolside")
		if exists && p.APIBase != "" {
			fmt.Printf("API base URL [%s]: ", p.APIBase)
		} else {
			fmt.Printf("API base URL [%s]: ", poolsideBase)
		}
		fmt.Scanln(&apiBase)
		if apiBase == "" {
			if p.APIBase == "" {
				apiBase = poolsideBase
			} else {
				apiBase = p.APIBase
			}
		}
		p.APIBase = strings.TrimSpace(apiBase)

		defaultModel := providers.GetDefaultModel("poolside")
		if exists && p.Model != "" {
			defaultModel = p.Model
		}
		models, err := providers.ListModels(providers.Config{
			APIKey:  p.APIKey,
			APIBase: p.APIBase,
		})
		if err != nil {
			fmt.Printf("\nCould not fetch models: %v\n", err)
			fmt.Printf("Model (default: %s): ", defaultModel)
		} else if len(models) > 0 {
			selected := promptModelSelection(models, defaultModel)
			p.Model = selected
		} else {
			fmt.Printf("Model (default: %s): ", defaultModel)
		}
		var modelInput string
		fmt.Scanln(&modelInput)
		if modelInput == "" && p.Model == "" {
			p.Model = defaultModel
		} else if modelInput != "" {
			p.Model = strings.TrimSpace(modelInput)
		}
	case "ollama":
		if exists && p.APIBase != "" {
			fmt.Printf("Ollama base URL [%s]: ", p.APIBase)
		} else {
			fmt.Print("Ollama base URL [http://localhost:11434]: ")
		}
		fmt.Scanln(&apiBase)
		if apiBase == "" {
			if p.APIBase == "" {
				apiBase = "http://localhost:11434"
			} else {
				apiBase = p.APIBase
			}
		}
		p.APIBase = strings.TrimSpace(apiBase)

		ollamaClient := providers.NewOllamaClient(p.APIBase)
		models, err := ollamaClient.ListModels()
		if err != nil {
			fmt.Printf("\nCould not fetch models from Ollama: %v\n", err)
		}

		modelName := promptOllamaModelSelection(models)
		if modelName == "" && exists && p.Model != "" {
			modelName = p.Model
		}
		if modelName == "" {
			fmt.Print("Enter model name: ")
			fmt.Scanln(&modelName)
			modelName = strings.TrimSpace(modelName)
		}
		p.Model = modelName

		timeoutSecs := 300
		if exists && p.Timeout > 0 {
			timeoutSecs = int(p.Timeout.Seconds())
		}
		fmt.Printf("Timeout in seconds (CPU models need longer) [%d]: ", timeoutSecs)
		var timeoutInput string
		fmt.Scanln(&timeoutInput)
		if timeoutInput != "" {
			fmt.Sscanf(timeoutInput, "%d", &timeoutSecs)
		}
		p.Timeout = time.Duration(timeoutSecs) * time.Second

		fmt.Println(`=== Ollama Tips ===
• CPU-only: Pull smaller models: ollama pull llama3.2:3b
• List models: ollama list
• Test model: ollama run <model-name>
• Check GPU: ollama run llama3.2 (faster with GPU)`)
	case "github-copilot":
		fmt.Println("GitHub Copilot uses OAuth authentication.")
		fmt.Println("You will be shown a URL and code to authorize.")
		fmt.Println()

		homeDir, _ := copilot.GetHomeDir()
		token, err := copilot.LoadToken(homeDir)
		if err == nil && token != nil && token.AccessToken != "" {
			fmt.Println("Already authenticated with GitHub Copilot.")
			fmt.Println("Run 'joshbot auth github-copilot' to re-authenticate if needed.")
		} else {
			ctx := context.Background()
			_, err = copilotRunDeviceFlow(ctx)
			if err != nil {
				fmt.Printf("OAuth failed: %v\n", err)
				return cfg
			}
			// Token already saved by RunDeviceFlow
			fmt.Println("\nSuccessfully authenticated with GitHub Copilot!")
		}

		fmt.Println("\nNote: GitHub Copilot is configured via OAuth.")
		fmt.Println("The access token is stored securely in ~/.joshbot/auth.json")

		token, err = copilot.LoadToken(homeDir)
		if err == nil && token != nil && token.AccessToken != "" {
			defaultModel := providers.GetDefaultModel("github-copilot")
			if exists && p.Model != "" {
				defaultModel = p.Model
			}
			var models []string
			models, err = copilotListModels(token.AccessToken)
			if err != nil {
				fmt.Printf("\nCould not fetch models: %v\n", err)
				fmt.Printf("Model (default: %s): ", defaultModel)
			} else if len(models) > 0 {
				selected := promptModelSelection(models, defaultModel)
				p.Model = selected
			} else {
				fmt.Printf("Model (default: %s): ", defaultModel)
			}
			var modelInput string
			fmt.Scanln(&modelInput)
			if modelInput == "" && p.Model == "" {
				p.Model = defaultModel
			} else if modelInput != "" {
				p.Model = strings.TrimSpace(modelInput)
			}
		}
	}

	// Validate credentials if API key was provided
	if p.APIKey != "" {
		fmt.Println("\nValidating credentials...")
		if err := validateProviderCredentials(provider, p.APIKey, p.APIBase); err != nil {
			fmt.Printf("Warning: %v\n", err)
			fmt.Print("Save anyway? (y/N): ")
			var confirm string
			fmt.Scanln(&confirm)
			if strings.ToLower(confirm) != "y" {
				return cfg
			}
		} else {
			fmt.Println("Credentials validated successfully!")
		}
	}

	p.Enabled = true
	cfg.Providers[provider] = p

	// If this is the first provider, set it as default with its model
	// Otherwise, if this is the current default provider, update the model
	if cfg.ProviderDefaults.Default == "" {
		cfg.ProviderDefaults.Default = provider
		if p.Model != "" {
			cfg.Agents.Defaults.Model = p.Model
		} else {
			cfg.Agents.Defaults.Model = providers.GetDefaultModel(provider)
		}
	} else if cfg.ProviderDefaults.Default == provider && p.Model != "" {
		cfg.Agents.Defaults.Model = p.Model
	}

	fmt.Println()
	return cfg
}

func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func promptOllamaModelSelection(models []providers.ModelInfo) string {
	if len(models) == 0 {
		return ""
	}

	fmt.Println("\nAvailable models:")
	for i, m := range models {
		sizeStr := ""
		if m.Size > 0 {
			sizeStr = fmt.Sprintf(" (%s)", formatSize(m.Size))
		}
		fmt.Printf("  %d. %s%s\n", i+1, m.Name, sizeStr)
	}
	fmt.Println()

	fmt.Printf("Enter number (1-%d) or model name: ", len(models))

	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	if num, err := strconv.Atoi(input); err == nil && num >= 1 && num <= len(models) {
		return models[num-1].Name
	}

	return input
}

var modelListStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("86"))
var selectedModelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
var helpStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

func promptModelSelection(models []string, defaultModel string) string {
	if len(models) == 0 {
		return defaultModel
	}

	reader := bufio.NewReader(os.Stdin)

	filtered := models
	filter := ""

	for {
		fmt.Println()
		fmt.Printf("Available models (default: %s):\n", defaultModel)
		if filter != "" {
			fmt.Printf("  Filter: %s (%d matches)\n", filter, len(filtered))
		}
		fmt.Println(helpStyle.Render("  Type number to select, or text to filter (Enter for default):"))
		fmt.Println()

		maxShow := 15
		if len(filtered) > maxShow {
			for i, m := range filtered[:maxShow] {
				if m == defaultModel {
					fmt.Printf("  %d. %s %s\n", i+1, selectedModelStyle.Render(m), helpStyle.Render("(default)"))
				} else {
					fmt.Printf("  %d. %s\n", i+1, modelListStyle.Render(m))
				}
			}
			fmt.Printf("\n  ... and %d more (type more to filter, or number to select)\n", len(filtered)-maxShow)
		} else {
			for i, m := range filtered {
				if m == defaultModel {
					fmt.Printf("  %d. %s %s\n", i+1, selectedModelStyle.Render(m), helpStyle.Render("(default)"))
				} else {
					fmt.Printf("  %d. %s\n", i+1, modelListStyle.Render(m))
				}
			}
		}

		fmt.Println()
		fmt.Print("> ")

		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input == "" {
			return defaultModel
		}

		if num, err := strconv.Atoi(input); err == nil && num >= 1 && num <= len(filtered) {
			return filtered[num-1]
		}

		newFilter := input
		if newFilter == filter {
			if len(filtered) == 1 {
				return filtered[0]
			}
			fmt.Println(helpStyle.Render("  Press Enter for default or type a number"))
			continue
		}

		filtered = filterModels(models, newFilter)
		filter = newFilter

		if len(filtered) == 0 {
			fmt.Printf("\n  No models match '%s', showing all\n\n", filter)
			filtered = models
			filter = ""
		}
	}
}

func filterModels(models []string, filter string) []string {
	filterLower := strings.ToLower(filter)
	var result []string
	for _, m := range models {
		if strings.Contains(strings.ToLower(m), filterLower) {
			result = append(result, m)
		}
	}
	return result
}

// validateProviderCredentials tests the API credentials for a provider.
func validateProviderCredentials(provider, apiKey, apiBase string) error {
	switch provider {
	case "openrouter", "groq", "nvidia", "poolside":
		// Test call to list models
		req, err := http.NewRequest("GET", apiBase+"/models", nil)
		if err != nil {
			return fmt.Errorf("connection failed: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("connection failed: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == 401 {
			return fmt.Errorf("invalid API key")
		}
		if resp.StatusCode != 200 {
			return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		}
		return nil
	case "ollama":
		resp, err := http.Get(apiBase + "/api/tags")
		if err != nil {
			return fmt.Errorf("cannot connect to Ollama at %s", apiBase)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		}
		return nil
	}
	return nil
}

// setDefaultProvider prompts the user to select the default provider.
func setDefaultProvider(cfg *config.Config) *config.Config {
	// Find configured providers
	var configured []string
	homeDir, _ := copilot.GetHomeDir()
	for name, p := range cfg.Providers {
		if name == "github-copilot" {
			// Check for OAuth token
			token, err := copilot.LoadToken(homeDir)
			if err == nil && token != nil && token.AccessToken != "" {
				configured = append(configured, name)
			}
		} else if p.APIKey != "" {
			configured = append(configured, name)
		}
	}

	if len(configured) == 0 {
		fmt.Println("\nNo providers configured yet. Configure a provider first.")
		return cfg
	}

	fmt.Println("\n=== Set Default Provider ===")
	fmt.Println()

	for i, name := range configured {
		marker := " "
		if name == cfg.ProviderDefaults.Default {
			marker = "*"
		}
		fmt.Printf("  %d. %s %s\n", i+1, marker, configure.GetProviderDisplayName(name))
	}
	fmt.Println()

	fmt.Print("Select default provider: ")

	var choice int
	fmt.Scanln(&choice)

	if choice < 1 || choice > len(configured) {
		fmt.Println("Invalid choice.")
		return cfg
	}

	cfg.ProviderDefaults.Default = configured[choice-1]

	// Use user-configured per-provider model if available, else registry default
	if p, ok := cfg.Providers[cfg.ProviderDefaults.Default]; ok && p.Model != "" {
		cfg.Agents.Defaults.Model = p.Model
	} else {
		cfg.Agents.Defaults.Model = providers.GetDefaultModel(cfg.ProviderDefaults.Default)
	}
	fmt.Printf("\nDefault provider set to: %s\n", configure.GetProviderDisplayName(cfg.ProviderDefaults.Default))

	return cfg
}

// configureFallbackOrder prompts the user to configure the fallback order.
func configureFallbackOrder(cfg *config.Config) *config.Config {
	// Find configured providers
	var configured []string
	for name, p := range cfg.Providers {
		if p.APIKey != "" {
			configured = append(configured, name)
		}
	}

	if len(configured) < 2 {
		fmt.Println("\nNeed at least 2 configured providers for fallback.")
		return cfg
	}

	fmt.Println("\n=== Configure Fallback Order ===")
	fmt.Println()
	fmt.Println("Current fallback order:")
	if len(cfg.ProviderDefaults.FallbackOrder) == 0 {
		fmt.Println("  (not set - will use providers as configured)")
	} else {
		for i, name := range cfg.ProviderDefaults.FallbackOrder {
			fmt.Printf("  %d. %s\n", i+1, configure.GetProviderDisplayName(name))
		}
	}
	fmt.Println()
	fmt.Println("Available providers:")
	for i, name := range configured {
		fmt.Printf("  %d. %s\n", i+1, configure.GetProviderDisplayName(name))
	}
	fmt.Println()
	fmt.Print("Enter fallback order (e.g., 1,2,3): ")

	var orderStr string
	fmt.Scanln(&orderStr)
	orderStr = strings.TrimSpace(orderStr)

	if orderStr == "" {
		cfg.ProviderDefaults.FallbackOrder = nil
		fmt.Println("\nFallback order cleared.")
		return cfg
	}

	// Parse order
	var order []string
	parts := strings.Split(orderStr, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if idx, err := strconv.Atoi(part); err == nil && idx >= 1 && idx <= len(configured) {
			order = append(order, configured[idx-1])
		}
	}

	if len(order) == 0 {
		fmt.Println("Invalid order, keeping current.")
		return cfg
	}

	cfg.ProviderDefaults.FallbackOrder = order
	fmt.Println("\nFallback order updated.")

	return cfg
}

// runServiceInstall installs joshbot as a system service.
func runServiceInstall(c *cli.Context) error {
	svc, err := newServiceManager(service.Config{
		Name:        "joshbot",
		DisplayName: "Joshbot AI Assistant",
		Description: "Personal AI assistant with Telegram integration",
	})
	if err != nil {
		return fmt.Errorf("service not supported on this platform: %w", err)
	}

	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════╗")
	fmt.Println("║      Installing joshbot service          ║")
	fmt.Println("╚═══════════════════════════════════════════╝")
	fmt.Println()

	result, err := svc.Install()
	if err != nil {
		return fmt.Errorf("failed to install service: %w", err)
	}

	fmt.Println(result.Message)
	fmt.Println()

	if result.LogPath != "" {
		fmt.Printf("Logs: %s\n", result.LogPath)
	}

	return nil
}

// runServiceUninstall uninstalls the joshbot system service.
func runServiceUninstall(c *cli.Context) error {
	svc, err := newServiceManager(service.Config{
		Name:        "joshbot",
		DisplayName: "Joshbot AI Assistant",
		Description: "Personal AI assistant with Telegram integration",
	})
	if err != nil {
		return fmt.Errorf("service not supported on this platform: %w", err)
	}

	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════╗")
	fmt.Println("║     Uninstalling joshbot service         ║")
	fmt.Println("╚═══════════════════════════════════════════╝")
	fmt.Println()

	result, err := svc.Uninstall()
	if err != nil {
		return fmt.Errorf("failed to uninstall service: %w", err)
	}

	fmt.Println(result.Message)
	return nil
}

// runServiceStatus checks the joshbot service status.
func runServiceStatus(c *cli.Context) error {
	svc, err := newServiceManager(service.Config{
		Name:        "joshbot",
		DisplayName: "Joshbot AI Assistant",
		Description: "Personal AI assistant with Telegram integration",
	})
	if err != nil {
		return fmt.Errorf("service not supported on this platform: %w", err)
	}

	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════╗")
	fmt.Println("║        joshbot service status            ║")
	fmt.Println("╚═══════════════════════════════════════════╝")
	fmt.Println()

	status, err := svc.Status()
	if err != nil {
		fmt.Printf("Status: Unable to determine (%v)\n", err)
		return nil
	}

	// A service that was never installed reports an empty Status string, which
	// printed a bare "Status: " and told the operator nothing.
	statusText := status.Status
	if strings.TrimSpace(statusText) == "" {
		if status.Running {
			statusText = "running"
		} else {
			statusText = "not installed"
		}
	}
	fmt.Printf("Status: %s\n", statusText)
	if status.Running {
		fmt.Println("The service is currently running.")
	} else {
		fmt.Println("The service is not running.")
	}

	return nil
}

// The device flow cannot run under test: it prints a user code, opens a
// browser and polls GitHub until a human approves. Nor can the model list,
// which needs a live Copilot token. Both are package vars so tests can drive
// runAuthCopilot's own branching — the already-authenticated short circuit,
// --force, and the "authenticated but the model list failed" path, which must
// still succeed because the token is already on disk.
var (
	copilotRunDeviceFlow = copilot.RunDeviceFlow
	copilotListModels    = copilot.ListModels
)

func runAuthCopilot(c *cli.Context) error {
	homeDir, err := copilot.GetHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	token, err := copilot.LoadToken(homeDir)
	if err == nil && token != nil && token.AccessToken != "" && !c.Bool("force") {
		fmt.Println("Already authenticated with GitHub Copilot.")
		fmt.Println("Run 'joshbot auth github-copilot --force' to re-authenticate.")
		return nil
	}

	fmt.Println("Starting GitHub Copilot authentication...")
	fmt.Println()

	ctx := context.Background()
	token, err = copilotRunDeviceFlow(ctx)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	if err := copilot.SaveToken(homeDir, token); err != nil {
		return fmt.Errorf("failed to save token: %w", err)
	}

	fmt.Println()
	fmt.Println("Successfully authenticated with GitHub Copilot!")

	fmt.Println("\nFetching available models...")
	models, err := copilotListModels(token.AccessToken)
	if err != nil {
		fmt.Printf("Could not fetch models: %v\n", err)
		fmt.Println("You can configure models later with 'joshbot configure'.")
		return nil
	}

	if len(models) == 0 {
		fmt.Println("No models available.")
		return nil
	}

	defaultModel := providers.GetDefaultModel("github-copilot")
	fmt.Println("\nSelect a model:")
	selected := promptModelSelection(models, defaultModel)

	saveCopilotModel(selected)

	fmt.Println("You can now use 'joshbot agent' with GitHub Copilot.")
	return nil
}

// saveCopilotModel records the chosen model on the github-copilot provider,
// refusing to write at all when the existing config file cannot be read. It
// reports whether the config was saved.
func saveCopilotModel(selected string) bool {
	// LoadStrict, not loadConfig: config.Load answers an unreadable or
	// unparseable file with Defaults() and a *nil* error, so saving what it
	// returned would replace every provider, key and setting the operator had
	// with defaults. A model preference is not worth that.
	cfg, err := config.LoadStrict()
	if err != nil {
		if _, statErr := os.Stat(config.ConfigPath()); statErr == nil {
			// The file is there and unusable. Anything written now is a
			// destructive overwrite of content we could not read.
			fmt.Printf("Warning: existing config could not be read: %v\n", err)
			fmt.Printf("Not saving the model, to avoid overwriting it. Fix the file, then run:\n")
			fmt.Printf("  joshbot configure --provider github-copilot --model %s\n", selected)
			return false
		}
		// No config file yet — nothing to destroy.
		fmt.Println("No config file yet; creating one with your GitHub Copilot settings.")
	}
	if cfg == nil {
		cfg = config.Defaults()
	}
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]config.ProviderConfig)
	}
	// Update in place: replacing the struct would drop any other field the
	// operator had already set on this provider.
	pc := cfg.Providers["github-copilot"]
	pc.Enabled = true
	pc.Model = selected
	cfg.Providers["github-copilot"] = pc

	if err := config.Save(cfg); err != nil {
		fmt.Printf("Warning: Could not save config: %v\n", err)
		return false
	}
	fmt.Printf("\nModel '%s' saved to config.\n", selected)
	return true
}

func runAuthStatus(c *cli.Context) error {
	format, err := outputFormat(c)
	if err != nil {
		return err
	}
	if _, err := copilot.GetHomeDir(); err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	doc := output.Auth{
		SchemaVersion: output.SchemaVersion,
		Providers: []output.AuthedProvider{
			{Name: "github-copilot", Authenticated: copilotAuthenticated()},
		},
	}

	out := reportWriter()
	if format == output.JSON {
		return output.WriteJSON(out, doc)
	}
	output.RenderAuthText(out, doc)
	return nil
}

// Helper functions

// providerRequiresAPIKey reports whether the named legacy provider must have
// a non-empty api_key to be registered by setupComponents. Keep this in sync
// with the gating conditions there: openrouter/nvidia/groq/poolside/custom
// all require p.APIKey != "", while ollama (local server) and github-copilot
// (OAuth token file, not api_key) do not.
// isSupportedProvider reports whether name is one of the providers the guided
// paths know how to configure, i.e. one the runtime can actually dial.
func isSupportedProvider(name string) bool {
	for _, p := range configure.SupportedProviders() {
		if p == name {
			return true
		}
	}
	return false
}

func providerRequiresAPIKey(name string) bool {
	switch name {
	case "ollama", "github-copilot":
		return false
	default:
		return true
	}
}

// noProvidersRegisteredError builds the diagnostic error returned when the
// legacy provider config yields zero registered providers.
//
// It names the actual cause rather than guessing. A provider can fail to
// register for two different reasons, and telling someone to set
// "enabled": true when it is already set is exactly the kind of misleading
// diagnostic issue #71 was filed about. See issue #71.
func noProvidersRegisteredError(providersCfg map[string]config.ProviderConfig) error {
	if len(providersCfg) == 0 {
		return newExitError(exitAuth, "run 'joshbot onboard' to configure a provider",
			fmt.Errorf("no providers configured. Run 'joshbot onboard' first"))
	}

	names := make([]string, 0, len(providersCfg))
	var keyless, unknown []string
	for name, p := range providersCfg {
		names = append(names, name)
		if !p.Enabled {
			continue
		}
		if !isSupportedProvider(name) {
			unknown = append(unknown, name)
			continue
		}
		if providerRequiresAPIKey(name) && p.APIKey == "" {
			keyless = append(keyless, name)
		}
	}
	sort.Strings(names)
	sort.Strings(keyless)
	sort.Strings(unknown)

	// Enabled but not a provider that exists: telling this user to add
	// "enabled": true is doubly wrong — it is already set, and the name is the
	// actual fault.
	if len(unknown) > 0 {
		return newExitError(exitAuth, "fix the provider name in config.json, or run 'joshbot configure'",
			fmt.Errorf(
				"no providers usable: %s enabled but not a known provider — supported providers are: %s",
				strings.Join(unknown, ", "), strings.Join(configure.SupportedProviders(), ", "),
			))
	}

	// Enabled but unusable: the enabled flag is not the problem.
	if len(keyless) > 0 {
		return newExitError(exitAuth, "set an api_key for the provider, or run 'joshbot configure'",
			fmt.Errorf(
				"no providers usable: %s enabled but missing \"api_key\" — set an api_key, or run 'joshbot configure'",
				strings.Join(keyless, ", "),
			))
	}

	return newExitError(exitAuth, "add \"enabled\": true to the provider you want to use, or run 'joshbot configure'",
		fmt.Errorf(
			"no providers enabled: %d provider(s) found in config (%s) but none have \"enabled\": true — add \"enabled\": true to the provider you want to use",
			len(providersCfg), strings.Join(names, ", "),
		))
}

func boolToEnabled(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

func statusBool(b bool) string {
	if b {
		return "(exists)"
	}
	return "(missing)"
}

// maskToken masks a Telegram bot token for display.
// Example: "1234567890:ABCdefGHIjklMNOpqrsTUVwxyz" -> "1234567890:****...****wxyz"
func maskToken(token string) string {
	if token == "" {
		return ""
	}
	if len(token) <= 16 {
		return token[:4] + "****" + token[len(token)-4:]
	}
	// Show first 4 and last 4 characters (tokens are like "id:secret")
	parts := strings.SplitN(token, ":", 2)
	if len(parts) == 2 {
		// Show id and last 4 of secret
		return parts[0] + ":****...****" + parts[1][len(parts[1])-4:]
	}
	// No colon - just show first 4 and last 4
	return token[:4] + "****...****" + token[len(token)-4:]
}

// sanitizeToken removes control characters and escape sequences from input.
// This fixes issues where terminal escape sequences (like \x1b[C) get into the token.
func sanitizeToken(token string) string {
	// Remove common control characters except printable ASCII
	var result strings.Builder
	result.Grow(len(token))

	for _, r := range token {
		// Keep: printable ASCII (32-126), and common non-ASCII that might be valid
		// Remove: control characters (0-31 except tab=9, newline=10, carriage return=13)
		if r >= 32 && r <= 126 {
			result.WriteRune(r)
		}
		// Also keep tab, newline, carriage return if somehow present
		if r == 9 || r == 10 || r == 13 {
			result.WriteRune(r)
		}
	}

	return result.String()
}

// checkExistingInstall checks for existing joshbot installation files.
// Returns whether config.json and workspace directory exist, plus a list of found items.
func checkExistingInstall(homeDir string) (configExists, workspaceExists bool, files []string) {
	// Check for config.json
	configPath := filepath.Join(homeDir, "config.json")
	if _, err := os.Stat(configPath); err == nil {
		configExists = true
		files = append(files, "config.json")
	}

	// Check for workspace directory
	workspacePath := filepath.Join(homeDir, "workspace")
	if _, err := os.Stat(workspacePath); err == nil {
		workspaceExists = true
		files = append(files, "workspace/")
	}

	// Check for memory directory
	memoryPath := filepath.Join(workspacePath, "memory")
	if _, err := os.Stat(memoryPath); err == nil {
		files = append(files, "memory/")
	}

	return configExists, workspaceExists, files
}

// backupExisting creates a timestamped backup of the joshbot home directory.
// Returns the backup path on success, or an error.
func backupExisting(homeDir string) (string, error) {
	// Create backup directory name with timestamp
	backupDir := filepath.Dir(homeDir)
	backupName := fmt.Sprintf(".joshbot.backup.%s", time.Now().Format("2006-01-02-150405"))
	backupPath := filepath.Join(backupDir, backupName)

	// Check if homeDir exists
	if _, err := os.Stat(homeDir); os.IsNotExist(err) {
		return "", fmt.Errorf("directory does not exist: %s", homeDir)
	}

	// Use os.Rename for atomic move on same filesystem
	if err := os.Rename(homeDir, backupPath); err != nil {
		return "", fmt.Errorf("failed to backup directory: %w", err)
	}

	log.Info("Backed up existing installation", "from", homeDir, "to", backupPath)
	return backupPath, nil
}

func getPersonalitySoul(choice string) string {
	switch choice {
	case "1": // Professional
		return `# Soul

## Personality
I am professional, efficient, and focused. I communicate clearly and concisely.
I prioritize getting things done over making conversation.

## Communication Style
- Direct and to-the-point
- Use technical language when appropriate
- Avoid unnecessary pleasantries
- Focus on actionable information

## Values
- Accuracy and correctness
- Efficiency and productivity
- Clear communication
`
	case "2": // Friendly
		return `# Soul

## Personality
I am warm, approachable, and genuinely interested in helping. I enjoy conversation
and like to add a bit of humor when appropriate.

## Communication Style
- Friendly and conversational
- Use analogies and examples to explain things
- Light humor to keep things engaging
- Encouraging and supportive

## Values
- Helpfulness and empathy
- Making complex things accessible
- Building rapport
- Positive energy
`
	case "3": // Sarcastic
		return `# Soul

## Personality
I have a sharp wit and a dry sense of humor. I'm the friend who roasts you
but always has your back. Underneath the sarcasm, I'm deeply helpful.

## Communication Style
- Dry wit and clever observations
- Self-deprecating humor
- Still accurate and helpful with actual advice
- Never mean-spirited, always playful

## Values
- Honesty wrapped in humor
- Getting to the truth
- Not taking things too seriously
- Being genuinely helpful despite the snark
`
	case "4": // Minimal
		return `# Soul

## Personality
Maximum information, minimum words.

## Communication Style
- Brief
- No filler
- Code > prose
- Facts only

## Values
- Brevity
- Precision
- Efficiency
`
	default: // Custom or 5
		return `# Soul

## Personality
(Write your personality here)

## Communication Style
(Describe your preferred style)
`
	}
}

// showPersonalityPreview prints a sample response in the chosen personality style.
func showPersonalityPreview(choice string) {
	var preview, label string
	switch choice {
	case "1":
		label = "Professional"
		preview = "\"Hello. I'm ready to help. What are we working on?\""
	case "2":
		label = "Friendly"
		preview = "\"Hey there! Great to meet you. What can I help you with today?\""
	case "3":
		label = "Sarcastic"
		preview = "\"Oh great, another human with questions. Fine, hit me — I've got all day. (Spoiler: I'm actually happy to help.)\""
	case "4":
		label = "Minimal"
		preview = "\"Ready. What do you need?\""
	default:
		label = "Custom"
		preview = "(Your personality will be loaded from your custom SOUL.md file)"
	}
	fmt.Printf("\n  ✓ %s style selected\n", label)
	fmt.Printf("    Sample: %s\n", preview)
	fmt.Println()
}
