# Operations

Running a Ghosttree server: serving the installer, the interface language, privacy and security notes, and the TLS and reverse-proxy boundary. For systemd units, backups and maintenance commands see [`deploy/README.md`](../deploy/README.md).

## Serving the installer

`ctx serve` can hand out the `ctx` binaries itself, so a new machine needs one
command. Build the archives, put them where the server can read them, and point
the server at that directory:

```sh
make dist                                  # dist/release/ctx_<version>_<os>_<arch>.tar.gz + checksums.txt
GHOSTTREE_DIST_DIR=/srv/ghosttree-dist ctx serve ...   # or --dist-dir
```

The server then answers `GET /install.sh` and `GET /dist/<file>` (only files
listed in `checksums.txt`; nothing else in the directory is reachable). Without
the setting both routes return 404. A machine installs with

```sh
curl -fsSL https://<server>/install.sh | sh
```

The script picks the build for Linux or macOS (amd64, arm64; Windows only
inside WSL), downloads the archive and `checksums.txt`, verifies the SHA-256,
and installs `ctx` to `~/.local/bin` (`$XDG_BIN_HOME` if set), without sudo. The
server address in the script is `GHOSTTREE_PUBLIC_URL`, or else the address the
request came in on. `GHOSTTREE_DOWNLOAD_BASE` on the client replaces the
download location, for example with a GitHub release URL, since the archives
use goreleaser's layout. The installer refuses a plain-http source (except
loopback, or `GHOSTTREE_ALLOW_HTTP=1`), and the server serves `/install.sh`
only for an https address: set `GHOSTTREE_PUBLIC_URL`, or let a trusted proxy
send `X-Forwarded-Proto` and `X-Forwarded-Host`. `/dist/` has no rate limit of
its own; limit it at the reverse proxy. Keep one ctx version per directory.
Cautious readers can fetch the script, read it, and
run it afterwards.

## Interface language

The web interface speaks English by default. `GHOSTTREE_LANGUAGE=de` (or
`ctx serve --language de`) switches the whole interface, including the sign-in
and invitation pages, dates and times, to German. The language is resolved per
request; today it comes from this server setting. Text that people or agents
wrote is shown as written, and the texts meant for agents (bootstrap, MCP
descriptions, rules) stay English.

## Privacy and security

Ghosttree may hold source paths, prompts, command output, and operational
history. Treat the server as private infrastructure:

- bind it to loopback or a trusted private interface;
- use TLS at a reverse proxy when traffic crosses an untrusted network;
- run the reverse proxy on the same host, or list its network in
  `--trusted-proxies`: forwarding headers are believed only from loopback and
  from those networks (see "Running behind TLS or a reverse proxy");
- keep person tokens out of repositories and logs;
- back up the SQLite database and test restores;
- review retention and access rules for your team.

The collector redacts common credential formats before upload, and document
writes reject suspected secrets instead of rewriting them. Regex redaction is a
last line of defense, not a guarantee. The current bearer token identifies who
made a change. Snapshot reads, creates, and release bindings additionally
require explicit per-project capabilities. Per-project roles (owner, lead,
member, guest) decide what an account may read and write, but they are only
enforced when the server runs with `GHOSTTREE_ENFORCE_ACCESS=1`; without it the
server only logs what it would have refused.

Security reports belong at
[security@deadweightlabs.com](mailto:security@deadweightlabs.com), not in a
public issue. See [SECURITY.md](../SECURITY.md).

## Running behind TLS or a reverse proxy

`ctx serve` speaks plain HTTP. The browser session cookie, and the OIDC flow
cookie, are always `HttpOnly` and `SameSite=Lax`. They get `Secure` when any of
these holds:

1. the connection itself is TLS;
2. `--public-url` (env `GHOSTTREE_PUBLIC_URL`) is an `https://` URL, or the
   OIDC redirect URL is `https://`;
3. the request carries exactly one `X-Forwarded-Proto: https` and its TCP peer
   is a trusted proxy.

The login and the logout cookie are built the same way and always carry the
same attributes. Otherwise the cookie is not `Secure`, so a private HTTP
deployment (loopback, VPN) keeps working with no extra setting. A browser never
sends a `Secure` cookie over HTTP, so an https setup that gets this wrong shows
up as a login loop rather than a silent downgrade.

Trusted proxies are loopback (a proxy on the same host) plus whatever you list
in `--trusted-proxies` / `GHOSTTREE_TRUSTED_PROXIES`, a comma-separated list of
CIDRs or addresses. A client outside that list cannot switch `Secure` on with
a header, and its forwarding headers also make the same-origin check on form
posts fail. List only the proxy's own address, never a network that ordinary
clients share. `/0`, IPv4-mapped prefixes and zoned addresses are rejected at
startup.

Loopback is trusted by default. Anything that makes a remote client appear as a
loopback peer (`ssh -L`, `socat`, a Docker port mapping or other local
forwarder) therefore also gets its forwarding headers believed. If you run such
forwarders, add the word `none` to the list (`GHOSTTREE_TRUSTED_PROXIES=none`,
or `none,192.0.2.10`) to switch loopback trust off and trust only what you list.
With `none`, a proxy on the same host is no longer believed either, so forwarding
headers from it are ignored; enter that proxy's address explicitly in the list.

The client address used to rate-limit device logins comes from
`X-Forwarded-For`, read only when the direct peer is trusted: all header lines
are joined and read from right to left, trusted proxies are skipped, and the
first untrusted address is the client. Entries a client sent itself therefore
stay to the left and are ignored.

Recommended setup behind a TLS terminator on another host:

```bash
GHOSTTREE_PUBLIC_URL=https://ghosttree.example.com
GHOSTTREE_TRUSTED_PROXIES=192.0.2.10        # the proxy's address as seen by ctx serve
GHOSTTREE_LISTEN=192.0.2.20:8474            # private interface, not a public one
```

The proxy must forward `Host`, `X-Forwarded-Proto` and `X-Forwarded-Host`, and
must overwrite those headers rather than append to client-supplied ones:

```nginx
location / {
    proxy_pass http://192.0.2.20:8474;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Host $host;
    proxy_set_header X-Forwarded-For $remote_addr;
}
```

`X-Forwarded-For $remote_addr` overwrites whatever the client sent and is the
recommended form. With `$proxy_add_x_forwarded_for` (append) ghosttree still
picks the proxy-added entry, but only if the proxy is in `--trusted-proxies`.

`--public-url` is the simplest switch: with it every cookie is `Secure` no
matter what headers arrive, and requests whose `Origin` equals it are accepted
even if the proxy forwards no headers. It also fixes the base URL printed by the
device login. Use `--trusted-proxies` alone when one server answers under
several host names. `ctx serve --help` lists both flags.

## Test runs never write to a real server

Set `GHOSTTREE_HOOKS=off` (also `0` or `false`) for every agent a test harness
starts. Every hook then exits at once without a server call, `ctx watch`
does not start, and `ctx mcp` only reads. Independently of that, a remote that names no shared repository
(a local path, a `file://` URL, a checkout under a temporary or cache folder, or
a host under `.invalid`, `.test`, `.example`, `localhost`) never becomes a
project: the collector treats such a folder as one without a remote, and the
server answers `invalid_remote` for writes and stores a session from it without
its project.

## Short helper runs: the lean profile

Every session carries about 55 KB of fixed context from ghosttree: the tool
list, the rule section and the briefing at the start. A reviewer, a script that
calls `claude -p` or any other short helper run needs little of it. Set
`GHOSTTREE_PROFILE=lean` in the environment of such an agent (the variable
reaches `ctx mcp` and the hooks through the process that starts the agent; the
installed hooks need no change):

| | `full` (default) | `lean` |
|---|---|---|
| tools loaded up front | `context_search`, `context_get`, `context_remember`, `request`, `pain`, `idea`, `coord`, `human_task`, `package` (34.9 KB) | the first four (15.8 KB) |
| briefing at session start | everything it has | binding instructions, at most three pitfalls of the project, 1,500 characters |
| context the hooks may add | 24,000 characters | 6,000 characters |

Tools not loaded up front (`feature`, `runbook` and `comment` always, the rest
under `lean`) stay in the tool list by name, and every one answers under either
profile. What a harness does with that differs:

- Claude Code loads such a tool on demand through tool search and keeps the four
  of the core tier up front (the `anthropic/alwaysLoad` mark in `tools/list`).
  The measurements in the changelog are from Claude Code.
- Codex defers all MCP tools behind its own `tool_search` when the model and the
  provider support it, and lists them directly otherwise
  ([openai/codex#29486](https://github.com/openai/codex/pull/29486)). Whether it
  reads the `alwaysLoad` mark is not stated there, so do not expect the saving
  of the tool list under Codex to match the figures for Claude Code.
- Any other harness loads the tools the way it does.

The profile is set on purpose only: without the variable, and with a value other
than `lean` or `full`, the session is `full`. `ctx status` shows which profile
applies to the shell it runs in, and `ctx doctor --only budget` names the hook
budget of both profiles ("bis zu 24.000 (lean: 6.000)") next to the profile of
that shell; a budget receipt does not record the profile of its session. The
profile of a session that a harness started with its own environment is not
visible from there.

### What the profile does not change

- **`context_get` without paths** returns the whole briefing under `lean` too.
  Asking for it with a tool is deliberate, and the notice that closes a spent
  hook budget names `context_get` as the way to the rest. A lean session that
  wants the short briefing already has it from its start.
- **Subagents** do not follow the profile. The briefing the SubagentStart hook
  puts in front of a Claude Code subagent (up to 6,000 characters: the working
  rules, the work package, matching pitfalls, the mirror) and the rules in it
  are the same under `lean` and `full`, and they do not count against the hook
  budget of the session.
- **The coordination channel** keeps its own budget (12,000 characters per five
  minutes) under both profiles; messages from other agents arrive as before.

### How the hook budget treats a runbook

A runbook that matches a prompt, a command or a path is delivered once per
session, so it must not be cut: the server remembers it as delivered, and it
would never come again. The hook therefore tells the server how many characters
of the budget are left, and the server claims a runbook only if it fits whole
(shortened to the steps' titles if need be, with the line that asks for the
report). One that does not fit is neither sent nor remembered and waits for a
later call. In the text of a hook the runbook comes before other knowledge, and
if the budget has to cut, it cuts after the last whole line and leaves no half
runbook. A hook that runs at the very moment another uses up the budget can
still lose the race for the last characters; the cut is then still clean, but
the runbook has been remembered.

## The gardener's schedule

Owners switch the gardener on per organization or person in the settings
(Gardener) and set how often, which repositories, the budget per run and per
month, and the rules of automatic approval. The gardener's sandboxed unit asks
the server what is due and reports each run:

```sh
ctx garden run-due --repos-dir /path/to/checkouts --state-file /path/state.json \
  --out-dir /path/reports -- --mode agent --propose --sessions 4 \
  --per-session 30 --turns 60 --no-mcp --claude /path/claude --claude-dir /path/claude-home
```

Run it from a timer every hour; most runs find nothing due. Each repository
needs a checkout at `<repos-dir>/<owner>/<name>`. The automatic approvals run
inside the server every ten minutes, on the authority of the owner who switched
the rules on, and each one can be undone on the Gardener tab.
