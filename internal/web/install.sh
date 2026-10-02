#!/bin/sh
# ghosttree installer. Served by your ghosttree server:
#   curl -fsSL https://<server>/install.sh | sh -s -- --pair XXXX
#
# Downloads the ctx archive for this machine, checks its SHA-256 against
# checksums.txt, and installs ctx into ~/.local/bin ($XDG_BIN_HOME if set).
# No sudo, no tokens, no telemetry. Arguments after "--" go to `ctx join`.
#
# GHOSTTREE_DOWNLOAD_BASE replaces the download location (default: the
# /dist/ path of the server this script came from), e.g. a release URL.
#
# Everything lives in main(), which runs on the last line: a download cut off
# half way executes nothing.

main() {
  set -eu

  server='__GHOSTTREE_SERVER__'
  base=${GHOSTTREE_DOWNLOAD_BASE:-$server/dist}
  base=${base%/}

  say() { printf '%s\n' "$*"; }
  die() { printf 'ghosttree install: %s\n' "$*" >&2; exit 1; }

  case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    MINGW* | MSYS* | CYGWIN*) die "Windows is supported through WSL only: run this command inside a WSL shell." ;;
    *) die "unsupported operating system: $(uname -s) (supported: Linux, macOS)" ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) die "unsupported CPU architecture: $(uname -m) (supported: amd64, arm64)" ;;
  esac

  if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL --proto '=https,http' -o "$2" "$1"; }
  elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -qO "$2" "$1"; }
  else
    die "curl or wget is required"
  fi

  if command -v sha256sum >/dev/null 2>&1; then
    sha256() { sha256sum "$1" | cut -d ' ' -f 1; }
  elif command -v shasum >/dev/null 2>&1; then
    sha256() { shasum -a 256 "$1" | cut -d ' ' -f 1; }
  else
    die "sha256sum or shasum is required to verify the download"
  fi

  tmp=$(mktemp -d "${TMPDIR:-/tmp}/ghosttree-install.XXXXXX") || die "cannot create a temporary directory"
  trap 'rm -rf "$tmp"' EXIT INT TERM HUP

  say "Downloading checksums from $base"
  fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "cannot download $base/checksums.txt"

  archive=$(awk -v suffix="_${os}_${arch}.tar.gz" '
    NF == 2 && substr($2, 1, 4) == "ctx_" && substr($2, length($2) - length(suffix) + 1) == suffix { print $2; exit }
  ' "$tmp/checksums.txt")
  [ -n "$archive" ] || die "no ctx build for ${os}/${arch} listed in $base/checksums.txt"
  case "$archive" in */* | *..*) die "unexpected archive name in checksums.txt" ;; esac

  want=$(awk -v f="$archive" 'NF == 2 && $2 == f { print $1; exit }' "$tmp/checksums.txt")
  [ -n "$want" ] || die "no checksum for $archive"

  say "Downloading $archive"
  fetch "$base/$archive" "$tmp/$archive" || die "cannot download $base/$archive"

  got=$(sha256 "$tmp/$archive")
  if [ "$got" != "$want" ]; then
    die "checksum mismatch for $archive (expected $want, got $got); nothing was installed"
  fi
  say "Checksum OK (sha256 $got)"

  mkdir "$tmp/x"
  tar -xzf "$tmp/$archive" -C "$tmp/x" ctx || die "cannot unpack $archive"

  bindir=${XDG_BIN_HOME:-$HOME/.local/bin}
  mkdir -p "$bindir" || die "cannot create $bindir"
  chmod 755 "$tmp/x/ctx"
  # Same directory, then rename: an existing ctx is replaced atomically.
  cp "$tmp/x/ctx" "$bindir/.ctx.new.$$" || die "cannot write to $bindir"
  mv -f "$bindir/.ctx.new.$$" "$bindir/ctx" || die "cannot install into $bindir"

  say "Installed: $("$bindir/ctx" version 2>&1 | head -n 1) -> $bindir/ctx"

  case ":$PATH:" in
    *":$bindir:"*) ;;
    *) say "Note: $bindir is not on your PATH. Add this to your shell profile: export PATH=\"$bindir:\$PATH\"" ;;
  esac

  if [ "$#" -gt 0 ]; then
    if "$bindir/ctx" help 2>&1 | grep -q '^  join '; then
      say "Joining $server"
      "$bindir/ctx" join --server "$server" "$@"
    else
      say "This ctx build has no 'ctx join' yet; run 'ctx join --server $server $*' after updating."
    fi
  fi
}

main "$@"
