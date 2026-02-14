#!/usr/bin/env bash
set -euo pipefail

GREEN='\033[0;32m' RED='\033[0;31m' YELLOW='\033[1;33m' RESET='\033[0m'
info() { printf "${GREEN}[INFO]${RESET} %s\n" "$1"; }
success() { printf "${GREEN}[OK]${RESET} %s\n" "$1"; }
warn() { printf "${YELLOW}[WARN]${RESET} %s\n" "$1"; }

if ! command -v joshbot &>/dev/null && ! pipx list 2>/dev/null | grep -q joshbot; then
    warn "joshbot is not installed"
    exit 0
fi

pipx uninstall joshbot
success "joshbot uninstalled"

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
