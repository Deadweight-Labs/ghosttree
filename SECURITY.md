# Security policy

## Reporting a vulnerability

Email [security@deadweightlabs.com](mailto:security@deadweightlabs.com). Include
the affected version or commit, a clear reproduction, likely impact, and any
workaround you already know. Do not open a public issue for an undisclosed
vulnerability or include live credentials in a report.

We will acknowledge a report within five business days. Timelines for a fix and
disclosure depend on severity and whether downstream users need time to update.
No bounty program is promised.

## Supported versions

Ghosttree is pre-1.0. Security fixes are made on the latest release line and, at
the maintainers' discretion, on the current release candidate. Older versions
may receive no patch. Upgrade guidance will accompany a release when a database
migration or operational step is required.

## Deployment boundary

The server is intended for loopback or a trusted private network. Its bearer
tokens identify an account. Per-project roles (owner, lead, member, guest) and
project visibility exist, but the server enforces them only when it runs with
`GHOSTTREE_ENFORCE_ACCESS=1`; by default it merely logs what it would refuse.
Do not expose the server directly to the public internet. Use a private network
or a properly configured TLS reverse proxy, protect backups, and rotate a token
if it may have leaked.

Transcript redaction and document secret detection are safeguards, not proof
that stored data is free of secrets. Operators remain responsible for access,
retention, backups, and incident response.
