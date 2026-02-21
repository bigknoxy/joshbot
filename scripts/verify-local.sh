#!/usr/bin/env bash
# Quick verification script for local development
# - builds the binary
# - runs onboard to create workspace and memory files
# - runs tests
# - starts the agent briefly and checks that files exist

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT_DIR"

echo "1) Run go tests (fast subset)"
go test ./internal/memory ./internal/skills ./internal/subagent ./internal/cron ./internal/context ./internal/learning ./internal/integration -v

echo "2) Build joshbot CLI"
mkdir -p ./bin
go build -o ./bin/joshbot ./cmd/joshbot

WORKSPACE="$ROOT_DIR/.verify-workspace"
# Use correct env var format for nested config (double underscore for nesting)
export JOSHBOT_AGENTS__DEFAULTS__WORKSPACE="$WORKSPACE"

echo "3) Onboard with --force (creates workspace and memory files)"
# Use --force to run non-interactively, backing up any existing installation
./bin/joshbot onboard --force

echo "Workspace: $WORKSPACE"
echo "Listing memory dir:"
ls -la "$WORKSPACE" || true
ls -la "$WORKSPACE/memory" || true

echo "4) Start agent in background for 6 seconds to exercise startup hooks"
./bin/joshbot agent > /tmp/joshbot-verify.log 2>&1 &
AGENT_PID=$!
echo "Agent PID: $AGENT_PID"
sleep 6

echo "Agent log (last 50 lines):"
tail -n 50 /tmp/joshbot-verify.log || true

echo "5) Create HEARTBEAT.md with a task and wait 6 seconds"
    cat > "$WORKSPACE/HEARTBEAT.md" <<'EOF'
- [ ] Verify heartbeat task
EOF
sleep 6

echo "6) Kill agent"
kill $AGENT_PID || true
wait $AGENT_PID 2>/dev/null || true

echo "7) Check memory and history files"
if [ -f "$WORKSPACE/memory/MEMORY.md" ]; then
  echo "MEMORY.md exists"
else
  echo "MEMORY.md missing"; exit 1
fi

if [ -f "$WORKSPACE/memory/HISTORY.md" ]; then
  echo "HISTORY.md exists"
else
  echo "HISTORY.md missing"; exit 1
fi

echo "Verification complete. Review /tmp/joshbot-verify.log for agent startup details."
