#!/usr/bin/env bash
# Invitation flow end to end (REQ-434), in both orders, with real binaries and
# throw-away HOMEs, without a VM and without an identity provider:
#
#   scripts/e2e/join-order.sh [--keep]
#
#   A  browser first: the invited account accepts the invitation, the page waits
#      with the one-liner, then the installer runs and claims; the page switches
#      to the approval question, the account approves.
#   B  installer first: the account accepts and copies the command, the installer
#      runs and claims before the browser looks at the pairing page again; the
#      first look then already shows the approval question.
#
# Each run uses the exact command the pairing page shows, the archives built by
# scripts/build-dist.sh and the server's own /install.sh, the loopback PKCE path
# (a fake xdg-open stands in for the browser and records the address it is
# given; the script plays the browser by posting the approval and following the
# redirect to the installer's 127.0.0.1 listener). At the end the machine's
# config must hold a token that belongs to the invited account, and the token of
# the other account must not appear.
#
# Needs: go, curl, tar, sh. Nothing outside the temp directory is touched: the
# installer runs with its own HOME, with ~/.local/bin not on PATH and without the
# collector service (--no-watch).
set -uo pipefail
# shellcheck disable=SC2015
KEEP=0
[ "${1:-}" = --keep ] && KEEP=1
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
SRC=$(cd "$HERE/../.." && pwd)
WORK=$(mktemp -d "${TMPDIR:-/tmp}/gt-join-order.XXXXXX") || exit 2
PORT=${GT_PORT:-$((20000 + RANDOM % 20000))}
SERVER_URL=http://127.0.0.1:$PORT
DB=$WORK/data/ghosttree.db
FAILS=0
SERVER_PID=""
BG_PIDS=()

cleanup() {
  local p
  for p in "${BG_PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null
  if [ "$KEEP" = 1 ]; then echo "kept $WORK"; else rm -rf -- "$WORK"; fi
}
trap cleanup EXIT

pass() { printf 'PASS  %s\n' "$*"; }
fail() { printf 'FAIL  %s\n' "$*"; FAILS=$((FAILS + 1)); }
step() { printf '\n== %s\n' "$*"; }

mkdir -p "$WORK/data" "$WORK/bin" "$WORK/hosthome/.config/ghosttree"

step "build"
if ! ( cd "$SRC" && VERSION=0.0.0-e2e sh scripts/build-dist.sh "$WORK/dist" >"$WORK/build.log" 2>&1 \
      && go build -o "$WORK/ctx" ./cmd/ctx >>"$WORK/build.log" 2>&1 ); then
  tail -20 "$WORK/build.log"; echo "build failed"; exit 1
fi
pass "archives and ctx built"

hostctx() { HOME=$WORK/hosthome XDG_CONFIG_HOME=$WORK/hosthome/.config "$WORK/ctx" "$@"; }

GHOSTTREE_ENFORCE_ACCESS=1 GHOSTTREE_DIST_DIR=$WORK/dist "$WORK/ctx" serve --db "$DB" --listen "127.0.0.1:$PORT" >"$WORK/server.log" 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 50); do curl -fs "$SERVER_URL/api/health" >/dev/null 2>&1 && break; sleep 0.2; done
curl -fs "$SERVER_URL/api/health" >/dev/null 2>&1 || { tail -5 "$WORK/server.log"; echo "server did not start"; exit 1; }

form_value() { grep -o "name=\"$1\" value=\"[^\"]*\"" | head -1 | cut -d'"' -f4; }
page_state() { grep -o 'data-join-state="[a-z]*"' | head -1 | cut -d'"' -f2; }

web_login() { # ACCOUNT JAR
  local link code
  link=$(hostctx account login-link "$1" --url "$SERVER_URL" --db "$DB" | head -1)
  rm -f "$2"
  curl -fs -c "$2" -b "$2" -o "$WORK/login.html" "$link" || return 1
  code=$(grep -o 'value="[0-9a-f]\{64\}"' "$WORK/login.html" | head -1 | cut -d'"' -f2)
  curl -s -c "$2" -b "$2" -H "Origin: $SERVER_URL" -o /dev/null -w '%{http_code}' --data-urlencode "code=$code" "$SERVER_URL/ui/login/code" | grep -q 303
}

# Admin, organization, a project that exists, and one invitation link per call.
setup_admin() {
  hostctx account add robin --admin --db "$DB" >/dev/null || return 1
  local tok
  tok=$(hostctx account token create robin --db "$DB" | sed -n 's/^token: //p')
  printf '{"server_url":"%s","token":"%s","machine":"host-admin"}\n' "$SERVER_URL" "$tok" >"$WORK/hosthome/.config/ghosttree/config.json"
  hostctx org create testorg >/dev/null || return 1
  hostctx org default testorg >/dev/null || return 1
  curl -fs -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' \
    -d '{"harness":"claude-code","external_id":"seed-1","scope":{"project":"github.com/gt-test/demo","branch":"main","machine":"host-admin"},"cwd":"/tmp/demo","started_at":"2026-10-11T14:00:00Z","last_seen_at":"2026-10-11T14:00:00Z"}' \
    "$SERVER_URL/api/sessions" >/dev/null || return 1
  web_login robin "$WORK/jar-robin"
}

make_invitation() { # sets INVITE_URL
  curl -fs -b "$WORK/jar-robin" "$SERVER_URL/ui/orgs?org=testorg" -o "$WORK/orgs.html" || return 1
  local csrf loc id
  csrf=$(form_value csrf_token <"$WORK/orgs.html")
  loc=$(curl -s -b "$WORK/jar-robin" -H "Origin: $SERVER_URL" -o /dev/null -w '%{redirect_url}' \
    --data-urlencode "csrf_token=$csrf" -d org=testorg -d project=github.com/gt-test/demo -d claim=1 -d project_role=member -d days=1 \
    "$SERVER_URL/ui/orgs/invite")
  id=${loc##*new=}
  curl -fs -b "$WORK/jar-robin" "$SERVER_URL/ui/orgs?org=testorg&new=$id" -o "$WORK/orgs2.html" || return 1
  INVITE_URL=$(grep -o "http://127.0.0.1:$PORT/join/[0-9a-f]*" "$WORK/orgs2.html" | head -1)
  [ -n "$INVITE_URL" ]
}

accept_invitation() { # ACCOUNT; leaves the jar $WORK/jar-ACCOUNT signed in and the invitation accepted
  local jar=$WORK/jar-$1 csrf
  hostctx account add "$1" --db "$DB" >/dev/null || return 1
  web_login "$1" "$jar" || return 1
  curl -fs -b "$jar" -c "$jar" "$INVITE_URL" -o "$WORK/join.html" || return 1
  csrf=$(form_value csrf_token <"$WORK/join.html")
  curl -s -b "$jar" -c "$jar" -H "Origin: $SERVER_URL" -o /dev/null -w '%{http_code}' \
    --data-urlencode "csrf_token=$csrf" -d "confirm_account=$1" "$INVITE_URL/accept" | grep -q 303
}

pair_page() { curl -fs -b "$WORK/jar-$1" -c "$WORK/jar-$1" "$SERVER_URL/join/pair"; }

command_from_page() { # reads the page on stdin
  sed 's/<[^>]*>//g' | grep -Eo 'curl -fsSL [^ ]+ \| sh -s -- --pair [A-Z0-9]{4}-[A-Z0-9]{4}' | head -1 \
    | python3 -c 'import html,sys;print(html.unescape(sys.stdin.read().strip()))'
}

# Run the page's one-liner as the machine's user. The trailing flags go to
# ctx join through the installer: no questions without a terminal, no service.
start_installer() { # NAME CMD; background, output in $WORK/NAME.out
  local name=$1 cmd=$2 home=$WORK/home-$1
  mkdir -p "$home"
  printf '#!/bin/sh\necho "$@" >> "%s/opened-%s.log"\n' "$WORK" "$name" >"$WORK/bin/xdg-open"
  chmod +x "$WORK/bin/xdg-open"
  env -i HOME="$home" USER=e2e LOGNAME=e2e SHELL=/bin/sh DISPLAY=:0 XDG_CONFIG_HOME="$home/.config" \
    PATH="$WORK/bin:/usr/local/bin:/usr/bin:/bin" TMPDIR="$WORK" \
    sh -c "$cmd --yes --no-watch --name $name" >"$WORK/$name.out" 2>&1 &
  BG_PIDS+=($!)
  LAST_PID=$!
}

wait_for() { # SECONDS COMMAND...
  local n=$(( $1 * 5 )); shift
  while [ "$n" -gt 0 ]; do "$@" && return 0; sleep 0.2; n=$((n - 1)); done
  return 1
}

state_is() { [ "$(pair_page "$1" | page_state)" = "$2" ]; }

approve() { # ACCOUNT -> follows the redirect to the installer's listener
  local acct=$1 jar=$WORK/jar-$1 page csrf nonce loc
  page=$(pair_page "$acct") || return 1
  csrf=$(printf '%s' "$page" | form_value csrf_token)
  nonce=$(printf '%s' "$page" | form_value nonce)
  loc=$(curl -s -b "$jar" -c "$jar" -H "Origin: $SERVER_URL" -o /dev/null -w '%{redirect_url}' \
    --data-urlencode "csrf_token=$csrf" -d "nonce=$nonce" -d "confirm_account=$acct" -d decision=approve \
    "$SERVER_URL/join/pair/decide")
  case $loc in http://127.0.0.1:*/callback\?code=*) ;; *) echo "approve gave '$loc'"; return 1 ;; esac
  curl -fs -o /dev/null "$loc"
}

check_machine() { # NAME ACCOUNT OTHER
  local name=$1 acct=$2 other=$3 home=$WORK/home-$1 who tok
  if wait_for 30 grep -q "Wrote" "$WORK/$name.out"; then pass "$name: installer finished the join"; else fail "$name: installer did not finish"; sed 's/^/    /' "$WORK/$name.out"; return; fi
  [ -x "$home/.local/bin/ctx" ] && pass "$name: ctx installed into ~/.local/bin" || fail "$name: no ctx in ~/.local/bin"
  [ -s "$home/.config/ghosttree/config.json" ] && pass "$name: config written" || { fail "$name: no config"; return; }
  tok=$(sed -n 's/.*"token": *"\([^"]*\)".*/\1/p' "$home/.config/ghosttree/config.json")
  who=$(curl -fs -H "Authorization: Bearer $tok" "$SERVER_URL/api/whoami" 2>&1)
  if printf '%s' "$who" | grep -q "\"$acct\""; then pass "$name: the token belongs to $acct"; else fail "$name: whoami does not name $acct: $who"; fi
  if printf '%s' "$who" | grep -q "$other"; then fail "$name: whoami names the other account"; fi
  if grep -rq -- "--pair\|[A-Z0-9]\{4\}-[A-Z0-9]\{4\}" "$home/.config" 2>/dev/null; then fail "$name: a pairing code ended up in the config directory"; else pass "$name: no pairing code stored"; fi
}

step "server, admin, organization"
setup_admin || { echo "setup failed"; tail -5 "$WORK/server.log"; exit 1; }
pass "server up on $SERVER_URL"

step "A: browser first, installer second"
make_invitation || { fail "A: no invitation"; exit 1; }
accept_invitation anna || fail "A: accept"
page=$(pair_page anna)
[ "$(printf '%s' "$page" | page_state)" = waiting ] && pass "A: the page waits for the installer" || fail "A: state is '$(printf '%s' "$page" | page_state)'"
cmd=$(printf '%s' "$page" | command_from_page)
[ -n "$cmd" ] && pass "A: command shown: ${cmd%--pair*}--pair ****" || { fail "A: no command on the page"; exit 1; }
start_installer machine-a "$cmd"
wait_for 30 state_is anna claimed && pass "A: the page switched to the approval question" || fail "A: page never showed the claim"
approve anna && pass "A: approved, installer's callback reached" || fail "A: approve"
check_machine machine-a anna berta

step "B: installer first, browser second"
make_invitation || { fail "B: no invitation"; exit 1; }
accept_invitation berta || fail "B: accept"
cmd=$(pair_page berta | command_from_page)
[ -n "$cmd" ] && pass "B: command copied from the page" || { fail "B: no command"; exit 1; }
start_installer machine-b "$cmd"
sleep 4
kill -0 "$LAST_PID" 2>/dev/null && pass "B: the installer is waiting for the browser" || fail "B: the installer ended without approval"
page=$(pair_page berta)
[ "$(printf '%s' "$page" | page_state)" = claimed ] && pass "B: first look at the page already shows the approval question" || fail "B: state is '$(printf '%s' "$page" | page_state)'"
printf '%s' "$page" | grep -q "machine-b" && pass "B: the question names the machine" || fail "B: machine not named"
approve berta && pass "B: approved, installer's callback reached" || fail "B: approve"
check_machine machine-b berta anna

step "pairing codes in the server log"
if grep -Eq '[A-Z0-9]{4}-[A-Z0-9]{4}' "$WORK/server.log"; then fail "a pairing-code-shaped string is in the server log"; else pass "none"; fi

echo
if [ "$FAILS" -eq 0 ]; then echo "ALL PASS"; else echo "$FAILS FAILED"; exit 1; fi
