#!/bin/bash
# sandbox-run.sh <timeout-s> <befehl> [argumente...]
# Läuft in der Sandbox als Agenten-Benutzer. Startet die Weiterleitungen aus /bench-in/forwards
# (je Zeile "<port> <unix-socket>": 127.0.0.1:<port> im leeren Netz der Sandbox -> Socket des Hosts),
# führt den Befehl mit Zeitgrenze aus und räumt die Weiterleitungen danach wieder ab.
# Die Zeitgrenze liegt hier und nicht im Harness: ein abgebrochener `docker exec` lässt den Prozess
# im Container sonst weiterlaufen.
set -u
to=$1
shift
pids=()
ports=()
while read -r port sock; do
  [ -n "${port:-}" ] || continue
  socat "TCP-LISTEN:${port},bind=127.0.0.1,fork,reuseaddr" "UNIX-CONNECT:${sock}" &
  pids+=("$!")
  ports+=("$port")
done < /bench-in/forwards
trap 'kill "${pids[@]}" 2>/dev/null' EXIT
for port in "${ports[@]}"; do
  for _ in $(seq 1 50); do
    (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null && break
    sleep 0.1
  done
done
timeout -k 20 "$to" "$@"
