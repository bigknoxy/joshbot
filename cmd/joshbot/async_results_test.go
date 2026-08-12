package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/tools"
)

// A background task's notification is unsolicited: the user is not waiting on
// it, and it arrives long after the turn that started it. Every failure mode
// here is silent. A failed task rendered as a success line is a task the user
// believes finished. An output cut with no marker reads as a complete answer
// that happens to stop mid-sentence. And a notification published against the
// wrong channel is delivered to somebody who never started the task — on
// Telegram, to a different chat entirely.

// An error must be reported as one. The tool name goes with it because the user
// has no other way to tell which of several background tasks failed.
func TestAsyncResultMessageReportsAFailure(t *testing.T) {
	msg := asyncResultMessage(tools.AsyncResult{
		ToolName: "shell",
		Output:   "partial output before the failure",
		Error:    errors.New("exit status 1"),
	})

	if !strings.Contains(msg, "failed") {
		t.Errorf("message = %q, want it to say the task failed", msg)
	}
	if !strings.Contains(msg, "shell") {
		t.Errorf("message = %q, want the tool name", msg)
	}
	if !strings.Contains(msg, "exit status 1") {
		t.Errorf("message = %q, want the underlying error", msg)
	}
	if strings.Contains(msg, "completed") {
		t.Errorf("a failed task was reported as completed: %q", msg)
	}
}

// Output at or under the cap is passed through whole. Truncating early would
// cost the user the end of a short answer for no reason.
func TestAsyncResultMessageKeepsShortOutputWhole(t *testing.T) {
	out := strings.Repeat("x", asyncMaxOutput)
	msg := asyncResultMessage(tools.AsyncResult{ToolName: "shell", Output: out})

	if strings.Contains(msg, "truncated") {
		t.Error("output exactly at the cap was truncated")
	}
	if !strings.Contains(msg, out) {
		t.Error("the output was not carried in the notification")
	}
}

// Over the cap it is cut, and the cut has to be visible. Telegram hard-fails
// over 4096 bytes, so an uncapped build log is a notification that never
// arrives at all.
func TestAsyncResultMessageTruncatesLongOutputVisibly(t *testing.T) {
	out := strings.Repeat("x", asyncMaxOutput+500)
	msg := asyncResultMessage(tools.AsyncResult{ToolName: "shell", Output: out})

	if !strings.Contains(msg, "truncated") {
		t.Error("over-long output was cut with no marker; the user reads it as complete")
	}
	if strings.Count(msg, "x") != asyncMaxOutput {
		t.Errorf("kept %d bytes of output, want the %d-byte cap", strings.Count(msg, "x"), asyncMaxOutput)
	}
}

// The result carries the channel and chat it belongs to, and the notification
// has to go back to that one. Publishing against a zero channel drops it, and
// publishing against the wrong one delivers somebody else's task output.
func TestPublishAsyncResultsRoutesBackToTheOriginatingChat(t *testing.T) {
	msgBus := bus.NewMessageBus()
	ch := make(chan tools.AsyncResult, 1)

	done := make(chan struct{})
	go func() {
		publishAsyncResults(ch, msgBus)
		close(done)
	}()

	ch <- tools.AsyncResult{ToolName: "shell", Output: "ok", Channel: "telegram", ChatID: "12345"}

	select {
	case got := <-msgBus.OutboundChannel():
		if got.Channel != "telegram" || got.ChannelID != "12345" {
			t.Errorf("published to %s/%s, want telegram/12345", got.Channel, got.ChannelID)
		}
		if !strings.Contains(got.Content, "ok") {
			t.Errorf("content = %q, want the task output", got.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing reached the bus; the notification is lost")
	}

	// Closing the callback channel has to end the goroutine. It is started once
	// per setupComponents, so a loop that outlives its channel leaks a
	// goroutine per gateway restart.
	close(ch)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publishAsyncResults did not return after its channel closed")
	}
}
