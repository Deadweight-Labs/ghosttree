# Coordination in Claude Code

How messages between agents and people reach a running Claude Code session, what was measured, and where the channel stops working.

## Coordination messages in Claude Code (opt-in)

`ctx claude [claude args...]` starts Claude Code with the ghosttree channel
loaded. The aim is that `ctx coord` messages and agent mentions reach the
session: a waiting session wakes up, a working one receives the message at
its next tool result. Measured with Claude Code 2.1.284 (2026-09-30) and
2.1.286 (2026-10-01, with the handshake rule below).
The launcher generates one identity per launch (`claude:<host>:<uuid>`), writes a
temporary MCP config for the `ghosttree-channel` server (`ctx channel --agent
<id>`), and runs `claude --mcp-config <tmp> --dangerously-load-development-channels
server:ghosttree-channel ...`. The identity also goes to the process environment
as `GHOSTTREE_AGENT_ID`, which `ctx mcp` reads before any harness session id, so
the channel and the `coord_*` tools act as the same agent. The variable changes
only the coordination identity; search, interrupted-work handoff, and snapshots
keep the harness session id. A `codex` or `opencode` started inside such a
session ignores the variable (its provider prefix does not match); a `claude`
started inside it without the launcher cannot be told apart and shares the
identity. Arguments after the
launcher's own flags (`--dry-run`, `--agent <id>`) pass through unchanged, the
exit code is claude's, and the config file is removed afterwards.
`ctx claude --dry-run` (or `GHOSTTREE_CLAUDE_DRY_RUN=1`) prints the command and
config without starting anything. Claude answers with the channel's `reply`
tool, which stores the answer and marks the message `acked`. It starts a new
conversation with the channel's `send` tool: `text` (required), `mention` (list
of agent ids), `room` (`project`, the default, or `machine`) and `intent`
(`question`, `approval`, `blocker`, `handoff` or `ack`; the first four need a
mention). No second `ctx mcp` entry is needed for that.

This is opt-in at start: `ctx install claude` does not register the channel, and
a running session cannot be attached later. The channel is a Claude Code
research-preview feature; ghosttree is not on Anthropic's allowlist, so
`--dangerously-load-development-channels` is required and Claude asks you to
confirm it at every start. Team and Enterprise plans can switch channels off
(`channelsEnabled`).

### ghclaude: the channel without typing `ctx claude`

`ghclaude` is a link to `ctx` in the same directory. When ctx is started under
that name it runs `claude` with the channel and passes every argument through
unchanged (it replaces itself with claude, so exit code, signals and
stdin/stdout are claude's). It looks for `claude` like `ctx claude` does
(`GHOSTTREE_CLAUDE_BIN`, then `PATH`) and, because a GUI such as T3 often has a
shorter `PATH`, next to itself. `ctx install claude` and `ctx join` ask once
(`Install ghclaude? [Y/n]`, on `/dev/tty`; without a terminal nothing is created
and the command to run is printed). `ctx install ghclaude` creates it without a
question, `ctx uninstall ghclaude` removes only that link, and `ctx doctor`
checks that it leads to ctx. A shell alias would not do: T3 starts Claude
through the Agent SDK with a path to the executable, and an alias hides what runs.

Measured with Claude Code 2.1.296 (2026-10-16, claude.ai login, `--model sonnet`,
local server, throwaway ghosttree config): the channel works only in an
interactive session. A non-interactive `claude` (`-p`, also with
`--input-format stream-json --output-format stream-json`, the way the Agent SDK
starts it) accepts `--dangerously-load-development-channels` without error, shows no
confirmation dialog and does not block, but never registers the channel: the
debug log has no `Channel notifications registered`, and a message sent to the
idle session stays `injected` and starts no turn (the interactive run in the same
setup was `acked` and answered). So `ghclaude` set as the Claude executable in T3
starts the session but does not wake it; it wakes idle agents in a terminal.
Channels also need a claude.ai login: with `ANTHROPIC_AUTH_TOKEN` and a custom
`ANTHROPIC_BASE_URL` Claude Code reports `channels feature is not currently
available` even interactively. `--resume` under `-p` was not run separately; it
takes the same code path as the other `-p` runs.

What was measured (Claude Code 2.1.284, `ctx channel --capabilities`):

| Capability | State |
| --- | --- |
| `receive_at_safe_point` | measured |
| `wake_idle_session` | measured |
| `receive_for_named_subagent` | gap |
| `human_steer` | gap |
| `human_interrupt` | gap |
| `activity_observation` | gap |

Delivery is at-most-once: a message is claimed atomically before it is sent and
never re-sent, so a crash between claim and write loses it. A reply wakes the
agent that asked when its original message was a request: a message with the
intent `question`, `approval`, `blocker` or `handoff`, or a message that is not
itself a reply and mentions the replier by name (the mention `reply` adds
automatically does not count). A reply to a reply, such as a thanks, does not
wake, so a chain ends after one answer. To ask again after an answer (for
example a re-review after a fix), call `reply` with `intent` set to `question`,
`approval`, `blocker` or `handoff`, or use `send` with a mention; a `reply`
without intent wakes nobody. In direct and group rooms, which have no mentions, any plain message counts as
a request. `send` with a mention and `reply` with an attention intent accept at most 10 messages per minute and 30
per 15 minutes per channel process and refuse beyond that; the receiving
session also has a coordination budget of 12000 characters per 5 minutes, which
short messages exhaust only late. `<channel` in message text is rewritten to
`&lt;channel` so a body cannot imitate a channel event. The limits are
spelled out in `internal/claudechannel/doc.go`. The status line may say "no MCP
server configured with that name" while delivery works.

Channels need the old `initialize` connection. Claude Code 2.1.286 first sends
`server/discover`; if the server answers with MCP 2026-07-28, Claude Code
registers no channel for that connection. The channel server therefore refuses
`server/discover` with JSON-RPC -32601, and Claude Code falls back to
`initialize` with 2025-11-25. Measured with Claude Code 2.1.286 on 2026-10-01;
a later release may behave differently. `scripts/verify-claude-channel.sh` is a
manual tmux check against a real session (not part of CI). It passes `--model
opus`, because the model in a user's settings may be one Claude Code rejects.
