# Phase 4 - the inventory run

Read this whole file before you start. The format below is the reason a bulk
run is allowed at all.

## A description has two parts

They carry different burdens of proof, and confusing them is how a ghost tree
turns into either an empty shell or a pile of confident invention.

    Synopsis:
    Entry point for the HTTP API. Builds the router, attaches middleware
    and owns server startup and shutdown.

    Context:
    Graceful shutdown must happen before the event bus closes; reversing
    that order dropped final events in production. (from #1204)

**Synopsis** - may be derived from the code, and should be. It answers: what is
this file responsible for, and is it worth opening. This is the navigation
layer, and it is the reason the tree exists at all - understanding a codebase
without reading it, including for models with a small context window.

What it must NOT be is a retelling of the implementation:

    Defines Server.
    Has method Start().
    Has method Stop().
    Calls router.New().

That is worse than nothing. It occupies the place where the answer should be,
and a reader who finds it there stops looking. The distinction is not derivable
versus non-derivable - it is RESPONSIBILITY versus SYNTAX.

**Context** - need not be derivable, and must not be invented. Non-obvious
invariants, decisions with a reason, pitfalls, history. It is optional; most
files do not have one. When it is there, it names where it came from.

## Three rules for a bulk run

    The run MAY produce a Synopsis.
    The run MAY only carry over Context from knowledge it was given, with its entry number.
    The run MUST NOT infer Context from the code.

The third rule is the one that matters. It closes the road from "assembly-line
prose" to "apparent knowledge": a bulk run cannot manufacture Context, it can
only pass it along. Something you believe you can see in the code and cannot
source becomes a question for phase 5 - never a sentence in the tree.

Writing nothing is a legitimate result. Use `context_describe_file` with
`nothing_to_say: true`. The path leaves the work list, bound to the file's
current contents, and comes back if the file changes. An empty entry costs
nothing; a restatement costs trust in every other description.

## Before you start: the gate

A full inventory run over an entire repository is blocked while REQ-198 is open.
Ghost context is delivered on every file read and has no hard budget yet; a run
that lifts coverage towards 100% produces exactly the load case that was
knowingly deferred, and the two-part format makes each description longer on top
of that.

Pilot runs over single directories are allowed, and are how that budget gets
measured in the first place. If you are about to run a whole repository and
REQ-198 is still open: stop and say so.

## Pick a mode

Ask the operator once:

- **inline** - you read and describe, section by section. Best coherence,
  hits the context window on anything large.
- **subagents** - one subagent per top-level directory.
- **workflow** - same unit, deterministic orchestration, resumable.

Offer workflow only if the tool exists here; if it does not, say so in one
sentence rather than silently dropping it. A missing option looks like a
non-existent one.

All three run the same protocol and write the same thing. The mode decides who
reads, not what results.

## The unit of work

One top-level directory. If it is too large - more than about 15 independent
files or 4000 lines - split it into subdirectories, and write the parent entry
LAST, once the children stand. The other way round, a directory description is
a guess.

The work list is `.ghosttree/tree/**/__dir.md`, the "Noch nicht beschrieben"
group. Incidental files - tests, testdata, generated code - are already filtered
out of it. A full run therefore means "everything the tree tracks
individually", not "every file".

## The brief for a directory subagent

Four parts:

1. The open files of that section.
2. The knowledge from phases 1 to 3 relevant to it, WITH ENTRY NUMBERS. Those
   numbers are the source a Context has to carry.
3. The one-line summaries of the sibling directories, from `__dir.md`.
4. The rules: read every file completely; run `git log --oneline -- <dir>`;
   write a Synopsis that names responsibility rather than syntax; write a
   Context only if part 2 supplies one, with its number.

**The subagent does not write.** It returns drafts. There is no delete: once a
description is in the tree it can only be overwritten, so a critic that judges
after the write could paint over a mistake but never remove it. Ten files of
drafts is roughly 1500 tokens through the main context - that is the price of
only reviewed material reaching the tree.

## The critic

A second agent, working on the text. Its questions are chosen so they can be
answered WITHOUT the code - "is this derivable from the source" cannot be, by
anyone who has not read the source.

For each Synopsis:

- Does it name a responsibility rather than syntax?
- Is it short?
- Does it help decide whether this file matters for a task?

For each Context:

- Does it contain a concrete, non-obvious fact?
- Is a source given?
- Missing source, or a claim that does not look sourced? -> LOOK

Three verdicts: KEEP, DROP, LOOK. Only LOOK costs a second read. Dropping is
selective: an unusable Synopsis drops the whole entry, an unusable Context drops
only itself and the Synopsis survives.

## The halt

After the first section always; after that every three. Show the operator three
descriptions verbatim and three numbers:

- Synopses dropped
- Contexts dropped
- paths recorded as `nothing_to_say`

The numbers are the real signal. Zero drops means the critic is not working. A
Synopsis drop rate above eighty percent means the inventory run is wrong for
this repository - say so and stop, rather than producing another two hundred.
