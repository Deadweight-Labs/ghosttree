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

Behind a TLS terminator add the proxy settings to the same file (the unit
passes the environment to `ctx serve`); see "Running behind TLS or a reverse
proxy" in the main README:

```text
GHOSTTREE_PUBLIC_URL=https://ghosttree.example.com
GHOSTTREE_TRUSTED_PROXIES=<proxy-address>
```

To let organization owners connect a GitHub organization ("Connect GitHub" on the
Organization page, the proof behind remote prefixes), register a GitHub OAuth app
with the callback URL `<server address>/ui/orgs/source/callback` and give the
server its credentials. The flow asks for the scope `read:org`; the user's access
token is never stored. Without these variables the page shows that GitHub is not
set up:

```text
GHOSTTREE_GITHUB_CLIENT_ID=<client id of the app>
GHOSTTREE_GITHUB_CLIENT_SECRET_FILE=/etc/ghosttree/github-client-secret
```

`GHOSTTREE_GITHUB_CLIENT_SECRET` holds the secret directly instead of a file.

Use the server's address on the trusted private network, such as NetBird, in
the listen setting, client configuration and monitoring target. After changing
the client URL, restart the collector and reconnect running MCP servers: they
read the configuration when the process starts. Verify health from a client on
that network; a probe of the server's own VPN address can be subject to different
network policies.

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

### Runtime writer

File-backed runtime stores admit domain writes into one bounded FIFO and use a
separate read-only pool. Active operations count against both limits. A write
succeeds only after its commit. Only adjacent pending chunk operations share a
transaction, with no gathering delay.

| Environment variable | Serve flag | Default |
| --- | --- | ---: |
| `GHOSTTREE_WRITER_MAX_OPERATIONS` | `--writer-max-operations` | 1,024 |
| `GHOSTTREE_WRITER_MAX_BYTES` | `--writer-max-bytes` | 268,435,456 |
| `GHOSTTREE_WRITER_MAX_BATCH` | `--writer-max-batch` | 64 |
| `GHOSTTREE_WRITER_READ_CONNECTIONS` | `--writer-read-connections` | 3 |

These values must be positive. The byte limit must be at least the snapshot
logical-byte limit. Snapshot creation also reserves bounded capture scratch
space; raising snapshot limits may require raising the writer byte budget.
The byte budget covers owned inputs and reserved capture space, not total RSS.

Saturation returns HTTP 503, `writer_busy`, `retryable: true` and
`Retry-After: 1`. Closed admission returns `writer_closed`. Snapshot endpoints
preserve `snapshot_store_busy`. Clients expose retry metadata without
replaying writes automatically; resolve ambiguous outcomes using the operation's
existing sequence, revision or idempotency identity before retrying.

SIGINT and SIGTERM stop new HTTP connections and allow active handlers up to
five minutes to finish, then drain accepted writer work before closing SQLite.
The sample unit allows 330 seconds in total. If this deadline is exceeded,
systemd can terminate the process; unfinished operations have no success ACK.
The writer makes no new schema or file-format change, so a rollback restores
the saved binary and service configuration without a database migration.

### Audit logs and metrics

Every non-probe API request emits one structured JSON `http_request` event to
stderr, which systemd captures in journald for collection by Alloy/Loki. The
event includes the actor, request ID, method, matched route, concrete path,
remote IP, status, duration, byte counts, and a bounded error class. It never
includes request or response bodies, query parameters, bearer tokens, or
authorization headers. `/api/health` and `/metrics` are excluded from audit
events.

Prometheus metrics are available at `/metrics` without authentication so that
vmagent can scrape them. This endpoint has the same network exposure as the
server itself and must therefore remain restricted to a trusted private network.
HTTP metric labels are deliberately bounded to method,
matched route, status, and error class; actor, request ID, remote IP, concrete
path, project, and slug are never labels.

`ghosttree_writer_*` exposes queue operations and reserved bytes, high-water
marks, admission/completion/rejection counts, per-kind bookkeeping drops, fixed
histograms for queue wait, domain execution through commit or rollback, and
chunk batch size, plus drain and worker state. Commit duration includes the
whole domain operation, not just the SQLite commit call; best-effort accounting
transactions are excluded. `ghosttree_reader_*` reports read-pool waits and
connections. Metrics keep fixed labels and buckets without retaining requests.

Add a scrape job to vmagent using the same private address. The monitoring host
needs network access to the server's TCP port 8474:

```yaml
  - job_name: ghosttree
    metrics_path: /metrics
    static_configs:
      - targets: ['<private-address>:8474']
        labels: {site: home, host: apps}
```

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
exclusive access (`ctx db vacuum`). Keep and verify the backup printed by
`ctx upgrade-schema` before restarting the server.

### Images in stored transcripts

Screenshots and attached files arrive in session transcripts as base64 and are
the largest part of the database, although search and the session view only use
text. The server therefore replaces every base64 payload of 1 KB or more in a
stored line with a short placeholder (type, size, first 16 hex digits of the
SHA-256), and `ctx watch` does the same before uploading. Chunks stored by an
older version still carry their images. `ctx strip-images` replaces those; it is
safe to run while the server is up (short transactions, 30 seconds of waiting for
a lock, `--sleep` between steps) and can be stopped with Ctrl-C and run again:

```bash
DB=/var/lib/ghosttree/ghosttree.db
RUN='sudo systemd-run --pipe --wait --collect --quiet -p DynamicUser=yes -p User=ghosttree -p StateDirectory=ghosttree /usr/local/bin/ctx'
$RUN strip-images --db $DB --dry-run   # counts chunks and bytes, writes nothing
$RUN strip-images --db $DB             # replaces; a second run changes nothing
```

The image data cannot be recovered afterwards, so keep a backup if it might be
needed. The report lists the chunks checked and replaced and the bytes saved.
The database file does not get smaller by this: the freed pages are reused by
new data, which already stops the growth. To give the space back to the
filesystem, run `ctx db vacuum` once with the service stopped, as described in
the next section.

### Shrinking and checking the database

Deleting rows (`ctx strip-images`, a cleanup after a redaction) leaves free pages
inside the file; it only shrinks when `VACUUM` rewrites it. Run `ctx db vacuum`
after a large deletion, or when the file is far bigger than its content: the
plan it prints names the share of free pages and the size to expect. Use `ctx db
vacuum` and `ctx db check`, not the `sqlite3` CLI: the CHECK constraints of the
snapshot tables call the SQL function `ghosttree_valid_utf8`, which only `ctx`
registers, so `sqlite3 <db> VACUUM` and `sqlite3 <db> 'PRAGMA quick_check'` stop
with `no such function`.

`ctx db vacuum` needs the service stopped. It refuses, and changes nothing, if
another process has the database open (it names the process; on Linux it looks
into `/proc`, which shows processes of other users only to root), if another
connection holds the write lock, or if there is not enough free disk space.
That space is about the size of the live data (the file minus its free pages,
plus a tenth and 64 MiB) twice: once for the temporary copy that `VACUUM` writes
into `--tmpdir` (default: the directory of the database; SQLite alone would use
`/var/tmp`), once for the new WAL beside the database. On one filesystem both
must fit together. It checkpoints the WAL, runs `VACUUM`, checkpoints again and
finishes with `quick_check`; the exit status is not 0 unless that says `ok`. On
a 10.9 GB database the vacuum took about ten minutes and left 7.0 GB. It asks
before it starts; there is no terminal in a transient unit, so pass `--yes`
there:

```bash
sudo systemctl stop ghosttree
sudo systemd-run --pipe --wait --collect --quiet \
  -p DynamicUser=yes -p User=ghosttree -p StateDirectory=ghosttree \
  /usr/local/bin/ctx db vacuum --db /var/lib/ghosttree/ghosttree.db --yes
sudo systemctl start ghosttree
```

`--fts-optimize` first runs `optimize` on every full-text table (`chunks_fts`,
`sess_fts`, `knowledge_fts`, ...). After rows are deleted or redacted, their
tokens stay in the segments of the full-text index until it is optimized, and
`VACUUM` does not touch them; use the option after a bulk deletion or redaction.

`ctx db check` runs `quick_check` (`--full`: `integrity_check`, slower, also
compares the indexes with their tables) and prints `ok` or the first findings.
It writes nothing, so it can run while the service is up. Run it through the
same transient unit, not as another user: SQLite skips the CHECK constraints on
a file the user cannot write (the command says so), and `-wal` or `-shm` files
created by another user can lock the service out.

```bash
sudo systemd-run --pipe --wait --collect --quiet \
  -p DynamicUser=yes -p User=ghosttree -p StateDirectory=ghosttree \
  /usr/local/bin/ctx db check --db /var/lib/ghosttree/ghosttree.db
```

## Context snapshots

Snapshot rows are immutable and have no ordinary deletion or redaction path.
Authenticated identities default to read and create access when no explicit
project access row exists. Release binding defaults to denied. Verify backup
and restore procedures and choose finite budgets based on measured project
sizes. Set an explicit row to override the default for a person and project:

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
To revoke all access, run the write form with no `--read`, `--create`, or
`--release-bind` flags; the explicit denial takes precedence over the default.

The server can rebuild repository-local snapshot indexes only for explicitly
mapped roots. Every mapping is repeatable, canonicalized by project, and must
name an existing absolute real directory rather than a symlink:

```text
--snapshot-root github.com/owner/one=/srv/projects/one
--snapshot-root github.com/owner/two=/srv/projects/two
```

For the sample systemd unit, add a drop-in that clears and restates `ExecStart`
with the unit's existing database, listen, snapshot and writer limit arguments,
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

Snapshots created by earlier development binaries using payload schema 1 or 2
are preserved and readable. The database invariant upgrade may add derived
logical-size metadata after verifying the historical entry digest; it does not
change the sealed snapshot's identity, payload, creation time, or digest.
The current binary creates schema 3 and reports `HeadBound: false` when
verifying older snapshots because their digests did not bind provenance.
Do not recreate or rehash historical snapshots to make them appear to carry
the newer guarantee. Unsupported future schemas and actual corruption remain
errors, rather than being silently omitted from listings.

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

## Gardener in the sandbox

`ghosttree-garden.service` runs the gardener (`ctx garden check-knowledge`) as a
Claude Code agent. Its entries are data an attacker may have written, so the
session is treated as untrusted:

- **No Bash.** The session gets Read, Grep and Glob (only below the checkout)
  and two fixed git tools, `git_log` and `git_show`, served by
  `ctx garden git-mcp`. Their arguments are a hash or `HEAD`, a plain relative
  path, a number and a text for the pickaxe; nothing is ever parsed as an option.
- **Own config directory.** Every session gets an empty directory under
  `--claude-dir` (also `GHOSTTREE_GARDEN_CLAUDE_DIR`), which is its
  `CLAUDE_CONFIG_DIR` and `HOME`, and is removed afterwards. Without
  `--claude-dir` the agent mode does not start. The proxy and the token come
  from the `garden` entry of `llm.json`, not from a settings file.
- **Small environment.** The session sees `PATH`, locale, certificate paths and
  the one value that assigns it to its account. Nothing else of the unit's
  environment reaches it.
- **Neutral git.** `ctx` runs its own git calls with `core.hooksPath=/dev/null`,
  no fsmonitor, no system config and an empty global config, and compares not
  only `git status --ignored` but also `.git/config`, `.git/hooks`, `.git/info`
  and `HEAD` before and after each session.
- **systemd sandbox.** The unit runs as `ghosttree-garden` (create it with
  `systemd-sysusers deploy/ghosttree-garden.sysusers`), hides everything under
  `/var/lib/ghosttree` and binds back only the checkout (read-only), `bin/`,
  `run.sh`, and the writable `state/`, `reports/` and `claude/`. `/home`, `/root`,
  the database, private data and backups are not visible. The gardener cannot
  open the database from there, so it keeps its memory in
  `garden/state/state.json` (`--state-file`) instead of `--db`.

The ghosttree MCP server of a session (`ctx mcp`, for `context_search` and
`context_get`) reads its client config from `$HOME/.config/ghosttree/config.json`
with the `HOME` of the unit, so with the example unit from
`/var/lib/ghosttree/garden/claude/.config/ghosttree/config.json` (`server_url`
and the token of the gardener). The old `garden/home/.config/ghosttree` is not
visible in the sandbox any more; move the file. The probe checks that it is
readable (`CLIENT_CONFIG` for another place).

`context_search` and `context_get` cannot be limited to one project by an
allowlist, so a gardener that must not read other projects' entries runs with
`--no-mcp`: the session then gets no ghosttree server (and needs no client
config), is not allowed those two tools and is not told about them. The fixed
git tools `git_log` and `git_show` stay either way.

### What the gardener writes, and what only people do

The gardener proposes, a person decides. With `--propose` it makes exactly these
calls to the server besides reading, and the account of its client config
(`garden/claude/.config/ghosttree/config.json`) needs nothing more than the role
**member** in the project, not lead and not reviewer:

- **Staged notes** with harness `gardener`, never trusted, never replacing
  anything: stale and correction verdicts, duplicates between entries, and
  answers to the regression question for pitfalls nobody has judged (a test that
  exists, a sketch of a missing test, or "not testable"). The answers are checked
  against the checkout first: a named test has to be defined in the file the
  session read.
- **One thread per contradiction**, linked to every entry it is about, titled as a
  question, opened by the gardener's account with the reason and the verbatim
  quotes as its first post. For the same entries there is never a second open
  thread, and a thread a person closed stays closed while the entries are
  unchanged (the thread's question records which wording it was about); once an
  entry changes, a new thread may ask again, and the old one is left as it is.
  To read and open threads the gardener registers as the agent `gardener:<project>`
  in the project's room. If a thread cannot be opened the finding becomes a staged
  note.

Everything that changes an entry is a button on the review page (`/ui/review`)
that a person presses, and the server asks that person for the right the same
step needs elsewhere: **Merge** on a duplicate note supersedes the older entry by
the newer one (right to change every entry involved; the reason is "Gärtner-Vorschlag"
with the number of the note and the name of the person), **Record as covered** and
**Record as not testable** on a regression note set the regression verdict of the
pitfall (right to verify). The gardener has no call for any of this, and its own
account, a plain member, is refused by the server if it tried.

**Set the gardener's account on the server.** Only one account may write entries
with harness `gardener` or a session reference starting with `gardener:`, and the
review page acts only on notes it wrote: the account whose token is in the
gardener's client config. Say which one on the server host, once:

```bash
sudo systemd-run --pipe --wait --collect --quiet \
  -p DynamicUser=yes -p User=ghosttree -p StateDirectory=ghosttree \
  /usr/local/bin/ctx account set-gardener <account name> --db /var/lib/ghosttree/ghosttree.db
```

(The identity is the one of "Running maintenance commands" above; run it as the
user that owns the database if yours differs.) The command looks the account up
once and stores its **number** in the database (`org_state`, no schema change); the
running server uses it at once, without a restart. `ctx account list` marks it with
`gardener=true`, `ctx account set-gardener --clear` takes the role away, and
`ctx serve` logs `gardener_account` at start with the account and whether one is
set. The number, not the name, is what counts: renaming the account changes
nothing, and an account that takes its old name gets nothing. Nothing that is
reachable over HTTP sets it. Until one is set nobody may write gardener entries:
the API answers 403 `gardener_reserved`, which is also what the gardener's
proposals get on a server that has not been set up, and the review page offers no
action on a gardener note.

The review page acts on a note only if that account wrote it (the account number
stored with the note, not a label), nobody has edited it since, and the entries its
title names are the ones its session reference names; the card shows the entries of
the reference, with title and link. The test a regression note names is checked for
its shape (a relative path, a colon, a function name), shown whole on the card and
recorded as the note holds it. The server has no checkout, so it cannot look the
test up; `ctx` did that before it filed the note, and the account and the unedited
note are what vouch for it. The server does not force `staged` on the gardener's
own account; the command does, and a test records every call a gardener run makes
against a real server.

Layout under `/var/lib/ghosttree/garden`: `repo/` (checkout, owned by root,
updated by `ghosttree-garden-update.service`), `bin/` (the `claude` binary, pass
`--claude`), `run.sh`, `state/`, `reports/`, `claude/` (writable by
`ghosttree-garden`). Settings of the run (key paths, `GHOSTTREE_GARDEN_CLAUDE_DIR`,
`--state-file`) belong in `/etc/ghosttree/garden.env` and `run.sh`, not in
`server.env`.

After installing or changing the unit, prove the sandbox on the machine:

```sh
sudo deploy/garden-sandbox-probe.sh
```

It starts a transient unit with the settings read from the installed unit file
and tries to read the data directory, database, private data, backups, `/home`,
`/root` and the server environment, to write the checkout and `.git/config`, and
to reach a non-loopback address. Every one of those has to fail, reading the
checkout has to work; otherwise the exit status is 1. `--print` shows the
`systemd-run` command line without running it.

## Client machines

```bash
install -m755 dist/ctx-linux-amd64 ~/.local/bin/ctx
ctx setup --server http://<private-host>:8474 --token <token>
ctx install claude
ctx install codex
ctx install opencode
ctx watch --once
ctx install watch
ctx status
ctx doctor
```

`ctx install claude` and `ctx install codex` set up the collector as well (a
systemd user unit on Linux, a LaunchAgent on macOS, both with the absolute path
of `ctx`); `--no-watch` skips it and `ctx install watch` sets up only the
collector. `deploy/ghosttree-watch.service` is the same unit for manual setups.

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

## "In short" notes (batch)

`ghosttree-explain.service` runs `ctx explain backfill --batch`: it collects
finished Message Batches, then submits one batch for every object that has no
current note. `ghosttree-explain.timer` runs it hourly, which is cheap while a
batch is open (nothing is sent twice) and picks results up within the hour.
`ghosttree-explain.path` starts a first run when the key file is written, and
`ConditionPathExists=` keeps the service from doing anything while it is
missing. The key belongs to the `explain` entry of `llm.json` (see the main
README); the sample uses a systemd credential, so write
`{"explain": {"credential": "explain-key"}}` there.

```sh
install -m 600 /dev/null /etc/ghosttree/explain-key   # then paste the key into it
systemctl enable --now ghosttree-explain.timer ghosttree-explain.path
```
