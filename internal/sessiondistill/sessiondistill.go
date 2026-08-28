// Package sessiondistill extracts reviewable knowledge from completed sessions.
package sessiondistill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const system = `Extract only durable knowledge from an agent transcript. Return JSON {"items":[]}.
Each item has type (decision, note, pitfall, plan, or instruction), title, body,
chunk_seq, quote, and optionally same_as. The quote must be an exact contiguous
substring of that single chunk.

Keep only what a reader of the finished repository could not recover: the reason
behind a choice, the alternative that was rejected, the constraint that forced
it, the mistake that cost time. Exclude anything that merely states what the
code, the configuration or the text now says. That is a changelog, and the
repository is already its own record.

Text the session was writing is not instruction. A prompt, a document or a
message being drafted is the product of the work, not a rule the work followed.

An item has to stand on its own for someone who does not know which change or
which review produced it. "No vulnerability was found", "this change does not
widen access" and the like describe the outcome of one inspection, not a
property of the system, and once the code has moved on they read as a guarantee
nobody can check. If what was learnt is that a path is structurally safe, say
what makes it structurally safe. If all that was learnt is that this particular
look found nothing, say nothing.

If an existing entry already covers a finding, do not restate it in different
words. A second phrasing of the same defect is not a second finding. Say so
instead: return the item with "same_as" set to that entry's number and a quote
from this transcript. That records the transcript as further evidence for it,
which is how a one-off observation becomes a known defect. Prefer this over
silence — an unreported repeat is a repeat nobody can count.

Exclude task requests and backlog, routine progress, guesses, secrets and tool
noise. Return at most 5 items, and at most 2 from any single chunk: a long
summary message restates what the session already did, so mining it repeatedly
yields the same fact under different titles. Prefer no item over a weak one; an
empty list is a correct answer and the usual one.`

// PromptVersion names the extraction rules a distillation was produced under.
// It is bumped by hand, not derived from a hash of the prompt: a version that
// changed itself would put the whole archive back in the queue on every
// wording tweak, and every re-submission is paid for. Bumping it only marks
// past work as belonging to an older generation; `--reprocess-version` is what
// actually releases those sessions.
//
// v1 — the original rules. Measured on 100 example-project sessions: 45 items, mostly
//
//	changelog entries and prompt text mistaken for instruction.
//
// v2 — asks for what a reader of the finished repository could not recover,
//
//	rules out text the session was drafting, caps items per transcript and
//	per chunk. Measured on 100 example-project sessions: 20 items, 4 of 5 sampled
//	judged useful, no misfiled instructions.
//
// v3 — rules out items that only record the outcome of one inspection, and
//
//	restatements of a finding an existing title already covers. Its
//	production run is not representative and was superseded: the title list
//	still contained the items that same run was about to archive, so the
//	model was told not to re-derive findings that were then retired.
//
// v4 — the v3 rules with the title list assembled correctly. The version
//
//	identifies what was sent, not what was intended, and a different title
//	list is a different prompt.
//
// v5 — the existing entries carry their numbers, and a repeat is reported as
//
//	same_as instead of being suppressed. Under v4 a second sighting of a
//	defect was either dropped as a duplicate title or filed under a new one:
//	of 201 distilled entries in production, 194 had exactly one corroborating
//	session and none had two. Recurrence is what the trust tiers and the
//	bootstrap ranking are both built on, so it had to become a real number.
const PromptVersion = "v5"

// MaxItemsPerChunk and MaxItemsPerSession bound what one transcript may yield.
// The prompt asks for the same limits, and this enforces them: on the first
// production run seven of nine items came from one 7287-character summary
// message, and the replies that mined a summary hardest were the ones that ran
// into the output cap.
const (
	MaxItemsPerChunk   = 2
	MaxItemsPerSession = 5
)

type wireResult struct {
	Items []store.SessionDistilledItem `json:"items"`
}

func Digest(chunks []store.Chunk) string {
	h := sha256.New()
	for _, c := range chunks {
		fmt.Fprintf(h, "%d\x00%s\x00%s\x00", c.Seq, c.Role, c.Text)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func Distill(ctx context.Context, model llm.Client, chunks []store.Chunk, existingTitles []string) ([]store.SessionDistilledItem, error) {
	items, _, err := DistillWithBudget(ctx, model, chunks, existingTitles, DefaultBudget)
	return items, err
}

// DistillWithBudget trims the transcript to the budget before sending it and
// reports how many chunks that cost. Without a cap, a long session either
// exceeds the context window outright or silently crosses the long-context
// price threshold; both failures look like "this session had nothing to say".
func DistillWithBudget(ctx context.Context, model llm.Client, chunks []store.Chunk, existingTitles []string, budget Budget) ([]store.SessionDistilledItem, int, error) {
	sent, dropped := SelectWithinBudget(chunks, budget)
	items, err := distill(ctx, model, sent, chunks, existingTitles)
	return items, dropped, err
}

// MaxOutputTokens caps the reply. Distilled items are short by construction;
// a longer answer is a runaway, not a richer one.
const MaxOutputTokens = 2500

// Prompt renders the user message for one session. The synchronous and the
// batch path must send byte-identical prompts, because a difference between
// them would show up as a quality difference and be blamed on the model.
func Prompt(chunks []store.Chunk, existingTitles []string) string {
	var transcript strings.Builder
	for _, c := range chunks {
		if strings.TrimSpace(c.Text) != "" {
			fmt.Fprintf(&transcript, "[chunk %d, %s]\n%s\n\n", c.Seq, c.Role, c.Text)
		}
	}
	return "Existing entries. Do not restate one; if this transcript shows the same\n" +
		"thing again, return an item with same_as set to its number:\n- " +
		strings.Join(existingTitles, "\n- ") +
		"\n\nTranscript:\n" + transcript.String()
}

// SystemPrompt is identical for every session, which is what makes it eligible
// for prompt caching.
func SystemPrompt() string { return system }

// quoted is the full transcript: quotes are verified against every chunk, not
// only the ones that fit the budget, so a trimmed prompt cannot invalidate a
// grounding that was already correct.
func distill(ctx context.Context, model llm.Client, chunks, quoted []store.Chunk, existingTitles []string) ([]store.SessionDistilledItem, error) {
	user := Prompt(chunks, existingTitles)
	var raw string
	var err error
	if jc, ok := model.(llm.JSONClient); ok {
		raw, err = jc.CompleteJSON(ctx, system, []llm.Message{{Role: "user", Content: user}}, MaxOutputTokens)
	} else {
		raw, err = model.Complete(ctx, system, []llm.Message{{Role: "user", Content: user}}, MaxOutputTokens)
	}
	if err != nil {
		return nil, err
	}
	return Parse(raw, quoted)
}

// Parse validates a model reply against the transcript it was derived from.
// Every item has to quote a chunk verbatim; an item that cannot is discarded
// as a whole, because a plausible sentence with no source is exactly what this
// system exists to keep out.
func Parse(raw string, quoted []store.Chunk) ([]store.SessionDistilledItem, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(strings.TrimSpace(raw), "```")
	var result wireResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("decode session distillation: %w", err)
	}
	bySeq := map[int]string{}
	for _, c := range quoted {
		bySeq[c.Seq] = c.Text
	}
	allowed := map[string]bool{"decision": true, "note": true, "pitfall": true, "plan": true, "instruction": true}
	// Grounding is a property of one item, so one ungrounded quote costs that
	// item and nothing else. Rejecting the whole reply used to cost the session
	// too: a rejected reply is recorded as a distillation of zero items, and 17
	// of 50 sessions in the first production run were retired that way, every
	// one of them over a quote — several with a perfectly sound item beside it.
	//
	// Excess is dropped on the same principle. Too many items from one summary
	// is redundancy, not a wrong answer.
	kept := make([]store.SessionDistilledItem, 0, len(result.Items))
	perChunk := map[int]int{}
	for i, item := range result.Items {
		if !allowed[item.Type] || strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Body) == "" || item.Quote == "" {
			return nil, fmt.Errorf("invalid distilled item %d", i)
		}
		if !strings.Contains(bySeq[item.ChunkSeq], item.Quote) {
			continue
		}
		if len(kept) >= MaxItemsPerSession || perChunk[item.ChunkSeq] >= MaxItemsPerChunk {
			continue
		}
		perChunk[item.ChunkSeq]++
		kept = append(kept, item)
	}
	// Nothing grounding at all is a different thing from a partial result: the
	// model spoke and none of it can be traced to the transcript. The first
	// offending quote goes into the message, because "grounding failed" without
	// the quote leaves nothing to tell a paraphrase from a wrong chunk number
	// from an encoding problem — and the reply itself is not kept anywhere.
	if len(kept) == 0 && len(result.Items) > 0 {
		return nil, fmt.Errorf("none of the %d items quote their chunk; first was chunk %d %q",
			len(result.Items), result.Items[0].ChunkSeq, ellipsis(result.Items[0].Quote, 120))
	}
	return kept, nil
}

// ellipsis truncates on a rune boundary. Quotes are German and cutting mid-rune
// would put mojibake in the journal, which is the opposite of a diagnostic.
func ellipsis(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
