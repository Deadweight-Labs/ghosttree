#!/bin/bash
# Probe of the gardener's sandbox: starts a transient unit with exactly the
# sandbox settings of ghosttree-garden.service (read from the unit file, so the
# two cannot drift apart) and tries, inside it, to read what the session must not
# reach and to write what it must not change. Every attempt that should fail has
# to fail, the one that should work has to work; anything else is a failure and
# the exit status is 1.
#
#   sudo deploy/garden-sandbox-probe.sh [unit-file]   run the probe (needs root)
#   deploy/garden-sandbox-probe.sh --print [unit-file] print the systemd-run
#                                                    command line and stop
#
# Paths can be set in the environment for a layout other than the example unit:
#   GARDEN_DIR (default /var/lib/ghosttree/garden)
#   FORBIDDEN_FILES  space separated files that must not be readable. Default:
#                    one probe file in each place the unit hides.
set -u

print_only=0
if [ "${1:-}" = "--print" ]; then
	print_only=1
	shift
fi
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
unit="${1:-$here/ghosttree-garden.service}"
garden="${GARDEN_DIR:-/var/lib/ghosttree/garden}"

# Settings of the [Service] section that make up the sandbox. Everything else
# (ExecStart, Environment, timeouts, ...) is left out of the probe.
sandbox_keys='User|Group|TemporaryFileSystem|BindReadOnlyPaths|BindPaths|ReadOnlyPaths|ReadWritePaths|InaccessiblePaths|ProtectHome|ProtectSystem|PrivateTmp|PrivateDevices|PrivateUsers|NoNewPrivileges|CapabilityBoundingSet|AmbientCapabilities|RestrictSUIDSGID|RestrictNamespaces|RestrictRealtime|LockPersonality|RemoveIPC|ProtectKernelTunables|ProtectKernelModules|ProtectKernelLogs|ProtectControlGroups|ProtectClock|ProtectHostname|ProtectProc|ProcSubset|SystemCallArchitectures|SystemCallFilter|RestrictAddressFamilies|IPAddressDeny|IPAddressAllow|UMask'

props=()
section=""
while IFS= read -r line; do
	case "$line" in
	"["*"]") section="$line"; continue ;;
	esac
	[ "$section" = "[Service]" ] || continue
	key="${line%%=*}"
	[[ "$line" == *=* ]] || continue
	[[ "$key" =~ ^($sandbox_keys)$ ]] || continue
	props+=("-p" "$line")
done <"$unit"

if [ "${#props[@]}" -eq 0 ]; then
	echo "no sandbox settings found in $unit" >&2
	exit 2
fi

# The checks run as one shell inside the sandbox. Each prints PASS or FAIL.
# shellcheck disable=SC2016  # the checks are meant to expand inside the sandbox
checks='
fails=0
expect_fail() { # description command...
	d="$1"; shift
	if "$@" >/dev/null 2>&1; then echo "FAIL  $d: erreichbar"; fails=$((fails+1)); else echo "PASS  $d: gescheitert"; fi
}
expect_ok() {
	d="$1"; shift
	if "$@" >/dev/null 2>&1; then echo "PASS  $d: gelungen"; else echo "FAIL  $d: gescheitert"; fails=$((fails+1)); fi
}
garden="$GARDEN_DIR"
expect_ok   "Checkout lesen"                     test -r "$garden/repo/.git/HEAD"
expect_fail "Checkout beschreiben"               sh -c "echo x > $garden/repo/probe-write"
expect_fail "Checkout verändern (.git/config)"   sh -c "echo x >> $garden/repo/.git/config"
expect_fail "ghosttree-Daten lesen"              ls /var/lib/ghosttree/data
expect_fail "ghosttree-Datenbank lesen"          sh -c "cat /var/lib/ghosttree/data/*.db"
expect_fail "private Daten lesen"                ls /var/lib/ghosttree/private
expect_fail "Backups lesen (ghosttree)"          ls /var/lib/ghosttree/backups
expect_fail "Backups lesen (var/backups)"        ls /var/backups
expect_fail "Server-Umgebung lesen"              cat /etc/ghosttree/server.env
expect_fail "/home lesen"                        ls /home
expect_fail "/root lesen"                        ls /root
expect_fail "fremde Prozess-Umgebung lesen"      sh -c "cat /proc/1/environ"
expect_fail "System schreiben (/usr)"              sh -c "echo x > /usr/probe-write"
expect_fail "Netz außer Loopback"                timeout 5 bash -c "exec 3<>/dev/tcp/203.0.113.1/80"
for f in $FORBIDDEN_FILES; do
	expect_fail "Datei $f lesen" cat "$f"
done
exit $fails
'

cmd=(systemd-run --quiet --pipe --wait --collect "${props[@]}" -E "GARDEN_DIR=$garden" -E "FORBIDDEN_FILES=${FORBIDDEN_FILES:-}" /bin/bash -c "$checks")

if [ "$print_only" = 1 ]; then
	printf '%q ' "${cmd[@]}"
	echo
	exit 0
fi
if [ "$(id -u)" -ne 0 ]; then
	echo "the probe starts a transient unit and needs root" >&2
	exit 2
fi
"${cmd[@]}"
status=$?
if [ "$status" -eq 0 ]; then
	echo "Sandbox hält."
else
	echo "Sandbox hält NICHT ($status Versuche außerhalb sind nicht gescheitert oder der Start schlug fehl)." >&2
fi
[ "$status" -eq 0 ]
