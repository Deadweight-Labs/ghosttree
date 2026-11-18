# Security policy

## Reporting a vulnerability

Email [security@deadweightlabs.com](mailto:security@deadweightlabs.com). Include
the affected version (`ctx version`) or commit, a clear reproduction, likely
impact, and any workaround you already know. Do not open a public issue for an
undisclosed vulnerability, and do not include live credentials or someone else's
data in a report.

We acknowledge a report within five business days and tell you what we found.
Fix and disclosure are agreed with you and depend on severity and on how long
operators need to update; we aim to publish a fix within 90 days of a report and
credit you if you wish. Research done in good faith on your own installation is
welcome; please do not test against a server you do not run. No bounty program is
promised.

## Supported versions

Ghosttree is pre-1.0. Security fixes are made on the latest release line and, at
the maintainers' discretion, on the current release candidate. Older versions
may receive no patch; a release that has been replaced because of a security
problem says so in the changelog. Upgrade guidance accompanies a release when a
database migration or operational step is required. Update with `ctx update`
(client) and `ctx server update` or the installer (server), which check the
signature of what they install (see below); going back to an older release is a
downgrade and is refused unless you ask for it.

## What Ghosttree assumes, and who it protects against

A Ghosttree server is a multi-user service: people sign in with an identity
provider, an invitation or an email link, belong to organizations and projects
with roles (owner, lead, member, guest) and visibility, and connect machines and
agents with bearer tokens. It can be reached by many people and its install
command is public.

- **Access rules are enforced.** From this version on the server refuses what a
  role does not allow. Only `GHOSTTREE_ENFORCE_ACCESS=0` switches to a log mode
  that, inside an organization, merely logs what it would refuse; that mode is
  for a server with a single user and is not a setup for people who must be kept
  apart. The border between organizations, private transcripts and project
  snapshots hold in both modes.
- **Put the server behind TLS.** Run it on loopback or a private network behind a
  reverse proxy that terminates TLS, set `GHOSTTREE_PUBLIC_URL` to its https
  address (it decides the address in invitations, mails and the join command) and
  name the proxies you trust. Do not expose the plain HTTP port. `/metrics` has no
  sign-in: do not forward it to the internet.
- **The operator is trusted completely.** Whoever runs the server can read
  everything in it, and a machine that connects to it takes in what the server
  says: briefings and knowledge go into the context of the agents on that machine,
  and `ctx update --channel dev` installs a build the server offers (see
  "Checking a release"). Connect only to servers you trust, with accounts you can
  revoke.
- **Members are trusted by role, not unconditionally.** One organization or
  project must not see or change another's data; a guest sees less than a member.
  A member's text (issues, knowledge, transcripts) reaches other people's agents,
  so treat it as input an agent reads, not as instructions you wrote.
- **Secrets are not proof against everything.** Transcript redaction and document
  secret detection are safeguards, not proof that stored data is free of secrets.
  Operators remain responsible for access, retention, backups (keep a database
  backup and its encryption key in different places), and incident response. Rotate
  a token if it may have leaked.
- **Outbound calls.** The server connects to addresses an organization names
  (model providers, GitHub) only to public hosts; it will not reach loopback or
  private networks on an organization's behalf.

## What the server sends to a model provider

Nothing leaves the server for a model unless the operator configured it; each
of these is off until then:

- **"In short" notes** send the text of the knowledge entry, request, pain, task
  or room question that gets a note to the provider the operator named in
  `llm.json` or `GHOSTTREE_EXPLAIN_KEY`, within daily caps. No key, or
  `GHOSTTREE_EXPLAIN=off`: nothing is sent.
- **Session distillation** (`ctx distill-sessions`, run by a timer the operator
  installs) sends excerpts of stored transcripts to the provider of `llm.json`.
  Transcripts are redacted for secrets when they are stored; redaction is a
  safeguard, not a guarantee.
- **Embeddings** send document text to the embedding server at
  `GHOSTTREE_EMBED_URL`. The sample deployment runs it as a container on the same
  machine, reachable on loopback only; an address elsewhere sends the text there.
- **An organization's own AI connection** uses the key and provider that
  organization entered, never the operator's.

The first three are the operator's, for the whole server: there is no switch
per organization yet. Text of every organization on the server can go to the
operator's provider, so an operator tells the people on the server which of
these are on, and an organization that must exclude them runs its own server or
asks the operator to leave them off.

## Checking a release

Every release is signed. `checksums.txt` lists the SHA-256 of each archive and is
signed with the Ghosttree release key (minisign); the signed comment is
`ghosttree <tag>`, so the signed list of one release does not pass for another. It
is also signed keyless with Sigstore (`checksums.txt.sigstore.json`) for people who
want a second, independent check; ctx itself relies on the minisign key.

Release key: not issued yet

When the key is issued its public half (one line of base64, 56 characters) stands
here, in `deploy/downloads/ghosttree/minisign.pub`, in the `pubkey='...'` line of
`install.sh`, and in `internal/releasekey/minisign.pub`, which is built into ctx.
The release workflow refuses to publish unless this file carries the same key, so
compare the key of an `install.sh` you were handed with the one here, on GitHub:
that is a second source that does not depend on the host the script came from.

What checks what:

- **The public installer** (`curl -fsSL https://downloads.deadweightlabs.com/ghosttree/install.sh | sh`)
  checks the minisign signature and the checksum before it installs anything, and
  refuses a release older than the ctx that is installed unless `--allow-downgrade`
  is given. Piping a script into `sh` trusts the host it came from (the key is in
  the script): for a stricter start download `install.sh` from the GitHub release,
  compare its key with the one above and run it.
- **`ctx update`** (stable channel) checks the signature against the key built into
  ctx, the signed tag against the release it was told to install, and the archive
  against the signed list. Nothing is replaced when a check fails.
- **`ctx server update` and `ctx server install`** do the same for the server.
- **`ctx update --channel dev`** takes the build your server offers. It is
  installed without question only if that server also offers a signature of the
  release key; a build the server made itself has none, and then the server alone
  is trusted, which `ctx update` says and only does with `--trust-server`.
  `ctx update --source` builds the newest commit of `dev` on your machine: you
  trust the Git remote and your Go toolchain.
- **The install script a server serves** (`https://<server>/install.sh`, the command
  on the join page) checks only the checksum of that same server and says so: you
  trust the server with the program that runs on your machine. Use the public
  installer to get a release that the key signed.
- **By hand:** `minisign -Vm checksums.txt -p minisign.pub`, then
  `sha256sum -c checksums.txt --ignore-missing`.

What a signature does not give you: it carries no date, so an old signed release
stays genuine. The installer and the updates therefore refuse to go back to an
older version without a flag, and which release is "the newest" comes from GitHub's
unsigned answer; name the release you want with `--version vX.Y.Z` when that
matters.
