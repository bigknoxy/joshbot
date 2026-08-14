package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bigknoxy/joshbot/internal/bus"
)

// The chat-id map survives a restart: a cron reminder firing before the
// user's first message must still know where to go.
func TestChatIDPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_ids.json")

	s1 := NewBusMessageSender(bus.NewMessageBus())
	if err := s1.EnablePersistence(path); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	s1.SetChatID("telegram", "123456")
	s1.SetChatID("cli", "cli_user")

	// A fresh sender (the restarted process) recalls both.
	s2 := NewBusMessageSender(bus.NewMessageBus())
	if err := s2.EnablePersistence(path); err != nil {
		t.Fatalf("EnablePersistence (reload): %v", err)
	}
	if id, ok := s2.GetChatID("telegram"); !ok || id != "123456" {
		t.Errorf("telegram id after restart = %q, %v", id, ok)
	}
	if id, ok := s2.GetChatID("cli"); !ok || id != "cli_user" {
		t.Errorf("cli id after restart = %q, %v", id, ok)
	}

	// 0600: chat ids identify the operator's chats.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("chat_ids.json mode = %o, want 0600", info.Mode().Perm())
	}
}

// A live value wins over a stale persisted one, and a damaged file degrades
// to an empty map instead of taking the gateway down.
func TestChatIDPersistenceLiveWinsAndCorruptTolerated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_ids.json")
	if err := os.WriteFile(path, []byte(`{"telegram":"old"}`), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewBusMessageSender(bus.NewMessageBus())
	s.SetChatID("telegram", "live")
	if err := s.EnablePersistence(path); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	if id, _ := s.GetChatID("telegram"); id != "live" {
		t.Errorf("stale persisted id overwrote the live one: %q", id)
	}

	if err := os.WriteFile(path, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	s2 := NewBusMessageSender(bus.NewMessageBus())
	if err := s2.EnablePersistence(path); err != nil {
		t.Fatalf("a damaged file must not error: %v", err)
	}
	s2.SetChatID("telegram", "fresh")
	s3 := NewBusMessageSender(bus.NewMessageBus())
	if err := s3.EnablePersistence(path); err != nil {
		t.Fatal(err)
	}
	if id, _ := s3.GetChatID("telegram"); id != "fresh" {
		t.Errorf("recovery write did not land: %q", id)
	}
}

// An id set before EnablePersistence must still reach disk: SetChatID skips
// persisting unchanged values, so if enablement did not write the merged map
// back, that id would silently vanish on the next restart (#269 review).
func TestChatIDSetBeforeEnablementSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_ids.json")

	s1 := NewBusMessageSender(bus.NewMessageBus())
	s1.SetChatID("telegram", "999")
	if err := s1.EnablePersistence(path); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}

	s2 := NewBusMessageSender(bus.NewMessageBus())
	if err := s2.EnablePersistence(path); err != nil {
		t.Fatalf("EnablePersistence (reload): %v", err)
	}
	if id, ok := s2.GetChatID("telegram"); !ok || id != "999" {
		t.Errorf("telegram id after restart = %q, %v (want %q, true)", id, ok, "999")
	}

	// And the file's mode was still set on that merge write-back.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("chat_ids.json mode = %v, want 0600", info.Mode().Perm())
	}
}
