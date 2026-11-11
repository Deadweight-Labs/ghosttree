# Context delivery and the session budget

How Ghosttree decides what an agent sees at the start of a session, with a prompt, and before a tool call, and how it keeps that amount bounded.

## Session budget

Automatic `SessionStart`, `UserPromptSubmit`, and `PreToolUse` context shares a
24,000-character budget per session on each work machine. The count includes
headings, reminders, and the one notice emitted when context is shortened.
Further automatic context is then suppressed; use `context_get` or the local
`.ghosttree/INDEX.md` to retrieve full content explicitly. Repository changes and
restarted hook processes keep the same session budget.

`ctx doctor --only budget` reports recent local output counts against the limit.
It distinguishes confirmed stdout writes from unconfirmed reservations after an
interrupted write; these are not confirmations that the harness used the text.
Accounting starts with the first hook run by the updated client, so earlier
output from an already running session is unknown. Receipts contain counts and a
hashed session identity under `$XDG_STATE_HOME/ghosttree/context-budget` (default
`~/.local/state/ghosttree/context-budget`). Missing session identity or unreadable
accounting state suppresses automatic context. Explicit retrieval still works.

## Context that arrives with a prompt

The context that arrives with a prompt (`UserPromptSubmit`) is picked from the
words of the prompt, and it is deliberately rare. A word only counts if few
knowledge entries name it and it is also unusual in your own recent session text
(only sessions of your own account are counted, never anyone else's), so ordinary
words of either language never fire. An entry arrives
once per session: what the start-up briefing already carried, or an earlier prompt
already brought, is not sent again, and another session gets it afresh. Each entry
names the word that brought it (`(triggered by "word")`). Unconfirmed (staged)
entries and the gardener's notes are never sent this way. For a very rare word
that no entry covers, one hint line may point at a request or session you can read
(`context_search` finds more); it follows the same visibility rules as search.
Everyday German and English words never get a hint, and neither does a word that
more than three of the requests or sessions you can read contain: that is
vocabulary, not a codename. The hint searches the sessions, so it is made only
when the prompt asks about earlier work (a time behind us such as "damals" or
"last week", "did we", "wie hatten wir", or a question about a thing such as
"was ist mit X"); any other prompt gets none.
`ctx usage --relevance --db <path>` replays archived prompts through the rule.

## What was delivered: the delivery log

`context_deliveries` only remembers what a session was already shown, for a week.
The delivery log is what outlives it, so that one can ask how much of the context
the hooks deliver is large, from which route, and whether it is read. It measures;
it changes nothing about what an agent is given.

Each hook call that puts context in front of the model is written down by the
hook, after the session budget has decided how much of the text goes out. A
delivery is divided into parts: one per knowledge entry, per path description,
per runbook and per section of the briefing (ledger, package, tasks, unfinished
work), plus one `frame` part for what is around them (headings, glue, the
truncation notice). The parts add up to exactly the characters the budget
counted, so the log can be checked against the receipt of the session. A part has
a route (`bootstrap`, `relevance`, `hint`, `runbook`, `path`, `subagent`), a kind
(`knowledge`, `ghost`, `runbook`, `ledger`, `feature`, `hint`, `frame`), an object
number or, for a path description, the repository path, and a size. The line of a
hint is made from a word of the prompt, so it is logged as a hint of some size and
nothing else; no text and no word of a prompt is stored anywhere in the log. The
briefing of a subagent is logged under the session that started it, flagged as a
subagent's, though it passes no budget.

The hook appends one line to the session's log directory and sends it with the tool
counts at the end of a turn (Claude Code: `Stop`; Codex: its stop hook), so the
hooks make no extra request. All reports of a flush share one deadline of 1.5
seconds; the first report the server cannot take (no answer in time, a connection
error, 5xx, 401, 408, 429) ends the sending, and that report and every one behind
it stay in the spool for the next flush. A report the server refuses for good (other
4xx) is dropped. A repeated report is stored once. A session logs at most 5,000
rows, and only for a project (the server takes nothing without one).

What one account can have stored for one project in one UTC day is bounded as well,
because `session_ref` and object numbers are free to choose: 20,000 delivery rows
and 900 signal rows (a session writes a few dozen and three). Beyond that nothing is
stored and the report is still answered as accepted; a new day starts a new
allowance. The counts live in `context_log_quota`, and rows of days before
yesterday are removed by the write that finds them.

Rows are kept 180 days. When a row is older, the write that finds it folds it into
the monthly sum of its project, route, kind and object (how often, how many
characters), at most 2,000 rows per write, and drops the path and the session. A
month of a project has at most 2,000 different objects in the sums; objects beyond
that are counted under object 0 of their route and kind, so the totals stay right
and the table grows with months and projects, not with what a caller sends.
Signals are kept a year. No maintenance job is needed.

Each session also records the names and values of `CLAUDE_CODE_ENTRYPOINT`,
`CLAUDE_CODE_SESSION_ATTENDED` and `CLAUDE_CODE_CHILD_SESSION` as the hook process
sees them (an unset variable is recorded as empty), so that it can be seen which
of them separates short runs from working sessions. A value is kept only when it is
a token of at most 32 letters, digits, `_`, `.` and `-`; anything else is recorded as
`(invalid)`. The hook applies this and so does the server.

Reports, from the server, over the projects whose statistics you may see (the same
rule as the tool statistics) or one `--project`; they show sums and no path, object
number or session:

- `ctx usage tools --since 2026-11-10` per tool: sessions, share of the sessions
  of the window, calls (`--actions` per action, `--json`).
- `ctx usage deliveries --since DAY` per route and kind: parts, characters,
  sessions. Months older than the retention are listed as folded sums, only the
  whole months that start inside the window; a month the window cuts into is left
  out, because its sum cannot be split.
- `ctx usage signals --since DAY` per signal value: sessions, and of the finished
  sessions with a stored transcript the share with at most three prompts.
- `ctx usage context --since DAY` per route and kind, and per profile: how many of
  the delivered entries and runbooks the agent used afterwards (see below), and
  their share of the characters.

Only people who may write knowledge in a project can add to its log, like the tool
counts.

## Was the delivered context used

A job in `ctx serve` reads the transcript of a finished session and writes into
the delivery log (`used`, `judged_at`) whether the agent used what a hook
delivered. It works in the background: a few sessions per step, with a pause
between steps, reading through the read pool and writing in one short transaction
at the end of a step. A session counts as finished after an hour without
activity. The job reads only sessions that belong to the account whose rows it
judges, and writes numbers, never text. It judges a part only if the server's own
delivery memory (`context_deliveries`, kept seven days) shows that it handed the
entry to that session: the log is written by the hook, and a reported number the
server did not deliver is left alone, so the result cannot be used to find out
what an entry the reporter may not read says. `GHOSTTREE_USAGE_JUDGE=off` switches it off.

Judged are knowledge entries and runbooks. Only what the agent said and did after
the delivery counts, not what the person wrote and not what tools returned (a
search result names an entry without anyone using it):

| `used` | Meaning | Reading |
|---|---|---|
| 1 | picked up | the agent named the entry by number (`KN-4251`, `#4251`, a link to the entry, a `knowledge_id` lookup) or by two distinguishing words of its title; for a runbook, by its name |
| 2 | followed | a tool call opens a path the entry names, runs a command in backticks that the entry names, or (runbook) runs or reports it |
| 0 | not used | neither |
| -1 | open | not judged yet, or judged without result (no transcript after 14 days, entry gone) |

Path descriptions, hint lines, briefing sections and subagent briefings stay at
-1: they have no number or title to recognize, and the concept measures path
descriptions differently. The reports count them but do not judge them.

An entry for all projects (machine or global) that was delivered in a project and
picked up there gets that project as a tie, which the briefing already respects
(`knowledge_project_affinity`, source `used`). `knowledge_usage` counts per entry
the sessions it was delivered in, picked up in, and followed in.

The signal is a hint. Two title words can be a coincidence in a session about
that very subject, so nothing is lowered, held back or retired because of it; a
check against a sample of judged deliveries comes before any such step.

## Descriptions for removed paths

Descriptions for removed paths stay active until a move is resolved or deletion
is explicitly confirmed. Preview selected paths with `ctx ghost archive <path>`;
archive them with `ctx ghost archive <path> --reason "why it was removed"
--confirm-deleted`. Selection is exact and never recursive, including directories.
The command refuses live paths and detectable moves; `ctx mirror` resolves
unambiguous moves first. Archived text remains available through
`ctx ghost history <path> --voll` and `context_file_history`.
