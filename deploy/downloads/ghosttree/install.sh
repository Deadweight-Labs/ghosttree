#!/bin/sh
# ghosttree installer.
#
#   curl -fsSL https://downloads.deadweightlabs.com/ghosttree/install.sh | sh
#
# Installs ctx from the newest GitHub release of Deadweight-Labs/ghosttree and,
# if you want, goes on to connect this machine to a server, to set up a server
# of your own (systemd) or to run one with Docker. Nothing is installed before
# the download has been checked twice: against the minisign signature of the
# release's checksums.txt (the public key below) and against the SHA-256 of the
# archive in that file. No tokens, no telemetry.
#
# Options (everything can be answered here, so a run needs no terminal):
#   --connect URL     install ctx and connect this machine to the server at URL
#   --server          run a ghosttree server on this machine with systemd
#   --docker          run a ghosttree server with Docker
#   --version TAG     a release other than the newest stable one (v0.1.0-rc.3)
#   --yes             take the defaults instead of asking
#   --modify-path     add ctx's directory to your shell profile if it is missing
#   --no-modify-path  never touch a shell profile and do not ask
# Every other option goes on to the command that follows: `ctx join` for
# --connect (for example --pair XXXX-XXXX), `ctx server install` for --server and
# --docker (for example --domain ghosttree.example.com --admin alex).
# Without a mode a terminal gets a menu; without a terminal only ctx is
# installed.
#
# Environment: GHOSTTREE_VERSION (as --version), GHOSTTREE_RELEASE_BASE (where
# <tag>/<file> is downloaded from) and GHOSTTREE_API_URL (where the newest tag is
# asked for) replace the GitHub addresses. The signature key cannot be replaced.
#
# Everything lives in main(), which runs on the last line: a download cut off
# half way executes nothing.

main() {
  set -eu

  repo='Deadweight-Labs/ghosttree'
  # The public minisign key that signs every release (the same line stands in
  # deploy/downloads/ghosttree/minisign.pub). Whoever swaps it is not signing
  # for us.
  pubkey='__GHOSTTREE_MINISIGN_PUBKEY__'

  say_usage() {
    printf '%s\n' \
      'usage: curl -fsSL https://downloads.deadweightlabs.com/ghosttree/install.sh | sh -s -- [options]' \
      '  --connect URL     install ctx and connect this machine to the server at URL' \
      '  --server          run a ghosttree server on this machine with systemd' \
      '  --docker          run a ghosttree server with Docker' \
      '  --version TAG     a release other than the newest stable one (v0.1.0-rc.3)' \
      '  --yes             take the defaults instead of asking' \
      '  --modify-path     add ctx to your shell profile PATH if it is missing' \
      '  --no-modify-path  never touch a shell profile and do not ask' \
      'other options go to ctx join (--connect) or ctx server install (--server, --docker)'
  }

  mode='' version=${GHOSTTREE_VERSION:-} connect='' yes=no modify=ask want=''
  for a in "$@"; do
    shift
    if [ -n "$want" ]; then
      case "$want" in
        version) version=$a ;;
        connect) connect=$a ;;
      esac
      want=''
      continue
    fi
    case "$a" in
      --connect) mode=connect; want=connect ;;
      --connect=*) mode=connect; connect=${a#--connect=} ;;
      --server) mode=server ;;
      --docker) mode=docker ;;
      --client) mode=client ;;
      --version) want=version ;;
      --version=*) version=${a#--version=} ;;
      --yes | -y) yes=yes; set -- "$@" "$a" ;;
      --modify-path) modify=yes ;;
      --no-modify-path) modify=no ;;
      -h | --help)
        say_usage
        exit 0 ;;
      *) set -- "$@" "$a" ;;
    esac
  done
  [ -z "$want" ] || { printf 'ghosttree install: --%s needs a value\n' "$want" >&2; exit 1; }

  say() { printf '%s\n' "$*"; }
  die() { printf 'ghosttree install: %s\n' "$*" >&2; exit 1; }

  case "$pubkey" in
    __GHOSTTREE*) die "this installer carries no release key yet, so it cannot check anything; refusing" ;;
  esac

  api=${GHOSTTREE_API_URL:-https://api.github.com}
  dlbase=${GHOSTTREE_RELEASE_BASE:-https://github.com/$repo/releases/download}
  api=${api%/}
  dlbase=${dlbase%/}

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

  # Plain http would let anyone on the path swap archive and checksums alike;
  # the signature would catch it, but there is no reason to allow it.
  # Plain http is only for this machine, and only when the host part is exactly
  # that: "http://localhost:1@example.org" has the user name "localhost" and
  # goes to example.org, so an "@" (or a backslash, which some clients read as
  # a slash) in the host part ends the discussion.
  protos_for() {
    case "$1" in
      https://*) printf '%s\n' '=https' ;;
      http://*)
        auth=${1#http://}
        auth=${auth%%[/?#]*}
        case "$auth" in
          *[@\\]*) die "refusing to download over plain http from $1: user information in an address hides the real host" ;;
          localhost | localhost:* | 127.0.0.1 | 127.0.0.1:* | \[::1\] | \[::1\]:*) printf '%s\n' '=http,https' ;;
          *) die "refusing to download over plain http from $1" ;;
        esac ;;
      *) die "refusing to download over plain http from $1" ;;
    esac
  }
  api_protos=$(protos_for "$api")
  dl_protos=$(protos_for "$dlbase")

  if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL --proto "$2" --proto-redir "$2" -o "$3" "$1"; }
  elif command -v wget >/dev/null 2>&1; then
    # wget has no switch that keeps a redirect on https: GNU wget's --https-only
    # only governs recursive downloads, and BusyBox wget follows a redirect to
    # plain http without asking. So GNU Wget 1.x is told not to follow anything
    # (--max-redirect=0) and every hop is followed here, after the address has
    # been checked by the same rule as the first one. BusyBox wget and Wget2
    # (which Fedora ships as wget) cannot be driven that way and are turned away
    # with the way out.
    wget_id=$(wget --version 2>&1 | head -n 1)
    case "$wget_id" in
      'GNU Wget 1.'*) ;;
      *) die "the wget on this system ($wget_id) is not GNU Wget 1.x; BusyBox wget and Wget2 cannot be stopped from following a redirect to plain http.
Install curl (apk add curl, apt install curl, dnf install curl) and run this again." ;;
    esac
    fetch() { # url, allowed protocols, output file
      furl=$1 fprotos=$2 fout=$3 hops=0
      while :; do
        hops=$((hops + 1))
        [ "$hops" -le 8 ] || { printf 'ghosttree install: too many redirects for %s\n' "$1" >&2; return 1; }
        if wget -q -S --max-redirect=0 -O "$fout" "$furl" 2>"$tmp/wget.headers"; then
          return 0
        fi
        fcode=$(awk '/^[[:space:]]*HTTP\//{ c = $2 } END { print c }' "$tmp/wget.headers")
        case "$fcode" in
          301 | 302 | 303 | 307 | 308) ;;
          *) return 1 ;;
        esac
        floc=$(sed -n 's/^[[:space:]]*[Ll][Oo][Cc][Aa][Tt][Ii][Oo][Nn]:[[:space:]]*//p' "$tmp/wget.headers" | tail -n 1 | tr -d '\r')
        case "$floc" in
          //*) return 1 ;;
          /*)
            forigin=${furl#*://}
            forigin=${furl%%://*}://${forigin%%/*}
            floc=$forigin$floc ;;
        esac
        # Each hop has to pass the rule the first address passed, and a hop may
        # not be less strict than the one before it.
        hop_protos=$(protos_for "$floc") || return 1
        if [ "$fprotos" = '=https' ] && [ "$hop_protos" != '=https' ]; then
          printf 'ghosttree install: refusing a redirect to plain http (%s)\n' "$floc" >&2
          return 1
        fi
        furl=$floc
      done
    }
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

  # ---- signature -------------------------------------------------------------

  # Checks that $2 is a minisign signature of $1 made with the key above. Three
  # ways, in this order: the minisign program, then OpenSSL 3 (Ed25519 and
  # BLAKE2b are all a minisign signature needs, so openssl is the checker that
  # most machines already have), and otherwise it stops and says how to get one.
  # Prints nothing on success and returns 1 on a bad signature.
  verify_signature() {
    vfile=$1 vsig=$2
    if command -v minisign >/dev/null 2>&1; then
      minisign -V -q -P "$pubkey" -m "$vfile" -x "$vsig" >/dev/null 2>&1
      return $?
    fi
    if command -v openssl >/dev/null 2>&1 && openssl pkeyutl -help 2>&1 | grep -q rawin &&
      openssl dgst -blake2b512 </dev/null >/dev/null 2>&1; then
      verify_with_openssl "$vfile" "$vsig"
      return $?
    fi
    die "cannot check the signature: neither minisign nor OpenSSL 3 is installed.
Install minisign (apt install minisign, dnf install minisign, brew install minisign, apk add minisign) and run this again."
  }

  # A minisign public key is base64 of: "Ed", 8 bytes key id, 32 bytes Ed25519
  # key. A signature is base64 of: "ED" (the file was hashed with BLAKE2b-512
  # first) or "Ed" (it was not), 8 bytes key id, 64 bytes signature; then a
  # trusted comment and a second signature over signature plus comment.
  verify_with_openssl() {
    ofile=$1 osig=$2
    w="$tmp/ossl"
    mkdir -p "$w"
    printf '%s' "$pubkey" | openssl base64 -d -A >"$w/pub.raw" 2>/dev/null || return 1
    [ "$(wc -c <"$w/pub.raw" | tr -d ' ')" = 42 ] || return 1
    [ "$(head -c 2 "$w/pub.raw")" = Ed ] || return 1
    head -c 10 "$w/pub.raw" | tail -c 8 >"$w/pub.id"
    # The SubjectPublicKeyInfo header of an Ed25519 key, then the key itself.
    { printf '\060\052\060\005\006\003\053\145\160\003\041\000'; tail -c +11 "$w/pub.raw"; } >"$w/pub.der"

    [ "$(wc -l <"$osig" | tr -d ' ')" -ge 3 ] || return 1
    sed -n 2p "$osig" | tr -d '\r\n' | openssl base64 -d -A >"$w/sig.raw" 2>/dev/null || return 1
    [ "$(wc -c <"$w/sig.raw" | tr -d ' ')" = 74 ] || return 1
    head -c 10 "$w/sig.raw" | tail -c 8 >"$w/sig.id"
    [ "$(od -An -v -tx1 <"$w/pub.id" | tr -d ' \n')" = "$(od -An -v -tx1 <"$w/sig.id" | tr -d ' \n')" ] || return 1
    tail -c +11 "$w/sig.raw" >"$w/sig.bin"
    alg=$(head -c 2 "$w/sig.raw")
    case "$alg" in
      ED) openssl dgst -blake2b512 -binary "$ofile" >"$w/msg" 2>/dev/null || return 1 ;;
      Ed) cp "$ofile" "$w/msg" ;;
      *) return 1 ;;
    esac
    openssl pkeyutl -verify -pubin -inkey "$w/pub.der" -keyform DER -rawin -in "$w/msg" -sigfile "$w/sig.bin" >/dev/null 2>&1 || return 1

    # The trusted comment is only as good as its own signature.
    sed -n 3p "$osig" | tr -d '\r' | sed 's/^trusted comment: //' | tr -d '\n' >"$w/comment"
    sed -n 4p "$osig" | tr -d '\r\n' | openssl base64 -d -A >"$w/global.bin" 2>/dev/null || return 1
    cat "$w/sig.bin" "$w/comment" >"$w/global.msg"
    openssl pkeyutl -verify -pubin -inkey "$w/pub.der" -keyform DER -rawin -in "$w/global.msg" -sigfile "$w/global.bin" >/dev/null 2>&1
  }

  # ---- which release ---------------------------------------------------------

  if [ -z "$version" ]; then
    say "Looking up the newest ghosttree release"
    fetch "$api/repos/$repo/releases/latest" "$api_protos" "$tmp/latest.json" ||
      die "there is no stable ghosttree release yet (or $api cannot be reached).
Release candidates are installed on request: run this again with --version v0.1.0-rc.3 (list: https://github.com/$repo/releases)."
    version=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$tmp/latest.json" | head -n 1)
  fi
  case "$version" in v*) ;; *) version=v$version ;; esac
  printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$' ||
    die "unexpected release tag '$version' (expected v1.2.3 or v1.2.3-rc.1)"
  relbase="$dlbase/$version"

  say "Downloading the checksums and signature of $version"
  fetch "$relbase/checksums.txt" "$dl_protos" "$tmp/checksums.raw" || die "cannot download $relbase/checksums.txt"
  fetch "$relbase/checksums.txt.minisig" "$dl_protos" "$tmp/checksums.minisig" ||
    die "cannot download $relbase/checksums.txt.minisig: a release without a signature is not installed"
  if ! verify_signature "$tmp/checksums.raw" "$tmp/checksums.minisig"; then
    die "the signature of checksums.txt does not match the ghosttree release key; nothing was installed"
  fi
  # What the key holder signed is "ghosttree <tag>": a genuine list of an older
  # release does not pass for this one.
  comment=$(sed -n 3p "$tmp/checksums.minisig" | tr -d '\r' | sed 's/^trusted comment: //')
  [ "$comment" = "ghosttree $version" ] ||
    die "the signed checksums.txt belongs to '$comment', not to ghosttree $version; nothing was installed"
  say "Signature OK ($comment)"

  # Same rules as the server's installer: CR dropped, "<64 hex>  [*]<name>" lines only.
  tr -d '\r' <"$tmp/checksums.raw" | awk '
    NF == 2 && length($1) == 64 && $1 !~ /[^0-9a-fA-F]/ { sub(/^\*/, "", $2); print $1, $2 }
  ' >"$tmp/checksums.txt"

  suffix="_${os}_${arch}.tar.gz"
  matches=$(awk -v suffix="$suffix" '
    (substr($2, 1, 4) == "ctx_" || substr($2, 1, 10) == "ghosttree_") && length($2) > length(suffix) && substr($2, length($2) - length(suffix) + 1) == suffix { n++ }
    END { print n + 0 }
  ' "$tmp/checksums.txt")
  [ "$matches" -ne 0 ] || die "no ctx build for ${os}/${arch} listed in the checksums of $version"
  [ "$matches" -eq 1 ] || die "the checksums of $version list $matches builds for ${os}/${arch}; refusing an ambiguous list"
  archive=$(awk -v suffix="$suffix" '
    (substr($2, 1, 4) == "ctx_" || substr($2, 1, 10) == "ghosttree_") && length($2) > length(suffix) && substr($2, length($2) - length(suffix) + 1) == suffix { print $2 }
  ' "$tmp/checksums.txt")
  case "$archive" in */* | *..* | -*) die "unexpected archive name in checksums.txt" ;; esac
  want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
  [ -n "$want" ] || die "no checksum for $archive"
  [ "$(printf '%s\n' "$want" | wc -l | tr -d ' ')" -eq 1 ] || die "duplicate checksum entries for $archive"

  say "Downloading $archive"
  fetch "$relbase/$archive" "$dl_protos" "$tmp/$archive" || die "cannot download $relbase/$archive"
  got=$(sha256 "$tmp/$archive")
  [ "$got" = "$want" ] || die "checksum mismatch for $archive (expected $want, got $got); nothing was installed"
  say "Checksum OK (sha256 $got)"

  mkdir "$tmp/x"
  tar -xzf "$tmp/$archive" -C "$tmp/x" ctx || die "cannot unpack $archive"
  if [ -L "$tmp/x/ctx" ] || [ ! -f "$tmp/x/ctx" ]; then
    die "the archive's ctx is not a regular file; nothing was installed"
  fi
  chmod 755 "$tmp/x/ctx"

  # ---- what to set up --------------------------------------------------------

  have_tty() { ( : </dev/tty ) 2>/dev/null; }
  ask() { # prompt, answer in $reply
    printf '%s' "$1" >/dev/tty
    reply=
    read -r reply </dev/tty || reply=
  }

  if [ -z "$mode" ] && [ "$yes" = no ] && have_tty; then
    say ""
    say "What do you want to do?"
    say "  1) Connect this machine to a ghosttree server"
    say "  2) Run a ghosttree server on this machine (systemd)"
    say "  3) Run a ghosttree server with Docker"
    say "  4) Only install ctx"
    ask "Choose [1-4, default 4]: "
    case "$reply" in
      1) mode=connect ;;
      2) mode=server ;;
      3) mode=docker ;;
      *) mode=client ;;
    esac
  fi
  [ -n "$mode" ] || mode=client
  if [ "$mode" = connect ] && [ -z "$connect" ]; then
    if [ "$yes" = no ] && have_tty; then
      ask "Address of the server (https://...): "
      connect=$reply
    fi
    [ -n "$connect" ] || die "--connect needs the address of the server"
  fi

  # ---- install ---------------------------------------------------------------

  if [ "$mode" = server ]; then
    # A system service wants ctx where every user finds it.
    bindir=/usr/local/bin
    if [ "$(id -u)" -ne 0 ]; then
      command -v sudo >/dev/null 2>&1 || die "setting up a server needs root: run this as root, or install sudo"
      sudo_cmd=sudo
    else
      sudo_cmd=
    fi
    # shellcheck disable=SC2086
    $sudo_cmd mkdir -p "$bindir" || die "cannot create $bindir"
    # shellcheck disable=SC2086
    $sudo_cmd cp "$tmp/x/ctx" "$bindir/.ctx.new.$$" || die "cannot write to $bindir"
    # shellcheck disable=SC2086
    $sudo_cmd mv -f "$bindir/.ctx.new.$$" "$bindir/ctx" || die "cannot install into $bindir"
  else
    sudo_cmd=
    bindir=${XDG_BIN_HOME:-$HOME/.local/bin}
    case "$bindir" in
      /*) ;;
      *) die "$bindir is not an absolute path; set XDG_BIN_HOME to one" ;;
    esac
    case "$bindir" in
      *[[:cntrl:]]*) die "the install directory contains a control character; refusing it" ;;
    esac
    mkdir -p "$bindir" || die "cannot create $bindir"
    # Same directory, then rename: an existing ctx is replaced atomically.
    cp "$tmp/x/ctx" "$bindir/.ctx.new.$$" || die "cannot write to $bindir"
    mv -f "$bindir/.ctx.new.$$" "$bindir/ctx" || die "cannot install into $bindir"
  fi
  ctx=$bindir/ctx
  say "Installed: $("$ctx" version 2>&1 | head -n 1) -> $ctx"

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
    if [ "$modify" = ask ] && [ "$yes" = no ] && have_tty; then
      ask "Add $line to $profile? [y/N] "
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

  case ":$PATH:" in
    *":$bindir:"*) ;;
    *) path_help "$bindir" ;;
  esac

  # ---- go on -----------------------------------------------------------------

  case "$mode" in
    client)
      say "Next: ctx join --server <address of your server> --pair <code>, or run this installer with --server to set up a server."
      ;;
    connect)
      "$ctx" join --help >/dev/null 2>&1 || die "this ctx build has no 'ctx join'; update it and run: ctx join --server $connect"
      say "Joining $connect"
      "$ctx" join --server "$connect" "$@"
      ;;
    server | docker)
      "$ctx" server install --help >/dev/null 2>&1 ||
        die "this ctx build has no 'ctx server install' yet; ctx is installed, but the server has to be set up by hand (deploy/README.md)"
      if [ "$mode" = docker ]; then
        set -- --docker "$@"
      fi
      # shellcheck disable=SC2086
      $sudo_cmd "$ctx" server install "$@"
      ;;
  esac
}

main "$@"
