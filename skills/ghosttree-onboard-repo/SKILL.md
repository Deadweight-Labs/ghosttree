---
name: ghosttree-onboard-repo
description: Bring a repository into ghosttree - migrate its agent markdown, capture what is known but never written down, and describe its files. Use when a repository is new to ghosttree, when its ghost tree is mostly empty, or when someone asks to onboard, migrate, or document a codebase into ghosttree.
---

# Bring a repository into ghosttree

Five phases, in this order. The order is the design, not a suggestion: by the
time you describe files, you should already know what this project knows.

## Before you start

Three checks, in this order. Each of them fails later and more confusingly if
you skip it now.

**1. Is `ghosttree-setup` finished here**, including its third proof - a fresh
session receiving context? If not, stop and say so. Half of this skill against a
broken install produces work nobody can find later.

**2. Does this repository have an `origin` remote?**

    git remote get-url origin

ghosttree derives the project axis from it. Without one there is no project,
nothing to hang knowledge or descriptions on, and `ctx migrate` refuses outright
with "repository has no origin remote". Stop here and say what the options are:
add a remote, or accept that this repository cannot be onboarded yet. Do not
start phase 1 and discover it there.

**3. Has the collector seen this session yet?**

`request_start_work` needs a session the collector has already uploaded, and
`ctx watch` lags by design at the beginning of a session. If linking fails,
that is expected: note that you will attach the session later and carry on. It
is not a reason to stop, and it is not a broken install.

Then open one ledger entry for the whole job, with one criterion per phase.

Long-form specifications, plans, investigations and reports belong in
`.ghosttree/edit/` and are published with `ctx doc push`. Existing files enter
through `ctx doc import`. Internal working documents must not be committed to
the repository; the generated `.ghosttree/docs/` tree is a disposable
projection, not an editing surface.

**Put a resume line in its description**, word for word:

    On resuming: re-read SKILL.md (three prohibitions) and the reference for
    the current phase before continuing.

That line is not decoration. This skill spans hours and its context WILL be
compacted at least once; when that happens, the instruction to re-read lives in
the file you have stopped reading, and cannot reach you. The ledger entry can:
interrupted work is pushed into a session unasked, so a pointer parked there
comes back on its own. A pointer and not a copy of the rules — two copies drift,
and the copy would be the one that got compacted.

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

## Three rules that hold everywhere in this skill

These are here, in the file every session reads, and not only in the reference
that covers their phase. A summary keeps instructions and drops their edge
cases, so anything that must survive an interruption belongs where it cannot be
summarised away.

**1. Never import an issue tracker wholesale.** Not "import fewer", not "import
the good ones" — take entries over ONE AT A TIME, each with a stated reason why
it belongs in ghosttree rather than where it already is. If the count of new
ledger entries matches the count of open issues, you have built a mirror, and a
mirror makes both copies untrustworthy: close the issue and the ledger entry
lives on, and nobody can tell which one is current. What belongs here is what
someone EXPLAINED, not what someone OPENED.

**2. Never invent Context.** A description may carry non-obvious knowledge only
when that knowledge was handed to you, with its entry number. What you think you
see in the code and cannot source is a question, not a sentence in the tree.

**3. Never delete without a separate consent.** `ctx migrate --clean` is its own
step, asked for on its own.

If the operator instructs you to do any of these anyway, that instruction wins —
they know their repository. But SAY SO first, in one sentence, naming which rule
you are setting aside and why. A rule that falls silently teaches nobody
anything, and the next session cannot tell an override from a mistake.

## After an interruption or a compaction

Re-read the reference for the phase you are in before continuing — the files are
on disk and re-reading one costs a single tool call. The three rules above are
what a summary erodes first, and each of them is a rule about what NOT to do,
which is exactly the kind that disappears when text gets compressed.
