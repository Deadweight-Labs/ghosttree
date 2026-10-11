#!/usr/bin/env bash
# Fresh-machine test (REQ-438): in a brand-new Ubuntu 24.04 Incus VM with nothing
# installed, the one-liner from the invitation page must lead to a connected
# machine with a running collector.
#
#   scripts/e2e/fresh-vm.sh [--server-url http://127.0.0.1:18480] [--vm gt-fresh]
#                           [--scenario all|plain|taken] [--create-vm] [--keep-vm-running]
#
# What it does, per scenario (the VM is reset to its snapshot "clean" first):
#   1. builds the ctx archives (scripts/build-dist.sh) and a ctx for the host,
#   2. starts a throw-away server (temp DB, GHOSTTREE_ENFORCE_ACCESS=1,
#      GHOSTTREE_DIST_DIR) and creates two accounts, an organization and a project
#      invitation link, all without a browser or an identity provider,
#   3. as the invited account, accepts the invitation and reads the command off
#      the pairing page (the same HTML a person sees),
#   4. runs exactly that command in the VM as user "robin" with ~/.local/bin not
#      on PATH, approves the machine on the pairing page with the code ctx prints,
#   5. checks doctor, hook/MCP paths, the collector service, a fake Claude
#      transcript reaching the server, and a VM reboot with linger.
# Scenario "taken": the machine name equals one that another account already
# holds; ctx join must pick a free name and not abort.
# No Claude Code is installed in the VM; `ctx install claude` runs against an
# empty ~/.claude.
#
# Why loopback: the pairing page only shows the one-liner for https or loopback,
# and `ctx join` only accepts those. The VM therefore reaches the host server
# under the SAME address http://127.0.0.1:PORT through two small TCP forwarders
# (scripts/e2e/tcpfwd.py: host bridge address -> loopback, VM loopback -> host
# bridge address). Incus cannot proxy loopback into a VM.
#
# Host changes: with an active ufw, two narrow rules on the Incus bridge are
# added (DHCP and the server port from the bridge subnet) and removed on exit
# (GT_KEEP_UFW=1 keeps them). Needs passwordless sudo for that; otherwise the
# script prints the commands. Nothing else outside $GT_WORK is touched; the
# host-side ctx runs with its own HOME so no real config is read or written.
#
# Needs: incus (user in incus-admin), go, curl, python3, sqlite3, git, flock.
set -uo pipefail

SERVER_URL=${GT_SERVER_URL:-http://127.0.0.1:18480}
VM=${GT_VM:-gt-fresh}
SNAP=${GT_SNAPSHOT:-clean}
SCENARIO=all
CREATE_VM=0
KEEP_VM=0
WORK=${GT_WORK:-/tmp/gt-fresh-vm}
BRIDGE=${GT_BRIDGE:-incusbr0}
IMAGE=${GT_IMAGE:-images:ubuntu/24.04}
VM_USER=robin
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO=$(cd "$HERE/../.." && pwd)

while [ $# -gt 0 ]; do
  case $1 in
    --server-url) SERVER_URL=$2; shift 2 ;;
    --vm) VM=$2; shift 2 ;;
    --scenario) SCENARIO=$2; shift 2 ;;
    --create-vm) CREATE_VM=1; shift ;;
    --keep-vm-running) KEEP_VM=1; shift ;;
    -h | --help) sed -n '2,/^set -uo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

case $SERVER_URL in
  http://127.0.0.1:* | http://localhost:*) ;;
  *) echo "--server-url must be http://127.0.0.1:PORT (the one-liner is only shown for https or loopback)" >&2; exit 2 ;;
esac
SERVER_URL=${SERVER_URL/localhost/127.0.0.1}
PORT=${SERVER_URL##*:}
case $SCENARIO in all | plain | taken) ;; *) echo "--scenario: all|plain|taken" >&2; exit 2 ;; esac

for t in incus go curl python3 sqlite3 git; do
  command -v "$t" >/dev/null || { echo "missing tool: $t" >&2; exit 2; }
done

mkdir -p "$WORK"
exec 9>"$WORK/lock"
flock -n 9 || { echo "another run holds $WORK/lock" >&2; exit 2; }

REPORT="$WORK/report.txt"
: >"$REPORT"
FAILS=0
t0=$(date +%s)
say() { printf '%s\n' "$*" | tee -a "$REPORT"; }
step() { say ""; say "== [$(($(date +%s) - t0))s] $*"; }
pass() { say "PASS  $*"; }
fail() { say "FAIL  $*"; FAILS=$((FAILS + 1)); }
info() { say "INFO  $*"; }
indent() { sed 's/^/      /' | tee -a "$REPORT"; }
check() { # check "label" cmd...
  local label=$1; shift
  if "$@" >"$WORK/check.out" 2>&1; then pass "$label"; else fail "$label"; indent <"$WORK/check.out"; fi
}

# ---------------------------------------------------------------- host side
HOST_HOME="$WORK/home"
hostctx() { HOME="$HOST_HOME" XDG_CONFIG_HOME="$HOST_HOME/.config" "$WORK/ctx" "$@"; }
DB="$WORK/data/ghosttree.db"
BRIDGE_IP=$(incus network get "$BRIDGE" ipv4.address 2>/dev/null | cut -d/ -f1)
BRIDGE_NET=$(incus network get "$BRIDGE" ipv4.address 2>/dev/null)
BRIDGE_NET=${BRIDGE_NET%.*}.0/24
[ -n "$BRIDGE_IP" ] || { echo "cannot read ipv4.address of $BRIDGE" >&2; exit 2; }

UFW_ADDED=()
ufw_rule() { # ufw_rule add|del ARGS...
  local op=$1; shift
  if [ "$op" = add ]; then sudo -n ufw allow "$@" >/dev/null 2>&1; else sudo -n ufw delete allow "$@" >/dev/null 2>&1; fi
}
setup_ufw() {
  command -v ufw >/dev/null || return 0
  sudo -n ufw status 2>/dev/null | grep -q "Status: active" || return 0
  local rules=(
    "in on $BRIDGE to any port 67 proto udp comment gt-fresh-vm-dhcp"
    "in on $BRIDGE from $BRIDGE_NET to $BRIDGE_IP port $PORT proto tcp comment gt-fresh-vm-server"
  )
  local r
  for r in "${rules[@]}"; do
    # shellcheck disable=SC2086
    if ufw_rule add $r; then UFW_ADDED+=("${r% comment *}"); else
      info "ufw is active and sudo -n failed; run: sudo ufw allow $r"
    fi
  done
}

kill_pidfile() { [ -f "$1" ] && kill "$(cat "$1")" 2>/dev/null; rm -f "$1"; return 0; }
stop_server() { kill_pidfile "$WORK/server.pid"; }
stop_fwd() { kill_pidfile "$WORK/fwd.pid"; }
cleanup() {
  stop_server
  stop_fwd
  if [ "$KEEP_VM" != 1 ]; then incus stop "$VM" --force >/dev/null 2>&1; fi
  if [ "${GT_KEEP_UFW:-0}" != 1 ]; then
    local r
    for r in "${UFW_ADDED[@]:-}"; do
      # shellcheck disable=SC2086
      [ -n "$r" ] && ufw_rule del $r
    done
  fi
}
trap cleanup EXIT

build() {
  step "build: ctx archives (scripts/build-dist.sh) and host ctx from $(git -C "$REPO" rev-parse --short HEAD)"
  if ! ( cd "$REPO" && VERSION="0.0.0-vmtest" sh scripts/build-dist.sh "$WORK/dist" >"$WORK/build.log" 2>&1 \
        && go build -o "$WORK/ctx" ./cmd/ctx >>"$WORK/build.log" 2>&1 ); then
    tail -20 "$WORK/build.log" | indent
    echo "build failed" >&2; exit 1
  fi
  pass "archives: $(ls "$WORK"/dist/*.tar.gz | wc -l), checksums.txt present"
}

start_server() {
  stop_server
  rm -rf "$WORK/data" "$HOST_HOME"
  mkdir -p "$WORK/data" "$HOST_HOME/.config/ghosttree"
  GHOSTTREE_ENFORCE_ACCESS=1 GHOSTTREE_DIST_DIR="$WORK/dist" \
    sh -c 'echo $$ >"$1"; exec "$2" serve --db "$3" --listen "127.0.0.1:$4"' _ "$WORK/server.pid" "$WORK/ctx" "$DB" "$PORT" \
    >"$WORK/server.log" 2>&1 &
  local i
  for i in $(seq 1 50); do curl -fs "$SERVER_URL/api/health" >/dev/null 2>&1 && return 0; sleep 0.2; done
  tail -5 "$WORK/server.log" | indent; echo "server did not start" >&2; return 1
}

start_fwd() {
  stop_fwd
  sh -c 'echo $$ >"$1"; exec python3 "$2" "$3" "$4" 127.0.0.1 "$4"' _ "$WORK/fwd.pid" "$HERE/tcpfwd.py" "$BRIDGE_IP" "$PORT" \
    >"$WORK/fwd.log" 2>&1 &
  sleep 0.5
}

# Web helpers: log in with a one-time login link, no identity provider.
form_value() { grep -o "name=\"$1\" value=\"[^\"]*\"" | head -1 | cut -d'"' -f4; }
web_login() { # web_login ACCOUNT JAR
  local link code
  link=$(hostctx account login-link "$1" --url "$SERVER_URL" --db "$DB" | head -1)
  rm -f "$2"
  curl -fs -c "$2" -b "$2" -o "$WORK/login.html" "$link" || return 1
  code=$(grep -o 'value="[0-9a-f]\{64\}"' "$WORK/login.html" | head -1 | cut -d'"' -f2)
  curl -s -c "$2" -b "$2" -H "Origin: $SERVER_URL" -o /dev/null -w '%{http_code}' --data-urlencode "code=$code" "$SERVER_URL/ui/login/code" | grep -q 303
}

make_invitation() { # sets INVITE_URL; $1 = precomputed machine name to pre-claim for robin ("" = none)
  local pre=$1 tok
  hostctx account add robin --admin --db "$DB" >/dev/null
  hostctx account add freund --db "$DB" >/dev/null
  tok=$(hostctx account token create robin --db "$DB" | sed -n 's/^token: //p')
  printf '{"server_url":"%s","token":"%s","machine":"host-admin"}\n' "$SERVER_URL" "$tok" >"$HOST_HOME/.config/ghosttree/config.json"
  hostctx org create testorg >/dev/null || return 1
  hostctx org default testorg >/dev/null || return 1
  # A project exists once one of its owner's sessions reached the server.
  curl -fs -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' \
    -d '{"harness":"claude-code","external_id":"seed-1","scope":{"project":"github.com/gt-test/demo","branch":"main","machine":"host-admin"},"cwd":"/tmp/demo","started_at":"2026-10-11T14:00:00Z","last_seen_at":"2026-10-11T14:00:00Z"}' \
    "$SERVER_URL/api/sessions" >/dev/null || return 1
  if [ -n "$pre" ]; then
    local t2
    t2=$(hostctx account token create robin --machine "$pre" --db "$DB" | sed -n 's/^token: //p')
    curl -fs -H "Authorization: Bearer $t2" -H 'Content-Type: application/json' \
      -d "{\"harness\":\"claude-code\",\"external_id\":\"seed-2\",\"scope\":{\"project\":\"github.com/gt-test/demo\",\"branch\":\"main\",\"machine\":\"$pre\"},\"cwd\":\"/tmp/demo\",\"started_at\":\"2026-10-11T14:00:00Z\",\"last_seen_at\":\"2026-10-11T14:00:00Z\"}" \
      "$SERVER_URL/api/sessions" >/dev/null || return 1
  fi
  web_login robin "$WORK/jar-robin" || return 1
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

accept_invitation() { # as freund; sets INSTALL_CMD
  web_login freund "$WORK/jar-freund" || return 1
  curl -fs -b "$WORK/jar-freund" -c "$WORK/jar-freund" "$INVITE_URL" -o "$WORK/join.html" || return 1
  local csrf
  csrf=$(form_value csrf_token <"$WORK/join.html")
  curl -s -b "$WORK/jar-freund" -c "$WORK/jar-freund" -H "Origin: $SERVER_URL" -o /dev/null -w '%{http_code}' \
    --data-urlencode "csrf_token=$csrf" -d confirm_account=freund "$INVITE_URL/accept" | grep -q 303 || return 1
  curl -fs -b "$WORK/jar-freund" -c "$WORK/jar-freund" "$SERVER_URL/join/pair" -o "$WORK/pair.html" || return 1
  # The page breaks the command into spans; the text content is the command.
  INSTALL_CMD=$(sed 's/<[^>]*>//g' "$WORK/pair.html" | grep -Eo 'curl -fsSL [^ ]+ \| sh -s -- --pair [A-Z0-9]{4}-[A-Z0-9]{4}' | head -1 \
    | python3 -c 'import html,sys;print(html.unescape(sys.stdin.read().strip()))')
  [ -n "$INSTALL_CMD" ]
}

approve_machine() { # approve_machine CONFIRM_CODE
  curl -fs -b "$WORK/jar-freund" -c "$WORK/jar-freund" "$SERVER_URL/join/pair" -o "$WORK/pair2.html" || return 1
  local csrf nonce
  csrf=$(form_value csrf_token <"$WORK/pair2.html")
  nonce=$(form_value nonce <"$WORK/pair2.html")
  curl -s -b "$WORK/jar-freund" -c "$WORK/jar-freund" -H "Origin: $SERVER_URL" -o /dev/null -w '%{http_code}' \
    --data-urlencode "csrf_token=$csrf" -d "nonce=$nonce" -d "confirm_code=$1" -d confirm_account=freund -d decision=approve \
    "$SERVER_URL/join/pair/decide" | grep -q 303
}

# ------------------------------------------------------------------ VM side
vmroot() { incus exec "$VM" -- "$@"; }
vmid=""
vmx() { # run as the test user, ~/.local/bin NOT on PATH
  incus exec "$VM" --user "$vmid" --group "$vmid" --cwd /home/$VM_USER \
    --env HOME=/home/$VM_USER --env USER=$VM_USER --env LOGNAME=$VM_USER --env SHELL=/bin/bash \
    --env PATH=/usr/local/bin:/usr/bin:/bin --env XDG_RUNTIME_DIR="/run/user/$vmid" -- "$@"
}

vm_create() {
  step "create VM $VM (2 vCPU, 4 GiB, 15 GiB disk) and snapshot $SNAP"
  incus info "$VM" >/dev/null 2>&1 && { info "VM $VM exists, not recreating"; return 0; }
  incus launch "$IMAGE" "$VM" --vm -c limits.cpu=2 -c limits.memory=4GiB -d root,size=15GiB >/dev/null || return 1
  vm_wait_agent || return 1
  vmroot sh -c "useradd -m -s /bin/bash -G sudo $VM_USER" || return 1
  incus stop "$VM" && incus snapshot create "$VM" "$SNAP"
}

vm_wait_agent() {
  local i
  for i in $(seq 1 90); do incus exec "$VM" -- true >/dev/null 2>&1 && return 0; sleep 2; done
  return 1
}
vm_wait_net() {
  local i
  for i in $(seq 1 60); do
    vmroot sh -c "ip -4 -br a show scope global | grep -q '10\\.'" >/dev/null 2>&1 && return 0
    [ "$i" = 15 ] && vmroot networkctl reconfigure enp5s0 >/dev/null 2>&1
    sleep 2
  done
  return 1
}

vm_reset() {
  step "VM: restore snapshot $SNAP, boot, bring up the server forwarder"
  incus info "$VM" >/dev/null 2>&1 || { echo "VM $VM missing (use --create-vm)" >&2; return 1; }
  incus stop "$VM" --force >/dev/null 2>&1
  incus snapshot restore "$VM" "$SNAP" >/dev/null || return 1
  incus start "$VM" || return 1
  vm_wait_agent || { echo "VM agent did not come up" >&2; return 1; }
  vm_wait_net || { echo "VM got no IPv4 (is the ufw DHCP rule in place?)" >&2; return 1; }
  vmid=$(vmroot id -u "$VM_USER") || { echo "user $VM_USER missing in $SNAP" >&2; return 1; }
  # Test harness only (not part of the tested path): the loopback forwarder.
  incus file push "$HERE/tcpfwd.py" "$VM/usr/local/sbin/tcpfwd.py" --mode 755 >/dev/null || return 1
  local unit="$WORK/gt-fwd.service"
  cat >"$unit" <<EOF
[Unit]
Description=test harness: VM loopback port to the host server
After=network-online.target
[Service]
ExecStart=/usr/bin/python3 /usr/local/sbin/tcpfwd.py 127.0.0.1 $PORT $BRIDGE_IP $PORT
Restart=always
[Install]
WantedBy=multi-user.target
EOF
  incus file push "$unit" "$VM/etc/systemd/system/gt-fwd.service" >/dev/null || return 1
  vmroot sh -c 'systemctl daemon-reload && systemctl enable --now gt-fwd' >/dev/null 2>&1 || return 1
  local i
  for i in $(seq 1 15); do
    [ "$(vmroot curl -s -o /dev/null -w '%{http_code}' "$SERVER_URL/api/health")" = 200 ] && return 0
    sleep 1
  done
  echo "VM cannot reach the server through the forwarder" >&2
  return 1
}

# Wait until SQL on the server DB returns a row; $1 timeout, $2 sql.
wait_sql() {
  local i out
  for i in $(seq 1 "$1"); do
    out=$(sqlite3 "$DB" "$2" 2>/dev/null)
    [ -n "$out" ] && { printf '%s\n' "$out"; return 0; }
    sleep 1
  done
  return 1
}

# ---------------------------------------------------------------- scenarios
run_scenario() { # run_scenario NAME PRECLAIMED_MACHINE
  local name=$1 pre=$2 cts
  step "scenario '$name': fresh server, accounts, organization, invitation"
  start_server || { fail "server start"; return; }
  start_fwd
  if make_invitation "$pre"; then pass "invitation link created ($INVITE_URL)"; else fail "invitation link"; return; fi
  vm_reset || { fail "VM reset"; return; }
  pass "VM reset to '$SNAP' and reachable"
  local before
  before=$(vmx sh -c 'ls -A ~ | tr "\n" " "')
  info "VM home before install: $before"
  if vmx sh -c 'command -v ctx curl sha256sum' >"$WORK/pre.out" 2>&1; then :; fi
  info "pre-installed tools: $(tr '\n' ' ' <"$WORK/pre.out")"

  step "scenario '$name': accept the invitation, read the command off the pairing page"
  if accept_invitation; then pass "pairing page shows: $INSTALL_CMD"; else fail "accept invitation / read command"; return; fi
  case $INSTALL_CMD in
    "curl -fsSL $SERVER_URL/install.sh | sh -s -- --pair "*) pass "command points at the server and has a pairing code" ;;
    *) fail "unexpected command shape: $INSTALL_CMD" ;;
  esac

  step "scenario '$name': run exactly that command in the VM as $VM_USER"
  rm -f "$WORK/confirm.txt"
  python3 "$HERE/ptyrun.py" --log "$WORK/join.log" --timeout 240 \
    --answer 'Add .* to .*\? \[y/N\]=n' \
    --answer 'Connect this machine as account.*\[y/N\]=y' \
    --capture 'Type this code in your browser: ([A-Z0-9]{4})='"$WORK/confirm.txt" \
    -- incus exec "$VM" -t --user "$vmid" --group "$vmid" --cwd /home/$VM_USER \
       --env HOME=/home/$VM_USER --env USER=$VM_USER --env SHELL=/bin/bash \
       --env PATH=/usr/local/bin:/usr/bin:/bin --env XDG_RUNTIME_DIR="/run/user/$vmid" \
       -- sh -c "$INSTALL_CMD" &
  local pyp=$! i
  for i in $(seq 1 120); do [ -s "$WORK/confirm.txt" ] && break; kill -0 "$pyp" 2>/dev/null || break; sleep 1; done
  if [ -s "$WORK/confirm.txt" ]; then
    if approve_machine "$(cat "$WORK/confirm.txt")"; then pass "machine approved on the pairing page (code $(cat "$WORK/confirm.txt"))"; else fail "approving on the pairing page"; fi
  else
    fail "ctx never printed a confirmation code"
  fi
  wait "$pyp"; local jrc=$?
  say "--- join output ---"; indent <"$WORK/join.log"
  if [ "$jrc" = 0 ]; then pass "install command exited 0"; else fail "install command exited $jrc"; fi
  check "ctx installed to ~/.local/bin" vmx test -x /home/$VM_USER/.local/bin/ctx
  grep -q 'Checksum OK' "$WORK/join.log" && pass "archive checksum verified" || fail "no 'Checksum OK' in installer output"
  check "ctx not on PATH (precondition of the hurdle test)" bash -c "! incus exec '$VM' --user $vmid --env PATH=/usr/local/bin:/usr/bin:/bin -- sh -c 'command -v ctx'"
  local machine
  machine=$(sed -n 's/^Account [^,]*, org [^,]*, machine \(.*\)$/\1/p' "$WORK/join.log" | head -1)
  info "machine name chosen: ${machine:-?}"
  if [ -n "$pre" ]; then
    if [ -n "$machine" ] && [ "$machine" != "$pre" ]; then pass "taken name '$pre' -> free name '$machine', no abort"; else fail "name '$pre' was held by another account; ctx used '${machine:-?}'"; fi
  fi
  local row
  row=$(sqlite3 "$DB" "select hostname||'|'||account_id from machines where hostname='${machine:-x}'")
  [ "$row" = "${machine:-x}|2" ] && pass "server lists machine '$machine' for account freund (id 2)" || fail "server machines row: '${row:-none}'"
  check "ctx status reports server reachable" bash -c "incus exec '$VM' --user $vmid --env HOME=/home/$VM_USER -- /home/$VM_USER/.local/bin/ctx status | grep -q 'server .*reachable'"
  info "after join, before any harness: $(vmx /home/$VM_USER/.local/bin/ctx status 2>&1 | grep -E '^(watch|claude|codex)' | tr -s ' ' | tr '\n' ';')"
  grep -q 'not found on this machine' "$WORK/join.log" && info "HURDLE: join on a machine without Claude/Codex sets nothing up, collector not installed"

  step "scenario '$name': ctx doctor on the machine as joined"
  vmx /home/$VM_USER/.local/bin/ctx doctor >"$WORK/doctor0.out" 2>&1; local drc=$?
  info "ctx doctor (all harnesses, none installed) exit $drc: $(grep -c ' FAIL ' "$WORK/doctor0.out") FAIL lines"

  step "scenario '$name': ctx install claude against an empty ~/.claude (no user session bus yet)"
  vmx mkdir -p /home/$VM_USER/.claude
  vmx /home/$VM_USER/.local/bin/ctx install claude >"$WORK/install-claude.out" 2>&1; local irc=$?
  indent <"$WORK/install-claude.out"
  [ "$irc" = 0 ] && pass "ctx install claude exit 0" || fail "ctx install claude exit $irc"
  grep -q 'collector not started' "$WORK/install-claude.out" && info "without a user bus: $(grep 'collector not started' "$WORK/install-claude.out" | sed 's/.*collector not started/collector not started/' | cut -c1-120)"
  check "settings.json hook commands are absolute paths" vmx python3 -c '
import json,sys
s=json.load(open("/home/robin/.claude/settings.json"))
cmds=[h["command"] for ev in s["hooks"].values() for g in ev for h in g["hooks"]]
assert cmds and all(c.startswith("/") for c in cmds), cmds
print(len(cmds),"hook commands, all absolute")'
  check "MCP entry uses the absolute ctx path" vmx python3 -c '
import json
c=json.load(open("/home/robin/.claude.json"))["mcpServers"]["ghosttree"]["command"]
assert c.startswith("/"), c
print(c)'

  step "scenario '$name': login session (linger), collector service"
  vmroot loginctl enable-linger "$VM_USER"
  for i in $(seq 1 20); do vmroot test -S "/run/user/$vmid/bus" && break; sleep 1; done
  vmx /home/$VM_USER/.local/bin/ctx install watch 2>&1 | indent
  check "ghosttree-watch.service active" bash -c "[ \"\$(incus exec '$VM' --user $vmid --env XDG_RUNTIME_DIR=/run/user/$vmid -- systemctl --user is-active ghosttree-watch)\" = active ]"
  vmx /home/$VM_USER/.local/bin/ctx doctor claude >"$WORK/doctor1.out" 2>&1; drc=$?
  say "--- ctx doctor claude ---"; indent <"$WORK/doctor1.out"
  [ "$drc" = 0 ] && pass "ctx doctor claude exit 0" || fail "ctx doctor claude exit $drc"
  ! grep -q ' FAIL ' "$WORK/doctor1.out" && pass "ctx doctor claude: no FAIL lines" || fail "ctx doctor claude has FAIL lines"

  step "scenario '$name': fake Claude transcript reaches the server"
  cts=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)
  vmx sh -c "mkdir -p ~/.claude/projects/-home-robin-demo && printf '%s\n%s\n' \
    '{\"type\":\"user\",\"cwd\":\"/home/robin/demo\",\"gitBranch\":\"main\",\"sessionId\":\"fake-$name\",\"timestamp\":\"$cts\",\"message\":{\"role\":\"user\",\"content\":\"hello from the fresh vm\"}}' \
    '{\"type\":\"assistant\",\"cwd\":\"/home/robin/demo\",\"gitBranch\":\"main\",\"sessionId\":\"fake-$name\",\"timestamp\":\"$cts\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"fresh vm says hi\"}]}}' \
    > ~/.claude/projects/-home-robin-demo/fake-$name.jsonl"
  local q="select machine||'|'||msg_count from sessions where external_id='fake-$name' and account_id=2 and msg_count>=2"
  if row=$(wait_sql 100 "$q"); then pass "session fake-$name on the server within the poll window: $row"; else fail "fake session did not reach the server in 100 s (collector polls every 30 s)"; tail -5 "$WORK/server.log" | cut -c1-200 | indent; fi

  if [ -z "$pre" ]; then
    step "scenario '$name': VM reboot, linger keeps the collector"
    incus restart "$VM" || fail "VM restart"
    vm_wait_agent; vm_wait_net
    vmx true 2>/dev/null
    for i in $(seq 1 30); do
      [ "$(vmx systemctl --user is-active ghosttree-watch 2>/dev/null)" = active ] && break; sleep 2
    done
    [ "$(vmx systemctl --user is-active ghosttree-watch 2>&1)" = active ] && pass "collector active after reboot (linger: $(vmroot loginctl show-user $VM_USER -p Linger))" || fail "collector not active after reboot"
    vmx sh -c "printf '%s\n' '{\"type\":\"user\",\"cwd\":\"/home/robin/demo\",\"gitBranch\":\"main\",\"sessionId\":\"fake-$name\",\"timestamp\":\"$cts\",\"message\":{\"role\":\"user\",\"content\":\"after reboot\"}}' >> ~/.claude/projects/-home-robin-demo/fake-$name.jsonl"
    if wait_sql 100 "select msg_count from sessions where external_id='fake-$name' and msg_count>=3" >/dev/null; then pass "line written after the reboot reached the server"; else fail "post-reboot line did not arrive"; fi
  fi
}

# --------------------------------------------------------------------- main
say "fresh-vm e2e on $(git -C "$REPO" rev-parse --short HEAD) ($(git -C "$REPO" rev-parse --abbrev-ref HEAD)), server $SERVER_URL, VM $VM, $(date -u +%FT%TZ)"
build
setup_ufw
[ "$CREATE_VM" = 1 ] && { vm_create || { echo "VM creation failed" >&2; exit 1; }; }
case $SCENARIO in all | plain) run_scenario plain "" ;; esac
case $SCENARIO in all | taken) run_scenario taken "$VM" ;; esac

step "summary ($(($(date +%s) - t0)) s)"
if [ "$FAILS" = 0 ]; then say "ALL CHECKS PASSED"; else say "$FAILS CHECK(S) FAILED"; fi
say "report: $REPORT   server log: $WORK/server.log   VM: $VM ($([ "$KEEP_VM" = 1 ] && echo left running || echo will be stopped))"
exit $((FAILS > 0))
