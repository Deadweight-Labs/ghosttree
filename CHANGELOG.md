# Changelog

Notable user-visible changes are recorded here. Ghosttree follows Semantic
Versioning, with pre-1.0 compatibility rules described in
[RELEASING.md](RELEASING.md).

## Unreleased

- Preserve agentbench failure transcripts and the append-only run journal;
  recovery retains product failures and regrading counts each run once.
  In-place regrading writes derived records to `regraded.jsonl`.
- Allow authenticated users to read and create ordinary snapshots without a
  manual project grant. Explicit denials remain effective; binding a release
  name still requires its own grant.
- Preserve schema-1/2 snapshots across upgrades and retries; verification now
  states whether the historical digest binds the metadata head.
- Include tracked generated files and submodules in snapshot Git provenance,
  preserve actual Git recheck errors, and accept ordinary dotted-date and draft
  names. The metadata index displays the Git source and digest scope.
- Reject impossible aggregate bounds in projected snapshot exports and return
  mirror warnings with a logged operation ID instead of private server paths.
- Add immutable named context snapshots spanning project Knowledge, Ghost
  files and reviews, document heads, and complete request details from one
  atomic SQLite view.
- Add authenticated REST, CLI, and bounded MCP snapshot creation and reads,
  canonical export verification, Git/release provenance, per-project grants,
  finite resource budgets, and a regenerable locked metadata mirror.
- Bind schema-3 snapshot metadata and entries into one digest, make projection
  verification explicitly partial, reject misleading release-like names, and
  label remote Git provenance as client-reported without a fictitious server
  recheck.
- Add additive snapshot-schema startup checks without historical backfill;
  existing live context and session data remain unchanged.

## 0.1.0-rc.3 — 2026-08-29

- Reject symlinked SQLite database and sidecar paths before changing file
  permissions.

## 0.1.0-rc.2 — 2026-08-29

- Keep SQLite databases and their WAL sidecars owner-readable only.

## 0.1.0-rc.1 — 2026-08-29

- Initial source-available preview.
- Keep exported transcripts and saved credentials owner-readable only.
- Reject non-local login return targets and bound HTTP server timeouts.
