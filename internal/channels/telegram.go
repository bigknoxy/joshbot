package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/log"
	"github.com/bigknoxy/joshbot/internal/providers"
	"gopkg.in/telebot.v3"
)

// TelegramMaxMessageLen is the maximum message length allowed by Telegram Bot API.
const TelegramMaxMessageLen = 4096

// TelegramChannel implements the Channel interface for Telegram.
type TelegramChannel struct {
	name    string
	bus     *bus.MessageBus
	cfg     *config.TelegramConfig
	mu      sync.RWMutex
	running bool
	stopCh  chan struct{}

	// Bot instance
	bot *telebot.Bot

	// allowIDs and allowNames are the deny-by-default allowlist, partitioned by
	// entry shape. Both empty rejects everyone. See IsAllowed for why a single
	// set matched against every field is an authentication bypass.
	allowIDs   map[string]struct{}
	allowNames map[string]struct{}

	// Retry configuration
	maxRetries    int
	retryDelay    time.Duration
	maxRetryDelay time.Duration

	// Polling configuration
	pollTimeout time.Duration

	// Typing keep-alive. Telegram clears a chat action after 5 seconds, so a
	// long agent turn needs the action re-sent on a timer. One entry per chat,
	// guarded by mu; the channel in it stops that chat's keep-alive.
	typingStop     map[string]chan struct{}
	typingInterval time.Duration
	// typingMaxDuration bounds a keep-alive. Nothing calls stopTyping if the
	// agent turn dies without producing a reply, and an uncapped goroutine
	// would then call the Bot API every few seconds for the life of the
	// process — burning rate limit and showing a permanent "typing…".
	typingMaxDuration time.Duration

	// download overrides the Bot API file fetch used for image attachments.
	// Only tests set it; in production it stays nil and t.bot.File is used.
	download func(*telebot.File) (io.ReadCloser, error)

	// transcriber turns a downloaded voice note into text. Nil means voice
	// transcription is not configured and a voice message gets the honest
	// refusal instead. Set once at wiring time via SetTranscriber, before
	// Start — it is read without the lock on the hot path.
	transcriber func(ctx context.Context, audio []byte, filename string) (string, error)

	// editor overrides the bot for streaming sends and edits, and
	// streamEditInterval overrides the minimum gap between two edits of the
	// same streamed message. Only tests set either; in production the bot and
	// defaultStreamInterval are used.
	editor             telegramEditor
	streamEditInterval time.Duration

	// notifier overrides the bot for chat-action and command-menu calls.
	// Only tests set it; in production it stays nil and the bot is used.
	notifier telegramNotifier

	// apiURL and offline point createBot at a stub Bot API. Only tests set
	// them; empty/false means the real api.telegram.org.
	apiURL  string
	offline bool
}

// telegramNotifier is the slice of *telebot.Bot this channel needs for chat
// actions and command-menu registration. It exists so both can be tested
// without a live Telegram connection.
type telegramNotifier interface {
	Notify(to telebot.Recipient, action telebot.ChatAction, threadID ...int) error
	SetCommands(opts ...interface{}) error
}

// botCommands is the command menu shown in the Telegram UI. Every entry here
// must have a matching handler registered in setupHandlers. Keep the two in
// step: a menu entry without a handler is silently swallowed by the
// unknown-command fallback, and a handler without a menu entry is invisible
// in the UI.
var botCommands = []telebot.Command{
	{Text: "start", Description: "Show what this bot can do"},
	{Text: "new", Description: "Start a new session"},
	{Text: "status", Description: "Show session status"},
	{Text: "model", Description: "Switch model for this chat"},
	{Text: "personality", Description: "Set a personality"},
	{Text: "compact", Description: "Summarize older context"},
	{Text: "resume", Description: "Continue after hitting the iteration limit"},
	{Text: "help", Description: "Show the list of commands"},
}

// forwardedCommands are the slash commands whose behaviour lives in the agent
// (the CLI and Telegram share the same handlers) and are therefore routed to
// the bus by handleCommandForward. They must all appear in botCommands, and
// every botCommands entry that is not handled locally (start/help/new) must
// appear here — TestTelegramChannel_CommandMenuAndHandlersInStep pins that.
var forwardedCommands = []string{"/status", "/model", "/personality", "/compact", "/resume"}

// NewTelegramChannel creates a new Telegram channel instance.
func NewTelegramChannel(bus *bus.MessageBus, cfg *config.TelegramConfig) *TelegramChannel {
	// Build the allowlist, partitioned by entry shape for fast lookup. An
	// all-digits entry is a user ID and may only ever match the numeric ID —
	// see IsAllowed.
	allowIDs := make(map[string]struct{})
	allowNames := make(map[string]struct{})
	for _, a := range cfg.AllowFrom {
		// Normalize: strip leading '@' and lowercase
		s := normalizeUsername(a)
		if s == "" {
			continue
		}
		if isSnowflake(s) {
			allowIDs[s] = struct{}{}
		} else {
			allowNames[s] = struct{}{}
		}
	}

	return &TelegramChannel{
		name:          "telegram",
		bus:           bus,
		cfg:           cfg,
		stopCh:        make(chan struct{}),
		allowIDs:      allowIDs,
		allowNames:    allowNames,
		maxRetries:    3,
		retryDelay:    500 * time.Millisecond,
		maxRetryDelay: 5 * time.Second,
		pollTimeout:   60 * time.Second,
		typingStop:    make(map[string]chan struct{}),
		// Telegram clears a chat action after 5s; re-send just under that.
		typingInterval: 4 * time.Second,
		// Longer than any sane agent turn, short enough to not leak forever.
		typingMaxDuration: 10 * time.Minute,
	}
}

// Name returns the channel identifier.
func (t *TelegramChannel) Name() string {
	return t.name
}

// normalizeUsername normalizes a username for allowlist comparison.
func normalizeUsername(username string) string {
	return strings.ToLower(strings.TrimPrefix(username, "@"))
}

// IsAllowed checks if a user is in the allowlist.
// Entries may be a numeric Telegram user ID, a @username, or a "First Last"
// display name.
//
// An empty allowlist denies everyone. This bot hands whoever it talks to a
// direct line into an agent loop holding the shell tool, so an unset allowlist
// must fail closed, not open — see the startup warning in Start that names the
// exact config key an operator has to set.
//
// The allowlist is partitioned by entry shape and each half is matched against
// only the field it can legitimately name — the same rule internal/channels
// applies on Discord, and for the same reason. Matching every entry against
// every field let a stranger set their free-form Telegram first name to the
// operator's numeric user ID and authenticate as them: display names are not
// unique and are not validated, so an ID-shaped allowlist entry must never be
// satisfied by a name.
func (t *TelegramChannel) IsAllowed(userID int64, username, firstName, lastName string) bool {
	if len(t.allowIDs) == 0 && len(t.allowNames) == 0 {
		return false
	}

	// Check by numeric user ID. This is the form the README documents, and
	// it is the only stable identifier — a user can change their username
	// and display name at any time.
	if _, ok := t.allowIDs[strconv.FormatInt(userID, 10)]; ok {
		return true
	}

	// Check by username
	if username != "" {
		if _, ok := t.allowNames[normalizeUsername(username)]; ok {
			return true
		}
	}

	// Check by first name + last name combination
	if firstName != "" {
		fullName := normalizeUsername(firstName)
		if lastName != "" {
			fullName += " " + normalizeUsername(lastName)
		}
		if _, ok := t.allowNames[fullName]; ok {
			return true
		}
	}

	return false
}

// Start begins the Telegram bot with long polling.
func (t *TelegramChannel) Start(ctx context.Context) error {
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return fmt.Errorf("Telegram channel is already running")
	}
	t.running = true
	t.mu.Unlock()

	// Validate config
	if t.cfg.Token == "" {
		t.mu.Lock()
		t.running = false
		t.mu.Unlock()
		return fmt.Errorf("Telegram token is not configured")
	}

	// Fail closed on an unset allowlist, but loudly: the operator has a running
	// bot that rejects every message until they name who may use it. Say
	// exactly what to set so this is actionable, not a silent lockout.
	if len(t.allowIDs) == 0 && len(t.allowNames) == 0 {
		log.Warn("Telegram allowlist is empty — every sender will be rejected. " +
			"Set channels.telegram.allow_from in ~/.joshbot/config.json (or " +
			"JOSHBOT_CHANNELS__TELEGRAM__ALLOW_FROM) to your numeric Telegram " +
			"user ID before this bot can be used.")
	}

	// Create bot with polling
	bot, err := t.createBot(ctx)
	if err != nil {
		t.mu.Lock()
		t.running = false
		t.mu.Unlock()
		return fmt.Errorf("failed to create Telegram bot: %w", err)
	}

	t.mu.Lock()
	t.bot = bot
	t.mu.Unlock()

	// Start outbound message consumer
	go t.consumeOutbound(ctx)

	// Start the bot with reconnection handling
	go t.runBot(ctx, bot)

	log.Info("Telegram channel started")
	return nil
}

// createBot creates a new Telegram bot instance.
func (t *TelegramChannel) createBot(ctx context.Context) (*telebot.Bot, error) {
	settings := telebot.Settings{
		Token:   t.cfg.Token,
		Poller:  &telebot.LongPoller{Timeout: t.pollTimeout},
		Verbose: false,
		URL:     t.apiURL,
		Offline: t.offline,
	}

	// Add proxy if configured
	if t.cfg.Proxy != "" {
		proxyURL, err := url.Parse(t.cfg.Proxy)
		if err == nil {
			settings.Client = &http.Client{
				Transport: &http.Transport{
					Proxy: http.ProxyURL(proxyURL),
				},
			}
		}
	}

	bot, err := telebot.NewBot(settings)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot: %w", err)
	}

	// Set up message handlers
	t.setupHandlers(bot)

	// Publish the command menu so Telegram shows the menu button and
	// autocompletes commands. Best effort: a failure here must not stop the
	// bot from starting.
	t.registerCommandsBestEffort(bot)

	return bot, nil
}

// runBot runs the bot's polling with automatic reconnection on failure.
func (t *TelegramChannel) runBot(ctx context.Context, bot *telebot.Bot) {
	delay := t.retryDelay

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.stopCh:
			return
		default:
			// Start the bot - this blocks until stopped or error
			log.Debug("Starting Telegram bot polling")
			bot.Start()

			// Check if we should reconnect
			select {
			case <-ctx.Done():
				return
			case <-t.stopCh:
				return
			default:
			}

			// Bot stopped unexpectedly, attempt reconnection
			log.Warn("Telegram bot polling stopped, attempting to reconnect", "retry_delay", delay)

			select {
			case <-time.After(delay):
				// Exponential backoff
				delay = time.Duration(math.Min(float64(delay*2), float64(t.maxRetryDelay)))
			case <-ctx.Done():
				return
			case <-t.stopCh:
				return
			}

			// Create a new bot for reconnection
			newBot, err := t.createBot(ctx)
			if err != nil {
				log.Error("Failed to create bot for reconnection", "error", err)
				continue
			}

			t.mu.Lock()
			t.bot = newBot
			t.mu.Unlock()

			// Set up handlers on new bot
			t.setupHandlers(newBot)

			// Rebind the loop's local bot so the next iteration restarts
			// the new bot instead of the stale one.
			bot = newBot
		}
	}
}

// setupHandlers registers all message handlers for the bot.
func (t *TelegramChannel) setupHandlers(bot *telebot.Bot) {
	// Text messages handler (including commands)
	bot.Handle(telebot.OnText, func(ctx telebot.Context) error {
		return t.handleMessage(ctx)
	})

	// Photo handler
	bot.Handle(telebot.OnPhoto, func(ctx telebot.Context) error {
		return t.handlePhoto(ctx)
	})

	// Voice message handler
	bot.Handle(telebot.OnVoice, func(ctx telebot.Context) error {
		return t.handleVoice(ctx)
	})

	// Document handler
	bot.Handle(telebot.OnDocument, func(ctx telebot.Context) error {
		return t.handleDocument(ctx)
	})

	// Audio handler
	bot.Handle(telebot.OnAudio, func(ctx telebot.Context) error {
		return t.handleAudio(ctx)
	})

	// Video handler
	bot.Handle(telebot.OnVideo, func(ctx telebot.Context) error {
		return t.handleVideo(ctx)
	})

	// Sticker handler
	bot.Handle(telebot.OnSticker, func(ctx telebot.Context) error {
		return t.handleSticker(ctx)
	})

	// Callback queries (button presses)
	bot.Handle(telebot.OnCallback, func(ctx telebot.Context) error {
		return t.handleCallback(ctx)
	})

	// Edited messages
	bot.Handle(telebot.OnEdited, func(ctx telebot.Context) error {
		return t.handleEdited(ctx)
	})

	// Commands using Handle with string endpoint
	bot.Handle("/start", func(ctx telebot.Context) error {
		return t.handleStart(ctx)
	})

	bot.Handle("/help", func(ctx telebot.Context) error {
		return t.handleHelp(ctx)
	})

	bot.Handle("/new", func(ctx telebot.Context) error {
		return t.handleNew(ctx)
	})

	// Commands whose behaviour lives in the agent (shared with the CLI): the
	// channel packages the raw text and routes it to the bus, and the agent
	// answers on the outbound channel. The set here must mirror botCommands.
	for _, command := range forwardedCommands {
		cmd := command
		bot.Handle(cmd, func(ctx telebot.Context) error {
			return t.handleCommandForward(ctx, cmd)
		})
	}

	// Any other message types we want to acknowledge but not process
	bot.Handle(telebot.OnVenue, func(ctx telebot.Context) error {
		return t.handleUnsupported(ctx, "venue")
	})

	bot.Handle(telebot.OnLocation, func(ctx telebot.Context) error {
		return t.handleUnsupported(ctx, "location")
	})

	bot.Handle(telebot.OnContact, func(ctx telebot.Context) error {
		return t.handleUnsupported(ctx, "contact")
	})
}

// handleMessage processes incoming text messages.
func (t *TelegramChannel) handleMessage(ctx telebot.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("panic in telegram message handler",
				"panic", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()),
			)
			err = fmt.Errorf("panic: %v", r)
		}
	}()

	msg := ctx.Message()
	log.Debug("handleMessage called", "sender", msg.Sender.ID)

	// Check if it's a command - let specific handlers deal with it
	if strings.HasPrefix(msg.Text, "/") {
		if isKnownCommand(msg.Text) {
			// Commands are handled by their specific handlers
			return nil
		}
		// An unknown command reaches OnText because no handler claimed it.
		// Say so rather than swallowing it, but never leak the command list
		// to someone outside the allowlist.
		if !t.IsAllowed(int64(msg.Sender.ID), msg.Sender.Username, msg.Sender.FirstName, msg.Sender.LastName) {
			return nil
		}
		_, err := ctx.Bot().Send(ctx.Sender(), unknownCommandText(msg.Text))
		if err != nil {
			log.Error("failed to send unknown-command reply", "error", err)
		}
		return nil
	}

	// Check allowlist
	if !t.IsAllowed(int64(msg.Sender.ID), msg.Sender.Username, msg.Sender.FirstName, msg.Sender.LastName) {
		// Send a polite rejection
		_, err := ctx.Bot().Send(ctx.Sender(), "⛔ You are not authorized to use this bot.")
		if err != nil {
			log.Error("failed to send authorization rejection", "error", err)
		}
		return nil
	}

	// Show typing indicator
	t.startTyping(ctx.Chat())

	// Convert to InboundMessage and send to bus
	inbound := t.convertToInboundMessage(msg)
	if !t.bus.Send(inbound) {
		log.Error("failed to send message to bus", "sender", msg.Sender.Username)
		// The keep-alive would otherwise show "typing…" for up to ten
		// minutes over a message that was dropped.
		t.stopTyping(ctx.Chat())
		_, err := ctx.Bot().Send(ctx.Sender(), "Sorry, I couldn't process your message. Please try again.")
		return err
	}

	return nil
}

// commandName extracts the bare command from message text: "/new extra" and
// "/new@joshbot" both yield "new".
func commandName(text string) string {
	name := strings.TrimPrefix(text, "/")
	if i := strings.IndexAny(name, " \t\n"); i >= 0 {
		name = name[:i]
	}
	if i := strings.Index(name, "@"); i >= 0 {
		name = name[:i]
	}
	return strings.ToLower(name)
}

// isKnownCommand reports whether the text names a command that has its own
// handler registered in setupHandlers.
func isKnownCommand(text string) bool {
	name := commandName(text)
	for _, c := range botCommands {
		if c.Text == name {
			return true
		}
	}
	return false
}

// unknownCommandText tells the user their command does not exist and lists the
// ones that do.
func unknownCommandText(text string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Unknown command: /%s\n\nAvailable commands:", commandName(text))
	for _, c := range botCommands {
		fmt.Fprintf(&b, "\n/%s - %s", c.Text, c.Description)
	}
	b.WriteString("\n\nOr just send me a message.")
	return b.String()
}

// handleStart handles the /start command.
func (t *TelegramChannel) handleStart(ctx telebot.Context) error {
	return t.handleHelp(ctx)
}

// handleHelp handles the /help command.
func (t *TelegramChannel) handleHelp(ctx telebot.Context) error {
	helpText := `🤖 *JoshBot*

Welcome! I'm here to help you.

Available commands:
/start - Show what this bot can do
/new - Start a new session
/status - Show session status
/model <name> - Switch model for this chat (add --global for all chats)
/personality <name> - Set a personality (or /personality none to clear)
/compact - Summarize older context now
/resume - Continue after hitting the iteration limit
/help - Show this help

Just send me a message and I'll respond!`

	_, err := ctx.Bot().Send(ctx.Sender(), helpText, &telebot.SendOptions{
		ParseMode: telebot.ModeMarkdown,
	})
	return err
}

// handleNew handles the /new command to start a new session.
func (t *TelegramChannel) handleNew(ctx telebot.Context) error {
	msg := ctx.Message()
	log.Debug("handleNew called", "sender", msg.Sender.ID)

	// The same allowlist gate handleMessage and handleCommandForward apply:
	// /new is dispatched outside handleMessage, so it must be re-checked here
	// or an unallowed caller could still trigger agent work.
	if !t.IsAllowed(int64(msg.Sender.ID), msg.Sender.Username, msg.Sender.FirstName, msg.Sender.LastName) {
		return nil
	}

	// Send new session command to bus
	inbound := bus.InboundMessage{
		SenderID:  fmt.Sprintf("telegram_%d", msg.Sender.ID),
		Content:   "/new",
		Channel:   t.name,
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"message_id": msg.ID,
			"chat_id":    msg.Chat.ID,
			"username":   msg.Sender.Username,
			"is_command": true,
		},
	}
	if !t.bus.Send(inbound) {
		log.Error("failed to send /new command to bus")
		_, err := ctx.Bot().Send(ctx.Sender(), "Sorry, couldn't start a new session. Please try again.")
		return err
	}

	_, err := ctx.Bot().Send(ctx.Sender(), "🔄 Starting new session...")
	return err
}

// handleCommandForward routes a slash command to the agent through the bus.
// The command's behaviour is owned by the agent (the CLI uses the same
// handlers), so the channel only packages the raw text and its routing
// metadata. Replies come back on the outbound channel.
//
// Unlike the unknown-command fallback, telebot routes a registered command
// directly to its handler, so the allowlist check that lives inside
// handleMessage is never reached here. It is repeated explicitly: a command
// like /status or /model would otherwise disclose bot configuration to anyone
// who can reach the bot, not just users the operator allowed.
func (t *TelegramChannel) handleCommandForward(ctx telebot.Context, command string) error {
	msg := ctx.Message()
	log.Debug("forwarding command to agent", "sender", msg.Sender.ID, "command", command)

	if !t.IsAllowed(int64(msg.Sender.ID), msg.Sender.Username, msg.Sender.FirstName, msg.Sender.LastName) {
		return nil
	}

	t.startTyping(ctx.Chat())

	inbound := bus.InboundMessage{
		SenderID:  fmt.Sprintf("telegram_%d", msg.Sender.ID),
		Content:   msg.Text,
		Channel:   t.name,
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"message_id": msg.ID,
			"chat_id":    msg.Chat.ID,
			"username":   msg.Sender.Username,
			"is_command": true,
		},
	}
	if !t.bus.Send(inbound) {
		log.Error("failed to send command to bus", "command", command)
		_, err := ctx.Bot().Send(ctx.Sender(), "Sorry, I couldn't process that. Please try again.")
		return err
	}
	return nil
}

// handlePhoto processes incoming photos.
func (t *TelegramChannel) handlePhoto(ctx telebot.Context) error {
	msg := ctx.Message()

	if !t.IsAllowed(int64(msg.Sender.ID), msg.Sender.Username, msg.Sender.FirstName, msg.Sender.LastName) {
		return nil
	}

	// Show typing indicator
	t.startTyping(ctx.Chat())

	// Build content with photo info
	photo := msg.Photo
	content := "[Photo]"
	if photo.Caption != "" {
		content = fmt.Sprintf("[Photo with caption]: %s", photo.Caption)
	}

	// The download happens only after the allowlist check above.
	img, ok := t.attachImage(ctx, photo.File, "photo", int64(photo.FileSize))
	if !ok {
		return nil
	}

	inbound := bus.InboundMessage{
		SenderID:  fmt.Sprintf("telegram_%d", msg.Sender.ID),
		Content:   content,
		Channel:   t.name,
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"message_id":     msg.ID,
			"chat_id":        msg.Chat.ID,
			"username":       msg.Sender.Username,
			"first_name":     msg.Sender.FirstName,
			"last_name":      msg.Sender.LastName,
			"media_type":     "photo",
			"file_id":        photo.File.FileID,
			"file_unique_id": photo.File.UniqueID,
			"caption":        photo.Caption,
			"width":          photo.Width,
			"height":         photo.Height,
		},
	}

	inbound.Images = []providers.Image{img}

	if !t.bus.Send(inbound) {
		log.Error("failed to send photo message to bus", "error", "queue full")
	}

	return nil
}

// handleVoice processes incoming voice messages.
func (t *TelegramChannel) handleVoice(ctx telebot.Context) error {
	msg := ctx.Message()

	if !t.IsAllowed(int64(msg.Sender.ID), msg.Sender.Username, msg.Sender.FirstName, msg.Sender.LastName) {
		return nil
	}

	// With a transcriber wired, the voice note is downloaded (after the
	// allowlist check above, like every attachment) and transcribed, and the
	// transcript runs through the normal agent pipeline. Without one, the
	// honesty rules from the imperceptible-media pass apply: a captionless
	// voice message must not be forwarded as a "[Voice message]" placeholder
	// — the model answers it confidently, about nothing — so the channel
	// answers honestly itself; a caption is forwarded framed so the model
	// knows what it cannot perceive.
	voice := msg.Voice
	// Typing starts before the transcription round-trip, which takes seconds;
	// the refusal path stops it again via replyCannotPerceive.
	t.startTyping(ctx.Chat())
	var content string
	switch {
	case t.transcriber != nil:
		transcript, ok := t.attachVoiceTranscript(ctx, voice)
		if !ok {
			return nil
		}
		content = fmt.Sprintf("[Voice message, transcribed]: %s", transcript)
		if voice.Caption != "" {
			content += fmt.Sprintf("\n[Its caption]: %s", voice.Caption)
		}
	case voice.Caption == "":
		return t.replyCannotPerceive(ctx, "🎙️ I can't listen to voice messages — the operator can enable transcription with the stt config. Mind typing that out?")
	default:
		content = fmt.Sprintf("[The user sent a voice message you cannot hear. Its caption]: %s", voice.Caption)
	}

	inbound := bus.InboundMessage{
		SenderID:  fmt.Sprintf("telegram_%d", msg.Sender.ID),
		Content:   content,
		Channel:   t.name,
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"message_id":     msg.ID,
			"chat_id":        msg.Chat.ID,
			"username":       msg.Sender.Username,
			"first_name":     msg.Sender.FirstName,
			"last_name":      msg.Sender.LastName,
			"media_type":     "voice",
			"file_id":        voice.File.FileID,
			"file_unique_id": voice.File.UniqueID,
			"duration":       voice.Duration,
			"mime_type":      voice.MIME,
			"caption":        voice.Caption,
		},
	}

	if !t.bus.Send(inbound) {
		log.Error("failed to send voice message to bus", "error", "queue full")
	}

	return nil
}

// handleDocument processes incoming documents.
func (t *TelegramChannel) handleDocument(ctx telebot.Context) error {
	msg := ctx.Message()

	if !t.IsAllowed(int64(msg.Sender.ID), msg.Sender.Username, msg.Sender.FirstName, msg.Sender.LastName) {
		return nil
	}

	t.startTyping(ctx.Chat())

	doc := msg.Document
	content := fmt.Sprintf("[Document: %s]", doc.FileName)
	if doc.Caption != "" {
		content = fmt.Sprintf("[Document: %s]\n%s", doc.FileName, doc.Caption)
	}

	// What happens to a document depends on whether the agent can perceive
	// it. An image claim earns a download and rides the turn as an image; a
	// text-like claim earns a download and is inlined as the message text; a
	// binary blob is refused honestly (or its caption forwarded, framed),
	// because "[Document: report.xlsx]" alone got a confident answer about a
	// file nobody opened. Claims decide only whether to spend the download —
	// the bytes decide what they are.
	var images []providers.Image
	switch {
	case providers.IsSupportedImageMIME(doc.MIME):
		img, ok := t.attachImage(ctx, doc.File, doc.FileName, int64(doc.FileSize))
		if !ok {
			return nil
		}
		images = []providers.Image{img}
	case isTextLikeDocument(doc.MIME, doc.FileName):
		text, ok := t.attachTextDocument(ctx, doc)
		if !ok {
			return nil
		}
		content = fmt.Sprintf("%s\n%s", content, text)
	case doc.Caption == "":
		return t.replyCannotPerceive(ctx, fmt.Sprintf("📄 I can't open %q yet — I can read text files (txt, md, csv, json, code) and images.", doc.FileName))
	default:
		content = fmt.Sprintf("[The user sent a file you cannot open (%s). Its caption]: %s", doc.FileName, doc.Caption)
	}

	inbound := bus.InboundMessage{
		SenderID:  fmt.Sprintf("telegram_%d", msg.Sender.ID),
		Content:   content,
		Channel:   t.name,
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"message_id":     msg.ID,
			"chat_id":        msg.Chat.ID,
			"username":       msg.Sender.Username,
			"first_name":     msg.Sender.FirstName,
			"last_name":      msg.Sender.LastName,
			"media_type":     "document",
			"file_id":        doc.File.FileID,
			"file_unique_id": doc.File.UniqueID,
			"file_name":      doc.FileName,
			"mime_type":      doc.MIME,
			"file_size":      doc.FileSize,
			"caption":        doc.Caption,
		},
	}

	inbound.Images = images

	if !t.bus.Send(inbound) {
		log.Error("failed to send document message to bus", "error", "queue full")
	}

	return nil
}

// handleAudio processes incoming audio files.
func (t *TelegramChannel) handleAudio(ctx telebot.Context) error {
	msg := ctx.Message()

	if !t.IsAllowed(int64(msg.Sender.ID), msg.Sender.Username, msg.Sender.FirstName, msg.Sender.LastName) {
		return nil
	}

	// Same honesty rule as voice: no caption, no agent turn.
	audio := msg.Audio
	if audio.Caption == "" {
		return t.replyCannotPerceive(ctx, "🎵 I can't listen to audio yet.")
	}
	t.startTyping(ctx.Chat())

	content := fmt.Sprintf("[The user sent an audio file you cannot hear (%s). Its caption]: %s", audio.Title, audio.Caption)

	inbound := bus.InboundMessage{
		SenderID:  fmt.Sprintf("telegram_%d", msg.Sender.ID),
		Content:   content,
		Channel:   t.name,
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"message_id":     msg.ID,
			"chat_id":        msg.Chat.ID,
			"username":       msg.Sender.Username,
			"first_name":     msg.Sender.FirstName,
			"last_name":      msg.Sender.LastName,
			"media_type":     "audio",
			"file_id":        audio.File.FileID,
			"file_unique_id": audio.File.UniqueID,
			"title":          audio.Title,
			"performer":      audio.Performer,
			"duration":       audio.Duration,
			"mime_type":      audio.MIME,
			"file_size":      audio.FileSize,
			"caption":        audio.Caption,
		},
	}

	if !t.bus.Send(inbound) {
		log.Error("failed to send audio message to bus", "error", "queue full")
	}

	return nil
}

// handleVideo processes incoming videos.
func (t *TelegramChannel) handleVideo(ctx telebot.Context) error {
	msg := ctx.Message()

	if !t.IsAllowed(int64(msg.Sender.ID), msg.Sender.Username, msg.Sender.FirstName, msg.Sender.LastName) {
		return nil
	}

	// Same honesty rule as voice: no caption, no agent turn.
	video := msg.Video
	if video.Caption == "" {
		return t.replyCannotPerceive(ctx, "🎬 I can't watch videos yet.")
	}
	t.startTyping(ctx.Chat())

	content := fmt.Sprintf("[The user sent a video you cannot watch. Its caption]: %s", video.Caption)

	inbound := bus.InboundMessage{
		SenderID:  fmt.Sprintf("telegram_%d", msg.Sender.ID),
		Content:   content,
		Channel:   t.name,
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"message_id":     msg.ID,
			"chat_id":        msg.Chat.ID,
			"username":       msg.Sender.Username,
			"first_name":     msg.Sender.FirstName,
			"last_name":      msg.Sender.LastName,
			"media_type":     "video",
			"file_id":        video.File.FileID,
			"file_unique_id": video.File.UniqueID,
			"duration":       video.Duration,
			"width":          video.Width,
			"height":         video.Height,
			"mime_type":      video.MIME,
			"file_size":      video.FileSize,
			"caption":        video.Caption,
		},
	}

	if !t.bus.Send(inbound) {
		log.Error("failed to send video message to bus", "error", "queue full")
	}

	return nil
}

// handleSticker processes incoming stickers.
func (t *TelegramChannel) handleSticker(ctx telebot.Context) error {
	msg := ctx.Message()

	if !t.IsAllowed(int64(msg.Sender.ID), msg.Sender.Username, msg.Sender.FirstName, msg.Sender.LastName) {
		return nil
	}

	// A sticker carries no content the agent can act on, and forwarding
	// "[Sticker]" spent a full LLM turn on producing a confused answer.
	// Ignore it quietly — a sticker accompanies conversation, it isn't a
	// question.
	log.Debug("ignoring sticker", "emoji", msg.Sticker.Emoji, "set", msg.Sticker.SetName)
	return nil
}

// replyCannotPerceive answers a media message the agent has no way to
// perceive (voice, audio, video) honestly from the channel itself, spending
// no agent turn. Forwarding a "[Voice message]" placeholder instead got the
// user a confident answer about content nobody heard. Threaded to the media
// message; plain text on purpose (the note contains no formatting).
func (t *TelegramChannel) replyCannotPerceive(ctx telebot.Context, note string) error {
	t.stopTyping(ctx.Chat())
	b := ctx.Bot()
	if b == nil {
		return nil
	}
	_, err := b.Send(ctx.Chat(), note, &telebot.SendOptions{ReplyTo: ctx.Message()})
	return err
}

// handleCallback processes callback queries from inline buttons.
func (t *TelegramChannel) handleCallback(ctx telebot.Context) error {
	cb := ctx.Callback()

	if !t.IsAllowed(int64(cb.Sender.ID), cb.Sender.Username, cb.Sender.FirstName, cb.Sender.LastName) {
		return nil
	}

	content := fmt.Sprintf("[Callback: %s]", cb.Data)

	inbound := bus.InboundMessage{
		SenderID:  fmt.Sprintf("telegram_%d", cb.Sender.ID),
		Content:   content,
		Channel:   t.name,
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"callback_id":   cb.ID,
			"message_id":    cb.Message.ID,
			"chat_id":       cb.Message.Chat.ID,
			"username":      cb.Sender.Username,
			"first_name":    cb.Sender.FirstName,
			"last_name":     cb.Sender.LastName,
			"callback_data": cb.Data,
			"media_type":    "callback",
		},
	}

	// Answer the callback
	if err := ctx.Bot().Respond(cb); err != nil {
		log.Warn("failed to answer callback", "error", err)
	}

	if !t.bus.Send(inbound) {
		log.Error("failed to send callback to bus", "error", "queue full")
	}

	return nil
}

// handleEdited processes edited messages.
func (t *TelegramChannel) handleEdited(ctx telebot.Context) error {
	msg := ctx.Message()

	if !t.IsAllowed(int64(msg.Sender.ID), msg.Sender.Username, msg.Sender.FirstName, msg.Sender.LastName) {
		return nil
	}

	// An edit is almost always a correction of a message the agent already
	// answered. The bare "[Edited]:" framing read as brand-new context and
	// earned a full fresh answer; naming it a correction lets the model
	// answer the fixed question and skip re-answering what did not change.
	content := fmt.Sprintf("[The user edited a previous message; treat this as the corrected version]: %s", msg.Text)

	inbound := bus.InboundMessage{
		SenderID:  fmt.Sprintf("telegram_%d", msg.Sender.ID),
		Content:   content,
		Channel:   t.name,
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"message_id": msg.ID,
			"chat_id":    msg.Chat.ID,
			"username":   msg.Sender.Username,
			"first_name": msg.Sender.FirstName,
			"last_name":  msg.Sender.LastName,
			"media_type": "edited",
		},
	}

	if !t.bus.Send(inbound) {
		log.Error("failed to send edited message to bus", "error", "queue full")
	}

	return nil
}

// handleUnsupported handles unsupported message types.
func (t *TelegramChannel) handleUnsupported(ctx telebot.Context, mediaType string) error {
	msg := ctx.Message()

	if !t.IsAllowed(int64(msg.Sender.ID), msg.Sender.Username, msg.Sender.FirstName, msg.Sender.LastName) {
		return nil
	}

	_, err := ctx.Bot().Send(ctx.Sender(), fmt.Sprintf("Sorry, I don't support %s messages yet.", mediaType))
	return err
}

// convertToInboundMessage converts a Telegram message to an InboundMessage.
func (t *TelegramChannel) convertToInboundMessage(msg *telebot.Message) bus.InboundMessage {
	content := msg.Text

	return bus.InboundMessage{
		SenderID:  fmt.Sprintf("telegram_%d", msg.Sender.ID),
		Content:   content,
		Channel:   t.name,
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"message_id": msg.ID,
			"chat_id":    msg.Chat.ID,
			"username":   msg.Sender.Username,
			"first_name": msg.Sender.FirstName,
			"last_name":  msg.Sender.LastName,
			"is_command": strings.HasPrefix(content, "/"),
		},
	}
}

// startTyping shows "typing…" for a chat and keeps it alive until stopTyping
// is called, the channel shuts down, or the process exits. Telegram clears a
// chat action after 5 seconds, but an agent turn routinely runs far longer, so
// a single sendChatAction leaves the user staring at an idle chat.
// Calling it again for a chat that is already typing is a no-op.
func (t *TelegramChannel) startTyping(recipient telebot.Recipient) {
	key, ok := recipientKey(recipient)
	if !ok {
		return
	}

	t.mu.Lock()
	notifier := t.currentNotifierLocked()
	if notifier == nil {
		t.mu.Unlock()
		return
	}
	if _, ok := t.typingStop[key]; ok {
		t.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	t.typingStop[key] = stop
	interval := t.typingInterval
	maxDuration := t.typingMaxDuration
	shutdown := t.stopCh
	t.mu.Unlock()

	go func() {
		notifyTyping(notifier, recipient)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		var expired <-chan time.Time
		if maxDuration > 0 {
			timer := time.NewTimer(maxDuration)
			defer timer.Stop()
			expired = timer.C
		}

		for {
			select {
			case <-stop:
				return
			case <-shutdown:
				return
			case <-expired:
				// Give up rather than type at Telegram forever. Clear the map
				// entry so a later message for this chat can start again.
				t.clearTyping(key, stop)
				return
			case <-ticker.C:
				notifyTyping(notifier, recipient)
			}
		}
	}()
}

// stopTyping ends the keep-alive for a chat. Stopping a chat that is not
// typing is a no-op.
func (t *TelegramChannel) stopTyping(recipient telebot.Recipient) {
	key, ok := recipientKey(recipient)
	if !ok {
		return
	}

	t.mu.Lock()
	stop, ok := t.typingStop[key]
	if ok {
		delete(t.typingStop, key)
	}
	t.mu.Unlock()

	if ok {
		close(stop)
	}
}

// clearTyping removes a chat's keep-alive entry, but only if it is still the
// one this goroutine owns — a newer keep-alive for the same chat must not be
// evicted by an older one expiring.
func (t *TelegramChannel) clearTyping(key string, own chan struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cur, ok := t.typingStop[key]; ok && cur == own {
		delete(t.typingStop, key)
	}
}

// recipientKey returns the map key for a recipient, reporting false when there
// is nothing usable to key on. A nil *telebot.Chat or *telebot.User carried in
// a non-nil interface would panic inside Recipient(), so both are screened.
func recipientKey(recipient telebot.Recipient) (string, bool) {
	switch r := recipient.(type) {
	case nil:
		return "", false
	case *telebot.Chat:
		if r == nil {
			return "", false
		}
	case *telebot.User:
		if r == nil {
			return "", false
		}
	}
	key := recipient.Recipient()
	if key == "" {
		return "", false
	}
	return key, true
}

// currentNotifierLocked returns the notifier to use for chat actions and the
// command menu. Callers must hold mu.
func (t *TelegramChannel) currentNotifierLocked() telegramNotifier {
	if t.notifier != nil {
		return t.notifier
	}
	if t.bot != nil {
		return t.bot
	}
	return nil
}

func notifyTyping(notifier telegramNotifier, recipient telebot.Recipient) {
	if err := notifier.Notify(recipient, telebot.Typing); err != nil {
		log.Debug("failed to send typing indicator", "error", err)
	}
}

// registerCommands publishes the command menu so Telegram shows the menu
// button and autocompletes commands in private chats.
func (t *TelegramChannel) registerCommands(notifier telegramNotifier) error {
	if notifier == nil {
		return fmt.Errorf("no bot available to register commands")
	}
	return notifier.SetCommands(
		botCommands,
		telebot.CommandScope{Type: telebot.CommandScopeAllPrivateChats},
	)
}

// registerCommandsBestEffort registers the command menu, logging rather than
// failing: a missing menu must never stop the bot from starting.
func (t *TelegramChannel) registerCommandsBestEffort(notifier telegramNotifier) {
	if err := t.registerCommands(notifier); err != nil {
		log.Warn("failed to register Telegram command menu", "error", err)
		return
	}
	log.Debug("registered Telegram command menu", "commands", len(botCommands))
}

// Stop gracefully shuts down the Telegram channel.
func (t *TelegramChannel) Stop() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.running {
		return nil
	}

	t.running = false
	close(t.stopCh)

	// The keep-alive goroutines also select on stopCh, so closing it is what
	// actually stops them; dropping the entries just releases the map.
	t.typingStop = make(map[string]chan struct{})

	// Stop the bot's long poller so runBot's blocking bot.Start() call
	// returns. bot.Close() is the Telegram Bot API "close" session-teardown
	// RPC (for moving a bot between API servers) and never unblocks
	// bot.Start(), which would leak the poller goroutine.
	if t.bot != nil {
		t.bot.Stop()
		t.bot = nil
	}

	log.Info("Telegram channel stopped")
	return nil
}

// Send delivers an outbound message to Telegram.
// Splits messages exceeding TelegramMaxMessageLen into multiple messages.
func (t *TelegramChannel) Send(msg bus.OutboundMessage) error {
	t.mu.RLock()
	bot := t.bot
	t.mu.RUnlock()

	if bot == nil {
		return fmt.Errorf("bot not initialized")
	}

	log.Debug("Send called", "channelID", msg.ChannelID, "metadata_chat_id", msg.Metadata["chat_id"])

	var recipient telebot.Recipient
	if msg.ChannelID != "" {
		if chatID, err := strconv.ParseInt(msg.ChannelID, 10, 64); err == nil {
			recipient = telebot.ChatID(chatID)
		}
	}
	if recipient == nil {
		if cid, ok := msg.Metadata["chat_id"].(int64); ok {
			recipient = telebot.ChatID(cid)
		} else if cid, ok := msg.Metadata["chat_id"].(float64); ok {
			recipient = telebot.ChatID(int64(cid))
		}
	}
	if recipient == nil {
		return fmt.Errorf("no valid recipient specified")
	}

	// The reply is on its way, so the "typing…" keep-alive for this chat has
	// done its job.
	t.stopTyping(recipient)

	parseMode := telebot.ModeDefault
	if pm, ok := msg.Metadata["parse_mode"].(string); ok {
		switch strings.ToLower(pm) {
		case "markdown", "md":
			parseMode = telebot.ModeMarkdown
		case "html":
			parseMode = telebot.ModeHTML
		}
	}

	// Parts after the first carry a "— Part N of M —" header. Re-split with
	// that reserved so header+part still fits, rather than truncating the
	// part afterwards — truncation used to chop off the closing code fence
	// that splitMessage had just added.
	const partHeaderOverhead = 35
	parts := splitMessage(msg.Content, TelegramMaxMessageLen)
	if len(parts) > 1 {
		parts = splitMessage(msg.Content, TelegramMaxMessageLen-partHeaderOverhead)
	}

	for i, part := range parts {
		partOpts := telebot.SendOptions{ParseMode: parseMode}
		if i == 0 {
			if replyToMsg, ok := msg.Metadata["reply_to_message"].(**telebot.Message); ok && replyToMsg != nil {
				partOpts.ReplyTo = *replyToMsg
			}
			// The gateway carries the triggering message's id, not a
			// *telebot.Message: an id-only reply target is all Telegram
			// needs to thread the answer under its question.
			switch id := msg.Metadata["reply_to_id"].(type) {
			case int:
				if id != 0 {
					partOpts.ReplyTo = &telebot.Message{ID: id}
				}
			case float64:
				if id != 0 {
					partOpts.ReplyTo = &telebot.Message{ID: int(id)}
				}
			}
			if markup, ok := msg.Metadata["reply_markup"].(map[string]any); ok {
				partOpts.ReplyMarkup = t.buildReplyMarkup(markup)
			}
		}

		content := part
		if i > 0 {
			content = fmt.Sprintf("\n\n— *Part %d of %d* —\n\n", i+1, len(parts)) + part
		}

		var lastErr error
		delay := t.retryDelay

		for attempt := 0; attempt < t.maxRetries; attempt++ {
			_, err := bot.Send(recipient, content, &partOpts)
			if err == nil {
				lastErr = nil
				break
			}

			// Never lose a message to a formatting error: Telegram rejected
			// the entities in this part, so retry the same content once with
			// ParseMode cleared. Plain text always sends. Clearing partOpts
			// (not just this attempt) means a later retry in this same loop
			// also stays plain rather than re-triggering the same rejection.
			if isParseEntityError(err) && partOpts.ParseMode != telebot.ModeDefault {
				log.Warn("telegram rejected message formatting, retrying as plain text",
					"part", i+1, "total_parts", len(parts), "error", err,
				)
				partOpts.ParseMode = telebot.ModeDefault
				if _, fallbackErr := bot.Send(recipient, content, &partOpts); fallbackErr == nil {
					lastErr = nil
					break
				} else {
					err = fallbackErr
				}
			}

			lastErr = err
			log.Warn("failed to send message part, retrying",
				"attempt", attempt+1, "max_retries", t.maxRetries,
				"part", i+1, "total_parts", len(parts),
				"error", err, "recipient", msg.ChannelID,
			)
			if !isRetryable(err) {
				break
			}
			select {
			case <-time.After(delay):
				delay = time.Duration(math.Min(float64(delay*2), float64(t.maxRetryDelay)))
			case <-t.stopCh:
				return fmt.Errorf("stopped while retrying: %w", lastErr)
			}
		}
		if lastErr != nil {
			return fmt.Errorf("failed to send message part %d/%d after %d retries: %w",
				i+1, len(parts), t.maxRetries, lastErr)
		}

		if i < len(parts)-1 {
			select {
			case <-time.After(500 * time.Millisecond):
			case <-t.stopCh:
				return nil
			}
		}
	}

	return nil
}

// splitMessage splits a message into chunks of at most maxLen bytes each.
// Avoids splitting inside markdown code blocks by closing them before the
// split and reopening them after. Tries to split on newlines first. A
// non-positive maxLen leaves the content unsplit.
func splitMessage(content string, maxLen int) []string {
	if maxLen <= 0 || len(content) <= maxLen {
		return []string{content}
	}

	var parts []string
	for len(content) > 0 {
		if len(content) <= maxLen {
			parts = append(parts, content)
			break
		}

		prefix, suffix := splitOnce(content, maxLen)
		parts = append(parts, prefix)
		content = suffix
	}

	return parts
}

// splitOnce cuts one part of at most maxLen bytes off the front of content and
// returns it with the exact remainder. Callers that need both halves must use
// this rather than re-deriving the remainder from the part: the fence
// bookkeeping is lossy in that direction, since a part genuinely ending in a
// closing fence is indistinguishable from one this function closed.
// content must be longer than maxLen.
func splitOnce(content string, maxLen int) (string, string) {
	// Closing a block appends fenceClose to this part and reopening prepends
	// fenceOpen to the next one; both have to fit inside maxLen.
	const (
		fenceClose = "\n```"
		fenceOpen  = "```\n"
	)

	{
		// Reserve room for the closing fence up front. Appending it after
		// slicing at maxLen would push the part past Telegram's hard limit,
		// and Telegram rejects the whole send rather than truncating.
		limit := maxLen
		if strings.Count(content[:maxLen], "```")%2 == 1 {
			limit = maxLen - len(fenceClose)
		}
		if limit < 1 {
			limit = 1
		}

		splitAt := strings.LastIndex(content[:limit], "\n")
		// Reopening a block prepends fenceOpen to the remainder. Splitting at
		// or before that length would leave the remainder no shorter than it
		// started, so the loop would never terminate. Hard split instead.
		if splitAt <= len(fenceOpen) {
			splitAt = limit
		}
		splitAt = runeBoundary(content, splitAt)

		prefix := content[:splitAt]
		suffix := content[splitAt:]

		// If the prefix has an odd number of code fences, we're mid-code-block.
		// Close the block at the end of this part and reopen at the start of the next.
		if strings.Count(prefix, "```")%2 == 1 {
			prefix += fenceClose
			suffix = fenceOpen + suffix
		}

		// The remainder must always shrink and every part must carry at least
		// one byte; anything else would hang the outbound consumer.
		if len(suffix) >= len(content) || len(prefix) == 0 {
			splitAt = runeBoundary(content, maxLen)
			if splitAt == 0 {
				splitAt = maxLen
			}
			prefix, suffix = content[:splitAt], content[splitAt:]
		}

		return prefix, suffix
	}
}

// runeBoundary backs idx up to the start of a UTF-8 rune so that a hard split
// never cuts a multibyte character in half. Telegram counts UTF-16 code
// units, so a byte limit is always conservative — the risk here is corruption,
// not overflow.
func runeBoundary(s string, idx int) int {
	if idx >= len(s) {
		return len(s)
	}
	for idx > 0 && !utf8.RuneStart(s[idx]) {
		idx--
	}
	return idx
}

// isParseEntityError reports whether err is Telegram rejecting a message's
// formatting rather than some other failure. LLM output routinely contains
// unescaped Markdown/HTML (stray `_`, `*`, unclosed backticks, `<x>`), and
// Telegram's 400 for that case must never be treated as a generic retryable
// or non-retryable error — see isRetryable, which has no special case for it
// and would otherwise let the whole reply be silently dropped. Matches are
// deliberately narrow (specific description substrings, case-insensitive),
// never on "400" alone: other 400s exist (chat not found, message to reply
// not found, etc.) and must not be silently downgraded to plain text.
func isParseEntityError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())

	// Telegram prefixes entity failures with "can't parse entities:" (older
	// API versions used "can't parse message text:"), so the first two
	// patterns carry the weight. The rest are defensive against phrasings we
	// have not observed and may match nothing in practice.
	//
	// The asymmetry justifies erring wide: missing a real formatting error
	// loses the reply entirely, which is the bug this exists to prevent, while
	// a false positive costs one extra plain-text attempt that either succeeds
	// (user gets an unformatted reply instead of none) or fails and falls
	// through to the normal retry path. Matching bare "400" would be too wide
	// — that would swallow "chat not found" and similar real failures.
	patterns := []string{
		"can't parse entities",
		"can't parse message text",
		"unsupported start tag",
		"unclosed",
	}
	for _, p := range patterns {
		if strings.Contains(errStr, p) {
			return true
		}
	}
	return false
}

// isRetryable determines if an error should trigger a retry.
func isRetryable(err error) bool {
	errStr := strings.ToLower(err.Error())

	// Network errors
	if strings.Contains(errStr, "network") ||
		strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "connection") {
		return true
	}

	// Transient Telegram rate limiting — retrying after a backoff is correct.
	if strings.Contains(errStr, "too many requests") ||
		strings.Contains(errStr, "retry after") {
		return true
	}

	// Permanent failures. "bot was blocked" and "user is deactivated" are 403s
	// that will never succeed on retry — retrying them burns ~7.5s of backoff
	// per outbound part inside the single consumeOutbound goroutine, delaying
	// every other queued message behind a doomed send. A missing chat or reply
	// target is equally permanent.
	permanentPatterns := []string{
		"bot was blocked",
		"user is deactivated",
		"message to reply",
		"chat not found",
	}
	for _, pattern := range permanentPatterns {
		if strings.Contains(errStr, pattern) {
			return false
		}
	}

	// Default to retry for unknown errors, but say so: an error we do not
	// recognize being retried 3× is a signal that this classifier needs a new
	// case, not something to swallow silently.
	log.Debug("telegram: retrying unclassified send error", "error", errStr)
	return true
}

// buildReplyMarkup builds a ReplyMarkup from metadata.
func (t *TelegramChannel) buildReplyMarkup(markup map[string]any) *telebot.ReplyMarkup {
	rm := &telebot.ReplyMarkup{}

	// Handle inline keyboard
	if keyboard, ok := markup["inline_keyboard"].([][]map[string]any); ok {
		var rows [][]telebot.InlineButton

		for _, row := range keyboard {
			var buttonRow []telebot.InlineButton
			for _, button := range row {
				btn := telebot.InlineButton{}

				if text, ok := button["text"].(string); ok {
					btn.Text = text
				}

				// Handle different button types
				if url, ok := button["url"].(string); ok {
					btn.URL = url
				}
				if cbData, ok := button["callback_data"].(string); ok {
					btn.Data = cbData
				}

				buttonRow = append(buttonRow, btn)
			}
			rows = append(rows, buttonRow)
		}

		rm.InlineKeyboard = rows
	}

	// Handle keyboard (reply keyboard)
	if keyboard, ok := markup["keyboard"].([][]map[string]any); ok {
		var rows [][]telebot.ReplyButton

		for _, row := range keyboard {
			var buttonRow []telebot.ReplyButton
			for _, button := range row {
				btn := telebot.ReplyButton{}

				if text, ok := button["text"].(string); ok {
					btn.Text = text
				}
				if requestContact, ok := button["request_contact"].(bool); ok && requestContact {
					btn.Contact = true
				}
				if requestLocation, ok := button["request_location"].(bool); ok && requestLocation {
					btn.Location = true
				}

				buttonRow = append(buttonRow, btn)
			}
			rows = append(rows, buttonRow)
		}

		rm.ReplyKeyboard = rows
	}

	// Handle resize_keyboard
	if resize, ok := markup["resize_keyboard"].(bool); ok {
		rm.ResizeKeyboard = resize
	}

	// Handle one_time_keyboard
	if oneTime, ok := markup["one_time_keyboard"].(bool); ok {
		rm.OneTimeKeyboard = oneTime
	}

	// Handle selective
	if selective, ok := markup["selective"].(bool); ok {
		rm.Selective = selective
	}

	return rm
}

// consumeOutbound listens for outbound messages from the bus.
func (t *TelegramChannel) consumeOutbound(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("panic in telegram outbound consumer",
				"panic", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()),
			)
		}
	}()

	ch := t.bus.OutboundChannel()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.stopCh:
			return
		case msg := <-ch:
			log.Debug("consumeOutbound received message", "msg_channel", msg.Channel, "my_name", t.name, "channelID", msg.ChannelID)
			if msg.Channel == t.name || msg.Channel == "all" {
				if err := t.Send(msg); err != nil {
					log.Error("failed to send outbound message", "error", err, "channel", msg.Channel, "channelID", msg.ChannelID)
				} else {
					log.Info("Successfully sent message to Telegram", "channelID", msg.ChannelID)
				}
			}
		}
	}
}

// telegramAPIBaseURL is the Bot API endpoint ValidateToken talks to. It is a
// package var (not a const) so tests can point validation at a fake server.
var telegramAPIBaseURL = "https://api.telegram.org"

// telegramTokenTimeout bounds a single getMe attempt. The transport-level TLS
// handshake has its own ~10s timeout, so a hung connection cannot stall the
// onboard wizard forever; this covers the whole exchange.
const telegramTokenTimeout = 10 * time.Second

// telegramTokenAttempts is how many getMe attempts a transient connectivity
// failure (dial error, TLS handshake timeout, timeout, connection reset) gets
// before the token is reported invalid. A single TLS hiccup is a routine event
// on flaky networks and used to abort an otherwise working setup.
const telegramTokenAttempts = 3

// telegramTokenFormat is the documented shape of a Bot API token: a numeric bot
// id, a colon, and a base64url secret. Validated offline so an obviously
// malformed token fails instantly and without any network traffic.
var telegramTokenFormat = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]{30,}$`)

// ValidateToken validates a Telegram bot token by making a getMe API call.
// Returns nil if the token is valid, or an error describing the failure.
// Transient connectivity failures are retried up to telegramTokenAttempts
// times; a definite API rejection (400/401/404) is never retried.
func ValidateToken(token string) error {
	return validateTokenWith(token, telegramAPIBaseURL, &http.Client{Timeout: telegramTokenTimeout})
}

func validateTokenWith(token, baseURL string, client *http.Client) error {
	if err := validateTokenFormat(token); err != nil {
		return err
	}

	var lastErr error
	for attempt := 1; attempt <= telegramTokenAttempts; attempt++ {
		err := validateTokenAttempt(token, baseURL, client)
		if err == nil {
			return nil
		}
		lastErr = err
		if !errors.Is(err, ErrTelegramNetwork) || attempt == telegramTokenAttempts {
			break
		}
		time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
	}
	return lastErr
}

// validateTokenFormat rejects tokens that cannot possibly be valid before any
// network traffic leaves the machine.
func validateTokenFormat(token string) error {
	if token == "" {
		return fmt.Errorf("token is empty")
	}
	if !telegramTokenFormat.MatchString(token) {
		return fmt.Errorf("token is not a valid Telegram bot token (expected <numeric-id>:<secret>)")
	}
	return nil
}

// ErrTelegramNetwork is wrapped into errors from ValidateToken when the
// Telegram API could not be reached at all — a dial failure, TLS handshake
// timeout, or request timeout — as opposed to a definite rejection of the
// token. Only these failures are retried, and callers use IsNetworkError to
// tell "check your network" apart from "check your token".
var ErrTelegramNetwork = errors.New("telegram API unreachable")

// IsNetworkError reports whether err is a connectivity failure (dial error, TLS
// handshake timeout, timeout, reset) rather than a definite rejection of the
// token by the API.
func IsNetworkError(err error) bool {
	return errors.Is(err, ErrTelegramNetwork)
}

func validateTokenAttempt(token, baseURL string, client *http.Client) error {
	reqURL := fmt.Sprintf("%s/bot%s/getMe", baseURL, token)

	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("invalid token: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		// client.Do wraps a transport failure as a *url.Error whose string form
		// embeds the full request URL — including the token, which would land
		// verbatim in a terminal and in the setup output users paste into
		// issues. Unwrap to the cause so the credential never escapes.
		cause := err
		if urlErr, ok := err.(*url.Error); ok {
			cause = urlErr.Err
		}
		return fmt.Errorf("failed to connect to Telegram API: %w: %v", ErrTelegramNetwork, cause)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		// A reset or timeout after the headers arrived is still a
		// connectivity failure and must be retried, not reported as a
		// rejected token.
		return fmt.Errorf("failed to read response: %w: %v", ErrTelegramNetwork, err)
	}

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			ID       int64  `json:"id"`
			IsBot    bool   `json:"is_bot"`
			Username string `json:"username"`
		} `json:"result"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !result.OK {
		if result.Description != "" {
			return fmt.Errorf("token validation failed: %s", result.Description)
		}
		return fmt.Errorf("token validation failed: unknown error")
	}

	if !result.Result.IsBot {
		return fmt.Errorf("token does not belong to a bot")
	}

	return nil
}

// Ensure TelegramChannel implements Channel interface
var _ Channel = (*TelegramChannel)(nil)
