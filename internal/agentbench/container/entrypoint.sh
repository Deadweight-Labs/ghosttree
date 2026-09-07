#!/usr/bin/env bash
# Jeder Arm bekommt nur die Werkzeugverzeichnisse, auf die er Anspruch hat.
# Laege alles im PATH, koennte der bare-Arm ctx oder claude-mem entdecken und
# benutzen — und dann misst der Vergleich nicht mehr, was er behauptet.
set -euo pipefail

: "${AGENTBENCH_ARM:?AGENTBENCH_ARM is required}"

case "$AGENTBENCH_ARM" in
  bare|claudemd|claude-native|oracle) TOOLS="" ;;
  ghosttree)                          TOOLS="/opt/arm/ghosttree/bin" ;;
  claude-mem)                         TOOLS="/opt/arm/claude-mem/bin" ;;
  agentmemory)                        TOOLS="/opt/arm/agentmemory/bin" ;;
  *) echo "unknown arm: $AGENTBENCH_ARM" >&2; exit 2 ;;
esac

export PATH="/opt/arm/common/bin:/usr/bin:/bin${TOOLS:+:$TOOLS}"
export HOME="/work/home"
export XDG_CONFIG_HOME="/work/home/.config"
mkdir -p "$HOME" "$XDG_CONFIG_HOME"

# Der bare-Arm darf auch keine global installierten Agent-Regeln erben.
if [ "$AGENTBENCH_ARM" = "bare" ]; then
  rm -rf "$HOME/.claude" "$HOME/.codex"
fi

exec "$@"
