# Changelog

Notable user-visible changes are recorded here. Ghosttree follows Semantic
Versioning, with pre-1.0 compatibility rules described in
[RELEASING.md](RELEASING.md).

## Unreleased

### Fixed

- `ctx watch` no longer syncs the journals that Claude Code's Workflow tool
  writes under `subagents/workflows/<run>/journal.jsonl`. They are not
  transcripts and carry no session id, so every journal registered as session
  `journal`, the server refused it with `409 session_id_collision`, and the
  collector retried each one on every sweep. A registration refused with 409
  now also waits for the registration backoff instead of being retried every
  30 seconds.

Changes since `0.1.0-rc.3`. The very detailed notes that used to stand here,
entry by entry with their review rounds, are in the Git history of this file;
the pull request titles in `git log v0.1.0-rc.3..HEAD` name every change.

### Before you upgrade

- `ctx person add` is deprecated; use `ctx account add`. Existing person tokens
  keep working and are migrated on startup.
- Per-project roles and visibility are only enforced with
  `GHOSTTREE_ENFORCE_ACCESS=1`. Without it the server logs what it would have
  refused (`access: would deny`) and refuses nothing.
- Hybrid search is off unless `GHOSTTREE_EMBED_URL` is set. Sessions are only
  sent to the embedding server when `session` is named in
  `GHOSTTREE_EMBED_KINDS` (`all` does not include it; `all,session` does).
- Building from source needs Go 1.26.10 or newer.

### Added

#### Accounts, organizations and sharing

- Accounts with revocable, time-bound API tokens (`ctx account`), sign-in to the
  web interface with OIDC or one-time codes and login links, and `ctx login`, a
  browser-approved device login that gives each machine its own token. Browser
  pages list and revoke tokens.
- Organizations, projects and invitations (`ctx org`, `ctx project`, the
  Organizations page). Project roles `owner`, `lead`, `member` and `guest`, a
  reviewer flag, and agent roles (`ctx claude --role`). Machines, sessions and
  agent identities belong to the account that first claimed them.
- Sharing like on GitHub: a project is shared with its organization or private,
  an organization can claim a repository prefix such as `github.com/<org>/*` and
  connect GitHub to import its repositories as organization projects, and an
  invitation invites into the organization.
- Projects without Git: a project can be just a name (`<org>/<name>`), created
  with `ctx project create` or in the web interface.
- Invitation links open a page at `/join/<code>`; a one-time pairing code
  connects a new machine (`ctx join --server <url> --pair XXXX-XXXX`). The
  server can serve the installer itself (`GET /install.sh`, `GET /dist/<file>`,
  with SHA-256 verification) when `GHOSTTREE_DIST_DIR` is set.
- Visibility by role behind `GHOSTTREE_ENFORCE_ACCESS` (see "Before you
  upgrade"), and private means private: a session that is not shared is read by
  its owner alone.

#### Coordination

- Coordination rooms with delivery states, direct messages and groups, durable
  threads, file activity (`coord_touched`), `ctx coord` and the MCP tools
  `coord_peers`, `coord_status`, `coord_send`, `coord_inbox`, `coord_dm`,
  `thread_open`, `thread_read`, `thread_reply`, `thread_find`, `thread_resolve`
  and `thread_propose`.
- Delivery into running Claude Code sessions through an opt-in channel
  (`ctx claude`, or `ghclaude`, a link to `ctx` that starts Claude Code with the
  channel and needs no alias). Idle sessions wake up, busy ones receive the
  message at the next tool result. Agents can reply, start conversations and ask
  questions, approvals and blockers.
- `@name` mentions reach people and agents across accounts, `@agents` addresses
  every active agent of a project, and a message carries an authority
  (`directive` or `request`) derived from the sender's project role.
- Presence as reachability and work state, each with its origin; a loop guard
  and a report of agents that wait for each other; people can pause or
  interrupt a Claude agent from the web.

#### Knowledge, requests and review

- Pains (`PAIN-n`) record a problem without a solution, and tasks for people
  record what only a person can provide (`ctx pain`, `ctx task`, the MCP tools
  `pain_*` and `human_task_*`, and the pages `/ui/pains` and `/ui/tasks`).
- Agents can replace outdated knowledge (`supersedes`) or retire it
  (`context_retire`, `ctx knowledge retire`); relations between knowledge
  entries have a data model.
- Knowledge and requests can be filed in another project or for another machine
  (`project` and `machine` arguments).
- "In short" (Kurz erklärt): a plain-language note for every knowledge entry,
  request, pain, task and room question, written by a model with its own
  provider entry. The backlog is written in bulk through the Message Batches
  API (`ctx explain backfill --batch`).
- The gardener (`ctx garden check-knowledge`, opt-in, needs an API key) checks
  knowledge against the code, rotates through projects, consolidates
  duplicates, opens threads for contradictions, asks about regression
  protection and compares the ledger and the file descriptions with the
  repository. It only proposes; a person decides on the review page.
- Immutable context snapshots (`ctx snapshot`) with per-project access, REST,
  CLI and MCP access, export and verification.
- Bounded automatic hook context (24,000 characters per session) with a visible
  notice and `ctx doctor --only budget`; `ctx ghost archive` archives
  descriptions of confirmed-deleted files.
- Knowledge that arrives with a prompt is picked from rare words, arrives once
  per session and names the word that triggered it.

#### Search

- Hybrid search: full text and an embedding ranking, fused by reciprocal rank,
  for knowledge, requests, file descriptions, pains, documents and (opt-in)
  sessions. It needs a local embedding server (`GHOSTTREE_EMBED_URL`); a second
  server can embed the queries (`GHOSTTREE_EMBED_QUERY_URL`). Hits found only by
  meaning are marked. `ctx embed status` and `ctx eval search` show and measure
  it.
- Sessions are searchable and addressable by a random id, with a rebuilt
  Sessions page for reading and searching.

#### Web interface

- A new look and app shell, an Overview start page, a Profile page, an Agents
  page, rooms as a real chat, and redesigned pages for knowledge, review,
  requests (with editing in the browser) and administration. Guided pages for
  invitations and pairing new machines, and empty states that explain what
  to do first.
- `GHOSTTREE_LANGUAGE=de` (or `--language de`) switches the interface to German.
- Every `/ui/` page sends a strict Content-Security-Policy.

#### Operations

- `ctx db vacuum` and `ctx db check`, which handle ghosttree databases that the
  `sqlite3` tool cannot.
- `ctx redact-sessions` (with `--literals-file`) and `ctx strip-images` clean
  stored transcripts.
- `GHOSTTREE_PUBLIC_URL` and `GHOSTTREE_TRUSTED_PROXIES` for TLS and reverse
  proxies, with `Secure` cookies.
- `DELETE /api/tokens/self` revokes the token that made the request.

### Changed

- Secrets typed into a prompt are redacted by their label (`Password: …`,
  `DB_PASSWORD=…`, `api key …`) before a transcript is stored, and by `ctx
  redact-sessions` in existing data. The server also redacts on arrival.
- Stored transcripts no longer keep base64 images and files; a placeholder with
  type, size and hash stands in their place.
- The gardener runs as a real, hardened Claude Code agent by default (no Bash,
  sandboxed, allowlists) instead of one API call per entry.
- The model for "In short" notes has its own `explain` entry in `llm.json`; it
  never falls back to the provider of another job.
- Runtime writes go through a bounded queue, and large migrations and document
  secret checks use less CPU and writer time. Migration dry runs are local and
  make no model calls.
- `ctx watch` no longer costs CPU while nothing happens: a transcript that has
  not grown is left after a stat, and the state file is written compactly and
  only when it changed.
- `ctx doc new` and `ctx doc pull` no longer repeat a date that the slug already
  starts with.
- Organization names may not contain control characters.

### Fixed

- Joining from a fresh machine: the collector service is always set up,
  `ctx doctor` skips harnesses that are not installed, and a machine name that
  another account uses no longer blocks the join. Pairing pages no longer
  navigate away from the code, and one-time-code sign-in no longer ends in a 403.
- Guests no longer learn that hidden sessions exist, no longer get the member
  list of a project room, and cannot count hidden events through ids or
  cursors; organization membership alone no longer lists project names;
  snapshots honour project access.
- Coordination: agents can open question, approval, blocker and handoff waits;
  a lifted pause is announced to the session; rejected registrations of
  `ctx coord` are shown; the `@agents` brake cannot be bypassed.
- Idempotency keys of requests are per project and author.
- "In short" and the gardener no longer fail when the model's thinking used up
  the output limit; cache tokens are counted; `ctx cost` works on a database
  without distilled sessions.
- The collector never uploads to session 0 when the server withholds the
  number.
- The web interface: the project selector is readable and works everywhere,
  administration pages are no longer hidden by ad blockers, and the home page
  no longer hides the largest queue.
- `ctx garden check-knowledge --no-mcp` leaves out only the ghosttree tools.

### Security

- Go 1.26.10 (GO-2026-6809 in `crypto/x509`); released binaries use the fixed
  standard library.
- Pull request links only with a host boundary and a fixed path for coordination
  forwarding (CodeQL findings).
- The Claude channel neutralizes `<channel` inside message text, bounds `send`
  and treats plain messages as requests, never as directives.
- Observed activity is bound to the account that reported it; access to the
  coordination event stream no longer exposes the global event sequence.
- Join sessions are hardened after review: invitation pages answer every invalid
  code with the same 404 and are rate limited per client address.
- Dependency updates: MCP Go SDK 1.8.0, `modernc.org/sqlite` 1.60, `golang.org/x/sys`,
  GoReleaser action and CodeQL action.

## 0.1.0-rc.3 — 2026-08-29

- Reject symlinked SQLite database and sidecar paths before changing file
  permissions.

## 0.1.0-rc.2 — 2026-08-29

- Keep SQLite databases and their WAL sidecars owner-readable only.

## 0.1.0-rc.1 — 2026-08-29

- Initial source-available preview.
- Keep exported transcripts and saved credentials owner-readable only.
- Reject non-local login return targets and bound HTTP server timeouts.
- Add OIDC sign-in for the web UI (authorization code with PKCE, discovery via
  the issuer) configured with `--oidc-issuer`, `--oidc-client-id`,
  `--oidc-redirect-url` (or the `GHOSTTREE_OIDC_*` variables; the client secret
  is read from `GHOSTTREE_OIDC_CLIENT_SECRET` only). Accounts are created by
  one-time codes, never by open registration: an empty instance writes a
  bootstrap code to `<data dir>/bootstrap-code`, `ctx account claim-code`
  connects an existing account to its OIDC identity, and `ctx account
  login-link` issues a single-use login link for setups without an identity
  provider. Web sessions now belong to an account. The token login stays until
  an account has an OIDC identity. Session cookies are `Secure` over HTTPS.
