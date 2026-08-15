# Lessons Learned

## 2026-05-17: CI failed on gofmt formatting after v1.19.0 release

**Failure mode**: Pushed commit `1822402` (feat: intelligent memory system and skill self-creation) to main. CI run #108 failed on the "Check formatting" step. Release workflow (v1.19.0 tag) also failed on the test job.

**Detection signal**: CI `gofmt -l .` returned `internal/learning/learning.go` which had vertical-alignment padding in a struct literal that `gofmt` rejects.

**Root cause**: The code-simplifier subagent restructured `internal/learning/learning.go` and introduced non-standard padding on a struct literal field:
```go
Model:       c.provider.Config().Model,  // BAD: extra spaces
Model: c.provider.Config().Model,        // CORRECT: gofmt standard
```

**Contributing factors**:
1. No `gofmt` check in the local verification step (only `go build` + `go test -race`)
2. The release tag was pushed *before* CI on the main push completed — tag pushed at commit `1822402`, CI failed ~30s later
3. The release workflow (`.github/workflows/release.yml`) runs tests independently from CI but uses the same commit, so it also fails

**Prevention rule**: Run `gofmt -d .` (check only, exit non-zero if diffs exist) as part of pre-commit or pre-push verification. Add to AGENTS.md checklist.

**Timeline**:
- 19:26: Pushed commit to main + v1.19.0 tag
- 19:27: CI run #108 failed on "Check formatting"
- 19:29: Release run #5 failed on "Test" job (depended on same commit)
- 19:31: Root cause identified (gofmt in learning.go)
- 19:32: Fixed with `gofmt -w internal/learning/learning.go`, pushed fix commit

## 2026-06-03: conversation_summary leak — triple-layer defense required, pink elephant on system prompt

**Failure mode**: `<conversation_summary>` XML tag leaked into LLM responses (e.g. "I see you've sent me a `<conversation_summary>`"), causing confusing user-facing output.

**Root cause**: Three simultaneous failures:
1. Missing `</conversation_summary>` closing tag — LLM saw unclosed XML and tried to "help" close it
2. No output sanitization — LLM response passed verbatim to user
3. System prompt taught LLM about the tag (pink elephant) — backfired, LLM hallucinated seeing it

**Fix**: `internal/agent/agent.go`: Added `</conversation_summary>` closing tags in both `checkAndCompactContext()` and `buildMessages()`. Added `sanitizeResponse()` output filter. `internal/agent/context.go`: Removed system prompt instructions mentioning the tag (v1.28.1). Tests: `TestSanitizeResponse` (7 subtests), `TestSanitizeResponse_LeakPrevention`, `TestSystemPromptNoConversationSummaryReference`.

**Prevention rule**: Never tell the LLM about internal context tags in system prompts — it primes the LLM to think about them (pink elephant). Use structural fixes (proper XML closing) + output sanitization instead.

## 2026-06-03: Pages deploy CI — gh-pages must exist, use peaceiris action

**Failure mode**: Initial CI auto-deploy used `actions/upload-pages-artifact` + `actions/deploy-pages` but `gh-pages` branch didn't exist yet, causing workflow failure.

**Root cause**: `actions/deploy-pages` requires the `gh-pages` branch to already exist with Pages enabled in repo settings. First deploy must either: (a) manually enable Pages with a branch, or (b) use `peaceiris/actions-gh-pages` which creates the branch.

**Fix**: Switched to `peaceiris/actions-gh-pages@v4` which creates the `gh-pages` branch automatically and handles force-push. Simpler: no artifact upload step needed — just deploy from `./site` directly.

## Commit workflow expectations
- CI must pass for ALL commits on main before cutting a release tag
- Release tags must only be pushed AFTER CI on the corresponding main commit is green
- Never push a tag and main commit simultaneously — wait for CI confirmation first

## 2026-08-15 dogfooding session
- **Failure mode**: pointed joshbot at a sandbox with `JOSHBOT_HOME` and it silently used `~/.joshbot` instead — the home lever is `HOME` (`DefaultHome = $HOME/.joshbot`, config.go:106). `JOSHBOT_*` vars are only an env-override namespace for config keys, never for the home dir. **Detection**: model read paths under `/root/.joshbot/workspace` and logs showed `cron_jobs_file=~/.joshbot/workspace`. **Prevention**: to sandbox joshbot, set `HOME`; verify with the "Background services started cron_jobs_file=..." log line.
- **Failure mode**: `grep -o 'sk-or-[A-Za-z0-9]*'` truncated the key at the first hyphen, and a `GET /models` 200 was read as proof the key was valid. **Detection**: POST /chat/completions 401 "User not found". **Prevention**: extract keys with `sk-or-v1-[A-Za-z0-9_-]+`; probe keys with a chat completion, never `/models` (OpenRouter's /models is unauthenticated — the AGENTS.md gotcha, hit for real).
- **Failure mode**: used the dead OpenRouter key to dogfood the production model and burned two turns on 401s. **Prevention**: check which key a config will actually use (`joshbot preflight` / `configure --list`) before a repro that needs the network.
- **Verification**: streaming bugs do NOT reproduce via `agent -m` (no TTY ⇒ no stream sink). Always allocate a pty with `script -qec` when the symptom is a streaming/delivery issue.
