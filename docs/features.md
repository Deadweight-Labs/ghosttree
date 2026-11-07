# What works today

The complete list of what Ghosttree does in the current development version. The [README](../README.md) gives the short version.

- Scoped context by repository, branch, and machine, with path-activated
  instructions.
- Full-text search across knowledge, requests, file descriptions, pains,
  documents, and captured sessions. Search can optionally be hybrid: with a local
  embedding server (`GHOSTTREE_EMBED_URL`, see `deploy/README.md`) it also finds
  entries by meaning, for the kinds you switch on (`GHOSTTREE_EMBED_KINDS`);
  without one nothing changes.
- Incremental Claude Code and Codex transcript collection with local credential
  redaction and outage replay.
- MCP tools for context lookup, corrections, request tracking, regression
  evidence, and ghost-file maintenance.
- Harness installation and diagnostics for Claude Code, Codex, and OpenCode.
- A work ledger with acceptance criteria, relationships, evidence, and
  interrupted-session handoff.
- Work packages: a branch (later a pull request) carries its requests as a
  requirements list, each request mandatory (`pflicht`) or `optional`. A session
  on the branch is briefed with the package at start, a branch cut from another
  inherits that one's package, and a reviewer reads the complete list with every
  criterion, its evidence and the Git state the evidence was written at
  (`package_get`, `package_update`, `package_review`, `ctx package`). A criterion
  whose evidence does not hold up is reopened with a reason
  (`request_record_progress` with `criterion_reopen`).
- Quarantined session distillation, review, usage measurement, recurrence
  tracking, and cost reporting.
- Versioned documents with local drafts, optimistic concurrency, byte-preserved
  UTF-8 revisions, history, diff, rename, archive, and provenance-backed import.
- Immutable named snapshots that materialize a consistent project-context
  state, bind their recorded Git provenance and entries into one digest, and
  remain independently exportable and verifiable after live context changes.
- Accounts with revocable API tokens, sign-in with OIDC or one-time codes,
  organizations, per-project roles, and invitation links with a pairing code for
  new machines (`ctx join`, `ctx login`).
- A web interface (`/ui/`) with an overview, the coordination rooms in which
  people and agents talk, review of proposed knowledge, requests and pains,
  sessions, agents, tasks for people, and organization and token administration.
- Coordination between agents and people: rooms, direct messages, threads,
  `@name` mentions, and delivery into Claude Code sessions through an opt-in
  channel.

Run `ctx` or `ctx <command>` without arguments to see the available command
