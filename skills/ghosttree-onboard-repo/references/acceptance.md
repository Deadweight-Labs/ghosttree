# Phase 5, and what finished looks like

## Phase 5 - the questions that came back

Every subagent that read a directory properly will have hit something it could
see but could not source: a special case with no visible reason, an ordering
that looks arbitrary, a check that guards against something no longer obvious.
Under the three rules those could not go into a description. They go here.

**Answer what you can yourself first.** There are two kinds of question in that
pile, and only one of them belongs to the operator:

- *Does the code do X?* - answerable by reading. Go and read. "Is there really a
  re-check under the lock" is a question about the file, not about history.
- *Why does it do X?* - not answerable by reading, ever. That one is theirs.

Bringing someone a list padded with questions you could have answered wastes the
only attention you get.

Then bring the rest as a list. This is the part an interview does not produce,
because the question does not occur to anyone until they have looked at the
code.

The answers become knowledge with `context_remember`. After that - and only
after that - they may be added as Context to the affected description, with the
new entry number attached.

## When the operator cannot answer either

Expect this, especially on a cold repository. "No idea" is a legitimate answer
and it arrives more often than not.

**Do not let the questions evaporate.** Write them into the tree as a single
`note`, naming the paths they concern and stating plainly that nobody has
answered them yet. That the answer is unknown is itself a fact worth having:
the next person who trips over the same odd construct starts from "someone
already noticed this and could not explain it" instead of from zero, and does
not spend an afternoon rediscovering the question.

A list of open questions living only in a chat message is gone tomorrow. That
is the one outcome that makes the tree read as more complete than it is.

## The four states, again

You will look at this list to decide whether the job is done:

| state            | meaning                                                       |
|------------------|---------------------------------------------------------------|
| `undescribed`    | never looked at - still on the work list                       |
| `reviewed-empty` | looked at, deliberately left empty, bound to the file's blob   |
| `described`      | has a description, blob still matches                          |
| `stale`          | has a description, the file has changed since                  |

`reviewed-empty` expires by itself. The decision was about one version of the
file, so a change puts the path back on the work list. It is not permanent, and
it is not a verdict on the path - only on the version somebody read.

## Done, for the scope that was agreed

- Every phase criterion on the ledger entry is ticked, with evidence.
- No path is left in a state nobody chose: everything in the agreed scope is
  `described` or `reviewed-empty`.
- The three numbers from the last halt are recorded on the request - Synopses
  dropped, Contexts dropped, paths left empty. They are what a later run needs
  in order to know whether the criteria were right.
- The open questions from phase 5 are either answered and written down, or
  listed as still open. Silently dropping them is the one outcome that makes the
  tree read as more complete than it is.

## And what is deliberately not done

If the full inventory run was blocked by REQ-198, say that plainly in the
handoff. A repository with three directories described and the rest untouched is
a fine state to be in - as long as the tree says so, which it does, because
`undescribed` is visible in every `__dir.md`.
