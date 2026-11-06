# Snapshots: marking a project-context state

Immutable, named marks of the project context: who may create them, what they contain, and how an export is verified.

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
