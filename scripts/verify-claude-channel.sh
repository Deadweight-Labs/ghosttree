#!/usr/bin/env bash
# Manual, real end-to-end check of `ctx claude`: starts a private ghosttree
# server, runs Claude Code in tmux through the launcher, sends it a mention via
# `ctx coord`, and waits until Claude answers with the channel's reply tool.
# Prints PASS or FAIL. Needs claude (logged in), tmux, sqlite3, python3, go.
# Not run in CI: it uses a real Claude session.
#
# Env: KEEP=1 keeps the work dir; OUT=<dir> copies the evidence there;
#      TIMEOUT=<seconds> (default 180); CTX=<ctx binary> skips the build;
#      MODEL=<alias> is passed to claude as --model (default opus), so a model
#      in ~/.claude/settings.json that Claude Code rejects does not matter.
set -u
cd "$(dirname "$0")/.." || exit 1
TIMEOUT=${TIMEOUT:-180}
MODEL=${MODEL:-opus}
WORK=$(mktemp -d /tmp/gt-claude-channel-e2e.XXXXXX)
SESSION=gt-claude-e2e-$$
PORT=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])')
AGENT="claude:e2e:$(python3 -c 'import uuid;print(uuid.uuid4())')"
TOKEN_FILE=$WORK/token
SERVER_PID=
RESULT=1
PROJECT_DIR=$WORK/project
fail() { echo "FAIL: $*"; exit 1; }

# Runs on every exit, including early ones and INT/TERM.
cleanup() {
  trap - EXIT INT TERM
  tmux capture-pane -p -t "$SESSION" -S -300 >"$WORK/pane.txt" 2>/dev/null
  tmux kill-session -t "$SESSION" 2>/dev/null
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null
  if [ -n "${OUT:-}" ]; then
    mkdir -p "$OUT"
    cp "$WORK/pane.txt" "$WORK/server.log" "$WORK/coord-state.txt" "$WORK/claude-debug.txt" "$OUT"/ 2>/dev/null
    echo "evidence: $OUT"
  fi
  [ -z "${KEEP:-}" ] && rm -rf "$WORK"
  [ "$RESULT" = 0 ] && echo PASS
  exit "$RESULT"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

for tool in claude tmux sqlite3 python3; do
  command -v "$tool" >/dev/null || { echo "FAIL: $tool not found"; exit 1; }
done
CTX=${CTX:-$WORK/ctx}
[ -x "$CTX" ] || go build -o "$CTX" ./cmd/ctx || fail "build ctx"

export XDG_CONFIG_HOME=$WORK/config
mkdir -p "$PROJECT_DIR"
DB=$WORK/ghosttree.db
"$CTX" person add e2e --db "$DB" | sed -n 's/^token: //p' >"$TOKEN_FILE"
[ -s "$TOKEN_FILE" ] || fail "person add"
"$CTX" serve --db "$DB" --listen "127.0.0.1:$PORT" >"$WORK/server.log" 2>&1 &
SERVER_PID=$!
for _ in $(seq 50); do curl -fs "http://127.0.0.1:$PORT/" >/dev/null 2>&1 && break; sleep 0.2; done
"$CTX" setup --server "http://127.0.0.1:$PORT" --token "$(cat "$TOKEN_FILE")" >/dev/null || fail "ctx setup"

# claude runs in a scratch directory, not a repository: the machine room is the
# shared room. XDG_CONFIG_HOME is passed through so ctx channel finds the lab.
tmux new-session -d -s "$SESSION" -x 200 -y 50 -c "$PROJECT_DIR" \
  "env XDG_CONFIG_HOME=$XDG_CONFIG_HOME $CTX claude --agent '$AGENT' -- --model '$MODEL' --debug-file $WORK/claude-debug.txt"

# Accepting Claude's trust dialog for the scratch directory adds a project entry
# to ~/.claude.json. A separate CLAUDE_CONFIG_DIR would avoid it but loses the
# login, so the entry stays; this script never edits ~/.claude.json itself.
echo "note: this run trusts $PROJECT_DIR in ~/.claude.json (harmless; remove the"
echo "      key .projects[\"$PROJECT_DIR\"] there by hand if you want it gone)"
# Accept the trust dialog (default selection is "No, exit", so Down first) for
# the scratch directory only, and the development-channel confirmation (default
# is "I am using this for local development", so Enter).
deadline=$((SECONDS + 90))
registered=
while [ $SECONDS -lt $deadline ]; do
  pane=$(tmux capture-pane -p -t "$SESSION" 2>/dev/null)
  if grep -q "Yes, I trust this folder" <<<"$pane"; then tmux send-keys -t "$SESSION" Down Enter; sleep 4; continue; fi
  if grep -qi "local development" <<<"$pane" && grep -qi "I am using this for local development" <<<"$pane"; then tmux send-keys -t "$SESSION" Enter; sleep 4; continue; fi
  if "$CTX" coord peers --machine 2>/dev/null | grep -qF "$AGENT"; then registered=1; break; fi
  sleep 1
done
[ -n "$registered" ] || fail "channel never registered $AGENT (see pane); check login and the development-channel prompt"
sleep 8 # let the session settle at its prompt

MARK="E2E-$(date +%s)"
"$CTX" coord send "@$AGENT $MARK: reply via the reply tool with the exact text GOT $MARK" --machine --mention "$AGENT" >/dev/null || fail "coord send"
MSG=$(sqlite3 "$DB" "select max(id) from coord_messages where body like '%$MARK%';")

reply="" state=""
deadline=$((SECONDS + TIMEOUT))
while [ $SECONDS -lt $deadline ]; do
  reply=$(sqlite3 "$DB" "select body from coord_messages where reply_to=$MSG and sender_external_id='$AGENT' limit 1;")
  state=$(sqlite3 "$DB" "select state from coord_deliveries where message_id=$MSG and recipient_external_id='$AGENT';")
  [ -n "$reply" ] && [ "$state" = acked ] && break
  sleep 2
done
{
  echo "agent: $AGENT"; echo "message: $MSG ($MARK)"
  echo "delivery state: ${state:-none}"; echo "reply: ${reply:-none}"
  sqlite3 -header "$DB" "select * from coord_deliveries;"
  sqlite3 -header "$DB" "select id,sender_external_id,reply_to,body from coord_messages;"
} >"$WORK/coord-state.txt"
cat "$WORK/coord-state.txt"
[ -n "$reply" ] || fail "no reply from $AGENT within ${TIMEOUT}s"
grep -qF "$MARK" <<<"$reply" || fail "reply does not contain $MARK: $reply"
[ "$state" = acked ] || fail "delivery state is '${state:-none}', want acked"
RESULT=0
