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

## Four rules for a bulk run

    The run MAY produce a Synopsis.
    The run MAY only carry over Context from knowledge it was given, with its entry number.
    The run MUST NOT infer Context from the code.
    The run MAY write nothing at all for a file, and this is a normal outcome.

The third rule closes the road from "assembly-line prose" to "apparent
knowledge": a bulk run cannot manufacture Context, it can only pass it along.
Something you believe you can see in the code and cannot source becomes a
question for phase 5 - never a sentence in the tree.

The fourth rule is the one that gets skipped, so read it twice.

## Writing nothing is a result, not a failure

Use `context_describe_file` with `nothing_to_say: true`. The path leaves the
work list, bound to the file's current contents, and comes back on its own if
the file changes.

Reach for it whenever the honest Synopsis would be a restatement of the
signature list: a one-line wrapper, a constants file, a generated-adjacent
shim, a `main.go` that only dispatches. An empty entry costs nothing - the path
is simply marked as looked-at. A restatement costs trust in every OTHER
description, because a reader who finds one padded entry starts skimming all of
them.

**A run that produces zero of these has almost certainly not considered it.**
Measured on the first real pilot: 29 files read, 29 described, not one left
empty - across three directories that certainly contained a few files with
nothing to say. Treat your own count of zero as a signal that you defaulted to
writing, not as evidence that every file was interesting.

## Before you start: the gate

A full inventory run over an entire repository is blocked while REQ-198 is open.
Ghost context is delivered without being asked for and has no hard budget yet; a
run that lifts coverage towards 100% produces exactly the load case that was
knowingly deferred. (Measured: the cost is cumulative PER SESSION, not per read
— a path is delivered at most once per session — and the two-part format turned
out to make descriptions slightly shorter, not longer.)

Pilot runs over single directories are allowed, and are how that budget gets
measured in the first place. If you are about to run a whole repository and
REQ-198 is still open: stop and say so.

**The operator can overrule this, and should be able to.** A gate they cannot
override is a gate built wrong. What must not happen is the gate falling
silently. So when they say run the whole thing:

1. Name the gate and what it guards, in one sentence, before you start.
2. Say what load case this creates — roughly how many paths, and that every
   session in this repository will carry that text from now on.
3. Record it afterwards WITH THE MEASURED NUMBERS. A full run is the measurement
   that budget has been waiting for; letting it happen without writing down the
   result wastes the one chance to get it.

Where to record it: if the gating request belongs to ANOTHER project, do not
edit it — write a decision in THIS project's scope explaining why coverage went
to 100% despite the gate, and let the other project pick it up. Reaching into a
foreign project's ledger is how two teams start overwriting each other.

Refusing quietly and obeying quietly are the same failure: in both cases the
decision leaves no trace.

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

One top-level directory - and the reason matters more than the size, because
the two pull in opposite directions.

**Why a directory and not a file:** so that siblings are read together. "Mirrors
the Apache backend in structure" can only be written by someone who read both.
Split a package of variants across separate readers and each one describes its
variant in isolation, correctly and uselessly.

**Why there is a limit at all:** a reader whose context is full summarises
instead of understanding, and the prose goes flat.

So: **about 15 independent files or 4000 lines is where you start weighing**,
not where you split automatically. Above it, ask which loss is worse. A
directory of six parallel backends stays together even at 19 files, because
splitting destroys exactly what the unit exists for. A directory of unrelated
helpers splits happily at 12.

When you do split, write the parent entry LAST, once the children stand. The
other way round, a directory description is a guess.

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
- **Does the source actually say this?** Read the entry. Do not check that a
  number is present — check that the claim is in it.
- Missing source, or a claim the entry does not support? -> DROP

That third question is the one that earns the critic its keep, and it is not
optional. Measured on a real run: a reader returned four Context lines citing a
genuine entry number for facts that entry does not contain — a plausible
history, a plausible figure, a plausible origin story, all invented, all
correctly formatted, all with a real citation beside them. Every one of them
would have passed a presence check, and each would have been MORE credible than
an honest description with no Context at all.

This is also why the citation rule works. An unsourced claim can only be
doubted; a miscited one can be refuted by reading one entry. The rule does not
make invention impossible - it makes invention cheap to catch. That only pays
off if somebody actually opens the entry.

Three verdicts: KEEP, DROP, LOOK. Only LOOK costs a second read. Dropping is
selective: an unusable Synopsis drops the whole entry, an unusable Context drops
only itself and the Synopsis survives.

## The halt

After the first section always; after that every three. Show the operator three
descriptions verbatim and three numbers:

- Synopses dropped
- Contexts dropped
- paths recorded as `nothing_to_say`

The numbers are the real signal, and two of them are read as warnings rather
than as results:

- **Zero drops** means the critic is not working. Nobody writes twenty
  descriptions without one weak Synopsis.
- **Zero `nothing_to_say`** means rule four did not reach the readers. Say so
  at the halt and put it back in the next brief, in as many words.
- **A Synopsis drop rate above eighty percent** means the inventory run is
  wrong for this repository - say so and stop, rather than producing another
  two hundred.

## If the operator told you to run autonomously

Then the halt does not happen — and the halt was the only place the numbers were
ever looked at. Do not let them disappear with it. Autonomy removes the
CONVERSATION, not the CHECK.

After every section, compute the three numbers anyway and act on them yourself:

- **Synopsis drops above eighty percent** — stop. Do not finish the repository
  and report it afterwards. That threshold means the run is wrong here, and two
  hundred more descriptions make it worse, not more complete.
- **Zero drops, or zero `nothing_to_say`, across a whole section** — put rule
  four and the critic questions back into the next brief verbatim. Something is
  being skipped.

And report all of it at the end, per section, whether or not anyone asked: how
many Synopses and Contexts were dropped, how many paths were left empty, and how
much delivered text the repository now carries per session. An autonomous run
that reports only "done, 270 paths described" has hidden the only evidence
anyone could have judged it by.

## If the session was interrupted or compacted

Read this file again before continuing. The four rules above are the condition
under which a bulk run is permitted at all, and a summary of them keeps the
instruction while losing the edge cases - the entry number on a Context, the
permission to write nothing. The file is on disk; re-reading it costs one tool
call and is cheaper than one invented Context.
