# Tasks

## Goal: Fix model config bug + abstract configure with CLI flags + tests

### Acceptance Criteria
- `joshbot config --provider nvidia --api-key xxx --model foo` sets the config correctly
- Interactive `joshbot config` wizard produces identical results
- `joshbot agent` picks up the configured model, not the registry default
- All paths have unit test coverage

### Bugs to Fix
1. `setDefaultProvider` (main.go:2901) overwrites model with registry default
2. `configureProvider` auto-default (main.go:2683) doesn't set model
3. NVIDIA registration (main.go:369-381) doesn't pass `p.Model`

### Implementation Plan

**Phase 1: Fix Bugs (targeted)**
- main.go:2901 — prefer per-provider model over registry default
- main.go:2683-2684 — also set model when auto-defaulting
- main.go:369-381 — pass p.Model to NVIDIA registration

**Phase 2: Create `internal/configure/` package**
- `configurator.go` — Configurator type with non-interactive API
- `configure_test.go` — comprehensive tests

**Phase 3: Wire CLI flags in main.go**
- `--provider`, `--api-key`, `--api-base`, `--model`, `--set-default`, `--remove` flags
- `runConfigure` delegates to configure package when flags are set
- Interactive wizard delegates to same package

**Phase 4: End-to-end verification**
- Build + test
- Manual smoke test
