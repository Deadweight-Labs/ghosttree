# Proving the install, instead of assuming it

An exit code of 0 means a command ran. It does not mean anything reached a
model. Those two came apart here twice, and both times the install looked
finished for a long time.

The first time, ghosttree's hooks were written into Codex's configuration
correctly, `ctx doctor` found them, and they never ran once - Codex requires
every unmanaged command hook to be trusted a single time, keyed by a hash of its
definition, and these had never been trusted. From outside, "ran and returned
nothing" and "never ran at all" look identical. That went on for 482 sessions.

The second time the hook ran but was filtered out by a tool-name matcher that
did not match anything Codex actually emits.

Neither would have survived Proof 3. That is why it is not optional.

## Proof 1 - the connection stands

    ctx status

Shows a server, a reachable one, and a body of knowledge. Zero entries in a
fresh install is fine; an error is not.

## Proof 2 - nothing is wired wrong

    ctx doctor

Quiet means: hooks registered where the harness looks for them, MCP registered,
rule text present and not drifted, and no orphaned ghost descriptions. Anything
it prints is a finding, not a warning to skim past.

## Proof 3 - context actually arrives

Start a NEW session in a repository and check two things:

- The ghosttree MCP tools are offered (`context_search`, `context_get`,
  `context_remember`, `context_describe_file`, the `request_*` family).
- Something from ghosttree appeared at the start of the session without anyone
  asking for it - known context, a ledger line, or a note that the project is
  empty.

If the tools are missing, the MCP registration did not take, or the harness has
not been restarted. If the tools are there but no context appeared, the hook is
the problem, not the MCP registration - they are separate channels and they fail
separately.

## Why you cannot do Proof 3 now

The MCP server is started as a child process of the session that uses it, and it
lives as long as that session. A registration written during this session
reaches the next one. The same is true for a replaced binary: every process that
is already running keeps the old inode.

So: leave criterion 3 open on the setup request, say plainly that it is open,
and let the next session close it. An open item that ghosttree knows about will
come back on its own. An open item mentioned only in a chat message will not.
