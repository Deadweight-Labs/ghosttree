package sessiondistill

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The requests mode reads what a person asked for, not what the work produced.
//
// It exists because wishes are voiced in passing and then lost. Somebody says
// "eigentlich hätte ich da gern auch noch X" while the session is about
// something else; the agent finishes the something else, and X is gone with the
// context window. Nobody wrote it down because at that moment it was an aside.
//
// This mode reads only what a person typed. Not the agent's suggestions, not
// what it decided to do next: an assistant proposing work and a person asking
// for it look identical once both are in a transcript, and only one of them is
// a requirement.
const requestSystem = `Read a person's messages to a coding agent and return the things they asked
for that are not yet in the backlog. Return JSON {"items":[]}. Each item has
type (feature, change, bug, or investigation), title, body, chunk_seq, quote,
and optionally same_as. The quote must be an exact contiguous substring of that
single chunk.

Take wishes, feature ideas, complaints about how something behaves, and
constraints stated as expectations. Take them even when they were said in
passing and the session went on to do something else — that is the case this
exists for.

Do NOT take: instructions for the task at hand ("run the tests now", "commit
this"), questions, corrections of the agent's last answer, or anything the
agent proposed rather than the person. Steering the current work is not a
requirement about the product.

The title is what the person wants, in their words where possible, not a
restatement of the whole message. The body says what was asked for and any
condition attached to it. Do not invent acceptance criteria, scope or priority:
that is a commitment, and the person did not make it.

If the backlog already covers a wish, do not restate it. Return the item with
"same_as" set to that entry's number and a quote from this transcript. Saying a
thing a second time is the strongest evidence there is that it was meant, and an
entry marked [done] or [dropped] being asked for again is worth knowing.

Return at most 4 items. Prefer no item over a weak one; an empty list is a
correct answer and the usual one.`

// RequestPromptVersion is versioned separately from the knowledge prompt: the
// two modes read different inputs for different purposes and their generations
// have no reason to move together.
//
// req-v1 — the original rules.
const RequestPromptVersion = "req-v1"

// MaxRequestItems bounds one transcript's yield. A session is a conversation,
// not a requirements workshop; a reply naming ten wishes has started
// paraphrasing the work rather than reading the asks.
const MaxRequestItems = 4

type requestWire struct {
	Items []store.DistilledRequest `json:"items"`
}

// UserChunks keeps only what a person typed. Everything else in a transcript is
// the agent's own voice, and mining it would turn the agent's suggestions into
// the person's requirements.
func UserChunks(chunks []store.Chunk) []store.Chunk {
	launched := commandLaunches(chunks)
	out := make([]store.Chunk, 0, len(chunks))
	for _, c := range chunks {
		if c.Role != "user" {
			continue
		}
		// Harness plumbing arrives as a user turn but nobody typed it: task
		// notifications, plugin listings, the echo of a slash command.
		text := strings.TrimSpace(c.Text)
		if text == "" || strings.HasPrefix(text, "<") {
			continue
		}
		// A command or skill expands into a plain user turn with no marker of
		// its own. REQ-128 was filed as a person's wish from the /init
		// boilerplate — "Please analyze this codebase and create a CLAUDE.md
		// file" — which nobody typed. The turn is recognisable by its parent:
		// it hangs off the tool result that launched the command.
		if parent := parentTurn(c.Raw); parent != "" && launched[parent] {
			continue
		}
		out = append(out, c)
	}
	return out
}

// commandLaunches collects the turns that started a command or a skill. Their
// own text is empty, so they never reach the prompt themselves; what matters is
// that everything hanging off them is machine-written.
func commandLaunches(chunks []store.Chunk) map[string]bool {
	launched := map[string]bool{}
	for _, c := range chunks {
		var turn struct {
			UUID          string `json:"uuid"`
			ToolUseResult struct {
				CommandName string `json:"commandName"`
			} `json:"toolUseResult"`
		}
		// A transcript line that does not parse, or whose toolUseResult is a
		// bare string rather than an object, simply launches nothing.
		if json.Unmarshal([]byte(c.Raw), &turn) != nil {
			continue
		}
		if turn.UUID != "" && turn.ToolUseResult.CommandName != "" {
			launched[turn.UUID] = true
		}
	}
	return launched
}

func parentTurn(raw string) string {
	var turn struct {
		ParentUUID string `json:"parentUuid"`
	}
	if json.Unmarshal([]byte(raw), &turn) != nil {
		return ""
	}
	return turn.ParentUUID
}

func RequestSystemPrompt() string { return requestSystem }

// RequestPrompt renders the user message for one session.
func RequestPrompt(chunks []store.Chunk, backlog []string) string {
	var transcript strings.Builder
	for _, c := range chunks {
		fmt.Fprintf(&transcript, "[chunk %d]\n%s\n\n", c.Seq, c.Text)
	}
	known := "(the backlog is empty)"
	if len(backlog) > 0 {
		known = "- " + strings.Join(backlog, "\n- ")
	}
	return "Backlog already recorded for this project:\n" + known +
		"\n\nWhat the person said:\n" + transcript.String()
}

// ParseRequests validates a reply against the transcript it came from. An item
// that cannot quote its chunk is dropped; a wish nobody voiced would become a
// task somebody works on.
func ParseRequests(raw string, quoted []store.Chunk) ([]store.DistilledRequest, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(strings.TrimSpace(raw), "```")
	var result requestWire
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("decode request distillation: %w", err)
	}
	bySeq := map[int]string{}
	for _, c := range quoted {
		bySeq[c.Seq] = c.Text
	}
	kept := make([]store.DistilledRequest, 0, len(result.Items))
	for i, item := range result.Items {
		if strings.TrimSpace(item.Title) == "" || item.Quote == "" {
			return nil, fmt.Errorf("invalid distilled request %d", i)
		}
		if !strings.Contains(bySeq[item.ChunkSeq], item.Quote) {
			continue
		}
		if len(kept) >= MaxRequestItems {
			continue
		}
		kept = append(kept, item)
	}
	if len(kept) == 0 && len(result.Items) > 0 {
		return nil, fmt.Errorf("none of the %d wishes quote their chunk; first was chunk %d %q",
			len(result.Items), result.Items[0].ChunkSeq, ellipsis(result.Items[0].Quote, 120))
	}
	return kept, nil
}
