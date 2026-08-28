# Deployment

These units are starting points for a small private Linux deployment. Read and
adapt them before installation; Ghosttree does not know which interface,
reverse proxy, backup target, or model provider your environment uses.

## Server

`ghosttree.service` uses `DynamicUser=yes` and
`StateDirectory=ghosttree`. Systemd owns `/var/lib/ghosttree` and exposes the
real state directory to the service with the correct identity.

The unit binds to `127.0.0.1:8474` by default. To use another trusted interface,
create `/etc/ghosttree/server.env` before starting it:

```text
GHOSTTREE_LISTEN=<private-address>:8474
```

Do not bind directly to a public interface. Use a private network or a TLS
reverse proxy with suitable access controls.

```bash
make build-all
scp dist/ctx-linux-amd64 deploy/ghosttree.service <host>:/tmp/
ssh <host> 'sudo install -m755 /tmp/ctx-linux-amd64 /usr/local/bin/ctx
            sudo install -m644 /tmp/ghosttree.service /etc/systemd/system/ghosttree.service
            sudo systemctl daemon-reload'
```

Create the first person token before the first start:

```bash
ssh <host> 'sudo /usr/local/bin/ctx person add <name> --db /var/lib/ghosttree/ghosttree.db'
ssh <host> 'sudo systemctl enable --now ghosttree'
curl -fsS http://<private-host>:8474/api/health
```

`person add` prints the token once. A root-owned write into the state directory
while the service is running can leave SQLite WAL files that the dynamic user
cannot write. Stop the service before later server-side maintenance.

### Running maintenance commands

Do not rely on `sudo -u ghosttree`: a dynamic user may not exist outside the
running unit, and `/var/lib/private` is root-only. Recreate the service identity
with a transient unit:

```bash
sudo systemd-run --pipe --wait --collect --quiet \
  -p DynamicUser=yes -p User=ghosttree -p StateDirectory=ghosttree \
  /usr/local/bin/ctx <command> --db /var/lib/ghosttree/ghosttree.db
```

Stop `ghosttree.service` first for schema upgrades or commands that require
exclusive access. Keep and verify the backup printed by `ctx upgrade-schema`
before restarting the server.

## Session distillation

Distillation is a separate timer because it calls a model provider and may cost
money. Configure it only after ordinary collection and retrieval work.

The sample unit reads provider configuration from
`/etc/ghosttree/llm.json` and an API key from a systemd credential. Replace the
placeholders with a provider, endpoint, and model you have chosen:

```json
{
  "format": "openai",
  "base_url": "<provider-base-url>",
  "model": "<model-name>",
  "credential": "llm-key",
  "api_key_file": "/etc/ghosttree/llm-key"
}
```

Install the files with restrictive permissions, then use a dry run to inspect
the expected work and cost before enabling the timer.

```bash
sudo install -d -m755 /etc/ghosttree
sudo install -m640 -o root -g ghosttree <key-file> /etc/ghosttree/llm-key
sudo install -m644 deploy/ghosttree-distill.service deploy/ghosttree-distill.timer /etc/systemd/system/
sudo systemctl daemon-reload

sudo systemd-run --pipe --wait --collect --quiet \
  -p DynamicUser=yes -p User=ghosttree -p StateDirectory=ghosttree \
  -p LoadCredential=llm-key:/etc/ghosttree/llm-key \
  -p Environment=GHOSTTREE_LLM_CONFIG=/etc/ghosttree/llm.json \
  /usr/local/bin/ctx distill-sessions --db /var/lib/ghosttree/ghosttree.db --dry-run

sudo systemctl enable --now ghosttree-distill.timer
```

Generated knowledge remains quarantined until review. The bill does not, so
start with a small limit and inspect the results.

## Client machines

```bash
install -m755 dist/ctx-linux-amd64 ~/.local/bin/ctx
ctx setup --server http://<private-host>:8474 --token <token>
ctx install claude
ctx install codex
ctx install opencode
ctx watch --once
cp deploy/ghosttree-watch.service ~/.config/systemd/user/
systemctl --user enable --now ghosttree-watch
ctx status
ctx doctor
```

Installer and doctor operations can be limited to specific components:

```bash
ctx install codex --only hooks
ctx install claude --only mcp --only skills
ctx doctor codex --only mcp
ctx doctor claude --only hooks --fix
```

`UNVERIFIED` means the static configuration or synthetic probe passed, but a
real harness event has not yet supplied runtime evidence.

## Moving an existing local server

A running SQLite database in WAL mode is more than its main file. Stop the
local service before copying, or create an online SQLite backup:

```bash
systemctl --user stop ghosttree
cp ~/.local/share/ghosttree/ghosttree.db /tmp/ghosttree.db

# Alternatively, while it is running:
sqlite3 ~/.local/share/ghosttree/ghosttree.db ".backup '/tmp/ghosttree.db'"
```

Verify the copied database with `PRAGMA integrity_check`, put it in the server's
state directory while the service is stopped, and then point each client at the
new private URL with `ctx setup`.
