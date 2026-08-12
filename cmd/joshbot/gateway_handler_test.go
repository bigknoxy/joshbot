package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/bigknoxy/joshbot/internal/bus"
)

// The gateway handler is where every rule that decides what a user actually
// sees lives: whether a reply is published at all, whether it is published
// twice, and whether a crash reaches the chat or vanishes into the log. All of
// them fail silently — a duplicated reply and a lost reply both look like a
// working bot to everything except the person reading it.

type recordingDeps struct {
	mu        sync.Mutex
	published []bus.OutboundMessage
	chatIDs   [][2]string
}

func (r *recordingDeps) publish(m bus.OutboundMessage) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.published = append(r.published, m)
	return true
}

func (r *recordingDeps) setChatID(channel, chatID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.chatIDs = append(r.chatIDs, [2]string{channel, chatID})
}

func (r *recordingDeps) out() []bus.OutboundMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bus.OutboundMessage(nil), r.published...)
}

// fakeStreamer stands in for *channels.TelegramStreamer. delivered is what
// Finish reports: true means the whole answer already reached the chat.
type fakeStreamer struct {
	deltas    []string
	delivered bool
	finishErr error
	finished  bool
}

func (f *fakeStreamer) Delta(text string) { f.deltas = append(f.deltas, text) }
func (f *fakeStreamer) Finish(procErr error) bool {
	f.finished = true
	f.finishErr = procErr
	return f.delivered
}

func telegramMsg(sender, content string) bus.InboundMessage {
	return bus.InboundMessage{
		Channel:  "telegram",
		SenderID: sender,
		Content:  content,
		Metadata: map[string]any{"chat_id": "4242"},
	}
}

// baseDeps wires a handler with no streaming and a fixed reply.
func baseDeps(r *recordingDeps, reply string, err error) gatewayDeps {
	return gatewayDeps{
		publish:     r.publish,
		setChatID:   r.setChatID,
		process:     func(context.Context, bus.InboundMessage) (string, error) { return reply, err },
		newStreamer: func(bus.InboundMessage) gatewayStreamer { return nil },
	}
}

// An unstreamed turn must publish exactly one outbound message, addressed to
// the chat the request came from. getChannelID reads it out of the metadata;
// dropping it sends the reply to an empty channel id, which the Telegram
// channel silently discards.
func TestGatewayHandlerPublishesTheReply(t *testing.T) {
	rec := &recordingDeps{}
	gatewayHandler(baseDeps(rec, "hello there", nil))(context.Background(), telegramMsg("user1", "hi"))

	out := rec.out()
	if len(out) != 1 {
		t.Fatalf("published %d messages, want 1: %+v", len(out), out)
	}
	if out[0].Content != "hello there" {
		t.Errorf("content = %q, want the agent's reply", out[0].Content)
	}
	if out[0].ChannelID != "4242" {
		t.Errorf("ChannelID = %q, want the sender's chat id (4242)", out[0].ChannelID)
	}
	if len(rec.chatIDs) != 1 || rec.chatIDs[0] != [2]string{"telegram", "4242"} {
		t.Errorf("the chat id was not recorded for proactive messaging: %+v", rec.chatIDs)
	}
}

// A panic inside the agent must still produce a reply. Without the recover the
// bus goroutine dies and the user waits forever on a message that will never
// come, with the cause visible only in the log.
func TestGatewayHandlerTurnsAPanicIntoAReply(t *testing.T) {
	rec := &recordingDeps{}
	d := baseDeps(rec, "", nil)
	d.process = func(context.Context, bus.InboundMessage) (string, error) { panic("boom") }

	gatewayHandler(d)(context.Background(), telegramMsg("user1", "hi"))

	out := rec.out()
	if len(out) != 1 {
		t.Fatalf("a panicking turn published %d messages, want 1", len(out))
	}
	if !strings.Contains(out[0].Content, "internal error") {
		t.Errorf("the panic reply does not tell the user anything: %q", out[0].Content)
	}
	if strings.Contains(out[0].Content, "boom") {
		t.Errorf("the panic value was leaked to the chat: %q", out[0].Content)
	}
}

// HEARTBEAT_OK means "nothing needs your attention". Publishing it turns a
// background check into unsolicited chatter on whatever cadence the heartbeat
// runs at.
func TestGatewayHandlerSuppressesQuietHeartbeats(t *testing.T) {
	for _, reply := range []string{"HEARTBEAT_OK", "heartbeat_ok — nothing to do", "   ", ""} {
		rec := &recordingDeps{}
		gatewayHandler(baseDeps(rec, reply, nil))(context.Background(), telegramMsg("heartbeat", "scan"))
		if n := len(rec.out()); n != 0 {
			t.Errorf("heartbeat reply %q published %d message(s), want 0", reply, n)
		}
	}
}

// A heartbeat that did find something must still be delivered — suppressing
// everything from the heartbeat makes the whole feature invisible.
func TestGatewayHandlerDeliversActionableHeartbeats(t *testing.T) {
	rec := &recordingDeps{}
	gatewayHandler(baseDeps(rec, "the deploy failed", nil))(context.Background(), telegramMsg("heartbeat", "scan"))
	if n := len(rec.out()); n != 1 {
		t.Fatalf("an actionable heartbeat published %d message(s), want 1", n)
	}
}

// When the stream carried the whole answer, the bus copy must be suppressed:
// publishing it posts the complete reply a second time under the incremental
// one.
func TestGatewayHandlerSuppressesTheBusCopyOnlyWhenTheStreamDelivered(t *testing.T) {
	t.Run("stream delivered", func(t *testing.T) {
		rec := &recordingDeps{}
		fs := &fakeStreamer{delivered: true}
		d := baseDeps(rec, "streamed answer", nil)
		d.newStreamer = func(bus.InboundMessage) gatewayStreamer { return fs }

		gatewayHandler(d)(context.Background(), telegramMsg("user1", "hi"))
		if n := len(rec.out()); n != 0 {
			t.Errorf("the reply was published %d time(s) as well as streamed", n)
		}
		if !fs.finished {
			t.Error("Finish was never called, so the final edit never landed")
		}
	})

	// The inverse is the one that loses data: a stream that broke mid-way
	// reports false, and the bus fallback exists for exactly that case.
	t.Run("stream delivered nothing", func(t *testing.T) {
		rec := &recordingDeps{}
		d := baseDeps(rec, "streamed answer", nil)
		d.newStreamer = func(bus.InboundMessage) gatewayStreamer { return &fakeStreamer{delivered: false} }

		gatewayHandler(d)(context.Background(), telegramMsg("user1", "hi"))
		out := rec.out()
		if len(out) != 1 {
			t.Fatalf("a broken stream published %d message(s), want the bus fallback", len(out))
		}
		if out[0].Content != "streamed answer" {
			t.Errorf("the fallback content = %q, want the whole reply", out[0].Content)
		}
	})
}

// Process reports LLM failures in band, as reply text with a nil error. The
// handler must translate that back for the streamer, or a turn that streamed
// some text and then failed ends silently: the partial text sits on screen and
// nothing says why it stopped.
func TestGatewayHandlerTranslatesInBandErrorsForTheStreamer(t *testing.T) {
	rec := &recordingDeps{}
	fs := &fakeStreamer{delivered: true}
	d := baseDeps(rec, "Error processing request: provider unreachable", nil)
	d.newStreamer = func(bus.InboundMessage) gatewayStreamer { return fs }

	gatewayHandler(d)(context.Background(), telegramMsg("user1", "hi"))

	if fs.finishErr == nil {
		t.Fatal("Finish was given a nil error for an in-band failure; the stream ends with no explanation")
	}
	if !strings.Contains(fs.finishErr.Error(), "provider unreachable") {
		t.Errorf("Finish error = %v, want the provider failure", fs.finishErr)
	}
}

// A hard error goes to the chat only when the stream carried nothing.
// Publishing it as well as appending it to the stream contradicts what the
// user is already reading.
func TestGatewayHandlerErrorPathRespectsTheStream(t *testing.T) {
	t.Run("nothing streamed", func(t *testing.T) {
		rec := &recordingDeps{}
		gatewayHandler(baseDeps(rec, "", errors.New("context deadline exceeded")))(
			context.Background(), telegramMsg("user1", "hi"))

		out := rec.out()
		if len(out) != 1 {
			t.Fatalf("published %d message(s) for a failed turn, want 1", len(out))
		}
		if !strings.Contains(out[0].Content, "context deadline exceeded") {
			t.Errorf("the error reply does not name the cause: %q", out[0].Content)
		}
	})

	t.Run("error appended to the stream", func(t *testing.T) {
		rec := &recordingDeps{}
		fs := &fakeStreamer{delivered: true}
		d := baseDeps(rec, "", errors.New("context deadline exceeded"))
		d.newStreamer = func(bus.InboundMessage) gatewayStreamer { return fs }

		gatewayHandler(d)(context.Background(), telegramMsg("user1", "hi"))
		if n := len(rec.out()); n != 0 {
			t.Errorf("the error was published %d time(s) as well as appended to the stream", n)
		}
		if fs.finishErr == nil {
			t.Error("the error never reached the stream either, so the turn ended silently")
		}
	})
}

// A turn with no streamer must not panic on Finish. The production seam returns
// a nil interface for an unstreamed turn, and the handler substitutes a no-op —
// wrapping a typed nil *TelegramStreamer here instead would produce a non-nil
// interface and a call on a nil receiver.
func TestGatewayHandlerHandlesAnAbsentStreamer(t *testing.T) {
	rec := &recordingDeps{}
	d := baseDeps(rec, "ok", nil)
	d.newStreamer = func(bus.InboundMessage) gatewayStreamer { return nil }

	gatewayHandler(d)(context.Background(), telegramMsg("user1", "hi"))
	if n := len(rec.out()); n != 1 {
		t.Fatalf("published %d message(s), want 1", n)
	}
}
