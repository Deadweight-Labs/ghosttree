#!/bin/sh
# Erzeugt die Allowlist aus AGENTBENCH_ALLOWED_DOMAINS und startet tinyproxy.
# Die Liste steht bewusst nicht im Image: sie gehoert zur Kampagne, nicht zum
# Werkzeug, und muss im Bericht auftauchen koennen.
set -eu

: "${AGENTBENCH_ALLOWED_DOMAINS:?AGENTBENCH_ALLOWED_DOMAINS is required}"

filter=/etc/tinyproxy/filter
: > "$filter"
for domain in $AGENTBENCH_ALLOWED_DOMAINS; do
  # Punkte maskieren, sonst passt "api.anthropic.com" auch auf
  # "apiXanthropicYcom" — ein Angreifer braucht dafuer nur eine Domain.
  escaped=$(printf '%s' "$domain" | sed 's/\./\\./g')
  printf '(^|\\.)%s$\n' "$escaped" >> "$filter"
done
chmod 0644 "$filter"

echo "agentbench proxy: allowing $AGENTBENCH_ALLOWED_DOMAINS" >&2
exec tinyproxy -d -c /etc/tinyproxy/tinyproxy.conf
