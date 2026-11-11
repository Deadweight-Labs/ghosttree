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

## Descriptions for removed paths

Descriptions for removed paths stay active until a move is resolved or deletion
is explicitly confirmed. Preview selected paths with `ctx ghost archive <path>`;
archive them with `ctx ghost archive <path> --reason "why it was removed"
--confirm-deleted`. Selection is exact and never recursive, including directories.
The command refuses live paths and detectable moves; `ctx mirror` resolves
unambiguous moves first. Archived text remains available through
`ctx ghost history <path> --voll` and `context_file_history`.
