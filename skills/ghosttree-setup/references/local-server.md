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

**Or snapshot it while it runs.** SQLite can write a consistent copy of a live
database by itself:

    sqlite3 ~/.local/share/ghosttree/ghosttree.db "VACUUM INTO '/somewhere/ghosttree.db'"

This is the same mechanism ghosttree uses internally to back the database up
before a schema rebuild. Prefer it when you are unsure.

After the file is on the new host, point every machine at it with `ctx setup`
and keep going. Nothing else changes.

## What you give up in the meantime

A single-machine server sees only this machine's sessions. That is the whole
difference. Knowledge, ghost files, the ledger and search all work; they just
have one source of transcripts instead of several.
