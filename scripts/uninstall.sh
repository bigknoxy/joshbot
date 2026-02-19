#!/bin/bash
#
# joshbot uninstall script
# Safely removes joshbot Go binary and optionally configuration
#
# Usage:
#   ./uninstall.sh              # Interactive mode
#   ./uninstall.sh --force      # Skip confirmations
#   ./uninstall.sh --keep-config # Don't ask about config
#   ./uninstall.sh --help       # Show help
#

set -euo pipefail

# Configuration
BINARY_NAME="joshbot"
CONFIG_DIR="$HOME/.joshbot"

# Default values
FORCE=false
KEEP_CONFIG=false

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Print functions
print_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[OK]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Show help
show_help() {
    cat << 'EOF'
joshbot uninstaller

Usage:
    ./uninstall.sh              # Interactive mode
    ./uninstall.sh [options]

Options:
    -f, --force         Skip all confirmation prompts
    -k, --keep-config   Don't ask about removing configuration
    -h, --help          Show this help message

Description:
    This script removes the joshbot binary from your system and optionally
    removes the configuration directory (~/.joshbot).

    The script will:
    1. Detect where joshbot is installed
    2. Show what will be removed
    3. Ask for confirmation (unless --force)
    4. Remove the binary
    5. Ask about configuration removal (unless --keep-config)
    6. Show a summary of what was removed

EOF
}

# Parse arguments
parse_args() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            -f|--force)
                FORCE=true
                shift
                ;;
            -k|--keep-config)
                KEEP_CONFIG=true
                shift
                ;;
            -h|--help)
                show_help
                exit 0
                ;;
            *)
                print_error "Unknown option: $1"
                echo "Use --help for usage information"
                exit 1
                ;;
        esac
    done
}

# Detect where joshbot is installed
detect_joshbot() {
    local locations=()
    
    # Check common installation locations
    local check_paths=(
        "$HOME/.local/bin/$BINARY_NAME"
        "/usr/local/bin/$BINARY_NAME"
        "/usr/bin/$BINARY_NAME"
        "/opt/joshbot/bin/$BINARY_NAME"
    )
    
    for path in "${check_paths[@]}"; do
        if [ -f "$path" ] && [ -x "$path" ]; then
            locations+=("$path")
        fi
    done
    
    # Also check PATH
    if command -v "$BINARY_NAME" &> /dev/null; then
        local path_bin
        path_bin=$(command -v "$BINARY_NAME")
        
        # Avoid duplicates
        local found=false
        for loc in "${locations[@]}"; do
            if [ "$loc" = "$path_bin" ]; then
                found=true
                break
            fi
        done
        
        if [ "$found" = false ]; then
            locations+=("$path_bin")
        fi
    fi
    
    # Return locations (space-separated)
    if [ ${#locations[@]} -eq 0 ]; then
        echo ""
    else
        printf '%s\n' "${locations[@]}"
    fi
}

# Verify it's actually joshbot binary
verify_joshbot() {
    local binary="$1"
    
    # Check if file exists and is executable
    if [ ! -f "$binary" ]; then
        return 1
    fi
    
    if [ ! -x "$binary" ]; then
        return 1
    fi
    
    # Try to get version or help to verify it's joshbot
    # Use both --version and --help as different versions may support different flags
    if "$binary" --version &> /dev/null; then
        return 0
    fi
    
    if "$binary" --help &> /dev/null; then
        return 0
    fi
    
    # Check file header for ELF/Mach-O (Go binary indicator)
    if file "$binary" 2>/dev/null | grep -qE '(ELF|Mach-O)'; then
        # Likely a binary, be conservative and accept it
        return 0
    fi
    
    return 1
}

# Check if running from current directory
check_running_from_cwd() {
    local binary="$1"
    local cwd
    
    cwd=$(pwd)
    
    # Get real path of binary
    local real_binary
    real_binary=$(realpath "$binary" 2>/dev/null || echo "$binary")
    local real_cwd
    real_cwd=$(realpath "$cwd" 2>/dev/null || echo "$cwd")
    
    # Check if binary is in current directory
    if [[ "$real_binary" == "$real_cwd/"* ]] || [ "$real_binary" = "$real_cwd" ]; then
        return 0
    fi
    
    return 1
}

# Show what's in config directory
show_config_contents() {
    if [ -d "$CONFIG_DIR" ]; then
        echo ""
        echo "Contents of $CONFIG_DIR:"
        echo "------------------------"
        ls -la "$CONFIG_DIR" 2>/dev/null || echo "(unable to list)"
        echo "------------------------"
    else
        echo ""
        echo "Configuration directory does not exist: $CONFIG_DIR"
    fi
}

# Ask for confirmation
ask_confirmation() {
    local message="$1"
    
    if [ "$FORCE" = true ]; then
        return 0
    fi
    
    echo ""
    read -p "$message [y/N] " -n 1 -r
    echo ""
    
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        return 0
    fi
    
    return 1
}

# Remove binary
remove_binary() {
    local binary="$1"
    
    # Verify it's joshbot first
    if ! verify_joshbot "$binary"; then
        print_error "Cannot verify that $binary is a joshbot binary. Skipping."
        return 1
    fi
    
    # Check if running from current directory
    if check_running_from_cwd "$binary"; then
        print_error "Refusing to remove joshbot binary from current working directory."
        print_error "This prevents accidental removal of the binary you're running."
        return 1
    fi
    
    # Try to remove
    if rm -f "$binary"; then
        print_success "Removed: $binary"
        return 0
    else
        print_error "Failed to remove: $binary"
        
        # Check permissions
        if [ ! -w "$(dirname "$binary")" ]; then
            print_error "Permission denied. Try running with sudo:"
            print_error "  sudo rm $binary"
        fi
        
        return 1
    fi
}

# Remove configuration
remove_config() {
    if [ ! -d "$CONFIG_DIR" ]; then
        print_info "Configuration directory does not exist, skipping."
        return 0
    fi
    
    # Ask for confirmation
    if [ "$KEEP_CONFIG" = true ]; then
        print_info "Keeping configuration directory (--keep-config specified)"
        return 0
    fi
    
    show_config_contents
    
    if ask_confirmation "Remove configuration directory $CONFIG_DIR?"; then
        if rm -rf "$CONFIG_DIR"; then
            print_success "Removed: $CONFIG_DIR"
            return 0
        else
            print_error "Failed to remove: $CONFIG_DIR"
            return 1
        fi
    else
        print_info "Keeping configuration directory"
        return 0
    fi
}

# Main function
main() {
    local binaries=()
    local removed_count=0
    local failed_count=0
    
    echo ""
    echo "========================================"
    echo "       joshbot uninstaller"
    echo "========================================"
    echo ""
    
    # Parse arguments
    parse_args "$@"
    
    # Detect joshbot installations
    print_info "Detecting joshbot installations..."
    echo ""
    
    while IFS= read -r line; do
        if [ -n "$line" ]; then
            binaries+=("$line")
        fi
    done < <(detect_joshbot)
    
    # Check if any found
    if [ ${#binaries[@]} -eq 0 ]; then
        print_warning "No joshbot installation found."
        echo ""
        echo "joshbot is not installed in any of these locations:"
        echo "  - ~/.local/bin/$BINARY_NAME"
        echo "  - /usr/local/bin/$BINARY_NAME"
        echo "  - /usr/bin/$BINARY_NAME"
        echo "  - /opt/joshbot/bin/$BINARY_NAME"
        echo ""
        echo "Make sure joshbot is in your PATH or specify the location."
        exit 0
    fi
    
    # Show what will be removed
    echo "Found joshbot installation(s):"
    echo ""
    for binary in "${binaries[@]}"; do
        local size
        size=$(du -h "$binary" 2>/dev/null | cut -f1 || echo "unknown")
        echo "  - $binary ($size)"
    done
    echo ""
    
    # Ask for confirmation
    if ! ask_confirmation "Remove the above binary(ies)?"; then
        print_info "Aborted."
        exit 0
    fi
    
    # Remove each binary
    echo ""
    for binary in "${binaries[@]}"; do
        echo "Removing $binary..."
        if remove_binary "$binary"; then
            removed_count=$((removed_count + 1))
        else
            failed_count=$((failed_count + 1))
        fi
        echo ""
    done
    
    # Handle configuration
    echo ""
    remove_config
    
    # Show summary
    echo ""
    echo "========================================"
    echo "           Summary"
    echo "========================================"
    echo "  Binaries removed: $removed_count"
    echo "  Binaries failed:  $failed_count"
    echo "  Config kept:      $KEEP_CONFIG"
    echo "========================================"
    echo ""
    
    if [ $failed_count -gt 0 ]; then
        print_warning "Some binaries could not be removed."
        print_info "You may need to run with sudo or check permissions."
        exit 1
    fi
    
    print_success "joshbot has been uninstalled!"
    exit 0
}

# Run main
main "$@"
