# Changelog

Notable user-visible changes are recorded here. Ghosttree follows Semantic
Versioning, with pre-1.0 compatibility rules described in
[RELEASING.md](RELEASING.md).

## Unreleased

- Add organizations, projects and invitations. An organization has members
  (roles `owner` and `member`; an account can belong to several) and owns
  projects; every project (a normalized remote such as
  `github.com/owner/repo`) belongs to exactly one organization. A write for a
  remote nobody owns yet assigns it to the writer's default organization, or to
  their only one; with several organizations and no default the write is
  refused with `409 project_unclaimed` and the choices rather than guessed, and
  an account without any organization is not affected. An owner invites by
  single-use code (kept only as a hash, seven days by default, 30 at most, at
  most 100 pending per organization). Redeeming it during OIDC sign-in creates
  the account and the membership; an invitation without an email works by the
  code alone, one with an email also requires the identity provider to report
  that same address as verified, otherwise nothing is created and the code stays
  valid. Wrong codes lock the signing-in identity for ten minutes after five
  attempts. Existing instances get a `Default` organization (slug
  `default`; rename it with `ctx org rename`) on startup with the first account as its owner and all known
  projects in it; the migration only reads the small tables (never session
  chunks) and adds three empty tables, so it takes milliseconds on a large
  database. A new instance gets a `default` organization with its bootstrap
  account. Organizations do not change who can see what yet.
- Add roles per project. Within a project an account is `owner`, `lead`,
  `member` or `guest` (in that order), and `can_review` marks a reviewer; the
  flag is not a rank. An organization owner is implicitly owner of every project
  of the organization, derived on every lookup and never stored; organization
  membership alone gives no role. Owners grant any role; leads grant `member` or
  `guest` (and the reviewer flag) to accounts below lead; members and guests
  grant nothing. Nobody raises their own role, and the last owner of a project
  cannot be demoted or removed. Leaving an organization or moving a project
  drops the stored roles. Every change appends to `role_events` (append-only,
  with who, what and via api, cli or web). `GET /api/projects/{id}/members`,
  `PUT` and `DELETE .../members/{account}`, `ctx project roles <remote>` and
  `ctx project role set <remote> <account> <role> [--review]` (also `remove`)
  and a role select per project on the organizations page read and change them;
  requests marked as coming from an agent are refused, and the CLI refuses to
  run inside an agent session. Roles are not enforced anywhere yet, so nobody
  sees or may do less than before. Existing instances get the two tables and a
  `role` column on `coord_agents` (default `member`) on startup; existing
  organization members start without a stored role.
- Add agent roles. `ctx claude --role lead|member|guest` (default `member`)
  passes the requested role to `ctx channel` and `ctx mcp` in
  `GHOSTTREE_AGENT_ROLE`, and both send it when they register. The effective
  role is computed live on the server as the lower of the requested role and the
  rank of the agent's account in the project, never above `lead`; an account
  without a role gives `guest`, and demoting an account demotes its agents at
  once. Agents never hold `owner` and never grant roles. `coord_peers`, the
  participant list of the coordination page (project rooms) and
  `GET /api/coord/agents` show the effective role and the reviewer flag.
- Add the organization API and CLI. `ctx org list|create|members|invite|
  invitations|accept|default` and `ctx project list|claim|move` talk to the
  server with your token (`/api/orgs`, `/api/projects`, `/api/invitations/accept`).
  Only an instance admin creates organizations; owners invite, change roles
  and remove members (the last owner stays) and move a project between
  organizations they own; members only list. Non-members see an organization
  as nonexistent. Bodies of these routes are capped at 16 KiB, and five wrong
  codes in ten minutes lock `accept` for that account. Sessions, knowledge,
  requests and documents written for an unknown project assign it as described
  above (`409 project_unclaimed` lists the organizations).
- Keep projects from being claimed out from under their owners: `ctx project
  claim` and its API now need an owner of the target organization, and a write
  into a project that belongs to another organization is refused with `409
  project_claimed`, also for accounts without any organization. Writes by members
  of the project's organization, such as the collector of the first account, are
  unchanged. Admins hand a squatted project back with `ctx project move --force
  <remote> --org <org> --db <path>` (database access, recorded in `org_events`).
  `ctx org rename <org> <name> [--slug S]` renames an organization (owner).
  Writing for a project nobody owns yet claims it only for an owner of the
  writer's default organization; a plain member's write goes through but leaves
  the project unclaimed, and the first owner of any organization who then writes
  there or claims it wins it.
- Add an Organizations page (`/ui/orgs`) with members, invitations and
  projects: owners create invitations (the code is shown once), change roles,
  remove members and move projects; members see the lists and can leave or join
  with a code. All forms are CSRF protected and size limited. An invitation link
  (`/ui/login/code?code=...`) leads to the identity provider on OIDC instances;
  without OIDC it creates the account from the code and a chosen name (an
  invitation bound to an email needs OIDC, since nothing else verifies the
  address).
- Add ownership of machines, sessions and agent identities. Machine names are
  unique across the instance and belong to the account that first claims them
  (`ctx login` or the first upload); a second account gets `409
  machine_name_taken`, and a token bound to a machine can only write under that
  machine (`403`). Every uploaded session is stamped with the account of the
  token, and re-uploading a session id that belongs to another account or
  machine now returns `409 session_id_collision` instead of overwriting it; the
  same account and machine stays idempotent. Agent identities belong to the
  registering account and cannot be taken over. Existing machines, sessions and
  agents belong to the first account (`person:1`) without rewriting the
  sessions table. Sessions, machines (`GET /api/machines`) and peers show their
  `owner`, and `?owner=me` filters sessions and machines. Legacy tokens keep
  working unchanged. Known gap: a legacy token (also one of a test person)
  still claims a free machine name on its first upload, because the existing
  collector uploads for new hostnames without a login; an admin frees or
  reassigns such a name with `ctx machine release <name>` and `ctx machine
  transfer <name> <account>` (database access, like `ctx account`).
- Add browser pages for the device login and for tokens: `/ui/device` approves a
  `ctx login` (signed in, CSRF protected, wrong codes lock the account briefly)
  and `/ui/account/tokens` lists your tokens with label, machine, kind
  (legacy, device, manual), creation date and expiry and revokes them with
  immediate effect. Admins see and revoke every token.
- Add `ctx login`, a device login (RFC 8628 style) against the ghosttree server:
  the CLI shows a URL and a short user code, you approve it in the browser, and
  the machine receives its own API token, bound to its name and stored in the
  client config. Codes are short-lived, single use and kept only as hashes in
  server memory; unauthenticated starts are capped per sender and cannot crowd
  out others. A new login replaces the earlier device token of the same machine.
- Add accounts with revocable, time-bound API tokens; each token belongs to an
  account. Existing person-based tokens are migrated to legacy tokens on startup
  and function unchanged. Introduce `ctx account` (add, list, tokens, token
  create, token revoke) CLI commands; `ctx person add` is deprecated. End web
  sessions and coordination event streams when their token is revoked or the
  account is deactivated.
- Add coordination rooms with delivery states (`stored < fetched < injected <
  acked`) and monotonic read cursors, DMs and groups with principal isolation,
  per-principal unread, and message expiry. Introduce `coord_peers`,
  `coord_send`, `coord_inbox` MCP tools, `ctx coord` CLI, durable threads as
  a first-class domain, file activity tracking, and a web workspace at
  `/ui/coord`.
- Deliver coordination messages and agent mentions into Claude Code sessions
  through the ghosttree channel via `ctx claude`. Wake idle sessions and receive
  at safe points with atomic, at-most-once delivery. The channel is a Claude
  Code research-preview feature requiring the legacy `initialize` connection
  and `--dangerously-load-development-channels` flag.
- Wake a channel agent when its request is answered: a reply to a question-type
  message, or to a message that mentioned the replier, now delivers instead of
  waiting for polling. Replies to replies stay quiet.
- Let the channel `reply` tool carry an optional `intent` so a reply to a reply
  can ask for something again (such as a re-review) and wake the other agent;
  such replies count against the `send` limit, plain replies still wake nobody.
- Add a `send` tool to the Claude channel server so a channel agent can start
  conversations (text, mention, room, intent) without a separate `ctx mcp`.
- Tell Claude in the channel instructions that `<channel>` events are genuine
  requests from the coordination room, and add `sender_kind` (human or agent)
  to their meta.
- Bound channel `send` (10 mentions per minute, 30 per 15 minutes), treat plain
  direct and group messages as requests that a reply wakes, report duplicate
  sends, and neutralize `<channel` tags inside message text.
- Serialize runtime writes through a bounded queue while keeping reads on an
  independent connection pool. Batch pending session chunks, report retryable
  saturation errors, expose writer metrics, and drain accepted writes on shutdown.
- Reduce CPU spent checking document bodies for known secret formats while
  preserving detection rules and reported line numbers.
- Reduce writer time spent starting large migrations by inserting artifact sets
  together, with bounded temporary encoding and byte-preserving fallback.
- Make migration dry runs local and free of model calls. Report selected files,
  skipped Markdown with reasons, and unscanned repository boundaries before
  migration or cleanup.
- Bound automatic hook context to 24,000 Unicode characters per session, with
  a visible cutoff notice and local output accounting in `ctx doctor`.
- Add explicit archival of confirmed-deleted file descriptions, preserving
  their full history and preventing stale confirmations from removing newer
  descriptions. Doctor now names the repair command.
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
