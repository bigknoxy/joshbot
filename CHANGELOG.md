# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### ⚠️ Breaking
- **Telegram: an empty `allow_from` now denies every sender** (previously it allowed everyone). A bot with no allowlist handed anyone on the internet a direct line into an agent loop holding the shell tool. `IsAllowed` now fails closed on an empty allowlist and `Start` logs a loud warning naming the exact key to set (`channels.telegram.allow_from`, or the comma-separated `JOSHBOT_CHANNELS__TELEGRAM__ALLOW_FROM` env override; Discord has the matching `channels.discord.allow_from` / `JOSHBOT_CHANNELS__DISCORD__ALLOW_FROM`). **If you relied on an open bot, add your numeric Telegram user ID to `allow_from` or it will reject all messages.** The new Discord channel enforces the same fail-closed rule.

- **The default shell allowlist no longer includes `find`, `go` or `git`**, and while any allowlist is in force a command containing a shell construct that can introduce a second command word (`;`, `&`, `|`, newline, `` ` ``, `$(`, `<(`, `>(`) is refused. The default list only applies on platforms with no sandbox (`tools.shell_allow_list` unset, no Landlock/Seatbelt), where it is the *only* boundary — and each of those three launches a program of the caller's choosing (`find -exec sh -c`, `go run`, `git -c core.pager=...`), while `echo hi; id` passed a first-word-only match. **If you relied on them, name them explicitly in `tools.shell_allow_list`, and run one command per call.**

- **The default shell allowlist also drops `rg` and `sort`, and redirection (`>`, `>>`, `2>`, `&>`, `<`, `<>`) is refused while an allowlist is in force.** Both binaries run a program named by a flag — `rg --pre /bin/sh` is invoked once per file and `sort --compress-program=/bin/sh` is exec'd when the sort spills to disk — so an allowed first word was a route to `/bin/sh` on exactly the platforms where the list is the only boundary. Redirection was missing from the separator screen and the command reaches `sh -c` unchanged, so `echo PWNED > /outside/escaped.txt` passed both the allowlist and the deny list and wrote the file; `>>` against `~/.ssh/authorized_keys` is the same call with two characters changed. Carriage return (`\r`) and `$'…'` ANSI-C quoting are refused too. **If you relied on `rg`, `sort`, or shell redirection, name the binaries explicitly in `tools.shell_allow_list` and write output with the `filesystem` tool instead.**

### Added
- **Human approval gate for shell commands** (#130) — `tools.shell_approval` (`"off"` default, `"interactive"`, `"always"`) prompts before a shell command runs, showing the full command line and working directory. `"interactive"` offers `[a]ll for this session`; `"always"` never remembers an answer. The approver rides the request context rather than the tool struct, so concurrent Telegram turns cannot be handed each other's prompts — and a request carrying no approver gets `DenyAll`. That is what makes it safe to leave on: cron, the heartbeat scanner, the gateway and piped `agent -m` runs have no human to ask, so their shell commands are refused immediately rather than hanging a background goroutine or silently auto-approving. Only an explicit `y` approves; EOF, a context deadline and Ctrl-C are denials, `async=true` is gated identically, and an unrecognised config value is a startup error rather than a silent `"off"`. The gate runs after deny-list and allowlist screening so a command that was going to be refused anyway never produces a prompt.
- **`api_key_env` and `joshbot preflight`** (#128) — a provider or model entry may name an environment variable (`"api_key_env": "MY_OPENROUTER_KEY"`) instead of carrying the secret, so a config file that is backed up, synced or pasted into an issue holds a variable name. Precedence is `JOSHBOT_PROVIDERS__<NAME>__API_KEY` > `api_key_env` > `api_key`; setting both fields on one entry, or naming a variable that is not set, is a fatal load error rather than a silent downgrade to a config nothing can dial. The new `joshbot preflight` resolves the config the way the agent does — same `ResolveModelConfig`, same prefix rules — and reports provider, the exact model ID sent on the wire, the API host, and where the credential came from, without contacting any provider and without printing the credential. It exits non-zero when joshbot would not start, and unlike every other command it does not fall back to defaults on an unusable config (new `config.LoadStrict`), because a report about a config you never wrote is the opposite of a diagnosis.
- **MCP servers are now actually started** — `cmd/joshbot` connects the servers declared in `config.json` during component setup and registers their tools; the spawned processes are reaped on exit. Previously the subsystem existed but nothing wired it in, so a release build spawned no server no matter what was configured. Startup is fail-soft: a server that will not start is logged and skipped rather than aborting joshbot.
- **Agent-friendly CLI parity** (#144, #148, #149) — `agent` gained `--output-format text|json|stream-json` (JSON modes are non-interactive and require `-m`; stdout carries only the result document, logs go to stderr), `--resume`/`--session` to thread a prior session by id, and a stable exit-code contract (`0` ok, `1` general, `2` auth/no-provider, `3` validation, `4` confirmation-reserved) with machine-readable JSON errors on stderr. New global `--no-color` and `--log-level debug|info|warn|error` flags.
- **Non-interactive onboarding** (#142) — `onboard` gained `--provider`, `--api-key` and `--api-base`; the API key also falls back to `JOSHBOT_PROVIDERS__<PROVIDER>__API_KEY`.
- **Discord channel** (#159) — implements the existing `Channel` interface with `/help` and `/new`, 2000-char code-fence-aware splitting, typing keep-alive, and the same fail-closed allowlist as Telegram.
- **MCP client** (#155) — stdio-transport Model Context Protocol servers configurable in `config.json`; discovered tools register into the normal tool registry, namespaced `mcp__<server>__<tool>` so an MCP server cannot shadow a built-in tool.
- **macOS Seatbelt shell sandbox** (#150) — `tools.shell_sandbox: "workspace"` now confines shell commands on macOS via `sandbox-exec`, mirroring the Linux Landlock design. On platforms with no sandbox, the shell tool defaults to allowlist-only.
- **`heartbeat.interval` config key** (#141) — the scanner interval is configurable instead of hardcoded.
- **Documented configuration precedence** (#148) — README now states the order (defaults < config file < `JOSHBOT_*` env < command flags) and records that there is deliberately no project-scoped config file, so an `agent -m` run cannot change provider or workspace based on the directory it was invoked from.

### Changed
- **`allow_from` env overrides** — `JOSHBOT_CHANNELS__TELEGRAM__ALLOW_FROM` and `JOSHBOT_CHANNELS__DISCORD__ALLOW_FROM` accept a comma-separated list (blank entries dropped), so the key the fail-closed startup warning names can actually be set from the environment.
- **`providers.ListModels` no longer defaults to OpenRouter when `api_base` is empty** — it errors instead. The old fallback made credential validation for any other or unknown provider dial openrouter.ai and print "✓ validated"; `configure.ValidateProviderCredentials` now reports "could not verify ... no API base URL configured" for providers with no fixed endpoint (azure, custom, litellm).
- **`joshbot agent` exits non-zero when the turn fails** — the agent reports LLM failures in band as reply text (`Error processing request: ...`) so chat channels can show them; the CLI now translates that into exit code `1`, with `"is_error": true` in the JSON result document and a `{"type":"error",...}` document on stderr.
- **`onboard --provider` validates the provider name** and `--force --provider <name>` picks that provider's default model (e.g. `llama3.1:8b` for ollama) instead of the OpenRouter default. Interactive `onboard` also exits non-zero when no provider ended up configured.
- **Skill trust is now bound to the whole skill directory tree**, not just `SKILL.md` — the SHA-256 digest folds in every regular file's relative path and content, so editing, adding or removing any file in a trusted skill dir (including a sibling script) revokes trust. A legacy `SKILL.md`-only digest safely revokes rather than crashing.

### Fixed
- **`onboard --config` rewrote the real `~/.joshbot`** (#97) — `runOnboard` read `config.DefaultHome` before anything applied the flag, so `--config /tmp/trial/config.json --force` wrote the new config where asked but inspected, backed up and recreated the default home. Pointing `--config` at a scratch file to try something out disturbed the live install. The flag is now applied first, via `config.UseConfigFile`, which anchors the home without requiring the file to exist already (`LoadFrom` still insists on one, since a silent fallback to defaults would tell a reader their file had been loaded when it had not).
- **Everything under `~/.joshbot/` was a world-listable directory** — `Config.EnsureDirs` created the whole tree (`sessions/`, `media/`, `cron/`, `workspace/` and the home directory itself) at `0755`. Because onboarding creates them first and `MkdirAll` leaves an existing directory's mode alone, that silently overrode the `0750` `session.NewManager` asks for: session *files* were `0600` while the directory listing them was readable by every local account. A session file is named `<channel>:<senderID>`, so the transcripts were unreadable but the identity of everyone talking to the bot was not. All of these directories are now `0700`, and the mode is re-applied on every start so an existing install is corrected rather than left as it was. Found by dogfooding a fresh v1.45.3 install. Three creators outside `EnsureDirs` were missed on the first pass and are fixed too: `~/.joshbot/logs` (created at `0755` by the gateway cron entry and by the launchd service, and absent from `EnsureDirs` entirely, so nothing re-applied a mode to it) and the onboarding `workspace/memory`. `logs/` is now in the `EnsureDirs` list, which is what the regression test pins — a walk over the tree cannot fail on a directory that does not exist yet.
- **`joshbot configure` printed the operator's full home directory path** — `internal/config` carries its own fallback logger that writes straight to the standard library's `log` package, so it never passed through the redacting writer `internal/log` installs, and it is the logger in force before joshbot has configured its own. `configure` therefore printed `/home/<account>/.joshbot/config.json` while every other command printed `~`. That logger now redacts, which covers credentials too — this is the package that handles config values.
- **Workspace escape and credential read via a swapped parent directory** — `O_NOFOLLOW` constrains only the *last* component of a path, so the `filesystem` tool's write path (`os.MkdirAll` then open with `O_NOFOLLOW`) could be redirected by replacing an intermediate directory with a symlink between the containment check and the open: `ws/sub` repointed at `/outside` put the write at `/outside/file.txt` with no error. The read paths — `read_file`, the pre-read inside `edit_file`, and `grep`'s per-file read — used plain `os.ReadFile` and had no `O_NOFOLLOW` at all, so swapping the leaf for a symlink to `~/.ssh/id_rsa` was enough to exfiltrate it. Every path the tool opens now goes through one helper (`internal/tools/openat.go`) that holds the containment root open as a descriptor and resolves each component below it with `openat(2)` via Go 1.24's `os.Root`, so a component that has become a symlink out of the root fails instead of being followed. `mkdirAllIn` replaces `os.MkdirAll` for the same reason.
- **`resolveSymlinks` swallowed non-ENOENT `Lstat` failures** — a component it could not stat (EACCES, EIO) was treated as a plain name and re-appended lexically. It now fails closed on anything other than "does not exist".
- **`onboard --force </dev/null` reported success while configuring nothing** (#142) — the prior guard checked `len(cfg.Providers)==0`, but `Defaults()` seeds a stub `openrouter{}` entry, so it was never true. Replaced with a real check that only passes when a genuine credential (or a keyless local provider) was wired; `--force` with no way to configure a provider now returns a non-zero exit and an actionable message.
- **Debug stderr leak in the gateway bus handler** (#139) — a raw `fmt.Fprintf(os.Stderr, "!!! BUS HANDLER INVOKED …")` is now a level-gated `log.Debug`, so nothing leaks at the default log level.
- **Incomplete provider coverage in the guided config path** (#160) — `SupportedProviders()` now lists all 11 providers and drives `ListProviders`, default API bases, and display names from the registry; onboarding validates credentials after save.
- **Discord allowlist auth bypass** — `allow_from` entries are now partitioned by shape: an all-digits entry matches only the numeric user ID, a non-numeric entry only the username or global display name. Matching every entry against every field let a stranger set their free-form global display name to the operator's snowflake and be admitted.
- **A failed MCP handshake wedged the client permanently** — the read-loop done channel was owned by the `Client` rather than by the process, so the failed start's read loop closed it and a later successful `Connect` answered every call with "server stopped" while reporting healthy. The channel is now allocated per process.
- **A hostile MCP server could hang every MCP call** — a server answering the same JSON-RPC id twice filled the waiting caller's buffer and blocked the read loop for good. `dispatch` now claims the pending entry before replying and sends without blocking.
- **The Discord channel could not be restarted, and a second `Stop` panicked** — `Stop` closed `stopCh` and `Start` never replaced it, so a restarted channel delivered nothing and aborted every send retry, and the next `Stop` was a close of a closed channel. `Start` now allocates a fresh stop signal and `Stop` is idempotent.
- **Discord classified send failures with Telegram's rules** — Discord REST errors that can never succeed (50007 cannot-send-to-user, 50001 missing access, 10003 unknown channel, …) were retried through the full backoff inside the single outbound goroutine, and unclassified errors logged as `telegram:`. Discord now has its own classifier.
- **MCP child processes no longer inherit joshbot's environment** — they get the same allowlisted, credential-screened environment as shell children (`internal/childenv`), so an MCP server cannot read provider API keys out of `env`.
- **Unbounded MCP server output** — a tool result is capped at 4,000 characters and a single server message over 4 MiB closes the connection, so a server cannot exhaust the agent's context or joshbot's heap.
- **Skill trust digest now covers symlinks** — folded in by name and raw target, never dereferenced, so adding, repointing or removing a symlink inside an approved skill revokes trust.
- **Workspace escape via a symlink to a nonexistent target** — `resolveSymlinks` resolved the nearest existing ancestor but re-appended the remaining components lexically, and `EvalSymlinks` reports a dangling symlink as ENOENT exactly like a missing file. `ws/evil -> /outside/x` therefore passed the containment check and was then written. Each re-appended component is now resolved in turn, and filesystem writes go through `O_NOFOLLOW` so the residual check-then-open TOCTOU window costs an `ELOOP` instead of a file outside the workspace.
- **Telegram allowlist authentication bypass** — `allow_from` entries are now partitioned by shape, the same rule Discord uses: an all-digits entry matches only the numeric user ID, a non-numeric entry only the username or full name. Matching every entry against every field let a stranger set their free-form Telegram first name to the operator's numeric user ID and be admitted.
- **MCP client lifecycle** — the done channel is now allocated per connected process rather than per client, so a `Connect` after a failed handshake no longer reports "server stopped" on every subsequent call forever; `call` snapshots that channel so a concurrent reconnect cannot make it wait on the wrong process; `readErr` is read and written under the mutex; and a response is claimed out of the pending map with a non-blocking send, so a server answering the same id twice cannot wedge the read loop (and with it every other in-flight call).
- **Discord channel could not be restarted, and `Stop` twice panicked the process** — `stopCh` now belongs to one Start/Stop cycle instead of to the channel, so a restarted channel is not left with a permanently-closed stop signal that made `consumeOutbound` return immediately and every `Send` abort its retries; the close is latched so a double `Stop` is a no-op.
- **Discord retry classifier** — outbound send failures are classified against Discord's own error vocabulary (`discordgo.RESTError` codes, 429 vs other 4xx) instead of Telegram's Bot API description strings, which never match. A permanent failure like Missing Permissions no longer burns the full backoff inside the single outbound goroutine, delaying every message queued behind it.
- **Message bus context race on restart** — the inbound goroutine and handler dispatch now carry the context their run started with instead of reading the `mb.ctx` field, which a later `Start` reassigns while the previous run's un-joined goroutines are still reading it.
- **Heartbeat published tasks it could never check off** — publishing and checking used two different regexes, so a line like `-[ ] task` or `* [ ]task` re-fired on every tick forever. There is now a single pattern used for both. A task is also only checked off once the bus has actually accepted it (a dropped `Send` used to lose the task silently), and `HEARTBEAT.md` is rewritten atomically through a uniquely-named temp file so an edit made during a tick is not half-clobbered.
- **macOS Seatbelt profile granted unrestricted `mach-lookup`** — it is now an allowlist of named services. A blanket grant reaches XPC services that run outside the profile and will act on the sandboxed process's behalf, undercutting the deny-by-default file and network rules. `(allow process*)` is likewise narrowed to `process-exec`/`process-fork`/`process-info*`.
- **`go build` failed under the macOS sandbox** — `GOCACHE` is usually unset on macOS (the toolchain defaults it to `~/Library/Caches/go-build`, which is not under `~/.cache`), so nothing granted the build cache. It is now resolved via `go env GOCACHE` with that documented default as the fallback.
- **A sandbox workspace that did not exist yet was silently dropped from the profile**, leaving no write grant and failing every command with an opaque "Operation not permitted". The workspace and its scratch dir are created before the profile is rendered, and an unwritable location is reported as such.
- **CI per-package coverage floors were not enforcing anything** — the `cmd/joshbot` floor sat at 15% while the package measured ~40%, so any amount of its test suite could be deleted with the build still green. Floors are re-measured and raised to just under current coverage, and documented as ratcheting upward.
- Security and stability sweep across bus, channels, tools and skills (committed as `4f55479`; issues #138, #140, #143, #147, #151, #153, #157).

### Removed
- **Repo debris deleted: `pkg/`, `pyproject.toml`, `tests/test_markdown.py`, `tests/test_outbound_messages.py`.** `pkg/` was an abandoned parallel refactor holding duplicate copies of `internal/bus` and `internal/channels` that nothing imported — but because it sat under `pkg/`, it was joshbot's public `pkg.go.dev` surface, so the first Go API a visitor saw was dead code frozen mid-refactor. The `pyproject.toml` and the two Python tests were left over from joshbot's pre-Go era and made the repo read as a half-migrated project. `tests/integration_test.go` is live Go (package `integration`) and stays.

## [1.46.0] - 2026-08-10

### Added
- **Telegram slash commands for `/model`, `/personality`, `/compact` and `/status`** — these commands now run through the agent and are registered in the Telegram command menu (alongside `/start`, `/new`, `/help`), instead of being rejected as unknown. `/model <name>` switches the model for the current session and persists it; `/model <name> --global` writes the default to `config.json` for all sessions. `/personality <name>` sets a named personality (`concise`, `technical`, `pirate`, `cheerful`, `formal`), any custom instruction, or `none` to clear it. `/compact` summarizes older context on demand. `/status` reports the effective model, tool count, memory window and max iterations.
- **Per-session model and personality persistence** — `Session` now carries `ModelOverride` and `Personality`, stored in the session metadata sidecar, so a model or personality chosen mid-conversation survives a restart. `/new` clears both.
- **Interactive CLI line editor** — when `joshbot agent` runs with a real TTY on both stdin and stdout, the `> ` prompt is replaced by a raw-mode line editor with Tab slash-command completion, Up/Down history (or multiline cursor movement), Home/End/Delete editing, Alt+Enter multiline input, Ctrl+C/Ctrl+D to quit, and a prompt that shows the session's current model.

### Changed
- **`handleCommand` dispatches on the command word** — `/model fast` and friends are no longer mis-parsed as unknown commands because the whole line was treated as the command name.
- **`/new` on Telegram now applies the same allowlist gate as the forwarded commands** — it is dispatched outside `handleMessage`, so an unallowed caller could previously trigger a session reset.
- **Clearing a session's model override or personality now removes the metadata sidecar** — `/personality none` and `/model ... --global` (which clears the session override) previously left a stale sidecar that re-injected the cleared value on the next message after a restart.
- **`/model` and `/personality` now fail loudly (return an error) if the session cannot be saved**, instead of reporting success for a change that would be lost on the next message.

### Fixed
- **CLI `/status` and the model/personality/compact commands are now session-aware** — `/status` reports the effective model for the current session rather than the config default.

## [1.45.5] - 2026-08-10

### Fixed
- **`joshbot onboard` could silently drop a working provider on a keep-current reconfigure** — reconfiguring an existing install and pressing Enter at the API key prompt ("or press Enter to keep current") returned an empty key, and `runOnboard` read that as "no provider configured": it skipped the provider-config block entirely, so the config was saved with only the disabled `openrouter` default and the chosen provider (e.g. NVIDIA) was gone. `joshbot agent` then died with "no providers enabled: 1 provider(s) found in config (openrouter)". Pressing Enter with an existing key now preserves that key, matching the Telegram-token contract and the `--force` path.

## [1.45.4] - 2026-08-09

### Fixed
- **`joshbot onboard` could silently disconnect a working Telegram bot on a transient network failure** — a valid token, one TLS handshake timeout against `api.telegram.org`, and the old code abandoned the token entry: `setupTelegram` returned `nil`, the config was saved with Telegram **disabled**, and the service was installed on top of it, so the bot that "used to work" went silent with no warning. Token validation now retries transient connectivity failures (dial errors, TLS handshake timeouts, timeouts, and connection resets while reading the response body) up to three times inside `ValidateToken`, gives the user one more chance to re-enter the token, and on persistent failure **preserves the existing working token** when there is one — or leaves Telegram cleanly disabled on a fresh install — instead of dropping the whole configuration. Aborting a token *change* with `cancel` or an empty input likewise keeps the existing token rather than saving Telegram as disabled. The failure message now tells you whether the problem was reaching the API (network) or the token being rejected.
- **`ValidateToken` printed the bot token into the error message** — `http.Get` wraps transport failures in a `*url.Error` whose string form is `Get "https://api.telegram.org/bot<token>/getMe": ...`, and onboard printed that verbatim, so the setup output a user pastes into an issue carried the credential. The transport cause is now unwrapped before the error leaves `internal/channels`. A malformed token is also rejected **offline** by a format check before any request is made, and the getMe call now runs on a client with a real timeout instead of relying on the TLS layer's.
- **`onboard --force` was documented as non-interactive but blocked on stdin** — the force path called `promptProviderAPIKey`, which reads a line: with a terminal attached `onboard --force` hung forever waiting for input, and with stdin closed it silently saved a config with **no provider configured at all**. `--force` now reuses the configured API key from the existing config without reading stdin, so the documented "backup + defaults, no prompts" behaviour is actually true.
- **Comma-separated Telegram usernames with spaces were truncated** — `fmt.Scanln` reads one space-delimited token, so entering `@alice, bob` at the "Usernames" prompt kept only `@alice`. The prompt now reads a full line, so both usernames are kept.
- **`joshbot uninstall` refused to run for any install path containing `/tmp/`** — the same guard `joshbot update` needed in 1.45.1, in a second call site and in the shared `detectRunningContext`. All three now go through one `runningFromGoRun` helper that matches the go-build cache and nothing else, so an installation under `/tmp` can uninstall itself and the "running from source with `go run`" message is only shown when that is actually true. `uninstall` also exits non-zero when it refuses, rather than reporting success.
- **`joshbot uninstall` printed full home directory paths** — six lines showing what would be and had been removed. They now print `~/...`, matching `onboard` and `status`.
- **`joshbot service status` printed a bare `Status:`** — a service that was never installed reports an empty status string, which told the operator nothing. It now says "not installed".

## [1.45.3] - 2026-08-09

### Fixed
- **The installer failed silently when `--bin-dir` named a directory that did not exist** — it reported a successful download and checksum, then died on a bare `mv: No such file or directory` with nothing else printed. `--bin-dir` now creates the directory, including nested paths. Several other failure modes were mute or misleading for the same underlying reason: `--bin-dir` with no value exited on `shift 2` printing nothing at all, a read-only install directory passed the pre-flight check (which accepted a writable *parent*) and failed later at the `mv`, and a version with no matching build printed four raw URLs. Each now names what went wrong and what to do about it, and a single `EXIT` trap reports anything undiagnosed — previously `download_binary` installed its own cleanup trap, which silently replaced the error handler.

### Changed
- **The installer refuses to install an unverified binary** — a checksum *mismatch* already aborted, but missing or unfetchable checksums printed "No checksums available" and carried on installing. Verification being unavailable now fails closed, since every joshbot release publishes `checksums.txt` and its absence means the download path is not what it should be. `JOSHBOT_SKIP_CHECKSUM=1` overrides it deliberately. `scripts/test-install.sh` now exercises all of this against the real release — 16 checks covering directory creation, argument handling, overwrite protection, permissions and both checksum failure modes. It is not wired into CI, because it downloads published artifacts and would fail on a release that does not exist yet; run it by hand before tagging and after any change to `install.sh`.
- **The installer writes the binary atomically** — it staged the download in `/tmp` and moved it across filesystems onto the target path, which is a copy rather than a rename, so an interruption could leave a truncated binary at the exact path the user runs. It now stages inside the install directory and renames, and reports that the existing installation was left untouched when a write fails.

## [1.45.2] - 2026-08-09

### Fixed
- **Bundled skills never shipped with the release** — a fresh install reported `No skills found`, and the agent ran without the `cron`, `github`, `memory` and `skill-creator` skills entirely. `NewLoader` looked for the bundled set at the *relative* path `filepath.Join("skills")`, which resolves against the process working directory, so they loaded only when joshbot was run from a checkout of its own source tree. The release artifact is a bare binary with no files beside it, so no installed copy ever found them — while `internal/skills/trust.go` exempted them from the approval gate on the grounds that they "arrive with the binary", which was not true. The tree moved to `internal/skills/bundled/` and is now pulled in with `//go:embed`, so it genuinely arrives with the binary and loads from any working directory. A bundled skill's content is cached at discovery (there is no file to re-read), and `Loader.Delete` now refuses one rather than handing an embed path to `os.RemoveAll`. Found by dogfooding a release install.
- **`joshbot onboard` printed the full home directory path** — the setup summary and the existing-install detection showed `/home/<account>/.joshbot/...`, which carries the account name and is the first output a new user pastes into an issue when setup goes wrong. Those paths now go through `redact.HomePath` and print as `~/.joshbot/...`, matching `joshbot status`.

## [1.45.1] - 2026-08-09

### Fixed
- **`joshbot status` redacted its own numeric settings** — `Max tokens: 8192` printed as `Max tokens: [REDACTED]`. The assignment rule matched the label `tokens:` (the `TOKEN` fragment plus the plural `s`), so an operator could not read their own configuration. All-numeric values are now exempt: no key class this package detects is bare digits, while numeric settings collide constantly. The trade is stated in `internal/redact`'s doc comment — `password: 12345678` now stays in the clear, consistent with the existing exemption for bare high-entropy strings. Found by dogfooding the v1.45.0 release binary.
- **`joshbot update` refused to run for any install path containing `/tmp/`** — the guard meant to catch `go run` also matched a normal installation under `/tmp`, and reported the reason as "Cannot update when running from source with 'go run'", which was untrue and unactionable. It now matches only the `go-build` cache, which is what `go run` actually uses, and exits non-zero instead of `0` so a script does not read the refusal as a completed update.

## [1.45.0] - 2026-08-09

### Added
- **Streaming stage 3: reactLoop streams via ChatStream when a sink is attached, plus the CLI sink** — `reactLoop` now calls `ChatStream` instead of `Chat` when `agents.defaults.streaming` is enabled and a `StreamSink` is attached to the request context. Text deltas are forwarded to the sink as they arrive; the full response is accumulated via the stage-2 `ChunkAccumulator` so everything downstream is unchanged. The CLI sink (`cmd/joshbot/main.go`) writes deltas directly to the TTY output, stops the spinner on the first delta, and avoids duplicating the final print. Mid-stream failures append a visible `[stream error: ...]` marker to whatever text was already shown — never silently truncated. Ships behind `agents.defaults.streaming` (default `false`). 7 tests cover flag-off identity, incremental TTY output, piped non-TTY identity, tool-call execution with streaming, and two mid-stream failure scenarios. Four defects found in review and fixed here: the first streamed delta stopped the spinner while holding the mutex the spinner itself takes to draw a frame, deadlocking the CLI whenever a tick landed in that window; the decision to skip printing the response keyed off the config flag rather than whether anything was actually streamed, so a slash command, a session-load failure or a stream that failed to open printed two blank lines and swallowed the answer; a stream that died before emitting any text suppressed the error marker, leaving an empty response that the loop replaced with "I've processed your request."; and `TestStreaming_FlagOffIdenticalToNonStreaming` attached no sink, making it identical in setup to the flag-on test and proving nothing about the flag. `agents.defaults.streaming` is now documented as a key in its own right — in README.md, docs/INSTALL.md and both site pages — including that it is CLI-and-TTY only and that it gives up the transparent provider fallback.
- **Output redaction** (`internal/redact`) — joshbot had none: `grep -ri redact internal/` returned nothing, and everything it logged or printed was verbatim. That matters because the log is what people paste into bug reports, and a tool result carries credentials nobody intended to expose — the model runs `cat config.yml` and the key becomes a debug line. Log output (stdout and file, every level) and `joshbot status` now pass through a redactor that strips vendor key shapes (Anthropic, OpenAI, OpenRouter, GitHub, Slack, Google, NVIDIA, Groq, AWS), `Authorization` values under any scheme, Telegram bot tokens as they appear in an `api.telegram.org` URL, any value assigned to a credential-shaped name in JSON/YAML/env form, and the host home directory (replaced with `~`). Session files are **deliberately not** redacted — rewriting content on save would mangle legitimate text and add a second corruption path, so data at rest is protected by the `0600` mode instead; a test pins that boundary. Bare high-entropy strings are also deliberately not matched: hashes, UUIDs and git SHAs are indistinguishable from secrets by entropy, and redacting them is how redaction ends up switched off. The credential-name list moved into `internal/redact` and is now shared with the shell-environment screen in `internal/tools`, so the two cannot drift. Four ways to redact the wrong half of a line were found while building it, each now pinned by a test. (1) `Authorization` is itself credential-shaped by name, so the assignment rule redacted the *scheme* and published the token; (2) recognising only `Bearer`/`Basic` meant GitHub's own `Authorization: Token <secret>` still fell through to that rule, so both rules now capture any leading scheme word and preserve it while replacing what follows — including the `=` form and the bracketed shape Go's `%v` gives an `http.Header`; (3) gating the scan on the underscored spellings let `api-key: <secret>` through; and (4) matching a name fragment as a bare substring rewrote `Author: Josh Knox`, `unauthorized: request failed` and `secretariat: horse`, and a value class that swallowed `[` turned `tokens=[1,2,3]` into `tokens=[[REDACTED],2,3]`. A fragment must now end the identifier apart from a plural `s` and separator-led segments, and `AUTH` is excluded from the assignment rule while staying in the shared list for the shell-environment screen. Measured on 1 MB of log-shaped prose containing the trigger words: **20 ms**, down from 381 ms before the assignment gate became a precise walk rather than a substring test. A pathological megabyte that is nothing but credential assignments still costs ~345 ms, since every line genuinely matches; real log records are a few hundred bytes. (#126)
- **`joshbot sessions` — list, show, prune and new** — `session.Manager` already had `List`, `Load`, `Delete` and `GetOrCreate`, but none of it was reachable from the CLI: there was no way to see what conversations existed, read one back, or clear a damaged one short of deleting a file under `~/.joshbot/sessions` by hand. This is deliberately not a `--resume` feature; sessions are keyed `channel:senderID` and loaded on every message, so there is exactly one per user per channel and nothing to select. `list` reports message count, on-disk size, age and a `NOTES` column flagging corrupt, compacted and archived state. `show` prints the transcript (with `--last N`) through the redactor, leaving the file verbatim. `prune` takes an id or `--older-than 30d`, and `new` archives a session and starts it empty rather than destroying it. Destructive commands confirm, accept `--force`, and decline rather than hang when there is no terminal. Session IDs are validated against path traversal before any filesystem call — `Load`, `Save`, `Delete`, `Archive`, `Stat` and `Reset` all reject `/`, `..` and null bytes, which they previously did not. Two bugs were found and fixed while building this: `Manager.List` matched any file ending in `.jsonl`, so the `.history.jsonl` compaction archive added in #125 was reported as a phantom session named `<id>.history`; and urfave/cli v2 stops flag parsing at the first positional argument, so `sessions prune <id> --force` would have silently declined and `sessions show <id> --last 2` would have silently printed the entire transcript — trailing flags are now parsed explicitly, and an unknown one is an error rather than being treated as the session id. (#127) Two behaviours changed in review: a session that cannot be scanned at all keeps its real mtime and is reported as `unreadable` rather than defaulting to the zero time, which made `--older-than` delete a session that had merely failed to read; and `prune`/`new` now take the `.jsonl.corrupt` quarantine copy and the `.history.jsonl` compaction archive with them, since both hold the same conversation and an orphaned archive was inherited by the next session under that `channel:senderID`. `--older-than` is also read when it trails the id, and a refusal for want of a terminal exits non-zero.

### Fixed
- **Session files were world-readable, and a single torn write bricked a user permanently** — three defects in one path. (1) `internal/session/manager.go` wrote session JSONL and its `.meta.json` sidecar with mode `0644`, and `internal/log` wrote the log file `0644` in a `0755` directory; all of them hold full conversation content and tool output, so every local account could read every conversation. All are now `0600` with `0750` directories, matching the treatment `config.json` and the trust store already got. (2) The "atomic" write was not atomic across processes: the temp file was the fixed name `<path>.tmp`, shared by every writer of the same sessions directory, so the gateway and a concurrent `agent -m` interleaved into one temp file and the surviving rename published a torn mix of both. The `sync.RWMutex` only ever guarded one process. Writes now go through `writeFileAtomic`, which uses `os.CreateTemp` for a per-writer name. (3) `Load` failed the entire file on any unparseable line — and since there is exactly one session per `channel:senderID` and no CLI to delete one, a torn line meant that Telegram user got an error on **every** subsequent message with no recovery short of `rm`. Damaged lines are now skipped, everything that parses is preserved, and the original bytes are quarantined at `<session-id>.jsonl.corrupt` with a single warning. The quarantine copy survives later loads — the first copy is the only one taken, since a later load reads the already-repaired file — and is removed only when the session itself is explicitly deleted. `TestConcurrentWritersNeverPublishATornFile` reproduces (2) — it fails against the old fixed-temp-name write — and the quarantine and permission behaviours are covered by focused tests in `internal/session/durability_test.go`.
- **Context compaction was recomputed on every turn and never persisted** — `checkAndCompactContext` built an LLM summary of the conversation, returned it as a fresh in-memory slice, and dropped it when `Process` returned. `buildMessages` then rebuilt the full history from `sess.Messages` on the next turn, so every turn past `agents.defaults.compaction_threshold` compressed the whole history again, forever, while the session file kept growing and was rewritten in full on each save. The compaction is now stored in the session as a single compaction record (`Message.Compaction`, always at index 0), so it is computed once and reused. A later compaction summarizes that record together with everything after it and replaces it, so a session never holds more than one. The record is held out of the memory-window slide — sliding over it would silently discard the entire earlier conversation — and the messages it replaces are appended to a `<session-id>.history.jsonl` archive rather than deleted; if the archive cannot be written the compaction is skipped, because recomputing a summary is cheaper than losing a conversation. Measured on the real binary against a scripted endpoint: five turns produced **five** compactions and a 14,373-byte session before, and **one** compaction with a 2,482-byte session after. `WithContextCompressor` was added so the behaviour can be substituted in tests; `WithCompressor` is unchanged. The summary is built from the stored session rather than the provider-facing slice: that slice has already been truncated to `agents.defaults.memory_window`, so summarizing it while replacing the whole stored prefix would have discarded every message the window had slid past. `TestCompactionSummarizesEverythingItReplaces` pins that correspondence and fails against the truncated input. The archive is append-only and is neither rotated nor pruned, so it grows for the life of the session. (#125)

## [1.44.0] - 2026-07-26

### Added
- **Streaming chunk accumulator** (`internal/providers/accumulator.go`) — `ChunkAccumulator` consumes a sequence of `StreamChunk` values and produces the same `*ChatResponse` shape the non-streaming `Chat` path returns. Tool-call fragments are joined by index, not by arrival order, so interleaved arguments from multiple tool calls are reassembled correctly. Truncated streams (no finish reason, or tool call missing id/name) return an error rather than a plausible-looking partial result. `ToolCall` gains an `Index` field and `FunctionCall` fields use `omitempty` for streaming correctness. 15 tests including a mutation check that proves the index-based join is load-bearing.

## [1.43.0] - 2026-07-26

### Changed
- **Progress callback moved from `Agent` struct to per-request context** — `Agent.progress` was a plain struct field with no synchronisation, written by `SetProgressCallback` and read inside `reactLoop`. It was not a live bug today (only the serial CLI path sets it), but it was a trap: Telegram processes messages concurrently, so the moment a second channel attaches a sink, cross-talk between users' replies would occur. The callback now rides `context.Context` via `agent.WithSink`, scoped to one `Process` call. `WithProgressCallback` and `SetProgressCallback` are removed from `*agent.Agent`; `cmd/joshbot/main.go` attaches the sink to the context per message. The `mockProgressAgent` in `cmd/joshbot/main_test.go` still implements `SetProgressCallback` for backward compatibility with the existing test suite, which passes unmodified. A new `TestConcurrentProcessNoCrossDelivery` test spawns two concurrent `Process` calls with distinct sinks and asserts no cross-delivery — this test would fail against the old design.

### Removed
- **`internal/channels/cli.go` deleted** — 497 lines implementing a CLI `Channel` that nothing ever constructed. `NewCLIChannel` had no caller in `cmd/` or `internal/`; the interactive CLI has always been `runAgentLoop` in `cmd/joshbot/main.go`. It was not merely unused but actively harmful: it carried its own copy of the input loop, and the first diagnosis of the unkillable-process bug (#104) was written against it before the live path was identified. The `Channel` interface it hosted moved to `internal/channels/channel.go`; the 22 tests that exercised only the dead code were removed with it, and `stripHTML`, its one general-purpose helper, had no non-test caller either. Coverage stays above the CI floor at 54.6%.

## [1.42.0] - 2026-07-26

### Added
- **Interactive CLI progress indicators** — `joshbot agent` in interactive mode used to go silent for the whole ReAct loop (often 30–90s with tool calls), giving no signal the process was working, stuck, or dead. It now shows a single-line elapsed-time spinner while waiting on the model, and announces each tool call with a completion line and elapsed time (e.g. `⏺ shell(go test ./...)` / `⎿ ok (1.2s)`). Wired via an optional `agent.ProgressFunc` callback (nil by default — zero behaviour change for other callers) and gated on stdout being a real terminal, so piped and non-interactive output (`agent -m`, `scripts/verify-local.sh`) stays clean and undecorated.

### Changed
- **Every quoted number in the docs re-measured** — `AGENTS.md` claimed 19,640 LOC, 597 test functions and 48 test files against an actual 22,813 / 1,056 / 83, so the test-file count was off by 73%. `CLAUDE.md` and `site/architecture.html` were closer but also stale, and a "~30ms startup" claim was deleted rather than carried forward — it measures nearer 10ms and nothing keeps it honest. `AGENTS.md` also described `internal/channels/cli.go` as the CLI channel; it has no callers at all, and the live interactive CLI is `runAgentLoop` in `cmd/joshbot/main.go`.

### Fixed
- **Telegram silently dropped replies whose formatting it rejected** — when a message was sent with a Markdown or HTML parse mode, Telegram answered `400 ... can't parse entities` for anything malformed, and LLM output produces that constantly: a stray `_`, an unclosed backtick, a bare `<tag>`. `isRetryable` has no case for it, so the send was abandoned and the user saw **nothing at all** — no reply, no error. Each part of a message is now retried once with the parse mode cleared, and plain text always sends. Matching is on Telegram's specific description text, never on the bare `400`, so unrelated failures such as `chat not found` are not quietly downgraded to unformatted output.

## [1.41.0] - 2026-07-26

### Added
- **Telegram command menu** — `/start`, `/help` and `/new` have always had handlers, but `setMyCommands` was never called, so none of them appeared behind Telegram's menu button or autocompleted. They are now registered on startup, scoped to private chats. Registration failure is logged and the bot starts regardless.
- **Unknown Telegram commands get an answer** — any text starting with `/` that no handler claimed was dropped on the floor, so a typo like `/nwe` produced no reply at all. It now gets an "Unknown command" message listing the real ones. Users outside `allow_from` still get nothing.

### Fixed
- **"Typing…" vanished mid-answer on Telegram** — the chat action was sent once per inbound message, but Telegram clears it after 5 seconds while an agent turn with tool calls routinely runs far longer, leaving the user watching an idle chat. The indicator is now refreshed every 4 seconds per chat until the reply is sent or the channel shuts down.

## [1.40.3] - 2026-07-26

### Fixed
- **The interactive agent could not be stopped** — `joshbot agent` ignored Ctrl-C, and ignored `kill` too: three SIGINTs and a SIGTERM all left it running, and only SIGKILL worked. Two compounding causes. The input loop checked for shutdown only *between* reads, so a signal arriving while it sat at the prompt — which is essentially always — was never observed. And the signal handler consumed exactly one signal before exiting, while `signal.Notify` had already disabled Go's default termination for SIGINT, SIGTERM and SIGHUP, so every later signal was swallowed by a listener that was no longer there. Reading now happens on its own goroutine and the loop selects over input, shutdown and context cancellation together; a second signal exits immediately rather than being absorbed.

### Fixed
- **Poolside could not be configured correctly by any route** — three separate defects, all found by dogfooding against the live API. The interactive `configure` wizard offered `https://api.poolside.ai/v1` as the default endpoint, **a host that does not resolve**, so anyone accepting the default got a config that could never connect; the real endpoint is `https://inference.poolside.ai/v1`. The registered default model, `poolside/laguna-m.1`, carries a deprecation date of 2026-07-28 — two days out — so the out-of-the-box choice was about to stop working; it is now `poolside/laguna-s-2.1`. And `configure --help` omitted poolside from the list of providers it accepts, alongside five others. The endpoint is now recorded once in the provider registry (`ProviderInfo.DefaultAPIBase`) and read from there by the wizard, with a test asserting the declared endpoint matches the one the factory actually dials — the duplicate copy is what drifted.

## [1.40.2] - 2026-07-26

### Fixed
- **Poolside never worked** — joshbot strips the provider prefix from a model name before sending it, which is right for providers where the prefix is joshbot's own routing hint. Poolside's published model IDs genuinely begin with `poolside/`, so every request went out as `laguna-s-2.1` and came back `404 {"error":"please check the model you provided"}`. This applied to the provider's own registered default, `poolside/laguna-m.1`, so no poolside configuration could work out of the box. Confirmed against the live API: the full ID answers 200, the stripped one 404. Prefixes that form part of the model ID are now kept.

## [1.40.1] - 2026-07-26

### Fixed
- **A one-shot reminder's countdown restarted with the process** — `delay:` schedules were stored as a duration only, so on every start the scheduler called `time.After(duration)` again. A "remind me in 30 minutes" that was 29 minutes in when joshbot restarted fired 30 minutes *after* the restart, and a reminder could be pushed back indefinitely by routine restarts. Jobs now persist an absolute `due_at`, so a restart waits out only the remaining time, and a reminder that came due while joshbot was stopped fires shortly after it starts instead of being lost. Reminders saved before this change have no `due_at`; they are backfilled as due one duration from load — the previous behaviour — so an existing `jobs.json` does not fire everything at once. Recurring (`every:`) jobs are unchanged.

## [1.40.0] - 2026-07-26

### Added
- **`cron` tool — scheduled reminders the agent can actually create** — joshbot advertised scheduled reminders, shipped a skill teaching the agent to run `cron create ...`, and started a scheduler at boot, but no tool existed and nothing outside `internal/cron` ever called `AddJob`: the scheduler ran with a permanently empty job list and every attempt produced a confident-sounding failure. The tool exposes create/list/delete, is registered only when a scheduler is actually running, and returns the job ID so a reminder can be cancelled. Schedules are durations (`30m`, `2h`, `1d`, `1h30m`), one-off or repeating.

### Fixed
- **Scheduled jobs could not be deleted, and one-shot jobs fired forever** — `internal/cron` had no `DeleteJob` or `ListJobs` at all. A `delay:` job was never removed after firing, so every restart replayed every reminder the user had ever set. `AddJob` also read `running` without holding the mutex (a data race under `-race`), accepted schedules the scheduler could not run and then silently never fired them, and `Stop` closed a channel that `Start` never recreated, so the service could not be restarted. Deleting a job now stops its timer rather than only removing the record.
- **The cron skill documented an interface that never existed** — it taught 5-field cron expressions (`0 9 * * *`), a `--name` flag and shell-style invocation, none of which the scheduler or tool support. Rewritten to match the real tool, including the fact that a one-shot reminder's countdown restarts if joshbot restarts. A new test fails the build if a bundled skill names a tool joshbot does not have — the drift that produced this.
- **Landing page claimed trigger types that do not exist** — "React to triggers (new file, webhook, cron)" described file-watch and webhook triggers with no implementation anywhere in the codebase. Replaced with what actually ships: duration-based reminders and the heartbeat.

## [1.39.2] - 2026-07-26

### Fixed
- **Two abandoned copies of the install and uninstall scripts** — `scripts/install.sh` and `scripts/uninstall.sh` were forks frozen before the v-prefixed asset-name fix landed in the repo-root copies. The `scripts/` installer tried three naming patterns, none of which matched what `release.yml` actually publishes, and downgraded a checksum mismatch to a warning it continued past; the `scripts/` uninstaller predated systemd/launchd/pipx removal. Nothing referenced either file except a `.goreleaser.yaml` glob, and the advertised `curl | bash` path was never affected — but both were what a reader looking in `scripts/` would find first. Deleted, the glob repointed, and the naming contract between `install.sh` and `.github/workflows/release.yml` is now pinned by tests so the two cannot drift silently again.

## [1.39.1] - 2026-07-25

### Fixed
- **`--config` ignored the file name it was given** — Only the directory was used, so `--config /path/staging.json` loaded `/path/config.json`, and three configs in one directory all resolved to the same file. A path with no `config.json` beside it silently fell back to defaults, leaving the impression the file had been read. The home override was also reverted immediately after loading, so sessions, media, cron, the skills trust store and `Save` still pointed at `~/.joshbot`: joshbot read one file and wrote another. `configure` and `onboard` ignored the flag entirely — `configure --config x.json --api-key ...` reported success while writing the key to `~/.joshbot/config.json`.

## [1.39.0] - 2026-07-25

### Security
- **Workspace skills could install permanent instructions without review** — A `SKILL.md` under the workspace was discovered and injected into the system prompt: its description always, and with `always: true` its entire body inline on every request, surviving restarts. Nothing checked its origin, and `always` was never validated. Because the agent's own `skill_registry` tool writes skills, text steered by a fetched page, a chat message or a document could induce the agent to write standing instructions it would then follow indefinitely; anything else able to write to the workspace — a cloned repo, an extracted archive — had the same effect without involving the model. Workspace skills are now inert until approved with `joshbot skills trust`, approval is recorded outside the workspace and bound to a hash of the file so edits revoke it, and creating a skill never approves it.

### Fixed
- **New files under `cmd/joshbot/` were silently untracked** — The `.gitignore` entry for the built `joshbot` binary was unanchored, so it also matched the `cmd/joshbot` directory and `git add` skipped anything new there. Anchored to the repo root; this recovered 492 lines of existing tests that had never been committed.

## [1.38.0] - 2026-07-25

### Added
- **OS-level sandbox for shell commands** (`tools.shell_sandbox`) — Opt-in containment via Landlock on Linux: filesystem access is confined to the workspace and build caches, and outbound TCP is denied unless `tools.shell_sandbox_allow_network` is set. Neither `$HOME` nor the shared `/tmp` is granted, so SSH keys, cloud credentials and joshbot's own config are unreachable. This is the boundary the deny list in `shell_deny.go` cannot be: screening has to anticipate every dangerous command an attacker might write, while the kernel refuses the access however it was spelled. Off by default because enabling it changes what existing setups can do. It fails closed — an unknown mode, a platform without an implementation, or a kernel without Landlock all produce a startup error rather than an unconfined run.

## [1.37.0] - 2026-07-25

### Security
- **Provider API keys were handed to every shell command** — `exec.Cmd` with a nil `Env` inherits the parent's environment, and `runCommand` never assigned one, so any command the model ran received joshbot's full environment including `JOSHBOT_PROVIDERS__*__API_KEY`. A bare `env` was enough to read them: no filesystem access, no deny-list rule involved, and no filesystem sandbox would have helped. Spawned commands now get an allowlisted environment, screened a second time for credential-shaped names.
- **`config.json` was written world-readable (0644)** — It holds live provider API keys, so every account on the machine could read them, which also made any containment of the shell tool beside the point. Now written 0600, along with the migration backup that copies it verbatim.

### Fixed
- **Every Telegram message was written to stderr in plaintext** — Two debug statements left in since May printed each sender ID and full message text directly to stderr, bypassing the log-level system entirely, so a personal assistant's whole conversation landed in the journal regardless of `log_level`.
- **Stopping the Telegram channel did not stop it** — `Stop()` called telebot's `Close()` (the Bot API session-teardown RPC) instead of `Stop()`, so the long-poller goroutine never exited while the channel logged "Telegram channel stopped". Harmless only because the single call site sits immediately before process exit; anything restarting the channel in-process would have produced two concurrent `getUpdates` pollers, which Telegram rejects.
- **`joshbot status` reported disabled providers as configured** — A provider without `"enabled": true` is silently skipped, but `status` listed it anyway, so the one command you would run to diagnose the problem confirmed the broken config looked fine. Startup now fails with a message naming the actual cause — the missing `enabled` flag or a missing `api_key`, whichever it is — and the README's legacy example no longer omits `enabled`.
- **Memory consolidation stored whatever the model returned** — A refusal, meta-commentary or a wall of prose became a permanent fact in `MEMORY.md`, which is loaded into every subsequent turn's context. Output now passes a deterministic content gate before anything is persisted; a completion yielding no valid facts is logged and discarded.
- **Context compression could silently erase a conversation** — Past 50 messages, a provider returning a choice with empty content made `CompressMessages` return an empty string with a nil error, skipping the deterministic fallback. The assistant would forget everything before that point and carry on. Degenerate summaries are now rejected, and the provider call takes a caller-supplied context so cancellation propagates.

## [1.36.0] - 2026-07-25

### Security
- **SSRF protection in `web_fetch` could be bypassed by any attacker-controlled hostname** — The private-address check only resolved a hostname when the name itself contained a keyword such as `metadata` or `localhost`, so any other name reached any address it pointed at, including loopback, private networks and cloud metadata endpoints. Two further defects made the keyword list ineffective even for the names it covered: a failed DNS lookup counted as safe, and the private-range check excluded link-local, where `169.254.169.254` lives. The combination meant `http://metadata.google.internal/` was allowed despite being listed in the tool's own block list. Reachable in the default configuration — `web_fetch` has no enable flag — including via indirect prompt injection from a page the assistant was asked to read. Addresses are now checked after resolution and again at dial time, which also closes the DNS-rebinding window and covers the search-engine redirect path, which followed `Location` headers with no check at all.

## [1.35.1] - 2026-07-25

### Fixed
- **Long conversations were rejected by the LLM provider** — Once a conversation grew past the token budget, context reduction rebuilt older messages without their tool-call linkage, so joshbot sent `tool` messages carrying no `tool_call_id`. OpenAI-compatible endpoints reject that request with a 400, meaning long sessions failed while short ones worked. The memory window could orphan a tool result the same way by cutting between an assistant tool call and its result.

### Added
- **Behavioural eval harness for the ReAct loop** (`internal/agent/evalharness_test.go`) — Runs the real loop against a scripted provider that records every request, so tests assert the trajectory (what was sent, dispatched and persisted) rather than the reply text. Runs in CI with no network or API key. `assertProtocolInvariants` enforces provider wire-format rules on every scenario. Validated by mutation testing: 9 of 9 deliberately introduced regressions were caught. `internal/agent` coverage 63.5% → 75.3%.

## [1.35.0] - 2026-07-25

### Security
- **`golang.org/x/text` bumped v0.3.7 → v0.30.0 (CVE-2022-32149)** — Denial of service in language tag parsing. The package is reachable from this build.

### Fixed
- **Telegram could hang on long replies containing an unbalanced code fence** — A reply over the 4096-character limit whose ``` fences did not pair up made the message splitter loop forever, so the message was never delivered, no error was reported, and the Telegram sender stopped processing further outbound messages. Long code-heavy answers were the common trigger.
- **Dangerous shell commands could evade the safety deny list** — Padded whitespace (`rm -rf  /`), reordered or split flags (`rm -fr /`, `rm -r -f /`), quote splicing (`r"m" -rf /`), wrapper commands (`sudo`, `env`, `nohup`, `timeout`), command substitution (`$(...)`, backticks), and `$IFS` separators all bypassed the previous substring matcher.
- **Telegram `allow_from` ignored numeric user IDs** — `IsAllowed` matched only usernames and display names, so a user following the README (which documents `"allow_from": ["123456789"]`) was locked out of their own bot. Numeric IDs now match, and they are the only identifier a user cannot change.

### Changed
- **Shell command screening rewritten** — Commands are now split into segments at unquoted separators, unwrapped, and matched against structural rules rather than literal substrings. This also removes false rejections such as `echo shutdown` and `ls | sha256sum`. It remains defence in depth, not a security boundary: isolation still requires an OS-level sandbox.
- **`ShellToolConfig.DenyList` is now additive** — Custom patterns apply on top of the built-in rules instead of replacing them, so configuration can tighten screening but not weaken it.

### Added
- Test coverage for `internal/channels` (47.5% → 59.8%) and a regression suite for shell command screening.
- **Panel review workflow** — A repo-scoped five-expert review (security, evals, experience, growth, Go systems) that analyses, debates and scores a change. Charters live in `.claude/skills/panel-review/references/experts.md` and are harness-portable. See the Panel Review section of `AGENTS.md`.

## [1.34.0] - 2026-06-06

> 1.33.0 and 1.34.0 were tagged from a release branch that was not merged
> back to `main` until 2026-07-25. Their dates therefore predate 1.31.0 and
> 1.32.0, which were released from `main` in the meantime.

### Changed
- **Prompt optimization (GEPA workflow)** — Core identity prompt reduced 457→356 words (22%). Variant A selected: structural cleanup preserves all behavioral guardrails with better organization. Tool descriptions now include tool name prefix for better function-calling disambiguation. Eval harness (all 24/24 weighted checks pass).

## [1.33.0] - 2026-06-05

### Added
- **Prompt optimization** — Core identity prompt reduced 411→299 words (27%). All 22 tool descriptions trimmed ~30-40%. Eval harness at `internal/agent/prompt_eval_test.go`.
- **MEMORY.md size cap** — Configurable `max_memory_size` (default 4KB). Auto-trims oldest entries on overflow.
- **Skill.Always field wired up** — Skills with `always: true` now inject full content into system prompt (was dead code).

### Fixed
- **Model prefix bug** — `StripProviderPrefix()` now called in `Chat()` and `ChatStream()` so provider prefixes (e.g., `nvidia/`) are stripped before sending to API.
## [1.32.0] - 2026-07-24

### Added
- **ConfigureTool for in-chat config management** — New `configure` tool allows listing available models and settings, switching the active model, adjusting settings like temperature and max_tokens, and viewing the current active configuration — all from within a conversation.

### Fixed
- **Model-centric routing fix** — Fixed model routing when using the model-centric config format (`models_config`). Provider is now correctly auto-detected from the model prefix.
- **Identity rewrite** — Updated workspace identity files (`IDENTITY.md`, `SOUL.md`, `USER.md`) for improved system prompt coherence.
- **Conversation coherence improvements** — Enhanced context assembly and prompt optimization for more coherent multi-turn conversations.

## [1.31.0] - 2026-07-24

### Added
- **ConfigureTool** — In-chat configuration management tool with `list`, `get`, `set`, and `save` operations for models, temperature, max_tokens, workspace, and other settings.

### Fixed
- **Model-centric routing** — Fixed model routing logic for the model-centric config format.
- **gofmt formatting** — Fixed formatting in 3 files.

## [1.30.0] - 2026-06-03

### Fixed
- **`<conversation_summary>` still leaking with weaker models** — Renamed internal compression tag from `<conversation_summary>` to `<ctx_compress>`. The old name was too semantically rich: weaker models (e.g., step-3.5-flash) saw "conversation summary" in context and responded to it even with v1.28.1's system prompt removal + output sanitization. The new name is semantically inert — models have no reason to reference it. Dogfooded with 7.5K Kubernetes deep-dive: zero tag leakage.

## [1.29.0] - 2026-06-03

### Added
- **Use case transcripts** — 5 detailed conversation transcripts in `tasks/use-cases.md` covering: Research + Code Generation, Self-Learning + Persistent Memory, Multi-Step Content Pipeline, Bug Investigation + Fix, and Skill Creation. Each includes exact tool schemas, parameters, execution paths, and expected outputs.
- **Filesystem alias tests** — 9 test functions across 35 assertions covering all 6 filesystem aliases (read_file, write_file, edit_file, list_dir, glob, grep).
- **Isolated unit tests** — Direct tests for `parseTasksArg`, `parseStepsArg`, `applyTemplates` with edge cases (empty arrays, overlapping template names, single-quoted JSON, invalid inputs).

### Fixed
- **Subagent maxTokens=500 truncated generated code** — Increased to 4096 so chain execution steps (e.g., code generation of ~100+ line Go files) complete without truncation. Also updated subagent system prompt from "max 500 tokens" to "max 4096 tokens".
- **applyTemplates() variable name collision** — Sorts template variable names by length descending before iterating, preventing `{{a}}` from partially matching inside `{{ab}}`.

## [1.28.1] - 2026-06-03

### Fixed
- **System prompt backfire (pink elephant)** — Removed all mentions of `<conversation_summary>` from the system prompt. The LLM was being primed to think about the tag, causing hallucinations of seeing it in the conversation. Defense is now purely structural (proper XML closing tags + output sanitization).

## [1.28.0] - 2026-06-03

### Fixed
- **`<conversation_summary>` leaking into LLM responses** — Three-layer fix:
  1. Added proper `</conversation_summary>` closing tag (was missing, LLM treated as unclosed XML)
  2. Added system prompt instruction telling LLM to never reference or output internal context tags
  3. Added `sanitizeResponse()` output sanitizer that strips any `<conversation_summary>` tags from responses (defense-in-depth)

## [1.27.0] - 2026-06-02

### Added
- **`chain_execution` tool** — Sequential multi-step subagent execution. Each step's output feeds as context to the next. Supports template substitution (`{{name}}` → prior step output), initial context, graceful step failure (subsequent steps continue), and cancellation. 12 tests.
- **`subagent_config` tool** — CRUD for YAML-based agent profile configs stored in `~/.joshbot/agents/`. Operations: `list`, `get`, `save`, `discover`. Each config defines model, temperature, max_tokens, system_prompt, tools, skills, and tags. 26 tests.
- **Auto-discovery of agent configs** — `~/.joshbot/agents/` directory scanned at startup, `*.yaml`/`*.yml` files loaded as named agent profiles.

### Fixed
- **Single-quote JSON parsing** — `parseTasksArg` now falls back to replacing single quotes with double quotes, handling LLMs that serialize arrays as single-quoted JSON strings.

### Other
- **GitHub Pages deploy** — `site/` directory (landing page + architecture docs) deployed to GitHub Pages on every release. URL set as repo homepage. Pages deploy job added to release workflow.

## [1.26.0] - 2026-06-01

### Fixed
- **Log format string bug** — `defaultLogger` had mismatched format verbs in structured log calls, causing potential panics on certain log levels.

## [1.25.0] - 2026-06-01

### Added
- **Poolside provider** — New provider registration for `poolside/laguna-m.1` with `ExtraBody` support for `chat_template_kwargs`.
- **`onboard --force` stdin hang fix** — Zero stdin pipe detection prevents blocking on first-time setup.

## [1.24.0] - 2026-05-31

### Fixed
- **exa-cli search JSON parsing** — Handles pretty-printed multi-line JSON output from exa-cli search results.

## [1.23.0] - 2026-05-31

### Changed
- **`CompressMessages` refactored** — Extracted `lastNonEmptyContent` helper, returns error on all-empty content instead of silently wrapping empty string in `<conversation_summary>` tags.

## [1.22.0] - 2026-05-31

### Added
- **`parallel_subagent` tool** — Fan-out independent tasks to subagents with configurable concurrency, semaphore-bounded goroutine pool, per-task success/failure reporting. 11 unit + 9 integration tests.
- **Subagent runner** — `internal/subagent/` package wrapping single-turn LLM calls with configurable model, temperature, max tokens, and timeout.

## [1.21.0] - 2026-05-31

### Added
- **Auto-skill-creation pipeline** — `SkillDetector` → `Extractor` → `Loader.Create` fully wired in `main.go` (lines 600-616). Tool usage patterns generate SKILL.md files automatically.

## [1.20.2] - 2026-05-31

### Fixed
- **HTTP 410 error on LLM calls** — Default model `arcee-ai/trinity-large-preview:free` was removed from OpenRouter, causing all API calls to fail with `API error (410)`. Changed default to `openrouter/free` (auto-routes to best available free model), updated all references in config, provider registry, context budget, docs, and tests.
- **Migration typo** — Config migration from v0→v1 wrote misspelled model name `arcee-ai/tranny-large-preview:free` (typo: "tranny") instead of the correct model. Fixed to migrate to `openrouter/free`. Hardenend migration test to assert exact expected model.
- **410 not triggering fallback** — `isFallbackStatusCode()` did not include 410 (Gone), so a deprecated model caused immediate failure instead of triggering fallback to another provider. Added 410 to the fallback status code list.

## [1.20.0] - 2026-05-19

### Added
- **CLI flags for `joshbot config`** — `--provider`, `--api-key`, `--api-base`, `--model`, `--set-default`, `--remove` flags for headless configuration
- **`joshbot version` command** — Shows the current version string
- **`internal/configure/` package** — Shared configurator API used by both CLI flags and interactive wizard, gating CLI/interactive parity with tests

### Fixed
- **Model config bugs** — `setDefaultProvider` no longer overwrites per-provider model with registry default; `configureProvider` auto-default now sets `agents.defaults.model`; NVIDIA provider registration now passes `p.Model` on creation and registration

### Changed
- `internal/config/config.go` — Added `JOSHBOT_NVIDIA_API_KEY` shorthand env var support with auto-`Enabled: true`
- `internal/config/config_test.go` — Added `TestMain()` to isolate tests from env var pollution
- Deduplicated `getProviderDisplayName` and `maskAPIKey` into `internal/configure/` package (removed from main.go)
- Replaced deprecated `strings.Title` with `cases.Title` from `golang.org/x/text`

## [1.19.0] - 2026-05-17

### Added

#### Intelligent Memory System
- **Structured Fact format** — Facts stored with SHA256-based `FactID`, category enum (`user_info`, `preference`, `project`, `decision`, `skill`, `system`), confidence scoring (0.0-1.0), source tracking, and deduplication
- **Memory search tool** (`memory_search`) — Keyword + category + tag search with relevance scoring (recency, confidence, access count, keyword match)
- **Markdown-backed persistence** — Facts stored in MEMORY.md with backward-compatible user-editable format
- **Metadata support** — `UserMetadata` struct for tracking memory version, preferences, and last updated
- **Fact reconciliation** — `ReconcileFacts()` merges new facts with existing memory via upsert + dedup + eviction (max 100 facts)
- **Learning extraction** — `learning.go` now extracts structured facts from conversations with LLM-based summarization
- **New files**: `internal/memory/fact.go`, `search.go`, `metadata.go`, `internal/tools/memory_tool.go`

#### Skill Self-Creation
- **Skill Detection** — Weighted heuristic scoring system (`SkillDetector`) that detects skill-worthy patterns from conversation traces (3+ tool use triggers, repeated command patterns, explicit skill creation requests). Threshold: ≥2.0 confidence
- **LLM-based Skill Extraction** — `Extractor` generates valid SKILL.md files from conversation traces using an LLM prompt
- **Skill Validation** — `ValidateSkill()` checks YAML frontmatter, required fields, name uniqueness, and body content
- **Skill Registry Tool** (`skill_registry`) — Exposes `list`, `create`, and `delete` actions to the ReAct loop
- **Skill creation flow** — `CreateSkill()` writes to `~/.joshbot/workspace/skills/{name}/SKILL.md`, auto-discovered by the skills Loader
- **New files**: `internal/skills/detection.go`, `extraction.go`, `validation.go`, `detection_test.go`, `internal/tools/skill_tool.go`

### Changed
- `internal/agent/agent.go` — Wire skill detection into reactLoop (after each iteration and after processing)
- `internal/agent/context.go` — `BuildSmartPrompt` uses strings.Builder, extracted methods
- `internal/learning/learning.go` — Extracted `saveSummary` and `heuristicFallback` methods
- `internal/memory/memory.go` — `WriteFacts`, `ReconcileFacts`, structured markdown output with categorized sections
- `internal/skills/skills.go` — Added `Create()`, `Delete()`, `List()`, `Invalidate()` methods on Loader
- `internal/tools/registry.go` — Registers SkillRegistryTool alongside defaults

## [1.18.0] - 2026-05-17

### Changed
- **AGENTS.md rewritten** — Accurate LOC count (~16,000 non-test Go), Go 1.24.0, correct interface signatures
- **Code simplifications** — Simplified `cleanCommand`, `normalizeUsername`, `joinParts`, `stripHTML`
- **Schema building deduplication** — Replaced duplicated schema building in `toolToProviderTool` with `GenerateSchema`
- **Dead code removal** — Removed `FormatToolResult`, `FormatAssistantToolCalls`, `stringsSplitN`
- **Regex compilation fix** — Eliminated regex in `extractStatusCode`, fixed false positive on port numbers
- **Provider normalization** — Simplified `normalizeProviderName` with case-insensitive lookup

### Added
- Unit tests for `scanStatusCode`, `scanAnyStatusCode`, `normalizeProviderName`

## [1.17.1] - 2026-04-02

### Fixed

- **Broken MCP package removed** — `internal/mcp/` referenced undefined types from MCP SDK v1.4.0, blocking `go vet` and CI
- **Debug output leaks** — replaced 5 `fmt.Printf("[DEBUG]...")` statements in copilot auth with structured `log.Debug()`
- **Checksum verification bypass** — `install.sh` now exits on checksum mismatch instead of continuing with potentially corrupted binary

### Added

- **Smoke tests for 5 zero-coverage packages** — heartbeat (7 tests), skills (14 tests), service (4 tests), pkg/bus (1 test), copilot (6 tests)
- **SECURITY.md** — vulnerability reporting process, security best practices, and architecture notes
- **VERSION file** — proper version tracking for releases

### Changed

- **uninstall.sh rewritten** — now handles Go binary removal, systemd/launchd services, and legacy pipx installation
- **docker-compose.yml** — removed deprecated `version: '3.8'` key
- **CHANGELOG.md** — backfilled missing v1.13-v1.15 entries

## [1.17.0] - 2026-03-05

### Added

#### Async Tools for Long-Running Operations
- **AsyncTool interface** - Tools can implement async execution for background tasks
- **Auto-detection** - Commands like `python`, `npm run`, `make`, `docker build` automatically run async
- **Explicit control** - `async: true` parameter to force background execution
- **Callback notifications** - Background task completion delivered via message bus or CLI
- **Timeout prevention** - Operations can run longer than 60 seconds without timeout errors
- **Better UX**: "Started in background, I'll notify you when done."

#### Debug Logging
- **`--debug` flag** for agent and gateway commands to enable detailed troubleshooting logs
- **HTTP response logging** - Log status codes and model information
- **LLM response details** - Log content length, tool calls, and finish reasons
- **Empty content detection** - Warn when LLM returns empty responses after tool execution

## [1.16.0] - 2026-03-04

### Added

#### Model-Centric Configuration
- **New `models_config` format** - Simplified model configuration with provider auto-detection
- **Fallback chains** - Configure multiple models; automatically try next if primary fails
- **Provider auto-detection** - Model prefixes (`anthropic/`, `groq/`, `ollama/`, etc.) automatically set API base URL
- **Supported providers**: Anthropic, OpenAI, Groq, Ollama, OpenRouter, NVIDIA NIM, DeepSeek, Google Gemini, Cerebras
- **Environment variable support**: `JOSHBOT_MODELS_CONFIG__MODELS__0__NAME`, etc.
- **Backward compatible** - Legacy `providers` format still supported

#### System Prompt Caching
- **Intelligent caching** - Static system prompt cached in memory, reducing file I/O on every message
- **mtime-based invalidation** - Cache automatically rebuilds when source files change
- **Tracked files**: AGENTS.md, SOUL.md, USER.md, TOOLS.md, IDENTITY.md, memory/MEMORY.md, skills/*/SKILL.md
- **Force refresh** - `InvalidatePromptCache()` for programmatic cache clearing

### Changed

- Configuration now supports both model-centric and provider-centric formats
- Faster response times due to reduced file I/O

## [1.15.0] - 2026-03-01

### Added

#### GitHub Copilot Integration
- **GitHub Copilot LLM provider** with OAuth device code flow authentication
- **Token management** with automatic refresh and expiry detection
- **Streaming support** for Copilot chat completions
- **Model listing** via Copilot API

#### Subagent System
- **Spawn tool** for creating isolated subagent tasks
- **Subagent isolation** with separate contexts and memory
- **Background execution** for long-running operations

## [1.14.0] - 2026-02-28

### Added

#### MCP (Model Context Protocol) Support
- **MCP server connections** via HTTP/SSE and stdio transports
- **Dynamic tool discovery** from connected MCP servers
- **Tool registration** with joshbot's tool registry

#### Cron Scheduling
- **Recurring task scheduler** with cron expression support
- **Automatic task execution** in background subagents
- **Task persistence** across restarts

### Fixed

- Memory consolidation race conditions during idle periods
- Session file corruption on abrupt shutdowns

## [1.13.0] - 2026-02-27

### Added

#### Heartbeat System
- **Periodic health checks** for self-monitoring
- **Automatic maintenance tasks** during idle periods
- **Memory consolidation** triggered by heartbeat

#### Learning System
- **Cross-session learning** from user feedback
- **Pattern recognition** for common tasks
- **Knowledge graph** for persistent entity relationships

### Changed

- Improved error messages for provider connection failures
- Better handling of rate limits across all providers

## [1.12.1] - 2026-02-25

### Fixed

- **Model not updating when changing providers** - `joshbot config` now correctly updates the default model when switching providers, preventing 404 errors with NVIDIA NIM
- **Missing tool_call_id field** - Tool result messages now correctly include `tool_call_id`, fixing "missing field tool_call_id" errors with strict providers like Arcee AI

## [1.12.0] - 2026-02-24

### Added

#### Web Fetch Enhancement
- **Exa crawl integration** for web_fetch tool to handle JavaScript-rendered pages
- Improved content extraction from dynamic websites

### Fixed

- **Version display** - Release binaries now show actual version instead of "dev"
  - Fixed ldflags in GoReleaser, CI workflow, and Dockerfile
- **Status command** - Telegram and Workspace restricted now show "enabled"/"disabled" instead of "(exists)"

## [1.11.0] - 2026-02-24

### Added

#### Enhanced Ollama Integration
- **Model listing in configure wizard** - Fetches and displays available Ollama models
- **`--model` flag** for `agent` and `onboard` commands to override model at runtime
- **Configurable timeout** for Ollama provider (default 300s for CPU-only)
- **CPU tips** displayed after Ollama configuration

### Changed

- **No fallback on Ollama 404** - "Model not found" errors require user to `ollama pull <model>`
- Improved error handling with provider-aware fallback logic

## [1.1.0] - 2026-02-21

### Added

#### Interactive Telegram Setup
- Telegram setup wizard integrated into `joshbot onboard` (Step 4)
- Guides users through @BotFather bot creation
- Validates bot token via `getMe` API before saving
- Optional allowed usernames configuration
- Auto-saves to config without manual editing

#### Service Management
- `joshbot service install`: Install joshbot as a system service
- `joshbot service uninstall`: Remove the system service
- `joshbot service status`: Check service status
- **Systemd support** (Linux): Service installed to `/etc/systemd/system/joshbot.service`
- **Launchd support** (macOS): Service installed to `~/Library/LaunchAgents/com.joshbot.plist`
- Auto-start on boot with proper logging

#### Enhanced Onboard Flow
- Step 1: API key setup
- Step 2: Personality selection
- Step 3: Model selection
- Step 4: Telegram setup (optional)
- Step 5: Service installation (recommended for Telegram users)
- Explains why service install is needed for Telegram bots

### Changed

- Onboard now offers to start gateway automatically after Telegram setup
- Telegram token validation happens during setup (not at runtime)

## [1.0.0] - 2026-02-21

### Migration Notes

This release marks a complete rewrite of joshbot from Python to Go. The new Go implementation
offers improved performance, simpler deployment, and a more robust architecture. 

**Key changes:**
- Configuration from previous Python version is **not** compatible
- Memory files (MEMORY.md, HISTORY.md) in `~/.joshbot/` remain compatible
- Sessions in `~/.joshbot/sessions/` remain compatible
- Skills in `workspace/skills/` remain compatible
- Run `./joshbot onboard` to set up fresh configuration

### Added

#### Core Architecture
- Complete Go implementation (~3,600 LOC) with goroutine-based concurrency
- Message bus architecture decoupling chat channels from the agent loop
- ReAct agent loop with tool execution and reflection cycles (max 20 iterations)
- Multi-provider LLM support via OpenRouter-compatible APIs

#### Memory System
- **MEMORY.md**: Persistent long-term memory, always included in context
- **HISTORY.md**: Searchable event log (grep-based) for recent context
- Self-learning memory consolidation during idle periods
- Context compression for small models with token budgeting

#### Skills System
- Skill discovery from `workspace/skills/{skill}/SKILL.md` files
- Progressive loading: summary on first use, full content after first execution
- YAML frontmatter for skill metadata (name, triggers, description)

#### Tools
- Filesystem tool: read, write, list, glob, grep operations
- Shell tool: command execution with safety deny-list
- Web tool: search and fetch capabilities
- Message tool: send messages to Telegram/CLI
- Spawn tool: create subagents for isolated long-running tasks
- Cron tool: schedule recurring tasks

#### Proactive Behavior
- **Cron scheduling service**: Background scheduler for periodic tasks
- **Heartbeat**: Periodic health checks and self-maintenance tasks
- Subagent runner for background task isolation

#### Channels
- **Telegram**: Full bot integration with commands and inline queries
- **CLI**: Interactive terminal mode with readline support

#### CLI & Configuration
- `joshbot onboard`: First-time setup wizard
- `joshbot agent`: Interactive CLI mode
- `joshbot gateway`: Telegram + all channels mode
- `joshbot status`: Show configuration and status
- `--force` flag: Force fresh onboarding (skips existing config check)
- `--keep-data` flag: Preserve memory and sessions during re-onboarding
- Configuration via `~/.joshbot/config.json` with `JOSHBOT_` env var prefix

### Changed

- Default model: `anthropic/claude-3.5-sonnet` via OpenRouter
- Default log level: WARNING (cleaner output)
- Session format: JSONL files in `~/.joshbot/sessions/`
- Architecture: Channel-based message bus (publish/subscribe pattern)

### Removed

- Python runtime dependency
- litellm library (replaced with native HTTP client)
- pip/pipx installation (Go binary distribution)
- Python virtual environment management
