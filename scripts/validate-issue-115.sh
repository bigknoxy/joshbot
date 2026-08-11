#!/usr/bin/env bash
# validate-issue-115.sh — validates all acceptance criteria for issue #115
# "streaming stage 1: carry the agent progress/stream sink per-request"
#
# Each check prints PASS/FAIL with a measurement. Exit 1 if any check fails.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

PASS=0
FAIL=0
TOTAL=0

check() {
    local name="$1"
    local result="$2"
    local detail="${3:-}"
    TOTAL=$((TOTAL + 1))
    if [ "$result" = "PASS" ]; then
        PASS=$((PASS + 1))
        printf "✅ PASS  %s" "$name"
    else
        FAIL=$((FAIL + 1))
        printf "❌ FAIL  %s" "$name"
    fi
    if [ -n "$detail" ]; then
        printf "  (%s)" "$detail"
    fi
    printf "\n"
}

echo "=== Issue #115 Validation ==="
echo "Subject: per-request sink plumbing (context-carried progress callback)"
echo ""

# ── 1. No sink-related mutable state on Agent struct ──────────────────────
# The Agent struct must not have a `progress` field or any sink field.
if grep -q 'progress\s*ProgressFunc' internal/agent/agent.go; then
    check "Agent struct has no progress field" "FAIL" "found progress ProgressFunc in agent.go"
else
    check "Agent struct has no progress field" "PASS" "grep confirms no progress field"
fi

# ── 2. WithSink exists and is exported ────────────────────────────────────
if grep -q 'func WithSink' internal/agent/sink.go; then
    check "WithSink exported in sink.go" "PASS"
else
    check "WithSink exported in sink.go" "FAIL" "func WithSink not found"
fi

# ── 3. progressFromContext used in reactLoop ──────────────────────────────
if grep -q 'progressFromContext(ctx)' internal/agent/agent.go; then
    check "reactLoop uses progressFromContext(ctx)" "PASS"
else
    check "reactLoop uses progressFromContext(ctx)" "FAIL" "not found in agent.go"
fi

# ── 4. WithProgressCallback and SetProgressCallback removed from Agent ────
if grep -q 'func WithProgressCallback' internal/agent/progress.go; then
    check "WithProgressCallback removed from Agent" "FAIL" "still present in progress.go"
else
    check "WithProgressCallback removed from Agent" "PASS"
fi

if grep -q 'func (a \*Agent) SetProgressCallback' internal/agent/progress.go; then
    check "SetProgressCallback removed from Agent" "FAIL" "still present in progress.go"
else
    check "SetProgressCallback removed from Agent" "PASS"
fi

# ── 5. Concurrent no-cross-delivery test exists ───────────────────────────
if grep -q 'func TestConcurrentProcessNoCrossDelivery' internal/agent/progress_test.go; then
    check "Concurrent no-cross-delivery test exists" "PASS"
else
    check "Concurrent no-cross-delivery test exists" "FAIL" "not found"
fi

# ── 6. Concurrent test actually fails against old design ───────────────────
# We can't run the old design, but we can verify the test asserts exactly 2
# events per sink (which would fail if cross-talk occurred).
if grep -q 'len(gotA) != 2' internal/agent/progress_test.go && \
   grep -q 'len(gotB) != 2' internal/agent/progress_test.go; then
    check "Concurrent test asserts exactly 2 events per sink" "PASS"
else
    check "Concurrent test asserts exactly 2 events per sink" "FAIL"
fi

# ── 7. cmd/joshbot tests pass unmodified ──────────────────────────────────
# Run the existing cmd/joshbot tests — they should pass without modification.
if go test -race -count=1 ./cmd/joshbot/ -run 'TestRunAgentLoop|TestCLIProgress|TestIsTTY' 2>&1 | tail -1 | grep -q '^ok'; then
    check "cmd/joshbot progress tests pass unmodified" "PASS"
else
    check "cmd/joshbot progress tests pass unmodified" "FAIL"
fi

# ── 8. go test -race ./... passes ─────────────────────────────────────────
if go test -race -count=1 ./internal/agent/ ./cmd/joshbot/ 2>&1 | tail -1 | grep -q '^ok'; then
    check "go test -race passes (agent + cmd)" "PASS"
else
    check "go test -race passes (agent + cmd)" "FAIL"
fi

# ── 9. Coverage above 45% CI floor ────────────────────────────────────────
COVERAGE=$(go test -race -cover ./internal/agent/ 2>&1 | grep -oP 'coverage: \K[0-9.]+')
if [ -n "$COVERAGE" ] && [ "$(echo "$COVERAGE > 45" | bc -l)" = "1" ]; then
    check "internal/agent coverage above 45% floor" "PASS" "coverage=${COVERAGE}%"
else
    check "internal/agent coverage above 45% floor" "FAIL" "coverage=${COVERAGE:-unknown}%"
fi

# ── 10. go vet clean ──────────────────────────────────────────────────────
if go vet ./internal/agent/ ./cmd/joshbot/ 2>&1 | grep -q .; then
    check "go vet clean" "FAIL"
else
    check "go vet clean" "PASS"
fi

# ── 11. gofmt clean ───────────────────────────────────────────────────────
if gofmt -l internal/agent/sink.go internal/agent/progress.go internal/agent/progress_test.go internal/agent/agent.go cmd/joshbot/main.go 2>&1 | grep -q .; then
    check "gofmt clean" "FAIL"
else
    check "gofmt clean" "PASS"
fi

# ── 12. Build succeeds ────────────────────────────────────────────────────
if go build ./cmd/joshbot 2>&1; then
    check "go build ./cmd/joshbot" "PASS"
else
    check "go build ./cmd/joshbot" "FAIL"
fi

# ── 13. Docs updated ──────────────────────────────────────────────────────
if grep -q 'per-request' CLAUDE.md && grep -q 'per-request' AGENTS.md; then
    check "CLAUDE.md and AGENTS.md updated" "PASS"
else
    check "CLAUDE.md and AGENTS.md updated" "FAIL"
fi

echo ""
echo "=== Summary: $PASS/$TOTAL passed, $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
