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

set -e

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
            INSTALL_DIR="$2"
            shift 2
            ;;
        -v|--version)
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

EOF
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
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

# Get latest version from GitHub API
get_latest_version() {
    if [ "$VERSION" != "latest" ]; then
        echo "$VERSION"
        return
    fi
    
    local version
    version=$(curl -sSL "https://api.github.com/repos/${REPO}/releases/latest" | grep -o '"tag_name": "[^"]*' | cut -d'"' -f4)
    
    if [ -z "$version" ]; then
        echo "Error: Could not determine latest version" >&2
        exit 1
    fi
    
    echo "$version"
}

# Determine install directory
get_install_dir() {
    if [ -n "$INSTALL_DIR" ]; then
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
    
    # Normalize version (remove 'v' prefix if present)
    local version_normalized="${version#v}"
    
    # Build download URLs (try multiple naming patterns)
    # Pattern 1: Archive with version (preferred)
    local archive_filename="${BINARY_NAME}_${version_normalized}_${os}_${arch}"
    # Pattern 2: Raw binary without version (GoReleaser default)
    local binary_filename="${BINARY_NAME}_${os}_${arch}"
    # Pattern 3: Binary with double underscore (GoReleaser bug)
    local binary_filename_alt="${BINARY_NAME}__${os}_${arch}"
    
    local extension=""
    if [ "$os" = "windows" ]; then
        extension=".zip"
    else
        extension=".tar.gz"
    fi
    local archive="${archive_filename}${extension}"
    
    local archive_url="https://github.com/${REPO}/releases/download/${version}/${archive}"
    local binary_url="https://github.com/${REPO}/releases/download/${version}/${binary_filename}"
    local binary_url_alt="https://github.com/${REPO}/releases/download/${version}/${binary_filename_alt}"
    
    echo "Downloading joshbot ${version} for ${os}/${arch}..."
    
    # Create temp directory
    local temp_dir
    temp_dir=$(mktemp -d)
    trap "rm -rf $temp_dir" EXIT
    
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
                echo "Error: Failed to download joshbot ${version} for ${os}/${arch}" >&2
                echo "Tried:" >&2
                echo "  - ${archive_url}" >&2
                echo "  - ${binary_url}" >&2
                echo "  - ${binary_url_alt}" >&2
                exit 1
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
            checksum=$(echo "$checksums" | grep -i "${binary_filename}" | awk '{print $1}' | tr '[:upper:]' '[:lower:]')
        fi
        
        if [ -n "$checksum" ]; then
            local actual_checksum
            actual_checksum=$(shasum -a 256 "$downloaded_file" 2>/dev/null | awk '{print $1}' | tr '[:upper:]' '[:lower:]')
            
            if [ "$checksum" = "$actual_checksum" ]; then
                echo "  ✓ Checksum verified"
            else
                echo "Warning: Checksum mismatch (continuing anyway)" >&2
            fi
        else
            echo "  Checksum not found in release"
        fi
    else
        echo "  No checksums available"
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
        echo "Error: Failed to locate binary after download" >&2
        exit 1
    fi
    
    # Make executable
    chmod +x "$binary"
    
    # Check if binary already exists
    local target="${install_dir}/${BINARY_NAME}"
    if [ "$os" = "windows" ]; then
        target="${install_dir}/${BINARY_NAME}.exe"
    fi
    
    if [ -f "$target" ] && [ "$FORCE" = "false" ]; then
        echo "Error: Binary already exists at ${target}. Use --force to overwrite." >&2
        exit 1
    fi
    
    # Move binary to install directory
    mv -f "$binary" "$target"
    
    echo ""
    echo "✓ Successfully installed joshbot ${version} to ${install_dir}"
}

# Main
main() {
    local os arch version install_dir
    
    os=$(detect_os)
    arch=$(detect_arch)
    version=$(get_latest_version)
    install_dir=$(get_install_dir)
    
    echo "Detected: ${os}/${arch}"
    echo "Installing to: ${install_dir}"
    
    download_binary "$version" "$os" "$arch" "$install_dir"
    
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
    echo "Run 'joshbot onboard' to configure joshbot!"
}

main "$@"
