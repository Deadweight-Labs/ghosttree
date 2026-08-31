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

### Marking a project-context state

Snapshot access is denied until an operator grants it for the exact canonical
project. Run the write command against the server database while the service is
stopped or through the maintenance procedure documented in `deploy/`:

```bash
ctx person snapshot-access <person-name> \
  --project github.com/owner/repository --read --create --db /path/to/ghosttree.db
ctx person snapshot-access show <person-name> \
  --project github.com/owner/repository --db /path/to/ghosttree.db
```

Use `--release-bind` as well only for an identity that may bind release-style
names such as `v1.2.3`. In a configured repository, the complete user command
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

Snapshot schema 3 binds the immutable metadata head and every ordered entry
digest into `content_digest`. A complete export can therefore detect changes to
either provenance metadata or payloads. A domain/key-filtered export is only a
projection: it verifies each included payload but does not claim to prove the
snapshot-wide digest. For remote creates, CLI and MCP observe Git locally just
before the request and the server records the source as `client-reported`; the
server does not have the checkout and cannot repeat that observation itself.

Sealed snapshots have no update, delete, or redaction API. Treat messages and
all snapshotted context as permanent, keep secrets out, and use `show` before
`export` so large historical payloads enter a tool or model context only when
deliberately requested. The generated `.ghosttree/snapshots/INDEX.md` contains
metadata only; payloads remain in the server store.

## Privacy and security

Ghosttree may hold source paths, prompts, command output, and operational
history. Treat the server as private infrastructure:

- bind it to loopback or a trusted private interface;
- use TLS at a reverse proxy when traffic crosses an untrusted network;
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
