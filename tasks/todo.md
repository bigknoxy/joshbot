# Joshbot: Reliability & Polish Sprint

## Goal
Ship v1.22.0 with three improvements:
1. **Telegram message splitting** — solve 4096-char limit properly with async/multi-command splitting
2. **Better onboarding UX** — improve `./joshbot onboard` wizard
3. **Test coverage to 85%** — add valuable tests across the codebase

## Acceptance Criteria
- Telegram messages >4096 bytes are split into multiple messages and sent sequentially
- Onboarding wizard is intuitive, guides users through config setup with clear prompts
- Test coverage reaches >=85% on `go test -cover ./...` on packages that matter (bus, agent, tools, config, providers, memory, skills, session)
- All existing tests pass, no regressions
- `gofmt -d .` empty, `go vet ./...` clean

<!-- /autoplan restore point: /root/.gstack/projects/joshbot-go/main-autoplan-restore-20260601-011635.md -->
