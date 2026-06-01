package providers

import (
	"testing"
	"time"
)

func TestAPIKeyPool_SingleKey(t *testing.T) {
	p := NewAPIKeyPool([]string{"key1"}, 0, 0)
	if got := p.Next(); got != "key1" {
		t.Fatalf("expected key1, got %s", got)
	}
	if got := p.Len(); got != 1 {
		t.Fatalf("expected 1 key, got %d", got)
	}
}

func TestAPIKeyPool_RoundRobin(t *testing.T) {
	p := NewAPIKeyPool([]string{"key1", "key2", "key3"}, 0, 0)
	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		seen[p.Next()]++
	}
	if len(seen) != 3 {
		t.Fatalf("expected 3 unique keys, got %d", len(seen))
	}
	for k, v := range seen {
		if v != 2 {
			t.Fatalf("key %s expected 2 uses, got %d", k, v)
		}
	}
}

func TestAPIKeyPool_CooldownOnFailures(t *testing.T) {
	p := NewAPIKeyPool([]string{"key1", "key2"}, 0, 2)

	// First failure: still available (2 failures needed before cooldown)
	p.ReportFailure("key1")
	if got := p.Next(); got != "key1" {
		t.Fatalf("expected key1 after 1 failure, got %s", got)
	}

	// Second failure: now in cooldown, should skip to key2
	p.ReportFailure("key1")
	if got := p.Next(); got != "key2" {
		t.Fatalf("expected key2 after 2 failures, got %s", got)
	}
}

func TestAPIKeyPool_ReportSuccessResetsFailures(t *testing.T) {
	p := NewAPIKeyPool([]string{"key1"}, 0, 2)
	p.ReportFailure("key1")
	p.ReportSuccess("key1")
	if got := p.Next(); got != "key1" {
		t.Fatalf("expected key1 after success reset, got %s", got)
	}
	p.ReportFailure("key1")
	if got := p.Next(); got != "key1" {
		t.Fatalf("expected key1 after 1 failure (reset), got %s", got)
	}
}

func TestAPIKeyPool_AllInCooldown(t *testing.T) {
	p := NewAPIKeyPool([]string{"key1", "key2"}, 0, 1)
	p.ReportCooldown("key1")
	p.ReportCooldown("key2")
	if got := p.Next(); got != "" {
		t.Fatalf("expected empty (all cooldown), got %s", got)
	}
}

func TestAPIKeyPool_ReportCooldown(t *testing.T) {
	p := NewAPIKeyPool([]string{"key1"}, 0, 1)
	p.ReportCooldown("key1")
	if got := p.Next(); got != "" {
		t.Fatalf("expected empty after cooldown, got %s", got)
	}
}

func TestAPIKeyPool_Available(t *testing.T) {
	p := NewAPIKeyPool([]string{"key1", "key2", "key3"}, 0, 1)
	if got := p.Available(); got != 3 {
		t.Fatalf("expected 3 available, got %d", got)
	}
	p.ReportCooldown("key1")
	if got := p.Available(); got != 2 {
		t.Fatalf("expected 2 available, got %d", got)
	}
}

func TestAPIKeyPool_DefaultValues(t *testing.T) {
	p := NewAPIKeyPool([]string{"key1"}, 0, 0)
	if p.cooldownDur != 24*time.Hour {
		t.Fatalf("expected 24h cooldown, got %v", p.cooldownDur)
	}
	if p.cooldownAfterFailures != 3 {
		t.Fatalf("expected cooldownAfterFailures 3, got %d", p.cooldownAfterFailures)
	}
}

func TestAPIKeyPool_EmptyPool(t *testing.T) {
	p := NewAPIKeyPool([]string{}, 0, 0)
	if got := p.Next(); got != "" {
		t.Fatalf("expected empty from empty pool, got %s", got)
	}
	if got := p.Len(); got != 0 {
		t.Fatalf("expected len 0, got %d", got)
	}
}
