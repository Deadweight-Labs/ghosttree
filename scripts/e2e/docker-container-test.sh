#!/bin/sh
# Runs INSIDE a fresh distribution container that can talk to the host's Docker
# (scripts/e2e/docker-containers.sh starts it): the one-line installer sets a
# server up with Docker, the login link signs in, ctx server update moves it to a
# newer image, an update to an image that does not come up goes back. No terminal.
#
# Expects in /e2e: ctx-0.1.0 (built with the test key), key, key.pub, install.sh;
# in the environment: PORT (the server's port on the host), RELPORT (the release
# server's port), PROJECT (compose project name), DIR (where the setup goes).
set -eu

# shellcheck disable=SC2016
fails=0
ok() { printf 'PASS %s\n' "$1"; }
bad() { printf 'FAIL %s\n' "$1"; fails=$((fails + 1)); }
check() { # description, command...
  desc=$1; shift
  if "$@" >/tmp/check.out 2>&1; then ok "$desc"; else bad "$desc"; sed 's/^/     /' /tmp/check.out | tail -n 15; fi
}

pub=$(sed -n 2p /e2e/key.pub)
rel=/srv/rel
mkdir -p "$rel/dl" "$rel/api/repos/Deadweight-Labs/ghosttree/releases"
# What a Docker install and update take from a release: the signed list, with
# ghosttree-image.txt, the digest of the image, in it.
sign_release() { # tag; the files of the release are in its directory
  d=$rel/dl/$1
  cp "/e2e/digests/${1#v}" "$d/ghosttree-image.txt"
  (cd "$d" && sha256sum ghosttree-image.txt >>checksums.txt &&
    minisign -S -s /e2e/key -m checksums.txt -x checksums.txt.minisig -t "ghosttree $1" >/dev/null)
}
for tag in v0.1.1 v0.1.2; do
  mkdir -p "$rel/dl/$tag"
  : >"$rel/dl/$tag/checksums.txt"
  sign_release "$tag"
done
(cd "$rel" && nohup python3 -m http.server "$RELPORT" --bind 127.0.0.1 >/tmp/relsrv.log 2>&1 &)
i=0; until curl -fsS "http://127.0.0.1:$RELPORT/dl/v0.1.1/checksums.txt" >/dev/null 2>&1; do i=$((i + 1)); [ $i -lt 50 ] || { echo "release server did not start"; exit 1; }; sleep 0.2; done
export GHOSTTREE_API_URL=http://127.0.0.1:$RELPORT/api
export GHOSTTREE_RELEASE_BASE=http://127.0.0.1:$RELPORT/dl
export COMPOSE_PROJECT_NAME=$PROJECT

# The archive of a release the installer would download: ctx itself, version 0.1.0.
a=$(uname -m); case "$a" in x86_64) a=amd64 ;; aarch64) a=arm64 ;; esac
mkdir -p "$rel/dl/v0.1.0" /tmp/w && cp /e2e/ctx-0.1.0 /tmp/w/ctx && chmod 755 /tmp/w/ctx
tar -czf "$rel/dl/v0.1.0/ghosttree_0.1.0_linux_$a.tar.gz" -C /tmp/w ctx
(cd "$rel/dl/v0.1.0" && sha256sum ./*.tar.gz | sed 's|\./||' >checksums.txt)
sign_release v0.1.0
printf '{"tag_name": "v0.1.0"}\n' >"$rel/api/repos/Deadweight-Labs/ghosttree/releases/latest"

sed "s|__GHOSTTREE_MINISIGN_PUBKEY__|$pub|" /e2e/install.sh >/tmp/install.sh
if sh /tmp/install.sh --docker --yes --admin alex --domain gt.example.test --language en \
  --dir "$DIR" --listen "127.0.0.1:$PORT" --image-repo "$IMAGE_REPO" >/tmp/install.out 2>&1 </dev/null; then
  ok "install.sh --docker --yes finishes without a terminal"
else
  bad "install.sh --docker --yes finishes without a terminal"; sed 's/^/     /' /tmp/install.out | tail -n 40
fi
cat /tmp/install.out

ctxd() { docker compose --project-directory "$DIR" "$@"; }
check "the install pinned the image to the digest of the signed release" grep -Fq "GHOSTTREE_IMAGE=$IMAGE_REPO:0.1.0@$(cat /e2e/digests/0.1.0)" "$DIR/.env"
check "ctx is installed for the user" sh -c '[ -x /root/.local/bin/ctx ]'
check "the server and backup containers run" sh -c "ctxd_out=\$(docker compose --project-directory $DIR ps --status running --services); echo \"\$ctxd_out\" | grep -q ghosttree && echo \"\$ctxd_out\" | grep -q backup"
check "the container is healthy" sh -c "for i in 1 2 3 4 5 6 7 8 9 10 11 12; do s=\$(docker compose --project-directory $DIR ps --format '{{.Health}}' ghosttree); [ \"\$s\" = healthy ] && exit 0; sleep 5; done; echo \$s; exit 1"
check "the server answers on the host" curl -fsS "http://127.0.0.1:$PORT/api/health"
check "the server key is in the secrets directory" sh -c "[ \"\$(stat -c %a $DIR/secrets)\" = 700 ] && [ -s $DIR/secrets/secret-key ]"
check "the container does not run as root" sh -c "docker compose --project-directory $DIR top ghosttree | awk 'NR>1 {print \$1}' | grep -vq '^root\$'"
check "the first backup is in the backups volume" sh -c "docker run --rm -v ${PROJECT}_backups:/b debian:12 sh -c 'ls /b | grep -q \"ghosttree-.*\\.db\"'"
check "the Caddy configuration was written" grep -q 'reverse_proxy 127.0.0.1' "$DIR/proxy/Caddyfile"
check "the nginx configuration was written" grep -q 'server_name gt.example.test;' "$DIR/proxy/nginx.conf"
check "ctx server doctor is green" /root/.local/bin/ctx server doctor --docker --dir "$DIR"

link=$(sed -n 's/^  \(https\{0,1\}:\/\/.*code=.*\)$/\1/p' /tmp/install.out | head -n 1)
if [ -n "$link" ]; then ok "the install ends with a login link"; else bad "the install ends with a login link"; fi
code=${link##*code=}
curl -s -D /tmp/hdr -o /dev/null -X POST -H "Origin: https://gt.example.test" \
  --data-urlencode "code=$code" -d codepage=1 "http://127.0.0.1:$PORT/ui/login/code" || true
cookie=$(sed -n 's/^[Ss]et-[Cc]ookie: \([^;]*\).*/\1/p' /tmp/hdr | head -n 1)
signed_in=$(curl -s -o /dev/null -w '%{http_code} %{url_effective}' -L -H "Cookie: $cookie" "http://127.0.0.1:$PORT/ui/" || true)
case "$signed_in" in
  "200 "*login*) bad "the login link signs the administrator in ($signed_in)" ;;
  "200 "*) ok "the login link signs the administrator in" ;;
  *) bad "the login link signs the administrator in ($signed_in)" ;;
esac

# ---- the same command again: refused; --force goes the way an update goes ----
FLAGS="--docker --yes --admin alex --domain gt.example.test --language en --dir $DIR --listen 127.0.0.1:$PORT --image-repo $IMAGE_REPO"
# shellcheck disable=SC2086
if /root/.local/bin/ctx server install $FLAGS >/tmp/again.out 2>&1 </dev/null; then bad "ctx server install --docker on a setup that exists is refused"; else ok "ctx server install --docker on a setup that exists is refused"; fi
check "the refusal points to the update" grep -q 'ctx server update --docker' /tmp/again.out
check "the refusal left the server running" curl -fsS "http://127.0.0.1:$PORT/api/health"
# shellcheck disable=SC2086
if /root/.local/bin/ctx server install $FLAGS --force --wait 90s >/tmp/force.out 2>&1 </dev/null; then ok "ctx server install --docker --force finishes"; else bad "ctx server install --docker --force finishes"; sed 's/^/     /' /tmp/force.out | tail -n 30; fi
check "--force copied the database first" sh -c "docker run --rm -v ${PROJECT}_backups:/b debian:12 sh -c 'ls /b | grep -q pre-update-0.1.0-to-0.1.0-'"
check "the account survived --force" sh -c "docker compose --project-directory $DIR exec -T ghosttree /usr/local/bin/ctx account list --db /data/ghosttree.db | grep -q '	alex	'"
check "the server answers after --force" curl -fsS "http://127.0.0.1:$PORT/api/health"

# ---- update to a newer image ------------------------------------------------
sed -i 's/"v0.1.0"/"v0.1.1"/' "$rel/api/repos/Deadweight-Labs/ghosttree/releases/latest"
if /root/.local/bin/ctx server update --docker --dir "$DIR" --wait 90s >/tmp/update.out 2>&1; then ok "ctx server update succeeds"; else bad "ctx server update succeeds"; fi
cat /tmp/update.out
check "the server runs 0.1.1" sh -c "curl -fsS http://127.0.0.1:$PORT/api/version | grep -q '\"version\":\"0.1.1\"'"
check ".env pins the new image to the digest of the signed release" grep -Fq "GHOSTTREE_IMAGE=$IMAGE_REPO:0.1.1@$(cat /e2e/digests/0.1.1)" "$DIR/.env"
check "a copy from before the update is in the backups volume" sh -c "docker run --rm -v ${PROJECT}_backups:/b debian:12 sh -c 'ls /b | grep -q pre-update-0.1.0-to-0.1.1-'"
check "the account survived the update" sh -c "docker compose --project-directory $DIR exec -T ghosttree /usr/local/bin/ctx account list --db /data/ghosttree.db | grep -q '	alex	'"
check "updating to the same version does nothing" sh -c "/root/.local/bin/ctx server update --docker --dir $DIR --version v0.1.1 | grep -q 'already runs'"

# ---- going back to an older release needs --force ---------------------------
if /root/.local/bin/ctx server update --docker --dir "$DIR" --version v0.1.0 >/tmp/down.out 2>&1; then bad "an update to an older release is refused"; else ok "an update to an older release is refused"; fi
check "the refusal says downgrade and names --force" sh -c 'grep -q downgrade /tmp/down.out && grep -q -- --force /tmp/down.out'
check "the downgrade changed nothing" sh -c "curl -fsS http://127.0.0.1:$PORT/api/version | grep -q '\"version\":\"0.1.1\"'"

# ---- an image that does not come up: back to the old one ---------------------
if /root/.local/bin/ctx server update --docker --dir "$DIR" --version v0.1.2 --wait 20s >/tmp/broken.out 2>&1; then bad "a broken image is reported as a failed update"; else ok "a broken image is reported as a failed update"; fi
cat /tmp/broken.out
check "the message says the old server runs again" grep -q 'runs again' /tmp/broken.out
check ".env names the old image again" grep -Fq "GHOSTTREE_IMAGE=$IMAGE_REPO:0.1.1@$(cat /e2e/digests/0.1.1)" "$DIR/.env"
check "the server answers 0.1.1 again" sh -c "curl -fsS http://127.0.0.1:$PORT/api/version | grep -q '\"version\":\"0.1.1\"'"
check "the account is still there" sh -c "docker compose --project-directory $DIR exec -T ghosttree /usr/local/bin/ctx account list --db /data/ghosttree.db | grep -q '	alex	'"
check "the failed database was kept aside" sh -c "docker run --rm -v ${PROJECT}_data:/d debian:12 sh -c 'ls /d | grep -q \"ghosttree.db.failed-\"'"

# ---- a swapped image digest and a tampered list are refused before anything is touched
printf 'sha256:%064d\n' 0 >"$rel/dl/v0.1.1/ghosttree-image.txt"
if /root/.local/bin/ctx server update --docker --dir "$DIR" --version v0.1.1 --force >/tmp/swap.out 2>&1; then bad "a swapped image digest is refused"; else ok "a swapped image digest is refused"; fi
check "the refusal names the checksum" grep -q 'checksum mismatch for ghosttree-image.txt' /tmp/swap.out
cp "/e2e/digests/0.1.1" "$rel/dl/v0.1.1/ghosttree-image.txt"
printf 'x' >>"$rel/dl/v0.1.1/checksums.txt"
if /root/.local/bin/ctx server update --docker --dir "$DIR" --version v0.1.1 --force >/tmp/tamper.out 2>&1; then bad "a tampered release list is refused"; else ok "a tampered release list is refused"; fi
check "the refusal names the signature" grep -q 'does not match the ghosttree release key' /tmp/tamper.out
check "the server is still up" curl -fsS "http://127.0.0.1:$PORT/api/health"

ctxd down -v >/dev/null 2>&1 || true
if [ "$fails" -ne 0 ]; then echo "$fails check(s) failed"; exit 1; fi
echo "all checks passed"
