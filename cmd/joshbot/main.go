// Package main is the entry point for the joshbot CLI.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bigknoxy/joshbot/internal/agent"
	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/channels"
	"github.com/bigknoxy/joshbot/internal/config"
	ctxpkg "github.com/bigknoxy/joshbot/internal/context"
	"github.com/bigknoxy/joshbot/internal/cron"
	"github.com/bigknoxy/joshbot/internal/heartbeat"
	"github.com/bigknoxy/joshbot/internal/learning"
	"github.com/bigknoxy/joshbot/internal/log"
	"github.com/bigknoxy/joshbot/internal/memory"
	"github.com/bigknoxy/joshbot/internal/providers"
	"github.com/bigknoxy/joshbot/internal/session"
	"github.com/bigknoxy/joshbot/internal/skills"
	"github.com/urfave/cli/v2"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
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
		},
		Commands: []*cli.Command{
			{
				Name:   "agent",
				Usage:  "Start joshbot in interactive CLI mode",
				Action: runAgent,
			},
			{
				Name:   "gateway",
				Usage:  "Start joshbot gateway (Telegram + all channels)",
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
				},
				Action: runOnboard,
			},
			{
				Name:   "status",
				Usage:  "Show configuration and status",
				Action: runStatus,
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
		},
		Before: func(c *cli.Context) error {
			// Update log level if verbose is set
			if c.Bool("verbose") {
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

	if cfgPath != "" && cfgPath != "~/.joshbot/config.json" {
		// Load from custom path - temporarily override DefaultHome
		oldHome := config.DefaultHome
		config.DefaultHome = filepath.Dir(cfgPath)
		defer func() { config.DefaultHome = oldHome }()
		cfg, err = config.Load()
	} else {
		cfg, err = config.Load()
	}

	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	return cfg, nil
}

// setupComponents initializes all required components.
func setupComponents(cfg *config.Config) (*bus.MessageBus, *providers.LiteLLMProvider, *session.Manager, *agent.Agent, error) {
	// Ensure directories exist
	if err := cfg.EnsureDirs(); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to create directories: %w", err)
	}

	// Initialize memory manager
	memoryManager, err := memory.New(cfg.Agents.Defaults.Workspace)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to init memory manager: %w", err)
	}
	if err := memoryManager.Initialize(context.Background()); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to initialize memory files: %w", err)
	}

	// Initialize skills loader
	skillsLoader, err := skills.NewLoader(cfg.Agents.Defaults.Workspace)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to init skills loader: %w", err)
	}
	// Discover skills now so agent has summaries available
	_ = skillsLoader.Discover()

	// Initialize message bus
	msgBus := bus.NewMessageBus()

	// Initialize provider - convert config to provider config
	providerCfg := providers.DefaultConfig()
	providerCfg.APIKey = getProviderAPIKey(cfg)
	providerCfg.Model = cfg.Agents.Defaults.Model
	providerCfg.MaxTokens = cfg.Agents.Defaults.MaxTokens
	providerCfg.Temperature = cfg.Agents.Defaults.Temperature

	// Get API base from provider config
	if p, ok := cfg.Providers["openrouter"]; ok {
		providerCfg.APIBase = p.APIBase
	}

	provider := providers.NewLiteLLMProvider(providerCfg)

	// Initialize session manager
	sessionMgr, err := session.NewManager(cfg.SessionsDir())
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to create session manager: %w", err)
	}

	// Get logger
	logger := log.Get()

	// Build context budgeting/compression components
	registry := ctxpkg.NewRegistry()
	budget := ctxpkg.NewBudgetManager(registry, 100)
	compressor := &ctxpkg.Compressor{Provider: provider}

	// Create agent (tools will be added later)
	agentInstance := agent.NewAgent(
		cfg,
		provider,
		nil, // tools - to be implemented
		sessionMgr,
		logger,
		agent.WithMemoryLoader(memoryManager),
		agent.WithSkillsLoader(skillsLoader),
		agent.WithBudgetManager(budget),
		agent.WithCompressor(compressor),
	)

	// Start background services (best-effort)
	cronSvc := cron.NewService(msgBus, cfg.Agents.Defaults.Workspace)
	cronSvc.Start()
	hb := heartbeat.NewService(msgBus, cfg.Agents.Defaults.Workspace)
	hb.SetInterval(5 * time.Minute) // shorter default for local setups
	hb.Start()

	// Start consolidator (self-learning memory consolidation)
	consolidator := learning.NewConsolidator(memoryManager, provider, 10*time.Minute)
	consolidator.Start()

	logger.Info("Background services started", "cron_jobs_file", cfg.Agents.Defaults.Workspace)

	return msgBus, provider, sessionMgr, agentInstance, nil
}

// getProviderAPIKey extracts the first available API key from config.
func getProviderAPIKey(cfg *config.Config) string {
	for _, p := range cfg.Providers {
		if p.APIKey != "" {
			return p.APIKey
		}
	}
	return ""
}

// setupGracefulShutdown sets up signal handling for graceful shutdown.
func setupGracefulShutdown(ctx context.Context, cancel context.CancelFunc, done chan<- struct{}) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Warn("Received signal, shutting down...", "signal", sig)
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

	if len(cfg.Providers) == 0 {
		return fmt.Errorf("no providers configured. Run 'joshbot onboard' first")
	}

	log.Info("Starting agent mode", "model", cfg.Agents.Defaults.Model)

	// Setup components
	msgBus, _, _, agentInstance, err := setupComponents(cfg)
	if err != nil {
		return err
	}

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	setupGracefulShutdown(ctx, cancel, done)

	// Start the message bus
	msgBus.Start()

	// Subscribe to CLI messages
	msgBus.Subscribe("cli", func(ctx context.Context, msg bus.InboundMessage) {
		log.Debug("Processing message", "content", msg.Content)
		response, err := agentInstance.Process(ctx, msg)
		if err != nil {
			log.Error("Agent error", "error", err)
			return
		}

		// Send response back to CLI
		outbound := bus.OutboundMessage{
			Content:   response,
			Channel:   "cli",
			Timestamp: time.Now(),
		}
		msgBus.Publish(outbound)
	})

	// Simple CLI loop
	fmt.Println("joshbot agent mode. Type 'exit' to quit.")
	for {
		select {
		case <-done:
			log.Info("Agent shutdown complete")
			return nil
		default:
			fmt.Print("> ")
			var input string
			fmt.Scanln(&input)

			if strings.ToLower(input) == "exit" {
				cancel()
				<-done
				return nil
			}
		}
	}
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
	if removeConfig {
		fmt.Println("Removed:")
		fmt.Printf("  - Binary: %s\n", absPath)
		fmt.Printf("  - Config: %s\n", configDir)
	} else {
		fmt.Println("Removed:")
		fmt.Printf("  - Binary: %s\n", absPath)
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

	if len(cfg.Providers) == 0 {
		return fmt.Errorf("no providers configured. Run 'joshbot onboard' first")
	}

	log.Info("Starting gateway mode",
		"model", cfg.Agents.Defaults.Model,
		"telegram", cfg.Channels.Telegram.Enabled,
	)

	// Setup components
	msgBus, _, _, agentInstance, err := setupComponents(cfg)
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
				// Keep existing data: skip file creation but run prompts
				skipFileCreation = true
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
	var apiKey, personalityChoice, model string
	var soulContent string

	if force {
		// Use defaults for non-interactive setup
		personalityChoice = "2" // Friendly
		soulContent = getPersonalitySoul(personalityChoice)
		model = config.DefaultModel
	} else {
		// Interactive prompts
		apiKey = promptAPIKey()
		personalityChoice = selectPersonality()
		soulContent = getPersonalitySoul(personalityChoice)
		model = selectModel()
	}

	// Build config
	cfg := config.Defaults()
	if apiKey != "" {
		cfg.Providers = map[string]config.ProviderConfig{
			"openrouter": {APIKey: apiKey},
		}
	}
	cfg.Agents.Defaults.Model = model

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

	// Print completion banner
	configPath := filepath.Join(homeDir, "config.json")
	wsDir := cfg.WorkspaceDir()

	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════╗")
	fmt.Println("║           Setup complete!                  ║")
	fmt.Println("╚═══════════════════════════════════════════╝")
	fmt.Println()
	fmt.Printf("Config: %s\n", configPath)
	fmt.Printf("Workspace: %s\n", wsDir)
	fmt.Println()
	fmt.Println("Quick start:")
	fmt.Println("  joshbot agent    - Chat in the terminal")
	fmt.Println("  joshbot gateway - Start Telegram + all channels")
	fmt.Println("  joshbot status  - Check configuration")
	fmt.Println()
	fmt.Println("Edit ~/.joshbot/config.json to configure Telegram and other settings.")

	return nil
}

// promptAPIKey prompts the user for their OpenRouter API key.
func promptAPIKey() string {
	fmt.Println("\n[Step 1] LLM Provider")
	fmt.Println("joshbot uses OpenRouter by default (supports many models with one API key).")
	fmt.Println("Get a free key at: https://openrouter.ai/keys")
	fmt.Print("Enter your OpenRouter API key (or press Enter to skip): ")

	var apiKey string
	fmt.Scanln(&apiKey)
	return apiKey
}

// selectPersonality prompts the user to choose a personality and returns the choice.
func selectPersonality() string {
	fmt.Println("\n[Step 2] Personality")
	fmt.Println("Choose joshbot's personality:")
	fmt.Println("  1. Professional - Concise, task-focused, minimal small talk")
	fmt.Println("  2. Friendly - Warm, conversational, uses humor")
	fmt.Println("  3. Sarcastic - Witty, dry humor, still helpful underneath")
	fmt.Println("  4. Minimal - Extremely terse, just the facts")
	fmt.Println("  5. Custom - Write your own SOUL.md")

	fmt.Print("Choose personality (1-5) [2]: ")
	var personalityChoice string
	fmt.Scanln(&personalityChoice)
	if personalityChoice == "" {
		personalityChoice = "2"
	}
	return personalityChoice
}

// selectModel prompts the user to select a model and returns the choice.
func selectModel() string {
	defaultModel := config.DefaultModel
	fmt.Println("\n[Step 3] Model")
	fmt.Printf("Default model: %s\n", defaultModel)
	fmt.Printf("Model name [%s]: ", defaultModel)

	var model string
	fmt.Scanln(&model)
	if model == "" {
		model = defaultModel
	}
	return model
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
	fmt.Printf("Model:          %s\n", cfg.Agents.Defaults.Model)
	fmt.Printf("Max tokens:     %d\n", cfg.Agents.Defaults.MaxTokens)
	fmt.Printf("Temperature:    %.1f\n", cfg.Agents.Defaults.Temperature)
	fmt.Printf("Memory window:  %d\n", cfg.Agents.Defaults.MemoryWindow)
	fmt.Println()

	providerNames := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		providerNames = append(providerNames, name)
	}
	if len(providerNames) == 0 {
		providerNames = []string{"none"}
	}
	fmt.Printf("Providers:      %s\n", strings.Join(providerNames, ", "))
	fmt.Printf("Telegram:       %s\n", statusBool(cfg.Channels.Telegram.Enabled))
	fmt.Printf("Workspace restricted: %s\n", statusBool(cfg.Tools.RestrictToWorkspace))
	fmt.Println()

	if memorySize > 0 || historySize > 0 {
		fmt.Printf("MEMORY.md:  %d bytes\n", memorySize)
		fmt.Printf("HISTORY.md: %d bytes\n", historySize)
	}

	return nil
}

// Helper functions

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
