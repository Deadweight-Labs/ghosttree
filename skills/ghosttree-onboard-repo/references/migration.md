# Phase 1 and 2 - what is already written down somewhere

## Phase 1a: the files `ctx migrate` knows about

    ctx migrate --dry-run <repo>

Go through the candidates with the operator before writing anything. Then:

    ctx migrate <repo>

Cleaning up is a separate command, run separately, with its own consent:

    ctx migrate --clean <repo>

It only removes files whose migration provenance exists. Do not run it in the
same breath as the migration itself, and never without asking.

## Phase 1b: the files it does not see, which is most of them

This is the part that matters, because the gap is invisible. `ctx migrate`
reports success either way.

What its scanner actually covers:

- six rule filenames: `CLAUDE.md`, `AGENTS.md`, `GEMINI.md`, `.cursorrules`,
  `.windsurfrules`, `CONVENTIONS.md`
- `.md` files under `docs/` or `.superpowers/` whose name contains "spec" or
  "plan"

What it therefore misses, and what you should go looking for by hand:

- a `README` with a gotchas, caveats or troubleshooting section
- architecture decision records - `doc/adr/`, `docs/decisions/`, `adr/`
- `NOTES.md`, `HACKING.md`, `CONTRIBUTING.md`, `docs/architecture.md`
- `docs/*.md` that happen not to have "spec" or "plan" in the filename
- long comment blocks in the source that explain operations rather than code -
  deployment steps, a workaround and why, an ordering that must not change

Read them, propose entries, let the operator decide, write with
`context_remember`. Only after that may the passages leave the repository, and
only with consent.

Types: a rule that binds behaviour is an `instruction`; a choice with a reason
is a `decision`; something that went wrong and will again is a `pitfall`;
everything else is a `note`.

## Phase 2a: TODO/FIXME in the source

    grep -rn "TODO\|FIXME\|XXX\|HACK" --include="*.*" . | head -50

Most of these are noise. The ones worth having are the ones with a reason
attached - "FIXME: this breaks when the queue is empty, see #412" is a pitfall
waiting to be written down; "TODO: refactor" is not.

## Phase 2b: the issue tracker, if there is one you can reach

This step is an adapter, not a fixed part of the flow. Run it only when both
are true:

    git remote get-url origin     # points at github.com
    gh auth status                # gh is installed and logged in

If either fails, skip the step and SAY that you skipped it, in one sentence. A
skill that quietly does one thing less on a GitLab or Gitea repository is how a
harness-neutral tool turns into a GitHub tool without anyone deciding to.

When it does run:

    gh issue list --state open --limit 50
    gh issue list --state closed --limit 30

Open issues can become ledger entries. Closed ones with a real post-mortem in
the comments can become pitfalls.

**Do not import.** Take entries over one at a time, each with a reason for why
it belongs in ghosttree rather than where it already is. A ledger that mirrors
the issue tracker is a second place for the same truth, and having two makes
both untrustworthy - nobody knows which one is current. What belongs here is
what someone EXPLAINED, not what someone OPENED.
