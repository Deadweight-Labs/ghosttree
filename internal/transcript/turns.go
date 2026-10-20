package transcript

import (
	"regexp"
	"slices"
	"strings"
)

// The texts of a session that say what it was about: what the user typed, and the
// answer the agent ended each turn with. Everything else in a transcript is
// bookkeeping (tool calls and their results, messages between two calls, notices
// of background jobs, summaries after a compaction), and a session search that
// embedded it would find the bookkeeping. Measured on the stock of one
// installation these are 7 % of the chunks that carry text.
//
// The chunk index is the source, not session_chunks.text: the collector writes a
// line "[tool call: ...]" there for every call, so that column holds mostly
// calls. chunk_index.role, is_prompt and tool_calls know the difference.

// Parts of a session that are embedded.
const (
	PartPrompt = "prompt"
	PartFinal  = "final"
)

var leadTag = regexp.MustCompile(`^<([A-Za-z][A-Za-z0-9_:-]{0,40})[ >/\n]`)

// injectedTags are the tags that the harnesses put into user lines; a text that
// starts with one of them was not typed.
var injectedTags = map[string]bool{
	"system-reminder": true, "task-notification": true, "local-command-caveat": true,
	"local-command-stdout": true, "local-command-stderr": true, "command-name": true,
	"command-message": true, "command-args": true, "user-prompt-submit-hook": true,
	"environment_context": true, "ide_opened_file": true, "ide_selection": true,
	"bash-input": true, "bash-stdout": true, "bash-stderr": true, "subagent_notification": true,
	"turn_aborted": true, "user_instructions": true, "ghosttree": true, "tool-use-id": true,
	"persisted-output": true, "task-id": true, "command-stdout": true, "system": true,
	"skill-name": true, "new-diagnostics": true, "hook_additional_context": true,
}

// ClassifyPrompt says what the text of a user line is: "prompt" for something a
// person typed, anything else for what a harness injected. The result is a label
// that is safe to print (a tag name or a fixed word, never content).
func ClassifyPrompt(s string) string {
	t := strings.TrimSpace(s)
	switch {
	case t == "":
		return "empty"
	case strings.HasPrefix(t, "[SYSTEM NOTIFICATION"):
		return "system-notification"
	case strings.HasPrefix(t, "[Request interrupted"):
		return "interrupt"
	case strings.HasPrefix(t, "Caveat:"):
		return "caveat"
	case strings.HasPrefix(t, "This session is being continued from a previous conversation"):
		return "compaction-summary"
	case strings.HasPrefix(t, "Stop hook feedback"), strings.HasPrefix(t, "UserPromptSubmit hook"):
		return "hook-feedback"
	}
	if m := leadTag.FindStringSubmatch(t); m != nil {
		if injectedTags[m[1]] {
			return "tag:" + m[1]
		}
		return "unknown-tag:" + m[1]
	}
	return "prompt"
}

// TypedPrompt says whether a label of ClassifyPrompt is a text a person typed.
// A leading tag that no harness is known to inject is more often pasted markup
// than an injection; it counts as typed.
func TypedPrompt(label string) bool {
	return label == "prompt" || strings.HasPrefix(label, "unknown-tag:")
}

// TurnRow is the part of a chunk index row the selection reads.
type TurnRow struct {
	Chunk     int64
	Seq       int
	Role      string
	Prompt    bool
	ToolCalls int
	// Text is the indexed text; it is only read for rows with a role.
	Text string
}

// Picked is a text to embed: a row of the session and what it is.
type Picked struct {
	TurnRow
	// Part is PartPrompt or PartFinal.
	Part string
	Turn int
	// Open is set for the answer of the last turn: no prompt has followed it yet,
	// so it is the final answer only as far as the session stays as it is. The
	// caller decides when it has stood long enough.
	Open bool
}

// PickTurnTexts picks the texts of a session: each prompt a person typed, and
// the last text the agent wrote in each turn. A turn starts with such a prompt
// (the answers before the first one are a turn of their own) and ends at the
// next. A harness notice inside a turn (a background job that reports, an
// interrupt marker) does not end it. A final answer that has a tool call after
// it counts: the turn ended on the call, by a stop or an error. The rows are
// those of one session, in any order; the result is in order of the turns, the
// prompt before the answer.
func PickTurnTexts(rows []TurnRow) []Picked {
	rows = slices.Clone(rows)
	slices.SortStableFunc(rows, func(a, b TurnRow) int { return a.Seq - b.Seq })
	type turn struct{ prompt, last *TurnRow }
	var turns []turn
	cur := turn{}
	for i := range rows {
		r := &rows[i]
		switch {
		case r.Prompt && r.Role == "user":
			if !TypedPrompt(ClassifyPrompt(r.Text)) {
				continue
			}
			if cur.prompt != nil || cur.last != nil {
				turns = append(turns, cur)
			}
			cur = turn{prompt: r}
		case r.Role == "assistant":
			cur.last = r
		}
	}
	if cur.prompt != nil || cur.last != nil {
		turns = append(turns, cur)
	}
	var out []Picked
	for i, t := range turns {
		if t.prompt != nil {
			out = append(out, Picked{TurnRow: *t.prompt, Part: PartPrompt, Turn: i})
		}
		if t.last != nil {
			out = append(out, Picked{TurnRow: *t.last, Part: PartFinal, Turn: i, Open: i == len(turns)-1})
		}
	}
	return out
}
