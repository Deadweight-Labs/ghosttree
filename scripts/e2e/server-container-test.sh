#!/bin/sh
# Runs INSIDE a container whose init is systemd (scripts/e2e/server-containers.sh
# starts it): the one-line installer sets up a server with systemd, the login link
# works, ctx server update moves it to a newer release, and an update to a
# release that does not come up goes back. No terminal anywhere.
#
# Expects in /e2e: ctx-0.1.0, ctx-0.1.1 (built with the test key), key (the
# secret key, made by minisign -G -W), key.pub, install.sh.
# shellcheck disable=SC2016
set -eu

fails=0
ok() { printf 'PASS %s\n' "$1"; }
bad() { printf 'FAIL %s\n' "$1"; fails=$((fails + 1)); }
check() { # description, command...
  desc=$1; shift
  if "$@" >/tmp/check.out 2>&1; then ok "$desc"; else bad "$desc"; sed 's/^/     /' /tmp/check.out | tail -n 15; fi
}

case "$(uname -m)" in x86_64) a=amd64 ;; aarch64) a=arm64 ;; *) echo "unsupported arch"; exit 1 ;; esac
pub=$(sed -n 2p /e2e/key.pub)

# ---- a fake GitHub: releases signed with the test key ------------------------
rel=/srv/rel
mk_release() { # tag, binary
  tag=$1 v=${1#v}
  d=$rel/dl/$tag
  mkdir -p "$d"
  w=$(mktemp -d)
  cp "$2" "$w/ctx"
  chmod 755 "$w/ctx"
  tar -czf "$d/ghosttree_${v}_linux_$a.tar.gz" -C "$w" ctx
  (cd "$d" && sha256sum "ghosttree_${v}_linux_$a.tar.gz" >checksums.txt &&
    minisign -S -s /e2e/key -m checksums.txt -x checksums.txt.minisig -t "ghosttree $tag" >/dev/null)
}
mkdir -p "$rel/api/repos/Deadweight-Labs/ghosttree/releases"
mk_release v0.1.0 /e2e/ctx-0.1.0
mk_release v0.1.1 /e2e/ctx-0.1.1
# A release that runs, upgrades nothing and cannot serve: the service does not
# come up and the update has to go back.
cat >/tmp/ctx-broken <<'EOF'
#!/bin/sh
case "$1" in version) echo "ctx 0.1.2" ;; upgrade-schema) echo "schema already current, nothing to do" ;; *) echo "broken release" >&2; exit 1 ;; esac
EOF
mk_release v0.1.2 /tmp/ctx-broken
printf '{"tag_name": "v0.1.0"}\n' >"$rel/api/repos/Deadweight-Labs/ghosttree/releases/latest"
(cd "$rel" && nohup python3 -m http.server 18080 --bind 127.0.0.1 >/tmp/relsrv.log 2>&1 &)
i=0; until curl -fsS http://127.0.0.1:18080/dl/v0.1.0/checksums.txt >/dev/null 2>&1; do i=$((i + 1)); [ $i -lt 50 ] || { echo "release server did not start"; exit 1; }; sleep 0.2; done

export GHOSTTREE_RELEASE_BASE=http://127.0.0.1:18080/dl
export GHOSTTREE_API_URL=http://127.0.0.1:18080/api

# ---- install: the one line, the server way ----------------------------------
sed "s|__GHOSTTREE_MINISIGN_PUBKEY__|$pub|" /e2e/install.sh >/tmp/install.sh
# A model provider, so that the service has to open llm.json (a dynamic user, in
# /etc/ghosttree: the install used to leave that directory closed to it).
printf 'sk-test-e2e\n' >/tmp/ai-key
if sh /tmp/install.sh --server --yes --admin alex --domain gt.example.test --language en \
  --ai anthropic --ai-key-file /tmp/ai-key --ai-model claude-test >/tmp/install.out 2>&1 </dev/null; then
  ok "install.sh --server --yes finishes without a terminal"
else
  bad "install.sh --server --yes finishes without a terminal"; sed 's/^/     /' /tmp/install.out | tail -n 40
fi
cat /tmp/install.out

check "the newest stable release was found and checked" grep -q 'Signature OK (ghosttree v0.1.0)' /tmp/install.out
check "ctx is installed system-wide at v0.1.0" sh -c '[ "$(/usr/local/bin/ctx version)" = "ctx 0.1.0" ]'
check "the service is active" systemctl is-active --quiet ghosttree.service
check "the backup timer is active" systemctl is-active --quiet ghosttree-backup.timer
check "the server answers" curl -fsS http://127.0.0.1:8474/api/health
check "the server key is private" sh -c '[ "$(stat -c %a /etc/ghosttree/secret-key)" = 600 ]'
check "the first backup exists" sh -c 'ls /var/lib/ghosttree/backups/ghosttree-*.db'
check "the Caddy configuration was written" grep -q 'reverse_proxy 127.0.0.1:8474' /etc/ghosttree/proxy/Caddyfile
check "the nginx configuration was written" grep -q 'server_name gt.example.test;' /etc/ghosttree/proxy/nginx.conf
check "ctx server doctor is green" /usr/local/bin/ctx server doctor
check "the service's own user can read llm.json" sh -c 'systemd-run --pipe --wait --collect --quiet -p DynamicUser=yes -p User=ghosttree cat /etc/ghosttree/llm.json | grep -q "\"credential\": \"llm-key\""'
check "the service started with it (no permission error in its journal)" sh -c '! journalctl -u ghosttree --no-pager | grep -qi "permission denied"'
check "the service's own user cannot read the server key" sh -c '! systemd-run --pipe --wait --collect --quiet -p DynamicUser=yes -p User=ghosttree cat /etc/ghosttree/secret-key >/dev/null 2>&1'
check "llm.json holds no key and can be read" sh -c '[ "$(stat -c %a /etc/ghosttree/llm.json)" = 644 ] && ! grep -q sk-test /etc/ghosttree/llm.json'
check "/etc/ghosttree can be entered but not listed" sh -c '[ "$(stat -c %a /etc/ghosttree)" = 711 ]'
check "the key of the model provider is root only" sh -c '[ "$(stat -c %a /etc/ghosttree/llm-key)" = 600 ]'
# The unit is hardened and the server runs with it (the checks above are its proof).
for prop in NoNewPrivileges=yes PrivateDevices=yes ProtectKernelTunables=yes ProtectKernelModules=yes ProtectKernelLogs=yes ProtectControlGroups=yes \
  RestrictNamespaces=yes LockPersonality=yes MemoryDenyWriteExecute=yes; do
  check "the service runs with $prop" sh -c "systemctl show ghosttree.service -p ${prop%%=*} | grep -qx '$prop'"
done
check "the service has no capabilities (proxy mode)" sh -c '[ "$(systemctl show ghosttree.service -p CapabilityBoundingSet --value)" = "" ]'
check "the service may use sockets only" sh -c 'systemctl show ghosttree.service -p RestrictAddressFamilies --value | grep -q AF_INET && ! systemctl show ghosttree.service -p RestrictAddressFamilies --value | grep -q AF_NETLINK'
check "the service filters system calls" sh -c 'systemctl show ghosttree.service -p SystemCallFilter --value | grep -q .'
check "the backup unit is hardened too" sh -c 'systemctl show ghosttree-backup.service -p NoNewPrivileges -p MemoryDenyWriteExecute | grep -c "=yes" | grep -qx 2'
systemd-analyze security --no-pager ghosttree.service 2>/dev/null | tail -n 1 || true

link=$(sed -n 's/^  \(https\{0,1\}:\/\/.*code=.*\)$/\1/p' /tmp/install.out | head -n 1)
if [ -n "$link" ]; then ok "the install ends with a login link"; else bad "the install ends with a login link"; fi
code=${link##*code=}
# What the browser does with the link: the page posts the code, from the public
# origin, and the answer carries the session cookie.
curl -s -D /tmp/hdr -o /dev/null -X POST -H "Origin: https://gt.example.test" \
  --data-urlencode "code=$code" -d codepage=1 http://127.0.0.1:8474/ui/login/code || true
cookie=$(sed -n 's/^[Ss]et-[Cc]ookie: \([^;]*\).*/\1/p' /tmp/hdr | head -n 1)
signed_in=$(curl -s -o /dev/null -w '%{http_code} %{url_effective}' -L -H "Cookie: $cookie" http://127.0.0.1:8474/ui/ || true)
case "$signed_in" in
  "200 "*login*) bad "the login link signs the administrator in ($signed_in)"; head -n 12 /tmp/hdr ;;
  "200 "*) ok "the login link signs the administrator in" ;;
  *) bad "the login link signs the administrator in ($signed_in)"; head -n 12 /tmp/hdr ;;
esac
second=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Origin: https://gt.example.test" --data-urlencode "code=$code" -d codepage=1 http://127.0.0.1:8474/ui/login/code || true)
if [ "$second" = 303 ] || [ "$second" = 302 ]; then bad "a login link works only once (second use: HTTP $second)"; else ok "a login link works only once (second use: HTTP $second)"; fi

# ---- the one line again, on a machine that has a server ---------------------
# install over a server would replace ctx under it; it points to update instead.
before=$(/usr/local/bin/ctx version)
if /usr/local/bin/ctx server install --yes >/tmp/again.out 2>&1; then bad "ctx server install on a server that exists is refused"; else ok "ctx server install on a server that exists is refused"; fi
check "the refusal points to ctx server update" grep -q 'ctx server update' /tmp/again.out
check "the refusal changed nothing" sh -c "[ \"\$(/usr/local/bin/ctx version)\" = \"$before\" ] && systemctl is-active --quiet ghosttree.service"
if sh /tmp/install.sh --server --yes --domain other.example.test >/tmp/again2.out 2>&1 </dev/null; then bad "install.sh --server with options on a server that exists is refused"; else ok "install.sh --server with options on a server that exists is refused"; fi
check "install.sh names the options that do not apply" grep -q 'these options do not apply' /tmp/again2.out
check "install.sh left ctx alone" sh -c "[ \"\$(/usr/local/bin/ctx version)\" = \"$before\" ]"

# ---- update to a newer release: the same one line, and it is an update -------
sed -i 's/"v0.1.0"/"v0.1.1"/' "$rel/api/repos/Deadweight-Labs/ghosttree/releases/latest"
if sh /tmp/install.sh --server --yes >/tmp/update.out 2>&1 </dev/null; then ok "install.sh --server on a server that exists updates it"; else bad "install.sh --server on a server that exists updates it"; fi
cat /tmp/update.out
check "it went through ctx server update" grep -q 'updating it with ctx server update' /tmp/update.out
check "ctx is v0.1.1" sh -c '[ "$(/usr/local/bin/ctx version)" = "ctx 0.1.1" ]'
check "the server runs v0.1.1" sh -c 'curl -fsS http://127.0.0.1:8474/api/version | grep -q "\"version\":\"0.1.1\""'
check "a copy from before the update exists" sh -c 'ls /var/lib/ghosttree/backups/pre-update-0.1.0-to-0.1.1-*.db'
check "the old ctx is kept" sh -c '[ -x /usr/local/bin/ctx.prev-0.1.0 ]'
check "the account survived the update" sh -c 'systemd-run --pipe --wait --collect --quiet -p DynamicUser=yes -p User=ghosttree -p StateDirectory=ghosttree /usr/local/bin/ctx account list --db /var/lib/ghosttree/ghosttree.db | grep -q "	alex	"'
check "updating to the same version does nothing" sh -c '/usr/local/bin/ctx server update --version v0.1.1 | grep -q "already runs"'

# ---- going back to an older release needs --force ----------------------------
if /usr/local/bin/ctx server update --version v0.1.0 >/tmp/down.out 2>&1; then bad "an update to an older release is refused"; else ok "an update to an older release is refused"; fi
check "the refusal says downgrade and names --force" sh -c 'grep -q downgrade /tmp/down.out && grep -q -- --force /tmp/down.out'
check "the downgrade changed nothing" sh -c '[ "$(/usr/local/bin/ctx version)" = "ctx 0.1.1" ] && systemctl is-active --quiet ghosttree.service'

# ---- an update that does not come up goes back ------------------------------
if /usr/local/bin/ctx server update --version v0.1.2 --wait 15s >/tmp/broken.out 2>&1; then bad "a broken release is reported as a failed update"; else ok "a broken release is reported as a failed update"; fi
cat /tmp/broken.out
check "the message says the old server runs again" grep -q 'runs again' /tmp/broken.out
check "ctx is back at v0.1.1" sh -c '[ "$(/usr/local/bin/ctx version)" = "ctx 0.1.1" ]'
check "the service is active again" systemctl is-active --quiet ghosttree.service
check "the server answers v0.1.1 again" sh -c 'curl -fsS http://127.0.0.1:8474/api/version | grep -q "\"version\":\"0.1.1\""'
check "the account is still there" sh -c 'systemd-run --pipe --wait --collect --quiet -p DynamicUser=yes -p User=ghosttree -p StateDirectory=ghosttree /usr/local/bin/ctx account list --db /var/lib/ghosttree/ghosttree.db | grep -q "	alex	"'
check "the failed database was kept aside" sh -c 'ls /var/lib/ghosttree/ghosttree.db.failed-*'

# ---- a tampered release never gets as far as the server ---------------------
tamper=$rel/dl/v0.1.1/ghosttree_0.1.1_linux_$a.tar.gz
cp "$tamper" /tmp/good.tgz
printf 'x' >>"$tamper"
if /usr/local/bin/ctx server update --version v0.1.1 --force >/tmp/tamper.out 2>&1; then bad "a tampered archive is refused"; else ok "a tampered archive is refused"; fi
check "the refusal names the checksum" grep -q 'checksum mismatch' /tmp/tamper.out
check "the server was not touched" systemctl is-active --quiet ghosttree.service
cp /tmp/good.tgz "$tamper"

if [ "$fails" -ne 0 ]; then echo "$fails check(s) failed"; exit 1; fi
echo "all checks passed"
