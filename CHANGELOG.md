# Changelog

Notable user-visible changes are recorded here. Ghosttree follows Semantic
Versioning, with pre-1.0 compatibility rules described in
[RELEASING.md](RELEASING.md).

## Unreleased

Changes since `0.1.0-rc.3`. The very detailed notes that used to stand here,
entry by entry with their review rounds, are in the Git history of this file;
the pull request titles in `git log v0.1.0-rc.3..HEAD` name every change.

### Before you upgrade

- Run `ctx install` again on every machine after upgrading: it adds the
  SubagentStart hook and rewrites the rule section in `CLAUDE.md` and
  `AGENTS.md`, which now names the twelve core tools.

- Existing instances move to owners with `ctx db migrate-owners --db <path>`
  (first `--dry-run`, then `--apply --backup <file>` with the server stopped;
  `--apply` refuses while another process has the database open, and the
  backup is the file as it was, taken before anything opens it).
  It keeps what a connected organization's prefix covers in that
  organization, gives everything else to the person who used it first,
  archives throwaway remotes, and refuses (changing nothing) if anyone but a
  service would lose access to a project. Whoever wrote anything under a
  remote counts (sessions, knowledge, requests, documents and the other
  entries); a remote several people wrote to stays where it is and is listed
  as contested until the plan assigns it with `to`. Sessions without an
  account go to the instance owner. A JSON plan (`--plan`) names the
  exceptions by the ids projects have before the migration; running it again
  with the same plan changes nothing.
- A GitHub owner verified by GitHub has precedence over an unverified
  namespace of the same name: linking a login (a person's namespace takes the
  login as its address) or connecting an organization's GitHub source moves
  an unverified namespace holding that name to `<name>-<n>`, without a
  redirect, logged, and with a notice to its owners on the organizations
  page. Former addresses reserve nothing against a verified owner, and no
  namespace takes a name a verified login or connected source owns.
- A verified GitHub account (`account_github.github_id`) belongs to one
  account, by number and by login: on startup, a number or a login several
  verified accounts share drops back to the login alone on all of them, and
  linking it again moves it.
- Organizations that exist before this version keep their name and address
  when GitHub is connected: only the picture follows the GitHub organization.
  An owner switches "Name and address: Take from GitHub" on in the settings
  (Connected sources) to let both follow; `ctx db migrate-owners` sets it with
  `"follow_github": true` in the plan and otherwise keeps them as set. Named
  project ids from before a change of address (`<old slug>/<name>`) keep
  working: the server reads and writes them as the project's current id.
- `GHOSTTREE_HOOKS=off` switches every hook and `ctx watch` off, and makes
  `ctx mcp` read only (every writing tool answers that nothing was written);
  set it for the agents a test harness starts, so test runs never write to a
  real server.
- The gardener is a built-in feature: its account stays a service but is
  listed neither among an organization's members nor under "Services"; it
  shows on the Gardener tab.
- The gardener has a schedule per organization and person, set by its owner in
  the settings (Gardener: on or off, daily, weekdays or weekly at a time,
  repositories, budget per run and per month, rules of automatic approval).
  Point the gardener's timer at `ctx garden run-due --repos-dir <dir>
  --state-file <file> -- <the flags of check-knowledge>` and run it hourly
  (`--state-file` is required; each namespace's monthly budget is checked
  before every repository); it asks the server what is due
  and reports each run. `ctx garden check-knowledge` keeps working as before.
- File descriptions are now mainly the gardener's: with `--propose` it writes
  them itself (harness `gardener`) instead of filing a description note. The
  files the merges into the default branch brought since its last run come
  first, anchored on their merge commit, and the directory of a new file
  follows; a description that no longer fits its file is revised. Paragraphs
  an agent or a person wrote stay word for word, and the server refuses a text
  of the gardener that drops one (`kept_paragraphs`). The description sessions
  run on `--ghost-model` (default `claude-haiku-5-5`) and count against the
  gardener's budget; `--dry-run` says how many files and directories it would
  describe and at what estimated cost. Agents are asked to describe a path only
  when they know something the code does not show; run `ctx install` again to
  get the new wording of the rule section.
- Projects now have owners as on GitHub. A write makes a remote a project
  only where nothing is in doubt: the claimed prefix of a connected
  organization the writer is a member of, or the writer's own GitHub login
  verified by GitHub. Everything else stays unclaimed (each author sees the
  own entries, sessions run without the project) until somebody claims it
  explicitly with `ctx project claim`; that is refused as contested while
  other accounts have entries under it, and the instance admin decides with
  `ctx project claim --force --org`. A `github.com/<login>/...` repository
  is claimed explicitly only by the verified owner of that login, a member of
  the organization connected to its prefix, or the instance admin
  (`not_github_owner`); the instance admin gives a known project another owner
  in the web (Organizations page, "Ownership") or on the server with
  `ctx project reassign <remote> --to <namespace|org> --db <path>`, logged on
  both sides. Moving ownership never works over the API, not even with the
  admin's token (`web_session_required` for `POST /api/projects/reassign` and
  for `POST /api/projects/claim` with `force`); `ctx project claim --force`
  needs `--db`. Guests never get a
  project or a namespace of their own; people invited into a project as
  members do get their own namespace. Projects in a person's namespace that an
  organization's prefix covers later wait for a takeover (the owner agrees,
  or the instance admin decides). "Organization for new projects" is gone: `ctx org default` and
  `PUT /api/account/default-org` answer that it no longer exists, and
  `ctx project create`/`claim` without `--org` use your own namespace. Existing
  projects keep their owner until an operator migrates them.
- When a remote becomes a project (or gets a new owner), entries others wrote
  under it before (knowledge, requests, pains, ideas, documents by accounts
  that are no owner or member of the new owner) are held back: their author
  still sees them, the project's owners, leads and reviewers decide them under
  "Held back" on the review page, each one readable on its own preview page
  for them only; until then they reach no list, search,
  context or mirror. `GET /api/projects/held` gives agents only the number,
  releasing is web-only. Every change of owner (reassign, move, takeover, the
  owner migration) holds again what the former owner's reviewers released.
- Remote URLs lose the user and a standard port (22, 443, 80, 9418) when they
  become a project id: `ssh://git@github.com:22/a/b` is `github.com/a/b`
  (before, `github.com/22/a/b`); another port stays (`git.internal:2222/a/b`),
  and `ssh.github.com` is `github.com`.
- Local paths, file URLs, checkouts in temporary or cache folders of a machine
  (`tmp`, `.cache`, `var/folders` as whole path segments, on a host without a
  dot; on a forge such names are ordinary owners or repositories), reserved
  test hosts (`.invalid`, `.test`, `.example`, `localhost`) and the reserved
  test owners `example-nobody`, `e2e-collab` and `pitwall-lab` never become
  projects. `ctx db migrate-owners` takes `"to": {"<prefix>": "<organization>"}`
  as well: such remotes go to (or stay in) the organization, shared with its
  members. Writes naming one are refused with `invalid_remote`; a session from
  such a folder is stored without a project.
- Some refusals now hold even without `GHOSTTREE_ENFORCE_ACCESS`: a guest
  writes nothing the role matrix does not give them (no knowledge edits, no
  runbooks, no runbook runs, no tasks), and approving knowledge needs the
  review right (owner, lead or `can_review`). Reporting a run of a project
  runbook (`POST /api/runbooks/{id}/runs`) needs a member, not just reading. Reading stays open in log mode as before. A guest no
  longer marks tasks done, not even one assigned to them.
- `ctx person add` is deprecated; use `ctx account add`. Existing person tokens
  keep working and are migrated on startup.
- Per-project roles and visibility are only enforced with
  `GHOSTTREE_ENFORCE_ACCESS=1`. Without it the server logs what it would have
  refused (`access: would deny`) and refuses nothing.
- Hybrid search is off unless `GHOSTTREE_EMBED_URL` is set. Sessions are only
  sent to the embedding server when `session` is named in
  `GHOSTTREE_EMBED_KINDS` (`all` does not include it; `all,session` does).
- Building from source needs Go 1.26.10 or newer.
- The first start after the upgrade builds a narrow index from chunks to
  sessions (`chunk_index_chunk`). On a large database this takes about as long
  as reading `chunk_index` once (around a minute for three million chunks on
  slow disks) and adds roughly 40 MB.
- Transcript lines are packed (deflate with a dictionary) from the first start
  on: new lines are stored packed, existing ones stay as they are until `ctx
  db slim-sessions` runs. An older ghosttree cannot read packed lines, so going
  back to it needs the backup taken before the upgrade. The first start adds
  the table `session_raw_dicts` and the column `session_chunks.slim_version`
  (both additive, instant); about 90 seconds after the start the server learns
  a dictionary from a sample once, if it has none and at least 5000 lines.
- To shrink an existing database: stop the server and take a compressed
  backup of the database file (with its `-wal` and `-shm`, if present), start
  the new server, run `ctx db
  slim-sessions --db <path> --dry-run` and then without `--dry-run` beside the
  running server (on local transcripts about a minute per 100,000 lines, of
  which 18 seconds hold the write lock, in steps of 200 lines), then stop the
  server and run `ctx db vacuum --db <path>`. The run itself needs no extra
  disk space (it rewrites in place and frees pages); VACUUM needs about the size
  of the remaining data once more in the database directory and in the WAL. On
  local transcripts the file went from 3.2 GiB to 1.0 GiB, `raw` from 2.1 GiB
  to 217 MiB.

### Added

- The search page answers at once: it shows a progress bar and the groups of
  hits (requests, ideas, knowledge, sessions, tasks, pains) as each kind is
  done, streamed from `/ui/search/stream`. Each kind has a time budget; one
  that runs over it is stopped and can be loaded again. Leaving the page, a new
  search or Stop ends the search on the server: the queries of the search
  paths are interrupted (the full-text, vector and fact reads of every kind;
  helper reads such as the rendering of a hit run to their end). An account
  has one running search; a new one ends the old. The kinds of all searches
  together use one read connection less than the server has
  (`--writer-read-connections`). Without JavaScript the page searches before
  it is sent, as before, and now searches the kinds at the same time.
- The search page and its stream write an `http_request` log line with route,
  path (without the query, so no search text), status and duration, as the
  API does.
- Searching session transcripts no longer reads every chunk of every readable
  session: the full-text hits and the session embeddings look up their session
  in a narrow index, and the kinds of one search share a single embedding of the
  query. On the production database a search for a frequent word went from
  15–19 seconds to about one second.

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

#### Terminal interface

- `ctx` without arguments opens a terminal interface in a terminal (`ctx tui`
  does the same; in pipes and without a terminal `ctx` still prints its help).
  A quick search line jumps straight to `REQ-n`, `PAIN-n`, `TASK-n`, `IDEA-n`,
  `FEAT-n`, `#id` or `pr:n` and shows the card; anything else runs as a
  `ctx search` line, `preset:NAME` included. Enter opens the web page, `c`
  copies the link. Three lists: your open tasks, open requests of the current
  repository and unread room messages.
- Local settings in the interface: servers (as a list, ready for several
  server profiles), the login per server (account, machine, whether the token
  is valid; never the token), update channel, harness state from `ctx doctor`
  with repair, the collector service with restart, the machine name, and a
  link to the web profile for account connections. Each has its own command:
  `ctx login`, `ctx logout`, `ctx config set channel`, `ctx doctor [--fix]`,
  `ctx install watch --restart`.
- `ctx login` in a terminal runs the device login in the interface (server and
  machine, code, browser, waiting, result); `--plain` and runs without a
  terminal keep the text flow.
- New `ctx logout`: the server revokes the token and frees the machine name,
  the client keeps server, machine and channel; exit code 3 when the token was
  removed locally but the server did not confirm the revocation. Signing in on
  another server revokes the login on the previous one, or says it could not.
  New `ctx install watch --restart` restarts the collector, for example after
  an update.
- Text from the server (accounts, codes, addresses, error messages) is
  filtered before it reaches the terminal, in the interface and in the text
  output of `ctx login` and `ctx logout`; only http(s) addresses are opened,
  and the login page only on the server that was confirmed.
- The interface uses what the terminal offers and falls back without it:
  synchronized output, true colour reduced to the terminal's palette, light or
  dark from the terminal background, `NO_COLOR`, clickable links (OSC 8),
  copying over OSC 52, mouse, window title. The logo is drawn in text and
  moves for about a second at start; `--look mark|tree|lines` (or
  `GHOSTTREE_TUI_LOOK`, f2 inside) picks one of three looks,
  `GHOSTTREE_TUI_STILL=1` keeps it still.

- `ctx` in a folder without a project no longer shows a raw `HTTP 400`. The
  terminal interface says what is missing (no git repository, no git remote,
  a remote ghosttree does not know) and offers three ways on: create the
  project from this folder (only with a git remote, after a confirmation with
  the name and the organization; `ctx project claim`), choose another folder
  (typed path with tab completion or the recent folders of this machine's
  sessions; `cd … && ctx`), or show the projects you can see with their open
  requests and your tasks (`ctx project list`). Failed loads read as no
  project, no access, server not reachable or not signed in; search,
  settings and your tasks across all projects keep working. Open requests are
  no longer listed for every project when the folder has none, and an
  unknown room reads as no messages.

- The terminal interface has two working modes instead of one crowded
  screen, switched with shift+tab and shown on a rail at the left (or in the
  header line with `--switch bar`, `GHOSTTREE_TUI_SWITCH=bar` or f3).
  Project: the folder's project with open requests, pains, tasks and room
  messages, one list at a time (tab switches); without a project only the
  "no project" state and its three actions. Inbox: what waits for you across
  projects, each row with its project: your tasks, decisions and questions
  or approvals for people, messages of the last seven days, and suggestions
  to review when there are any; the switch shows a number once something
  waits. The big logo only shows at the start and gives way to a compact
  header line. Room messages are read through the search where this
  terminal never joined the room, so reading never writes.

- Command palette in the terminal interface: ctrl+k opens it over the
  current screen (dimmed behind it where the terminal has colours, framed
  always). Fuzzy search over jump targets (REQ-n, PAIN-n, TASK-n, #id, pr:n,
  projects, recent folders), actions (switch mode, settings, update channel,
  doctor and repair, restart collector, sign out with its confirmation,
  create project from this folder, open in browser, copy link) and a search
  in the search language; each action shows its key or command, the
  recently used ones come first. ctrl+f jumps into the search line from any
  screen but forms; esc leaves it. The footer names ctrl+k.

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

- The commit page shows what GitHub says about a commit: parents as pills,
  the whole message, co-authors, the changed files with their added and removed
  lines, and the state of the checks; the list shows the checks and the
  co-authors too, and both have a `<>` button to the code at that commit.
  `ctx github sync-prs --commits` (and the webhook, for the newest commits,
  after it has answered GitHub) fetches and stores it, so a page never asks
  GitHub. The changed files need the App permission `Contents: read`, the checks
  `Checks: read`; a missing permission leaves the block out, is noted per
  installation, and does not keep those commits at the head of the queue. A
  commit GitHub no longer knows (after a force push) is skipped on its own. A guest who may not read every branch gets
  none of it.
- The profile has a block "Notifications" with one switch per kind (mentions,
  questions to me, decisions, tasks, gardener) for the inbox and the bell; a kind
  switched off is missing there and from the count, and stays readable where it
  comes from. Mentions only ever appear in the inbox, the bell does not count
  them, so their switch names only the inbox. The profile grid follows the mock (portrait 296 px).
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
  `ctx` typed by a person in a terminal (no agent session, no agent harness in
  the environment, stdin and stdout a terminal) is recorded as "person
  (terminal)", apart from "person (web)". It is the client's own word: it grants
  no right, an agent could claim it, and a call with an agent session is always
  an agent.
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
- Documents in the browser: a repository with documents gets a Docs tab with
  title, kind, slug, latest revision and who wrote it (an agent as "Robin's
  Claude"). A document has a fixed address, `/ui/{org}/{repo}/docs/{slug}`,
  shows its Markdown like comments do (references as pills with hover cards,
  no raw HTML, no `javascript:` links), every revision, the diff between any
  two revisions, its raw text and "Copy link". Documents appear in the web
  search and its live stream with the line that matched (`is:doc` narrows to
  them). `ctx doc new` names the address the draft will have,
  `ctx doc push` and `import` print it, and `ctx open` / `open_link` take
  `doc:slug` and `doc:owner/repo/slug`. Reading follows the project's
  knowledge: guests read, whoever has no role gets 404. A renamed document
  keeps its old address (302 to the new slug). A revision may have at most
  5 MB of text; the server answers 413 `document_too_large`, and `ctx doc push`
  and `import` say so before sending.
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
- One search index over sessions: the words of every transcript line are
  indexed once, in `chunk_fts`, which keeps no copy of the text; snippets are
  made from the stored line for the hits of a page. The first start adds
  `chunk_fts` and the columns `chunk_index.fts` and `chunk_index.prompt`
  (additive, instant, no scan). Existing rows keep their copies until `ctx db
  slim-search --db <path>` runs (first `--dry-run`, then beside the running
  server, then `ctx db vacuum` with the server stopped): it moves their words
  from `sess_fts` to `chunk_fts`, empties the four text fields of
  `chunk_index`, empties `sess_fts`, drops the trigger `chunks_ai` and empties
  `chunks_fts`. Nothing is derived again from the transcripts, so no
  distillation and no embedding starts again. On local transcripts
  `chunk_index` went from 273 MiB to 23 MiB and the file from 878 MiB to
  563 MiB; the web search answers byte for byte as before. An older ghosttree
  started after `slim-search` does not find the moved sessions, so going back
  needs the backup.
- The agents' session search (`context_search` with sessions,
  `ctx search`) and the sessions of a request search the same fields as the
  web search: messages, thinking, commands and tool output. Instructions a
  harness injects (Codex developer messages, plugin and skill lists,
  environment context) are no longer hits.
- `ctx db chunk-stats --db PATH [--sample N]` shows, from a sample, how many
  bytes stored transcript lines take per line type: as text, as stored (packed
  or not), after the slim rule and after slimming and packing; read-only and
  throttled, so it may run beside the server. Administrators see the same per
  line type on the settings page (Transcripts).
- `ctx db slim-sessions --db PATH [--dry-run] [--batch N] [--pause D]` brings
  stored transcripts to the form of new lines: slimmed, packed, lines the rule
  drops deleted together with their rows in `chunk_index`, `sess_fts`,
  `chunks_fts` and their session vectors (a line a knowledge or request quote
  cites stays), and `text` cut like long outputs except in sessions with an
  open distillation batch. It reports lines and bytes before and after, skips
  what is already current, may be interrupted and started again, and pauses
  at least as long as each transaction held the write lock. At the end it
  merges both full-text indexes with `optimize` (`--skip-optimize` leaves that
  to `ctx db vacuum --fts-optimize`).

### Changed

- `ctx` starts in 4.5 ms instead of 14.5 ms. It is one binary, and every start
  parsed the page templates of the web interface in both languages, although
  only `ctx serve` shows a page. They are now parsed when the server starts, so
  a broken template still stops it at once. Every hook and every command is
  that much faster. Together with the request it no longer makes, the hook
  before a shell command that names no file takes 7.8 ms instead of 18.9 ms
  against a local server.
- A session start no longer waits for the files under `.ghosttree/` to be
  rewritten. The hook asked the server once per document and several times per
  discussion before the model saw anything: 71 requests in a small project,
  1.8 seconds at 25 ms a request. Where the files exist, a process of its own
  (`ctx hook refresh-files <repo>`) now rewrites them while the session
  begins, and the hook waits for two requests (79 ms in the same setup). For
  those seconds the files show the state of the previous session start. A
  checkout without the files still gets them before the session starts.
- The hook before a tool call asks the server about runbooks only when a
  command or path trigger matches. It keeps the triggers of the project for
  five minutes (`GET /api/runbook-triggers`, under
  `$XDG_STATE_HOME/ghosttree/runbook-triggers`) and matches them with the
  server's own rule, so a Bash call that names no file costs no request
  instead of one. A runbook approved in the meantime reaches the tool hook
  within those five minutes and the prompt hook at once. A server without the
  route is asked before every call, as before.
- A tool call on several paths nothing is known about no longer delivers a
  line break as context, and no longer counts against the session budget.
- The MCP server lists twelve core tools instead of 76: `context_search`,
  `context_get`, `context_remember`, `request`, `pain`, `idea`, `coord`,
  `human_task`, `package`, `feature`, `runbook` and `comment`. Each takes an
  `action` (for example `request` with `action=criterion_met`); the old names
  still answer when called directly but are no longer listed. The list shrank
  from 100,709 to about 41,600 bytes, and every core tool asks Claude Code to
  load it up front (`_meta` `anthropic/alwaysLoad`), so agents see the
  descriptions without tool search.
- A core tool answers with text alone. An answer used to carry the rendered
  text and a structured copy of the records, and Claude Code shows its model
  only the copy: the summary and the card with the web link never arrived,
  every timestamp did. `request action=get` on a request with nine criteria now
  reaches the model as 3,500 characters instead of 4,600, with
  `response_format=detailed` as 4,300 instead of 10,100, and names the
  `criterion_id` next to each criterion.
- A core tool follows a field name that was guessed from a neighbouring tool
  (`id` for `request_id`, `content` for `body`, `detailed` for
  `response_format`), takes a reference like `REQ-12` where the number is asked
  for, and names a missing field with what it is for.
- `context_search` takes `limit` (default 20, at most 50 per area).
- `context_remember action=describe_file` takes an absolute path for a file in
  another worktree of the repository and files the description under that
  worktree's branch. A relative path that is not found says where it was
  looked for.
- A subagent in Claude Code starts with a short ghosttree briefing: the working
  rules, the branch's work package with its open criteria, the pitfalls its
  task names and where the `.ghosttree/` mirror is (SubagentStart hook).
- A session that changed files but recorded nothing in ghosttree gets one
  reminder at the end of a turn with what is worth keeping. Read-only sessions
  get none.
- Calls of ghosttree's tools are counted per session, tool, action, harness and
  subagent, and the statistics page (Agents, Models) shows them with the
  sessions that called none.

- Guests see their project only: no page of the organization (repositories,
  people, services, agents, machines, rooms, gardener, settings), the
  organization is not in their menu, and a machine shared with it shows them
  no user and no paths. A member who holds nothing but guest roles counts as a
  guest. An administrator's token page lists only the own tokens.
- Every member of an organization (not a guest) creates projects in it, also
  by an explicit `ctx project claim --org`; whoever is not an owner of the
  organization becomes owner of the project.
- The role is called "Owner" in German too (it said "Besitzer" in places).
- Joining a project room (which `ctx coord inbox` does before it reads) no
  longer creates a project for an unknown remote; only creating it, a session
  or written content does.
- "Create project from this folder" in the terminal interface offers the
  owner rule first: the connected organization named like the remote's GitHub
  owner, else your own namespace (claimed without `--org`), then other
  organizations you own.
- The gardener left the inbox: its proposals, runs and automatic approvals are
  on a Gardener tab of the organization or person where it is switched on, for
  those who may review them. Three narrow rules can approve on their own
  (missing proof with a new proof, missing description, existing test); each
  approval is logged with what it changed and can be undone. Corrections,
  staleness, merges and contradictions always wait for a person.
- The machines tab of an organization has two parts: machines of the
  organization and machines people shared with it. Archived machines leave the
  lists.
- A person's own namespace has a page at `/ui/<handle>` with their
  repositories (private, or shared with N collaborators), their machines and,
  with GitHub linked, their picture. Its settings have no members and no
  sources, and a personal project cannot be "shared with the org".
- An organization connected to a GitHub organization shows its picture; once
  an owner switches "Name and address: Take from GitHub" on, also its name and
  its login as address. This happens on connecting, on "Read repos again" and
  once a day (GitHub sends no webhook for it), until an owner renames the
  organization by hand. A former address answers members with a redirect
  (302); named projects move with the address.
- Accounts have a kind, person or service (`ctx account set-kind --db`). A
  service (the gardener, an operations runner) is listed under the new
  Services tab of an organization instead of People, and it gets no role from
  "shared with the org": it reads and writes only in projects that name it.
  Guests do not see the Services tab.
- A session in a repository you may not write to (someone else's, archived, a
  throwaway checkout) is kept without its project instead of being refused.
- A github.com remote under the linked GitHub login of a person belongs to that
  person; no other account can occupy it first.
- A moved organization slug stays reserved: its former named-project ids and
  the slug itself cannot be taken over, and a personal namespace never blocks
  hostless remotes of other people.
- Founding an organization is no longer a tab in the settings of another one:
  it is in the "+" menu (`/ui/orgs/new`). After connecting GitHub the settings
  open at the connected sources.
- Agent search (`context_search`, `ctx search`, `/api/search`) leaves out
  unconfirmed (staged) entries and the gardener's notes, as delivery already
  did, so they no longer crowd out the entries they are about. `is:staged`,
  `author:gardener` or `harness:gardener` bring them back. The web search
  shows staged entries to people as before, the gardener's notes only when
  the search line asks for them.
- `tools/list` of `ctx mcp` sends each output schema as its root type only
  (`{"type":"object"}`), and the longest tool and field descriptions are
  shorter: the list drops from about 193,000 to about 101,000 bytes. Tools
  still answer with structured content, which the server checks against the
  full schema; none of Claude Code, Codex and OpenCode sends output schemas to
  the model outside their code-execution modes.
- Secrets typed into a prompt are redacted by their label (`Password: …`,
  `DB_PASSWORD=…`, `api key …`) before a transcript is stored, and by `ctx
  redact-sessions` in existing data. The server also redacts on arrival.
- Stored transcripts no longer keep base64 images and files; a placeholder with
  type, size and hash stands in their place.
- Transcripts are stored slim: lines and fields that only repeat what is
  already there or that no reader uses are left out before upload (Claude
  Code's `api-request*` lines, `prompt_snapshot` and `hook_success`
  attachments, thinking signatures, `wireToolInputs`, the copies in
  `toolUseResult`; Codex's `item_completed` copies, encrypted reasoning,
  replaced history after compaction, rate limits), and tool outputs over
  8 KB keep their first 6 KB and last 2 KB with length and hash. Prompts,
  answers, thinking, tool calls, models and times stay. The server applies the
  same rule to lines from older clients. Left-out lines leave gaps in the line
  numbers. On local transcripts this saves about two thirds of the stored
  bytes. `ctx export` returns the slim lines, which read the same but cannot be
  resumed in Claude Code or Codex. What this gives up: the middle of a long
  tool output is no longer stored, so search and embeddings cannot find it
  (the marker's hash is over the redacted text); and an input that was queued
  while the agent worked but never sent (`queue-operation` content) is no
  longer in the archive. Secrets are redacted before anything is cut.
- `session_chunks.raw` is stored packed: deflate (Go standard library) with a
  32 KB dictionary learned from the database's own lines, versioned in
  `session_raw_dicts` and never deleted while lines use it, with a CRC-32C of
  the line in every frame so that a wrong dictionary or a damaged frame is an
  error, never other text. Short lines that would not shrink stay plain text.
  Every reader unpacks through one function; the hash that ties a session
  vector to its line is taken over the unpacked line, so repacking does not
  make the vectors stale. Checks that ran in SQL over `raw` (`instr`,
  `length`) now run in Go over the unpacked line.
- `session_chunks.text` is cut like long tool outputs (first 6 KB, marker, last
  2 KB) when it is stored. The session index already kept 8 KB per field; the
  full-text search over `text` and the distiller now see the same. This does
  not start a new distillation: sessions are chosen by prompt version, not by
  the digest of their text. On local transcripts a third of `text` was above
  8 KB, almost all of it tool output.
- The session view shows the commands Codex's `exec` tool ran, with their
  output, and the output of `exec` itself, which it skipped before.
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

- A tool call during a restart of the server no longer returns the HTML error
  page of the proxy in front of it. The message is one line, a read is tried
  twice more first (`ctx mcp` only; a write is never repeated), and a failed
  write says that it is not known whether it arrived.
- Searching sessions by meaning no longer takes 40 s or more for a reader who
  may read only part of the transcripts. SQLite looked every embedding up once
  per readable session; it now looks it up once (0.1 s with a thousand readable
  sessions).
- `ctx db slim-sessions` no longer ends when the server holds the write lock
  longer than its busy timeout: the step waits (5 s up to 2 min) and is tried
  again, and its counts are taken only once.
- `context_search` without `kind` and `ctx search` no longer lose every hit
  when one area fails. The areas run at the same time, each with a time
  budget of its own (on the server, sessions get 8 seconds when other areas
  are asked too); a failed area is named in one line (`sessions: timed out`)
  and the hits of the others are returned. Asked for one area alone, its
  failure is still the answer.
- The hint line on a prompt no longer fires on everyday words such as
  "wohin" or "hattest". Common German and English words never count as
  codenames, and a word that more than three of the reader's sessions or the
  project's requests use gets no hint either.

- The server starts without reading every transcript line: the check for
  lines an older binary wrote without search index compares the two highest
  line numbers instead of scanning all lines since the last index run. Once
  per start, two minutes after the index backfill, a throttled background
  pass compares the line keys with the index keys (no transcript text is read)
  and indexes any line it finds missing (`session_index_gaps` in the log).
- The repository list of an organization called projects shared with the
  organization "Knowledge public" and the others "Knowledge private". Nothing
  there is public: the labels now read "Shared with the org" and "Restricted",
  and the filter is called "Visibility".
- The knowledge, review, context and sessions pages of a repository showed
  guests and members without admin rights all projects instead of that
  repository (including machine entries of other people in log mode).
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
