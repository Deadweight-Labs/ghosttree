# Phase 3 - what nobody has written down

This phase produces the only material the inventory run is allowed to use for
the Context part of a description. Skip it and the run can produce Synopsis and
nothing else - which is an honest result, but a much thinner one.

## First: git archaeology, as a memory aid

You are not looking for facts here. You are looking for prompts - things that
will make the operator remember something they stopped noticing years ago.

    git log --oneline --grep="revert" -i | head -20
    git log --oneline --grep="^fix\|hotfix\|urgent" -i | head -30
    git log --format="%H %s" --numstat | ...   # files touched most often
    git log --format="%h %s%n%b" | awk 'length($0) > 200'   # unusually long bodies

What each signal tends to mean:

- **Reverts** - something looked right and was not. The reason is rarely in the
  revert message.
- **Repeated fixes to the same path** - a place that is harder than it looks. A
  code path repaired three times is one nobody explains any more, because
  everyone involved considers it common knowledge.
- **Long commit bodies** - somebody already tried to explain something and
  picked the only place available.

Bring the findings to the operator **as questions**, never as finished pitfalls.
A three-times-repaired code path is a suspicion, not a finding. Writing it up
yourself is exactly the invention this whole design is built to prevent - and it
would carry your confidence into the tree without your uncertainty.

Ask like this: "`internal/queue/drain.go` has been fixed four times in a year,
twice with a revert in between. Is there something about it that is not visible
in the code?"

## Then: the interview

Five axes. Ask them one at a time, and follow up.

1. What has gone wrong here more than once?
2. Which decision do you find yourself explaining over and over?
3. What would somebody new get wrong on their second day?
4. What looks like a bug in this code and is actually deliberate?
5. What is the most expensive mistake somebody could make in this repository?

Question 4 usually produces the best entries. It is the one thing that is
guaranteed not to be derivable from the code, because the code is what makes it
look wrong.

## Writing it down

Only what the operator confirmed, and with them as the author. That is what
makes an entry legitimately trusted rather than a guess wearing a confident
tone.

    context_remember
      type:       pitfall | decision | note
      scope_hint: project   (usually - branch only if it dies with the branch)

For a decision, cover why it was taken, what was rejected, and what the
trade-off was. A decision that records only its outcome cannot be revisited,
because the reason is exactly what a future reader needs.

Note the entry numbers as you go. The inventory run needs them: a Context is
only allowed into a description with its source attached.
