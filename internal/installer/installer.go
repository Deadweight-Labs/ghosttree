// Package installer wires ghosttree into the agent harnesses. Every change is
// idempotent and merges into existing files instead of replacing them.
package installer

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

type Change struct {
	Path   string `json:"path"`
	Action string `json:"action"`
}

const (
	markerStart = "<!-- ghosttree:start -->"
	markerEnd   = "<!-- ghosttree:end -->"
)

// ruleText is the section both harnesses get: keep operational history out of
// the source tree.
const ruleText = `## ghosttree

ghosttree has twelve MCP tools; each takes an ` + "`action`" + `: ` + "`context_search`" + `,
` + "`context_get`" + `, ` + "`context_remember`" + `, ` + "`request`" + `, ` + "`pain`" + `, ` + "`idea`" + `, ` + "`coord`" + `,
` + "`human_task`" + `, ` + "`package`" + `, ` + "`feature`" + `, ` + "`runbook`" + `, ` + "`comment`" + `.

Code comments explain code to humans. Operational history, failed approaches
and session notes belong in ghosttree via the ` + "`context_remember`" + ` MCP tool
rather than in source comments. Search prior knowledge with ` + "`context_search`" + `
before re-deriving it. Call ` + "`context_get`" + ` again with repository-relative paths
before working in another subtree.

Search before you write. When a known state changes, supersede the old entry
(` + "`context_remember`" + ` with ` + "`supersedes`" + ` and ` + "`supersede_reason`" + `) instead of adding
a follow-up that leaves both active. When what an entry describes no longer
exists, retire it with ` + "`context_remember action=retire`" + ` and a reason.

What a file or directory DOES belongs in its ghost file. Whenever you are about
to write an explanatory comment that serves later readers more than the person
in front of you — and whenever you create a file — put that text at the path
instead, with ` + "`context_remember action=describe_file`" + `. This replaces the comment; it does not
forbid it.

What ghosttree knows about this repository is also on disk, under ` + "`.ghosttree/`" + `:
` + "`.ghosttree/tree/`" + ` holds one ` + "`.md`" + ` per file and a ` + "`__dir.md`" + ` per directory,
` + "`.ghosttree/knowledge/`" + ` holds the pitfalls, decisions and notes a session here
would be given, ` + "`.ghosttree/requests/`" + ` holds the work ledger split into open
and done, ` + "`.ghosttree/docs/`" + ` holds the generated document projection, and
` + "`.ghosttree/edit/`" + ` is the writable document worktree. Start at ` + "`.ghosttree/INDEX.md`" + `, which also names what
is deliberately NOT mirrored there. Reading it with ls, cat and grep costs no
tool call and is the cheapest way into an unfamiliar part of the codebase —
` + "`grep -ril \"topic\" .ghosttree/knowledge/`" + ` before ` + "`context_search`" + `. It is a
projection: editing it changes nothing, and it is only as fresh as its last
write, which the index states.

Long-form specifications, plans, investigations and reports belong in the
document worktree. Create or import them with ` + "`ctx doc new`" + ` and
` + "`ctx doc import`" + `, publish revisions with ` + "`ctx doc push`" + `, and keep
the source tree clean; never commit specs or plans as internal working files.

For substantial feature, architecture, migration, or multi-session work, use
` + "`request action=search`" + ` before implementation. Continue a matching request and
associate the current session, or create a request with explicit acceptance
criteria. Trivial local fixes and routine maintenance do not require a request.
Mark criteria and requests complete only with concrete evidence.

Messages to you from people and agents in this project arrive in the hook
context, under "ghosttree: new messages for you", once each. Answer with
` + "`coord action=thread_reply`" + ` inside a thread or ` + "`coord action=send`" + ` (set ` + "`mention`" + ` to the
sender); ` + "`coord action=inbox`" + ` lists everything in the room and marks what is
addressed to you. Write ` + "`@name`" + ` in a message to address a person or agent.

When you start work on a PR or branch, say so with ` + "`coord action=status`" + ` (one
sentence, at most 200 characters, for example "PR #77 feat/mac-sync, room sync");
update it when it changes and clear it with an empty note when you are done.
Others see it as self-reported next to your name in ` + "`coord action=peers`" + ` and on your
messages. Never put a secret in it. ` + "`@agents`" + ` in a message reaches every agent
of the project seen working in the last 15 minutes.

A problem without a solution belongs in ` + "`pain action=record`" + `, not in a request
(requests need criteria). Never set a pain to resolved just because a request
is finished: use ` + "`pain action=update`" + ` with evidence, and for mitigated with ` + "`remaining`" + `.

A wish you voice in passing, without working it out, belongs in ` + "`idea action=record`" + `
(look in ` + "`idea action=search`" + ` first); ` + "`idea action=promote`" + ` makes a request of it.
` + "`context_search`" + ` also takes qualifiers (is:open, label:bug, area:internal/store,
pr:12, branch:...; the tool description lists them), and ` + "`context_search action=preset_run`" + ` runs saved
searches such as pr-ergebnis (what was done in a pull request).

What only the human can provide (a key, a file, a decision, a manual step) you
create with ` + "`human_task action=create`" + `, with where it lives and how to check it, instead
of saying it in chat. Never write a secret or file contents into it, only where
it lives; token patterns are refused (a heuristic, not a guarantee). Never set
a task to done yourself. Check what the human marked done, then call
` + "`human_task`" + ` with action confirm or reopen.

Give a task ` + "`steps`" + ` (numbered, one line each, with a link where a page helps) and an
` + "`explanation`" + `: the person then gets a checklist, and nothing is generated for the task
later. Every answer about one object ends with its web link and a short card; hand
that link to the person instead of describing where to click. ` + "`context_get action=link`" + ` gives link
and card for REQ-n, TASK-n, PAIN-n, #id, pr:n or preset:name values. Short commands:
` + "`/ghost open|task|req|preset`" + ` in Claude Code, ` + "`$ghost ...`" + ` in Codex.

The people who read what you write change things but do not dive into every small
finding of an agent. So give each entry a short ` + "`explanation`" + `: ` + "`context_remember`" + `
(every entry), ` + "`request action=create`" + `, ` + "`pain action=record`" + `, ` + "`human_task action=create`" + `, and
` + "`coord action=send`" + ` with intent question. Two to four plain sentences in the language
the project's people use: what it is about in everyday words, why it matters
(who it affects, when), what has to be done or decided, and your recommendation.
No markdown, no secret. You know the context; a text the server generates later
only knows the entry.`

func section(body string) string {
	return markerStart + "\n" + body + "\n" + markerEnd + "\n"
}

// replaceSection swaps the marked section for body, or appends it.
func replaceSection(content, body string) string {
	block := section(body)
	start := strings.Index(content, markerStart)
	end := strings.Index(content, markerEnd)
	if start >= 0 && end > start {
		return content[:start] + block + content[end+len(markerEnd)+1:]
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if content != "" {
		content += "\n"
	}
	return content + block
}

func writeMarkerFile(path, body string) (Change, error) {
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return Change{}, err
	}
	updated := replaceSection(string(old), body)
	if string(old) == updated {
		return Change{Path: path, Action: "unchanged"}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Change{}, err
	}
	action := "updated"
	if len(old) == 0 {
		action = "created"
	}
	return Change{Path: path, Action: action}, writeAtomic(path, []byte(updated), 0o644)
}

func writeAtomic(path string, data []byte, perm os.FileMode) error {
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp := path + ".ghosttree.tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func jsonValuesEqual(left, right any) bool {
	return reflect.DeepEqual(left, right)
}

// readJSONFile returns an empty object for a missing file so callers can merge
// unconditionally, but refuses to touch a file it cannot parse.
func readJSONFile(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func writeJSONFile(path string, m map[string]any) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeAtomic(path, append(b, '\n'), 0o644)
}

// HasMarker reports whether a file already carries the ghosttree section.
func HasMarker(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(b), markerStart)
}
