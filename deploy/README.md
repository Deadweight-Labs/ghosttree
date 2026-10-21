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

### Passwords in stored transcripts

Tokens with a fixed prefix (`ghp_`, `sk-ant-`, `AKIA`, JWT, private keys) have always
been replaced. Since REQ-526 the server and `ctx watch` also replace the value behind a
label such as `Passwort:`, `password=`, `api_key:`, `token`, `PIN` or `Zugangsdaten`, and the
password in `https://user:password@host`, with `[REDACTED:labeled]` or `[REDACTED:url]`;
the label stays. Placeholders (`${DB_PASSWORD}`), code (`r.FormValue("password")`), paths
and prose ("Passwort zurücksetzen") are left alone. New chunks are covered when they are
stored, also those from older clients. `ctx redact-sessions` does the same for the
chunks already stored, and for every copy of their text (`session_chunks`, `chunks_fts`,
`chunk_index`, `sess_fts`, session titles, evidence quotes). It is safe to run while the
server is up and can be stopped with Ctrl-C and run again:

```bash
DB=/var/lib/ghosttree/ghosttree.db
RUN='sudo systemd-run --pipe --wait --collect --quiet -p DynamicUser=yes -p User=ghosttree -p StateDirectory=ghosttree /usr/local/bin/ctx'
$RUN redact-sessions --db $DB --dry-run   # counts per table and kind, writes nothing, prints no value
$RUN redact-sessions --db $DB             # replaces; a second run changes nothing
```

Take a backup first if the text might be needed. The report lists the chunks checked and
changed, the rows cleaned per table, the replacements per kind, and the knowledge entries
and requests whose evidence came from a changed chunk (their own text may hold a copy:
look at them). Deleting a row from an FTS5 index only marks it as deleted; the words
stay in the index segments until the segments are merged. The run therefore merges both
indexes (`optimize`) at the end, which rewrites them in one statement and holds the write
lock while it does: on a large database that can take minutes, so start it when little is
written. `--skip-optimize` leaves that out. The run also overwrites what it frees
(`secure_delete`) and truncates the write-ahead log.

Three things it cannot reach. Pages that were already free before the run, from earlier
updates, may hold old text until the file is rewritten: run `ctx db vacuum` once afterwards
(see above, with the service stopped). Backups and copies
made before the run keep the old text until they are replaced. Context snapshots are
immutable and have no redaction path. After the run, check with a search: a part of the
password in the search box must find nothing.

What is not redacted when written: knowledge entries, requests, coordination messages and
notes. Human tasks, explanations and work notes are refused when they look like a secret;
an agent that copies a password into a knowledge entry stores it.

#### A known value without a label

A password that has leaked and is known often stands there without any label: in a command
line (`sed 's/<value>/…/'`), in the output of `grep`, or as single fragments (a value made of
parts joined by symbols is indexed by the full-text search as one word per part). The rule by
label does not find those. Name the value to `ctx redact-sessions` in a
file, one value per line (blank lines and lines that start with `#` are ignored; a value that
itself starts with `#` is written `\#`):

```bash
umask 077
$EDITOR ~/known-values.txt        # the full value and each part of it, four characters or more
ctx redact-sessions --db $DB --dry-run --literals-file ~/known-values.txt
ctx redact-sessions --db $DB --literals-file ~/known-values.txt
shred -u ~/known-values.txt
```

Run it as the user that owns the database, with the service up or down. The file has to be
readable by that user. The transient `DynamicUser=yes` unit used above is another user on
every start and cannot read your file; give it the file with systemd's `LoadCredential=` (the
unit then finds it under `$CREDENTIALS_DIRECTORY`) instead of loosening the permissions.

Every occurrence of every value becomes `[REDACTED:literal]`, with or without a label, in the
same places as above (`text`, `raw`, `chunks_fts`, `chunk_index`, `sess_fts`, titles, evidence
quotes, also the quotes whose chunk is gone). In `raw` the value is found as it is written
there, also as a JSON escape (`\u0026` for `&`, `\"` for a quote), and `raw` stays valid JSON.
A value matches in the spelling you give and in lower case, because the full-text columns do
not tell the cases apart and fragments often turn up in lower case; give another spelling as
a line of its own. Without `--literals-file` none of this happens.

The file is a secret itself. The command refuses it (exit 2) unless group and others have no
access to it (`chmod 600`), and refuses values shorter than four characters (exit 2), so that
a typo cannot empty the database. A value never appears in the output or in an error message
(they name lines and counts) and is never passed to SQL.

It runs in one pass over `session_chunks` together with the label rule, so it costs only a
little more than a run without the file (on 200,000 made-up chunks, 1.6 GB: a dry run 13.6 s
with the values against 11.6 s without; the real run with 800 changed chunks 33 s, including
`optimize`). It leaves knowledge entries, their versions and requests alone: they have
history, search copies and immutable snapshots, which a command cannot clean as a whole. It
counts the entries that hold a value and names their numbers; deciding what to do with them
is yours. After the run, run `ctx db vacuum` (the service stopped), and check with a search
for a part of the value.

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
- **Staged notes for the ledger** (job 4): "criterion `K` of request `#N` is met",
  with a proof: a commit (it exists, is in the history of `HEAD` and a git tool
  printed it to the session; squash commits carry the pull request number
  `(#149)`), a test (`path/to/x_test.go:TestName`, defined in a file the session
  read) or a file. A proposal without a valid proof is not made.
- **Staged notes for descriptions** (job 1): a proposed ghost description for a
  file whose description is stale or missing, with the content hashes of the file
  as the session read it (a file that changed meanwhile is dropped) and the hash
  of the description it would replace.
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
pitfall (right to verify). **Record criterion as met** on a ledger note records
the proof of the note as the evidence of the criterion, with the call and the
right of `request_record_progress criterion_met` (work on the request), and the
right to verify the note; the evidence ends with "Gärtner-Vorschlag #note,
bestätigt von person". It never closes a request. **Adopt description** on a
description note writes the text with the call of `context_describe_file`
(right to describe in the project, and to verify the note), after showing the
old and the new description side by side; the ghost then carries harness
`gardener`, the person who confirmed it, and the tree says "vom Gärtner
vorgeschlagen, bestätigt von person". It is refused (409) when another
description stands at the path than the one the note was written against.
The gardener has no call for any of this. The server refuses its own account
the steps that need more than membership (merge, verdict, thread state). Recording
a criterion and writing a description are plain member rights, though, so for
these two the guarantee is the program's own: `ctx garden` has no such call, a test
records every call a run makes and allows none, and the server refuses
the mark "gardener" on a description to everyone, the gardener's account included
(`POST /api/ghosts` answers 403 `gardener_reserved`): only the review page sets it.

What the new buttons act on is the server's own data, never a value the browser
sends: the request, the criterion or the path come from the reference of a note the
gardener's account wrote and nobody has edited, the title has to name the same,
the proof and the text are the ones in the note (held to a strict shape, since the
server has no checkout), and the form carries nothing but its token.

### Ledger, descriptions and idle requests

Two further jobs run in sessions of their own, before the knowledge sessions, and
share the budget of `--max-usd` with them; a failure of theirs never stops the
knowledge job (after two failed extra sessions the other extra sessions are
skipped).

- `--requests N` (default 8, 0 = off, at most 40): the open requests that are due,
  oldest first, one session for up to eight of them. A request is looked at again
  when its open criteria changed, or when the checkout moved and the last look was
  more than `--min-age` days ago.
- `--ghosts N` (default 8, 0 = off, at most 40): files whose description is stale,
  then files with none, then files that changed a little. Lock files, test data,
  generated and binary files, licences and files over 150 KB are left out, as are
  files somebody reviewed and left empty. A file is asked again only when it or
  its description changed.
- `--idle-days D` (default 30, 0 = off): the section "Liegengeblieben" of the
  report lists open requests without visible activity for D days. It is built
  from data alone, in every mode, also with `--dry-run`. What the gardener's account
  may not see (work in the private session of another account) does not count as
  activity, so the list is a hint.

Both jobs remember what they looked at in the same memory as the knowledge job
(`--state-file` or `--db`) under names of their own (`ledger|<project>`,
`ghosts|<project>`), so the request numbers never meet the entry numbers. The
gardener's account needs the right to read requests and ghosts of the project, which
a member has. A dry run lists the extra sessions with the tokens of their prompts.
Their instruction is separate, so the knowledge sessions do not grow by a byte.

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

Verify the copied database with `ctx db check --db /tmp/ghosttree.db` (the
`sqlite3` CLI cannot run `PRAGMA integrity_check` on this schema, see "Shrinking
and checking the database"), put it in the server's state directory while the
service is stopped, and then point each client at the new private URL with
`ctx setup`.

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

## Embeddings for hybrid search

Search finds knowledge entries by the words of the query. With an embedding
server it also finds the ones that say the same in other words: the two
rankings, words (full text) and meaning (vectors), are fused by reciprocal rank
fusion (k = 60) in `context_search`, the `/api/search` endpoint and the web
search. A hit that only the meaning found carries `"match": "semantic"` in the
API and `match:semantic` in the label of the MCP tool.

It is optional and off by default. Without `GHOSTTREE_EMBED_URL` nothing about it
runs and the answers are exactly those of the full-text search. Besides
knowledge, requests, pains, documents and the descriptions of paths ("ghosts")
can be embedded and searched the same way, and so can sessions (what was typed
and the answer each turn ended with); each kind is switched on by itself, see
"Other kinds" and "Sessions" below, and without a setting only knowledge is.
Neither the hook that delivers knowledge with a prompt nor the gardener use the
vectors, and the hint that a prompt brings about an earlier session asks for one
word and stays with the words.

Inference runs in a process of its own, not in `ctx`: ghosttree stays a cgo-free
single binary and talks to the model over HTTP on loopback. The text of every
embedded object (by default the active knowledge entries) goes to that process,
so keep it on the same machine. `ctx serve` logs a warning when the configured
address is not a loopback address.

### The embedding server

The measured choice is EmbeddingGemma 2 (768 dimensions, text only) as the Q8_0
GGUF of `ggml-org/embeddinggemma-2-GGUF`, about 295 MB, run by `llama-server` of
llama.cpp from release v0.7.0 (build b11454) on. Older builds do not know the
architecture. Download the model once:

```sh
install -d /var/lib/ghosttree-embed
curl -L -o /var/lib/ghosttree-embed/embeddinggemma-2-Q8_0.gguf \
  https://huggingface.co/ggml-org/embeddinggemma-2-GGUF/resolve/main/embeddinggemma-2-Q8_0.gguf
```

`ghosttree-embed.service` starts it as a container with a fixed memory limit
(1 GiB without swap, two cores), a read-only root filesystem, no capabilities,
the model mounted read-only and the port published on `127.0.0.1:8475` only.
`Restart=always` brings it back after a crash or a reboot. Pin `EMBED_IMAGE` to a
release tag (and change `EMBED_MODEL_DIR` or `EMBED_PORT` if you like) in
`/etc/ghosttree/embed.env`, then:

```sh
systemctl enable --now ghosttree-embed
curl -s http://127.0.0.1:8475/health        # {"status":"ok"} once the model is loaded
```

Without a container the same arguments run natively; put `MemoryMax=1G` in the
unit instead of the container limits:

```sh
llama-server -m embeddinggemma-2-Q8_0.gguf --alias embeddinggemma-2-q8_0 \
  --host 127.0.0.1 --port 8475 \
  --embeddings --pooling mean -c 4096 -ub 2048 -b 2048 -np 2 -t 2
```

Set every argument explicitly; defaults of `llama-server` change between releases
(v0.7.0 changed the default port). `--pooling mean` is the model's pooling. The
context of 4096 tokens is split over two slots of 2048 each: a text is cut to
2000 characters before it is sent (about 500 to 700 tokens of German or English
prose, never more than 2000 even for hashes and paths), so it always fits.

The second slot does not let a search overtake a batch. Both slots work on the
same compute threads, so a query sent to a server that is embedding a batch waits
for the batch. Measured on a server with two cores while the background job
embedded documents (batches of eight, about 5 seconds per document), every
search took 1.59 seconds: it ran into the query timeout of 1.5 seconds and was
answered from full text. Smaller `-ub` and `-b` do not help, because an embedding
needs the whole text in one physical batch. What helps is a second server for the
queries, see "A second server for queries" below. A single server keeps working
without it: a search that comes while the job is embedding documents is answered
from full text, which is the case during the first backfill (hours) and for a
few seconds after every new or edited entry.

### Configuring ghosttree

Put the address in `/etc/ghosttree/server.env` (the unit already reads that file)
and restart the server:

```text
GHOSTTREE_EMBED_URL=http://127.0.0.1:8475
```

| Environment variable | Serve flag | Default |
| --- | --- | ---: |
| `GHOSTTREE_EMBED_URL` | `--embed-url` | empty: off |
| `GHOSTTREE_EMBED_QUERY_URL` | `--embed-query-url` | empty: the server of `--embed-url` |
| `GHOSTTREE_EMBED_MODEL` | `--embed-model` | `embeddinggemma-2-q8_0` |
| `GHOSTTREE_EMBED_QUERY_TIMEOUT` | `--embed-query-timeout` | `1500ms` |
| `GHOSTTREE_EMBED_BATCH` | `--embed-batch` | 8 |
| `GHOSTTREE_EMBED_PAUSE` | `--embed-pause` | `2s` |
| `GHOSTTREE_EMBED_IDLE` | `--embed-idle` | `30s` |
| `GHOSTTREE_EMBED_MAX_BACKOFF` | `--embed-max-backoff` | `5m` |
| `GHOSTTREE_EMBED_MIN_SIMILARITY` | `--embed-min-similarity` | `0.55`; `0` is no minimum |
| `GHOSTTREE_EMBED_KINDS` | `--embed-kinds` | `knowledge` |
| `GHOSTTREE_EMBED_SEARCH_KINDS` | `--embed-search-kinds` | the same as `GHOSTTREE_EMBED_KINDS` |
| `GHOSTTREE_EMBED_SESSION_WINDOW` | `--embed-session-window` | `00:00-06:00`; `HH:MM-HH:MM` in the local time of the server, may cross midnight, or `always` |
| `GHOSTTREE_EMBED_SESSION_WEIGHT` | `--embed-session-weight` | `2`; above 0 up to 100, the weight of the vectors against 1 for the words in the search of sessions |

The model id names the vectors: it is stored with each of them, together with the
version of the text formats the model card prescribes (`task: search result |
query: …` for a query, `title: … | text: …` for an entry) and a hash of the text.
Changing the model id, the formats (a new ghosttree release) or the text of an
entry makes the vector out of date and the job computes it again; search only
reads current vectors. If you exchange the model file but keep the id, the stored
vectors have another length than the new ones; search leaves them out and logs
`embed_vector_dimension` once a minute. Give the new model a new id.

### Other kinds: requests, pains, documents and descriptions of paths

Knowledge is the kind hybrid search began with. Four more can be embedded and
searched the same way, and each one has two switches of its own, so that a kind
can be embedded and measured before its search uses the vectors:

```text
GHOSTTREE_EMBED_KINDS=knowledge,request,pain,document,ghost
GHOSTTREE_EMBED_SEARCH_KINDS=knowledge
```

`GHOSTTREE_EMBED_KINDS` (`--embed-kinds`) names the kinds the background job
embeds, `GHOSTTREE_EMBED_SEARCH_KINDS` (`--embed-search-kinds`) the kinds whose
search uses the vectors; it must be a subset of the first and defaults to all of
it. The names are `knowledge`, `request`, `pain`, `document`, `ghost` and
`session` (also `requests`, `pains`, `documents`, `docs`, `ghosts`, `files`,
`sessions`), or `all`, which is the five kinds before `session`: sessions are
embedded only when they are named (`all,session` is every kind), see "Sessions".
Without
either variable it is `knowledge`, and every answer, the cost and the stored data
are those of knowledge alone. A kind that is embedded but not searched answers
from full text and its queries are not sent to the embedding server. Take a kind
in step by step: add it to `GHOSTTREE_EMBED_KINDS`, wait until `ctx embed status`
shows nothing due, measure with `ctx eval search --set` (below), then add it to
`GHOSTTREE_EMBED_SEARCH_KINDS` if it helps.

| Kind | What is embedded | Where it is searched | Order and paging |
| --- | --- | --- | --- |
| `request` | title, the first 1,200 characters of the description, the acceptance criteria (their text, not their state); every state | `GET /api/search?kind=requests` (`context_search`), `GET /api/requests/search` (`request_search`, `ctx request search`), the requests page and the search page of the web UI | With a query: full-text rank (bm25 over title and description) fused with the vectors; pages are positions, the cursor is `~N`. Without a query, or for a query of ordinary words: the list by id with its cursor of ids, as before |
| `pain` | title and text (not the state) | `GET /api/pains?q=` (`pain_search`, `ctx pain list`), the search page | The words side is what it always was: the pains that contain the query as a phrase, by state. Fused with the vectors |
| `document` | title and the first 1,500 characters of the head revision; active documents only | `GET /api/documents?q=` and `ctx doc search`; the list of documents without `q` is unchanged | Full-text rank over title, slug and the whole head text, fused with the vectors; `limit` 20 |
| `ghost` | the path (a directory with a closing slash) and its description | `GET /api/ghosts/search` (`context_search kind=files`) | Full-text rank over path and description, fused with the vectors |

Documents had no search before; the words side of their hybrid search is a new
full-text index, `documents_fts` (title, slug and the text of the head revision of
every document), made when the server starts with this version (all documents in
one go, seconds for hundreds) and kept by triggers when a revision, a title or a
slug changes. A document that is archived is never a hit. The index is also what a
document search answers from without embeddings.

A request search that is ranked cannot continue after an id, so its `next_cursor`
is `~N`, the number of hits already shown, and the pages are slices of one fusion:
both rankings are read to the same depth for every page (1,000 requests each, ties
by id), so nothing falls between two pages and nothing shows on two, however far
one pages. For that the page size and the query must not change between the pages
(the room for hits that only the meaning found is a share of a page: five, or half
of a short one; it is used up once for the whole search, not once per page). The
fusion ends where the rankings do. If the ranking by words was cut at its depth
because more requests contain the words than that, the last page has
`"truncated": true` (`truncated` in the output of `request_search`) and no cursor:
more match than any cursor reaches, so narrow the query or the filter. A ranking
by meaning that was cut loses no hit and does not set it: only its best few
neighbours join the fusion in any case.

The kind of cursor decides how the search continues, not the state of the
embedding server. A cursor that is an id (from a page answered from the list)
always continues the list by ids, even if the server is back, so the paging goes on
forward instead of starting the ranking over; a cursor `~N` always continues a
ranking, and where the embedding is not available (the server failed between two
pages, or the kind was switched off) it is read as "skip N" in the list, which goes
on in another order than the ranking but never jumps back.

Every hit that only the meaning found carries `"match": "semantic"` in the JSON of
the search of its kind and `match:semantic` in the label of the MCP tools
(requests: `match_reason` says "semantic match"). Hits of the words carry nothing.

The vector part sees exactly what the words part sees for that reader, and nothing
is compared that the reader may not read. What can be put in SQL is part of both
queries: for requests the whole rule (project roles, shared projects, requests
without a project belong to their author, the ones in a remote nobody has claimed
to their author and the admin, and requests distilled from the private session of
another account are held back); for the descriptions of paths and for documents
the projects the account has a role in (a project shared with the organisation
gives its members theirs) and the unclaimed ones; for pains the project, which
every pain search names (the pains group of the web search names all the projects
the viewer sees, in one search with one embedding of the query). The row rule of the caller (can the reader see it, may it
be delivered, is it active) is applied to both rankings before they are fused, so
a row that is hidden can neither appear nor move another one, and at most 1,000
rows of a ranking are checked. The vector of an object that is archived or gone is
never read: the search reaches a vector only through its object.

Vectors of these kinds live in the table `embeddings` (`kind`, `object_id`, and
the columns of `knowledge_embeddings`: model, text format, hash, int8 vector with
its scale, the time the text was read) and failures in `embedding_failures`.
Knowledge keeps `knowledge_embeddings` and `knowledge_embedding_failures`: it needs
no migration, its 2,800 vectors are not computed again, and a rollback to the
previous release still finds them. The tables are made on start. The job serves the
kinds in turn, one batch of one kind at a time (knowledge, requests, pains,
documents, then descriptions of paths), with the same batch, pause, back-off and
parking for all of them, so a kind with thousands of objects does not make a new
pain wait. Sessions are not in that turn: they come after all the others and only
in their window, see "Sessions". A change in a request's criteria makes its vector out of date although
the request itself was not touched; ticking a criterion off does not. When nothing
is due, the job removes the vectors of objects that are gone or archived, at most
every 10 minutes.

How long the first catch-up takes is not measured here. A server with about 5,500
descriptions of paths needs about 690 batches of eight; at the default pause of 2
seconds that is 23 minutes of waiting alone, plus what the model needs for eight
short texts (the 5 seconds per document measured for knowledge was for texts of up
to 2,000 characters; a description is a fraction of that), so plan for one to two
hours, during which the other kinds keep their turn. Requests (hundreds), pains
(dozens) and documents (hundreds, 1,500 characters each) take minutes.

### Sessions

Sessions are searched by what was said in them: the prompts a person typed and the
answer each turn ended with, as one vector each. Not the tool calls, their
results, the messages between two calls, the thinking, and nothing a harness
injected (notices of background jobs, interrupt markers, the summary after a
compaction, echoes of local commands). Which chunk is which comes from the search
index of the session view (`chunk_index`) and not from `session_chunks.text`: for
Claude Code that column holds a line `[tool call: …]` for every call, and most of
what it counts as text is that. A text has to be at least 20 characters long after
cleaning and is cut to 2,000 (the text format is the document format with the title
`none`).

```text
GHOSTTREE_EMBED_KINDS=knowledge,session
GHOSTTREE_EMBED_SEARCH_KINDS=knowledge
GHOSTTREE_EMBED_SESSION_WINDOW=00:00-06:00
```

Without `session` in `GHOSTTREE_EMBED_KINDS` and `GHOSTTREE_EMBED_SEARCH_KINDS` the
answers, the cost and the data are those of the release before. Take it in like the
other kinds: embed first, measure, then search. `all` does not include sessions
(they send what was typed and answered to the embedding server, and whoever set
`all` before there were sessions did not agree to that): name them, alone or as
`all,session`.

**What a text is.** It is made from the stored line the way every text gets into the
database: the images are replaced (`blobstrip`), secrets are redacted in the line and
in the text (`redact`), then the placeholders (`[ghosttree: …]`), the image marks
(`[Image #3]`) and the blocks a harness puts into a prompt (`<system-reminder>` and
the like) are removed. A text that the chain changes was changed on arrival already;
the chain is the second lock, for what reaches the database another way and for
what a later rule finds. The hash of the finished text decides whether a vector is
current. `ctx redact-sessions` and `ctx strip-images` change what is stored for a
chunk; the vector of every chunk whose stored line they change is dropped in the same
transaction (a change of the text column alone leaves it: the text of a vector is made
from the line), and the mark that its session is complete becomes a tombstone (version
0, and its generation counted up), so that the server looks at the session again. A
look hands back the generation of the mark it saw, and the server writes the mark of
a session only if it is still that generation; a look made before the change, which
found the session complete with the vector that is gone, can therefore not bring the
old mark back, however long the transaction of the command ran after it wrote the
tombstone. The server makes the vector again from the text as it is now. A dry run
drops nothing and counts what it would drop (`session_embeddings` in the report of
`ctx redact-sessions`).

**When.** A prompt is embedded as soon as the server has it. The answer that ends a
turn is embedded when the turn is closed: a prompt that a person typed has come
after it, or the session has been quiet for 30 minutes. Until then every message an
agent writes between two calls would be the last one for a moment, and the server
would spend three seconds on each and keep a vector for a text that is not the
answer. An agent that works uploads every few minutes, also while it waits for a
build; half an hour of silence is a person who has gone or a session that is over.
An answer that the session outgrows after it was embedded (it went on after the
half hour) loses its vector the next time the session is looked at, which the new
chunk brings about.

**Night work.** There are many texts: 14 thousand on the installation they were
measured on, in 5,300 sessions, and the model needs about 3.2 seconds for one on
two cores. The first run over such a stock takes about 13 hours, so three nights
of six; after that a day brings some hundreds of texts, about 18 minutes of work.
The job therefore embeds sessions only inside `GHOSTTREE_EMBED_SESSION_WINDOW`, in
the local time of the server, by default midnight to six; `always` has no window.
Outside it they wait and cost nothing. Every other kind goes first, at any hour: the
sessions are asked for only when nothing else is due, so a new request waits at most
for the batch that is running (eight texts, about 25 seconds). The texts go to the
job server (`GHOSTTREE_EMBED_URL`) and never to the server for queries; the pace
(batch, pause, back-off, parking of a text the server fails on) is that of the other
kinds. The newest session comes first, so that what was written last is searchable
first while the stock is caught up on. The state is in the database, and an
interrupted run goes on where it stopped. While the index of the chunks is still
being built after an update nothing is embedded: a session whose chunks are not all
in it would be taken for complete.

**Storage.** One row per text in `session_embeddings` (the chunk, model, text format,
hash, the int8 vector with its scale, the time the text was read), failures in
`session_embedding_failures`, and in `session_embedding_sync` the sessions that were
looked at and found complete: all their texts are embedded, or too short to count,
or refused by the server for good (a text that fails for a time keeps its session
waiting, so that it is tried again at its time). Such a session is left alone until
it gets a chunk or its mark was made under another model, text format or version of
the rules that make the texts (`sessionTextVersion` in the code, raised with any
change of them): a release that changes what counts as a typed prompt, which chunk
ends a turn or how a text is cleaned thus reaches the sessions nobody writes to
any more, and what the new rules no longer call for goes. The rows reference their
chunk (`session_chunks.id`) and the mark its session, all `ON DELETE CASCADE`: a
chunk that is deleted takes its vector and its failure along, a session that is
deleted (after its chunks, which do not cascade from it) its mark, without the job
having to find them. The look at a session, which only reads, names the vectors
that have no text any more (an outgrown answer, a text now too short or empty, a
prompt the rules now take for injected); the job drops them in small writer jobs of
200, so uploads wait for no walk through the stock. A vector is stored only if the
stored line of its chunk is still the one the text was made from: the look gives the
hash of the line with the item, and the writer reads the line inside the transaction
that stores the vector and compares the hashes (a read and a hash: about a
millisecond for two megabytes, where the chain that makes the text took 6 to 80
milliseconds on logs and tokens of that size, with every other write waiting). A
redaction that comes while the job embeds the chunk cannot leave the vector of the
text before. In its idle turn, at most every 10
minutes and at any hour, the job also removes what a database without foreign keys
left behind (three statements on keys). A text takes about
a kilobyte, so 14 thousand are 15 MB. The tables are made on start. A search scans
the vectors of the sessions the reader may read: some 50 ms for 14 thousand texts
from SQLite (11 ms from memory), 225 ms at five times that; a cache of the matrix
would pay off around 30,000 texts and is not built.

**Search.** `GET /api/search?kind=sessions` (`context_search kind=sessions`,
`context_sessions` with a query) and the web search of sessions (the page of sessions
with a query and the sessions group of the search page) rank a query by words and by
meaning and fuse the two rankings by reciprocal rank (k = 60), the vectors counting
`GHOSTTREE_EMBED_SESSION_WEIGHT` times (2) against 1 for the words: on questions asked
in sentences the vectors had the session in the first ten for nearly every question
and the words for three of four, and the words stay in for names, ids and error
messages, which no question measured. A session scores in the vector ranking with the
best of its texts. The words ranking is that of sessions (in the order of their first
hit), and what comes back is one hit per session: the first words hit, or, for a
session that only the meaning found, the text that came closest, from its beginning.
Such a hit carries `"match": "semantic"` (`[match:semantic]` in the MCP tools), and at
most five of ten hits are there by meaning alone. A query of ordinary words, the sort
by time and the kinds of text of the web search that are not messages (output,
commands, thinking) are answered by words, and so is a search while the embedding
server does not answer. The hint a prompt brings ("this also came up in session …")
is always by words. In the web search a value of a facet (project, machine, platform)
counts what the list holds after a click on it: the sessions the words found within
that filter, and the few found by meaning alone, taken from the sessions within it (the
filters of time and "mine" included). A facet may therefore say more than the list
shows now, since the room for hits of meaning alone is spent in the project that has
them first.

**Who sees what.** The readable sessions are fixed first, with the rule of the words
search (`TranscriptPrefilter` and, where it is not exact, the row rule): private only
for the owner, shared for the members of the project, with guests for the guests. The
vectors are compared only over those, so a session the reader may not read is not
compared and cannot move one he may; afterwards the usual `CanSeeTranscript` and
`MetaView` apply (a guest gets the address and the title, not the number, the machine,
the branch, the directory or the owner). A vector never leaves the server, and the
text a hit shows is text the reader may read.

**Measuring.** `ctx eval search --set queries.jsonl` takes `"kind": "session"` with
the number of the session, or its address, as `target`; a query runs through the
production search over the whole instance (a session of any project may be the one).
`--sessions-builtin` adds the titles of active knowledge entries with evidence (at
least 15 characters, `--sessions-sample 300` of them, drawn with `--seed`, by default
the draw of the experiment this was decided on), with any session of the evidence as
the answer. Those are summaries written by a model and not questions, the harder set:
on them the gain was small, on questions asked in sentences it was large. Both print
counts only. `--embed-session-weight` sets the weight for the fused row. Run `ctx
embed status` first.

```sh
ctx eval search --db /var/lib/ghosttree/ghosttree.db --embed-url http://127.0.0.1:8475 \
  --set questions.jsonl --sessions-builtin
```

`ctx embed status` has a line for the sessions when `--embed-kinds` (or
`GHOSTTREE_EMBED_KINDS`) names them: the number of texts that count now, how many
have a vector, how many are due, and the window with whether it is open. The sessions
are then part of the total of the background job.

```text
session          embedded 2300 of 14126, due 11826  [embedding on, search on]  [window 00:00-06:00 local time of the server, opens in 3h12m0s]
```

It counts by chunk and does not read the text of a chunk that has a vector, so a text
that changed is only noticed by the job, which compares hashes when it looks at a
session. The texts that have no vector are read and cleaned to count them, which takes
a while on a large stock: the command does it only for an installation that embeds
sessions. Without the sessions among the kinds the line says `not counted` (and what
to name), the sessions are not in the total, and nothing of them is read.

### A second server for queries

Searches and the background job can use separate embedding servers. The reason is
the measurement above: while the job embeds documents, a query on the same server
waits behind them, and the search falls back to full text. A second `llama-server`
that only embeds queries is never busy with documents.

`ghosttree-embed-query.service` is that server as an example: the same GGUF file,
its own port (8476), `-t 1 -np 1 -c 1024 -ub 1024 -b 1024`, a container with
`--cpus 1 --memory 768m --memory-swap 768m`, a read-only root filesystem, no
capabilities, and the port published on `127.0.0.1` only. It reads `EMBED_IMAGE`
and `EMBED_MODEL_DIR` from the same `/etc/ghosttree/embed.env` as
`ghosttree-embed.service`, which stays the server of the background job; its own
port is `EMBED_QUERY_PORT`, so that `EMBED_PORT` does not move it. At their limits
the two containers take three cores and 1.75 GiB together. A query longer than the
1024 tokens of its context (ghosttree cuts it to 1000 characters; only a text of
rare characters comes near) is refused and that search is answered from full text.

```sh
systemctl enable --now ghosttree-embed-query
curl -s http://127.0.0.1:8476/health
```

```text
GHOSTTREE_EMBED_URL=http://127.0.0.1:8475
GHOSTTREE_EMBED_QUERY_URL=http://127.0.0.1:8476
```

`GHOSTTREE_EMBED_QUERY_URL` (`--embed-query-url`, also in `ctx embed status` and
`ctx eval search`) is the base URL of the server that embeds search queries. Empty
means the same server as `GHOSTTREE_EMBED_URL`, which is exactly the behaviour
without the option, and naming the same address twice counts as one server. The
job always uses `GHOSTTREE_EMBED_URL`. Setting only the query URL is a
configuration error and `ctx serve` does not start with it: without the job there
are no vectors to search. The URL is checked like `GHOSTTREE_EMBED_URL` (no
credentials, no query or fragment), and `ctx serve` warns when it is not a
loopback address, because then every search query leaves the machine.

A query vector is only worth something next to vectors of the same model, so both
servers have to run the same GGUF file. ghosttree checks that in the background
after the start, without holding anything up: it sends the same short fixed text
to both servers, in the query format, and compares the two vectors. They count as
the same model with the same number of dimensions and a cosine of at least 0.999;
the same file on other threads or in another build differs only by rounding, far
above that, while another model or quantisation is far below. If they differ, the
log says `embed_models_differ` as an error with what differs, and searches use
the vectors no longer: they are answered from full text, without an error for the
caller, until a check finds the servers equal and the log says
`embed_models_agree`. The background job goes on; the stored vectors are the job
server's. To clear it, start both from the same file; after changing the model on
both, give it a new `--embed-model` id.

The check repeats every 10 minutes while the servers agree: a model is exchanged
by someone restarting a server, a query vector of another model makes the ranking
worse and not wrong, and each check costs two short texts, one of them in a slot
of the server that is busy with the job. While they differ it repeats every
minute, so the search is back one check after the fix. While a server does not
answer (down, still loading the model, or too busy to answer in a minute) the
check repeats after 5 seconds, then 10 and so on up to a minute, logs
`embed_models_unverified`, and changes nothing: the last verdict stands, and
before the first one searches run as with one server, so a job server that is
down or busy does not switch the vector part off. The log repeats a state at most
every 10 minutes.

One server alone keeps working; leave `GHOSTTREE_EMBED_QUERY_URL` empty. The
disadvantage is the one described above: a search that comes while the job is
embedding documents is answered from full text.

### Backfill and upkeep

The server computes the vectors itself, in a goroutine of its own that starts ten
seconds after the server and never blocks it. It takes the entries that were
edited most recently first, sends at most `--embed-batch` of them in one request,
waits `--embed-pause` between two requests, and never has more than one request
open. If the embedding server fails it waits 5 seconds, then 10, 20 and so on up
to `--embed-max-backoff`, and tries again; after a restart it continues with what
is still missing, because its state is in the database.

When nothing is due it looks again every `--embed-idle`. It does not read the
text of every entry for that: an entry is looked at again when it was edited at
or after the time its vector was made (every change of a text goes through
`updated_at`), and only those texts are read and compared with the hash; one look
at every entry is made when the server starts, which finds a text that was
changed behind the application's back. A change that leaves the text alone, such
as a new confidence, costs no request to the embedding server.

One text can make the embedding server fail while it answers the others. When a
batch fails twice, the texts of that batch are sent one by one; the ones that
the server answers are stored and the one it does not answer is parked in the
table `knowledge_embedding_failures`, for 5 minutes, then 10, then 20, up to 6
hours, and logged as `embed_entry_parked`. If the server answers none of them it
is the server, and nothing is parked. A text that the server refuses outright (an
HTTP 400, 413 or 422, for example a text longer than its context) is parked for
good until the entry changes or the model or text format does; to try such texts
again with the same model, empty the table. Parked entries count as due in
`ctx embed status`, with their number.

A database of about 2,700 entries needs roughly 20 minutes at the defaults on the
hardware the sample was written for (a batch of eight documents in about 1.4
seconds, plus the pause). On two cores of a server CPU a document took about 5
seconds, so the first run over about 2,800 entries takes hours. Meanwhile search
already uses the vectors that exist, and with a second server for queries it does
so at full speed.

```sh
ctx embed status --db /var/lib/ghosttree/ghosttree.db --embed-url http://127.0.0.1:8475
```

shows how many active entries have a current vector, how many are due, how the
job last fared (failing since, error, last progress) and whether the embedding
server answers. The three lines about entries are knowledge; below them each
other kind has a line of its own, for example

```text
request          embedded 412 of 469, due 57
ghost            embedded 1204 of 5510, due 4306 (2 parked)
```

(embedded of the objects that count, due, and how many of the due are parked).
With `--embed-kinds` and `--embed-search-kinds` (or their environment variables;
the command cannot know which ones the running server has, so it says nothing
about them unless it is given them) each line ends with `[embedding on, search
off]` as that configuration would have it. With `--embed-query-url` (the same
address as for `ctx serve`) it shows both servers, `job server` and `query
server`, and a line `same model` that says whether the two answer the probe text
alike. It only reads the database, so the server may keep running; run it as
described in "Running maintenance commands". The vectors take 768 bytes per object
and live in the table `knowledge_embeddings` (knowledge) and `embeddings` (the
other kinds).

### When the embedding server is away

A search waits at most `--embed-query-timeout` for the vector of its query. If the
server is down, slow or answers an error, the search is answered from the
full-text ranking, as if embeddings were off; the caller sees no error. The server
logs `embed_search_fallback` at most once a minute, with the error and the number
of searches since the last line. A question made only of ordinary words ("what is
left to do") is never sent to the embedding server. With a second server for
queries, it is that server whose absence makes a search fall back; the job server
being down or busy does not touch searches.

To turn it off, remove `GHOSTTREE_EMBED_URL` (and `GHOSTTREE_EMBED_QUERY_URL`)
and restart; the stored vectors are not read any more and can stay.
`ghosttree-embed.service` and `ghosttree-embed-query.service` can be stopped at
any time without touching ghosttree.

### Measuring what it gains

```sh
ctx eval search --db /var/lib/ghosttree/ghosttree.db --embed-url http://127.0.0.1:8475
```

takes the quotes in `knowledge_evidence` of at least 30 characters that back
active entries (`--sample 1500` of them, drawn with `--seed 20261020`) and asks
the production search which entry a quote leads to, once by words alone, once by
vectors alone and once fused. It prints n, R@1, R@3, R@10 and MRR@10 per ranking
for all targets, for the targets that are not quarantined, and for a search that
leaves the quarantined entries out, as the search for agents does. The full-text
row is the "before", the hybrid row the "after". It never prints a quote or any
text of an entry. With a second server for queries, add `--embed-query-url`: the
queries are embedded there, and the run stops first if the two servers do not
serve the same model, since the numbers would then mean nothing. Wait until
`ctx embed status` shows nothing due, or the vector rows understate the result. The quotes are passages of transcripts, closer to the
entry than a question a person asks, so read the numbers as "finds the entry for
the passage it came from".

### Measuring the other kinds

```sh
ctx eval search --db /var/lib/ghosttree/ghosttree.db --embed-url http://127.0.0.1:8475 \
  --set queries.jsonl --requests-builtin
```

measures requests, pains, documents, descriptions of paths and knowledge on queries
whose right answer is known, once by words, once by vectors and once fused, through
the search function of each kind as the instance owner sees it, reading only. Each
line of the set is a JSON object with the query, the kind and the target:

```json
{"query": "where is the trust of an entry sorted", "kind": "ghost", "target": "github.com/dw/p:internal/store/knowledge.go"}
{"query": "is there a request for this already", "kind": "request", "target": "412"}
```

`kind` is `request`, `ghost`, `pain`, `document`, `knowledge` or `session`;
`target` is the id of the object, for a description of a path `project:path` (split
at the first colon), and for a session its number or its address. Other members of a line are ignored. A query is run in the project of its
target, the way an agent asks, with ten hits; the report has a block per kind with
n, R@1, R@3, R@10 and MRR@10 for the three rankings, how many targets no longer
exist (they are not measured), and how many objects of the kind have a current
vector. Nothing of a query or of an object is printed, only counts. The "words"
row is what the search gives today: for requests the list by id of the requests
that match (the whole list, for a query of nothing but ordinary words), for pains
the pains that contain the query as a phrase; so the words row of requests is not
a ranking, and part of what the fusion gains is having one.
`--requests-builtin` adds the queries the request ledger already holds the answer
to: the quotes of the sessions in which a wish was spoken (`request_sightings`) with
the request that came of them, and, for every request marked as a duplicate of
another or as superseding it, its title as the query and the other request as the
answer (`--sample` of each, `--min-chars` for the quotes). A set made of private
data belongs on the server and not in a repository. The run needs the embedding
server for the queries, `--embed-query-url` as above, and the vectors of the kinds
it measures: see `ctx embed status`.

### What a search shows that only the meaning found

Without a limit on similarity the embedding ranking has a nearest neighbour for
any query, however far away, and a result list would be padded with them. Two
things keep that in check. An entry enters the embedding ranking only with a
cosine of at least `--embed-min-similarity` (default 0.55). The value belongs to
EmbeddingGemma 2: on the project's own entries the best neighbour of each of 398
quotes had a cosine of at least 0.66, about 0.78 for the median quote, so 0.55 cuts
the tail of unrelated entries and not the answers. The quotes are longer than what
people type and score higher than short questions; treat 0.55 as a starting value
and re-measure on your server with `ctx eval search` and several values of
`--embed-min-similarity`, and set it again when you change the model. And a
search shows at most five hits that only the meaning found (at most half of a
short result), after the hits that contain the words; so a search that finds two
entries by their words comes back with two and a handful of neighbours, not with a
full page.

What a search counts as used (`search_hits`, which orders the bootstrap and feeds
`ctx usage`) is what it delivers, once. The entries the search fetches beyond the
limit to have enough left after the access rules, and the ones the rules drop,
are not counted, with or without an embedding server.
