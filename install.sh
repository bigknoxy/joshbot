#!/usr/bin/env bash
set -euo pipefail

# Colors with fallback for non-tty
if tput setaf 1 >/dev/null 2>&1; then
    GREEN=$(tput setaf 2)
    RED=$(tput setaf 1)
    YELLOW=$(tput setaf 3)
    RESET=$(tput sgr0)
else
    GREEN='\033[0;32m'
    RED='\033[0;31m'
    YELLOW='\033[0;33m'
    RESET='\033[0m'
fi

info()  { printf "${GREEN}[INFO]${RESET} %s\n" "$1"; }
success(){ printf "${GREEN}[OK]${RESET} %s\n" "$1"; }
warn()  { printf "${YELLOW}[WARN]${RESET} %s\n" "$1"; }
error() { printf "${RED}[ERROR]${RESET} %s\n" "$1" >&2; }

# Check Python 3.11+
info "Checking Python version..."
if ! command -v python3 >/dev/null 2>&1; then
    error "python3 not found. Install Python 3.11+ from https://python.org"
    exit 1
fi
PYTHON_VERSION=$(python3 -c 'import sys; print(f"{sys.version_info.major}.{sys.version_info.minor}")')
PYTHON_MAJOR=${PYTHON_VERSION%%.*}
PYTHON_MINOR=${PYTHON_VERSION##*.}
if [[ "$PYTHON_MAJOR" -lt 3 ]] || [[ "$PYTHON_MAJOR" -eq 3 && "$PYTHON_MINOR" -lt 11 ]]; then
    error "Python 3.11+ required, found $PYTHON_VERSION"
    exit 1
fi
success "Python $PYTHON_VERSION"

# Check/install pipx
info "Checking pipx..."
PIPX="pipx"
if ! command -v pipx >/dev/null 2>&1; then
    if python3 -m pipx --version >/dev/null 2>&1; then
        PIPX="python3 -m pipx"
    else
        warn "pipx not found, installing..."
        PIPX_INSTALLED=false

        # Method 1: pip (most common)
        if python3 -m pip install --user pipx 2>/dev/null; then
            PIPX_INSTALLED=true
        # Method 2: bootstrap pip via ensurepip, then install pipx
        elif python3 -m ensurepip --user 2>/dev/null && python3 -m pip install --user pipx 2>/dev/null; then
            PIPX_INSTALLED=true
        # Method 3: system package manager (sudo as last resort)
        elif command -v apt-get >/dev/null 2>&1; then
            info "pip not available, trying: sudo apt-get install -y pipx"
            if sudo apt-get update -qq && sudo apt-get install -y -qq pipx; then
                PIPX_INSTALLED=true
            fi
        elif command -v dnf >/dev/null 2>&1; then
            info "pip not available, trying: sudo dnf install -y pipx"
            if sudo dnf install -y pipx 2>/dev/null; then
                PIPX_INSTALLED=true
            fi
        elif command -v brew >/dev/null 2>&1; then
            info "pip not available, trying: brew install pipx"
            if brew install pipx 2>/dev/null; then
                PIPX_INSTALLED=true
            fi
        fi

        if [[ "$PIPX_INSTALLED" != "true" ]]; then
            error "Could not install pipx. Please install it manually:"
            error "  https://pipx.pypa.io/stable/installation/"
            exit 1
        fi

        # Determine how to invoke pipx
        if command -v pipx >/dev/null 2>&1; then
            PIPX="pipx"
        else
            PIPX="python3 -m pipx"
        fi
        $PIPX ensurepath >/dev/null 2>&1 || true
        warn "You may need to restart your shell for PATH changes."
    fi
fi
success "pipx available"

# Check if already installed
if command -v joshbot >/dev/null 2>&1; then
    warn "joshbot is already installed."
    info "To upgrade: pipx upgrade joshbot --pip-args='--force-reinstall'"
    exit 0
fi

# Install joshbot
info "Installing joshbot from GitHub..."
$PIPX install "joshbot @ git+https://github.com/bigknoxy/joshbot.git" 2>&1 | tail -3

# Verify — check PATH and common pipx locations
JOSHBOT_BIN=""
if command -v joshbot >/dev/null 2>&1; then
    JOSHBOT_BIN="joshbot"
elif [[ -x "$HOME/.local/bin/joshbot" ]]; then
    JOSHBOT_BIN="$HOME/.local/bin/joshbot"
fi

if [[ -n "$JOSHBOT_BIN" ]]; then
    success "joshbot installed!"
    printf "\n  Next steps:\n"
    printf "    joshbot onboard    # First-time setup\n"
    printf "    joshbot agent      # Start chatting\n\n"
    if ! command -v joshbot >/dev/null 2>&1; then
        warn "Add ~/.local/bin to your PATH, then restart your shell."
    fi
else
    error "Installation may have failed. Try: python3 -m pipx run joshbot --help"
    exit 1
fi
