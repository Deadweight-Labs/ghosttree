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

The sample unit also sets every snapshot resource limit to a finite default.
`server.env` may lower or raise these values for a measured deployment, but no
value may be zero or negative:

| Environment variable | Serve flag | Default |
| --- | --- | ---: |
| `GHOSTTREE_SNAPSHOT_MAX_ENTRY_BYTES` | `--snapshot-max-entry-bytes` | 4,194,304 |
| `GHOSTTREE_SNAPSHOT_MAX_ENTRIES` | `--snapshot-max-entries` | 20,000 |
| `GHOSTTREE_SNAPSHOT_MAX_PAYLOAD_BYTES` | `--snapshot-max-payload-bytes` | 134,217,728 |
| `GHOSTTREE_SNAPSHOT_MAX_HEAD_BYTES` | `--snapshot-max-head-bytes` | 32,768 |
| `GHOSTTREE_SNAPSHOT_MAX_LOGICAL_BYTES` | `--snapshot-max-logical-bytes` | 167,772,160 |
| `GHOSTTREE_SNAPSHOT_MAX_PROJECT_COUNT` | `--snapshot-max-project-count` | 1,000 |
| `GHOSTTREE_SNAPSHOT_MAX_PROJECT_BYTES` | `--snapshot-max-project-bytes` | 8,589,934,592 |
| `GHOSTTREE_SNAPSHOT_MAX_STORE_COUNT` | `--snapshot-max-store-count` | 10,000 |
| `GHOSTTREE_SNAPSHOT_MAX_STORE_BYTES` | `--snapshot-max-store-bytes` | 68,719,476,736 |

Counts stop attacks made from many empty snapshots; logical-byte limits include
canonical head metadata, domains, keys, digests, and payloads. Values exactly
at a limit are accepted. Capacity planning must also leave space for SQLite
indexes, WAL files, and backups because those are intentionally not part of the
portable logical-byte calculation.

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

## Context snapshots

Snapshot rows are immutable and have no ordinary deletion or redaction path.
Before enabling creates, verify backup and restore procedures, choose finite
budgets based on measured project sizes, and grant only the required project
capabilities:

```bash
sudo systemctl stop ghosttree
sudo systemd-run --pipe --wait --collect --quiet \
  -p DynamicUser=yes -p User=ghosttree -p StateDirectory=ghosttree \
  /usr/local/bin/ctx person snapshot-access <person-name> \
    --project github.com/owner/repository --read --create \
    --db /var/lib/ghosttree/ghosttree.db
sudo systemctl start ghosttree
```

Add `--release-bind` only when that identity must create SemVer release marks.
Use the read-only `snapshot-access show` form to confirm the stored tuple.

The server can rebuild repository-local snapshot indexes only for explicitly
mapped roots. Every mapping is repeatable, canonicalized by project, and must
name an existing absolute real directory rather than a symlink:

```text
--snapshot-root github.com/owner/one=/srv/projects/one
--snapshot-root github.com/owner/two=/srv/projects/two
```

For the sample systemd unit, add a drop-in that clears and restates `ExecStart`
with the unit's existing database, listen, and nine finite limit arguments,
then append the required `--snapshot-root` arguments. Run
`systemctl daemon-reload` and inspect `systemctl show ghosttree.service
--property=ExecStart` before restarting. The service identity must be able to
write `.ghosttree/snapshots/` in every mapped repository.

A mirror failure does not roll back an already sealed database snapshot. The
client reports `snapshot_mirror_degraded`; repair it from that repository with:

```bash
ctx snapshot mirror rebuild
```

Retry `snapshot_store_busy` and the explicitly retryable
`snapshot_git_changed` only after re-reading the current state. A
`snapshot_storage_exhausted`/HTTP 507 response means the SQLite or filesystem
store is full; free or provision space and verify the database before retrying.
Limit errors require choosing a smaller durable context or deliberately
changing a finite deployment budget, never disabling the budget.

### Existing database rollout

Back up the SQLite database and its WAL state before installing a
snapshot-capable binary. On first open Ghosttree adds snapshot tables, indexes,
checks, and immutability triggers; it does not rewrite live Knowledge, Ghost,
document, request, or session rows and performs no historical backfill. The
server refuses startup if trigger definitions are stale, a committed
`building` snapshot exists, or the rollback-only invariant probe fails. Keep
the old binary and verified backup available until startup, health, snapshot
create, export, and verify have all succeeded.

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
