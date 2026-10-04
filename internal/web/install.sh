#!/bin/sh
# ghosttree installer. Served by your ghosttree server:
#   curl -fsSL https://<server>/install.sh | sh -s -- --pair XXXX
#
# Downloads the ctx archive for this machine, checks its SHA-256 against
# checksums.txt, and installs ctx into ~/.local/bin ($XDG_BIN_HOME if set).
# No sudo, no tokens, no telemetry. Arguments after "--" go to `ctx join`,
# except two that belong to this script:
#   --modify-path     append the PATH line to your shell profile if ctx's
#                     directory is not on PATH (never edits existing lines)
#   --no-modify-path  never touch a shell profile and do not ask
# Without either, a terminal gets the question (default: no); a pipe without a
# terminal only gets the instructions.
#
# GHOSTTREE_DOWNLOAD_BASE replaces the download location (default: the
# /dist/ path of the server this script came from), e.g. a release URL.
#
# Everything lives in main(), which runs on the last line: a download cut off
# half way executes nothing.

main() {
  set -eu

  modify=ask
  for a in "$@"; do
    shift
    case "$a" in
      --modify-path) modify=yes ;;
      --no-modify-path) modify=no ;;
      *) set -- "$@" "$a" ;;
    esac
  done

  server='__GHOSTTREE_SERVER__'
  base=${GHOSTTREE_DOWNLOAD_BASE:-$server/dist}
  base=${base%/}

  say() { printf '%s\n' "$*"; }
  die() { printf 'ghosttree install: %s\n' "$*" >&2; exit 1; }

  # The profile a new interactive shell reads for this user's shell.
  profile_for() {
    case "${SHELL:-}" in
      */zsh) printf '%s\n' "$HOME/.zshrc" ;;
      */bash)
        if [ "$os" = darwin ]; then printf '%s\n' "$HOME/.bash_profile"; else printf '%s\n' "$HOME/.bashrc"; fi ;;
      *) printf '%s\n' "$HOME/.profile" ;;
    esac
  }

  # ctx's directory is not on PATH: say what that means and how to fix it. A
  # profile is only appended to on request, and never rewritten.
  path_help() {
    dir=$1
    # A directory with a quote, dollar sign, backtick, backslash or bang would
    # turn the line into something else when the shell reads the profile; those
    # get single quotes with every ' written as '\''.
    case "$dir" in
      *[\"\$\`\\\'!]*) line="export PATH='$(printf '%s' "$dir" | sed "s/'/'\\\\''/g")':\"\$PATH\"" ;;
      *) line="export PATH=\"$dir:\$PATH\"" ;;
    esac
    profile=$(profile_for)
    say ""
    say "Note: $dir is not on your PATH, so typing 'ctx' will not work in a new terminal yet."
    say "The hooks and the MCP server that ctx sets up use its full path and work regardless."
    if [ "$modify" = ask ] && ( : </dev/tty ) 2>/dev/null; then
      printf 'Add %s to %s? [y/N] ' "$line" "$profile" >/dev/tty
      reply=
      read -r reply </dev/tty || reply=
      case "$reply" in y | Y | yes | YES) modify=yes ;; *) modify=no ;; esac
    fi
    if [ "$modify" = yes ]; then
      if [ -f "$profile" ] && grep -F -e "$dir" "$profile" >/dev/null 2>&1; then
        say "$profile already mentions $dir; left unchanged."
      else
        { printf '\n# added by the ghosttree installer\n%s\n' "$line"; } >>"$profile" || die "cannot write $profile"
        say "Appended the PATH line to $profile. Open a new terminal to use it."
      fi
    else
      say "To fix it, add this line to $profile (or run the installer again with --modify-path):"
      say "  $line"
    fi
    say ""
  }

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

  # Plain http would let anyone on the path swap archive and checksums alike.
  case "$base" in
    https://*) protos='=https' ;;
    http://localhost | http://localhost:* | http://127.0.0.1 | http://127.0.0.1:* | http://\[::1\] | http://\[::1\]:* | http://\[::1]*)
      protos='=http,https' ;;
    http://*)
      if [ "${GHOSTTREE_ALLOW_HTTP:-}" = 1 ]; then
        protos='=http,https'
      else
        die "refusing to download over plain http from $base (use https, or set GHOSTTREE_ALLOW_HTTP=1 if you trust the network)"
      fi ;;
    *) die "unsupported download location: $base" ;;
  esac

  if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL --proto "$protos" --proto-redir "$protos" -o "$2" "$1"; }
  elif command -v wget >/dev/null 2>&1; then
    if [ "$protos" = '=https' ]; then
      fetch() { wget -qO "$2" --https-only "$1"; }
    else
      fetch() { wget -qO "$2" "$1"; }
    fi
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
  trap 'rm -rf "$tmp"' EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM

  say "Downloading checksums from $base"
  fetch "$base/checksums.txt" "$tmp/checksums.raw" || die "cannot download $base/checksums.txt"
  # Same rules as the server: CR dropped, "<64 hex>  [*]<name>" lines only.
  tr -d '\r' < "$tmp/checksums.raw" | awk '
    NF == 2 && length($1) == 64 && $1 !~ /[^0-9a-fA-F]/ { sub(/^\*/, "", $2); print $1, $2 }
  ' > "$tmp/checksums.txt"

  suffix="_${os}_${arch}.tar.gz"
  matches=$(awk -v suffix="$suffix" '
    substr($2, 1, 4) == "ctx_" && length($2) > length(suffix) && substr($2, length($2) - length(suffix) + 1) == suffix { n++ }
    END { print n + 0 }
  ' "$tmp/checksums.txt")
  [ "$matches" -ne 0 ] || die "no ctx build for ${os}/${arch} listed in $base/checksums.txt"
  [ "$matches" -eq 1 ] || die "$base/checksums.txt lists $matches builds for ${os}/${arch}; refusing an ambiguous list"

  archive=$(awk -v suffix="$suffix" '
    substr($2, 1, 4) == "ctx_" && length($2) > length(suffix) && substr($2, length($2) - length(suffix) + 1) == suffix { print $2 }
  ' "$tmp/checksums.txt")
  case "$archive" in */* | *..* | -*) die "unexpected archive name in checksums.txt" ;; esac
  want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
  [ -n "$want" ] || die "no checksum for $archive"
  [ "$(printf '%s\n' "$want" | wc -l | tr -d ' ')" -eq 1 ] || die "duplicate checksum entries for $archive"

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
  case "$bindir" in
    /*) ;;
    *) die "$bindir is not an absolute path; set XDG_BIN_HOME to one" ;;
  esac
  case "$bindir" in
    *[[:cntrl:]]*) die "the install directory contains a control character; refusing it" ;;
  esac
  mkdir -p "$bindir" || die "cannot create $bindir"
  if [ -L "$tmp/x/ctx" ] || [ ! -f "$tmp/x/ctx" ]; then
    die "the archive's ctx is not a regular file; nothing was installed"
  fi
  chmod 755 "$tmp/x/ctx"
  # Same directory, then rename: an existing ctx is replaced atomically.
  cp "$tmp/x/ctx" "$bindir/.ctx.new.$$" || die "cannot write to $bindir"
  mv -f "$bindir/.ctx.new.$$" "$bindir/ctx" || die "cannot install into $bindir"

  say "Installed: $("$bindir/ctx" version 2>&1 | head -n 1) -> $bindir/ctx"

  case ":$PATH:" in
    *":$bindir:"*) ;;
    *) path_help "$bindir" ;;
  esac

  if [ "$#" -gt 0 ]; then
    if "$bindir/ctx" join --help >/dev/null 2>&1; then
      say "Joining $server"
      "$bindir/ctx" join --server "$server" "$@"
    else
      say "This ctx build has no 'ctx join' yet; run 'ctx join --server $server $*' after updating."
    fi
  fi
}

main "$@"
