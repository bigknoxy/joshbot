package channels

import (
	"context"
	"fmt"
	"math"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
	"github.com/bigknoxy/joshbot/internal/log"
)

// DiscordMaxMessageLen is the maximum message length allowed by the Discord API
// for a single message. Longer replies are split by splitMessage, the same
// code-fence-aware splitter Telegram uses.
const DiscordMaxMessageLen = 2000

// discordCommand describes a text command joshbot answers on Discord. Discord's
// native slash commands require gateway registration and per-guild scoping;
// joshbot instead recognises these as plain "/name" text in a DM or channel,
// which keeps a single source of truth (this slice) for both the /help listing
// and the unknown-command fallback — mirroring Telegram's botCommands.
type discordCommand struct {
	Name        string
	Description string
}

var discordCommands = []discordCommand{
	{Name: "help", Description: "Show the list of commands"},
	{Name: "new", Description: "Start a new session"},
}

// discordSession is the subset of *discordgo.Session the channel needs, so the
// send/typing paths can be exercised in tests without a live gateway
// connection. In production it is a real *discordgo.Session.
type discordSession interface {
	ChannelMessageSend(channelID, content string, options ...discordgo.RequestOption) (*discordgo.Message, error)
	ChannelTyping(channelID string, options ...discordgo.RequestOption) error
}

// DiscordChannel implements the Channel interface for Discord.
type DiscordChannel struct {
	name    string
	bus     *bus.MessageBus
	cfg     *config.DiscordConfig
	mu      sync.RWMutex
	running bool
	stopCh  chan struct{}

	session   *discordgo.Session
	removeHnd func()

	// allowIDs and allowNames are the deny-by-default allowlist, partitioned by
	// entry shape. Both empty rejects everyone.
	allowIDs   map[string]struct{}
	allowNames map[string]struct{}

	// selfID is this bot's own user ID, used to ignore its own messages.
	selfID string

	// send overrides the session for outbound sends and typing actions. Only
	// tests set it; in production it stays nil and the session is used.
	send discordSession

	// Retry configuration for outbound sends.
	maxRetries    int
	retryDelay    time.Duration
	maxRetryDelay time.Duration

	// Typing keep-alive. Discord clears a typing indicator after ~10 seconds,
	// so a long agent turn needs it re-sent on a timer. One entry per channel,
	// guarded by mu; the channel in it stops that channel's keep-alive.
	typingStop        map[string]chan struct{}
	typingInterval    time.Duration
	typingMaxDuration time.Duration
}

// NewDiscordChannel creates a new Discord channel instance.
func NewDiscordChannel(b *bus.MessageBus, cfg *config.DiscordConfig) *DiscordChannel {
	allowIDs := make(map[string]struct{})
	allowNames := make(map[string]struct{})
	for _, a := range cfg.AllowFrom {
		s := normalizeUsername(a)
		if s == "" {
			continue
		}
		// An all-digits entry is a snowflake and may only ever match the user
		// ID — see IsAllowed for why matching it against a name is an auth
		// bypass.
		if isSnowflake(s) {
			allowIDs[s] = struct{}{}
		} else {
			allowNames[s] = struct{}{}
		}
	}

	return &DiscordChannel{
		name:          "discord",
		bus:           b,
		cfg:           cfg,
		stopCh:        make(chan struct{}),
		allowIDs:      allowIDs,
		allowNames:    allowNames,
		maxRetries:    3,
		retryDelay:    500 * time.Millisecond,
		maxRetryDelay: 5 * time.Second,
		typingStop:    make(map[string]chan struct{}),
		// Discord clears a typing indicator after ~10s; re-send just under that.
		typingInterval: 8 * time.Second,
		// Longer than any sane agent turn, short enough to not leak forever.
		typingMaxDuration: 10 * time.Minute,
	}
}

// Name returns the channel identifier.
func (d *DiscordChannel) Name() string {
	return d.name
}

// IsAllowed reports whether a sender may use the bot. An empty allowlist denies
// everyone: this bot hands whoever it talks to a direct line into an agent loop
// holding the shell tool, so an unset allowlist must fail closed, not open —
// see the startup warning in Start that names the exact config key to set.
//
// Entries may be a numeric Discord user ID (the stable identifier, and what the
// docs recommend) or a username / global display name. A user can change their
// username at any time, so ID is the reliable form.
//
// The allowlist is partitioned by entry shape and each half is matched against
// only the field it can legitimately name. Matching every entry against every
// field let a stranger set their free-form global display name to the
// operator's snowflake and authenticate as them — global names are not unique
// and are not validated, so an ID-shaped allowlist entry must never be
// satisfied by a name.
func (d *DiscordChannel) IsAllowed(userID, username, globalName string) bool {
	if len(d.allowIDs) == 0 && len(d.allowNames) == 0 {
		return false
	}
	if userID != "" {
		if _, ok := d.allowIDs[normalizeUsername(userID)]; ok {
			return true
		}
	}
	for _, name := range []string{username, globalName} {
		if name == "" {
			continue
		}
		if _, ok := d.allowNames[normalizeUsername(name)]; ok {
			return true
		}
	}
	return false
}

// isSnowflake reports whether an allowlist entry is a Discord user ID. Discord
// snowflakes are decimal digits only, and usernames may not be all digits, so
// the two shapes never overlap.
func isSnowflake(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// Start opens the Discord gateway connection and begins consuming messages.
func (d *DiscordChannel) Start(ctx context.Context) error {
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return fmt.Errorf("Discord channel is already running")
	}
	d.running = true
	d.mu.Unlock()

	if d.cfg.Token == "" {
		d.mu.Lock()
		d.running = false
		d.mu.Unlock()
		return fmt.Errorf("Discord token is not configured")
	}

	// Fail closed on an unset allowlist, but loudly: the operator has a running
	// bot that rejects every message until they name who may use it. Say
	// exactly what to set so this is actionable, not a silent lockout.
	if len(d.allowIDs) == 0 && len(d.allowNames) == 0 {
		log.Warn("Discord allowlist is empty — every sender will be rejected. " +
			"Set channels.discord.allow_from in ~/.joshbot/config.json (or " +
			"JOSHBOT_CHANNELS__DISCORD__ALLOW_FROM) to your numeric Discord " +
			"user ID before this bot can be used.")
	}

	// Bot tokens must be presented as "Bot <token>"; discordgo does this
	// automatically when the token lacks the prefix, but be explicit and
	// tolerate a token an operator already prefixed.
	token := d.cfg.Token
	if !strings.HasPrefix(token, "Bot ") {
		token = "Bot " + token
	}

	session, err := discordgo.New(token)
	if err != nil {
		d.mu.Lock()
		d.running = false
		d.mu.Unlock()
		return fmt.Errorf("failed to create Discord session: %w", err)
	}

	// Only the intents joshbot needs: direct messages, guild messages, and the
	// privileged MessageContent intent (required to read message text — the
	// operator must enable it in the Discord developer portal).
	session.Identify.Intents = discordgo.IntentsDirectMessages |
		discordgo.IntentsGuildMessages |
		discordgo.IntentsMessageContent

	remove := session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		d.handleMessageCreate(s, m)
	})

	if err := session.Open(); err != nil {
		remove()
		d.mu.Lock()
		d.running = false
		d.mu.Unlock()
		return fmt.Errorf("failed to open Discord gateway: %w", err)
	}

	selfID := ""
	if session.State != nil && session.State.User != nil {
		selfID = session.State.User.ID
	}

	d.mu.Lock()
	d.session = session
	d.removeHnd = remove
	d.selfID = selfID
	d.mu.Unlock()

	go d.consumeOutbound(ctx)

	log.Info("Discord channel started")
	return nil
}

// handleMessageCreate processes an inbound Discord message.
func (d *DiscordChannel) handleMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("panic in discord message handler",
				"panic", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()),
			)
		}
	}()

	if m.Author == nil {
		return
	}

	// Ignore our own messages and other bots so the bot never talks to itself.
	d.mu.RLock()
	selfID := d.selfID
	d.mu.RUnlock()
	if m.Author.Bot || (selfID != "" && m.Author.ID == selfID) {
		return
	}

	d.dispatch(m.Author.ID, m.Author.Username, m.Author.GlobalName, m.ChannelID, m.Content)
}

// dispatch applies the allowlist and command handling, then forwards a message
// to the bus. It is split out from handleMessageCreate so the decision logic
// can be tested without a discordgo message value.
func (d *DiscordChannel) dispatch(userID, username, globalName, channelID, content string) {
	allowed := d.IsAllowed(userID, username, globalName)

	if strings.HasPrefix(content, "/") {
		if !allowed {
			// Never leak the command list to someone outside the allowlist.
			return
		}
		if isKnownDiscordCommand(content) {
			d.handleCommand(userID, username, channelID, content)
			return
		}
		d.reply(channelID, unknownDiscordCommandText(content))
		return
	}

	if !allowed {
		d.reply(channelID, "⛔ You are not authorized to use this bot.")
		return
	}

	d.startTyping(channelID)

	inbound := d.inboundMessage(userID, username, channelID, content, false)
	if !d.bus.Send(inbound) {
		log.Error("failed to send discord message to bus", "sender", userID)
		d.reply(channelID, "Sorry, I couldn't process your message. Please try again.")
	}
}

// handleCommand handles a recognised "/name" command.
func (d *DiscordChannel) handleCommand(userID, username, channelID, content string) {
	switch commandName(content) {
	case "help":
		d.reply(channelID, discordHelpText())
	case "new":
		inbound := d.inboundMessage(userID, username, channelID, "/new", true)
		if !d.bus.Send(inbound) {
			log.Error("failed to send /new command to discord bus")
			d.reply(channelID, "Sorry, couldn't start a new session. Please try again.")
			return
		}
		d.reply(channelID, "🔄 Starting new session...")
	}
}

// inboundMessage builds a bus.InboundMessage for a Discord message. chat_id
// carries the Discord channel ID so the outbound path (getChannelID / Send) can
// route the reply back.
func (d *DiscordChannel) inboundMessage(userID, username, channelID, content string, isCommand bool) bus.InboundMessage {
	return bus.InboundMessage{
		SenderID:  fmt.Sprintf("discord_%s", userID),
		Content:   content,
		Channel:   d.name,
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"chat_id":    channelID,
			"channel_id": channelID,
			"username":   username,
			"user_id":    userID,
			"is_command": isCommand,
		},
	}
}

// isKnownDiscordCommand reports whether the text names a command with a handler.
func isKnownDiscordCommand(text string) bool {
	name := commandName(text)
	for _, c := range discordCommands {
		if c.Name == name {
			return true
		}
	}
	return false
}

// unknownDiscordCommandText tells the user their command does not exist and
// lists the ones that do.
func unknownDiscordCommandText(text string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Unknown command: /%s\n\nAvailable commands:", commandName(text))
	for _, c := range discordCommands {
		fmt.Fprintf(&b, "\n/%s - %s", c.Name, c.Description)
	}
	b.WriteString("\n\nOr just send me a message.")
	return b.String()
}

// discordHelpText renders the /help response from discordCommands.
func discordHelpText() string {
	var b strings.Builder
	b.WriteString("🤖 **JoshBot**\n\nWelcome! I'm here to help you.\n\nAvailable commands:")
	for _, c := range discordCommands {
		fmt.Fprintf(&b, "\n/%s - %s", c.Name, c.Description)
	}
	b.WriteString("\n\nJust send me a message and I'll respond!")
	return b.String()
}

// reply sends a short control message straight to a channel, splitting and
// retrying via Send. Errors are logged, not returned: these are best-effort
// acknowledgements.
func (d *DiscordChannel) reply(channelID, content string) {
	msg := bus.OutboundMessage{
		Content:   content,
		Channel:   d.name,
		ChannelID: channelID,
		Timestamp: time.Now(),
	}
	if err := d.Send(msg); err != nil {
		log.Error("failed to send discord reply", "error", err, "channel_id", channelID)
	}
}

// sender returns the discordSession to use for sends and typing. Callers must
// not hold mu.
func (d *DiscordChannel) sender() discordSession {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.send != nil {
		return d.send
	}
	if d.session != nil {
		return d.session
	}
	return nil
}

// Send delivers an outbound message to Discord, splitting content over
// DiscordMaxMessageLen into multiple messages and retrying transient failures.
//
// Discord renders Markdown leniently and does not reject malformed formatting
// the way Telegram's parse modes do, so there is no parse-entity fallback here —
// the equivalent hazard simply does not exist on this platform.
func (d *DiscordChannel) Send(msg bus.OutboundMessage) error {
	s := d.sender()
	if s == nil {
		return fmt.Errorf("discord session not initialized")
	}

	channelID := msg.ChannelID
	if channelID == "" {
		if cid, ok := msg.Metadata["chat_id"].(string); ok {
			channelID = cid
		}
	}
	if channelID == "" {
		return fmt.Errorf("no valid recipient specified")
	}

	// The reply is on its way, so the "typing…" keep-alive for this channel has
	// done its job.
	d.stopTyping(channelID)

	// Parts after the first carry a "— Part N of M —" header. Re-split with
	// that reserved so header+part still fits.
	const partHeaderOverhead = 35
	parts := splitMessage(msg.Content, DiscordMaxMessageLen)
	if len(parts) > 1 {
		parts = splitMessage(msg.Content, DiscordMaxMessageLen-partHeaderOverhead)
	}

	for i, part := range parts {
		content := part
		if i > 0 {
			content = fmt.Sprintf("\n\n— **Part %d of %d** —\n\n", i+1, len(parts)) + part
		}

		var lastErr error
		delay := d.retryDelay
		for attempt := 0; attempt < d.maxRetries; attempt++ {
			_, err := s.ChannelMessageSend(channelID, content)
			if err == nil {
				lastErr = nil
				break
			}
			lastErr = err
			log.Warn("failed to send discord message part, retrying",
				"attempt", attempt+1, "max_retries", d.maxRetries,
				"part", i+1, "total_parts", len(parts),
				"error", err, "channel_id", channelID,
			)
			if !isRetryable(err) {
				break
			}
			select {
			case <-time.After(delay):
				delay = time.Duration(math.Min(float64(delay*2), float64(d.maxRetryDelay)))
			case <-d.stopCh:
				return fmt.Errorf("stopped while retrying: %w", lastErr)
			}
		}
		if lastErr != nil {
			return fmt.Errorf("failed to send discord message part %d/%d after %d retries: %w",
				i+1, len(parts), d.maxRetries, lastErr)
		}

		if i < len(parts)-1 {
			select {
			case <-time.After(250 * time.Millisecond):
			case <-d.stopCh:
				return nil
			}
		}
	}

	return nil
}

// startTyping shows "typing…" for a channel and keeps it alive until stopTyping
// is called, the channel shuts down, or the max duration elapses. Discord
// clears a typing indicator after ~10 seconds, but an agent turn routinely runs
// far longer. Calling it again for a channel that is already typing is a no-op.
func (d *DiscordChannel) startTyping(channelID string) {
	if channelID == "" {
		return
	}

	d.mu.Lock()
	s := d.senderLocked()
	if s == nil {
		d.mu.Unlock()
		return
	}
	if _, ok := d.typingStop[channelID]; ok {
		d.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	d.typingStop[channelID] = stop
	interval := d.typingInterval
	maxDuration := d.typingMaxDuration
	shutdown := d.stopCh
	d.mu.Unlock()

	go func() {
		notifyDiscordTyping(s, channelID)

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
				d.clearTyping(channelID, stop)
				return
			case <-ticker.C:
				notifyDiscordTyping(s, channelID)
			}
		}
	}()
}

// stopTyping ends the keep-alive for a channel. Stopping a channel that is not
// typing is a no-op.
func (d *DiscordChannel) stopTyping(channelID string) {
	if channelID == "" {
		return
	}
	d.mu.Lock()
	stop, ok := d.typingStop[channelID]
	if ok {
		delete(d.typingStop, channelID)
	}
	d.mu.Unlock()
	if ok {
		close(stop)
	}
}

// clearTyping removes a channel's keep-alive entry, but only if it is still the
// one this goroutine owns — a newer keep-alive for the same channel must not be
// evicted by an older one expiring.
func (d *DiscordChannel) clearTyping(channelID string, own chan struct{}) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if cur, ok := d.typingStop[channelID]; ok && cur == own {
		delete(d.typingStop, channelID)
	}
}

// senderLocked returns the send target; callers must hold mu.
func (d *DiscordChannel) senderLocked() discordSession {
	if d.send != nil {
		return d.send
	}
	if d.session != nil {
		return d.session
	}
	return nil
}

func notifyDiscordTyping(s discordSession, channelID string) {
	if err := s.ChannelTyping(channelID); err != nil {
		log.Debug("failed to send discord typing indicator", "error", err)
	}
}

// consumeOutbound listens for outbound messages addressed to this channel.
//
// NOTE: the bus exposes a single outbound channel that consumers read
// competitively (see internal/bus). Running Discord and Telegram at once would
// have them steal each other's outbound messages; the fan-out fix belongs in
// the bus and is tracked as follow-up work. With one gateway channel enabled at
// a time (the common case) this routes correctly.
func (d *DiscordChannel) consumeOutbound(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("panic in discord outbound consumer",
				"panic", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()),
			)
		}
	}()

	ch := d.bus.OutboundChannel()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopCh:
			return
		case msg := <-ch:
			if msg.Channel == d.name || msg.Channel == "all" {
				if err := d.Send(msg); err != nil {
					log.Error("failed to send discord outbound message", "error", err, "channel_id", msg.ChannelID)
				} else {
					log.Info("Successfully sent message to Discord", "channel_id", msg.ChannelID)
				}
			}
		}
	}
}

// Stop gracefully shuts down the Discord channel.
func (d *DiscordChannel) Stop() error {
	d.mu.Lock()
	if !d.running {
		d.mu.Unlock()
		return nil
	}
	d.running = false
	close(d.stopCh)

	// The keep-alive goroutines also select on stopCh, so closing it stops
	// them; dropping the entries just releases the map.
	d.typingStop = make(map[string]chan struct{})

	remove := d.removeHnd
	session := d.session
	d.removeHnd = nil
	d.session = nil
	d.mu.Unlock()

	if remove != nil {
		remove()
	}
	if session != nil {
		if err := session.Close(); err != nil {
			log.Warn("failed to close Discord session", "error", err)
		}
	}

	log.Info("Discord channel stopped")
	return nil
}

// Ensure DiscordChannel implements Channel interface.
var _ Channel = (*DiscordChannel)(nil)
