# Changelog

Notable user-visible changes are recorded here. Ghosttree follows Semantic
Versioning, with pre-1.0 compatibility rules described in
[RELEASING.md](RELEASING.md).

## Unreleased

- Changed: the web interface starts on a new Overview (REQ-435, second part).
  `/ui/` and the redirect after sign-in lead to `/ui/overview`, and the brand
  link too. "Next" lists what needs a decision (knowledge to review for
  reviewers, requests addressed to you), "Agents" shows each agent as active
  (signal within 8 minutes), idle or offline (over a day) for the rooms you take
  part in, and the side column shows open requests with their criteria progress
  and what was learned this week. Everything is drawn from what the viewer may
  see; guests get only requests and knowledge, and the page is the same whether
  or not hidden projects hold data. Owners and admins of an instance without
  agents get one step instead: the `ctx login --server` command to copy, a field
  for the code from the terminal (the machine is named only after the code is
  entered, never listed), "Waiting for your machine", and the page switches by
  itself once the machine and an agent are there. The oldest account no longer
  counts as owner on its own; the first account of a fresh instance is an admin
  from the start. Without a `?project=`, members and guests get their first
  project preselected. The fonts moved to a versioned path
  (`/static/fonts/plex-1.1-2.5/`) so long caching stays safe, and the `js` class
  is set from `shell.js` in the page head so the narrow-screen menu no longer
  flashes open.
- Security: every `/ui/` page now sends a Content-Security-Policy (own origin
  only, no inline script or style, no framing), `X-Content-Type-Options: nosniff`
  and `Referrer-Policy: strict-origin`; the Overview is `no-store`. The inline
  script, the inline progress width and the inline `noscript` style moved into
  `shell.js` and `app.css`.
- Changed: the web interface has a new app shell (REQ-435, first part). A
  left navigation (Overview, Agents, Rooms, Knowledge, Requests; Administration
  for owners and admins, "My devices & tokens" for members) with a project
  selector, a search field and an account menu replaces the top bar. Guests see
  only Overview, Knowledge and Requests, and the navigation carries no counters
  and no project they have no role in. Existing pages keep their content and
  routes; `/ui/rooms` redirects to `/ui/coord`. The sign-in page is new: a
  provider button (named only when `GHOSTTREE_OIDC_NAME` or `--oidc-name` is
  set), one field for a code or pasted login link, and the person-token form
  behind "Paste a person token"; a wrong code shows the error inside the form.
  Design tokens and self-hosted IBM Plex Sans and Mono (SIL OFL, under
  `/static/fonts`) replace system fonts. `/` redirects to `/ui/`, and a
  favicon is served. All texts of the shell and sign-in page come from one
  message catalog. On an OIDC instance, a claim, bootstrap or invitation code
  typed into the code field (Enter) starts the provider sign-in, and a pasted
  login or `/join/<code>` link is understood by both buttons. Below 900 px the
  navigation folds behind a menu button.

- Added: the server can serve the `ctx` installer (REQ-434, fourth part).
  With `GHOSTTREE_DIST_DIR` (or `--dist-dir`) pointing at a directory of
  `ctx_<version>_<os>_<arch>.tar.gz` archives and `checksums.txt`, it answers
  `GET /install.sh` and `GET /dist/<file>` (listed files only; 404 when unset).
  `curl -fsSL https://<server>/install.sh | sh` verifies the SHA-256, installs
  to `~/.local/bin` without sudo, and passes arguments after `--` to
  `ctx join`. `make dist` builds the archives and checksums for linux and
  darwin on amd64 and arm64. The installer refuses plain http (except
  loopback) and an ambiguous `checksums.txt`; the server serves it only for an
  https address.

- Added: invitation page `/join/<code>` (REQ-434, first part). It shows the
  organization, project, role (member or guest) and expiry of an invitation
  link and uses nothing up by being opened; the invitation is consumed when
  the invitee signs in (identity provider or local code sign-in) or presses
  "Join" in a signed-in session, once and atomically. Every code that does not
  work (unknown, malformed, expired, used, revoked, inviter no longer owner,
  project moved, org-wide or email-bound invitation) gets the same 404 page,
  headers included; requests are limited per client address (30 per 10 minutes,
  `GHOSTTREE_TRUSTED_PROXIES` honoured), never per code. The page sends
  `Referrer-Policy: strict-origin` (not `no-referrer`, which makes browsers
  send `Origin: null` on every POST and breaks the same-origin check; the code
  in the path is still never passed on), `Cache-Control: no-store`, a
  script-free CSP and `X-Robots-Tag`, and the server logs nothing for `/join/` paths. Owners
  create project links on the organization page (member 7 days, guest 3 days
  by default); a link never grants owner or lead. Guest links need
  `GHOSTTREE_ENFORCE_ACCESS=1`, project links are accepted only in a browser
  session (the token API refuses them), the signed-in page asks to confirm the
  account name and offers "Not you? Sign out", and, when
  `GHOSTTREE_ENFORCE_ACCESS=1` is set, an org member who is not an owner sees only
  the owners, themselves and the accounts that share a project with them (a
  guest or a member without a project role: only owners and themselves), without
  other accounts' ids, in the member list (UI and API). Without enforcement
  every member sees everyone, as before, and a guest link is not valid. A project
  lead (and any owner, or anyone without enforcement) still sees all org members
  in the role forms of their project and can promote role-less members. The
  invitation list shows project and role. `/ui/orgs/accept` hands project codes
  over to the join page. At startup the server warns when
  `GHOSTTREE_PUBLIC_URL` is set but `GHOSTTREE_TRUSTED_PROXIES` is empty.
- Added: join session and pairing code (REQ-434, second part), so that
  installation and sign-in run in parallel. Opening a valid invitation page
  `/join/<code>` creates a join session bound to the invitation and the
  browser (HttpOnly `gt_join` cookie, 15 minutes, never in a URL) without using
  the invitation up, and shows the install command with a one-time pairing
  code `XXXX-XXXX`; reloading shows the same code. Invalid invitations create
  no session and stay byte-identical 404s. The installer calls
  `POST /api/join/claim` (no token) with the pairing code and a machine name
  and waits; after sign-in and joining, the session is bound to the account and
  only that account can approve "<machine> wants to connect" (interactive
  session, CSRF, same origin, account confirmation, bound to the shown
  request, with a "same network as this browser" line). Either order works.
  Preferred path (RFC 8252 with PKCE): the claim carries `code_challenge`,
  `loopback_port` and `state`; after approval the browser is redirected to
  `http://127.0.0.1:<port>/callback?code=..&state=..` and `POST /api/join/token`
  exchanges the one-use code for the token only with the matching
  `code_verifier`, so a thief of the pairing code gets nothing. Fallback
  (client without a loopback listener): the claim answer carries a
  confirmation code, the approval page asks for it, and the token comes through
  the device flow. A second claim discards the session. Unknown, expired,
  claimed, denied and malformed codes get the same answer; wrong codes are
  limited per /64 (and per /48 on IPv6) and logged when they pile up. Join
  device flows have no user code, so `/ui/device` cannot see or decide them.
  A server restart drops sessions; the page then says "Setup was interrupted".
  The command is shown only over https or loopback.
- Fixed: signing in with a one-time code, login link, invitation or bootstrap
  code in a browser ended with 403. The code pages sent
  `Referrer-Policy: no-referrer`, so the browser posted the form with
  `Origin: null`, which the
  same-origin check rightly rejects. They now send `strict-origin`: the browser
  sends the real origin, the Referer carries no path (the code is in the link's query), and
  `Origin: null` and foreign origins are still refused.
- Fixed: agents can now open question, approval, blocker and handoff waits.
  `coord_send` and `ctx coord send --intent` take an optional `intent`
  (question, approval, blocker, handoff, ack; the first four need a mention),
  the MCP schema describes the values (no enum, so "Question" is normalised
  rather than refused by the SDK), and the agent send route rejects `standing`,
  `attention` and unknown intents. Before, only the web UI could create these,
  so wait-cycle detection and attention never saw an agent-made wait.
- Fixed: after a pause or interruption is lifted, the Claude channel tells the
  session once ("Pause aufgehoben durch <person> (control #N) - du kannst
  weiterarbeiten", meta `event`, `control_id`, `resumed_by`). The notification
  wakes an idle session, so the agent continues its original task on its own
  instead of believing it is still paused. A restarted channel does not repeat
  it. `GET /api/agent-control` also returns the latest lifted control as
  `resumed` when none is active.
- Fixed: `ctx coord send` (and the other room commands) no longer swallow a
  rejected registration. The CLI identity `cli:<machine>` belongs to one
  project room; from a second repository the server's message ("already
  registered in another room") is shown with a hint, coded server errors
  (machine_bound, invalid_external_id, ...) are shown too, and
  `--agent-name <name>` posts as `ctx:<machine>:<name>`, which also works with
  machine-bound device tokens.
- Added: data model for relations between knowledge entries (REQ-157, first
  step). New tables `knowledge_relations` (kinds `supersedes` and `sibling`,
  states proposed/active/rejected/revoked, nothing is ever deleted),
  `knowledge_groups` (one volatility per sibling group) and the append-only
  `knowledge_relation_events`, plus store functions to add, approve, revoke and
  list relations and to set a group's volatility. Existing `superseded_by`
  values are copied into active `supersedes` edges when the database is
  opened (idempotent, one transaction, a revoked edge stays revoked, legacy
  cycles are skipped and logged). Nothing
  reads the new tables yet: delivery, search and the `superseded_by` patch
  behave as before. The `superseded_by` column stays in place.
- Added: configurable Secure cookies behind TLS and reverse proxies (REQ-236).
  `--public-url` / `GHOSTTREE_PUBLIC_URL` (an https URL makes every cookie
  Secure and its origin is accepted for same-origin checks) and
  `--trusted-proxies` / `GHOSTTREE_TRUSTED_PROXIES` (CIDRs or addresses whose
  `X-Forwarded-Proto`/`-Host` are believed; previously only loopback was).
  Loopback stays trusted, no other peer is by default, so plain private HTTP
  keeps working unchanged. Login and logout session cookies are now built by
  one function and carry identical attributes. The device-login base URL and
  rate-limit client address use the same trust set. README section "Running
  behind TLS or a reverse proxy".
- Added: loop guard for the Claude channel (REQ-360). A message counts as
  low content only if it brings no new word compared to the last 6 messages of
  the room (digits ignored, so a counter like "step 14 of 20" is no content;
  thanks and acknowledgements do not count as words) or repeats the sender's
  previous message. Each change of sender among such messages is a round. Any
  new word, commit hash, file path, code block, link, REQ/AC/PR reference or a
  message from a human resets the count. From 3 rounds the wake notification
  carries `loop_streak=N` in its meta, only for the agent it wakes. This
  release only observes: wakes are unchanged and the channel logs "would hold"
  at 5. `GHOSTTREE_LOOP_GUARD=enforce` (off by default) withholds the wake at
  5, leaves the message stored and readable by pull, and posts one ordinary
  ack notice into the room. Padding with fresh words evades the guard on
  purpose (false holds are worse); the send limit stays the backstop. Known
  limitation, to resolve before enforce: pure measurement exchanges ("p50
  12.4ms" then "11.9ms") reach hold level, because numbers are not content.
- Mutual waiting is reported instead of managed silently. Open questions,
  approvals and blockers between agents form a wait graph per room (a handoff
  is not a wait; threads restricted to some participants are left out). A cycle
  (A waits on B and B on A, or A, B, C in a ring) shows up in `coord_peers` and
  the web room as "gegenseitiges Warten: A ↔ B (seit ...)", in the peers API as
  `presence.cycle` on each member next to `waiting_peer`, and once per cycle as
  a room message from `system:wait-cycle` that mentions the members and goes
  through the normal wake rule. The message names nobody in its text, so a guest
  who can read the room does not learn who waits. A cycle that dissolves and
  forms again notifies again; a standing cycle never repeats. Every wait has a
  review date (the item's own expiry, otherwise 30 minutes after it was asked,
  marked as derived) and is flagged overdue after it; nothing is closed or
  answered automatically. An item with its own expiry never turns overdue: once
  expired it is no longer a current wait and drops out of the graph. The note is
  sent at most once per 10 minutes for the same set of agents (a held-back note
  is sent at the next change in the room once the window has passed and the cycle
  is still active; other cycles are not affected), a cycle that only shrinks does not notify
  again, and one that gains a member does. The prefix `system:` is now reserved:
  agents cannot register or send under it, and system notes show as `(system)`
  in message headers. Agents that already carry the prefix in an old database
  are kept but can no longer send.
  Threads without a home room deliberately do not take part in wait cycles,
  consistent with presence, which also reads waits per room only.
- Presence is now two separate fields with an origin. `coord_peers`, the peers
  API (`presence` on each peer) and the participant list in the web show
  reachability (`connected`, `unknown`, `ended`) and work state (`working`,
  `waiting_user`, `waiting_peer`, `blocked`, `paused`, `unknown`), each with
  origin (`observed`, `self_reported`, `derived`), time and age. Nothing claims
  more than was seen: silence is `unknown`, never idle or ended. `connected`
  needs a channel poll within 90 s; the channel (`ctx channel`) now reports its
  polling through `POST /api/coord/heartbeat`, written at most once per 30 s
  per agent and answered the same whether it wrote or not. `working` comes from
  a session the collector saw tool calls for in the last 2 minutes (only in
  project rooms, only for activity of that project, and only for an agent that
  reported its session id: `ctx claude` now starts Claude Code with
  `--session-id`, and the agent reports it at registration; there is no suffix
  matching on agent ids), `paused` only for an effective pause (hook ack and
  transcript proof; an acknowledged pause stays `unknown`),
  `waiting_peer`/`waiting_user` from an open question or approval the agent
  sent in that room, `blocked` from an open blocker. Known gaps, printed with
  the peer list: `ended` is never produced (no session-end signal exists),
  there is no self-report channel, Bash-only work is invisible, agents started
  without the launcher or resumed with `--resume` stay `unknown`, and codex
  agents have little to observe.
- Observed activity is bound to the account that reported it.
  `POST /api/activity` and `GET /api/activity/session` require that the caller
  has an uploaded session with that id (a bare session id used to pass for
  everyone, and a foreign row of another harness with the same id no longer
  matters). `path_activity` gets `account_id` (old rows stay 0, the instance
  owner; the table is rebuilt so a foreign row cannot shadow yours) and
  presence reads only the agent's own account. A client `at` more than 30 s
  ahead or 5 min behind server time is replaced by server time. While access
  enforcement is on, `GET /api/activity/path` shows ordinary members the agent
  that reported a session instead of its id, and hides the checkout path (the
  owner and project leads see both). This masking is cosmetic: members can
  read session ids through `/api/sessions`, and the account binding above is
  the actual protection. The asker's own session is left out by its registered
  id. The
  collector now logs a refused activity upload (at most once a minute). Agents
  without the launcher register the harness session id they know.
- Fixed: `ctx doc new` and `ctx doc pull` no longer prepend the creation date
  to the worktree filename when the slug already starts with a `YYYY-MM-DD`
  date (it produced `2026-10-02-2026-10-02-x.md`, sometimes with two
  different dates because the prefix is the UTC creation date). Existing
  files with a doubled prefix are not renamed and keep working: `ctx doc pull`
  now reuses the path already recorded for a document. To tidy one, rename
  the file under `.ghosttree/edit/` and update its `path` in
  `.ghosttree/edit/.state.json`; the server only stores the slug.

- A plain `ack` message from an agent (not a reply, no question, approval,
  blocker or handoff) in a direct or group room no longer wakes channel
  agents; it stays readable by pull. Acks that reply to a request, human acks
  and acks in project or machine rooms behave as before.

- Agents now see who a human sender is. The messages API has an optional
  `sender_display_name` (the account name, read live, human authors only,
  withheld from guests of a project room while access enforcement is on); the
  channel meta has `sender_name`; `coord_inbox`, `coord_dm_read` and thread
  reads show `Robin (person:1, human)` instead of `person:1 (human)`
  (`coord_dm_read` and thread posts had no human tag before, and thread posts
  are now header-safe too). The name is a label the person chose, not proof of
  identity: the sender id stays the identity, and the channel instructions and
  the MCP legend say so. Names are normalised (NFKC, invisible characters and
  Hangul fillers removed, only letters, digits, spaces and `. _ - '`, at most
  64 characters). New accounts are compared ignoring case and look-alike forms:
  an invited account whose name collides gets a `-2` style suffix, and
  `ctx account add` or `ctx person add` refuse it. Latin and Cyrillic letters
  inside one name, and look-alikes made with a legitimate combining mark, are
  still not caught. Names are normalised to a fixpoint (at most two combining
  marks per letter, overlay marks dropped) and the web shows the same
  normalised string. Guest masking applies to project rooms; in DMs and
  private groups members see each other as before. A guest sees sender ids instead of account names and
  agent owner names in the coordination page, in standing instructions (the sender id) and
  thread authors (hidden), and only their own row in the project role table of the
  organization page. Guests also no longer receive `author_principal_id` (the
  owner account of an agent) on messages or attention items. `ctx person add` reports the stored name.
- A person can pause or interrupt a Claude agent that was started with
  `ctx claude`, from the participant list of the coordination page. Allowed
  for project role lead or above and for the owner of the agent's account,
  from an interactive web session only (`POST /api/agent-control` answers
  `web_session_required` to every token). The state is derived from evidence:
  `requested`, `acknowledged` (the new `ctx hook pause-gate` PreToolUse hook
  blocked a call and the channel reported it), `effective` (that call is also
  stopped in the transcript, `hook_stopped_continuation`), `resumed`. The page
  says "pausiert" only for `effective`. `ctx install claude` adds the gate as a
  second PreToolUse entry with an empty matcher; it reads one local flag file
  and calls no server. An interrupt is a pause: a running tool call is not
  aborted and nothing stops while the model answers without a tool call
  (`human_interrupt` stays a named gap). Codex has no pause (`human_pause` is a
  named gap there). If `ctx channel` is not running the control stays
  `requested`. An agent without a person account is refused with a named gap,
  not left in `requested`. A pause can be lifted by the person who set it or by
  someone with at least their project rank; the owner of the agent's account
  as a mere member cannot lift a lead's pause. The hook entry carries
  `timeout 5`, and `ctx doctor` reports the gate as runnable and, until a pause
  has blocked a call, as unverified.
- Guests no longer get the member list of a project room through the
  recipient list. A guest may still mention: members of the room get the
  message (as a request, never a directive), and mentions of anyone outside the
  room are dropped silently with the same response. What a guest reads back of
  its own posts (mentions endpoint, "Erwähnt" in the web views, standing
  targets) is exactly what it typed, never the delivered subset, and outgoing
  attention items, attention and delivery events and the delivery summary are not shown to guests, so nothing tells members from
  non-members. Members and owners still see the real deliveries. The room page
  now opens for guests, without the agent list. Both gates follow
  `GHOSTTREE_ENFORCE_ACCESS` (log mode: old behaviour plus "would deny"). An
  unknown mention recipient of a member is a 400 with a fixed message instead
  of a 500 that echoed the probed id, in every mode. A legacy thread restricted
  to a list of readers now also needs the project role.
- The browser event stream (`/ui/coord/events`) no longer exposes the global
  event sequence: the SSE `id:`, the resync cursor and the page's initial cursor
  are AES-GCM sealed, opaque and different on every send, for every viewer, and
  the event payload carries no number. Gaps between a guest's own posts could
  otherwise count events hidden from it. An invalid, manipulated or foreign
  cursor yields a resync instead of an error. Read events are hidden from guests
  in project rooms like delivery events.
- New attention items get a random positive 63-bit id instead of the next
  row number (existing items keep theirs). A counter showed a guest which of
  its mentions reached a member: that mention created one more item, and the id
  of the guest's own item jumped. Attention lists are ordered by `created_at`,
  `message_id`, `id`; nothing relies on ids being monotonic.
- The coordination event log keeps events for ten minutes instead of the
  last 512. A count was observable: a guest could toggle read marks and count
  the steps until its cursor expired, 512 or 511 depending on whether a
  hidden event was added. Pruning now depends on time only (an insert trigger
  deletes older events); a cursor carries its own time and resyncs when it is
  older than 9 minutes, not after a number of events. A hard cap of 100000 events remains as
  an emergency brake.
- Project rooms of the coordination layer need a project role. Registering an
  agent into `project:<remote>` takes at least the guest role (org owners as
  before); org membership alone no longer joins. `GET /api/coord/rooms` drops
  project rooms for accounts without a role and omits the member list for
  guests, so a membership left behind after a role or org removal shows
  nothing. It is filtered on read, so the membership returns with the role.
  Both follow `GHOSTTREE_ENFORCE_ACCESS`: log mode keeps the old behaviour and
  logs "would deny". A known project now answers `not_found` for non-members
  and members without a role alike, and `POST /api/projects/claim` no longer
  returns id and name to org members without a role.
- Coordination messages carry an authority for their recipient: `directive` or
  `request`. One function, `store.AuthorityFor`, decides: a message is a
  directive only when the sender's rank in the recipient's project is higher
  than the recipient's and the sender is server-verified (a human from an
  interactive browser session, or a registered agent of the posting account,
  with its effective role). Equal rank, no role, an unverified sender, the
  machine room and agents without a project always give `request`. DMs and
  groups are judged in the recipient's project. The value is computed when the
  message is read, from current roles, so a demotion takes effect at once; it
  is never stored, and role or authority fields sent by a client are dropped.
  The channel adds `sender_role`, `recipient_role` and `authority` to the
  notification meta (empty values are omitted), `coord_inbox` and
  `coord_dm_read` print the same tag with a short legend, and the web room
  shows the sender's role next to each message. The channel instructions now
  explain directive versus request instead of calling every event a request.
  A directive from an agent is scoped: carry it out within the existing task and
  permissions and confirm destructive, irreversible or outward-facing steps with
  a human; a prompt injection at a lead agent can continue as a directive, and
  that confirmation is the mitigation. A directive also needs the recipient's
  account to hold a role in the project. `human` posts stored before the new
  rule (any post without an agent id, including CLI and bearer posts) stay
  unverified: the first start records the highest message id and only later
  human posts count. In `coord_inbox` and `coord_dm_read` every line after the
  first line of a body is indented, so a body cannot fake a message header, and
  agent ids are limited to letters, digits and `: . _ - /` (160 characters).
- Add visibility by role, behind `GHOSTTREE_ENFORCE_ACCESS`. One place,
  `ProjectAccess`, decides for (account, project, resource, action) from
  `Store.ProjectRole`; the API (and with it MCP and the hooks, which use the
  API), the web pages and the project room of coordination all go through it.
  Project knowledge, requests, documents and ghost descriptions are readable by
  members (a guest reads only knowledge with confidence `trusted` or
  `verified`, requests, documents and ghosts); members create and change their
  own, owners and leads change everything, and only `can_review`, lead or owner
  set or withdraw `confidence=verified`. The author of a knowledge entry comes
  from the token and cannot be patched; the confirmer is the account that
  verified it. Machine-axis knowledge is visible only to the account that owns
  the machine, whatever its project role. Session metadata is visible to
  members; a transcript (and its raw export and search snippets) to its owner
  and to project owners and leads, plus members when the owner shares it with
  the new `ctx session share <id>` (`--off` withdraws). The project room reads
  and writes by role (a guest reads and writes, peers are shown from member),
  direct and group rooms stay a matter of room membership only. Without a role
  in the project the object does not exist for the caller (404); with one but
  without the right it is 403. The bootstrap, relevant knowledge, interrupted
  work, ghost delivery and path activity answer a caller without access with
  what they may see (global knowledge) instead of an error, because hooks ask
  in every repository. Without `GHOSTTREE_ENFORCE_ACCESS=1` nothing is refused:
  the server logs `access: would deny` (account, project, resource, action,
  reason; no content; once a minute per combination) so you can see what would
  break before turning it on. Every route of the API and of the web UI is
  classified in a table (`accessRoutes`, `webRoutes`); registering an
  unclassified route aborts at startup, and tests fail if the table and the
  registered routes differ or if a project route answers without asking
  `ProjectAccess`. The roles of an account are loaded with one query per request.
  `public_only=1` on threads only selects unrestricted threads and never loosens
  the project role. In a remote nobody owns yet, the author sees and changes
  their own entries and the instance admin sees everything; nothing else is
  visible. Agents (bootstrap, relevant knowledge, ghost hook, search) are only
  handed content whose author is currently at least `member` of the project
  (ghosts: has write access); what strangers stored before a claim stays stored
  and visible to the owner but is not delivered until a reviewer, lead or owner
  sets the knowledge entry to `verified`. Content with no author (operator
  paths) is delivered.

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
  with who, what and via web or cli-db). Roles and memberships are changed by
  a person in the browser: the organizations page has a role select per project
  (with the reviewer checkbox; the select shows the current role, an account
  without a role shows a placeholder) and the organization, invitation and
  project-move forms. `GET /api/projects/{id}/members` and `ctx project roles
  <remote>` read them. Bearer tokens (CLI, collectors, agents) cannot change
  them: `PUT` and `DELETE /api/projects/{id}/members/{account}`,
  `PUT /api/orgs/{org}/members/{account}`, removing another member,
  `POST /api/orgs/{org}/invitations` and `POST /api/projects/move` answer `403
  web_session_required`, with or without `agent_external_id`, because a machine
  token sits in the config of every machine an agent runs on. Leaving an
  organization yourself, accepting an invitation, claiming a project, renaming
  an organization, revoking an invitation and choosing a default organization
  stay on the API: none of them gives anyone more power over other accounts.
  `ctx project role set|remove`, `ctx org members set-role|remove`, `ctx org
  invite` and `ctx project move` print where to go (the web UI link). The
  operator on the server host can run them with `--db <path>` (direct database
  access; it acts as the oldest owner of the organization, and role changes are
  logged with `via=cli-db`). A web session started by pasting a token is
  read-only: it shows no administration forms and the server answers them with
  403, because that token sits in the config of every machine an agent runs on.
  This covers project and organization roles, removing members, invitations,
  moving projects, approving devices and revoking other accounts' tokens.
  Sessions from OIDC, a login link or a bootstrap, claim or invitation code are
  interactive; without OIDC the operator creates one with `ctx account
  login-link <name> --db <path>`. Coordination posts are marked `human` only
  when they come from such an interactive browser session; a bearer token or a
  pasted-token session posts as `agent` with the account as sender, and the
  channel's `sender_kind` follows. Roles are not enforced anywhere yet, so nobody
  sees or may do less than before. Existing instances get the two tables and a
  `role` column on `coord_agents` (default `member`) on startup; existing
  organization members start without a stored role.
- Add agent roles. `ctx claude --role lead|member|guest` (default `member`)
  passes the requested role to `ctx channel` and `ctx mcp` in
  `GHOSTTREE_AGENT_ROLE`, and both send it when they register. The effective
  role is computed live on the server as the lower of the requested role and the
  rank of the agent's account in the project, never above `lead`; an account
  without a role gives `guest`, and demoting an account demotes its agents at
  once. A request above the account's rank is capped silently rather than
  refused (the spec says `403 role exceeds ceiling`; that would block every
  default agent before visibility is enforced); `coord_peers` shows
  "requested lead, effective member" when they differ. Agents never hold
  `owner`. `coord_peers`, the
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
