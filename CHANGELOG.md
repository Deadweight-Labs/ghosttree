# Changelog

Notable user-visible changes are recorded here. Ghosttree follows Semantic
Versioning, with pre-1.0 compatibility rules described in
[RELEASING.md](RELEASING.md).

## Unreleased

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
