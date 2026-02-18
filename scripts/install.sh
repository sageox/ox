#!/usr/bin/env bash
#
# SageOx (ox) installation script
# Usage: curl -sSL https://raw.githubusercontent.com/sageox/ox/main/scripts/install.sh | bash
#
# IMPORTANT: This script must be EXECUTED, never SOURCED
#   WRONG: source install.sh (will exit your shell on errors)
#   CORRECT: bash install.sh
#   CORRECT: curl -sSL ... | bash

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

REPO="sageox/ox"
BINARY="ox"
LAST_INSTALL_PATH=""

log_info() {
    echo -e "${BLUE}==>${NC} $1"
}

log_success() {
    echo -e "${GREEN}==>${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}==>${NC} $1"
}

log_error() {
    echo -e "${RED}Error:${NC} $1" >&2
}

release_has_asset() {
    local release_json=$1
    local asset_name=$2

    if echo "$release_json" | grep -Fq "\"name\": \"$asset_name\""; then
        return 0
    fi

    return 1
}

# Re-sign binary for macOS to avoid slow Gatekeeper checks
resign_for_macos() {
    local binary_path=$1

    # Only run on macOS
    if [[ "$(uname -s)" != "Darwin" ]]; then
        return 0
    fi

    # Check if codesign is available
    if ! command -v codesign &> /dev/null; then
        log_warning "codesign not found, skipping re-signing"
        return 0
    fi

    log_info "Re-signing binary for macOS..."
    codesign --remove-signature "$binary_path" 2>/dev/null || true
    if codesign --force --sign - "$binary_path"; then
        log_success "Binary re-signed for this machine"
    else
        log_warning "Failed to re-sign binary (non-fatal)"
    fi
}

# Detect OS and architecture
detect_platform() {
    local os arch

    case "$(uname -s)" in
        Darwin)
            os="darwin"
            ;;
        Linux)
            os="linux"
            ;;
        FreeBSD)
            os="freebsd"
            ;;
        *)
            log_error "Unsupported operating system: $(uname -s)"
            log_error "For Windows, download manually from https://github.com/$REPO/releases"
            exit 1
            ;;
    esac

    case "$(uname -m)" in
        x86_64|amd64)
            arch="amd64"
            ;;
        aarch64|arm64)
            arch="arm64"
            ;;
        *)
            log_error "Unsupported architecture: $(uname -m)"
            exit 1
            ;;
    esac

    echo "${os}_${arch}"
}

# Download and install from GitHub releases
install_from_release() {
    log_info "Installing $BINARY from GitHub releases..."

    local platform=$1
    local tmp_dir
    tmp_dir=$(mktemp -d)

    # Get latest release version
    log_info "Fetching latest release..."
    local latest_url="https://api.github.com/repos/$REPO/releases/latest"
    local version
    local release_json

    if command -v curl &> /dev/null; then
        release_json=$(curl -fsSL "$latest_url")
    elif command -v wget &> /dev/null; then
        release_json=$(wget -qO- "$latest_url")
    else
        log_error "Neither curl nor wget found. Please install one of them."
        return 1
    fi

    version=$(echo "$release_json" | grep '"tag_name"' | sed -E 's/.*"tag_name": "([^"]+)".*/\1/')

    if [ -z "$version" ]; then
        log_error "Failed to fetch latest version"
        return 1
    fi

    log_info "Latest version: $version"

    # Download URL
    local archive_name="ox_${version#v}_${platform}.tar.gz"
    local download_url="https://github.com/$REPO/releases/download/${version}/${archive_name}"

    if ! release_has_asset "$release_json" "$archive_name"; then
        log_warning "No prebuilt archive available for platform ${platform}. Falling back to source installation methods."
        rm -rf "$tmp_dir"
        return 1
    fi

    log_info "Downloading $archive_name..."

    cd "$tmp_dir"
    if command -v curl &> /dev/null; then
        if ! curl -fsSL -o "$archive_name" "$download_url"; then
            log_error "Download failed"
            cd - > /dev/null || cd "$HOME"
            rm -rf "$tmp_dir"
            return 1
        fi
    elif command -v wget &> /dev/null; then
        if ! wget -q -O "$archive_name" "$download_url"; then
            log_error "Download failed"
            cd - > /dev/null || cd "$HOME"
            rm -rf "$tmp_dir"
            return 1
        fi
    fi

    # Verify checksum if available
    local checksums_url="https://github.com/$REPO/releases/download/${version}/checksums.txt"
    if command -v curl &> /dev/null; then
        curl -fsSL -o checksums.txt "$checksums_url" 2>/dev/null || true
    elif command -v wget &> /dev/null; then
        wget -q -O checksums.txt "$checksums_url" 2>/dev/null || true
    fi

    if [[ -f checksums.txt ]]; then
        log_info "Verifying checksum..."
        if command -v sha256sum &> /dev/null; then
            sha256sum -c checksums.txt --ignore-missing --quiet 2>/dev/null || log_warning "Checksum verification failed (continuing anyway)"
        elif command -v shasum &> /dev/null; then
            shasum -a 256 -c checksums.txt --ignore-missing --quiet 2>/dev/null || log_warning "Checksum verification failed (continuing anyway)"
        else
            log_warning "No sha256sum or shasum available, skipping verification"
        fi
    fi

    # Extract archive
    log_info "Extracting archive..."
    if ! tar -xzf "$archive_name"; then
        log_error "Failed to extract archive"
        cd - > /dev/null || cd "$HOME"
        rm -rf "$tmp_dir"
        return 1
    fi

    # Determine install location
    local install_dir
    if [[ -w /usr/local/bin ]]; then
        install_dir="/usr/local/bin"
    else
        install_dir="$HOME/.local/bin"
        mkdir -p "$install_dir"
    fi

    # Install binary
    log_info "Installing to $install_dir..."
    if [[ -w "$install_dir" ]]; then
        mv "$BINARY" "$install_dir/"
    else
        sudo mv "$BINARY" "$install_dir/"
    fi
    chmod +x "$install_dir/$BINARY"

    # Re-sign for macOS to avoid Gatekeeper delays
    resign_for_macos "$install_dir/$BINARY"

    LAST_INSTALL_PATH="$install_dir/$BINARY"
    log_success "$BINARY installed to $install_dir/$BINARY"

    # Check if install_dir is in PATH
    if [[ ":$PATH:" != *":$install_dir:"* ]]; then
        log_warning "$install_dir is not in your PATH"
        echo ""
        echo "Add this to your shell profile (~/.bashrc, ~/.zshrc, etc.):"
        echo "  export PATH=\"\$PATH:$install_dir\""
        echo ""
    fi

    cd - > /dev/null || cd "$HOME"
    rm -rf "$tmp_dir"
    return 0
}

# Check if Go is installed and meets minimum version
check_go() {
    if command -v go &> /dev/null; then
        local go_version=$(go version | awk '{print $3}' | sed 's/go//')
        log_info "Go detected: $(go version)"

        local major=$(echo "$go_version" | cut -d. -f1)
        local minor=$(echo "$go_version" | cut -d. -f2)

        if [ "$major" -eq 1 ] && [ "$minor" -lt 24 ]; then
            log_error "Go 1.24 or later is required (found: $go_version)"
            echo ""
            echo "Please upgrade Go:"
            echo "  - Download from https://go.dev/dl/"
            echo "  - Or use your package manager to update"
            echo ""
            return 1
        fi

        return 0
    else
        return 1
    fi
}

# Install using go install (fallback)
install_with_go() {
    log_info "Installing $BINARY using 'go install'..."

    if go install github.com/$REPO/cmd/ox@latest; then
        log_success "$BINARY installed successfully via go install"

        local gobin
        gobin=$(go env GOBIN 2>/dev/null || true)
        if [ -n "$gobin" ]; then
            bin_dir="$gobin"
        else
            bin_dir="$(go env GOPATH)/bin"
        fi
        LAST_INSTALL_PATH="$bin_dir/$BINARY"

        # Re-sign for macOS to avoid Gatekeeper delays
        resign_for_macos "$bin_dir/$BINARY"

        # Check if GOPATH/bin (or GOBIN) is in PATH
        if [[ ":$PATH:" != *":$bin_dir:"* ]]; then
            log_warning "$bin_dir is not in your PATH"
            echo ""
            echo "Add this to your shell profile (~/.bashrc, ~/.zshrc, etc.):"
            echo "  export PATH=\"\$PATH:$bin_dir\""
            echo ""
        fi

        return 0
    else
        log_error "go install failed"
        return 1
    fi
}

# Build from source (last resort)
build_from_source() {
    log_info "Building $BINARY from source..."

    local tmp_dir
    tmp_dir=$(mktemp -d)

    cd "$tmp_dir"
    log_info "Cloning repository..."

    if git clone --depth 1 https://github.com/$REPO.git; then
        cd ox
        log_info "Building binary..."

        if go build -o "$BINARY" ./cmd/ox; then
            # Determine install location
            local install_dir
            if [[ -w /usr/local/bin ]]; then
                install_dir="/usr/local/bin"
            else
                install_dir="$HOME/.local/bin"
                mkdir -p "$install_dir"
            fi

            log_info "Installing to $install_dir..."
            if [[ -w "$install_dir" ]]; then
                mv "$BINARY" "$install_dir/"
            else
                sudo mv "$BINARY" "$install_dir/"
            fi

            # Re-sign for macOS to avoid Gatekeeper delays
            resign_for_macos "$install_dir/$BINARY"

            log_success "$BINARY installed to $install_dir/$BINARY"
            LAST_INSTALL_PATH="$install_dir/$BINARY"

            # Check if install_dir is in PATH
            if [[ ":$PATH:" != *":$install_dir:"* ]]; then
                log_warning "$install_dir is not in your PATH"
                echo ""
                echo "Add this to your shell profile (~/.bashrc, ~/.zshrc, etc.):"
                echo "  export PATH=\"\$PATH:$install_dir\""
                echo ""
            fi

            cd - > /dev/null || cd "$HOME"
            rm -rf "$tmp_dir"
            return 0
        else
            log_error "Build failed"
            cd - > /dev/null || cd "$HOME"
            rm -rf "$tmp_dir"
            return 1
        fi
    else
        log_error "Failed to clone repository"
        rm -rf "$tmp_dir"
        return 1
    fi
}

# Verify installation
verify_installation() {
    if command -v "$BINARY" &> /dev/null; then
        log_success "$BINARY is installed and ready!"
        echo ""
        $BINARY version 2>/dev/null || echo "$BINARY (development build)"
        echo ""
        echo "Get started:"
        echo "  cd your-project"
        echo "  $BINARY login"
        echo "  $BINARY init"
        echo ""
        return 0
    else
        log_error "$BINARY was installed but is not in PATH"
        return 1
    fi
}

# Main installation flow
main() {
    echo ""
    echo "SageOx (ox) Installer"
    echo ""

    log_info "Detecting platform..."
    local platform
    platform=$(detect_platform)
    log_info "Platform: $platform"

    # Try downloading from GitHub releases first
    if install_from_release "$platform"; then
        verify_installation
        exit 0
    fi

    log_warning "Failed to install from releases, trying alternative methods..."

    # Try go install as fallback
    if check_go; then
        if install_with_go; then
            verify_installation
            exit 0
        fi
    fi

    # Try building from source as last resort
    log_warning "Falling back to building from source..."

    if ! check_go; then
        log_warning "Go is not installed"
        echo ""
        echo "$BINARY requires Go 1.24 or later to build from source. You can:"
        echo "  1. Install Go from https://go.dev/dl/"
        echo "  2. Use your package manager:"
        echo "     - macOS: brew install go"
        echo "     - Ubuntu/Debian: sudo apt install golang"
        echo "     - Other Linux: Check your distro's package manager"
        echo ""
        echo "After installing Go, run this script again."
        exit 1
    fi

    if build_from_source; then
        verify_installation
        exit 0
    fi

    # All methods failed
    log_error "Installation failed"
    echo ""
    echo "Manual installation:"
    echo "  1. Download from https://github.com/$REPO/releases/latest"
    echo "  2. Extract and move '$BINARY' to your PATH"
    echo ""
    echo "Or install from source:"
    echo "  1. Install Go from https://go.dev/dl/"
    echo "  2. Run: go install github.com/$REPO/cmd/ox@latest"
    echo ""
    exit 1
}

main "$@"
