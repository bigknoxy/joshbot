package channels

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	telebot "gopkg.in/telebot.v3"
)

// editorCall is one Send or Edit the streamer made, recorded with the chat it
// targeted so a test can prove two chats never saw each other's text.
type editorCall struct {
	chat    string
	text    string
	mode    telebot.ParseMode
	edit    bool
	deleted bool
	replyTo int // 0 = unthreaded
	markup  *telebot.ReplyMarkup
}

// fakeEditor stands in for *telebot.Bot. Errors are queued per operation and
// popped in order, which is how the flood, parse-entity and dead-send cases are
// driven without a live API.
type fakeEditor struct {
	mu       sync.Mutex
	calls    []editorCall
	sendErrs []error
	editErrs []error
	nextID   int
}

func (f *fakeEditor) pop(errs *[]error) error {
	if len(*errs) == 0 {
		return nil
	}
	err := (*errs)[0]
	*errs = (*errs)[1:]
	return err
}

func (f *fakeEditor) Send(to telebot.Recipient, what interface{}, opts ...interface{}) (*telebot.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, editorCall{chat: to.Recipient(), text: fmt.Sprint(what), mode: modeOf(opts), replyTo: replyToOf(opts), markup: markupOf(opts)})
	if err := f.pop(&f.sendErrs); err != nil {
		return nil, err
	}
	f.nextID++
	var chatID int64
	fmt.Sscanf(to.Recipient(), "%d", &chatID)
	return &telebot.Message{ID: f.nextID, Chat: &telebot.Chat{ID: chatID}}, nil
}

func (f *fakeEditor) Edit(msg telebot.Editable, what interface{}, opts ...interface{}) (*telebot.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, chatID := msg.MessageSig()
	f.calls = append(f.calls, editorCall{
		chat: fmt.Sprint(chatID), text: fmt.Sprint(what), mode: modeOf(opts), edit: true, markup: markupOf(opts),
	})
	if err := f.pop(&f.editErrs); err != nil {
		return nil, err
	}
	return &telebot.Message{}, nil
}

func (f *fakeEditor) Delete(msg telebot.Editable) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, editorCall{text: "<deleted>", deleted: true})
	return nil
}

func (f *fakeEditor) snapshot() []editorCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]editorCall(nil), f.calls...)
}

func (f *fakeEditor) lastFor(chat string) string {
	last := ""
	for _, c := range f.snapshot() {
		if c.chat == chat {
			last = c.text
		}
	}
	return last
}

func replyToOf(opts []interface{}) int {
	for _, o := range opts {
		if so, ok := o.(*telebot.SendOptions); ok && so.ReplyTo != nil {
			return so.ReplyTo.ID
		}
	}
	return 0
}

func markupOf(opts []interface{}) *telebot.ReplyMarkup {
	for _, o := range opts {
		if so, ok := o.(*telebot.SendOptions); ok {
			return so.ReplyMarkup
		}
	}
	return nil
}

func modeOf(opts []interface{}) telebot.ParseMode {
	for _, o := range opts {
		if so, ok := o.(*telebot.SendOptions); ok {
			return so.ParseMode
		}
	}
	return telebot.ModeDefault
}

// streamTestChannel wires a channel to a fake editor with a throttle short
// enough that the test never waits on real time.
func streamTestChannel(t *testing.T, interval time.Duration) (*TelegramChannel, *fakeEditor) {
	t.Helper()
	tg := newTestTelegramChannel()
	ed := &fakeEditor{}
	tg.mu.Lock()
	tg.editor = ed
	tg.notifier = &fakeNotifier{}
	tg.streamEditInterval = interval
	tg.mu.Unlock()
	return tg, ed
}

// newTestStreamer returns a streamer whose sleep is recorded rather than taken,
// so a rate-limited Finish does not stall the suite.
func newTestStreamer(t *testing.T, tg *TelegramChannel, chat int64, slept *time.Duration) *TelegramStreamer {
	t.Helper()
	s := tg.NewStreamer(fmt.Sprint(chat))
	if s == nil {
		t.Fatal("NewStreamer returned nil for a usable chat id")
	}
	s.sleep = func(d time.Duration) {
		if slept != nil {
			*slept += d
		}
	}
	return s
}

// TestConcurrentChatsNeverSeeEachOthersText is the acceptance criterion issue
// #118 calls highest priority. Streaming state lives on the streamer and not on
// TelegramChannel precisely so this cannot happen; the test pins that, because
// a single field hoisted onto the channel would silently reintroduce it.
func TestConcurrentChatsNeverSeeEachOthersText(t *testing.T) {
	tg, ed := streamTestChannel(t, time.Nanosecond)

	a := newTestStreamer(t, tg, 111, nil)
	b := newTestStreamer(t, tg, 222, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			a.Delta("a")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			b.Delta("b")
		}
	}()
	wg.Wait()

	if !a.Finish(nil) || !b.Finish(nil) {
		t.Fatal("both streams delivered text and must report delivered")
	}

	for _, c := range ed.snapshot() {
		switch c.chat {
		case "111":
			if strings.Contains(c.text, "b") {
				t.Fatalf("chat 111 was sent chat 222's text: %q", c.text)
			}
		case "222":
			if strings.Contains(c.text, "a") {
				t.Fatalf("chat 222 was sent chat 111's text: %q", c.text)
			}
		default:
			t.Fatalf("unexpected chat %q", c.chat)
		}
	}
	if got := ed.lastFor("111"); got != strings.Repeat("a", 50) {
		t.Fatalf("chat 111 final text = %q", got)
	}
	if got := ed.lastFor("222"); got != strings.Repeat("b", 50) {
		t.Fatalf("chat 222 final text = %q", got)
	}
}

// Telegram bills an edit like a send and answers 429 beyond ~one operation per
// second, so an unthrottled stream would rate-limit itself off the air.
func TestEditsAreThrottled(t *testing.T) {
	tg, ed := streamTestChannel(t, time.Hour)
	var slept time.Duration
	s := newTestStreamer(t, tg, 5, &slept)

	s.Delta("one ")
	s.Delta("two ")
	s.Delta("three")

	if n := len(ed.snapshot()); n != 1 {
		t.Fatalf("throttle allowed %d writes before the interval elapsed; want 1", n)
	}

	if !s.Finish(nil) {
		t.Fatal("Finish must deliver the buffered text")
	}
	calls := ed.snapshot()
	if len(calls) != 2 {
		t.Fatalf("want send + final edit, got %d calls", len(calls))
	}
	if calls[1].text != "one two three" {
		t.Fatalf("final edit text = %q", calls[1].text)
	}
	// Bounded: a pending throttle must not pin the goroutine for an hour.
	if slept != maxFinalEditWait {
		t.Fatalf("Finish waited %v; want it clamped to %v", slept, maxFinalEditWait)
	}
}

// Telegram answers an unchanged edit with an error, so a no-op edit spends
// rate limit for nothing — with one deliberate exception: the final flush
// re-edits an unchanged buffer exactly once to apply the parse mode, because
// interim edits render plain and Markdown is the default. Exactly once, and
// only for formatting.
func TestNoEditWhenTheBufferIsUnchanged(t *testing.T) {
	tg, ed := streamTestChannel(t, time.Nanosecond)
	s := newTestStreamer(t, tg, 9, nil)

	s.Delta("hello")
	before := len(ed.snapshot())
	if !s.Finish(nil) {
		t.Fatal("Finish must report delivery")
	}
	calls := ed.snapshot()
	extra := calls[before:]
	// Markdown is what the streamer is set to and HTML is what goes on the
	// wire; "hello" carries no markup, so the text is unchanged.
	if len(extra) != 1 || extra[0].mode != telebot.ModeHTML || extra[0].text != "hello" {
		t.Fatalf("the unchanged final flush must be exactly one formatting edit, got %+v", extra)
	}

	// With formatting already applied (plain mode), no write at all.
	s2 := newTestStreamer(t, tg, 10, nil)
	s2.SetParseMode(telebot.ModeDefault)
	s2.Delta("hello")
	before = len(ed.snapshot())
	if !s2.Finish(nil) {
		t.Fatal("Finish must report delivery")
	}
	if got := len(ed.snapshot()); got != before {
		t.Fatalf("a plain-mode unchanged final flush issued %d extra writes", got-before)
	}
}

// The keep-alive exists because Telegram clears "typing…" after 5 seconds. Once
// text is on screen it is noise, and its goroutine must not leak.
func TestFirstDeltaStopsTyping(t *testing.T) {
	tg, _ := streamTestChannel(t, time.Nanosecond)
	tg.mu.Lock()
	tg.typingInterval = 5 * time.Millisecond
	tg.mu.Unlock()

	tg.startTyping(telebot.ChatID(77))
	tg.mu.RLock()
	_, typing := tg.typingStop["77"]
	tg.mu.RUnlock()
	if !typing {
		t.Fatal("startTyping did not register the chat")
	}

	newTestStreamer(t, tg, 77, nil).Delta("first")

	tg.mu.RLock()
	_, stillTyping := tg.typingStop["77"]
	tg.mu.RUnlock()
	if stillTyping {
		t.Fatal("the first delta must stop the typing keep-alive")
	}
}

// Telegram hard-fails past 4096 bytes. Rolling over has to close and reopen a
// code fence, or the tail message renders as prose.
func TestRolloverPastTheLengthLimitKeepsCodeFencesIntact(t *testing.T) {
	tg, ed := streamTestChannel(t, time.Nanosecond)
	s := newTestStreamer(t, tg, 3, nil)

	// Three messages' worth, so the tail rolls over more than once: a rollover
	// that mishandles the remainder compounds its damage on the second lap and
	// looks correct on the first.
	s.Delta("```go\n" + strings.Repeat("x", 3*TelegramMaxMessageLen) + "\n```\n")
	if !s.Finish(nil) {
		t.Fatal("Finish must report delivery")
	}

	calls := ed.snapshot()
	if len(calls) < 2 {
		t.Fatalf("text past the limit must roll into a second message, got %d writes", len(calls))
	}
	sends := 0
	for _, c := range calls {
		// Telegram measures a message after entities parsing, so the
		// rendered length is the one that has to fit -- the HTML the final
		// edit sends is longer in bytes and shorter on screen.
		if renderedLen(c.text) > TelegramMaxMessageLen {
			t.Fatalf("a write of %d rendered characters exceeds Telegram's %d limit", renderedLen(c.text), TelegramMaxMessageLen)
		}
		if !c.edit {
			sends++
		}
	}
	if sends < 2 {
		t.Fatalf("rollover must send a new message, got %d sends", sends)
	}
	// The per-message payload must survive the trip: rollover that mangles the
	// remainder still produces well-formed-looking messages, so count the
	// content rather than trusting the shape.
	// An edit replaces its message's prior content (and the final flush may
	// re-edit the last message unchanged to apply Markdown), so the payload
	// is the *last* text of each message, not the sum of every write.
	var finals []string
	msgs := 0
	for _, c := range calls {
		if strings.Contains(c.text, "``````") {
			t.Fatalf("rollover duplicated a fence: %q", c.text[:min(80, len(c.text))])
		}
		if !c.edit {
			msgs++
			finals = append(finals, c.text)
		} else if len(finals) > 0 {
			finals[len(finals)-1] = c.text
		}
	}
	xs := 0
	for _, text := range finals {
		xs += strings.Count(text, "x")
	}
	if msgs > 5 {
		t.Fatalf("3x the limit fragmented into %d messages; want at most 5", msgs)
	}
	// Every message is written once here (the throttle is a nanosecond and
	// each rollover head lands in a single send), so the x's must total exactly
	// what went in.
	if xs != 3*TelegramMaxMessageLen {
		t.Fatalf("rollover changed the payload: %d x's out, %d in", xs, 3*TelegramMaxMessageLen)
	}
	for _, c := range calls {
		if strings.Count(c.text, "```")%2 != 0 {
			t.Fatalf("a message left a code fence open: %q…", c.text[:min(60, len(c.text))])
		}
	}
}

// A half-written stream routinely splits `**bold**`, and Telegram rejects the
// whole edit for it. Losing the text over formatting is the worst outcome.
func TestParseEntityFailureFallsBackToPlainText(t *testing.T) {
	tg, ed := streamTestChannel(t, time.Nanosecond)
	s := newTestStreamer(t, tg, 4, nil)
	s.SetParseMode(telebot.ModeMarkdown)

	// Only the final write carries a parse mode — interim edits are plain text
	// precisely because a half-written **bold** is routine — so the rejection
	// lands on the last edit.
	ed.mu.Lock()
	ed.editErrs = []error{errors.New("telegram: Bad Request: can't parse entities: unclosed entity (400)")}
	ed.mu.Unlock()

	s.Delta("**half")
	if !s.Finish(nil) {
		t.Fatal("a parse-entity failure must degrade to plain text, not lose the turn")
	}

	calls := ed.snapshot()
	if len(calls) < 3 {
		t.Fatalf("want the send, the failed formatted edit and a plain-text retry, got %d", len(calls))
	}
	last := calls[len(calls)-1]
	if last.mode != telebot.ModeDefault {
		t.Fatalf("the retry must be plain text, got mode %q", last.mode)
	}
	if !strings.Contains(last.text, "**half") {
		t.Fatalf("the text was lost: %q", last.text)
	}
}

// Retrying into a 429 earns another one and pushes the wait further out.
func TestFloodErrorPushesTheNextEditOut(t *testing.T) {
	tg, ed := streamTestChannel(t, time.Nanosecond)
	var slept time.Duration
	s := newTestStreamer(t, tg, 6, &slept)

	ed.mu.Lock()
	ed.editErrs = []error{errors.New("telegram: too many requests: retry after 7 (429)")}
	ed.mu.Unlock()

	s.Delta("first")   // lands
	s.Delta(" second") // rate-limited
	before := len(ed.snapshot())

	s.Delta(" third") // must not retry into the 429
	if got := len(ed.snapshot()); got != before {
		t.Fatalf("the streamer retried during the retry_after window (%d extra writes)", got-before)
	}

	if !s.Finish(nil) {
		t.Fatal("Finish must still deliver after the window")
	}
	// 7s was asked for and honoured, but clamped: a hostile retry_after must
	// not pin the turn's goroutine.
	if slept != maxFinalEditWait {
		t.Fatalf("Finish waited %v; want the retry_after clamped to %v", slept, maxFinalEditWait)
	}
	if got := ed.lastFor("6"); got != "first second third" {
		t.Fatalf("final text = %q", got)
	}
}

// A partial answer that just stops reads as a complete one, so a mid-stream
// provider failure has to be visible in the chat.
func TestFinishMarksAnInterruptedStream(t *testing.T) {
	tg, ed := streamTestChannel(t, time.Nanosecond)
	s := newTestStreamer(t, tg, 8, nil)

	s.Delta("partial answer")
	if !s.Finish(errors.New("provider closed the connection")) {
		t.Fatal("an interrupted stream still delivered text")
	}
	last := ed.lastFor("8")
	if !strings.Contains(last, "partial answer") {
		t.Fatalf("the partial text was lost: %q", last)
	}
	if !strings.Contains(last, "cut short") || !strings.Contains(last, "provider closed the connection") {
		t.Fatalf("the failure is invisible in the chat: %q", last)
	}
}

// When the very first send fails, nothing is on screen and the caller has to
// deliver the reply the ordinary way — reporting delivered would lose it.
func TestFailedFirstSendReportsNotDelivered(t *testing.T) {
	tg, ed := streamTestChannel(t, time.Nanosecond)
	s := newTestStreamer(t, tg, 2, nil)

	ed.mu.Lock()
	ed.sendErrs = []error{errors.New("telegram: chat not found (400)")}
	ed.mu.Unlock()

	s.Delta("hello")
	if s.Delivered() {
		t.Fatal("nothing reached the chat; Delivered must be false")
	}
	if s.Finish(nil) {
		t.Fatal("Finish must report not-delivered so the caller falls back to the bus")
	}
	if n := len(ed.snapshot()); n != 1 {
		t.Fatalf("a broken stream kept writing: %d calls", n)
	}
}

// A nil streamer is the "no streaming this turn" signal and every method has to
// tolerate it: the gateway calls Finish unconditionally.
func TestNilStreamerIsInert(t *testing.T) {
	var s *TelegramStreamer
	s.Delta("x")
	s.SetParseMode(telebot.ModeMarkdown)
	if s.Delivered() || s.Finish(errors.New("boom")) {
		t.Fatal("a nil streamer must report nothing delivered")
	}
}

// A non-numeric chat id cannot be streamed to, and guessing would send one
// user's answer to whatever chat the parse happened to land on.
func TestNewStreamerRejectsAnUnusableChatID(t *testing.T) {
	tg, _ := streamTestChannel(t, time.Nanosecond)
	if s := tg.NewStreamer("not-a-chat"); s != nil {
		t.Fatal("a non-numeric chat id must not produce a streamer")
	}
}

// The retry_after seconds must survive telebot handing back an untyped error:
// backing off by a fixed guess instead is how a stream earns a second 429.
func TestFloodRetryAfterReadsTheSeconds(t *testing.T) {
	err := errors.New("telegram: too many requests: retry after 11 (429)")
	if !isFloodError(err) {
		t.Fatal("a 429 described in text is still a 429")
	}
	if got := floodRetryAfter(err); got != 11*time.Second {
		t.Fatalf("floodRetryAfter = %v; want 11s", got)
	}
	if got := floodRetryAfter(errors.New("telegram: too many requests (429)")); got != defaultStreamInterval {
		t.Fatalf("with no number to read, the ordinary throttle applies; got %v", got)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestFinishReportsUndeliveredWhenTheFinalEditFails pins the delivery contract
// the gateway relies on: a true return suppresses the bus publish, so it may
// only be returned when the whole answer is on screen. Reporting delivery
// because an earlier interim edit landed loses everything the user was waiting
// for — the deleted-message and transient-400 cases both end here.
func TestFinishReportsUndeliveredWhenTheFinalEditFails(t *testing.T) {
	tg, ed := streamTestChannel(t, time.Nanosecond)
	s := newTestStreamer(t, tg, 555, nil)

	s.Delta("partial answer")
	if !s.Delivered() {
		t.Fatal("first delta should have reached the chat")
	}

	// The user deleted the in-progress message, or Telegram rejected the edit.
	// One for the delta, one for the final flush that retries it.
	notFound := errors.New("400 Bad Request: message to edit not found")
	ed.editErrs = []error{notFound, notFound}

	s.Delta(" and the rest of it")
	if s.Finish(nil) {
		t.Fatal("Finish reported the answer delivered after the final edit failed; " +
			"the gateway suppresses the bus publish on true, so the reply is lost")
	}
	// The interesting case is a *partial* stream, not one that never started:
	// text is on screen, so the old `delivered && !broken` rule returned true.
	if !s.delivered || s.broken {
		t.Fatalf("test is not exercising a partial stream (delivered=%v broken=%v)", s.delivered, s.broken)
	}
}

// TestRolloverFailureAfterDeliveryDoesNotRepublish is the mirror case: once
// whole messages are on screen, a later failure must not declare the turn
// undelivered, because the bus fallback would then repeat everything already
// sent. Gating on s.msg == nil got this wrong — a rollover clears s.msg.
func TestRolloverFailureAfterDeliveryDoesNotRepublish(t *testing.T) {
	tg, ed := streamTestChannel(t, time.Nanosecond)
	s := newTestStreamer(t, tg, 666, nil)

	// Land the head, then fail the Send that opens the message after it.
	ed.sendErrs = []error{nil, errors.New("400 Bad Request: chat not found")}
	s.Delta(strings.Repeat("x", TelegramMaxMessageLen+500))

	if s.broken {
		t.Fatal("a failure after text is already on screen marked the turn broken; " +
			"the bus would republish the whole answer on top of what was sent")
	}
	if len(ed.snapshot()) < 2 {
		t.Fatalf("test is not exercising a rollover: %d editor calls", len(ed.snapshot()))
	}
}

// TestRolloverKeepsGenuineClosingFences pins that the remainder after a
// rollover is the real remainder. Deriving it by trimming a closing fence off
// the head cannot tell a fence splitOnce added from one the model wrote, so a
// message whose split point lands right after a genuine ``` had a spurious
// opening fence prepended — inverting fence parity for the rest of the answer.
func TestRolloverKeepsGenuineClosingFences(t *testing.T) {
	tg, ed := streamTestChannel(t, time.Nanosecond)
	s := newTestStreamer(t, tg, 777, nil)

	// A closed code block that ends exactly at the split point, then prose.
	body := "```\n" + strings.Repeat("c", TelegramMaxMessageLen-10) + "\n```"
	tail := "\nplain prose after the block, not code\n"
	s.Delta(body + tail)
	s.Finish(nil)

	last := ed.lastFor("777")
	if strings.HasPrefix(last, "```") {
		t.Fatalf("rollover reopened a code fence that the text had already closed; "+
			"tail begins %q", last[:min(40, len(last))])
	}
	if !strings.Contains(last, "plain prose after the block") {
		t.Fatalf("tail did not reach the chat: %q", last)
	}
}

// Every ordinary streamed reply renders as Markdown on the final edit — the
// default used to be plain text, showing literal ``` and ** on the phone
// while the parse-entity fallback protected a path nothing took.
func TestFinalEditDefaultsToMarkdown(t *testing.T) {
	tg, ed := streamTestChannel(t, time.Hour)
	s := newTestStreamer(t, tg, 42, nil)

	s.Delta("some `code` here")
	if !s.Finish(nil) {
		t.Fatal("Finish reported non-delivery")
	}
	calls := ed.snapshot()
	last := calls[len(calls)-1]
	// The default is Markdown in, HTML out: the legacy Markdown parser
	// rejects ordinary prose, so the source is converted before it is sent.
	if last.mode != telebot.ModeHTML {
		t.Errorf("final edit mode = %q, want HTML by default", last.mode)
	}
	if last.text != "some <code>code</code> here" {
		t.Errorf("final edit text = %q, want the converted HTML", last.text)
	}
	// Interim sends stay plain: a partial stream splits fences mid-way.
	if calls[0].mode != telebot.ModeDefault {
		t.Errorf("interim send mode = %q, want plain", calls[0].mode)
	}
}

// SetReplyTo threads the first message of the turn to the question; a 4096
// rollover's second message must not re-anchor.
func TestStreamedReplyThreadsToTheQuestion(t *testing.T) {
	tg, ed := streamTestChannel(t, time.Nanosecond)
	s := newTestStreamer(t, tg, 42, nil)
	s.SetReplyTo(777)

	s.Delta("part one ")
	// Force a rollover: exceed the message limit so a second message sends.
	s.Delta(strings.Repeat("x", TelegramMaxMessageLen))
	s.Finish(nil)

	var sends []editorCall
	for _, c := range ed.snapshot() {
		if !c.edit {
			sends = append(sends, c)
		}
	}
	if len(sends) < 2 {
		t.Fatalf("expected a rollover to produce a second send, got %d", len(sends))
	}
	if sends[0].replyTo != 777 {
		t.Errorf("first send replyTo = %d, want 777", sends[0].replyTo)
	}
	for _, c := range sends[1:] {
		if c.replyTo != 0 {
			t.Errorf("rollover send re-anchored to %d; only the first message threads", c.replyTo)
		}
	}
}

// A tool-progress status line reaches the chat while the agent works, and the
// reply then replaces it in the same message — the chat never accumulates a
// trail of stale progress lines above the answer.
func TestStatusIsReplacedInPlaceByTheReply(t *testing.T) {
	tg, ed := streamTestChannel(t, time.Nanosecond)
	s := newTestStreamer(t, tg, 42, nil)
	s.SetReplyTo(7)

	s.Status("⚙️ shell: go test ./...")
	s.Status("✅ shell (1.2s)")
	s.Delta("the tests pass")
	if !s.Finish(nil) {
		t.Fatal("Finish reported non-delivery")
	}

	calls := ed.snapshot()
	if len(calls) < 3 {
		t.Fatalf("calls = %+v", calls)
	}
	// First status is a send, threaded to the question; the rest are edits of
	// that same message — including the reply text.
	if calls[0].edit || calls[0].replyTo != 7 {
		t.Errorf("first status call = %+v, want a send threaded to 7", calls[0])
	}
	for i, c := range calls[1:] {
		if !c.edit {
			t.Errorf("call %d = %+v, want an edit of the status message", i+1, c)
		}
	}
	last := calls[len(calls)-1]
	if last.text != "the tests pass" {
		t.Errorf("final text = %q, want the reply", last.text)
	}
}

// Once reply text has begun arriving the message belongs to the answer: a
// late status must not clobber it.
func TestStatusNeverClobbersReplyText(t *testing.T) {
	tg, ed := streamTestChannel(t, time.Nanosecond)
	s := newTestStreamer(t, tg, 42, nil)

	s.Delta("partial answer")
	s.Status("⚙️ shell: rm -rf /")
	if !s.Finish(nil) {
		t.Fatal("Finish reported non-delivery")
	}
	for _, c := range ed.snapshot() {
		if strings.Contains(c.text, "⚙️") {
			t.Fatalf("a status write landed after reply text began: %+v", c)
		}
	}
}

// A turn whose reply never streams (broken stream, unsupported provider)
// deletes its status message: the answer arrives via the bus fallback and a
// leftover "⚙️ ..." line above it would read as a second, stuck turn.
func TestFinishDeletesAnOrphanedStatusMessage(t *testing.T) {
	tg, ed := streamTestChannel(t, time.Nanosecond)
	s := newTestStreamer(t, tg, 42, nil)

	s.Status("⚙️ shell: sleep 60")
	if s.Finish(nil) {
		t.Fatal("Finish reported delivery for a status-only turn")
	}
	calls := ed.snapshot()
	last := calls[len(calls)-1]
	if !last.deleted {
		t.Fatalf("the orphaned status message was not deleted: %+v", calls)
	}
}

// Status shares the edit-rate budget and is best-effort: inside the throttle
// window it is skipped, never queued.
func TestStatusIsSkippedInsideTheThrottleWindow(t *testing.T) {
	tg, ed := streamTestChannel(t, time.Hour)
	s := newTestStreamer(t, tg, 42, nil)

	s.Status("⚙️ first")
	s.Status("⚙️ second") // inside the window — dropped
	calls := ed.snapshot()
	if len(calls) != 1 || calls[0].text != "⚙️ first" {
		t.Fatalf("calls = %+v, want only the first status", calls)
	}
}

// A turn whose reply text never lands (the final write fails with nothing
// ever delivered) must also delete its status message — the answer arrives
// via the bus fallback, same as the status-only case.
func TestFailedFinalWriteStillDeletesTheStatusMessage(t *testing.T) {
	tg, ed := streamTestChannel(t, time.Nanosecond)
	s := newTestStreamer(t, tg, 42, nil)

	s.Status("⚙️ shell: sleep 60")
	ed.mu.Lock()
	ed.editErrs = []error{errors.New("400 chat not found")}
	ed.mu.Unlock()
	s.Delta("the answer")
	if s.Finish(nil) {
		t.Fatal("Finish reported delivery over a failed write")
	}
	calls := ed.snapshot()
	if !calls[len(calls)-1].deleted {
		t.Fatalf("stale status message was not deleted: %+v", calls)
	}
}

// A 429 on the initial status send must back off like the edit path does:
// ignoring retry_after just earns the next write another 429.
func TestStatusSendFloodErrorBacksOff(t *testing.T) {
	tg, ed := streamTestChannel(t, time.Nanosecond)
	s := newTestStreamer(t, tg, 42, nil)
	base := time.Unix(1000, 0)
	s.now = func() time.Time { return base }

	ed.mu.Lock()
	ed.sendErrs = []error{errors.New("telegram: retry after 30 (429)")}
	ed.mu.Unlock()
	s.Status("⚙️ first")
	s.Status("⚙️ second") // inside the flood window — must be skipped
	if n := len(ed.snapshot()); n != 1 {
		t.Fatalf("%d writes went out during the flood window, want 1", n)
	}
}
