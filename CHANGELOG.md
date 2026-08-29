# Changelog

Notable user-visible changes are recorded here. Ghosttree follows Semantic
Versioning, with pre-1.0 compatibility rules described in
[RELEASING.md](RELEASING.md).

## Unreleased

- Add immutable named context snapshots spanning project Knowledge, Ghost
  files and reviews, document heads, and complete request details from one
  atomic SQLite view.
- Add authenticated REST, CLI, and bounded MCP snapshot creation and reads,
  canonical export verification, Git/release provenance, per-project grants,
  finite resource budgets, and a regenerable locked metadata mirror.
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
