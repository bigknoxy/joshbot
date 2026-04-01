# Security Policy

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 1.17.x  | :white_check_mark: |
| < 1.17  | :x:                |

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

- **API Keys**: Never commit API keys or tokens to version control. Use environment variables or the config file with restricted permissions (`~/.joshbot/config.json` should be `0600`)
- **Shell Tool**: The shell tool executes commands with the same permissions as the joshbot process. Run joshbot with the minimum required privileges
- **Telegram Bot Token**: Keep your Telegram bot token secure. If compromised, regenerate it via @BotFather immediately
- **Memory Files**: `MEMORY.md` and `HISTORY.md` may contain sensitive information. Ensure `~/.joshbot/` directory permissions are restrictive
- **Updates**: Keep joshbot updated to the latest version to receive security patches

### Security Architecture

- **No secret logging**: API keys and tokens are never written to logs
- **File permissions**: Auth files are created with `0600` permissions
- **Input validation**: All tool inputs are validated before execution
- **Sandbox awareness**: joshbot runs with the same permissions as the user — it does not implement its own sandbox
