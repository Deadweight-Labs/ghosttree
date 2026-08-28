---
name: ghosttree-setup
description: Set up ghosttree on this machine - connect it to an existing server or start a local single-machine one, wire up the agent harnesses, and prove that context actually arrives. Use when ghosttree is not installed here yet, when `ctx status` fails, or when hooks and MCP tools are registered but nothing shows up in a session.
---

# Set up ghosttree on this machine

Run this once per machine. It ends with an open item you cannot close today, and
that is deliberate - see Proof 3.

## 1. Find out where you are

Do not skip this. Three questions, three commands:

    cat ~/.config/ghosttree/config.json   # is there a config, and which server?
    ctx status                            # does that server answer?
    type -a ctx                           # every ctx on PATH, not just the first

`type -a` and not `command -v ctx` plus `ctx version`. The second pair cannot
find a second installation: `ctx version` runs exactly the binary `command -v`
just found, so it agrees with itself no matter how many others exist.

The failure this guards against is not really about PATH, it is about running
processes. Replacing the file creates a new inode; every process already running
keeps the old one. So also ask:

    systemctl --user show ghosttree-watch -p ExecStart   # which binary the service runs
    pgrep -a ctx                                         # what is running right now

If these disagree, say so and stop. The fix is `systemctl --user restart
ghosttree-watch`, and MCP tool changes only take effect in the NEXT session
anyway, because the MCP server is a child process of the session that started
it.

## 2. Pick a path

**A server already exists** - you have a URL and a token:

    ctx setup --server <url> --token <token>
    ctx install claude          # and/or: codex, opencode
    ctx watch --once            # import the transcripts already on this machine

Install only what is needed when repairing one channel:

    ctx install codex --only hooks
    ctx install claude --only mcp --only skills

Codex hook definitions require one-time acceptance. After installing or
changing them, run `/hooks`, trust the ghosttree entries, and use a fresh
session.

**No server yet** - read `references/local-server.md`. It starts a local one on
this machine in a few commands, and explains what a later move to a networked
host actually involves. Do not improvise that move: a running SQLite database is
not one file.

Then set up the long-running collector as a user service; see `deploy/` in the
ghosttree repository.

## 3. Prove it, do not assume it

Read `references/verification.md` before you report success. In short: three
pieces of proof, and passing commands are not one of them.

1. `ctx status` shows a connection and a body of knowledge.
2. `ctx doctor` shows no `FAIL`; every claim it can prove is `OK`, and anything
   requiring a future real harness event is explicitly `UNVERIFIED`.
3. In a FRESH session, the MCP tools are there and the session-start hook
   delivered something.

Proof 3 cannot be produced in the session that did the installing. So do not
claim it. Instead, leave the state behind where ghosttree itself can see it:

    request_create
      title:  "ghosttree setup on <hostname>"
      type:   change
      criteria:
        1. ctx status shows a connection
        2. ctx doctor is quiet
        3. a fresh session receives context and has the MCP tools

Tick criteria 1 and 2 with `request_record_progress` before you finish. Leave 3
open. The next session on this machine gets it back automatically as
interrupted work, which is the whole point: an onboarding skill that ends with
"remember to check X later" and leaves no trace is asking a human to be the
database.

For a focused repair, use the matching proof instead of rerunning everything:

    ctx doctor codex --only mcp
    ctx doctor claude --only hooks --fix

## What this skill does not do

Set up a server on a DIFFERENT machine. That is a deployment, it touches a host
nobody here has inspected, and it lives in the ghosttree repository as a
document rather than as an automated step.
