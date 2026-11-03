#!/bin/sh
# Installs the `dotfiles` CLI and runs its setup wizard.
#
#   curl -fsSL https://raw.githubusercontent.com/amichaelyu/dotfiles/main/install.sh | sh
#
# Options (env): DOTFILES_VERSION=v1.2.3 (default: latest), DOTFILES_BIN_DIR=~/.local/bin, DOTFILES_NO_SETUP=1
set -eu

REPO="amichaelyu/dotfiles"
BIN_DIR="${DOTFILES_BIN_DIR:-$HOME/.local/bin}"
VERSION="${DOTFILES_VERSION:-latest}"

say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux)  os=linux ;;
  *) die "unsupported OS: $(uname -s) (macOS and Linux only)" ;;
esac
case "$(uname -m)" in
  arm64|aarch64) arch=arm64 ;;
  x86_64|amd64)  arch=amd64 ;;
  *) die "unsupported CPU: $(uname -m)" ;;
esac

if [ "$VERSION" = latest ]; then
  url="https://github.com/$REPO/releases/latest/download/dotfiles_${os}_${arch}.tar.gz"
else
  url="https://github.com/$REPO/releases/download/$VERSION/dotfiles_${os}_${arch}.tar.gz"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
say "downloading dotfiles ($os/$arch)"
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$url" -o "$tmp/dotfiles.tar.gz" || die "download failed: $url"
else
  wget -qO "$tmp/dotfiles.tar.gz" "$url" || die "download failed: $url"
fi
tar -xzf "$tmp/dotfiles.tar.gz" -C "$tmp"
mkdir -p "$BIN_DIR"
install -m 0755 "$tmp/dotfiles" "$BIN_DIR/dotfiles"
say "installed $BIN_DIR/dotfiles ($("$BIN_DIR/dotfiles" version))"

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) export PATH="$BIN_DIR:$PATH" ;;
esac

[ -n "${DOTFILES_NO_SETUP:-}" ] && exit 0
# `curl | sh` leaves stdin as the pipe; give the wizard the real terminal.
if [ -r /dev/tty ]; then
  exec "$BIN_DIR/dotfiles" setup < /dev/tty
fi
say "no terminal attached; run \`dotfiles setup\` to continue"
