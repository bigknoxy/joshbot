#!/usr/bin/env bash
set -euo pipefail

GREEN='\033[0;32m' RED='\033[0;31m' YELLOW='\033[1;33m' RESET='\033[0m'
info() { printf "${GREEN}[INFO]${RESET} %s\n" "$1"; }
success() { printf "${GREEN}[OK]${RESET} %s\n" "$1"; }
warn() { printf "${YELLOW}[WARN]${RESET} %s\n" "$1"; }

# Check if joshbot is installed
if ! command -v joshbot &>/dev/null; then
    warn "joshbot is not installed"
    exit 0
fi

# Find the binary location
JOSHBOT_PATH=$(which joshbot)
info "Found joshbot at: $JOSHBOT_PATH"

# Remove the binary
rm -f "$JOSHBOT_PATH"
success "Removed joshbot binary from $JOSHBOT_PATH"

# Remove systemd service if it exists
if [ -f "/etc/systemd/system/joshbot.service" ]; then
    info "Removing systemd service..."
    systemctl stop joshbot 2>/dev/null || true
    systemctl disable joshbot 2>/dev/null || true
    rm -f /etc/systemd/system/joshbot.service
    systemctl daemon-reload
    success "Removed systemd service"
fi

# Remove launchd service if it exists (macOS)
if [ -f "$HOME/Library/LaunchAgents/com.joshbot.plist" ]; then
    info "Removing launchd service..."
    launchctl unload "$HOME/Library/LaunchAgents/com.joshbot.plist" 2>/dev/null || true
    rm -f "$HOME/Library/LaunchAgents/com.joshbot.plist"
    success "Removed launchd service"
fi

# Remove legacy pipx installation if it exists
if command -v pipx &>/dev/null && pipx list 2>/dev/null | grep -q joshbot; then
    info "Removing legacy pipx installation..."
    pipx uninstall joshbot
    success "Removed pipx installation"
fi

# Ask about data directory cleanup
if [[ -d "$HOME/.joshbot" ]]; then
    if [[ -r /dev/tty ]]; then
        printf "Remove ~/.joshbot/ data directory? This includes config, memory, and sessions. (y/N): " >&2
        read -r answer < /dev/tty
        if [[ "$answer" =~ ^[Yy]$ ]]; then
            rm -rf "$HOME/.joshbot"
            success "Removed ~/.joshbot/"
        else
            info "Kept ~/.joshbot/"
        fi
    else
        info "Skipped data cleanup (non-interactive)"
    fi
fi

success "Uninstall complete"
