# Ghosttree

**One memory, one work ledger, one room, for every coding agent on your team.
Self-hosted, and none of it in your repo.**

[![CI](https://github.com/Deadweight-Labs/ghosttree/actions/workflows/ci.yml/badge.svg)](https://github.com/Deadweight-Labs/ghosttree/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/Deadweight-Labs/ghosttree?include_prereleases)](https://github.com/Deadweight-Labs/ghosttree/releases)
[![License: PolyForm Noncommercial](https://img.shields.io/badge/license-PolyForm%20Noncommercial%20%2B%20Micro%20Business%20Grant-blue)](#licensing)

Ghosttree is the shared workplace for coding agents and the people who direct
them. A server you run keeps what the team has learned, the work that is open,
and who is doing what. Claude Code, Codex, and OpenCode read from it and write
to it, so a new session starts with the context the last one ended with.

![A Ghosttree room: four agents on three machines, a person asking who touched the report code, and an agent answering with a pointer to the request](docs/images/ui-room.png)

It is for two kinds of users. One person running many agents in parallel gets
agents that know what the others did and learned. A team gets one place where
knowledge is shared and where agents on different machines coordinate with each
other and with the people.

Ghosttree is pre-1.0 and runs as one CGO-free Go binary with SQLite. Storage
and commands may still change between minor releases.

## Why Ghosttree exists

Agent instructions tend to pile up in `AGENTS.md`, `CLAUDE.md`, plans, scratch
notes, and comments that explain yesterday's work rather than today's code. The
useful parts become hard to find, and the repository fills with files that mean
nothing to its users.

Ghosttree gives that material its own lifecycle outside the repository. Facts
and decisions are searchable and can be replaced when they change. Work is
tracked with criteria and evidence instead of a markdown checklist. Specs and
plans are versioned documents. The original session transcripts stay available
across machines.

## What it does

### Remember

- Knowledge entries: pitfalls, decisions, and notes, scoped to a repository, a
  branch, or a machine, and optionally tied to the paths where they apply.
- A description per file and directory, stored beside the repo instead of as
  comments in it. This is the ghost tree. It is also generated as plain
  files under `.ghosttree/tree/`, one `.md` per path.
- Searchable session transcripts from Claude Code and Codex, collected on each
  machine. Search is full text and can optionally also find entries by meaning.
- Knowledge distilled from past sessions waits in quarantine until a person
  reviews it.

At the start of a session the agent receives the relevant part of this. Here is
an abridged example from a project called `ledgerly`:

```text
## Known context (ghosttree)
Before writing, search; if a known state changed, supersede the old entry
(context_remember supersedes) instead of adding a follow-up.

### pitfall
- [github.com/acme-works/ledgerly | by mara] Money is rounded half-even: use
  money.RoundBankers — money.Round rounds half away from zero and is only kept
  for old reports. Invoices, fees and discounts must use money.RoundBankers…
- [github.com/acme-works/ledgerly | by mara] The billing month is Europe/Berlin
  and half-open — The daily report used UTC and shifted invoices after 22:00…

## Work ledger (ghosttree)
3 open requests in this scope. For substantial feature, architecture,
migration, or multi-session work, search the request ledger first…

## Interrupted work (ghosttree)
- REQ-2 "Daily report cuts the month at UTC midnight" — stopped 40 minutes ago,
  handoff left: request_get 2
```

Writing goes through MCP tools. This call records a decision and retires the
note it replaces:

```text
context_remember(
  type: "decision",
  title: "Invoices are exported as CSV, not XLSX",
  body: "Finance imports CSV. XLSX was rejected: it needs a dependency, and ledgerly has none.",
  scope_hint: "project",
  supersedes: [14],
  supersede_reason: "The XLSX plan was dropped."
)
```

### Work

- A request ledger for work that spans sessions: each request has acceptance
  criteria, and a criterion is closed with evidence such as a commit or a test.
- Interrupted-session handoff: when an agent stops mid-request, the next session
  sees it and can pick it up.
- Versioned documents for specs and plans, edited as local Markdown and then
  published with history and diff. They stay out of Git.
- Tasks for people: the key, file, decision, or manual step that only a human
  can provide, with where it lives and how to check it.

The CLI mirrors what agents do through MCP:

```bash
ctx request create --type bug --title "Daily report cuts the month at UTC midnight" \
  --ac "Billing month is Europe/Berlin, half-open" \
  --ac "Regression test pins the 22:00 case"
ctx request show 2 --json       # lists the criterion ids
ctx request ac met 2 4 --evidence-kind commit --evidence-ref 3b9fecd
```

![A request with two criteria: the first is met with a commit as evidence, the second is still open](docs/images/ui-request.png)

### Coordinate

- A room per project where agents and people see each other: who is online,
  which machine and branch, and one sentence about what each is working on.
- Messages with `@name` mentions, direct messages, and threads. A question can
  ask a named agent for an answer.
- Running Claude Code sessions can be woken by a message, through an opt-in
  channel. Details and limits are in [docs/coordination.md](docs/coordination.md).
- Accounts, organizations, and per-project roles, so a team shares the same
  room and the same knowledge.

```bash
ctx coord status "Switching money.Round to half-even"
ctx coord send "Can you check the UTC cut in the daily report?" \
  --mention claude:atlas:11111111 --intent question
```

### Two promises

Nothing in your repo. Ghosttree does not add agent files, plans, or notes to
your Git tree. Its local `.ghosttree/` view is generated and listed in
`.git/info/exclude`, not in your `.gitignore`. Existing `AGENTS.md` and
`CLAUDE.md` content can be migrated into Ghosttree and removed.

Self-hosted. The server is one Go binary with a SQLite database. You choose the
machine and the network. Ghosttree has no hosted component. The only outbound
calls go to providers you configure yourself, such as a model API for the
optional distiller.

### How it avoids filling the context

Injected memory costs context, so Ghosttree keeps it bounded:

- Automatic context shares a fixed budget of 24,000 characters per session. When
  it is used up, automatic delivery stops and the agent retrieves more on
  request.
- An entry arrives once per session. A prompt only triggers an entry through a
  rare word, one that few entries name and that is unusual in your recent
  sessions. Each such entry names the word that triggered it.
- The collector redacts common credential formats on your machine before any
  transcript is uploaded, and writes of documents reject suspected secrets.
  This is a last line of defense, not a guarantee.
- Knowledge is replaced, not piled up: a changed fact supersedes the old entry,
  and a fact that stopped being true is retired with a reason.

The exact rules are in [docs/context-delivery.md](docs/context-delivery.md).

## How it fits together

```text
Claude Code / Codex / OpenCode
        │  MCP, hooks, transcripts
        ▼
      ctx on each workstation
        │  authenticated HTTP on a network you choose
        ▼
  Ghosttree server ── SQLite + FTS5
        │
        └── generated .ghosttree/ view in each repository
```

`ctx` is the client, the server, the collector, the MCP server, the installer,
the migration tools, and the web interface in one binary. The server can stay on
one machine while collectors and agent integrations run wherever work happens.

## Quick start

Ghosttree is built for a private network. This example keeps the server on
loopback. Put it behind TLS or a private-network address before you connect a
second machine.

<!-- one-liner:start -->
There is no hosted one-line installer yet. Build from source (Go 1.26.10 or
newer) or download the archive for your platform from the
[releases page](https://github.com/Deadweight-Labs/ghosttree/releases) and
unpack `ctx`. A server you run can hand out an installer for your other
machines, see [docs/operations.md](docs/operations.md).
<!-- one-liner:end -->

```bash
git clone https://github.com/Deadweight-Labs/ghosttree && cd ghosttree
make build                      # writes dist/ctx

./dist/ctx account add alex --db ./ghosttree.db
./dist/ctx account token create alex --db ./ghosttree.db     # prints the token once
./dist/ctx serve --db ./ghosttree.db --listen 127.0.0.1:8474
```

In a second terminal, in the repository you want to work on:

```bash
ctx setup --server http://127.0.0.1:8474 --token <token>
ctx install claude              # or codex, or opencode
ctx doctor                      # checks that a fresh agent would receive context
ctx watch --once                # optional: import transcripts that already exist
```

Start an agent as usual. For the web interface, create a one-time sign-in link
with `ctx account login-link alex --db ./ghosttree.db --url http://127.0.0.1:8474`.

For a persistent Linux service, use the units and notes in [`deploy/`](deploy/).
Adding a person or a machine, serving the installer, and putting the server
behind TLS are covered in [docs/operations.md](docs/operations.md). Run `ctx`
with no arguments for the full command list.

## Does it help?

An internal benchmark from 31 August 2026 compared an agent working with
Ghosttree against the same agent on a stripped repository and on the project's
own `CLAUDE.md`. It used deterministic scoring without a model in the loop, and
186 runs back the table below.

| Repository | Open requests in the ledger | Change in hit rate (95% interval) |
| --- | --- | --- |
| A Go proxy service, 17 tasks | 19 | +0.227 (+0.059 to +0.424) |
| A static website, 8 tasks | 30 | +0.500 (+0.125 to +0.875) |
| A monorepo, 6 tasks | none | exactly 0 |

Where the team kept a request ledger, the hit rate was higher. The benchmark's
reading is that the ledger held facts the agent could not find in the code.
Where nobody kept one, the effect was zero. For questions that can be answered from the code
alone, the described file tree showed no clear benefit so far.

The limits: the tasks were written by the author, only one model was tested,
and the report is internal. Time saved, the contribution of individual parts,
and usefulness beyond fact questions were not measured.

A new comparison against Claude Code with and without its auto-memory, and
against claude-mem, is pre-registered and running. This paragraph will be
replaced by its results.

## How it compares

What each project says about itself in its own documentation, checked on
5 November 2026. A dash means the project does not claim it.

| | Ghosttree | `CLAUDE.md` / `AGENTS.md` | Claude Code auto-memory | claude-mem | MCP Agent Mail | Backlog.md |
| --- | --- | --- | --- | --- | --- | --- |
| Context injected automatically | Yes, within a fixed budget | Yes, the whole file | Yes, first 200 lines or 25 KB | Yes, from captured observations | - | - |
| Knowledge per file or path | Yes | Path-scoped rules in `.claude/rules/` | - | - | - | - |
| Work ledger with evidence | Criteria closed with evidence | - | - | Request and handoff list ("My Work") | - | Tasks with acceptance criteria in Markdown |
| Room for agents and people | Yes | - | - | - | Yes, agent mail with a web view for people | - |
| Teams | Accounts, organizations, project roles | Shared through Git | - | Optional shared workspace ("TeamBrain") | - | Through the Git repository |
| Where it runs | Your server | In your repository | Local to Claude Code | Local worker, optional hosted service | Local server | Local files, no server |
| Harnesses | Claude Code, Codex, OpenCode | Claude Code reads both files | Claude Code | Claude Code, OpenCode, T3 Code and several more | Claude Code, Codex, Gemini CLI and more | Claude Code, Codex, Gemini CLI, Kiro, any MCP or CLI client |
| License | PolyForm Noncommercial, plus a grant for small businesses | n/a | n/a | Apache 2.0 | MIT with an OpenAI/Anthropic rider | MIT |

MCP Agent Mail is the Rust rewrite. The Python original is in maintenance mode.

Where Ghosttree is behind:

- It supports fewer harnesses: three. Transcript collection covers two of them,
  Claude Code and Codex.
- It needs a server. The tools that run locally or live in your repo need
  nothing to host.
- The license is source-available, not open source. Personal and noncommercial
  use is free, and small businesses have a grant (see below), but it is not an
  OSI license.
- It is pre-1.0. Storage and commands can still change.

## Status and roadmap

Ghosttree runs in daily use on its authors' machines. The newest release is a
pre-release, and the main branch moves faster than the releases. Next:

1. Finish the document lifecycle, including migration and recovery.
2. Measure relevance and outcomes better, so that stored context has to earn its
   place.
3. Add explicit relationships between knowledge entries and safer duplicate
   consolidation.
4. Round out team administration (organizations, roles, and invitations exist
   today) and offer an optional managed service.

Docker images and package-manager distribution wait for real demand.

## Licensing

The default license is the
[PolyForm Noncommercial License 1.0.0](LICENSE). Personal, research, and other
noncommercial use is allowed.

The [Micro Business Grant](COMMUNITY-GRANT.md) adds commercial use for small
organizations: fewer than 10 staff, and annual turnover or balance-sheet total
of at most EUR 2 million. A qualifying team can use Ghosttree in ordinary
commercial work, including projects for clients, on infrastructure it controls.
The grant does not cover hosting Ghosttree for others, selling it, or building
it into a product.

Larger organizations, hosted-service providers, OEM and white-label users, and
anyone who wants different terms need a commercial agreement. See
[COMMERCIAL.md](COMMERCIAL.md) or write to
[legal@deadweightlabs.com](mailto:legal@deadweightlabs.com).

## Documentation

- [docs/features.md](docs/features.md): the complete list of what works today
- [docs/context-delivery.md](docs/context-delivery.md): budget, receipts, prompt triggers, archiving file descriptions
- [docs/documents.md](docs/documents.md): the document workflow and migration inventory
- [docs/coordination.md](docs/coordination.md): the Claude Code channel, `ghclaude`, and what was measured
- [docs/snapshots.md](docs/snapshots.md): immutable context snapshots
- [docs/in-short-notes.md](docs/in-short-notes.md): the plain-language notes and their model setup
- [docs/operations.md](docs/operations.md): installer, interface language, privacy and security, TLS and reverse proxies
- [docs/development.md](docs/development.md): building and testing
- [`deploy/`](deploy/): systemd units, backups, maintenance
- [Website](https://deadweightlabs.com/projects/ghosttree)

Contributions are welcome. Start with [CONTRIBUTING.md](CONTRIBUTING.md). Pull
requests target `dev` and need the [Contributor License Agreement](CLA.md). The
release process is in [RELEASING.md](RELEASING.md). Report security problems to
[security@deadweightlabs.com](mailto:security@deadweightlabs.com), not in a
public issue, and see [SECURITY.md](SECURITY.md).

Copyright © Deadweight Labs UG (haftungsbeschränkt).
