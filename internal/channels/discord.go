package channels

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/commands"
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

var discordCommands = discordCommandList()

// discordCommandList renders the Discord command list from the shared table
// (internal/commands): every command the agent answers is forwarded, so a
// Discord user is never told a command the CLI and Telegram accept is
// unknown.
func discordCommandList() []discordCommand {
	out := make([]discordCommand, 0, len(commands.All))
	for _, c := range commands.All {
		out = append(out, discordCommand{Name: c.Name, Description: c.Description})
	}
	return out
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
	// stopCh belongs to one Start/Stop cycle, not to the channel: Stop closes
	// it and Start allocates a fresh one, so a restarted channel is not left
	// with a permanently-closed stop signal (which made consumeOutbound return
	// at once and every Send abort its retries). Read it via stopChan.
	stopCh     chan struct{}
	stopClosed bool

	// outboundWG tracks the consumeOutbound goroutine so Stop can join it.
	// Without the join, Stop returning does not mean stopped: a consumer parked
	// in Send's retry loop has already taken a message off the shared bus
	// channel, and a subsequent Start briefly runs two competing consumers.
	outboundWG sync.WaitGroup

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

// stopChan returns the current run's stop signal. Start replaces the field, so
// every reader outside the mutex must go through this rather than touching
// d.stopCh directly.
func (d *DiscordChannel) stopChan() chan struct{} {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.stopCh
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

// beginRun claims the channel for a new Start/Stop cycle. It refuses a second
// concurrent run and, crucially, allocates a fresh stop signal: Stop closes
// stopCh, so reusing the previous cycle's channel would make the new run's
// consumeOutbound return immediately and every Send abort its retries — the
// channel would look started and silently deliver nothing. Split out of Start
// so the lifecycle is reachable from a test without a live gateway.
func (d *DiscordChannel) beginRun() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running {
		return fmt.Errorf("Discord channel is already running")
	}
	d.running = true
	// Fresh stop signal for this run; the previous cycle's channel is closed.
	d.stopCh = make(chan struct{})
	d.stopClosed = false
	return nil
}

// abortRun releases a run claimed by beginRun that never got as far as starting
// its goroutines. It closes the stop signal beginRun allocated: leaving a fresh
// never-closed channel behind on a validation failure would strand anything
// that had already read it via stopChan, and the next beginRun replaces it
// anyway, so an unclosed one is pure garbage.
func (d *DiscordChannel) abortRun() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.running = false
	if !d.stopClosed && d.stopCh != nil {
		d.stopClosed = true
		close(d.stopCh)
	}
}

// Start opens the Discord gateway connection and begins consuming messages.
func (d *DiscordChannel) Start(ctx context.Context) error {
	if err := d.beginRun(); err != nil {
		return err
	}

	if d.cfg.Token == "" {
		d.abortRun()
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
		d.abortRun()
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
		d.abortRun()
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

	d.outboundWG.Add(1)
	go func() {
		defer d.outboundWG.Done()
		d.consumeOutbound(ctx)
	}()

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

// handleCommand forwards a recognised "/name" command to the agent, which
// owns every command's behaviour (the CLI and Telegram share the handlers).
// No local acknowledgement: the agent's reply is the acknowledgement, and a
// local "Starting new session..." made /new answer twice.
func (d *DiscordChannel) handleCommand(userID, username, channelID, content string) {
	if !isKnownDiscordCommand(content) {
		return
	}
	inbound := d.inboundMessage(userID, username, channelID, content, true)
	if !d.bus.Send(inbound) {
		log.Error("failed to send command to discord bus", "command", commandName(content))
		d.reply(channelID, "Sorry, I couldn't process that. Please try again.")
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
	return commands.UnknownText(commandName(text))
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

	stopCh := d.stopChan()

	// Discord has no native attachment path here yet (the session interface is
	// text-only). Degrade honestly by naming the files in the message rather
	// than dropping them: an agent that reports "sent you the chart" while
	// nothing arrives is worse than one that tells the user where it is.
	msg.Content = describeUnsentAttachments(msg.Content, msg.Attachments)

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
			if !isDiscordRetryable(err) {
				break
			}
			select {
			case <-time.After(delay):
				delay = time.Duration(math.Min(float64(delay*2), float64(d.maxRetryDelay)))
			case <-stopCh:
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
			case <-stopCh:
				return nil
			}
		}
	}

	return nil
}

// discordPermanentCodes are discordgo REST error codes that will never succeed
// on retry. Retrying one burns the full backoff (~7.5s per part) inside the
// single consumeOutbound goroutine, delaying every message queued behind it.
var discordPermanentCodes = map[int]struct{}{
	10003: {}, // Unknown Channel
	10013: {}, // Unknown User
	50001: {}, // Missing Access
	50007: {}, // Cannot send messages to this user
	50013: {}, // Missing Permissions
}

// isDiscordRetryable reports whether an outbound send failure is worth
// retrying. Discord has its own error vocabulary, so it does not share
// Telegram's classifier: the Telegram permanent list is string matching on
// Bot API descriptions that never appear here, and its unclassified-error log
// line names the wrong channel.
func isDiscordRetryable(err error) bool {
	if err == nil {
		return false
	}

	var restErr *discordgo.RESTError
	if errors.As(err, &restErr) {
		if restErr.Message != nil {
			if _, permanent := discordPermanentCodes[restErr.Message.Code]; permanent {
				return false
			}
		}
		// 429 is Discord's rate limit: back off and retry. Any other 4xx is a
		// request the server has already rejected on its merits.
		if restErr.Response != nil {
			status := restErr.Response.StatusCode
			if status == http.StatusTooManyRequests {
				return true
			}
			if status >= 400 && status < 500 {
				return false
			}
		}
	}

	errStr := strings.ToLower(err.Error())
	if strings.Contains(errStr, "network") ||
		strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "connection") ||
		strings.Contains(errStr, "eof") {
		return true
	}
	if strings.Contains(errStr, "rate limit") || strings.Contains(errStr, "too many requests") {
		return true
	}

	// Default to retry for unknown errors, but say so: an unrecognised error
	// retried 3× is a signal this classifier needs a new case.
	log.Debug("discord: retrying unclassified send error", "error", errStr)
	return true
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
// bus.OutboundChannel() (really RegisterOutboundConsumer under the hood)
// returns a private channel that gets a copy of every outbound message —
// Discord and Telegram running together no longer compete for the same
// underlying channel, so this filter is what decides whether a message is
// this channel's, not luck in a race.
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
	stopCh := d.stopChan()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stopCh:
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
	// Guarded rather than a bare close: running is the primary latch, but a
	// double close panics the process, so never rely on a single flag for it.
	if !d.stopClosed && d.stopCh != nil {
		d.stopClosed = true
		close(d.stopCh)
	}

	// The keep-alive goroutines also select on stopCh, so closing it stops
	// them; dropping the entries just releases the map.
	d.typingStop = make(map[string]chan struct{})

	remove := d.removeHnd
	session := d.session
	d.removeHnd = nil
	d.session = nil
	d.mu.Unlock()

	// Join the outbound consumer before returning. Closing stopCh only asks it
	// to stop; if it is inside Send's retry loop it has already claimed a
	// message from the shared bus channel, and returning here would let a
	// subsequent Start run a second consumer alongside it. Waited outside mu:
	// Send and the typing helpers take the same mutex.
	d.outboundWG.Wait()

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

// describeUnsentAttachments appends a note naming each attachment a channel
// could not deliver natively. It returns content unchanged when there are
// none, so the ordinary text path is byte-for-byte unaffected.
func describeUnsentAttachments(content string, attachments []bus.Attachment) string {
	if len(attachments) == 0 {
		return content
	}
	var b strings.Builder
	b.WriteString(content)
	if content != "" {
		b.WriteString("\n")
	}
	b.WriteString("\n[attachment not supported on this channel]")
	for _, att := range attachments {
		fmt.Fprintf(&b, "\n%s (%s) — %s", att.Filename, humanBytes(att.Size), att.SourcePath)
	}
	return b.String()
}
