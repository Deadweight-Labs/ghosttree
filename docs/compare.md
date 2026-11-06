# Ghosttree compared

How Ghosttree differs from the tools people usually weigh it against: `CLAUDE.md`
and `AGENTS.md`, Claude Code's own auto-memory and agent teams, claude-mem,
ai-memory, MCP Agent Mail, Backlog.md, Mem0 and OpenMemory, and ByteRover.

Every statement about another project comes from that project's own README or
documentation, read on 5 November 2026. The sources are linked in each section.
Where a project's documentation does not say something, this page says "not
documented" instead of guessing. These projects change quickly. If something here
is out of date, please open an issue.

Ghosttree is the shared workplace for coding agents and the people who direct
them: knowledge, a work ledger, and a room, on a server you run, with nothing added
to your repository. The [README](../README.md) has the short version.

## At a glance

| | Mainly for | Where it runs | License |
| --- | --- | --- | --- |
| Ghosttree | Shared knowledge, work ledger and room for agents and people | A server you run | PolyForm Noncommercial, plus a grant for small businesses |
| `CLAUDE.md` / `AGENTS.md` | Written instructions for agents | In your repository | Open format (`AGENTS.md`) |
| Claude Code auto-memory, agent teams | Notes Claude keeps for itself; teams of Claude sessions | Inside Claude Code | Part of Claude Code |
| claude-mem | Automatic capture and recall of what an agent did | Local worker, optional hosted service | Apache 2.0 |
| ai-memory | Shared memory and handoffs across many agents | A server you run | MIT |
| MCP Agent Mail | Messaging and file leases between agents | A server you run | MIT with an OpenAI/Anthropic rider |
| Backlog.md | Task board kept as Markdown files | Files in your repository | MIT |
| Mem0 / OpenMemory | A memory layer for AI applications | Library, self-hosted server, or hosted platform | Apache 2.0 |
| ByteRover | Curated, version-controlled project knowledge | Local CLI, optional hosted sync | Elastic License 2.0 |

## When to pick what

- You have one repository, one agent, and instructions that rarely change: a short
  `CLAUDE.md` or `AGENTS.md` is enough. Use nothing else.
- You want a single agent to remember what it did, with no hosting and no writing
  by you: claude-mem, or Claude Code's auto-memory.
- You use many different agent tools and want one shared memory: ai-memory
  supports far more of them than Ghosttree does.
- You want a task board that lives next to the code and goes through pull
  requests: Backlog.md.
- You want a server your team runs, where knowledge, open work, and the
  conversation between agents and people sit together, and where nothing lands in
  the repository: that is what Ghosttree is for.

## `CLAUDE.md` and `AGENTS.md`

### What it is

Markdown files that agents read at the start of a session. Claude Code's
documentation describes `CLAUDE.md` as instructions you write, shared with your team
through version control, and recommends keeping each file under 200 lines. Claude
Code can also read a repository's `AGENTS.md` in place of `CLAUDE.md`. The
`AGENTS.md` site calls its format "a README for agents" and says over 60,000 open
source projects use it.

### Where it is stronger

It needs no infrastructure. The file travels with the repository, so every clone
and every pull request gets it, and a reviewer sees changes to it next to the code.
`AGENTS.md` is read by many tools, which Ghosttree cannot match. For a small project
with stable rules this is the right tool.

### Where Ghosttree is stronger

A file has no lifecycle. When a fact stops being true, someone has to find the line
and edit it, and long files cost context on every session. Ghosttree stores
knowledge as entries that can be superseded or retired with a reason, scopes them to
a repository, branch, machine, or path, and delivers them within a fixed budget.
It also keeps the material out of the repository. Existing `AGENTS.md` and
`CLAUDE.md` content can be migrated into Ghosttree and removed.

### Together

Yes. Keep a short file for the rules every contributor needs, and move the rest to
Ghosttree.

Sources: [Claude Code memory documentation](https://code.claude.com/docs/en/memory),
[agents.md](https://agents.md).

## Claude Code auto-memory and agent teams

### What it is

Three separate features in Claude Code.

Auto-memory lets Claude write notes for itself as it works. According to the
documentation it is on by default in local sessions, stores plain Markdown files in
`~/.claude/projects/<project>/memory/`, shares one directory across all worktrees of
a repository, and loads the first 200 lines or 25 KB at the start of each session.
The documentation states that it is machine-local and that the files are not shared
across machines or cloud environments.

Agent teams let one Claude Code session lead several teammates that share a task
list and message each other. The documentation marks the feature as experimental
and disabled by default, and lists known limitations around session resumption,
task coordination, and shutdown.

Cross-session messaging, available since Claude Code v2.1.224, lets one of your
sessions send a text message to another session, on the same machine or on your
other machines and in the cloud.

### Where it is stronger

It is built in and first-party. There is nothing to install or host. Agent teams
cover something Ghosttree does not document: one lead session coordinating
teammates that each run in their own context window. For a single person working in Claude Code
on one machine, auto-memory plus cross-session messaging may be all you need.

### Where Ghosttree is stronger

These features work inside Claude Code. Ghosttree also serves Codex and OpenCode.
Its knowledge is shared between machines and between people, and it lives
outside any one session: a request ledger with acceptance criteria, a room that
people join through a web interface, and searchable transcripts. The room is a
shared place for agents and people, where a cross-session message is "a piece of
text one Claude writes to another".

### Together

Yes. Both inject context at session start, so the cost adds up: auto-memory loads up
to 200 lines or 25 KB, and Ghosttree's automatic delivery has a budget of 24,000
characters per session. You can turn auto-memory off with `/memory` or
`CLAUDE_CODE_DISABLE_AUTO_MEMORY=1` if you want one source of notes.

Sources: [memory](https://code.claude.com/docs/en/memory),
[agent teams](https://code.claude.com/docs/en/agent-teams),
[cross-session messaging](https://code.claude.com/docs/en/cross-session-messaging).

## claude-mem

### What it is

A memory plugin for Claude Code, licensed under Apache 2.0. Lifecycle hooks capture
tool usage, a model generates summaries, and later sessions get that material
injected. It runs a local worker (Bun) with SQLite and a Chroma vector database for
hybrid semantic and keyword search, a web viewer, and an MCP search workflow in
three steps. The README lists installers for Claude Code, OpenCode, T3 Code, and
several other harnesses. It also describes "My Work" (requests, progress, human
handoffs, a review queue), an optional shared workspace called TeamBrain, and
optional cloud sync to cmem.ai.

The installer offers a browser sign-in with a 30-day trial of a hosted observer; the
README says you can skip it with `--provider` or `CLAUDE_MEM_ONLINE_OPTIN=false`,
and choose your own OpenRouter or Gemini key or your Anthropic plan as the memory
provider. It needs Node.js 20 or newer, and installs Bun and `uv` if they are
missing.

### Where it is stronger

Capture is automatic: nobody has to write anything down. Semantic search ships in
the default setup, where Ghosttree's is optional and needs an embedding server you
provide. It installs with one command, is open source under an OSI license, and
covers more harnesses.

### Where Ghosttree is stronger

Ghosttree has no hosted component, and nothing requires an account or a model
provider to store and retrieve knowledge. It scopes knowledge by repository, branch,
machine, and path, replaces changed facts instead of accumulating observations, and
has accounts, organizations, and project roles in the server you run. Its request
ledger closes each acceptance criterion with evidence such as a commit or a test;
claude-mem's README describes requests and handoffs but does not document
acceptance criteria or evidence. The room, where agents and people message each
other, has no counterpart in claude-mem's README.

### Together

Possible in principle: both work through Claude Code hooks. The documentation of
neither project describes a combined setup, and both inject context at session
start, so the context cost adds up. A comparison of Ghosttree against Claude Code
with and without auto-memory, and against claude-mem, is pre-registered and running;
the README will carry its results.

Source: [claude-mem README](https://github.com/thedotmack/claude-mem).

## ai-memory (akitaonrails)

### What it is

A shared memory server written in Rust and licensed under MIT, installed with a
two-command script and a `setup` step. Lifecycle hooks capture sanitized
observations, the server consolidates them into a git-backed wiki of plain Markdown
pages, and the next session in any supported harness gets a bounded brief. Handoffs
between agents are typed, with an owner, and claimed once. The README lists more than
twenty harnesses, including Claude Code, Codex, Cursor, Gemini CLI, and OpenCode,
and describes multi-user authentication, per-person attribution, and an audit log.
The default path makes no model calls.

### Where it is stronger

It is the closest in shape to Ghosttree, and in several respects it is ahead. It
supports many more harnesses, including Cursor and Gemini CLI. It has a one-line
installer and packages for several platforms, and its memory is ordinary Markdown
you can read, grep, and edit by hand. It ages memory on a schedule without a
model, and it can carry a session from one harness to another with `ai-memory run`.
It is MIT licensed.

### Where Ghosttree is stronger

A request ledger with acceptance criteria and evidence is not documented in
ai-memory; its handoffs cover the baton between agents, not a record of open work.
Ghosttree also has a room with direct messages, threads, and `@name` mentions for
agents and people, descriptions per file and directory, versioned documents, and
tasks for people. ai-memory's README describes an optional management console for
user administration and session inspection rather than a shared room.

### Together

Both are servers with hooks and an MCP interface, so running both is technically
possible, but the documentation of neither covers it. You would also pay the
context cost twice. If your decisive requirement is a shared memory across many
harnesses, ai-memory fits it better today.

Source: [ai-memory README](https://github.com/akitaonrails/ai-memory).

## MCP Agent Mail

### What it is

A mail-like coordination layer for coding agents. Agents get identities, an
inbox and outbox, threaded messages, advisory file reservations ("leases") on files
or globs, and a pre-commit guard that blocks commits touching files reserved by
another agent. Messages and reservations are stored in Git-backed archives with a
SQLite index. The Rust rewrite, licensed MIT with an OpenAI/Anthropic rider, offers
45 MCP tools, a terminal interface, a web interface for human oversight, and a robot
CLI. It lists Claude Code, Codex, Gemini CLI, Oh My Pi, GitHub Copilot CLI, and any
MCP client. The original Python project is in maintenance mode and points to the
Rust version.

### Where it is stronger

It is built for one thing, agents working in parallel on the same files, and it has
file reservations and a commit guard, which Ghosttree's documentation does not
describe. Its messaging has more features: CC and BCC, acknowledgments, importance
levels. It works with more agent tools.

### Where Ghosttree is stronger

Agent Mail does not claim to be a memory or a work ledger (its installer sets up
the separate Beads task tracker for that). Ghosttree delivers
project knowledge to each new session, keeps a request ledger with handoffs for
interrupted sessions, and ties the room to those records. It has accounts,
organizations, and project roles.

### Together

Yes, and they overlap only on messaging. Using both gives agents two places to
send messages, so decide which one is the room.

Sources: [mcp_agent_mail_rust](https://github.com/Dicklesworthstone/mcp_agent_mail_rust),
[mcp_agent_mail (Python)](https://github.com/Dicklesworthstone/mcp_agent_mail).

## Backlog.md

### What it is

A task manager that stores every task as a Markdown file in a folder of your
repository. Tasks have descriptions, acceptance criteria, a Definition of Done
checklist, milestones, and dependencies. It has a terminal board, a local web
board, fuzzy search, and works with agents through generated instructions or an
optional MCP connector, for Claude Code, Codex, Gemini CLI, Kiro, and others. The
README calls it local-first: no server, no account, no telemetry. MIT licensed.

### Where it is stronger

Tasks live in Git. They go through pull requests next to the code they describe, and
the record stays with the repository. There is nothing to host. It has milestones
and dependencies, a board, and a review flow built around three checkpoints (spec,
plan, code).

### Where Ghosttree is stronger

Ghosttree keeps its ledger out of the repository, which suits teams that do not want
planning files in the tree. It adds interrupted-session handoff, so the next
session sees unfinished work, and it links requests to knowledge and to a room. Its
criteria are closed with evidence; Backlog.md's README documents checkboxes and a
Definition of Done but not evidence attached to them.

### Together

Technically yes, but two ledgers confuse agents about where work is tracked. Pick
one place for work. Backlog.md pairs naturally with a knowledge tool that is not a
task tracker.

Source: [Backlog.md README](https://github.com/MrLesk/Backlog.md).

## Mem0 and OpenMemory

### What it is

Mem0 is a memory layer for AI applications under Apache 2.0, with a Python and
npm library, a self-hosted server, and a hosted platform. It keeps memory at user,
session, and agent level and retrieves it with semantic, keyword, and entity
signals. The README reports 92.5 on LoCoMo and 94.4 on LongMemEval for the managed
platform and says open-source users should expect similar but not identical
numbers. Its documentation has setup guides for coding agents, including Claude
Code, Cursor, Codex, and OpenCode. Mem0's self-hosting guide describes three Docker
containers (API, Postgres with pgvector, Neo4j).

OpenMemory is Mem0's local-first MCP memory server with a built-in dashboard,
according to a Mem0 blog post updated on 25 August 2026. When checked, the
`openmemory` directory no longer existed on the `main` branch of the Mem0 repository,
so check its current state before depending on it. Mem0's hosted MCP server stores
memories in your Mem0 account.

### Where it is stronger

Mem0 is for building products: SDKs, user-level memory for an application's own
customers, a managed cloud with no operations, benchmark results with an open
evaluation framework, and integrations with many frameworks. Ghosttree is not a
library and does not do any of that.

### Where Ghosttree is stronger

Ghosttree is built for coding agents on a development team: per-repository and
per-branch knowledge, a work ledger, a room, file descriptions, and session
transcripts, all on a server you run. Mem0's documentation does not describe a work
ledger or a coordination room for agents and people.

### Together

They solve different problems. Mem0 can give your application memory about its
users while Ghosttree helps the team that builds the application. Using Mem0 as the
memory of your coding agents duplicates part of Ghosttree.

Sources: [mem0 README](https://github.com/mem0ai/mem0),
[Mem0 documentation](https://docs.mem0.ai/introduction),
[OpenMemory MCP announcement](https://mem0.ai/blog/introducing-openmemory-mcp),
[Mem0 MCP](https://docs.mem0.ai/platform/mem0-mcp).

## ByteRover

### What it is

`brv`, a command-line tool and terminal REPL that lets a language model curate
project knowledge into a "context tree". Its README lists a web dashboard, version
control for the tree (branch, commit, merge, push, pull), a review step to approve
or reject pending changes, MCP integration, 20 model providers, and support for more
than 22 coding agents. Everything works locally by default. ByteRover Cloud is a
hosted service for team sync, shared spaces, and member management, and the README
states that its infrastructure is SOC 2 Type II certified. The code is under the
Elastic License 2.0. The README reports 96.1% on LoCoMo and 92.8% on LongMemEval-S
and points to a paper.

### Where it is stronger

Versioning is central: knowledge has branches and merges like code. It supports
many agents, has a published paper behind its benchmarks, and offers hosted team
sync that needs no server of your own.

### Where Ghosttree is stronger

Ghosttree's team sharing runs on a server you control, with no hosted service
involved; ByteRover's README documents sync through ByteRover Cloud and does not
describe a self-hosted sync server. Ghosttree also has the work ledger, the room,
and session transcripts. Both projects are source-available rather than open source.

### Together

Possible in principle, since both inject knowledge and speak MCP. The documentation
of neither covers a combined setup. Choose one as the source of project knowledge.

Source: [byterover-cli README](https://github.com/campfirein/byterover-cli).

## Where Ghosttree is behind

- Harnesses. Ghosttree supports three: Claude Code, Codex, and OpenCode. Cursor and
  Gemini CLI are not among them. Several tools above support many more.
- Transcripts. Session transcript collection covers two of the three harnesses,
  Claude Code and Codex.
- It needs a server. The tools that run locally or live in your repository need
  nothing to host. Ghosttree has no hosted one-line installer yet, and its
  installer covers Linux and macOS, with Windows only inside WSL.
- License. It is source-available, not open source. Personal and noncommercial use
  is free, and small businesses have a [Micro Business Grant](../COMMUNITY-GRANT.md),
  but the PolyForm Noncommercial terms are not an OSI-approved license. Larger
  organizations need a [commercial agreement](../COMMERCIAL.md).
- Maturity. It is pre-1.0. Storage and commands may change between minor releases.
- Capture. Knowledge reaches the shared store when an agent writes it or when a
  person approves what the distiller proposes from past sessions. Ghosttree has no
  fully automatic path like the observation capture in claude-mem or ai-memory.
- Search. Full-text search is the default. Search by meaning needs an embedding
  server you set up.
- Evidence. The internal benchmark was written by the author, tested one model, and
  is not independent. See the README for the numbers and the limits.
