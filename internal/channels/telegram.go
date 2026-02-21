package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/log"
	"gopkg.in/telebot.v3"
)

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

	// Allowlist set for fast lookup
	allowSet map[string]struct{}

	// Retry configuration
	maxRetries    int
	retryDelay    time.Duration
	maxRetryDelay time.Duration

	// Polling configuration
	pollTimeout time.Duration
}

// NewTelegramChannel creates a new Telegram channel instance.
func NewTelegramChannel(bus *bus.MessageBus, cfg *config.TelegramConfig) *TelegramChannel {
	// Build allowlist set for fast lookup
	allowSet := make(map[string]struct{})
	for _, a := range cfg.AllowFrom {
		// Normalize: strip leading '@' and lowercase
		s := normalizeUsername(a)
		if s != "" {
			allowSet[s] = struct{}{}
		}
	}

	return &TelegramChannel{
		name:          "telegram",
		bus:           bus,
		cfg:           cfg,
		stopCh:        make(chan struct{}),
		allowSet:      allowSet,
		maxRetries:    3,
		retryDelay:    500 * time.Millisecond,
		maxRetryDelay: 5 * time.Second,
		pollTimeout:   60 * time.Second,
	}
}

// Name returns the channel identifier.
func (t *TelegramChannel) Name() string {
	return t.name
}

// normalizeUsername normalizes a username for allowlist comparison.
func normalizeUsername(username string) string {
	s := username
	if len(s) > 0 && s[0] == '@' {
		s = s[1:]
	}
	return strings.ToLower(s)
}

// IsAllowed checks if a user is in the allowlist.
func (t *TelegramChannel) IsAllowed(userID int64, username, firstName, lastName string) bool {
	// If allowlist is empty, allow everyone
	if len(t.allowSet) == 0 {
		return true
	}

	// Check by username
	if username != "" {
		if _, ok := t.allowSet[normalizeUsername(username)]; ok {
			return true
		}
	}

	// Check by first name + last name combination
	if firstName != "" {
		fullName := normalizeUsername(firstName)
		if lastName != "" {
			fullName += " " + normalizeUsername(lastName)
		}
		if _, ok := t.allowSet[fullName]; ok {
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

	return bot, nil
}

// runBot runs the bot's polling in a way that allows for reconnection.
func (t *TelegramChannel) runBot(ctx context.Context, bot *telebot.Bot) {
	// Start the bot - this blocks until stopped
	bot.Start()
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
func (t *TelegramChannel) handleMessage(ctx telebot.Context) error {
	msg := ctx.Message()

	// Check if it's a command - let specific handlers deal with it
	if strings.HasPrefix(msg.Text, "/") {
		// Commands are handled by their specific handlers
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
	go t.sendTyping(ctx.Sender())

	// Convert to InboundMessage and send to bus
	inbound := t.convertToInboundMessage(msg)
	if !t.bus.Send(inbound) {
		log.Error("failed to send message to bus", "sender", msg.Sender.Username)
		_, err := ctx.Bot().Send(ctx.Sender(), "Sorry, I couldn't process your message. Please try again.")
		return err
	}

	return nil
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
/help - Show this help message
/new - Start a new session

Just send me a message and I'll respond!`

	_, err := ctx.Bot().Send(ctx.Sender(), helpText, &telebot.SendOptions{
		ParseMode: telebot.ModeMarkdown,
	})
	return err
}

// handleNew handles the /new command to start a new session.
func (t *TelegramChannel) handleNew(ctx telebot.Context) error {
	msg := ctx.Message()

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

// handlePhoto processes incoming photos.
func (t *TelegramChannel) handlePhoto(ctx telebot.Context) error {
	msg := ctx.Message()

	if !t.IsAllowed(int64(msg.Sender.ID), msg.Sender.Username, msg.Sender.FirstName, msg.Sender.LastName) {
		return nil
	}

	// Show typing indicator
	go t.sendTyping(ctx.Sender())

	// Build content with photo info
	photo := msg.Photo
	content := "[Photo]"
	if photo.Caption != "" {
		content = fmt.Sprintf("[Photo with caption]: %s", photo.Caption)
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

	// Download photo in background
	go t.downloadFile(photo.File, "photo", msg.Chat.ID, msg.ID)

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

	go t.sendTyping(ctx.Sender())

	voice := msg.Voice
	content := "[Voice message]"
	if voice.Caption != "" {
		content = fmt.Sprintf("[Voice message with caption]: %s", voice.Caption)
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

	// Download voice in background
	go t.downloadFile(voice.File, "voice", msg.Chat.ID, msg.ID)

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

	go t.sendTyping(ctx.Sender())

	doc := msg.Document
	content := fmt.Sprintf("[Document: %s]", doc.FileName)
	if doc.Caption != "" {
		content = fmt.Sprintf("[Document: %s]\n%s", doc.FileName, doc.Caption)
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

	// Download document in background
	go t.downloadFile(doc.File, "document", msg.Chat.ID, msg.ID)

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

	go t.sendTyping(ctx.Sender())

	audio := msg.Audio
	content := fmt.Sprintf("[Audio: %s]", audio.Title)
	if audio.Caption != "" {
		content = fmt.Sprintf("[Audio: %s - %s]", audio.Title, audio.Caption)
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

	go t.sendTyping(ctx.Sender())

	video := msg.Video
	content := "[Video]"
	if video.Caption != "" {
		content = fmt.Sprintf("[Video with caption]: %s", video.Caption)
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

	// Stickers are just acknowledged, not sent to the agent
	content := "[Sticker]"

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
			"media_type":     "sticker",
			"file_id":        msg.Sticker.File.FileID,
			"file_unique_id": msg.Sticker.File.UniqueID,
			"emoji":          msg.Sticker.Emoji,
			"set_name":       msg.Sticker.SetName,
			"is_animated":    msg.Sticker.Animated,
			"is_video":       msg.Sticker.Video,
		},
	}

	if !t.bus.Send(inbound) {
		log.Error("failed to send sticker message to bus", "error", "queue full")
	}

	return nil
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

	content := fmt.Sprintf("[Edited]: %s", msg.Text)

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

// downloadFile downloads a file from Telegram and stores it locally.
func (t *TelegramChannel) downloadFile(file telebot.File, mediaType string, chatID int64, messageID int) {
	t.mu.RLock()
	bot := t.bot
	t.mu.RUnlock()

	if bot == nil {
		return
	}

	// Check if file is on cloud
	if !file.InCloud() {
		log.Debug("file not in cloud, skipping download", "media_type", mediaType, "file_id", file.FileID)
		return
	}

	// Create filename for local storage
	filename := fmt.Sprintf("%s_%d_%d_%s", mediaType, chatID, messageID, file.UniqueID)

	err := bot.Download(&file, filename)
	if err != nil {
		log.Error("failed to download file", "media_type", mediaType, "error", err, "file_id", file.FileID)
		return
	}

	log.Info("downloaded file", "media_type", mediaType, "filename", filename, "chat_id", chatID, "message_id", messageID)
}

// sendTyping sends a typing indicator to the user.
func (t *TelegramChannel) sendTyping(recipient telebot.Recipient) {
	t.mu.RLock()
	bot := t.bot
	t.mu.RUnlock()

	if bot == nil {
		return
	}

	_, err := bot.Send(recipient, telebot.Typing)
	if err != nil {
		log.Debug("failed to send typing indicator", "error", err)
	}
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

	// Stop the bot
	if t.bot != nil {
		t.bot.Close()
		t.bot = nil
	}

	log.Info("Telegram channel stopped")
	return nil
}

// Send delivers an outbound message to Telegram.
func (t *TelegramChannel) Send(msg bus.OutboundMessage) error {
	t.mu.RLock()
	bot := t.bot
	t.mu.RUnlock()

	if bot == nil {
		return fmt.Errorf("bot not initialized")
	}

	log.Debug("Send called", "channelID", msg.ChannelID, "metadata_chat_id", msg.Metadata["chat_id"])

	// Determine recipient - ChannelID is the chat ID
	var recipient telebot.Recipient

	// First try ChannelID
	if msg.ChannelID != "" {
		if chatID, err := strconv.ParseInt(msg.ChannelID, 10, 64); err == nil {
			recipient = telebot.ChatID(chatID)
			log.Debug("Using ChannelID as recipient", "chatID", chatID)
		}
	}

	// Fall back to metadata
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

	// Get parse mode from metadata
	parseMode := telebot.ModeDefault
	if pm, ok := msg.Metadata["parse_mode"].(string); ok {
		switch strings.ToLower(pm) {
		case "markdown", "md":
			parseMode = telebot.ModeMarkdown
		case "html":
			parseMode = telebot.ModeHTML
		}
	}

	// Prepare send options
	opts := &telebot.SendOptions{
		ParseMode: parseMode,
	}

	// Handle reply_to - pass the original message for reply
	if replyToMsg, ok := msg.Metadata["reply_to_message"].(**telebot.Message); ok && replyToMsg != nil {
		opts.ReplyTo = *replyToMsg
	}

	// Handle reply markup (inline buttons)
	if markup, ok := msg.Metadata["reply_markup"].(map[string]any); ok {
		opts.ReplyMarkup = t.buildReplyMarkup(markup)
	}

	// Retry logic for sending
	var lastErr error
	delay := t.retryDelay

	for attempt := 0; attempt < t.maxRetries; attempt++ {
		_, err := bot.Send(recipient, msg.Content, opts)
		if err == nil {
			return nil
		}

		lastErr = err
		log.Warn("failed to send message, retrying",
			"attempt", attempt+1,
			"max_retries", t.maxRetries,
			"error", err,
			"recipient", msg.ChannelID,
		)

		// Check if error is retryable
		if !isRetryable(err) {
			break
		}

		// Wait before retry with exponential backoff
		select {
		case <-time.After(delay):
			// Exponential backoff
			delay = time.Duration(math.Min(float64(delay*2), float64(t.maxRetryDelay)))
		case <-t.stopCh:
			return fmt.Errorf("stopped while retrying: %w", lastErr)
		}
	}

	return fmt.Errorf("failed to send message after %d retries: %w", t.maxRetries, lastErr)
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

	// Telegram-specific errors
	retryablePatterns := []string{
		"too many requests",
		"retry after",
		"bot was blocked",
		"user is deactivated",
	}

	for _, pattern := range retryablePatterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}

	// Message to chat not found - not retryable
	if strings.Contains(errStr, "message to reply") {
		return false
	}

	// Chat not found - not retryable
	if strings.Contains(errStr, "chat not found") {
		return false
	}

	// Default to retry for unknown errors
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

// runWithReconnect handles bot polling with automatic reconnection.
func (t *TelegramChannel) runWithReconnect(ctx context.Context) {
	delay := time.Second

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.stopCh:
			return
		default:
			t.mu.RLock()
			bot := t.bot
			t.mu.RUnlock()

			if bot == nil {
				return
			}

			// Wait a bit before checking again
			time.Sleep(delay)

			// Exponential backoff for reconnection
			delay = time.Duration(math.Min(float64(delay*2), float64(30*time.Second)))
		}
	}
}

// SendPhoto sends a photo to a recipient.
func (t *TelegramChannel) SendPhoto(recipient telebot.Recipient, photo *telebot.Photo, caption string, opts *telebot.SendOptions) error {
	t.mu.RLock()
	bot := t.bot
	t.mu.RUnlock()

	if bot == nil {
		return fmt.Errorf("bot not initialized")
	}

	if opts == nil {
		opts = &telebot.SendOptions{}
	}

	_, err := bot.Send(recipient, photo, opts)
	return err
}

// SendDocument sends a document to a recipient.
func (t *TelegramChannel) SendDocument(recipient telebot.Recipient, doc *telebot.Document, caption string, opts *telebot.SendOptions) error {
	t.mu.RLock()
	bot := t.bot
	t.mu.RUnlock()

	if bot == nil {
		return fmt.Errorf("bot not initialized")
	}

	if opts == nil {
		opts = &telebot.SendOptions{}
	}

	_, err := bot.Send(recipient, doc, opts)
	return err
}

// EditMessage edits an existing message.
func (t *TelegramChannel) EditMessage(recipient telebot.Recipient, messageID int, text string, opts *telebot.SendOptions) error {
	t.mu.RLock()
	bot := t.bot
	t.mu.RUnlock()

	if bot == nil {
		return fmt.Errorf("bot not initialized")
	}

	// Create an Editable wrapper for the message
	editable := &editableMessage{
		messageID: messageID,
		chatID:    0, // Will be determined from context
	}

	_, err := bot.Edit(editable, text, opts)
	return err
}

// editableMessage implements telebot.Editable for editing messages.
type editableMessage struct {
	messageID int
	chatID    int64
}

func (e *editableMessage) MessageSig() (string, int64) {
	return strconv.Itoa(e.messageID), e.chatID
}

// AnswerCallback answers a callback query.
// Note: This requires the original callback object. For proper implementation,
// use the handleCallback method directly which has access to the callback.
func (t *TelegramChannel) AnswerCallback(callbackID string, text string, showAlert bool) error {
	// This method is deprecated as it requires the full callback object.
	// Use handleCallback directly which properly responds to callbacks.
	return fmt.Errorf("AnswerCallback requires the original callback object - use handleCallback instead")
}

// ParseMarkdown parses markdown text and returns HTML for Telegram.
func (t *TelegramChannel) ParseMarkdown(text string) (string, error) {
	// Simple markdown to Telegram HTML conversion
	// Bold: **text** or *text* -> <b>text</b>
	boldRegex := regexp.MustCompile(`\*\*(.+?)\*\*|\*(.+?)\*`)
	text = boldRegex.ReplaceAllString(text, "<b>$1$2</b>")

	// Italic: __text__ or _text_ -> <i>text</i>
	italicRegex := regexp.MustCompile(`__(.+?)__|_(.+?)_`)
	text = italicRegex.ReplaceAllString(text, "<i>$1$2</i>")

	// Code: `code` -> <code>code</code>
	codeRegex := regexp.MustCompile("`(.+?)`")
	text = codeRegex.ReplaceAllString(text, "<code>$1</code>")

	// Pre: ```code``` -> <pre>code</pre>
	preRegex := regexp.MustCompile("```(.+?)```")
	text = preRegex.ReplaceAllString(text, "<pre>$1</pre>")

	// Links: [text](url) -> <a href="url">text</a>
	linkRegex := regexp.MustCompile(`\[(.+?)\]\((.+?)\)`)
	text = linkRegex.ReplaceAllString(text, `<a href="$2">$1</a>`)

	return text, nil
}

// ValidateToken validates a Telegram bot token by making a getMe API call.
// Returns nil if the token is valid, or an error describing the failure.
func ValidateToken(token string) error {
	if token == "" {
		return fmt.Errorf("token is empty")
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/getMe", token)

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to connect to Telegram API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
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
