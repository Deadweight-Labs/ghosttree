# Ghosttree

Ghosttree gives coding agents a memory that does not have to live in your Git
repository. It stores project knowledge, file descriptions, work requests,
long-form documents, and session history on a service you control. Claude Code,
Codex, and OpenCode can all use the same context.

The project is in active pre-1.0 development. It already runs as a single,
CGO-free Go binary, but its storage and command interfaces may still change
between minor releases.

> Ghosttree is source-available software, not open source. Personal and
> noncommercial use is allowed, and qualifying micro-businesses receive an
> additional commercial-use grant. See [Licensing](#licensing).

## Why Ghosttree exists

Agent instructions tend to accumulate in `AGENTS.md`, `CLAUDE.md`, plans,
scratch notes, and comments that explain yesterday's work rather than today's
code. The useful parts become hard to search, while the repository gains files
that are irrelevant to its users.

Ghosttree moves that material into a separate lifecycle:

- short facts, decisions, pitfalls, and conditional instructions are searchable
  knowledge;
- file and directory descriptions form a generated ghost tree beside the repo;
- substantial work is tracked in a request ledger with criteria and evidence;
- specs and plans are ordinary local Markdown files while being edited, then
  published as versioned documents outside Git;
- session collectors keep the original context available across machines and
  agent harnesses.

## How it fits together

```text
Claude Code / Codex / OpenCode
        │  MCP, hooks, transcripts
        ▼
      ctx on each workstation
        │  authenticated HTTP on a network you choose
        ▼
  Ghosttree server ── SQLite + FTS5
        │
        └── generated .ghosttree/ view in each repository
```

`ctx` contains the client, server, collector, MCP server, installer, migration
tools, and operator UI. The only runtime database is SQLite. A server can stay
on one machine, while collectors and agent integrations run wherever work
happens.

## What works today

- Scoped context by repository, branch, and machine, with path-activated
  instructions.
- Full-text search across knowledge, requests, file descriptions, and captured
  sessions.
- Incremental Claude Code and Codex transcript collection with local credential
  redaction and outage replay.
- MCP tools for context lookup, corrections, request tracking, regression
  evidence, and ghost-file maintenance.
- Harness installation and diagnostics for Claude Code, Codex, and OpenCode.
- A work ledger with acceptance criteria, relationships, evidence, and
  interrupted-session handoff.
- Quarantined session distillation, review, usage measurement, recurrence
  tracking, and cost reporting.
- Versioned documents with local drafts, optimistic concurrency, byte-preserved
  UTF-8 revisions, history, diff, rename, archive, and provenance-backed import.
- Immutable named snapshots that materialize a consistent project-context
  state, bind their recorded Git provenance and entries into one digest, and
  remain independently exportable and verifiable after live context changes.
- A read-only web interface for operators.

Run `ctx` or `ctx <command>` without arguments to see the available command
surface.

Automatic `SessionStart`, `UserPromptSubmit`, and `PreToolUse` context shares a
24,000-character budget per session on each work machine. The count includes
headings, reminders, and the one notice emitted when context is shortened.
Further automatic context is then suppressed; use `context_get` or the local
`.ghosttree/INDEX.md` to retrieve full content explicitly. Repository changes and
restarted hook processes keep the same session budget.

`ctx doctor --only budget` reports recent local output counts against the limit.
It distinguishes confirmed stdout writes from unconfirmed reservations after an
interrupted write; these are not confirmations that the harness used the text.
Accounting starts with the first hook run by the updated client, so earlier
output from an already running session is unknown. Receipts contain counts and a
hashed session identity under `$XDG_STATE_HOME/ghosttree/context-budget` (default
`~/.local/state/ghosttree/context-budget`). Missing session identity or unreadable
accounting state suppresses automatic context. Explicit retrieval still works.

Descriptions for removed paths stay active until a move is resolved or deletion
is explicitly confirmed. Preview selected paths with `ctx ghost archive <path>`;
archive them with `ctx ghost archive <path> --reason "why it was removed"
--confirm-deleted`. Selection is exact and never recursive, including directories.
The command refuses live paths and detectable moves; `ctx mirror` resolves
unambiguous moves first. Archived text remains available through
`ctx ghost history <path> --voll` and `context_file_history`.

## Quick start

Ghosttree is designed for a private network. The example below keeps the server
on loopback; choose your own private-network address or put it behind a reverse
proxy before connecting another machine.

```bash
make build

./dist/ctx person add <name> --db ./ghosttree.db
./dist/ctx serve --db ./ghosttree.db --listen 127.0.0.1:8474
```

`person add` prints the token once. In another terminal:

```bash
./dist/ctx setup --server http://127.0.0.1:8474 --token <token>
./dist/ctx install codex       # or claude / opencode
./dist/ctx watch --once        # import existing supported transcripts
./dist/ctx status
./dist/ctx doctor
```

For a persistent Linux service, use the templates and notes in
[`deploy/`](deploy/). The supplied server unit binds to loopback unless
`GHOSTTREE_LISTEN` is set in `/etc/ghosttree/server.env`.

### Writing a document

Drafts live under `.ghosttree/edit/`, which Ghosttree excludes from Git. The
server only sees a document when you push it.

```bash
ctx doc new spec storage-redesign
# edit the printed Markdown path with any editor
ctx doc push storage-redesign -m "Describe the storage boundary"
ctx doc log storage-redesign
```

Existing Markdown can be imported with an exact source hash. `--clean` removes
the source only after the server exposes proof for that same hash and stored
revision.

```bash
ctx doc import path/to/design.md --kind spec --slug storage-redesign --clean
```

### Coordination messages in Claude Code (opt-in)

`ctx claude [claude args...]` starts Claude Code with the ghosttree channel
loaded. The aim is that `ctx coord` messages and agent mentions reach the
session: a waiting session wakes up, a working one receives the message at
its next tool result. Measured with Claude Code 2.1.284 (2026-09-30) and
2.1.286 (2026-10-01, with the handshake rule below).
The launcher generates one identity per launch (`claude:<host>:<uuid>`), writes a
temporary MCP config for the `ghosttree-channel` server (`ctx channel --agent
<id>`), and runs `claude --mcp-config <tmp> --dangerously-load-development-channels
server:ghosttree-channel ...`. The identity also goes to the process environment
as `GHOSTTREE_AGENT_ID`, which `ctx mcp` reads before any harness session id, so
the channel and the `coord_*` tools act as the same agent. The variable changes
only the coordination identity; search, interrupted-work handoff, and snapshots
keep the harness session id. A `codex` or `opencode` started inside such a
session ignores the variable (its provider prefix does not match); a `claude`
started inside it without the launcher cannot be told apart and shares the
identity. Arguments after the
launcher's own flags (`--dry-run`, `--agent <id>`) pass through unchanged, the
exit code is claude's, and the config file is removed afterwards.
`ctx claude --dry-run` (or `GHOSTTREE_CLAUDE_DRY_RUN=1`) prints the command and
config without starting anything. Claude answers with the channel's `reply`
tool, which stores the answer and marks the message `acked`. It starts a new
conversation with the channel's `send` tool: `text` (required), `mention` (list
of agent ids), `room` (`project`, the default, or `machine`) and `intent`
(`question`, `approval`, `blocker`, `handoff` or `ack`; the first four need a
mention). No second `ctx mcp` entry is needed for that.

This is opt-in at start: `ctx install claude` does not register the channel, and
a running session cannot be attached later. The channel is a Claude Code
research-preview feature; ghosttree is not on Anthropic's allowlist, so
`--dangerously-load-development-channels` is required and Claude asks you to
confirm it at every start. Team and Enterprise plans can switch channels off
(`channelsEnabled`).

#### ghclaude: the channel without typing `ctx claude`

`ghclaude` is a link to `ctx` in the same directory. When ctx is started under
that name it runs `claude` with the channel and passes every argument through
unchanged (it replaces itself with claude, so exit code, signals and
stdin/stdout are claude's). It looks for `claude` like `ctx claude` does
(`GHOSTTREE_CLAUDE_BIN`, then `PATH`) and, because a GUI such as T3 often has a
shorter `PATH`, next to itself. `ctx install claude` and `ctx join` ask once
(`Install ghclaude? [Y/n]`, on `/dev/tty`; without a terminal nothing is created
and the command to run is printed). `ctx install ghclaude` creates it without a
question, `ctx uninstall ghclaude` removes only that link, and `ctx doctor`
checks that it leads to ctx. A shell alias would not do: T3 starts Claude
through the Agent SDK with a path to the executable, and an alias hides what runs.

Measured with Claude Code 2.1.296 (2026-10-16, claude.ai login, `--model sonnet`,
local server, throwaway ghosttree config): the channel works only in an
interactive session. A non-interactive `claude` (`-p`, also with
`--input-format stream-json --output-format stream-json`, the way the Agent SDK
starts it) accepts `--dangerously-load-development-channels` without error, shows no
confirmation dialog and does not block, but never registers the channel: the
debug log has no `Channel notifications registered`, and a message sent to the
idle session stays `injected` and starts no turn (the interactive run in the same
setup was `acked` and answered). So `ghclaude` set as the Claude executable in T3
starts the session but does not wake it; it wakes idle agents in a terminal.
Channels also need a claude.ai login: with `ANTHROPIC_AUTH_TOKEN` and a custom
`ANTHROPIC_BASE_URL` Claude Code reports `channels feature is not currently
available` even interactively. `--resume` under `-p` was not run separately; it
takes the same code path as the other `-p` runs.

What was measured (Claude Code 2.1.284, `ctx channel --capabilities`):

| Capability | State |
| --- | --- |
| `receive_at_safe_point` | measured |
| `wake_idle_session` | measured |
| `receive_for_named_subagent` | gap |
| `human_steer` | gap |
| `human_interrupt` | gap |
| `activity_observation` | gap |

Delivery is at-most-once: a message is claimed atomically before it is sent and
never re-sent, so a crash between claim and write loses it. A reply wakes the
agent that asked when its original message was a request: a message with the
intent `question`, `approval`, `blocker` or `handoff`, or a message that is not
itself a reply and mentions the replier by name (the mention `reply` adds
automatically does not count). A reply to a reply, such as a thanks, does not
wake, so a chain ends after one answer. To ask again after an answer (for
example a re-review after a fix), call `reply` with `intent` set to `question`,
`approval`, `blocker` or `handoff`, or use `send` with a mention; a `reply`
without intent wakes nobody. In direct and group rooms, which have no mentions, any plain message counts as
a request. `send` with a mention and `reply` with an attention intent accept at most 10 messages per minute and 30
per 15 minutes per channel process and refuse beyond that; the receiving
session also has a coordination budget of 12000 characters per 5 minutes, which
short messages exhaust only late. `<channel` in message text is rewritten to
`&lt;channel` so a body cannot imitate a channel event. The limits are
spelled out in `internal/claudechannel/doc.go`. The status line may say "no MCP
server configured with that name" while delivery works.

Channels need the old `initialize` connection. Claude Code 2.1.286 first sends
`server/discover`; if the server answers with MCP 2026-07-28, Claude Code
registers no channel for that connection. The channel server therefore refuses
`server/discover` with JSON-RPC -32601, and Claude Code falls back to
`initialize` with 2025-11-25. Measured with Claude Code 2.1.286 on 2026-10-01;
a later release may behave differently. `scripts/verify-claude-channel.sh` is a
manual tmux check against a real session (not part of CI). It passes `--model
opus`, because the model in a user's settings may be one Claude Code rejects.

### Inspecting a repository before migration

```bash
ctx migrate --dry-run /path/to/repo
```

The dry run is a local inventory with document validation. It lists candidates,
skipped Markdown files with reasons, and unscanned directories such as dependencies,
embedded checkouts, and `.ghosttree` state. It requires no client or model
configuration and makes no network calls or writes. Existing migration records
and distilled rule entries are evaluated when you run `ctx migrate <repo>`.

Every Markdown file under `docs/` is a document candidate, preserved byte for byte.
Root-level files such as `CONTRIBUTING.md` remain visible exclusions; review them
and use `ctx doc import <file> --kind other` for documents you choose to preserve.
Normal migration and cleanup print the same coverage report. Cleanup remains a
separate `ctx migrate --clean <repo>` operation requiring stored source evidence.

### Marking a project-context state

Authenticated users can read and create ordinary snapshots when their exact
canonical project has no explicit access row. An explicit row overrides that
default, including a denial. Release binding always needs an explicit grant.
Run access changes against the server database while the service is stopped
or through the maintenance procedure documented in `deploy/`:

```bash
ctx person snapshot-access <person-name> \
  --project github.com/owner/repository --read --create --db /path/to/ghosttree.db
ctx person snapshot-access show <person-name> \
  --project github.com/owner/repository --db /path/to/ghosttree.db
```

Use `--release-bind` as well only for an identity that may bind release-style
names such as `v1.2.3`. To deny all snapshot access, run the write command with
the person, project, and database but omit all three capability flags.
In a configured repository, the complete user command
surface is:

```bash
ctx snapshot create <name> [-m message] [--allow-dirty] [repo]
ctx snapshot list [repo]
ctx snapshot show <name> [repo]
ctx snapshot export <name> [--domain D] [--key K] [-o file] [repo]
ctx snapshot verify <name> [repo]
ctx snapshot mirror rebuild [repo]
```

A snapshot copies the project-bound Knowledge, Ghost files and reviews,
document heads, and complete request details visible in one SQLite transaction.
It does not reconstruct context from an old Git checkout: `created_at` is the
real creation time, while the Git fields record the observed checkout. A
release-style name requires the local tag, its peeled commit to equal `HEAD`,
and normally a clean worktree.
Ordinary names such as `2026.08.31-baseline`, `1.0-draft`, and `v2.0-plan`
do not require a release tag. Tracked files under generated `.ghosttree/`
directories still count as dirty and contribute to the worktree fingerprint;
only untracked generated output is excluded.

Snapshot schema 3 binds the immutable metadata head and every ordered entry
digest into `content_digest`. A complete export can therefore detect changes to
either provenance metadata or payloads. A domain/key-filtered export is only a
projection: it verifies each included payload but does not claim to prove the
snapshot-wide digest. For remote creates, CLI and MCP observe Git locally just
before the request and the server records the source as `client-reported`; the
server does not have the checkout and cannot repeat that observation itself.

Existing schema-1 and schema-2 snapshots remain readable and exportable with
their original bytes and digests. Their historical digest covers entries, not
the metadata head. Verification reports `Full: true` for a complete export and
`HeadBound: false` for that older guarantee; full schema-3 verification reports
`HeadBound: true`. Filtered exports always report `HeadBound: false` and no
verified snapshot digest. New snapshots use schema 3.

Sealed snapshots have no update, delete, or redaction API. Treat messages and
all snapshotted context as permanent, keep secrets out, and use `show` before
`export` so large historical payloads enter a tool or model context only when
deliberately requested. The generated `.ghosttree/snapshots/INDEX.md` contains
metadata only, including the Git metadata source, schema version, and digest
scope; payloads remain in the server store. A failed mirror refresh does not
undo a committed snapshot. Its warning includes an operation ID that operators
can correlate with the server log.

### Serving the installer

`ctx serve` can hand out the `ctx` binaries itself, so a new machine needs one
command. Build the archives, put them where the server can read them, and point
the server at that directory:

```sh
make dist                                  # dist/release/ctx_<version>_<os>_<arch>.tar.gz + checksums.txt
GHOSTTREE_DIST_DIR=/srv/ghosttree-dist ctx serve ...   # or --dist-dir
```

The server then answers `GET /install.sh` and `GET /dist/<file>` (only files
listed in `checksums.txt`; nothing else in the directory is reachable). Without
the setting both routes return 404. A machine installs with

```sh
curl -fsSL https://<server>/install.sh | sh
```

The script picks the build for Linux or macOS (amd64, arm64; Windows only
inside WSL), downloads the archive and `checksums.txt`, verifies the SHA-256,
and installs `ctx` to `~/.local/bin` (`$XDG_BIN_HOME` if set), without sudo. The
server address in the script is `GHOSTTREE_PUBLIC_URL`, or else the address the
request came in on. `GHOSTTREE_DOWNLOAD_BASE` on the client replaces the
download location, for example with a GitHub release URL, since the archives
use goreleaser's layout. The installer refuses a plain-http source (except
loopback, or `GHOSTTREE_ALLOW_HTTP=1`), and the server serves `/install.sh`
only for an https address: set `GHOSTTREE_PUBLIC_URL`, or let a trusted proxy
send `X-Forwarded-Proto` and `X-Forwarded-Host`. `/dist/` has no rate limit of
its own; limit it at the reverse proxy. Keep one ctx version per directory.
Cautious readers can fetch the script, read it, and
run it afterwards.

### Interface language

The web interface speaks English by default. `GHOSTTREE_LANGUAGE=de` (or
`ctx serve --language de`) switches the whole interface, including the sign-in
and invitation pages, dates and times, to German. The language is resolved per
request; today it comes from this server setting. Text that people or agents
wrote is shown as written, and the texts meant for agents (bootstrap, MCP
descriptions, rules) stay English.

### "In short" (Kurz erklärt)

Every knowledge entry, request, pain, task for a person and room question can
carry a two to four sentence note in plain language: what it is about, why it
matters, what to decide, and a recommendation. The review cards also show the
project and where the entry comes from (request, pull request, session, code
area) above it.

Agents write the note when they create the object (`explanation` on
`context_remember`, `request_create`, `pain_record`, `human_task_create` and
`coord_send` with a question; `--explanation` on the `ctx` commands). Missing or
outdated notes are generated from the object's own text and the project data
around it (titles of the requests it names, code paths), never from session
transcripts. A note is outdated when the text it explains changed, or, if it was
generated, when `GHOSTTREE_LANGUAGE` changed. The text and the model output
pass the same secret check as human tasks.

A new object gets its note from the model when its page is opened, one call
in the background. Everything else, the whole backlog, objects that changed and
notes in another language, is written in bulk through the Message Batches API at
half the price. Opening a page never starts bulk work.

The model has its own provider: the `explain` entry of `llm.json`
(`GHOSTTREE_LLM_CONFIG`, or `~/.config/ghosttree/llm.json`), an Anthropic API key
from the console organisation that pays for it:

```json
{"explain": {"base_url": "https://api.anthropic.com", "model": "claude-haiku-5-5",
             "api_key_file": "/etc/ghosttree/explain-key"}}
```

`base_url` and `model` are the values shown; `credential` (a systemd
credential) replaces `api_key_file`, and `GHOSTTREE_EXPLAIN_KEY` replaces both.
The entry is never filled in from the top-level provider of the distiller or from
the gardener's entry: a proxy or an OpenAI key left there must not receive these
calls. Without a key, or while the key file does not exist yet, nothing is sent
and `ctx explain status` says which file it waits for.

- `GHOSTTREE_EXPLAIN=auto|off` (`--explain`) switches generation, default `auto`.
- `GHOSTTREE_EXPLAIN_MODEL` (`--explain-model`) overrides the model of the entry.
- `GHOSTTREE_EXPLAIN_DAILY_CALLS` (default 200) and `GHOSTTREE_EXPLAIN_DAILY_TOKENS`
  (default 400000) cap one UTC day, 0 means no cap; see `ctx cost` for the monthly
  and dollar caps. Server and batches share the cap. Objects that wait in an open
  batch count as calls, and at an estimate (about 2500 tokens each, at the batch
  price) against the token and dollar caps, so a cap holds before the money is
  spent. **The default of 200 calls a day applies to `--batch` too**: a backlog of
  thousands needs `GHOSTTREE_EXPLAIN_DAILY_CALLS=0` (and `..._DAILY_TOKENS=0`) in
  `server.env`, or a dollar cap instead. Batch usage is counted at 50 % of the list price.
- Administrators see calls, tokens, failures and the last days on the Organization page.

A page never waits for the model: it shows the block once it exists. The bulk
path runs from a timer, the review queue first:

    ctx explain backfill --db /var/lib/ghosttree/ghosttree.db --batch
    ctx explain backfill --db /var/lib/ghosttree/ghosttree.db --collect
    ctx explain status --db /var/lib/ghosttree/ghosttree.db

`--batch` first collects every finished batch (saving its notes with the same
cleaning and secret check, counting its tokens), then submits one batch for each
object without a current note; several batches if the 100,000 request or 256 MB
limit of the API requires it. Batch ids are kept in the database, so an object
that waits in an open batch is not sent again, and one whose entry failed (an
error, or cut off at the 4096 token output limit) is left alone for a day. A
result for an object that changed in the meantime is discarded and the object is
sent again. A batch is reserved in the database before it is uploaded, so two
runs never send the same objects; a reservation whose run died is released after
two hours, and a batch that cannot be read 48 hours after it was sent is given up
and its objects are released. `--collect` only collects. `deploy/ghosttree-explain.*` run `--batch`
hourly and start a first run when the key file appears.

## Privacy and security

Ghosttree may hold source paths, prompts, command output, and operational
history. Treat the server as private infrastructure:

- bind it to loopback or a trusted private interface;
- use TLS at a reverse proxy when traffic crosses an untrusted network;
- run the reverse proxy on the same host, or list its network in
  `--trusted-proxies`: forwarding headers are believed only from loopback and
  from those networks (see "Running behind TLS or a reverse proxy");
- keep person tokens out of repositories and logs;
- back up the SQLite database and test restores;
- review retention and access rules for your team.

The collector redacts common credential formats before upload, and document
writes reject suspected secrets instead of rewriting them. Regex redaction is a
last line of defense, not a guarantee. The current bearer token identifies who
made a change. Snapshot reads, creates, and release bindings additionally
require explicit per-project capabilities; other endpoints retain their
existing authorization model.

Security reports belong at
[security@deadweightlabs.com](mailto:security@deadweightlabs.com), not in a
public issue. See [SECURITY.md](SECURITY.md).

### Running behind TLS or a reverse proxy

`ctx serve` speaks plain HTTP. The browser session cookie, and the OIDC flow
cookie, are always `HttpOnly` and `SameSite=Lax`. They get `Secure` when any of
these holds:

1. the connection itself is TLS;
2. `--public-url` (env `GHOSTTREE_PUBLIC_URL`) is an `https://` URL, or the
   OIDC redirect URL is `https://`;
3. the request carries exactly one `X-Forwarded-Proto: https` and its TCP peer
   is a trusted proxy.

The login and the logout cookie are built the same way and always carry the
same attributes. Otherwise the cookie is not `Secure`, so a private HTTP
deployment (loopback, VPN) keeps working with no extra setting. A browser never
sends a `Secure` cookie over HTTP, so an https setup that gets this wrong shows
up as a login loop rather than a silent downgrade.

Trusted proxies are loopback (a proxy on the same host) plus whatever you list
in `--trusted-proxies` / `GHOSTTREE_TRUSTED_PROXIES`, a comma-separated list of
CIDRs or addresses. A client outside that list cannot switch `Secure` on with
a header, and its forwarding headers also make the same-origin check on form
posts fail. List only the proxy's own address, never a network that ordinary
clients share. `/0`, IPv4-mapped prefixes and zoned addresses are rejected at
startup.

Loopback is trusted by default. Anything that makes a remote client appear as a
loopback peer (`ssh -L`, `socat`, a Docker port mapping or other local
forwarder) therefore also gets its forwarding headers believed. If you run such
forwarders, add the word `none` to the list (`GHOSTTREE_TRUSTED_PROXIES=none`,
or `none,192.0.2.10`) to switch loopback trust off and trust only what you list.
With `none`, a proxy on the same host is no longer believed either, so forwarding
headers from it are ignored; enter that proxy's address explicitly in the list.

The client address used to rate-limit device logins comes from
`X-Forwarded-For`, read only when the direct peer is trusted: all header lines
are joined and read from right to left, trusted proxies are skipped, and the
first untrusted address is the client. Entries a client sent itself therefore
stay to the left and are ignored.

Recommended setup behind a TLS terminator on another host:

```bash
GHOSTTREE_PUBLIC_URL=https://ghosttree.example.com
GHOSTTREE_TRUSTED_PROXIES=192.0.2.10        # the proxy's address as seen by ctx serve
GHOSTTREE_LISTEN=192.0.2.20:8474            # private interface, not a public one
```

The proxy must forward `Host`, `X-Forwarded-Proto` and `X-Forwarded-Host`, and
must overwrite those headers rather than append to client-supplied ones:

```nginx
location / {
    proxy_pass http://192.0.2.20:8474;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Host $host;
    proxy_set_header X-Forwarded-For $remote_addr;
}
```

`X-Forwarded-For $remote_addr` overwrites whatever the client sent and is the
recommended form. With `$proxy_add_x_forwarded_for` (append) ghosttree still
picks the proxy-added entry, but only if the proxy is in `--trusted-proxies`.

`--public-url` is the simplest switch: with it every cookie is `Secure` no
matter what headers arrive, and requests whose `Origin` equals it are accepted
even if the proxy forwards no headers. It also fixes the base URL printed by the
device login. Use `--trusted-proxies` alone when one server answers under
several host names. `ctx serve --help` lists both flags.

## Roadmap

The near-term sequence is deliberately small:

1. Finish and harden the document lifecycle, including migration and recovery
   ergonomics.
2. Improve relevance and outcome measurement so retained context earns its
   place.
3. Add explicit knowledge relationships and safer duplicate consolidation.
4. Round out team administration and offer an optional managed service.

Docker images and package-manager distribution are not planned until there is
real demand for them.

## Building and testing

Ghosttree currently targets Go 1.26.

```bash
make test
make build
make build-all
```

The direct dependencies are `modernc.org/sqlite`, the official MCP Go SDK, and
`fsnotify`. Release builds set `CGO_ENABLED=0` and target Linux and macOS on
amd64 and arm64.

## Contributing

Start with [CONTRIBUTING.md](CONTRIBUTING.md). Pull requests must target `dev`,
pass the project checks, state their release impact, and accept the
[Contributor License Agreement](CLA.md). The release process is documented in
[RELEASING.md](RELEASING.md).

## Licensing

The default license is the
[PolyForm Noncommercial License 1.0.0](LICENSE). The separate
[Ghosttree Micro Business Grant](COMMUNITY-GRANT.md) permits qualifying small
organizations to use Ghosttree in ordinary commercial project work, subject to
its limits.

Larger organizations, hosted-service providers, OEM and white-label users, and
anyone who wants different terms need a commercial agreement. See
[COMMERCIAL.md](COMMERCIAL.md) or contact
[legal@deadweightlabs.com](mailto:legal@deadweightlabs.com).

Copyright © Deadweight Labs UG (haftungsbeschränkt).
