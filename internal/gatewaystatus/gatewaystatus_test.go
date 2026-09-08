package gatewaystatus

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestPath(t *testing.T) {
	got := Path("/home/u/.joshbot")
	want := filepath.Join("/home/u/.joshbot", "gateway-status.json")
	if got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestWriteAndReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway-status.json")

	w := NewWriter(path)
	w.SetPID(1234, time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC))
	w.SetChannel("telegram", "connected")
	w.SetChannel("discord", "disabled")

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("SetChannel did not write the status file: %v", err)
	}

	doc := Read(path)
	if doc == nil {
		t.Fatal("Read returned nil for a file just written")
	}
	if doc.PID != 1234 {
		t.Fatalf("PID = %d, want 1234", doc.PID)
	}
	if len(doc.Channels) != 2 {
		t.Fatalf("len(Channels) = %d, want 2", len(doc.Channels))
	}
	byName := map[string]string{}
	for _, c := range doc.Channels {
		byName[c.Name] = c.State
	}
	if byName["telegram"] != "connected" {
		t.Errorf("telegram state = %q, want connected", byName["telegram"])
	}
	if byName["discord"] != "disabled" {
		t.Errorf("discord state = %q, want disabled", byName["discord"])
	}
}

func TestSetChannelUpdatesInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-status.json")
	w := NewWriter(path)
	w.SetChannel("telegram", "connected")
	w.SetChannel("telegram", "reconnecting")

	doc := Read(path)
	if doc == nil || len(doc.Channels) != 1 {
		t.Fatalf("expected one channel entry, got %+v", doc)
	}
	if doc.Channels[0].State != "reconnecting" {
		t.Fatalf("state = %q, want reconnecting", doc.Channels[0].State)
	}
}

func TestRemoveChannel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-status.json")
	w := NewWriter(path)
	w.SetChannel("telegram", "connected")
	w.SetChannel("discord", "connected")
	w.RemoveChannel("discord")

	doc := Read(path)
	if doc == nil || len(doc.Channels) != 1 || doc.Channels[0].Name != "telegram" {
		t.Fatalf("after RemoveChannel, expected only telegram, got %+v", doc)
	}
}

func TestReadMissingFileReturnsNil(t *testing.T) {
	if doc := Read(filepath.Join(t.TempDir(), "nope.json")); doc != nil {
		t.Fatalf("Read of a missing file = %+v, want nil", doc)
	}
}

func TestReadCorruptFileReturnsNil(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-status.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if doc := Read(path); doc != nil {
		t.Fatalf("Read of corrupt file = %+v, want nil", doc)
	}
}

func TestDeleteRemovesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-status.json")
	w := NewWriter(path)
	w.SetChannel("telegram", "connected")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file missing after write: %v", err)
	}
	w.Delete()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Delete left the file behind: %v", err)
	}
}

func TestWriterIsConcurrencySafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-status.json")
	w := NewWriter(path)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			w.SetChannel("chat", "connected")
			w.SetPID(n, time.Now())
		}(i)
	}
	wg.Wait()

	doc := Read(path)
	if doc == nil {
		t.Fatal("concurrent writes produced no readable file")
	}
}
