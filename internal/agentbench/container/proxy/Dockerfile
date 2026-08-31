FROM debian:trixie-slim

# curl gehoert dazu, obwohl der Proxy es nicht braucht: die Wartesschleife
# beim Hochfahren prueft mit ihm den vollstaendigen Pfad durch den Proxy statt
# nur einen offenen Port.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl tinyproxy \
    && rm -rf /var/lib/apt/lists/*

COPY tinyproxy.conf            /etc/tinyproxy/tinyproxy.conf
COPY --chmod=0755 entrypoint.sh /usr/local/bin/entrypoint.sh

EXPOSE 3128
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
