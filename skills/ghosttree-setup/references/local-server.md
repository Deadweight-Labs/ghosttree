# A local single-machine server

Use this when no ghosttree server exists yet. It gets you a working install in
minutes instead of after a deployment, and it does not paint you into a corner.

## Start it

    mkdir -p ~/.local/share/ghosttree
    ctx serve --db ~/.local/share/ghosttree/ghosttree.db --listen 127.0.0.1:8474

Bound to loopback on purpose. There is no authentication story that makes a
bare-network ghosttree a good idea, and the token is about attribution - who
wrote something - not about separating rights.

Run it as a user service rather than in a terminal you will close; the units in
`deploy/` are the template.

Then create a person and take the token, which is printed once and never again:

    ctx person add <your-name> --db ~/.local/share/ghosttree/ghosttree.db

And connect this machine to it, exactly as you would to a remote one:

    ctx setup --server http://127.0.0.1:8474 --token <token>

## Moving to a networked host later

Logically this is not a migration. It is the same SQLite file, and the schema
creates itself on open.

Physically it is not a copy either, and this is where people lose data. A
running SQLite database in WAL mode is not a single file: recent writes live in
a sidecar journal that has not been folded back in yet. `cp ghosttree.db
elsewhere` gives you a snapshot that is missing them, and it will look fine
until it does not.

Two correct ways:

**Stop the service, then copy.**

    systemctl --user stop ghosttree
    cp ~/.local/share/ghosttree/ghosttree.db /somewhere/

**Or snapshot it while it runs.** The `sqlite3` CLI can copy a live database
page by page, in a consistent state, with its backup command:

    sqlite3 ~/.local/share/ghosttree/ghosttree.db ".backup '/somewhere/ghosttree.db'"
    chmod 600 /somewhere/ghosttree.db

The copy has the permissions of your umask; the database holds transcripts, so
close it to others. Check it with ghosttree's own command, not with SQLite's
`PRAGMA integrity_check`:

    ctx db check --db /somewhere/ghosttree.db

Do not use `VACUUM INTO` with the `sqlite3` CLI, and do not run `VACUUM` or
`PRAGMA integrity_check` with it either. The tables of the context snapshots
have CHECK constraints that call an SQL function only `ctx` registers
(`ghosttree_valid_utf8`), so the CLI stops with `no such function`; `VACUUM INTO`
leaves an empty file at the target. `.backup` copies pages and never evaluates
those constraints, which is why it works. `ctx db vacuum` shrinks the file in
place and is not a way to make a copy.

After the file is on the new host, point every machine at it with `ctx setup`
and keep going. Nothing else changes.

## What you give up in the meantime

A single-machine server sees only this machine's sessions. That is the whole
difference. Knowledge, ghost files, the ledger and search all work; they just
have one source of transcripts instead of several.
