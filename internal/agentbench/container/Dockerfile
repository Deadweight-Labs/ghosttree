FROM debian:trixie-slim

# procps und iproute2 sind fuer den Agenten unnoetig, aber die Sonde braucht
# ps und ss. Ohne sie meldet sie keine Prozesse und keine Ports — und das
# liest sich wie ein sauberer Befund, statt wie eine Pruefung, die nie lief.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates git curl jq nodejs npm procps iproute2 \
    && rm -rf /var/lib/apt/lists/*

# Die Agenten-CLI liegt unter /opt/arm/common und ist damit fuer jeden Arm
# sichtbar — sie ist der Gegenstand der Messung, nicht eine der Variablen.
# Die Version wird festgeschrieben, weil dieselbe Modellfamilie je nach
# Harness unterschiedlich abschneidet.
ARG CLAUDE_CODE_VERSION=latest
RUN npm install -g --prefix /opt/arm/common "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}"

# ctx dagegen liegt in einem eigenen Verzeichnis, das der entrypoint nur dem
# ghosttree-Arm in den PATH legt. Laege es unter common, koennte der
# bare-Arm es finden und benutzen.
COPY --chmod=0755 bin/ctx       /opt/arm/ghosttree/bin/ctx
COPY --chmod=0755 entrypoint.sh /usr/local/bin/entrypoint.sh

ENV DISABLE_AUTOUPDATER=1 \
    DISABLE_TELEMETRY=1 \
    DISABLE_ERROR_REPORTING=1 \
    DISABLE_BUG_COMMAND=1 \
    npm_config_update_notifier=false

WORKDIR /work
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
