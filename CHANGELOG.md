# Changelog

Notable user-visible changes are recorded here. Ghosttree follows Semantic
Versioning, with pre-1.0 compatibility rules described in
[RELEASING.md](RELEASING.md).

## Unreleased

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
- Machines can belong to a person or to an organization (build servers, CI
  runners) and be shared with organizations as read or write. Machine knowledge
  stays private to the owner until each entry is shared with an organization;
  guests, strangers and former members see nothing, and withdrawing a share
  takes its entries back. Sharing a machine is done by a person in the web
  session, or by the operator with `ctx machine share --db`. Agents read with
  `machine_get` and share single entries with `machine_share_entry` or
  `share_with_orgs`.
- Projects without Git: a project can be just a name (`<org>/<name>`), created
  with `ctx project create` or in the web interface.
- Invitation links open a page at `/join/<code>`; a one-time pairing code
  connects a new machine (`ctx join --server <url> --pair XXXX-XXXX`). The
  server can serve the installer itself (`GET /install.sh`, `GET /dist/<file>`,
  with SHA-256 verification) when `GHOSTTREE_DIST_DIR` is set.
- Visibility by role behind `GHOSTTREE_ENFORCE_ACCESS` (see "Before you
  upgrade"), and private means private: a session that is not shared is read by
  its owner alone.

#### Short ways between agent and human

- Every answer about one object (issue, requirement, task, pain, idea, knowledge
  entry, work package, runbook, room thread) carries its web link and a compact
  card (title, state, next step, who); search hits carry the link only. The link
  has no token and no login: a signed-out browser signs in and returns to that
  very page (internal paths only). This holds for the MCP tools and the `ctx`
  commands that answer about exactly one object (room message, session, saved
  search and document included).
- `ctx open <REQ-n|TASK-n|PAIN-n|IDEA-n|#id|pr:n|commit:sha|preset:name values|path|search line>`
  opens the page in the browser of this machine, or prints link and card over
  ssh or without a display. The MCP tool `open_link` hands link and card to the
  agent without opening anything.
- Human tasks take `steps` (numbered one-line steps with optional links) next to
  `explanation`, shown as a checklist; without steps the answer says so
  (`ctx task add --step "text | https://link"`).
- `ctx install` sets up the `/ghost` shortcuts (`open`, `task`, `req`, `preset`)
  as a skill for Claude Code and Codex; `ctx doctor` checks them. An unchanged
  `/gt` skill from an earlier install is removed (it clashed with Graphite's `gt`).
- `ctx update [--channel stable|dev] [--pre]` replaces the binary with a signed
  release (verified with cosign); `--pre` also takes release candidates on the
  stable channel; `ctx version` also names the server's version.

#### Git-native ledger

- A GitHub App connects an organization: its repositories join the
  organization in ghosttree, and pull requests are bound by webhook
  (`ctx github sync-prs` catches up).
  A repository renamed on GitHub renames its project, with everything filed
  under it.
- Every write to knowledge, file descriptions, requests, criteria, pains, ideas,
  runbooks, room messages and threads records its Git anchor (commit, branch,
  pull request, time) and its origin (the paths and areas the session read and
  wrote). `ctx` commands send the anchor of their working directory; edits in
  the web interface are marked as coming from the web instead. Distilled
  entries carry the anchor of their source session, and the gardener measures
  staleness from an entry's anchor commit. Search filters by both: `commit:`, `pr:`, `branch:`, `since:`,
  `until:`, `area:`, plus `label:`, `is:` and free text, in the CLI, MCP and API.
- Labels, topics and saved searches (presets) for people and agents.
- File descriptions can differ per branch and carry over on merge.
- Ideas (`ctx idea`, `IDEA-n`): a quick idea needs nothing more. A larger one can
  be worked out: raw text kept verbatim, summary, a concept with requirements,
  a discussion thread, approval by a person in the web interface, then issues
  whose criteria point back at the requirements. Open decisions (`ctx decision`,
  `DEC-n`) offer variants with pros, cons, votes and images; a fixed rule says
  whether an agent may decide (reversible, local) or a person must (security,
  cost, data model, product, …), and deferred decisions return when their
  trigger comes up. A pain can motivate an idea.
- Features (`ctx feature`, `FEAT-n`): what the product does, written as it is now
  (behavior, limits, usage, the paths it lives in), every change a new version
  with who, the Git state and the idea, issues and pull request behind it. A pull
  request that touches a feature's paths gets "Feature X: Beschreibung angepasst?"
  in its checklist; agents working in those paths get the feature in short. An
  idea can name the feature it changes (`concerns FEAT-n`) and carries the
  description forward. `is:feature` and `feature:N` in the search language;
  `ctx feature export` and `ctx feature import` generate and take over
  `docs/features.md`.
- Runbooks (`ctx runbook`, `ctx run`): stored procedures with steps, checks,
  rollback and pitfalls that surface when an agent touches a matching command
  or path. Executable targets run without a shell, only after an operator allowed
  that version on that machine (`ctx runbook allow --db`) and only with
  `GHOSTTREE_RUNBOOK_EXEC=1`.
- Knowledge and runbooks can carry a project and a machine at once ("deploy
  this app on build-1"). Such an entry is seen and delivered by project role,
  like any project entry; the machine names where it applies. Knowledge that
  belongs to a machine alone stays private to its owner and is shared only
  through the machine's shares.
- Comments on issues, ideas and pains (`ctx comment`, `comment_add`,
  `comment_list`, `/api/comments`): the author edits with versions, the author
  or a lead deletes softly. Earlier versions stay visible to every reader.
- A request can name its parent and what it came from when it is created
  (`parent`, `derived_from`: an idea, a pain, another request or a thread), in
  one step with the creation.
- `GET /api/refs` resolves up to 50 references (REQ-n, PAIN-n, TASK-n, IDEA-n,
  FEAT-n, DEC-n, #n, pr:n) to title, state, summary, author, time and link, for
  hover cards; a reference one may not read answers like one that does not
  exist. `GET /api/inbox` lists open and recently done inbox items.
- Pull requests are readable: `GET /api/pulls` (state, branch, author, pages),
  `GET /api/pulls/{n}` with the work package in its review view, the feature
  checklist and what was said about the pull request, `ctx pr list|show|commits`,
  `pull_list`, `pull_get`. The server learns the commits of a pull request over
  the GitHub App permission "Pull requests: read" (webhook `pull_request`, and
  `ctx github sync-prs`), never from a push webhook or the code.
- Commits are known from two sources, merged by hash: the git anchors of the
  ledger and the commits of pull requests. `GET /api/commits` (project or branch),
  `GET /api/commits/{sha}` (what was said at that commit), `GET /api/pulls/{n}/commits`,
  `GET /api/paths/commits` (commits whose entries were written while working on a
  path), `commit_get`. Entries count and show only as far as the viewer may read them.
- Reactions on comments, ideas and issues from a fixed list (+1, -1, laugh,
  hooray, confused, heart, rocket, eyes), `PUT|DELETE|GET /api/reactions`,
  `ctx react`, `reaction_set`, `reaction_list`: members react, guests read.
- An agent can offer two to six answers with a question (`options` on `coord_send`
  with intent question or `ctx coord send --option`, on `human_task_create` for
  input and action tasks; the variants of an open decision count too). The inbox offers them as quick answers
  next to the person's own; only a person in a web session can pick one, a token
  never can.

#### Who wrote it: agents as authors

- The server records for every new request, met criterion, finished request,
  pain, idea, knowledge entry, task, comment, reaction, room message, feature
  and vote whether a person or an agent wrote it, and for an agent its harness,
  session and machine. It decides this itself: a person is only a web session,
  every token call (MCP, hooks, CLI) is an agent, and the session counts only
  when it belongs to the calling account. No tool parameter sets or overrides it;
  a token can no longer store `origin: human` on a request or knowledge entry.
  This includes `ctx` typed by hand in a terminal: it writes with a token and
  counts as an agent (without harness when no agent session runs).
- The model of a session is read from its uploaded transcript (Claude Code
  `message.model`, Codex `turn_context`), with every switch and its time; an
  entry gets the model that was active when it was written.
- The web interface shows agent entries as "Robin's Claude" with a quiet agent
  mark, in lists and on detail pages; model, machine and the session link appear
  only on the detail page, and only for viewers who may see that session (a
  private session only its owner). Search by `model:` and the statistics follow
  the same rule.
- Statistics per harness and model (entries created, criteria met, issues and
  features finished) at `GET /api/stats/actors` and on the new Models tab under
  Agents, over the projects the viewer sees in full.
- Search language: `by:agent`, `by:human`, `harness:` and `model:`
  (`model:opus` matches `claude-opus-5-5`).
- `ctx db backfill-actors --db PATH [--dry-run]` fills in the authors of
  existing entries where it can be proven and reports the numbers; everything
  else stays unknown. A person needs a web origin; a pain or idea without a
  session gets one only if that session called `pain_record`/`idea_record` (or
  the CLI) within two minutes. It runs in batches, skips entries younger than
  five minutes, and never runs at start-up; a live stamp replaces a backfilled one.

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
  `pain_*` and `human_task_*`, and the pages `/ui/pains` and `/ui/tasks`). A
  pain can be accepted (`accepted`: one lives with it on purpose, with a
  required reason); it counts as closed. Order of the states: active, mitigated,
  accepted, resolved, obsolete.
- Work packages bind requests to a branch and, by number, to a pull request
  (`ctx package`, the MCP tools `package_get`, `package_update` and
  `package_review`). Sessions on the branch, or on one cut from it, get the
  package in their briefing; the review view lists every criterion with its
  evidence and Git anchor. `criterion_reopen` takes a met or waived criterion
  back with a reason, and `ctx package import-relations` turns the old
  `branch:<project>@<branch>` relations into packages without deleting them.
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

- The qualifier search (`is:`, `label:`, `area:`, `since:`, `pr:`, `branch:` and
  more) also finds room messages and sessions (`kind:message`, `kind:session`),
  with the rights each of them has otherwise: a message only from a room you may
  read, private sessions stay private.
- Hybrid search: full text and an embedding ranking, fused by reciprocal rank,
  for knowledge, requests, file descriptions, pains, documents and (opt-in)
  sessions. It needs a local embedding server (`GHOSTTREE_EMBED_URL`); a second
  server can embed the queries (`GHOSTTREE_EMBED_QUERY_URL`). Hits found only by
  meaning are marked. `ctx embed status` and `ctx eval search` show and measure
  it.
- Sessions are searchable and addressable by a random id, with a rebuilt
  Sessions page for reading and searching.

#### Web interface

- A web interface modelled on how a code host works: the repository is the
  starting point, with its code tree and the file descriptions next to it,
  issues, pains, ideas, features, runbooks, pull requests and commits as tabs,
  each with what was said about it. A personal inbox (where sign-in lands)
  collects questions, open decisions and tasks, with the agent's suggested
  answers as buttons and recently done items on request. Comments, reactions,
  hover cards on every reference, a Markdown preview, machine pages with their
  knowledge and sharing, and the idea workshop with its decisions. The previous
  interface is gone; its addresses redirect.
- Guests see neither machine names nor branch names other than the default
  branch, and filters on branches are not offered to them.
- Every entry links its Git anchor (commit, pull request, branch, or "web"); the
  code tree follows the selected branch with what it inherits; commit and pull
  request pages list the room messages written there. Tasks and work packages
  have pages of their own (`/ui/{org}/{repo}/tasks`, `/packages`, short forms
  `/ui/t/{n}`, `/ui/r/{n}`), and the steps of a task are ticked off one by one.
- The web search understands the search language and shows date and main area
  per hit; saved searches for oneself, a project or an organisation, labels set
  in the browser, "create idea" on a pain, images on ideas and variants with a
  larger view, and runbooks written in the browser (executable targets stay
  operator-approved).
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

- Pains, ideas, tasks and comments written through MCP now carry their
  session: the server takes the session the client names in its header when the
  body has no numeric id. Before, a pain never had a session.
- Runbooks triggered by a command reach Claude Code: the pre-tool-use hook's
  matcher now includes `Bash` (it covered Read, Edit, Write and NotebookEdit
  only, so command triggers never fired). `ctx install claude` updates the
  existing hook.

- `ctx update` on the stable channel finds the release archives, which
  goreleaser names `ghosttree_<version>_<os>_<arch>.tar.gz`; it looked for
  `ctx_…` and found nothing. It restarts the collector service only when the
  service runs the binary that was just updated.
- The collector service restarts after any exit (`Restart=always`, launchd
  `KeepAlive`). `ctx watch` exits cleanly on SIGTERM, so a collector stopped by
  anything other than the service manager stayed off and sessions stopped
  reaching the server. `ctx install` replaces the older unit.

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
