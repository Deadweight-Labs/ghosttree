# ghosttree

A lifecycle-managed knowledge tree that lives next to your git repo instead of
inside it. Coding agents read and write it over MCP, session transcripts from
every machine land in one searchable place, and the repo itself stays free of
CLAUDE.md, AGENTS.md, plan markdown and the kind of comment that documents an
agent's afternoon rather than the code. Self-hosted, harness-neutral, invisible
on GitHub.

## How it works

One Go binary, `ctx`, serves the whole local side:

- `ctx serve` — the server: SQLite with FTS5 behind a small REST API, bearer
  token per person, meant to sit on a private network (a private network here).
- `ctx watch` — the collector: tails Claude Code and Codex transcripts, redacts
  credentials before anything leaves the machine, uploads incrementally and
  replays whatever an outage left behind.
- `ctx mcp` — the MCP server agents talk to. Its tools cover context and search,
  file descriptions and their history, regression coverage, session lookup,
  and the request ledger.
- `ctx install claude|codex|opencode` — wires each harness independently.
  Claude and Codex support `mcp`, `hooks`, `rules`, and `skills`; OpenCode
  supports `mcp` and `rules`. Repeat `--only` to install individual components.
  Existing configuration and locally edited skills are preserved.
- `ctx doctor` — verifies exact configuration and runnable behavior. It can
  scope checks and repairs by harness and component, performs a real MCP stdio
  handshake plus `context_get`, executes synthetic hook probes, and reports
  separately whether a real harness invocation has been observed.

### Scope axes

Every knowledge entry and session carries three optional, independent axes:

```text
project:  unset | normalized git remote (github.com/owner/repo)
branch:   unset | branch name (only meaningful with project)
machine:  unset | hostname
```

Unset means "applies everywhere along that axis". A session reads the union of
everything that applies to it: global, machine, project, project+branch,
project+machine. Writing follows defaults per entry type, so an agent only has
to say what kind of thing it learned, not where to file it.

### Conditional instructions

Instructions may be gated by repository-relative path. Session startup uses the
current working directory; call `context_get` again with `paths` before working
in another subtree. Several paths are OR. Search still finds inactive rules and
shows their gates.

The task gate that used to sit next to the path gate is gone (measured: 17 path
gates against 0 task gates over 25 instructions). A path is objectively
determinable and the server sees it; a task is a self-assessment nobody makes.

## Usage

```bash
# server (once, on the host that keeps the data)
ctx serve --db /var/lib/ghosttree/ghosttree.db --listen <private-host>:8474
ctx person add <name> --db /var/lib/ghosttree/ghosttree.db   # prints a token once

# every machine
ctx setup --server http://<host>:8474 --token <token>
ctx install claude
ctx install codex
ctx install opencode
ctx install codex --only hooks
ctx install claude --only mcp --only skills
ctx watch --once        # import existing transcripts
ctx watch               # or run as a user service, see deploy/
ctx status
ctx doctor
ctx doctor codex --only mcp
ctx doctor claude --only hooks --fix
```

Doctor reports `OK`, `FAIL`, or `UNVERIFIED`. `UNVERIFIED` means the local
configuration or synthetic path can be checked, but only a real future harness
event can supply the remaining evidence. After changing Codex hooks, run
`/hooks`, trust the ghosttree entries, and start a fresh session.

See `deploy/` for the systemd units and the deployment notes.

## Building

```bash
make test
make build        # ./dist/ctx
make build-all    # linux/amd64 and darwin/arm64, both cgo-free
```

Dependencies: `modernc.org/sqlite`, `github.com/modelcontextprotocol/go-sdk`,
`github.com/fsnotify/fsnotify`. Everything else is the standard library, and
the binary builds with `CGO_ENABLED=0`.

Redaction is a regex baseline, not a guarantee, which is why session data stays
on private infrastructure.
