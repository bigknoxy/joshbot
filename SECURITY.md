# Security Policy

## Supported Versions

joshbot is a single-maintainer project and fixes land on the latest release
only. Pinning version numbers here is what let this section go stale by more
than a dozen releases, so it now states the policy rather than a snapshot.

| Version | Supported |
| ------- | --------- |
| [Latest release](https://github.com/bigknoxy/joshbot/releases/latest) | :white_check_mark: |
| Anything earlier | :x: |

If you are running an older build, upgrade before reporting — the issue may
already be fixed.

## Reporting a Vulnerability

We take the security of joshbot seriously. If you believe you have found a security vulnerability, please report it to us as described below.

**Please do NOT report security vulnerabilities through public GitHub issues.**

Instead, please report them via:

1. **Email**: Send a detailed description to [security@joshbot.dev](mailto:security@joshbot.dev) (or open a GitHub Security Advisory if available)
2. **GitHub**: Use the [Private Vulnerability Reporting](https://github.com/bigknoxy/joshbot/security/advisories/new) feature

### What to Include

Please include the following information in your report:

- Description of the vulnerability
- Steps to reproduce the issue
- Potential impact assessment
- Suggested fix (if you have one)

### What to Expect

- **Acknowledgment**: We will acknowledge receipt of your vulnerability report within 48 hours
- **Updates**: We will keep you informed of our progress
- **Resolution**: We aim to resolve critical vulnerabilities within 30 days
- **Credit**: We will credit you in the release notes (unless you prefer to remain anonymous)

### Security Best Practices

When running joshbot:

- **API Keys**: Never commit API keys or tokens to version control. Use environment variables or the config file — `~/.joshbot/config.json` is written `0600` automatically
- **Shell Tool**: The shell tool executes commands with the same permissions as the joshbot process by default. Run joshbot with the minimum required privileges, and consider enabling `tools.shell_sandbox` (see below) for real containment
- **Telegram Bot Token**: Keep your Telegram bot token secure. If compromised, regenerate it via @BotFather immediately
- **Telegram allowlist**: Set `channels.telegram.allow_from` to your numeric Telegram user ID before exposing the bot. An empty allowlist now denies every sender (fail-closed) — the bot logs a loud warning at startup and rejects all messages until you populate it. Do not leave it unset: whoever can message the bot otherwise reaches an agent loop holding the `shell` tool
- **Memory Files**: `MEMORY.md` and `HISTORY.md` may contain sensitive information. Ensure `~/.joshbot/` directory permissions are restrictive
- **Workspace Skills**: A `SKILL.md` becomes part of the agent's standing instructions, so treat one you didn't write yourself as untrusted input until you've read it and run `joshbot skills trust <name>`
- **Updates**: Keep joshbot updated to the latest version to receive security patches

### Security Architecture

- **No secret logging**: API keys and tokens are never written to logs
- **File permissions**: Config (`config.json`), auth tokens, and the skills trust store are all written `0600`
- **Input validation**: All tool inputs are validated before execution
- **Command screening is defence in depth, not a boundary**: the shell tool's deny list closes known-dangerous command shapes, but no deny list can be sound against an adversarial model or a prompt-injected instruction. The only hard boundary is the OS-level sandbox below.
- **Shell sandbox (opt-in, Linux only)**: setting `tools.shell_sandbox: "workspace"` confines shell commands via Landlock — filesystem access limited to the workspace and toolchain caches, outbound TCP denied unless `tools.shell_sandbox_allow_network` is `true`. It's off by default (existing behavior is unchanged unless you opt in), and it fails closed: an unrecognized value, a non-Linux host, or a kernel without Landlock support is a startup error rather than a silent no-op.
- **Environment isolation**: shell commands no longer inherit joshbot's own process environment. They receive an allowlisted subset (PATH, common toolchain variables) with anything credential-shaped — API keys, tokens, secrets — stripped, so a spawned command cannot read joshbot's own provider credentials via `env`.
- **Telegram allowlist fails closed**: `channels.telegram.allow_from` is enforced deny-by-default. An empty or unset allowlist rejects every sender rather than admitting everyone, and the channel logs an actionable warning at startup naming the config key to set. This is a deliberate breaking change from the previous allow-all-when-empty behavior.
- **Skill approval**: a `SKILL.md` found in the workspace — including one the agent creates for itself via `skill_registry` — is inert until approved via `joshbot skills trust`. Approval is bound to a SHA-256 digest of the **entire skill directory tree** (every regular file's relative path and content, walked in sorted order for determinism), not just `SKILL.md`. So editing, adding, or removing any file in an approved skill directory — including a sibling script the `SKILL.md` tells the agent to run — revokes trust. A trust store written by an older joshbot (which hashed only `SKILL.md`) will no longer match the new digest, which safely revokes affected skills rather than crashing; re-inspect and re-trust them. Skills bundled with the release are exempt.
- **Filesystem tool containment is symlink-resolved**: when `restrict_to_workspace` is set, the `filesystem` tool resolves symlinks (`filepath.EvalSymlinks`) before the workspace-containment check and operates on the resolved path, so a symlink that is lexically inside the workspace but points outside it (`ln -s /etc ws/link`) is rejected rather than followed. For a not-yet-existing file the nearest existing parent is resolved, so creating a new file inside the workspace still works while a symlinked ancestor is still caught. Resolving and then operating on the same resolved path closes the check/use (TOCTOU) gap.

### Tool-Specific Security Notes

- **`shell` tool**: Screens commands against a deny list (defence in depth, not a boundary — see above) and, when `restrict_to_workspace` is set, confines the working directory. Enable `tools.shell_sandbox` for actual OS-level containment.
- **`memory_search` tool**: Can read all stored facts, including potentially sensitive information in MEMORY.md. Access control is file-permission based — ensure `~/.joshbot/` directory permissions are restrictive.
- **`skill_registry` tool**: Can create, list, and delete skills (SKILL.md files). Skill creation writes files under `~/.joshbot/workspace/skills/`, but a newly created skill does not take effect until an operator approves it with `joshbot skills trust` — see Skill Approval above. Treat skill changes as configuration changes — review skill content before approving.
- **`web_fetch` / `web_search` tools**: Refuse to connect to localhost, private IP ranges, and cloud metadata hosts, enforced at connection time (not just URL string matching) to resist DNS-rebinding-style bypasses.
