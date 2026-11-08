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
- Runbooks (`ctx runbook`, `ctx run`, `runbook_get`, `runbook_write`,
  `run_report`): named procedures for what must be done the same way every time
  (build, roll out, check) with typed parameters, preconditions, numbered steps
  with exact commands and checks, a way back, linked pitfalls and triggers. They
  live in Ghosttree, not in the repository, apply to a project, a machine, both,
  or (for the author alone) neither, and every version is kept with its content
  hash, writer, Git state and where it came from. A keyword in a prompt, a Bash
  command pattern or a touched path delivers the newest approved version to the
  session, at most two per answer and once per session. `ctx run NAME param=value`
  prints the checklist with the parameters filled in; every run is reported with
  its Git state and the runbook shows "last success on commit ...". A reported
  deviation becomes a proposal that an owner or lead must approve. The seed
  runbooks for a production rollout and SSH access are in
  `docs/runbooks/example.json` (`ctx runbook import`).

  Executable targets (like `make` targets) are a second, stricter stage, and
  **switched off** until the operator starts `ctx serve` with
  `GHOSTTREE_RUNBOOK_EXEC=1`. A target is a list of argument lists run without a
  shell; parameters are typed, limited to letters, digits and `. _ / : @ + , = -`,
  and replace a placeholder inside one argument, never a shell string. `ctx run
  NAME --target T` is a dry run that shows each argument list; only `--run`
  executes, in the foreground of the calling process, so the harness's normal
  Bash approval applies. It runs only a version that an owner or lead wrote or
  approved (a proposal or a member's version never runs) and that a person has
  allowed for this machine and this exact content hash. A changed version needs a
  new allowance. Every run is logged with version, machine, person and Git state.

  What the boundary is: the allowance for a machine is stored on the server and
  `ctx run --run` asks the server; a file on the machine allows nothing, and no
  API token, device token or agent token can create one. A person creates it, today
  as the operator with database access on the server (`ctx runbook allow --db ...`,
  which shows the diff and asks for the start of the hash at a terminal, the same
  boundary as role changes). What it is not: protection against a process that can
  open the server's database or, once the web page for allowing exists, steal a
  person's web session cookie. That page (an interactive session with CSRF and the
  diff shown with visible escapes) is not built yet; until then, allowing runs
  through the operator command. Texts of a runbook are single lines without control
  or invisible formatting characters, and the diff and the dry run show every
  non-ASCII character as an escape, so what a person approves is what runs.
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
