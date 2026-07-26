// Package main is the entry point for the joshbot CLI.
package main

import (
	"bufio"
	"context"
	"encoding/json"
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
	"syscall"
	"time"

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
	"github.com/bigknoxy/joshbot/internal/memory"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/service"
	"github.com/bigknoxy/joshbot/internal/session"
	"github.com/bigknoxy/joshbot/internal/skills"
	"github.com/bigknoxy/joshbot/internal/subagent"
	"github.com/bigknoxy/joshbot/internal/tools"
	"github.com/charmbracelet/lipgloss"
	"github.com/urfave/cli/v2"
)

// runningContext describes how joshbot is running.
type runningContext struct {
	IsService bool
	IsDocker  bool
	IsGoRun   bool
}

// detectRunningContext determines how joshbot is currently running.
func detectRunningContext() runningContext {
	ctx := runningContext{}

	// Check for go run
	exePath, _ := os.Executable()
	if strings.Contains(exePath, "go-build") || strings.Contains(exePath, "/tmp/") {
		ctx.IsGoRun = true
		return ctx
	}

	// Check for Docker
	if _, err := os.Stat("/.dockerenv"); err == nil {
		ctx.IsDocker = true
	}

	// Check for service installation
	svc, err := service.NewManager(service.Config{Name: "joshbot"})
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
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func runApp() error {
	// Setup global logger configuration
	loggerCfg := log.DefaultConfig()
	loggerCfg.Prefix = "joshbot"

	if err := log.Init(loggerCfg); err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}

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
					&cli.StringFlag{
						Name:    "message",
						Aliases: []string{"m"},
						Usage:   "Send a single message and exit (non-interactive mode)",
					},
					&cli.BoolFlag{
						Name:  "debug",
						Usage: "Enable debug logging",
					},
				},
				Action: runAgent,
			},
			{
				Name:  "gateway",
				Usage: "Start joshbot gateway (Telegram + all channels)",
				Flags: []cli.Flag{
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
				},
				Action: runOnboard,
			},
			{
				Name:   "status",
				Usage:  "Show configuration and status",
				Action: runStatus,
			},
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
						Action: runSkillsList,
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
						Usage: "Provider to configure (nvidia, openrouter, groq, ollama, github-copilot)",
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
				Action: runConfigure,
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
					},
					{
						Name:   "status",
						Usage:  "Show authentication status for all providers",
						Action: runAuthStatus,
					},
				},
			},
		},
		Before: func(c *cli.Context) error {
			// Update log level if verbose or debug is set
			if c.Bool("verbose") || c.Bool("debug") {
				log.SetLevel(log.DebugLevel)
			}
			return nil
		},
	}

	return app.Run(os.Args)
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
	if cfgPath != "" && cfgPath != "~/.joshbot/config.json" {
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
			return nil, nil, nil, nil, nil, nil, fmt.Errorf("no models configured")
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
			return nil, nil, nil, nil, nil, nil, noProvidersRegisteredError(cfg.Providers)
		}
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
		tools.WithCronService(cronSvc, defaultReminderChannel(cfg)),
	)

	// Create function to reload providers from config (for config tool hot-reload)
	reloadProviders := func() error {
		multiProvider.Clear()
		if cfg.UseModelsConfig() {
			resolvedModels := cfg.GetAllModelConfigs()
			for i, resolved := range resolvedModels {
				llmProvider := providers.NewProviderFromResolvedModel(resolved, &providers.DefaultLogger{})
				var provider providers.Provider = llmProvider
				if len(resolved.APIKeys) > 1 {
					pool := providers.NewAPIKeyPool(resolved.APIKeys, 24*time.Hour, 3)
					provider = providers.NewKeyRotatingProvider(llmProvider, pool)
				}
				multiProvider.Register(resolved.Name, provider, resolved.ModelID, i, true)
			}
		} else {
			if p, ok := cfg.Providers["openrouter"]; ok && p.APIKey != "" && p.Enabled {
				openrouterProvider, err := providers.GetProvider("openrouter", providers.Config{
					APIKey: p.APIKey, APIBase: p.APIBase, ExtraHeaders: p.ExtraHeaders,
					Model: cfg.Agents.Defaults.Model, MaxTokens: cfg.Agents.Defaults.MaxTokens,
					Temperature: cfg.Agents.Defaults.Temperature,
				})
				if err != nil {
					log.Warn("Failed to create OpenRouter provider", "error", err)
				} else {
					multiProvider.Register("openrouter", openrouterProvider, cfg.Agents.Defaults.Model, 0, p.Enabled)
				}
			}
			if p, ok := cfg.Providers["nvidia"]; ok && p.APIKey != "" && p.Enabled {
				nvidiaProvider, err := providers.GetProvider("nvidia", providers.Config{
					APIKey: p.APIKey, APIBase: p.APIBase, ExtraHeaders: p.ExtraHeaders, Model: p.Model,
				})
				if err != nil {
					log.Warn("Failed to create NVIDIA provider", "error", err)
				} else {
					model := p.Model
					if model == "" {
						model = cfg.Agents.Defaults.Model
					}
					multiProvider.Register("nvidia", nvidiaProvider, model, 1, p.Enabled)
				}
			}
		}
		return nil
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
	subagentRunner := subagent.NewRunner(multiProvider, agentModel, 4096, 0.3, 60*time.Second)
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

	go func() {
		for result := range asyncCallbackCh {
			var msg string
			if result.Error != nil {
				msg = fmt.Sprintf("❌ Background task failed (%s): %v", result.ToolName, result.Error)
			} else {
				output := result.Output
				if len(output) > 2000 {
					output = output[:2000] + "... (truncated)"
				}
				msg = fmt.Sprintf("✅ Background task completed (%s):\n%s", result.ToolName, output)
			}

			// Publish to message bus for gateway mode
			msgBus.Publish(bus.OutboundMessage{
				Channel:   result.Channel,
				ChannelID: result.ChatID,
				Content:   msg,
			})
		}
	}()

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
	hb.SetInterval(5 * time.Minute) // shorter default for local setups
	hb.Start()

	// Start consolidator (self-learning memory consolidation)
	consolidator := learning.NewConsolidator(memoryManager, multiProvider, 10*time.Minute)
	consolidator.Start()

	logger.Info("Background services started", "cron_jobs_file", cfg.Agents.Defaults.Workspace)

	return msgBus, multiProvider, sessionMgr, agentInstance, toolsRegistry, messageSender, nil
}

// defaultReminderChannel picks where a scheduled reminder goes when the agent
// does not name a channel. A reminder delivered to a CLI session nobody is
// sitting at is lost, so a configured Telegram channel wins.
func defaultReminderChannel(cfg *config.Config) string {
	if cfg != nil && cfg.Channels.Telegram.Enabled {
		return "telegram"
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
		sig := <-sigChan
		switch sig {
		case syscall.SIGHUP:
			log.Warn("Received SIGHUP signal, gracefully restarting...", "signal", sig)
		default:
			log.Warn("Received signal, shutting down...", "signal", sig)
		}
		cancel()
		close(done)
	}()
}

// runAgent executes the agent (interactive CLI) mode.
func runAgent(c *cli.Context) error {
	cfg, err := loadConfig(c.Path("config"))
	if err != nil {
		return err
	}

	// Check for either legacy providers or new model-centric config
	if !cfg.UseModelsConfig() && len(cfg.Providers) == 0 {
		return fmt.Errorf("no providers configured. Run 'joshbot onboard' first")
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
	if err != nil {
		return err
	}

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start async callback printer for CLI mode
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
				fmt.Fprint(os.Stdout, msg)
			}
		}
	}()

	// Non-interactive mode: send single message and exit
	if message := c.String("message"); message != "" {
		err := runAgentSingleMessage(ctx, agentInstance, message, os.Stdout, messageSender)
		// Wait a bit for async callbacks
		time.Sleep(2 * time.Second)
		return err
	}

	done := make(chan struct{})
	setupGracefulShutdown(ctx, cancel, done)

	if err := runAgentLoop(ctx, cancel, done, os.Stdin, os.Stdout, agentInstance, messageSender); err != nil {
		return err
	}
	return nil
}

type agentProcessor interface {
	Process(context.Context, bus.InboundMessage) (string, error)
}

func runAgentLoop(ctx context.Context, cancel context.CancelFunc, done <-chan struct{}, input io.Reader, output io.Writer, agentInstance agentProcessor, messageSender *tools.BusMessageSender) error {
	// Set chat ID for CLI mode so message tools can send messages proactively
	if messageSender != nil {
		messageSender.SetChatID("cli", "cli_user")
	}

	reader := bufio.NewReader(input)
	fmt.Fprintln(output, "joshbot agent mode. Type 'exit' to quit.")
	for {
		select {
		case <-done:
			log.Info("Agent shutdown complete")
			return nil
		default:
		}

		fmt.Fprint(output, "> ")
		line, readErr := reader.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			return fmt.Errorf("failed to read input: %w", readErr)
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

		response, procErr := agentInstance.Process(ctx, msg)
		if procErr != nil {
			log.Error("Agent error", "error", procErr)
			fmt.Fprintf(output, "Error: %v\n", procErr)
			continue
		}

		fmt.Fprintf(output, "\n%s\n\n", strings.TrimSpace(response))

		if readErr == io.EOF {
			cancel()
			return nil
		}
	}
}

// runAgentSingleMessage sends a single message and prints the response.
func runAgentSingleMessage(ctx context.Context, agentInstance agentProcessor, message string, output io.Writer, messageSender *tools.BusMessageSender) error {
	// Set chat ID for CLI mode so message tools work
	if messageSender != nil {
		messageSender.SetChatID("cli", "cli_user")
	}

	msg := bus.InboundMessage{
		SenderID:  "cli_user",
		Content:   message,
		Channel:   "cli",
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"username": "user",
		},
	}

	response, err := agentInstance.Process(ctx, msg)
	if err != nil {
		return fmt.Errorf("failed to process message: %w", err)
	}

	fmt.Fprintln(output, strings.TrimSpace(response))
	return nil
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

	// Check if running from source
	if strings.Contains(exePath, "go-build") || strings.Contains(exePath, "/tmp/") {
		fmt.Println()
		fmt.Println("Error: Cannot update when running from source with 'go run'.")
		fmt.Println("To update, install joshbot first (e.g., 'go install' or build a binary),")
		fmt.Println("then run 'joshbot update' from the installed binary.")
		return nil
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
		"https://github.com/bigknoxy/joshbot/releases/download/%s/joshbot_%s_%s_%s%s",
		latestVersion, latestVersion, runtime.GOOS, runtime.GOARCH, extension,
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
		svc, err := service.NewManager(service.Config{
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
	err = syscall.Exec(exePath, append([]string{exePath}, args...), os.Environ())
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
func getLatestVersion() (string, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	req, err := http.NewRequest("GET", "https://api.github.com/repos/bigknoxy/joshbot/releases/latest", nil)
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
	exePath, err := os.Executable()
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

// runUninstall uninstalls joshbot and optionally removes configuration.
func runUninstall(c *cli.Context) error {
	// Find the binary location
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not determine executable path: %w", err)
	}

	// Check if running from source (go run)
	// If the executable is in a temp directory or has "go-build" in path, it's likely from go run
	if strings.Contains(exePath, "go-build") || strings.Contains(exePath, "/tmp/") {
		fmt.Println()
		fmt.Println("Error: Cannot uninstall when running from source with 'go run'.")
		fmt.Println("To uninstall, install joshbot first (e.g., 'go install' or build a binary),")
		fmt.Println("then run 'joshbot uninstall' from the installed binary.")
		return nil
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
	fmt.Printf("Binary to remove: %s\n", absPath)

	// Determine config directory
	configDir := config.DefaultHome
	configExists := false
	if _, err := os.Stat(configDir); err == nil {
		configExists = true
	}

	if configExists && !c.Bool("keep-config") {
		fmt.Printf("Config to remove: %s\n", configDir)
	} else if configExists && c.Bool("keep-config") {
		fmt.Printf("Config (kept):    %s\n", configDir)
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

	svc, svcErr := service.NewManager(svcCfg)
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
	fmt.Printf("Removing binary: %s\n", absPath)
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
			fmt.Printf("Removing config: %s\n", configDir)
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
	fmt.Printf("  - Binary: %s\n", absPath)
	if removeConfig {
		fmt.Printf("  - Config: %s\n", configDir)
	}
	if serviceUninstalled {
		fmt.Println("  - Service: joshbot")
	}
	fmt.Println()
	fmt.Println("Thank you for using joshbot!")

	return nil
}

// runGateway executes the gateway (Telegram + channels) mode.
func runGateway(c *cli.Context) error {
	cfg, err := loadConfig(c.Path("config"))
	if err != nil {
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
	)

	// Setup components
	msgBus, _, _, agentInstance, _, sender, err := setupComponents(cfg)
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

	// Subscribe agent to all channels
	msgBus.Subscribe("all", func(ctx context.Context, msg bus.InboundMessage) {
		// DEBUG: Direct stderr logging
		fmt.Fprintf(os.Stderr, "!!! BUS HANDLER INVOKED channel=%s content=%q\n", msg.Channel, msg.Content)
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
				msgBus.Publish(outbound)
			}
		}()

		// Store the chat ID for this channel to enable proactive messaging
		if sender != nil {
			sender.SetChatID(msg.Channel, getChannelID(msg))
		}

		log.Debug("Processing message",
			"channel", msg.Channel,
			"content", msg.Content,
		)
		response, err := agentInstance.Process(ctx, msg)
		if err != nil {
			log.Error("Agent error", "error", err)
			// Send error response
			outbound := bus.OutboundMessage{
				Content:   fmt.Sprintf("Error: %v", err),
				Channel:   msg.Channel,
				ChannelID: getChannelID(msg),
				Timestamp: time.Now(),
			}
			msgBus.Publish(outbound)
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
		msgBus.Publish(outbound)
	})

	// Start Telegram channel if enabled
	var tgChannel *channels.TelegramChannel
	if cfg.Channels.Telegram.Enabled && cfg.Channels.Telegram.Token != "" {
		tgChannel = channels.NewTelegramChannel(msgBus, &cfg.Channels.Telegram)
		if err := tgChannel.Start(ctx); err != nil {
			log.Error("Failed to start Telegram channel", "error", err)
		} else {
			log.Info("Telegram channel started")
		}
	}

	// Print startup banner
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════╗")
	fmt.Println("║         joshbot gateway running           ║")
	fmt.Printf("║  Model: %-34s ║\n", cfg.Agents.Defaults.Model)
	fmt.Printf("║  Telegram: %-30s ║\n", boolToEnabled(cfg.Channels.Telegram.Enabled))
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

	log.Info("Gateway stopped")
	return nil
}

// sendTelegramMessage is a stub for sending Telegram messages.
func sendTelegramMessage(msg bus.OutboundMessage) {
	// This would use the Telegram API in production
	log.Debug("Telegram message",
		"content", msg.Content,
		"chat_id", msg.ChannelID,
	)
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
	homeDir := config.DefaultHome

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
			fmt.Printf("Backed up to: %s\n", backupPath)
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
			fmt.Printf("  Config:     %s %s\n", filepath.Join(homeDir, "config.json"), statusBool(configExists))
			fmt.Printf("  Workspace:  %s %s\n", filepath.Join(homeDir, "workspace/"), statusBool(workspaceExists))
			memoryPath := filepath.Join(homeDir, "workspace", "memory")
			if _, err := os.Stat(memoryPath); err == nil {
				fmt.Printf("  Memory:     %s %s\n", memoryPath, statusBool(true))
			}
			fmt.Println()

			fmt.Println("  [1] Keep existing data and reconfigure")
			fmt.Println("  [2] Delete and start fresh (backup created)")
			fmt.Println()
			fmt.Print("  Choose [1-2] (default: 1): ")

			var choice string
			fmt.Scanln(&choice)
			fmt.Println()

			if choice == "1" {
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
				fmt.Printf("Backed up to: %s\n", backupPath)
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
		if modelFlag != "" {
			model = modelFlag
		} else {
			model = config.DefaultModel
		}
		// Get provider from existing config or use default
		if existingCfg != nil && len(existingCfg.Providers) > 0 {
			for p := range existingCfg.Providers {
				provider = p
				break
			}
		}
		if provider == "" {
			provider = "openrouter"
		}
		var err error
		apiKey, err = promptProviderAPIKey(provider, existingCfg)
		if err != nil {
			return err
		}
	} else {
		// Interactive prompts - pass existing config for defaults
		provider = selectProvider(existingCfg)
		var err error
		apiKey, err = promptProviderAPIKey(provider, existingCfg)
		if err != nil {
			return err
		}
		personalityChoice = selectPersonality(existingCfg)
		soulContent = getPersonalitySoul(personalityChoice)
		userName = promptUserName(existingCfg)
		model = selectModel(existingCfg, provider, modelFlag)
		telegramConfig = setupTelegram(existingCfg)
	}

	// Build config
	cfg := config.Defaults()
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
	}
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

	// Print completion banner
	configPath := filepath.Join(homeDir, "config.json")
	wsDir := cfg.WorkspaceDir()

	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════╗")
	fmt.Println("║           Setup complete!                  ║")
	fmt.Println("╚═══════════════════════════════════════════╝")
	fmt.Println()
	fmt.Println("  Config:")
	fmt.Printf("    %s\n", configPath)
	fmt.Printf("    %s\n", wsDir)
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

// promptProviderAPIKey prompts for the API key based on the selected provider.
func promptProviderAPIKey(provider string, existingCfg *config.Config) (string, error) {
	var keyURL, keyName string
	switch provider {
	case "nvidia":
		keyURL = "https://build.nvidia.com"
		keyName = "NVIDIA API key"
	case "openrouter":
		keyURL = "https://openrouter.ai/keys"
		keyName = "OpenRouter API key"
	case "groq":
		keyURL = "https://console.groq.com/keys"
		keyName = "Groq API key"
	case "poolside":
		keyURL = "https://poolside.ai"
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

	fmt.Printf("\nGet your %s at: %s\n", keyName, keyURL)

	// Show existing key if available
	if existingCfg != nil {
		if p, ok := existingCfg.Providers[provider]; ok && p.APIKey != "" {
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
	return strings.TrimSpace(apiKey), nil
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
	fmt.Printf("Bot token: ")

	var token string
	fmt.Scanln(&token)

	if token == "cancel" || token == "" {
		fmt.Println("\nTelegram setup cancelled.")
		return nil
	}

	// Sanitize token: strip control characters and escape sequences
	token = strings.TrimSpace(sanitizeToken(token))

	fmt.Println("\nValidating token...")
	if err := channels.ValidateToken(token); err != nil {
		fmt.Printf("Token validation failed: %v\n", err)
		fmt.Println("Please check your token and try again.")
		return nil
	}
	fmt.Println("Token validated successfully!")

	fmt.Println("\nAllowed usernames (optional)")
	fmt.Println("Restrict bot access to specific Telegram usernames.")
	fmt.Println("Leave empty to allow anyone to use the bot.")
	fmt.Println()

	// Show existing allow from as default
	defaultUsernames := strings.Join(existingAllowFrom, ", ")
	fmt.Printf("Usernames (comma-separated) [current: %s]: ", defaultUsernames)

	var usernamesRaw string
	fmt.Scanln(&usernamesRaw)

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

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to detect executable path: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to detect home directory: %w", err)
	}

	logPath := filepath.Join(home, ".joshbot", "logs", "gateway.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
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
	svc, err := service.NewManager(service.Config{
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
	if err := os.MkdirAll(memDir, 0755); err != nil {
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

// runStatus displays the current configuration and status.
func runStatus(c *cli.Context) error {
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

	// Print status
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════╗")
	fmt.Println("║            joshbot status                ║")
	fmt.Println("╚═══════════════════════════════════════════╝")
	fmt.Printf("Version:        %s\n", Version)
	fmt.Printf("Config file:    %s %s\n", configPath, statusBool(configExists))
	fmt.Printf("Workspace:      %s %s\n", cfg.WorkspaceDir(), statusBool(wsExists))
	fmt.Printf("Sessions:       %s\n", cfg.SessionsDir())
	fmt.Println()

	// Display model info based on config format
	if cfg.UseModelsConfig() {
		fmt.Println("Config format:  model-centric")
		fmt.Printf("Active model:   %s\n", cfg.ModelsConfig.Agent.Model)
		if len(cfg.ModelsConfig.Agent.Fallback) > 0 {
			fmt.Printf("Fallback:       %s\n", strings.Join(cfg.ModelsConfig.Agent.Fallback, ", "))
		}
	} else {
		fmt.Printf("Model:          %s\n", cfg.Agents.Defaults.Model)
	}
	fmt.Printf("Max tokens:     %d\n", cfg.Agents.Defaults.MaxTokens)
	fmt.Printf("Temperature:    %.1f\n", cfg.Agents.Defaults.Temperature)
	fmt.Printf("Memory window:  %d\n", cfg.Agents.Defaults.MemoryWindow)
	fmt.Println()

	// Display providers/models
	if cfg.UseModelsConfig() {
		modelNames := make([]string, 0, len(cfg.ModelsConfig.Models))
		for _, m := range cfg.ModelsConfig.Models {
			if !m.Disabled {
				modelNames = append(modelNames, m.Name)
			}
		}
		if len(modelNames) == 0 {
			modelNames = []string{"none"}
		}
		fmt.Printf("Models:         %s\n", strings.Join(modelNames, ", "))
	} else {
		fmt.Printf("Providers:      %s\n", formatProviderStatus(cfg.Providers))
	}
	fmt.Printf("Telegram:       %s\n", boolToEnabled(cfg.Channels.Telegram.Enabled))
	fmt.Printf("Workspace restricted: %s\n", boolToEnabled(cfg.Tools.RestrictToWorkspace))

	// Skills awaiting review belong here, not only in a startup log line. In
	// gateway mode that log goes to the journal, where an operator would never
	// see it — they would just get a quietly worse assistant. `status` is
	// where someone looks when something seems off.
	if pending := pendingSkillNames(cfg); len(pending) > 0 {
		fmt.Printf("Skills:         %d awaiting review (%s)\n", len(pending), strings.Join(pending, ", "))
		fmt.Println("                not in use — review then run: joshbot skills trust <name>")
	}
	fmt.Println()

	if memorySize > 0 || historySize > 0 {
		fmt.Printf("MEMORY.md:  %d bytes\n", memorySize)
		fmt.Printf("HISTORY.md: %d bytes\n", historySize)
	}

	return nil
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
		return listProviders(cfg)
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
		fmt.Printf("Provider %q configured with model %q.\n", opts.Name, cfg.Providers[opts.Name].Model)
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
func listProviders(cfg *config.Config) error {
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════╗")
	fmt.Println("║        Configured Providers              ║")
	fmt.Println("╚═══════════════════════════════════════════╝")
	fmt.Println()

	providers := []string{"nvidia", "openrouter", "groq", "ollama", "github-copilot", "poolside"}
	defaultProvider := cfg.ProviderDefaults.Default

	for _, name := range providers {
		p, exists := cfg.Providers[name]
		isDefault := name == defaultProvider
		statusIcon := "○"
		statusText := "not configured"

		if exists && p.Enabled {
			if name == "github-copilot" {
				homeDir, _ := copilot.GetHomeDir()
				copilotToken, copilotErr := copilot.LoadToken(homeDir)
				if copilotErr == nil && copilotToken != nil && copilotToken.AccessToken != "" {
					statusIcon = "✓"
					statusText = "authenticated"
				} else {
					statusIcon = "○"
					statusText = "OAuth required"
				}
			} else if p.APIKey != "" {
				statusIcon = "✓"
				statusText = "configured"
			}
		}
		if isDefault {
			statusText += " (default)"
		}

		fmt.Printf("  %s %-12s %s\n", statusIcon, name, statusText)
	}

	fmt.Println()
	return nil
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
		fmt.Println("  7. Configure fallback order")
		fmt.Println("  8. Done")
		fmt.Println()

		fmt.Print("Choice [9]: ")

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
		if exists && p.APIBase != "" {
			fmt.Printf("API base URL [%s]: ", p.APIBase)
		} else {
			fmt.Print("API base URL [https://api.poolside.ai/v1]: ")
		}
		fmt.Scanln(&apiBase)
		if apiBase == "" {
			if p.APIBase == "" {
				apiBase = "https://api.poolside.ai/v1"
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
			_, err = copilot.RunDeviceFlow(ctx)
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
			models, err = copilot.ListModels(token.AccessToken)
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
	svc, err := service.NewManager(service.Config{
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
	svc, err := service.NewManager(service.Config{
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
	svc, err := service.NewManager(service.Config{
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

	fmt.Printf("Status: %s\n", status.Status)
	if status.Running {
		fmt.Println("The service is currently running.")
	} else {
		fmt.Println("The service is not running.")
	}

	return nil
}

func runAuthCopilot(c *cli.Context) error {
	homeDir, err := copilot.GetHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	token, err := copilot.LoadToken(homeDir)
	if err == nil && token != nil && token.AccessToken != "" {
		fmt.Println("Already authenticated with GitHub Copilot.")
		fmt.Println("Run 'joshbot auth github-copilot' to re-authenticate.")
		return nil
	}

	fmt.Println("Starting GitHub Copilot authentication...")
	fmt.Println()

	ctx := context.Background()
	token, err = copilot.RunDeviceFlow(ctx)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	if err := copilot.SaveToken(homeDir, token); err != nil {
		return fmt.Errorf("failed to save token: %w", err)
	}

	fmt.Println()
	fmt.Println("Successfully authenticated with GitHub Copilot!")

	fmt.Println("\nFetching available models...")
	models, err := copilot.ListModels(token.AccessToken)
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

	cfg, err := loadConfig("")
	if err != nil {
		fmt.Printf("Warning: Could not load existing config: %v\n", err)
		fmt.Println("Creating new config with GitHub Copilot settings.")
	}
	if cfg == nil {
		cfg = &config.Config{Providers: make(map[string]config.ProviderConfig)}
	}
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]config.ProviderConfig)
	}
	cfg.Providers["github-copilot"] = config.ProviderConfig{
		Enabled: true,
		Model:   selected,
	}

	if err := config.Save(cfg); err != nil {
		fmt.Printf("Warning: Could not save config: %v\n", err)
	} else {
		fmt.Printf("\nModel '%s' saved to config.\n", selected)
	}

	fmt.Println("You can now use 'joshbot agent' with GitHub Copilot.")
	return nil
}

func runAuthStatus(c *cli.Context) error {
	homeDir, err := copilot.GetHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	token, err := copilot.LoadToken(homeDir)

	fmt.Println("Authentication Status:")
	fmt.Println()
	fmt.Printf("  GitHub Copilot: ")

	if err != nil || token == nil || token.AccessToken == "" {
		fmt.Println("not authenticated")
		fmt.Println("    Run 'joshbot auth github-copilot' to authenticate")
	} else {
		fmt.Println("authenticated")
	}

	return nil
}

// Helper functions

// providerRequiresAPIKey reports whether the named legacy provider must have
// a non-empty api_key to be registered by setupComponents. Keep this in sync
// with the gating conditions there: openrouter/nvidia/groq/poolside/custom
// all require p.APIKey != "", while ollama (local server) and github-copilot
// (OAuth token file, not api_key) do not.
func providerRequiresAPIKey(name string) bool {
	switch name {
	case "ollama", "github-copilot":
		return false
	default:
		return true
	}
}

// formatProviderStatus renders the legacy providers map for `joshbot status`,
// flagging any provider that setupComponents will NOT register: either
// because "enabled": true is missing, or (for providers that need one)
// because api_key is empty. This mirrors the registration gates in
// setupComponents so status never claims a provider is configured when it
// is actually inert. See issue #71.
func formatProviderStatus(providersCfg map[string]config.ProviderConfig) string {
	if len(providersCfg) == 0 {
		return "none"
	}

	names := make([]string, 0, len(providersCfg))
	for name := range providersCfg {
		names = append(names, name)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, name := range names {
		p := providersCfg[name]
		switch {
		case !p.Enabled:
			parts = append(parts, fmt.Sprintf(`%s (disabled — set "enabled": true)`, name))
		case providerRequiresAPIKey(name) && p.APIKey == "":
			parts = append(parts, fmt.Sprintf(`%s (disabled — missing "api_key")`, name))
		default:
			parts = append(parts, name)
		}
	}

	return strings.Join(parts, ", ")
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
		return fmt.Errorf("no providers configured. Run 'joshbot onboard' first")
	}

	names := make([]string, 0, len(providersCfg))
	var keyless []string
	for name, p := range providersCfg {
		names = append(names, name)
		if p.Enabled && providerRequiresAPIKey(name) && p.APIKey == "" {
			keyless = append(keyless, name)
		}
	}
	sort.Strings(names)
	sort.Strings(keyless)

	// Enabled but unusable: the enabled flag is not the problem.
	if len(keyless) > 0 {
		return fmt.Errorf(
			"no providers usable: %s enabled but missing \"api_key\" — set an api_key, or run 'joshbot configure'",
			strings.Join(keyless, ", "),
		)
	}

	return fmt.Errorf(
		"no providers enabled: %d provider(s) found in config (%s) but none have \"enabled\": true — add \"enabled\": true to the provider you want to use",
		len(providersCfg), strings.Join(names, ", "),
	)
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
