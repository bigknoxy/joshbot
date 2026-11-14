package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/charmbracelet/log"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// FileName is the ledger file's name inside the joshbot home directory
// (~/.joshbot by default). The joining is done by the caller (cmd/joshbot)
// so this package never imports internal/config.
const FileName = "usage.json"

// fileMode mirrors internal/session, internal/tuning and internal/incidents:
// owner-only, because per-session token counts reveal who is using the bot.
const fileMode = 0o600

// maxSessions bounds how many per-session entries the ledger keeps. Sessions
// are keyed by an attacker-influenced id (the sender half comes from a chat
// platform), so an unbounded map is a memory-and-file growth primitive; the
// stalest session by LastSeen is dropped when the cap is hit, and its slice
// of spend folds nothing away — the lifetime totals already carry it.
const maxSessions = 256

// Totals aggregates token usage and cost for one scope (the process lifetime,
// one model, or one session). CostUSD carries only the KNOWN-cost share:
// calls whose model is not in the pricing table still count their tokens and
// land in UnknownCostCalls, so a total of zero with UnknownCostCalls > 0
// reads as "cost unknown", never as "nothing spent".
type Totals struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	Calls            int     `json:"calls"`
	CostUSD          float64 `json:"cost_usd"`
	// UnknownCostCalls is the number of calls whose model had no pricing
	// table entry. Kept beside CostUSD so "no cost recorded" is visibly a
	// coverage gap rather than a free run (issue #436).
	UnknownCostCalls int `json:"unknown_cost_calls,omitempty"`
}

// add folds one call's usage into the totals. costKnown=false contributes the
// tokens but leaves the cost untouched. Some providers omit total_tokens on
// the wire; when it reads zero it is derived from the prompt and completion
// counts rather than recorded as zero use.
func (t *Totals) add(u providers.Usage, cost float64, costKnown bool) {
	tokTotal := u.TotalTokens
	if tokTotal == 0 {
		tokTotal = u.PromptTokens + u.CompletionTokens
	}
	t.PromptTokens += u.PromptTokens
	t.CompletionTokens += u.CompletionTokens
	t.TotalTokens += tokTotal
	t.Calls++
	if costKnown {
		t.CostUSD += cost
	} else {
		t.UnknownCostCalls++
	}
}

// SessionTotals is one session's totals plus the freshness stamp the LRU
// eviction compares.
type SessionTotals struct {
	Totals
	LastSeen time.Time `json:"last_seen"`
}

// Ledger records per-model token usage and estimated cost, keyed overall
// (process lifetime) and per session, and persists the whole thing to one
// JSON file so `joshbot status` and the spend ceiling survive a restart.
//
// RecordUsage is safe from concurrent turns: everything happens under one
// mutex. The zero value is not usable; construct with NewLedger.
type Ledger struct {
	mu sync.Mutex

	path      string
	StartedAt time.Time
	Total     Totals
	Models    map[string]Totals
	Sessions  map[string]SessionTotals
}

// ledgerFile is the on-disk shape of a persisted ledger.
type ledgerFile struct {
	Schema    int                      `json:"schema"`
	StartedAt time.Time                `json:"started_at"`
	Total     Totals                   `json:"total"`
	Models    map[string]Totals        `json:"models"`
	Sessions  map[string]SessionTotals `json:"sessions"`
}

// NewLedger loads the ledger at path, or starts a fresh one when the file
// does not exist. The load is deliberately lenient — an unparseable or
// unreadable ledger reads as empty and the next RecordUsage overwrites it —
// because losing running totals is a status/reporting loss, never a reason
// to refuse to run. A nil-ledger caller (the file could not be opened at
// all) is handled by cmd/joshbot the way a failed incident-log open is:
// recording is disabled, the agent still runs.
func NewLedger(path string) (*Ledger, error) {
	l := &Ledger{
		path:      path,
		StartedAt: time.Now(),
		Models:    map[string]Totals{},
		Sessions:  map[string]SessionTotals{},
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return l, err
	}
	var f ledgerFile
	if err := json.Unmarshal(b, &f); err != nil {
		log.Warn("usage: ledger file unreadable, starting fresh", "path", path, "error", err)
		return l, nil
	}
	if !f.StartedAt.IsZero() {
		l.StartedAt = f.StartedAt
	}
	l.Total = f.Total
	if f.Models != nil {
		l.Models = f.Models
	}
	if f.Sessions != nil {
		l.Sessions = f.Sessions
	}
	return l, nil
}

// RecordUsage folds one LLM call into the ledger and persists it. The model
// id must be the one the provider actually answered with (the response's
// Model field), not the one addressed — the pricing lookup differs per entry.
// Persist failures are logged at warn and left out of band: a lost total is
// recoverable noise, while failing every turn over it is not.
func (l *Ledger) RecordUsage(sessionID, model string, u providers.Usage) {
	cost, known := CostOf(model, u)

	l.mu.Lock()
	defer l.mu.Unlock()

	l.Total.add(u, cost, known)
	m := l.Models[model]
	m.add(u, cost, known)
	l.Models[model] = m

	s := l.Sessions[sessionID]
	s.add(u, cost, known)
	s.LastSeen = time.Now()
	l.Sessions[sessionID] = s
	l.evictLocked()

	if err := l.saveLocked(); err != nil {
		log.Warn("usage: failed to persist ledger", "path", l.path, "error", err)
	}
}

// evictLocked drops the stalest session when the cap is hit. Caller holds mu.
func (l *Ledger) evictLocked() {
	if len(l.Sessions) <= maxSessions {
		return
	}
	oldestID := ""
	var oldest time.Time
	for id, s := range l.Sessions {
		if oldestID == "" || s.LastSeen.Before(oldest) {
			oldestID, oldest = id, s.LastSeen
		}
	}
	delete(l.Sessions, oldestID)
}

// Spend answers the agent's budget-ceiling query: known-cost spend for one
// session and for the process lifetime. Unpriced models contribute tokens
// but no cost here, by design — a fabricated number would be worse than none
// (see pricing.go's doc comment).
func (l *Ledger) Spend(sessionID string) (sessionUSD, totalUSD float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Sessions[sessionID].CostUSD, l.Total.CostUSD
}

// ModelUsage is one model's per-model totals for the status report.
type ModelUsage struct {
	Model  string `json:"model"`
	Totals Totals `json:"totals"`
}

// SessionUsage is one session's totals for the status report.
type SessionUsage struct {
	Session string `json:"session"`
	Totals  Totals `json:"totals"`
	// LastSeen orders the list on the report side; it is metadata, not a
	// spend figure, so the JSON form omits it.
	LastSeen time.Time `json:"-"`
}

// Snapshot renders the ledger for `joshbot status`: lifetime totals, the
// per-model breakdown, and the most recently seen sessions.
func (l *Ledger) Snapshot() (total Totals, models []ModelUsage, sessions []SessionUsage) {
	l.mu.Lock()
	defer l.mu.Unlock()

	total = l.Total

	for m, t := range l.Models {
		models = append(models, ModelUsage{Model: m, Totals: t})
	}
	// Sorted by cost descending, then model name, so the heaviest spend leads.
	sort.Slice(models, func(i, j int) bool {
		if models[i].Totals.CostUSD != models[j].Totals.CostUSD {
			return models[i].Totals.CostUSD > models[j].Totals.CostUSD
		}
		return models[i].Model < models[j].Model
	})

	for id, s := range l.Sessions {
		sessions = append(sessions, SessionUsage{Session: id, Totals: s.Totals, LastSeen: s.LastSeen})
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastSeen.After(sessions[j].LastSeen)
	})
	// Capped at five so the status document stays a
	// summary rather than a transcript of every session the process has seen.
	if len(sessions) > 5 {
		sessions = sessions[:5]
	}
	return total, models, sessions
}

// saveLocked writes the ledger atomically at owner-only mode. Caller holds
// mu, writes arrive from RecordUsage only, and the temp+rename keeps a crash
// or a concurrent reader from seeing a half-written file — the same contract
// internal/session's writeFileAtomic carries.
func (l *Ledger) saveLocked() error {
	f := ledgerFile{
		Schema:    1,
		StartedAt: l.StartedAt,
		Total:     l.Total,
		Models:    l.Models,
		Sessions:  l.Sessions,
	}
	b, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("usage: marshal ledger: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return fmt.Errorf("usage: mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".usage-*")
	if err != nil {
		return fmt.Errorf("usage: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("usage: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("usage: close: %w", err)
	}
	if err := os.Chmod(tmpName, fileMode); err != nil {
		return fmt.Errorf("usage: chmod: %w", err)
	}
	if err := os.Rename(tmpName, l.path); err != nil {
		return fmt.Errorf("usage: rename: %w", err)
	}
	return nil
}
