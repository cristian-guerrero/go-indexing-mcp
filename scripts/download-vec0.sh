#!/usr/bin/env bash
# Download sqlite-vec loadable extension for go-indexing-mcp.
set -euo pipefail

VERSION="0.1.9"
REPO="asg017/sqlite-vec"
BASE_URL="https://github.com/$REPO/releases/download/v$VERSION"

detect_platform() {
    local os arch
    os="$(uname -s)"
    arch="$(uname -m)"
    case "$os" in
        Linux)
            case "$arch" in
                x86_64|amd64)  echo "linux-x86_64" ;;
                aarch64|arm64) echo "linux-aarch64" ;;
                *) echo "unsupported arch: $arch" >&2; exit 1 ;;
            esac
            ;;
        Darwin)
            case "$arch" in
                x86_64|amd64)  echo "macos-x86_64" ;;
                arm64)         echo "macos-aarch64" ;;
                *) echo "unsupported arch: $arch" >&2; exit 1 ;;
            esac
            ;;
        *) echo "unsupported OS: $os" >&2; exit 1 ;;
    esac
}

PLATFORM="${1:-$(detect_platform)}"
LIB_DIR="$HOME/.go-mcp/indexing/lib"
ARCHIVE_NAME="sqlite-vec-${VERSION}-loadable-${PLATFORM}.tar.gz"
URL="$BASE_URL/$ARCHIVE_NAME"

mkdir -p "$LIB_DIR"
TMP_ARCHIVE=$(mktemp)

echo "Downloading sqlite-vec v${VERSION} (${PLATFORM})..."
echo "URL: $URL"
curl -fsSL "$URL" -o "$TMP_ARCHIVE"

echo "Extracting to $LIB_DIR..."
tar -xzf "$TMP_ARCHIVE" -C "$LIB_DIR"

ls -lh "$LIB_DIR"
rm -f "$TMP_ARCHIVE"
echo "Done! sqlite-vec extension installed at $LIB_DIR"
