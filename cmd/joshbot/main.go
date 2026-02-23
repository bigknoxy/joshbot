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
	"strconv"
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
	"github.com/bigknoxy/joshbot/internal/service"
	"github.com/bigknoxy/joshbot/internal/session"
	"github.com/bigknoxy/joshbot/internal/skills"
	"github.com/bigknoxy/joshbot/internal/tools"
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
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:    "message",
						Aliases: []string{"m"},
						Usage:   "Send a single message and exit (non-interactive mode)",
					},
				},
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
				Name:    "configure",
				Aliases: []string{"config"},
				Usage:   "Configure LLM providers and settings",
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:  "list",
						Usage: "List configured providers",
					},
					&cli.BoolFlag{
						Name:  "default",
						Usage: "Set default provider",
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
func setupComponents(cfg *config.Config) (*bus.MessageBus, providers.Provider, *session.Manager, *agent.Agent, *tools.Registry, *tools.BusMessageSender, error) {
	// Ensure directories exist
	if err := cfg.EnsureDirs(); err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("failed to create directories: %w", err)
	}

	// Initialize memory manager
	memoryManager, err := memory.New(cfg.Agents.Defaults.Workspace)
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
	// Discover skills now so agent has summaries available
	_ = skillsLoader.Discover()

	// Initialize message bus
	msgBus := bus.NewMessageBus()

	// Create BusMessageSender for tools that need to send messages
	messageSender := tools.NewBusMessageSender(msgBus)

	// Get logger
	logger := log.Get()

	// Create MultiProvider instead of single provider
	multiProvider := providers.NewMultiProvider(providers.MultiProviderConfig{
		DefaultProvider: cfg.ProviderDefaults.Default,
		Logger:          &providers.DefaultLogger{},
	})
	if cfg.ProviderDefaults.Default == "" {
		multiProvider.SetDefault("openrouter")
	}

	// Register OpenRouter (always registered if configured)
	if p, ok := cfg.Providers["openrouter"]; ok && p.APIKey != "" {
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
			multiProvider.Register("openrouter", openrouterProvider, cfg.Agents.Defaults.Model, 0)
		}
	}

	// Register NVIDIA NIM (if configured) - first fallback
	if p, ok := cfg.Providers["nvidia"]; ok && p.APIKey != "" && p.Enabled {
		nvidiaProvider, err := providers.GetProvider("nvidia", providers.Config{
			APIKey:       p.APIKey,
			APIBase:      p.APIBase,
			ExtraHeaders: p.ExtraHeaders,
		})
		if err != nil {
			log.Warn("Failed to create NVIDIA provider", "error", err)
		} else {
			priority := 1
			if idx := indexOf(cfg.ProviderDefaults.FallbackOrder, "nvidia"); idx >= 0 {
				priority = idx + 1
			}
			multiProvider.Register("nvidia", nvidiaProvider, "", priority)
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
			multiProvider.Register("groq", groqProvider, "", priority)
		}
	}

	// Register Ollama (if configured)
	if p, ok := cfg.Providers["ollama"]; ok && p.Enabled {
		ollamaProvider, err := providers.GetProvider("ollama", providers.Config{
			APIBase:      p.APIBase,
			ExtraHeaders: p.ExtraHeaders,
		})
		if err != nil {
			log.Warn("Failed to create Ollama provider", "error", err)
		} else {
			priority := len(cfg.ProviderDefaults.FallbackOrder) + 1
			if idx := indexOf(cfg.ProviderDefaults.FallbackOrder, "ollama"); idx >= 0 {
				priority = idx + 1
			}
			multiProvider.Register("ollama", ollamaProvider, "", priority)
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

	// Create tools registry with defaults
	toolsRegistry := tools.RegistryWithDefaults(
		cfg.Agents.Defaults.Workspace,
		cfg.Tools.RestrictToWorkspace,
		cfg.Tools.Exec.Timeout,
		0, // webTimeout - not configurable in config yet
		messageSender,
	)

	// Create agent with tools registry
	agentInstance := agent.NewAgent(
		cfg,
		multiProvider,
		toolsRegistry,
		sessionMgr,
		logger,
		agent.WithMemoryLoader(memoryManager),
		agent.WithHistoryAppender(memoryManager),
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
	consolidator := learning.NewConsolidator(memoryManager, multiProvider, 10*time.Minute)
	consolidator.Start()

	logger.Info("Background services started", "cron_jobs_file", cfg.Agents.Defaults.Workspace)

	return msgBus, multiProvider, sessionMgr, agentInstance, toolsRegistry, messageSender, nil
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
	_, _, _, agentInstance, _, _, err := setupComponents(cfg)
	if err != nil {
		return err
	}

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Non-interactive mode: send single message and exit
	if message := c.String("message"); message != "" {
		return runAgentSingleMessage(ctx, agentInstance, message, os.Stdout)
	}

	done := make(chan struct{})
	setupGracefulShutdown(ctx, cancel, done)

	if err := runAgentLoop(ctx, cancel, done, os.Stdin, os.Stdout, agentInstance); err != nil {
		return err
	}
	return nil
}

type agentProcessor interface {
	Process(context.Context, bus.InboundMessage) (string, error)
}

func runAgentLoop(ctx context.Context, cancel context.CancelFunc, done <-chan struct{}, input io.Reader, output io.Writer, agentInstance agentProcessor) error {
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
func runAgentSingleMessage(ctx context.Context, agentInstance agentProcessor, message string, output io.Writer) error {
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

	if len(cfg.Providers) == 0 {
		return fmt.Errorf("no providers configured. Run 'joshbot onboard' first")
	}

	log.Info("Starting gateway mode",
		"model", cfg.Agents.Defaults.Model,
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
		existingCfg, err = config.Load()
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
				existingCfg, err = config.Load()
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
		model = config.DefaultModel
	} else {
		// Interactive prompts - pass existing config for defaults
		provider = selectProvider(existingCfg)
		apiKey = promptProviderAPIKey(provider, existingCfg)
		personalityChoice = selectPersonality(existingCfg)
		soulContent = getPersonalitySoul(personalityChoice)
		userName = promptUserName(existingCfg)
		model = selectModel(existingCfg)
		telegramConfig = setupTelegram(existingCfg)
	}

	// Build config
	cfg := config.Defaults()
	if apiKey != "" || provider == "ollama" {
		cfg.Providers = map[string]config.ProviderConfig{
			provider: {APIKey: apiKey, Enabled: true},
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
	installService := promptServiceInstall()
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

// selectProvider prompts the user to choose an LLM provider.
func selectProvider(existingCfg *config.Config) string {
	fmt.Println("\n[Step 1] LLM Provider")
	fmt.Println("Choose your LLM provider:")
	fmt.Println("  1. NVIDIA NIM (Recommended - Free tier available)")
	fmt.Println("  2. OpenRouter (Many models, one API key)")
	fmt.Println("  3. Groq (Fast inference)")
	fmt.Println("  4. Ollama (Local, no API key needed)")

	// Show current default if exists
	if existingCfg != nil && existingCfg.ProviderDefaults.Default != "" {
		fmt.Printf("\nCurrent provider: %s\n", getProviderDisplayName(existingCfg.ProviderDefaults.Default))
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
	default:
		return "nvidia"
	}
}

// promptProviderAPIKey prompts for the API key based on the selected provider.
func promptProviderAPIKey(provider string, existingCfg *config.Config) string {
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
	case "ollama":
		fmt.Println("\nOllama runs locally - no API key needed.")
		return ""
	}

	fmt.Printf("\nGet your %s at: %s\n", keyName, keyURL)

	// Show existing key if available
	if existingCfg != nil {
		if p, ok := existingCfg.Providers[provider]; ok && p.APIKey != "" {
			fmt.Printf("Current API key: %s\n", maskAPIKey(p.APIKey))
			fmt.Print("Enter new API key (or press Enter to keep current): ")
		} else {
			fmt.Printf("Enter your %s (or press Enter to skip): ", keyName)
		}
	} else {
		fmt.Printf("Enter your %s (or press Enter to skip): ", keyName)
	}

	var apiKey string
	fmt.Scanln(&apiKey)
	return strings.TrimSpace(apiKey)
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

// selectModel prompts the user to select a model and returns the choice.
func selectModel(existingCfg *config.Config) string {
	defaultModel := config.DefaultModel

	// Use existing model as default if available
	if existingCfg != nil && existingCfg.Agents.Defaults.Model != "" {
		defaultModel = existingCfg.Agents.Defaults.Model
	}

	fmt.Println("\n[Step 4] Model")
	fmt.Printf("Model name [%s] (press Enter to accept): ", defaultModel)

	var model string
	fmt.Scanln(&model)
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultModel
	}
	return model
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

// runConfigure handles the configure command.
func runConfigure(c *cli.Context) error {
	// Load existing config
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// --list flag: show providers and exit
	if c.Bool("list") {
		return listProviders(cfg)
	}

	// Interactive wizard
	return runConfigureWizard(cfg)
}

// listProviders displays the configured providers.
func listProviders(cfg *config.Config) error {
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════╗")
	fmt.Println("║        Configured Providers              ║")
	fmt.Println("╚═══════════════════════════════════════════╝")
	fmt.Println()

	providers := []string{"nvidia", "openrouter", "groq", "ollama"}
	defaultProvider := cfg.ProviderDefaults.Default

	for _, name := range providers {
		p, exists := cfg.Providers[name]
		isDefault := name == defaultProvider
		statusIcon := "○"
		statusText := "not configured"

		if exists && p.APIKey != "" {
			statusIcon = "✓"
			statusText = "configured"
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
	providers := []string{"nvidia", "openrouter", "groq", "ollama"}

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

			if exists && p.APIKey != "" {
				icon = "✓"
				status = "configured"
			}
			if isDefault {
				status += " (default)"
			}

			fmt.Printf("  %s %s (%s)\n", icon, getProviderDisplayName(name), status)
		}

		fmt.Println()
		fmt.Println("What would you like to do?")
		fmt.Println("  1. Configure NVIDIA NIM")
		fmt.Println("  2. Configure OpenRouter")
		fmt.Println("  3. Configure Groq")
		fmt.Println("  4. Configure Ollama")
		fmt.Println("  5. Set default provider")
		fmt.Println("  6. Configure fallback order")
		fmt.Println("  7. Done")
		fmt.Println()

		fmt.Print("Choice [7]: ")

		var choice string
		fmt.Scanln(&choice)
		if choice == "" {
			choice = "7"
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
			cfg = setDefaultProvider(cfg)
		case "6":
			cfg = configureFallbackOrder(cfg)
		case "7":
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

// getProviderDisplayName returns the display name for a provider.
func getProviderDisplayName(name string) string {
	switch name {
	case "nvidia":
		return "NVIDIA NIM"
	case "openrouter":
		return "OpenRouter"
	case "groq":
		return "Groq"
	case "ollama":
		return "Ollama"
	default:
		return name
	}
}

// configureProvider prompts the user to configure a specific provider.
func configureProvider(cfg *config.Config, provider string) *config.Config {
	// Initialize providers map if needed
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]config.ProviderConfig)
	}

	p, exists := cfg.Providers[provider]

	fmt.Printf("\n=== Configure %s ===\n", getProviderDisplayName(provider))
	fmt.Println()

	// Get API key
	fmt.Print("API key")
	if exists && p.APIKey != "" {
		fmt.Printf(" [%s]", maskAPIKey(p.APIKey))
	}
	fmt.Print(": ")

	var apiKey string
	fmt.Scanln(&apiKey)
	apiKey = strings.TrimSpace(apiKey)

	// If user entered something, use it; otherwise keep existing
	if apiKey != "" {
		p.APIKey = apiKey
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

	// If this is the first provider, set it as default
	if cfg.ProviderDefaults.Default == "" {
		cfg.ProviderDefaults.Default = provider
	}

	fmt.Println()
	return cfg
}

// validateProviderCredentials tests the API credentials for a provider.
func validateProviderCredentials(provider, apiKey, apiBase string) error {
	switch provider {
	case "openrouter", "groq", "nvidia":
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
	for name, p := range cfg.Providers {
		if p.APIKey != "" {
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
		fmt.Printf("  %d. %s %s\n", i+1, marker, getProviderDisplayName(name))
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
	fmt.Printf("\nDefault provider set to: %s\n", getProviderDisplayName(cfg.ProviderDefaults.Default))

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
			fmt.Printf("  %d. %s\n", i+1, getProviderDisplayName(name))
		}
	}
	fmt.Println()
	fmt.Println("Available providers:")
	for i, name := range configured {
		fmt.Printf("  %d. %s\n", i+1, getProviderDisplayName(name))
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

// maskAPIKey masks an API key for display, showing only the first few and last few characters.
// Example: "sk-or-v1-abc123...xyz789" -> "sk-or-v1-****...****4c0"
func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 16 {
		return key[:2] + "****" + key[len(key)-4:]
	}
	// Show first 8 and last 4 characters
	prefix := key[:8]
	suffix := key[len(key)-4:]
	return prefix + "****...****" + suffix
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
