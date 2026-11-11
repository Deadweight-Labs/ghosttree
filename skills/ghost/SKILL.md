---
name: ghost
description: Short commands between agent and human in ghosttree. Use when the user types /ghost (Claude Code) or $ghost (Codex) followed by open, task, req or preset, or asks for the link to a requirement, task, pain or pull request, wants to be asked to do something step by step, wants a requirement filed, or wants a saved search run.
---

# /ghost - short ways between you and the human

The text after `gt` is `<command> <rest>`. Pick the command, do exactly what it
says, and end with the link to the object, so the human can click it. A link
carries no login: if the browser is signed out, the normal sign-in leads back
to that very page. Never put a token or a one-time login into a link.

## open <what>

Give the human the page of one object.

- `<what>` is `REQ-596`, `REQ-596#2` (a criterion), `TASK-124`, `PAIN-57`,
  `IDEA-3`, `PKG-1`, `KN-4295` (knowledge), `pr:155`, `commit:2d950bf`,
  `room:12`, `session:ID`, `doc:NAME`, `runbook:NAME`, `branch:NAME`,
  `preset:pr-ergebnis 155`, a repository path, a search line such as
  `is:open label:bug`, or `inbox`.
- Call the MCP tool `context_get` with `action=link` and `ref=<what>`. It returns the link and a
  card (title, state, next step, who). It opens nothing.
- Show the card and the link. On the human's own machine you may also run
  `ctx open <what>`: it opens the browser there, and over ssh or without a
  display it only prints the link.
- An object you may not read answers like one that does not exist. Say so;
  do not guess a link.

## task <what the human has to do>

Ask the human for something only they can do, as a task with a walk-through.

1. Call `human_task` with `action=create`: `kind` (secret, file, input or action), a short
   `title`, `location` for a secret or file (where it ends up, never the
   value), and `check_hint` (how you will check it).
2. Always add `steps`: numbered, in the order they are done, one line each,
   plain text, with a `url` where a page helps (a settings page, a file).
   No line breaks, no control or invisible characters, no secret, no login link.
3. Always add `explanation`: two to four plain sentences. What it is about in
   everyday words, why it matters, what has to be done, what you recommend.
   With steps and explanation from you, nothing is generated for this task later.
4. Hand over the link from the reply. The web page shows the steps as a
   checklist the human ticks off. You never mark a task done yourself; when the
   human says it is done, check it against the hint, then `human_task` with `action=confirm` (or `reopen`).

## req <title>

File a requirement.

1. Look first with `request` `action=search` (a title word or two). A match: give its
   link instead (`context_get` `action=link`) and offer to continue it.
2. No match: `request` with `action=create`, `type`, `title`, a `description` that says
   problem, evidence and approach, observable `criteria`, and an `explanation`.
3. Answer with the card and link of the new issue. Criteria are called
   requirements in the web interface and checklist items on a pull request.

## preset <name> [values]

Run a saved search and hand over its link.

1. Call `context_search` with `action=preset_run`, `name`, and the values the search
   takes by name: `pr`, `branch`, `area`, `since`. `preset:pr-ergebnis 155`
   means `name=pr-ergebnis`, `pr=155`. `action=list` shows what exists.
2. Call `context_get` with `action=link` and `ref=preset:<name> <values>` and show the link with a
   few lines of the result.

## Shape of every answer

Keep it short: what you did, the card, the link. The human decides what to
open; do not open a browser yourself and do not paste long listings.
