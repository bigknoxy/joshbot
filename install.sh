#!/bin/bash
#
# joshbot install script
# One-line installer for joshbot Go binary
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/bigknoxy/joshbot/main/install.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/bigknoxy/joshbot/v0.1.0/install.sh | bash
#
# Options:
#   -b, --bin-dir DIR       Install binary to DIR (default: ~/.local/bin or /usr/local/bin)
#   -v, --version VERSION  Install specific version (default: latest)
#   -f, --force            Overwrite existing installation
#   -h, --help            Show this help message
#
# Environment:
#   JOSHBOT_SKIP_CHECKSUM=1  Install even if the release checksums cannot be
#                            fetched. Off by default: verification being
#                            unavailable must not quietly become no verification.
#

set -e

# Any unexpected failure must say something. Without this, `set -e` aborts the
# script silently: `--bin-dir` with no value died on `shift 2` and printed
# nothing at all, and a failed `mv` left only the raw mv error with no context.
# TEMP_DIR is cleaned up by the single EXIT trap below. There must be exactly
# one: a second `trap ... EXIT` silently replaces the first, and download_binary
# used to install its own cleanup trap that removed this error reporting.
TEMP_DIR=""
HANDLED=false

# die reports a diagnosed failure and stops. Every line is printed to stderr so
# a caller piping stdout still sees why it failed.
die() {
    HANDLED=true
    local line
    for line in "$@"; do
        echo "$line" >&2
    done
    exit 1
}

on_exit() {
    local status=$?
    [ -n "$TEMP_DIR" ] && rm -rf "$TEMP_DIR"
    # Only for failures we did not diagnose ourselves. A `die` message is
    # already actionable; appending "Installation failed" to it is noise.
    if [ "$status" -ne 0 ] && [ "$HANDLED" != "true" ]; then
        echo "" >&2
        echo "Installation failed (exit $status)." >&2
        echo "Report this at https://github.com/bigknoxy/joshbot/issues" >&2
    fi
    exit $status
}
trap on_exit EXIT

# require_value exits with a usable message when a flag is missing its argument.
require_value() {
    if [ -z "${2:-}" ]; then
        die "Error: $1 needs a value (e.g. $1 $3)"
    fi
}

# Configuration
REPO="bigknoxy/joshbot"
BINARY_NAME="joshbot"

# Default values
INSTALL_DIR=""
VERSION="latest"
FORCE=false

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -b|--bin-dir)
            require_value "--bin-dir" "${2:-}" "~/.local/bin"
            INSTALL_DIR="$2"
            shift 2
            ;;
        -v|--version)
            require_value "--version" "${2:-}" "v1.45.2"
            VERSION="$2"
            shift 2
            ;;
        -f|--force)
            FORCE=true
            shift
            ;;
        -h|--help)
            cat << 'EOF'
joshbot installer

Usage:
    curl -fsSL https://raw.githubusercontent.com/bigknoxy/joshbot/main/install.sh | bash
    curl -fsSL https://raw.githubusercontent.com/bigknoxy/joshbot/main/install.sh | bash -s -- --version v0.1.0

Options:
    -b, --bin-dir DIR       Install binary to DIR (default: ~/.local/bin or /usr/local/bin)
    -v, --version VERSION  Install specific version (default: latest)
    -f, --force            Overwrite existing installation
    -h, --help            Show this help message

Environment:
    JOSHBOT_SKIP_CHECKSUM=1  Install even if the release checksums cannot be
                             fetched (not recommended)

EOF
            exit 0
            ;;
        *)
            die "Unknown option: $1" "Run with --help to see the available options."
            ;;
    esac
done

# Detect OS and architecture
detect_os() {
    local os
    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    case "$os" in
        linux*) echo "linux" ;;
        darwin*) echo "darwin" ;;
        msys*|mingw*|cygwin*) echo "windows" ;;
        *) echo "$os" ;;
    esac
}

detect_arch() {
    local arch
    arch=$(uname -m)
    case "$arch" in
        x86_64) echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        armv7) echo "armv7" ;;
        *) echo "$arch" ;;
    esac
}

# Version comparison (returns 0 if $1 < $2)
version_lt() {
    [ "$1" != "$2" ] && [ "$(printf '%s\n' "$1" "$2" | sort -V | head -n1)" = "$1" ]
}

# Get current installed version (if any)
get_current_version() {
    if command -v joshbot &> /dev/null; then
        joshbot --version 2>/dev/null | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' || echo ""
    else
        echo ""
    fi
}

# Get latest version from GitHub API
get_latest_version() {
    if [ "$VERSION" != "latest" ]; then
        echo "$VERSION"
        return
    fi
    
    local version
    version=$(curl -sSL "https://api.github.com/repos/${REPO}/releases/latest" | grep -o '"tag_name": "[^"]*' | cut -d'"' -f4)
    
    if [ -z "$version" ]; then
        die "Error: could not determine the latest joshbot version." \
            "GitHub may be unreachable, or rate-limiting this address." \
            "Retry, or pin a version with --version (e.g. --version v1.45.2)."
    fi
    
    echo "$version"
}

# Determine install directory
get_install_dir() {
    if [ -n "$INSTALL_DIR" ]; then
        # Create it. An explicit --bin-dir that does not exist yet is a normal
        # request, not an error — and leaving it missing meant the install died
        # on a raw "mv: No such file or directory" after already reporting a
        # successful download and checksum.
        if [ ! -d "$INSTALL_DIR" ] && ! mkdir -p "$INSTALL_DIR" 2>/dev/null; then
            die "Error: could not create install directory: ${INSTALL_DIR}" \
                "Choose a writable location with --bin-dir, or create it first."
        fi
        echo "$INSTALL_DIR"
        return
    fi
    
    # Check for explicit PATH directory
    for dir in "$HOME/.local/bin" "/usr/local/bin" "/opt/joshbot/bin"; do
        if [ -d "$dir" ] || [ -w "$(dirname "$dir")" ]; then
            # Prefer ~/.local/bin if it exists or we can create it
            if [ "$dir" = "$HOME/.local/bin" ]; then
                if [ ! -d "$dir" ]; then
                    mkdir -p "$dir"
                fi
                echo "$dir"
                return
            fi
            # Otherwise use /usr/local/bin if writable
            if [ -w "/usr/local/bin" ]; then
                echo "/usr/local/bin"
                return
            fi
        fi
    done
    
    # Fallback to ~/.local/bin (create if needed)
    mkdir -p "$HOME/.local/bin"
    echo "$HOME/.local/bin"
}

# Download and verify binary
download_binary() {
    local version="$1"
    local os="$2"
    local arch="$3"
    local install_dir="$4"
    local current_version="${5:-}"
    
    # Normalize version (remove 'v' prefix if present)
    local version_normalized="${version#v}"
    
    # Build download URLs (try multiple naming patterns)
    # Pattern 1: Binary with v-prefixed version (current format: joshbot_v1.3.0_linux_amd64)
    local binary_filename_v="${BINARY_NAME}_${version}_${os}_${arch}"
    # Pattern 2: Archive with version (normalized, no v prefix)
    local archive_filename="${BINARY_NAME}_${version_normalized}_${os}_${arch}"
    # Pattern 3: Binary without version (GoReleaser default)
    local binary_filename="${BINARY_NAME}_${os}_${arch}"
    # Pattern 4: Binary with double underscore (old format)
    local binary_filename_alt="${BINARY_NAME}__${os}_${arch}"
    
    local extension=""
    local binary_ext=""
    if [ "$os" = "windows" ]; then
        extension=".zip"
        binary_ext=".exe"
    else
        extension=".tar.gz"
    fi
    local archive="${archive_filename}${extension}"
    
    local binary_url_v="https://github.com/${REPO}/releases/download/${version}/${binary_filename_v}${binary_ext}"
    local archive_url="https://github.com/${REPO}/releases/download/${version}/${archive}"
    local binary_url="https://github.com/${REPO}/releases/download/${version}/${binary_filename}"
    local binary_url_alt="https://github.com/${REPO}/releases/download/${version}/${binary_filename_alt}"
    
    echo "Downloading joshbot ${version} for ${os}/${arch}..."
    
    # Create temp directory. Cleanup is handled by the single EXIT trap; adding
    # one here would replace it and lose the failure message.
    local temp_dir
    temp_dir=$(mktemp -d)
    TEMP_DIR="$temp_dir"
    
    # Try to download archive first, fall back to raw binary
    local use_archive=false
    local downloaded_file=""
    
    # Try archive download first
    echo "  Trying archive: ${archive}..."
    if curl -fsSL -o "${temp_dir}/${archive}" "$archive_url" 2>/dev/null; then
        use_archive=true
        downloaded_file="${temp_dir}/${archive}"
        echo "  ✓ Archive downloaded"
    else
        # Fall back to raw binary (try several naming patterns)
        echo "  Archive not found, trying raw binary..."
        # Try v-prefixed binary first (current format)
        local dest0="${temp_dir}/$(basename "$binary_url_v")"
        if curl -fsSL -o "$dest0" "$binary_url_v" 2>/dev/null; then
            downloaded_file="$dest0"
            echo "  ✓ Binary downloaded: $(basename "$dest0")"
        else
            # Try default binary name
            local dest1="${temp_dir}/$(basename "$binary_url")"
            if curl -fsSL -o "$dest1" "$binary_url" 2>/dev/null; then
                downloaded_file="$dest1"
                echo "  ✓ Binary downloaded: $(basename "$dest1")"
            else
                # Try alternate binary name (double underscore)
                local dest2="${temp_dir}/$(basename "$binary_url_alt")"
                if curl -fsSL -o "$dest2" "$binary_url_alt" 2>/dev/null; then
                    downloaded_file="$dest2"
                    echo "  ✓ Binary downloaded: $(basename "$dest2")"
                else
                    echo "Error: no joshbot ${version} build for ${os}/${arch}." >&2
                    echo "" >&2
                    echo "Check that the release exists and publishes this platform:" >&2
                    echo "  https://github.com/${REPO}/releases/tag/${version}" >&2
                    echo "" >&2
                    echo "URLs tried:" >&2
                    echo "  - ${binary_url_v}" >&2
                    echo "  - ${archive_url}" >&2
                    echo "  - ${binary_url}" >&2
                    echo "  - ${binary_url_alt}" >&2
                    HANDLED=true
                    exit 1
                fi
            fi
        fi
    fi
    
    # Verify checksum if available
    echo "Checking for checksums..."
    local checksum_url="https://github.com/${REPO}/releases/download/${version}/checksums.txt"
    local checksums
    if checksums=$(curl -fsSL "$checksum_url" 2>/dev/null); then
        local checksum=""
        if [ "$use_archive" = true ]; then
            checksum=$(echo "$checksums" | grep -i "${archive}" | awk '{print $1}' | tr '[:upper:]' '[:lower:]')
        else
            # Try to match the downloaded file name in checksums
            local downloaded_basename
            downloaded_basename=$(basename "$downloaded_file")
            checksum=$(echo "$checksums" | grep -i "${downloaded_basename}" | awk '{print $1}' | tr '[:upper:]' '[:lower:]')
            # Fallback to old pattern matching
            if [ -z "$checksum" ]; then
                checksum=$(echo "$checksums" | grep -i "${binary_filename_v}" | awk '{print $1}' | tr '[:upper:]' '[:lower:]')
            fi
            if [ -z "$checksum" ]; then
                checksum=$(echo "$checksums" | grep -i "${binary_filename}" | awk '{print $1}' | tr '[:upper:]' '[:lower:]')
            fi
        fi
        
        if [ -n "$checksum" ]; then
            local actual_checksum
            actual_checksum=$(shasum -a 256 "$downloaded_file" 2>/dev/null | awk '{print $1}' | tr '[:upper:]' '[:lower:]')
            
            if [ "$checksum" = "$actual_checksum" ]; then
                echo "  ✓ Checksum verified"
            else
                die "Error: checksum mismatch — the download is corrupted or tampered with." \
                    "Expected: $checksum" \
                    "Actual:   $actual_checksum" \
                    "Nothing was installed."
            fi
        else
            die "Error: ${downloaded_basename} is not listed in the release checksums." \
                "Refusing to install a binary that cannot be verified." \
                "Set JOSHBOT_SKIP_CHECKSUM=1 to override, at your own risk."
        fi
    else
        # Fail closed. Every joshbot release publishes checksums.txt, so its
        # absence means the download path is not what it should be — and
        # "verification unavailable" must never quietly become "not verified".
        if [ "${JOSHBOT_SKIP_CHECKSUM:-}" = "1" ]; then
            echo "  ! Checksums unavailable — continuing because JOSHBOT_SKIP_CHECKSUM=1"
        else
            die "Error: could not fetch the release checksums from:" \
                "  ${checksum_url}" \
                "Refusing to install an unverified binary." \
                "Set JOSHBOT_SKIP_CHECKSUM=1 to override, at your own risk."
        fi
    fi
    
    # Extract or prepare binary
    echo "Installing to ${install_dir}..."
    
    local binary=""
    if [ "$use_archive" = true ]; then
        # Extract from archive
        if [ "$os" = "windows" ]; then
            unzip -j -o "$downloaded_file" "${BINARY_NAME}.exe" -d "$temp_dir" > /dev/null 2>&1 || true
            binary="${temp_dir}/${BINARY_NAME}.exe"
        else
            tar -xzf "$downloaded_file" -C "$temp_dir" "$BINARY_NAME" 2>/dev/null || true
            binary="${temp_dir}/${BINARY_NAME}"
        fi
    else
        # Use raw binary directly (use the downloaded file path)
        binary="${downloaded_file}"
    fi
    
    # Check if binary exists
    if [ ! -f "$binary" ]; then
        die "Error: could not find the joshbot binary after download." \
            "The release layout may have changed; please report this."
    fi
    
    # Make executable
    chmod +x "$binary"
    
    # Check if binary already exists
    local target="${install_dir}/${BINARY_NAME}"
    if [ "$os" = "windows" ]; then
        target="${install_dir}/${BINARY_NAME}.exe"
    fi
    
    if [ -f "$target" ] && [ "$FORCE" = "false" ] && [ -z "$current_version" ]; then
        die "Error: joshbot is already installed at ${target}." \
            "Re-run with --force to overwrite it."
    fi
    
    # Force overwrite during upgrade
    if [ -f "$target" ] && [ -n "$current_version" ]; then
        FORCE=true
    fi
    
    if [ -f "$target" ] && [ "$FORCE" = "false" ]; then
        die "Error: joshbot is already installed at ${target}." \
            "Re-run with --force to overwrite it."
    fi
    
    # Stage inside the install directory, then rename over the target.
    #
    # A direct mv from the temp dir is a cross-filesystem copy, which is not
    # atomic: an interrupted install would leave a truncated binary at the very
    # path the user runs. A rename within one directory is atomic, so the target
    # is either the old binary or the complete new one.
    local staged="${target}.new-$$"
    if ! cp -f "$binary" "$staged"; then
        die "Error: could not write to ${install_dir}." \
            "The existing joshbot, if any, was left untouched."
    fi
    chmod +x "$staged"
    if ! mv -f "$staged" "$target"; then
        rm -f "$staged"
        die "Error: could not install to ${target}." \
            "The existing joshbot, if any, was left untouched."
    fi
    
    echo ""
    if [ -n "$current_version" ]; then
        echo "✓ Successfully upgraded joshbot to ${version} in ${install_dir}"
    else
        echo "✓ Successfully installed joshbot ${version} to ${install_dir}"
    fi
}

# Main
main() {
    local os arch version install_dir current_version
    
    os=$(detect_os)
    arch=$(detect_arch)
    
    # Get current installed version
    current_version=$(get_current_version)
    
    # Get latest version
    version=$(get_latest_version)
    
    # Check if already up to date
    if [ -n "$current_version" ] && [ "$current_version" = "$version" ]; then
        echo "joshbot $current_version is already installed and up to date"
        exit 0
    fi
    
    # Show upgrade message if applicable
    if [ -n "$current_version" ]; then
        echo "Upgrading joshbot $current_version → $version..."
    fi
    
    install_dir=$(get_install_dir)
    
    echo "Detected: ${os}/${arch}"
    if [ -z "$current_version" ]; then
        echo "Installing to: ${install_dir}"
    else
        echo "Installing to: ${install_dir} (upgrading)"
    fi
    
    # Check write permission on the directory we will actually write into.
    #
    # This used to also accept a writable *parent*, which is not the same thing:
    # a read-only install dir inside a writable parent passed the check and then
    # failed at the mv with a bare "Permission denied".
    if [ ! -w "$install_dir" ]; then
        die "Error: no write permission for install directory: ${install_dir}" \
            "Re-run with --bin-dir pointing somewhere writable (e.g. ~/.local/bin)," \
            "or with sudo if you meant to install system-wide."
    fi
    
    download_binary "$version" "$os" "$arch" "$install_dir" "$current_version"
    
    # Check if directory is in PATH
    if [[ ":$PATH:" != *":${install_dir}:"* ]]; then
        echo ""
        echo "IMPORTANT: ${install_dir} is not in your PATH."
        echo ""
        echo "Add this to your shell profile (~/.bashrc, ~/.zshrc, etc.):"
        echo ""
        if [ "$install_dir" = "$HOME/.local/bin" ]; then
            echo "    export PATH=\"\$HOME/.local/bin:\$PATH\""
        else
            echo "    export PATH=\"${install_dir}:\$PATH\""
        fi
        echo ""
    fi
    
    # Try to run joshbot to verify installation
    if [ "$os" = "windows" ]; then
        local verify_bin="${install_dir}/${BINARY_NAME}.exe"
    else
        local verify_bin="${install_dir}/${BINARY_NAME}"
    fi
    
    if "$verify_bin" --version > /dev/null 2>&1; then
        echo ""
        echo "Verification: OK"
    fi
    
    echo ""
    if [ -n "$current_version" ]; then
        echo "Upgrade complete! Run 'joshbot --version' to verify."
    else
        echo "Run 'joshbot onboard' to configure joshbot!"
    fi
}

main "$@"
