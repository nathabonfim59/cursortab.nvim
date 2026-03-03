#!/usr/bin/env bash
# Usage: ./scripts/nvim-test.sh <version> [-- nvim args...]
#   version  Neovim version, e.g. 0.10.2 or v0.10.2
#
# Options:
#   -c <config>   Path to init.lua (default: scripts/init.lua)
#   -k            Keep isolated state after exit (default: delete on exit)
#
# Examples:
#   ./scripts/nvim-test.sh 0.10.2
#   ./scripts/nvim-test.sh 0.10.2 -c /tmp/myinit.lua
#   ./scripts/nvim-test.sh 0.10.2 -- somefile.lua

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLUGIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
CACHE_DIR="${XDG_CACHE_HOME:-$HOME/.cache}/nvim-versions"

# --- parse args ---
VERSION=""
CONFIG="$SCRIPT_DIR/init.lua"
KEEP=0
NVIM_ARGS=()

while [[ $# -gt 0 ]]; do
    case "$1" in
        -c) CONFIG="$2"; shift 2 ;;
        -k) KEEP=1; shift ;;
        --) shift; NVIM_ARGS+=("$@"); break ;;
        -*)
            echo "Unknown option: $1" >&2
            echo "Usage: $0 <version> [-c config] [-k] [-- nvim args...]" >&2
            exit 1
            ;;
        *)
            if [[ -z "$VERSION" ]]; then
                VERSION="$1"
            else
                NVIM_ARGS+=("$1")
            fi
            shift
            ;;
    esac
done

if [[ -z "$VERSION" ]]; then
    echo "Error: version required" >&2
    echo "Usage: $0 <version> [-c config] [-k] [-- nvim args...]" >&2
    exit 1
fi

# Normalize version (strip leading v)
VERSION="${VERSION#v}"
TAG="v$VERSION"

# --- detect platform ---
OS="$(uname -s)"
ARCH="$(uname -m)"

if [[ "$OS" != "Darwin" ]]; then
    echo "Error: only macOS is supported by this script" >&2
    exit 1
fi

case "$ARCH" in
    arm64)  NVIM_ASSET="nvim-macos-arm64" ;;
    x86_64) NVIM_ASSET="nvim-macos-x86_64" ;;
    *) echo "Error: unsupported arch: $ARCH" >&2; exit 1 ;;
esac

# --- download & cache ---
NVIM_BIN="$CACHE_DIR/$VERSION/$NVIM_ASSET/bin/nvim"

if [[ ! -x "$NVIM_BIN" ]]; then
    echo "Downloading Neovim $TAG ($NVIM_ASSET)..."
    mkdir -p "$CACHE_DIR/$VERSION"

    URL="https://github.com/neovim/neovim/releases/download/$TAG/$NVIM_ASSET.tar.gz"
    TMP_TARBALL="$(mktemp /tmp/nvim-XXXXXX.tar.gz)"

    if ! curl -fL --progress-bar -o "$TMP_TARBALL" "$URL"; then
        echo "Error: failed to download $URL" >&2
        rm -f "$TMP_TARBALL"
        exit 1
    fi

    tar -xzf "$TMP_TARBALL" -C "$CACHE_DIR/$VERSION"
    rm -f "$TMP_TARBALL"
    echo "Cached to $CACHE_DIR/$VERSION/"
else
    echo "Using cached Neovim $TAG"
fi

# --- isolated state dirs ---
STATE_ROOT="/tmp/nvim-test-$VERSION"
mkdir -p "$STATE_ROOT"/{data,state,cache,config}

if [[ "$KEEP" -eq 0 ]]; then
    trap 'echo "Cleaning up $STATE_ROOT..."; rm -rf "$STATE_ROOT"' EXIT
else
    echo "State will be preserved at $STATE_ROOT"
fi

# --- run ---
echo "Running Neovim $TAG with isolated state..."
echo "  config:  $CONFIG"
echo "  plugin:  $PLUGIN_DIR"
echo "  state:   $STATE_ROOT"
echo ""

XDG_DATA_HOME="$STATE_ROOT/data" \
XDG_STATE_HOME="$STATE_ROOT/state" \
XDG_CACHE_HOME="$STATE_ROOT/cache" \
XDG_CONFIG_HOME="$STATE_ROOT/config" \
exec "$NVIM_BIN" -u "$CONFIG" "${NVIM_ARGS[@]}"
