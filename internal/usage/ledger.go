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
// RecordUsage is safe from concurrent turns: the accounting happens under
// one mutex, and the disk write happens outside it. The zero value is not
// usable; construct with NewLedger.
type Ledger struct {
	// mu guards the accounting state — the totals, the maps and the
	// reservations. It is never held across a disk write.
	mu sync.Mutex
	// saveMu serialises the writes themselves. Two concurrent saves would
	// otherwise race, and the older snapshot could be renamed last, losing
	// the newer totals from the file.
	saveMu sync.Mutex

	path      string
	StartedAt time.Time
	Total     Totals
	Models    map[string]Totals
	Sessions  map[string]SessionTotals

	// reserved is the estimated cost held by the calls that are in flight
	// and reservations maps a reservation id to the amount it holds. Both
	// are guarded by mu and neither is persisted: a reservation belongs to
	// a call that is running now, and a restart ends that call.
	reserved     float64
	reservations map[string]float64
	nextRes      uint64

	// persist writes one snapshot to disk. It is a field so a test can
	// hold a write open and observe that the state mutex is not held
	// across it; production writes through writeLedgerFile.
	persist func(ledgerFile) error
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
		path:         path,
		StartedAt:    time.Now(),
		Models:       map[string]Totals{},
		Sessions:     map[string]SessionTotals{},
		reservations: map[string]float64{},
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
	l.record("", sessionID, model, u)
}

// ceilingError is what Reserve returns when the spend ceiling cannot cover
// the call. It carries the figures the caller's reply needs — what was left
// of the ceiling and what the call could have cost — so the reply can
// explain the stop instead of quoting a log line.
type ceilingError struct {
	remaining float64
	bound     float64
}

func (e *ceilingError) Error() string {
	return fmt.Sprintf("usage: spend ceiling reached: $%.2f of the ceiling was left and this call could cost up to $%.2f", e.remaining, e.bound)
}

// Remaining is what was left of the ceiling when the call was refused.
func (e *ceilingError) Remaining() float64 { return e.remaining }

// Bound is what the refused call could have cost at most.
func (e *ceilingError) Bound() float64 { return e.bound }

// Reserve holds the worst case for one call against the spend ceiling and
// returns a reservation id.
//
// The hold and the check that admitted the call happen together under the
// ledger's lock: a turn spends nothing until its call is paid for, so a
// burst of concurrent turns cannot each pass a check that none of them has
// paid for yet. The call is admitted only when its bound fits in what is
// left of the ceiling — the recorded spend plus what the calls that are
// already in flight hold — so the recorded spend cannot pass the ceiling
// while calls are running.
//
// The reservation is settled by Commit (the call returned, and its actual
// cost replaces the bound) or by Release (the call failed and spent
// nothing).
func (l *Ledger) Reserve(model string, ceilingUSD float64, promptTokens, maxCompletionTokens int) (string, error) {
	bound := callCostBound(model, promptTokens, maxCompletionTokens)

	l.mu.Lock()
	defer l.mu.Unlock()

	if ceilingUSD > 0 {
		held := l.Total.CostUSD + l.reserved
		remaining := ceilingUSD - held
		if remaining < 0 {
			remaining = 0
		}
		if remaining <= 0 || held+bound > ceilingUSD {
			return "", &ceilingError{remaining: remaining, bound: bound}
		}
	}

	l.nextRes++
	id := fmt.Sprintf("r%d", l.nextRes)
	if l.reservations == nil {
		l.reservations = map[string]float64{}
	}
	l.reservations[id] = bound
	l.reserved += bound
	return id, nil
}

// Commit records one call's actual usage against the reservation it was
// admitted with: the bound the reservation held is released and the actual
// cost takes its place.
func (l *Ledger) Commit(reservationID, sessionID, model string, u providers.Usage) {
	l.record(reservationID, sessionID, model, u)
}

// Release drops a reservation whose call spent nothing — the call failed
// before the provider billed it — so the budget it held returns to the
// ceiling instead of being lost.
func (l *Ledger) Release(reservationID string) {
	l.mu.Lock()
	if held, ok := l.reservations[reservationID]; ok {
		delete(l.reservations, reservationID)
		l.reserved -= held
	}
	l.mu.Unlock()
}

// record folds one call into the totals and persists the ledger. The
// reservation, when there is one, is released and the call's actual cost
// takes its place.
func (l *Ledger) record(reservationID, sessionID, model string, u providers.Usage) {
	cost, known := CostOf(model, u)

	l.mu.Lock()
	if held, ok := l.reservations[reservationID]; ok {
		delete(l.reservations, reservationID)
		l.reserved -= held
	}
	l.Total.add(u, cost, known)
	m := l.Models[model]
	m.add(u, cost, known)
	l.Models[model] = m

	s := l.Sessions[sessionID]
	s.add(u, cost, known)
	s.LastSeen = time.Now()
	l.Sessions[sessionID] = s
	l.evictLocked()
	l.mu.Unlock()

	l.save()
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

// save persists the ledger. The state mutex is not held across the write:
// the write is the slow part and every turn pays for it, so holding the
// lock across it would serialise the whole agent behind the slowest disk
// on the machine. saveMu serialises the writes themselves, so two
// concurrent saves cannot publish the older snapshot last.
func (l *Ledger) save() {
	l.saveMu.Lock()
	defer l.saveMu.Unlock()

	l.mu.Lock()
	f := l.fileLocked()
	l.mu.Unlock()

	persist := l.persist
	if persist == nil {
		persist = func(f ledgerFile) error { return writeLedgerFile(l.path, f) }
	}
	if err := persist(f); err != nil {
		log.Warn("usage: failed to persist ledger", "path", l.path, "error", err)
	}
}

// fileLocked renders the on-disk shape. Caller holds mu.
func (l *Ledger) fileLocked() ledgerFile {
	// The maps are copied, not shared: the snapshot is marshalled outside
	// the state mutex, and the marshal would otherwise walk these maps
	// while the next turn's record mutates them — a race the race detector
	// catches under concurrent turns, and a torn read without it.
	models := make(map[string]Totals, len(l.Models))
	for k, v := range l.Models {
		models[k] = v
	}
	sessions := make(map[string]SessionTotals, len(l.Sessions))
	for k, v := range l.Sessions {
		sessions[k] = v
	}
	return ledgerFile{
		Schema:    1,
		StartedAt: l.StartedAt,
		Total:     l.Total,
		Models:    models,
		Sessions:  sessions,
	}
}

// writeLedgerFile writes the ledger atomically at owner-only mode. The
// temp+rename keeps a crash or a concurrent reader from seeing a
// half-written file — the same contract internal/session's writeFileAtomic
// carries.
func writeLedgerFile(path string, f ledgerFile) error {
	b, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("usage: marshal ledger: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("usage: mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".usage-*")
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
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("usage: rename: %w", err)
	}
	return nil
}
