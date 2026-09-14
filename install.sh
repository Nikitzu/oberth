#!/bin/sh
# Installs the oberth CLI from the latest release of Nikitzu/oberth.
#   curl -fsSL https://raw.githubusercontent.com/Nikitzu/oberth/poc/install.sh | sh
# Verifies the release checksum, puts the binary in ~/.local/bin (or $OBERTH_BIN),
# and tells you the one command that comes next. macOS users can use Homebrew
# instead: brew install nikitzu/oberth/oberth
set -eu

repo="Nikitzu/oberth"
bin_dir="${OBERTH_BIN:-$HOME/.local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac
case "$os" in
  linux|darwin) ;;
  *) echo "unsupported OS: $os" >&2; exit 1 ;;
esac

tag="${OBERTH_VERSION:-$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)}"
[ -n "$tag" ] || { echo "could not determine the latest release of $repo" >&2; exit 1; }
base="https://github.com/$repo/releases/download/$tag"
asset="oberth-$os-$arch"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/oberth" "$base/$asset"
curl -fsSL -o "$tmp/oberth.sha256" "$base/$asset.sha256"
expected=$(awk '{print $1}' "$tmp/oberth.sha256")
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/oberth" | awk '{print $1}')
else
  actual=$(shasum -a 256 "$tmp/oberth" | awk '{print $1}')
fi
[ "$expected" = "$actual" ] || { echo "checksum mismatch for $asset" >&2; exit 1; }

mkdir -p "$bin_dir"
install -m 755 "$tmp/oberth" "$bin_dir/oberth"
echo "installed oberth $tag to $bin_dir/oberth"
case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *) echo "add it to your PATH:  export PATH=\"$bin_dir:\$PATH\"" ;;
esac
echo
echo "next:  oberth install"
echo "It asks where Oberth should run (this machine with Docker, or a Kubernetes cluster) and does the rest."
