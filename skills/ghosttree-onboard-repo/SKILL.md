---
name: ghosttree-onboard-repo
description: Bring a repository into ghosttree - migrate its agent markdown, capture what is known but never written down, and describe its files. Use when a repository is new to ghosttree, when its ghost tree is mostly empty, or when someone asks to onboard, migrate, or document a codebase into ghosttree.
---

# Bring a repository into ghosttree

Five phases, in this order. The order is the design, not a suggestion: by the
time you describe files, you should already know what this project knows.

## Before you start

Check that `ghosttree-setup` is finished on this machine, including its third
proof - a fresh session receiving context. If it is not, stop and say so. Half
of this skill against a broken install produces work nobody can find later.

Then open one ledger entry for the whole job, with one criterion per phase:

    request_create
      title: "Onboard <repository> into ghosttree"
      type:  change
      criteria:
        1. existing agent markdown migrated or deliberately skipped
        2. scattered artifacts reviewed (TODO/FIXME, issue tracker if reachable)
        3. what is known but unwritten captured from the operator
        4. inventory run done for the agreed scope
        5. open questions from the run answered

This is the thread across sessions. Stop whenever you like; the ledger entry
comes back on its own, and the tree itself records how far the run got.

## The five phases

**1. What is already written down** - `references/migration.md`
`ctx migrate` for the files it knows about, then the ones it does not see. Its
scanner is narrower than it looks, and its success message does not say so.

**2. What is scattered around the repository** - `references/migration.md`
TODO/FIXME in the source, and the issue tracker if there is one you can reach.
Taken over one at a time with a reason. Never imported wholesale.

**3. What nobody has written down** - `references/interview.md`
Git archaeology as a memory aid, then a guided interview with the operator.
This is the only source for the Context part of a description, so skipping it
means the inventory run can produce Synopsis and nothing else.

**4. The inventory run** - `references/inventory-run.md`
Describing the files, with the knowledge from phases 1 to 3 in hand. Read that
file before starting: it carries the two-part description format and three rules
that are not negotiable.

**5. The questions that came back** - `references/acceptance.md`
Subagents will hit things they can see but cannot source. Those become questions
for the operator, not sentences in the tree.

## The four states of a path

This is why stopping costs nothing. Each path in the tree is in exactly one
state, and the state is derived, not stored twice:

| state            | meaning                                                        |
|------------------|----------------------------------------------------------------|
| `undescribed`    | never looked at - on the work list                              |
| `reviewed-empty` | looked at, deliberately left empty, bound to the file's blob    |
| `described`      | has a description, blob still matches                           |
| `stale`          | has a description, the file has changed since                   |

`reviewed-empty` is what makes a second run cheap: a path someone read and
deliberately left alone is not read again until the file changes. It shows up in
`.ghosttree/tree/**/__dir.md` as its own group, next to the work list. Watch that
group - if it grows faster than the described one, the criteria are too strict.

## What this skill does not do

- Import an issue tracker. The ledger must not become a second copy of one.
- Delete anything automatically. `ctx migrate --clean` is its own step with its
  own consent.
- Run the full inventory over an entire repository while REQ-198 is open. See
  `references/inventory-run.md`.
