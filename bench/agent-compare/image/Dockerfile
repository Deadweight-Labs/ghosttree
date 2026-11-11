FROM golang:1.27.3-bookworm
ARG TARGETARCH
# socat: macht aus den Unix-Sockets des Hosts 127.0.0.1-Ports im Netz der Sandbox (sandbox-run.sh).
# python3: die Isolationsprobe (probe.py). tini übernimmt `docker run --init`.
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      socat ripgrep jq python3 nodejs less procps tini git \
    && rm -rf /var/lib/apt/lists/*
# Die Version von Claude Code ist festgelegt: build.sh legt die gepinnte Binärdatei in den Kontext.
COPY claude-${TARGETARCH} /usr/local/bin/claude
RUN chmod 755 /usr/local/bin/claude
# vorgewärmter Build-Cache der Standardbibliothek (ledgerly nutzt nur stdlib); build.sh kopiert ihn je Kette nach
# /var/cache/gocache, weil der Cache schreibbar sein muss und nie zwischen Ketten geteilt wird.
ENV GOTOOLCHAIN=local GOFLAGS=-buildvcs=false
RUN mkdir -p /opt/gowarm/src && cd /opt/gowarm/src && printf 'module warm\n\ngo 1.24\n' > go.mod && \
    printf 'package main\nimport (\n_ "net/http"\n_ "net/http/httptest"\n_ "encoding/json"\n_ "encoding/csv"\n_ "log/slog"\n_ "testing"\n_ "crypto/rand"\n_ "regexp"\n_ "time"\n_ "sort"\n_ "database/sql"\n)\nfunc main(){}\n' > main.go && \
    GOCACHE=/opt/gowarm/cache go build ./... && GOCACHE=/opt/gowarm/cache go test -run X std/net/http/httptest >/dev/null 2>&1; \
    GOCACHE=/opt/gowarm/cache go vet ./... ; chmod -R a+rX /opt/gowarm
# Kein ENTRYPOINT: der Harness startet den Container mit `sleep infinity` und führt jede Sitzung per `exec` aus,
# als Benutzer des Hosts, mit leerer Umgebung (env -i) und nur lesbarer Wurzel.
