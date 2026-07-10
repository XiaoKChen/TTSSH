#!/bin/sh
# Builds ttssh and installs it to a directory on the PATH (Linux / macOS).
#
#   ./scripts/install.sh              build from source, then install
#   ./scripts/install.sh --no-build   install an already-built binary
#                                     (./ttssh or the matching dist/ binary)
#
# Installs to /usr/local/bin when writable (uses sudo if available),
# otherwise falls back to ~/.local/bin.

set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_bin=""

cleanup() { [ -n "$tmp_bin" ] && rm -f "$tmp_bin"; }
trap cleanup EXIT

if [ "${1:-}" = "--no-build" ]; then
    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    arch=$(uname -m)
    case "$arch" in
        x86_64)          arch=amd64 ;;
        aarch64 | arm64) arch=arm64 ;;
    esac
    for candidate in "$repo_root/ttssh" "$repo_root/dist/ttssh-$os-$arch"; do
        if [ -f "$candidate" ]; then
            source_bin=$candidate
            break
        fi
    done
    if [ -z "${source_bin:-}" ]; then
        echo "No prebuilt binary found (looked for ./ttssh and dist/ttssh-$os-$arch)." >&2
        echo "Run without --no-build to build from source." >&2
        exit 1
    fi
else
    ver=$(cd "$repo_root" && git describe --tags --always --dirty 2>/dev/null) || ver=dev
    [ -n "$ver" ] || ver=dev

    tmp_bin=$(mktemp)
    (cd "$repo_root" && go build -ldflags "-X main.version=$ver" -o "$tmp_bin" ./cmd/ttssh)
    source_bin=$tmp_bin
fi

# Prefer /usr/local/bin; fall back to ~/.local/bin if we can't write there.
if [ -w /usr/local/bin ]; then
    install_dir=/usr/local/bin
    install -m 0755 "$source_bin" "$install_dir/ttssh"
elif command -v sudo >/dev/null 2>&1; then
    install_dir=/usr/local/bin
    echo "Installing to $install_dir (needs sudo)..."
    sudo install -m 0755 "$source_bin" "$install_dir/ttssh"
else
    install_dir=$HOME/.local/bin
    mkdir -p "$install_dir"
    install -m 0755 "$source_bin" "$install_dir/ttssh"
fi

echo "Installed -> $install_dir/ttssh"

case ":$PATH:" in
    *":$install_dir:"*)
        echo "'ttssh' now works from anywhere."
        ;;
    *)
        echo "NOTE: $install_dir is not on your PATH. Add this to your shell profile:"
        echo "  export PATH=\"\$PATH:$install_dir\""
        ;;
esac
