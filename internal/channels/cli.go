package channels

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// Channel is the interface that all chat channels must implement.
type Channel interface {
	// Name returns the unique identifier for this channel.
	Name() string

	// Start begins the channel's operation.
	// The channel should run until the context is cancelled or Stop is called.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the channel.
	Stop() error

	// Send delivers an outbound message to the channel for display.
	Send(msg bus.OutboundMessage) error
}

// CLIChannel implements the Channel interface for terminal-based interaction.
type CLIChannel struct {
	name         string
	bus          *bus.MessageBus
	mu           sync.RWMutex
	running      bool
	stopCh       chan struct{}
	inputHistory []string
	historyPos   int
	prompt       string

	// Styling
	styles *CLIStyles
}

// CLIStyles defines the lipgloss styles for the CLI.
type CLIStyles struct {
	Prompt   lipgloss.Style
	UserName lipgloss.Style
	BotName  lipgloss.Style
	Error    lipgloss.Style
	Success  lipgloss.Style
	Info     lipgloss.Style
	Command  lipgloss.Style
	Help     lipgloss.Style
	Typing   lipgloss.Style
	Border   lipgloss.Style
	Message  lipgloss.Style
	Metadata lipgloss.Style
}

// DefaultStyles returns the default CLI styles.
func DefaultStyles() *CLIStyles {
	return &CLIStyles{
		Prompt: lipgloss.NewStyle().
			Foreground(lipgloss.Color("86")). // cyan-green
			Bold(true),

		UserName: lipgloss.NewStyle().
			Foreground(lipgloss.Color("75")). // blue
			Bold(true),

		BotName: lipgloss.NewStyle().
			Foreground(lipgloss.Color("82")). // green
			Bold(true),

		Error: lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")). // red
			Bold(true),

		Success: lipgloss.NewStyle().
			Foreground(lipgloss.Color("82")), // green

		Info: lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")), // gray

		Command: lipgloss.NewStyle().
			Foreground(lipgloss.Color("228")). // yellow
			Bold(true),

		Help: lipgloss.NewStyle().
			Foreground(lipgloss.Color("250")), // light gray

		Typing: lipgloss.NewStyle().
			Foreground(lipgloss.Color("242")). // dark gray
			Italic(true),

		Border: lipgloss.NewStyle().
			Foreground(lipgloss.Color("236")), // dark gray

		Message: lipgloss.NewStyle().
			Foreground(lipgloss.Color("255")), // white

		Metadata: lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")). // gray
			Italic(true),
	}
}

// NewCLIChannel creates a new CLI channel instance.
func NewCLIChannel(bus *bus.MessageBus) *CLIChannel {
	return &CLIChannel{
		name:   "cli",
		bus:    bus,
		stopCh: make(chan struct{}),
		prompt: ">>> ",
		styles: DefaultStyles(),
	}
}

// Name returns the channel identifier.
func (c *CLIChannel) Name() string {
	return c.name
}

// Start begins the interactive CLI loop.
func (c *CLIChannel) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return fmt.Errorf("CLI channel is already running")
	}
	c.running = true
	c.mu.Unlock()

	// Start the outbound message consumer
	go c.consumeOutbound(ctx)

	// Set up signal handling for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	// Print welcome message
	c.printWelcome()

	// Main input loop
	for {
		select {
		case <-ctx.Done():
			return c.Stop()
		case <-c.stopCh:
			return nil
		case <-sigCh:
			// Handle Ctrl+C gracefully
			fmt.Println()
			fmt.Print(c.styles.Info.Render("Press /quit to exit or continue typing... "))
			continue
		default:
			// Read and process input
			line := c.readInput()
			if err := c.processInput(ctx, line); err != nil {
				if err == ErrQuit || err == ErrExit {
					return c.Stop()
				}
				c.printError(fmt.Sprintf("Error: %v", err))
			}
		}
	}
}

// Stop gracefully shuts down the CLI channel.
func (c *CLIChannel) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.running {
		return nil
	}

	c.running = false
	close(c.stopCh)

	c.printInfo("Goodbye!")
	return nil
}

// Send displays an outbound message to the terminal.
func (c *CLIChannel) Send(msg bus.OutboundMessage) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.running {
		return nil
	}

	// Format and display the message
	c.formatAndPrintMessage(msg)
	return nil
}

// consumeOutbound listens for outbound messages from the bus.
func (c *CLIChannel) consumeOutbound(ctx context.Context) {
	ch := c.bus.OutboundChannel()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case msg := <-ch:
			if msg.Channel == c.name || msg.Channel == "all" {
				_ = c.Send(msg)
			}
		}
	}
}

// readInput reads a line of input from the terminal.
func (c *CLIChannel) readInput() string {
	// Simple input handling - in production you'd use a proper readline library
	fmt.Print(c.styles.Prompt.Render(c.prompt))

	var input strings.Builder
	for {
		var line string
		fmt.Scanln(&line)
		input.WriteString(line)
		break
	}

	return input.String()
}

// processInput handles the user's input.
func (c *CLIChannel) processInput(ctx context.Context, input string) error {
	// Trim whitespace
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}

	// Add to history
	c.addToHistory(input)

	// Check for commands
	if strings.HasPrefix(input, "/") {
		return c.handleCommand(ctx, input)
	}

	// Send as inbound message to the bus
	msg := bus.InboundMessage{
		SenderID:  "cli_user",
		Content:   input,
		Channel:   c.name,
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"username": "user",
		},
	}

	// Show typing indicator
	fmt.Print(c.styles.Typing.Render("  typing...\n"))

	if !c.bus.Send(msg) {
		return fmt.Errorf("failed to send message to bus")
	}

	return nil
}

// handleCommand processes slash commands.
func (c *CLIChannel) handleCommand(ctx context.Context, input string) error {
	parts := strings.Fields(input)
	command := strings.ToLower(parts[0])

	switch command {
	case "/quit", "/exit":
		return ErrQuit

	case "/new":
		c.printInfo("Starting new session...")
		c.inputHistory = nil
		c.historyPos = 0
		c.printInfo("New session started. All context has been cleared.")
		return nil

	case "/help":
		c.printHelp()
		return nil

	case "/clear":
		// Clear the screen
		if runtime.GOOS == "windows" {
			fmt.Print("\r\n")
		} else {
			fmt.Print("\033[2J\033[H")
		}
		c.printWelcome()
		return nil

	case "/history":
		c.printHistory()
		return nil

	default:
		c.printError(fmt.Sprintf("Unknown command: %s", command))
		c.printInfo("Type /help for available commands.")
		return nil
	}
}

// addToHistory adds input to the command history.
func (c *CLIChannel) addToHistory(input string) {
	// Don't add duplicates of the last entry
	if len(c.inputHistory) > 0 && c.inputHistory[len(c.inputHistory)-1] == input {
		return
	}
	c.inputHistory = append(c.inputHistory, input)
	c.historyPos = len(c.inputHistory)
}

// printWelcome displays the welcome message.
func (c *CLIChannel) printWelcome() {
	styles := c.styles

	// Get terminal width if possible, default to 60
	width := 60
	if termWidth := getTerminalWidth(); termWidth > 0 {
		width = termWidth
	}

	// Ensure minimum width
	if width < 40 {
		width = 40
	}

	border := strings.Repeat("─", width-2)
	header := styles.BotName.Render("╭" + border + "╮")
	header2 := styles.BotName.Render("│") + styles.Message.Render(strings.Repeat(" ", width-2)) + styles.BotName.Render("│")
	header3 := styles.BotName.Render("│") + styles.Prompt.Render("  Welcome to JoshBot CLI") + strings.Repeat(" ", width-25) + styles.BotName.Render("│")
	header4 := styles.BotName.Render("│") + styles.Info.Render("  Type /help for available commands") + strings.Repeat(" ", width-38) + styles.BotName.Render("│")
	header5 := styles.BotName.Render("╰" + border + "╯")

	fmt.Println()
	fmt.Println(styles.Border.Render(header))
	fmt.Println(styles.Border.Render(header2))
	fmt.Println(styles.Border.Render(header3))
	fmt.Println(styles.Border.Render(header4))
	fmt.Println(styles.Border.Render(header2))
	fmt.Println(styles.Border.Render(header5))
	fmt.Println()
}

// printHelp displays available commands.
func (c *CLIChannel) printHelp() {
	styles := c.styles

	helpText := `
Available Commands:
  /help     - Show this help message
  /new      - Start a new session (clears context)
  /clear    - Clear the screen
  /history  - Show command history
  /quit     - Exit the program

Tips:
  • Regular messages are sent to the AI agent
  • Use ↑/↓ arrows to navigate history (if supported)
  • Press Ctrl+C to interrupt the current operation
`

	fmt.Println(styles.Help.Render(helpText))
}

// printHistory displays the command history.
func (c *CLIChannel) printHistory() {
	styles := c.styles

	if len(c.inputHistory) == 0 {
		fmt.Println(styles.Info.Render("No history yet."))
		return
	}

	fmt.Println(styles.Info.Render("Command History:"))
	for i, cmd := range c.inputHistory {
		fmt.Printf("  %d: %s\n", i+1, styles.Command.Render(cmd))
	}
}

// formatAndPrintMessage formats and displays an outbound message.
func (c *CLIChannel) formatAndPrintMessage(msg bus.OutboundMessage) {
	styles := c.styles

	// Parse mode handling
	parseMode := ""
	if msg.Metadata != nil {
		if pm, ok := msg.Metadata["parse_mode"].(string); ok {
			parseMode = pm
		}
	}

	// Format content based on parse mode
	content := msg.Content
	if parseMode == "markdown" || parseMode == "md" {
		// Simple markdown-like formatting
		content = styles.Message.Render(content)
	} else if parseMode == "html" {
		// Strip HTML tags for plain display
		content = stripHTML(content)
		content = styles.Message.Render(content)
	} else {
		content = styles.Message.Render(content)
	}

	// Print with bot name prefix
	fmt.Println()
	fmt.Print(styles.BotName.Render("🤖 Bot: "))
	fmt.Println(content)

	// Print metadata if present
	if msg.Metadata != nil {
		if replyTo, ok := msg.Metadata["reply_to"].(string); ok && replyTo != "" {
			fmt.Printf("%s %s\n", styles.Info.Render("↩️  Reply to:"), styles.Metadata.Render(replyTo))
		}
	}
	fmt.Println()
}

// printError displays an error message.
func (c *CLIChannel) printError(msg string) {
	fmt.Println(c.styles.Error.Render("✗ " + msg))
}

// printInfo displays an info message.
func (c *CLIChannel) printInfo(msg string) {
	fmt.Println(c.styles.Info.Render("ℹ " + msg))
}

// printSuccess displays a success message.
func (c *CLIChannel) printSuccess(msg string) {
	fmt.Println(c.styles.Success.Render("✓ " + msg))
}

// stripHTML removes HTML tags from a string.
func stripHTML(s string) string {
	var result strings.Builder
	inTag := false

	for _, r := range s {
		if r == '<' {
			inTag = true
		} else if r == '>' {
			inTag = false
		} else if !inTag {
			result.WriteRune(r)
		}
	}

	return result.String()
}

// getTerminalWidth attempts to get the terminal width.
func getTerminalWidth() int {
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return 0
	}
	return width
}

// Custom errors
var (
	ErrQuit = fmt.Errorf("quit signal")
	ErrExit = fmt.Errorf("exit signal")
)

// Ensure CLIChannel implements Channel
var _ Channel = (*CLIChannel)(nil)
