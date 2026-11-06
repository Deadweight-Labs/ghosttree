# Documents and migration

Long-form specs and plans live outside Git as versioned documents. This page covers the document workflow and the local inventory that precedes a migration.

## Writing a document

Drafts live under `.ghosttree/edit/`, which Ghosttree excludes from Git. The
server only sees a document when you push it.

```bash
ctx doc new spec storage-redesign
# edit the printed Markdown path with any editor
ctx doc push storage-redesign -m "Describe the storage boundary"
ctx doc log storage-redesign
```

Existing Markdown can be imported with an exact source hash. `--clean` removes
the source only after the server exposes proof for that same hash and stored
revision.

```bash
ctx doc import path/to/design.md --kind spec --slug storage-redesign --clean
```

`ctx doc search "words from the title or the text"` finds the active documents of
the project (`--all-projects` for every project you may read), best first. With an
embedding server that embeds documents it also finds one that says the same in
other words; a hit that only the meaning found is marked `[match: semantic]`
(see `deploy/README.md`, "Embeddings for hybrid search").

## Inspecting a repository before migration

```bash
ctx migrate --dry-run /path/to/repo
```

The dry run is a local inventory with document validation. It lists candidates,
skipped Markdown files with reasons, and unscanned directories such as dependencies,
embedded checkouts, and `.ghosttree` state. It requires no client or model
configuration and makes no network calls or writes. Existing migration records
and distilled rule entries are evaluated when you run `ctx migrate <repo>`.

Every Markdown file under `docs/` is a document candidate, preserved byte for byte.
Root-level files such as `CONTRIBUTING.md` remain visible exclusions; review them
and use `ctx doc import <file> --kind other` for documents you choose to preserve.
Normal migration and cleanup print the same coverage report. Cleanup remains a
separate `ctx migrate --clean <repo>` operation requiring stored source evidence.
