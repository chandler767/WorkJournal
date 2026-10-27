#!/usr/bin/env bash
# Runs from the clone root before every Tessl cloud run. Safe to run more than once.
set -eo pipefail

GO_MIN_MINOR=26 # work-journal/go.mod declares go 1.26.0; any Go 1.26 or newer works
mkdir -p "$HOME/.local/bin"
export PATH="$HOME/.local/bin:$PATH"

go_ok() {
  command -v go >/dev/null 2>&1 || return 1
  local v
  v="$(go env GOVERSION 2>/dev/null)" || return 1
  v="${v#go}"
  v="${v%% *}"
  local major="${v%%.*}" rest="${v#*.}"
  local minor="${rest%%[!0-9]*}"
  local suffix="${rest#"$minor"}"
  [ "$major" -gt 1 ] && return 0
  [ "$major" -eq 1 ] || return 1
  [ "${minor:-0}" -gt "$GO_MIN_MINOR" ] && return 0
  # Go orders a prerelease such as go1.26rc1 below go1.26.0, so only a final 1.26 release counts.
  [ "${minor:-0}" -eq "$GO_MIN_MINOR" ] && { [ -z "$suffix" ] || [[ "$suffix" =~ ^\.[0-9]+$ ]]; }
}

echo "==> Checking for Go 1.${GO_MIN_MINOR} or newer"
if go_ok; then
  echo "Using $(go version)"
else
  echo "Found: $(command -v go >/dev/null 2>&1 && go version || echo 'no Go')"
  case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
  esac
  echo "==> Finding the latest stable Go release"
  version="$(curl -fsSL --connect-timeout 20 --max-time 60 'https://go.dev/VERSION?m=text' | head -n 1)"
  echo "==> Installing ${version} for linux-${arch} into ~/.local/go"
  tmp="$(mktemp -d)"
  curl -fsSL --connect-timeout 20 --max-time 600 "https://go.dev/dl/${version}.linux-${arch}.tar.gz" -o "$tmp/go.tar.gz"
  rm -rf "$HOME/.local/go"
  tar -C "$HOME/.local" -xzf "$tmp/go.tar.gz"
  rm -rf "$tmp"
  ln -sf "$HOME/.local/go/bin/go" "$HOME/.local/bin/go"
  ln -sf "$HOME/.local/go/bin/gofmt" "$HOME/.local/bin/gofmt"
  hash -r
  if ! go_ok; then
    echo "Go 1.${GO_MIN_MINOR}+ is required, but found: $(go version 2>&1)" >&2
    exit 1
  fi
  echo "Using $(go version)"
fi

echo "==> Downloading Go module dependencies for work-journal"
(cd work-journal && go mod download)

echo "==> Setup done"
