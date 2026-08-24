package sessiondistill

import "github.com/Deadweight-Labs/ghosttree/internal/store"

// charsPerToken is a deliberate underestimate. English prose runs about four
// characters per token, but these transcripts are German and English mixed
// with code, paths and JSON, which tokenize worse. Guessing low means the
// estimate errs towards a smaller prompt, and the cost of that is some unused
// context rather than an unplanned bill.
const charsPerToken = 3

// Budget caps how much transcript goes into one distillation prompt.
//
// The limit exists for price, not for capacity: the model takes far more than
// this, but input above roughly 272k tokens is billed at the long-context
// rate, which is double. Staying under that threshold is worth more than the
// extra context, because a transcript that needs 272k tokens is mostly tool
// output anyway.
type Budget struct {
	MaxChars int
}

// DefaultBudget is sized from the archive rather than from a round number.
// Measured over 1838 sessions on 2026-08-24: the largest transcript is 696110
// characters and only one session exceeds half a million. 750k characters
// estimates to 250k tokens at the conservative ratio and comes to roughly 214k
// at the ~3.5 chars per token the first two production batches actually
// billed — under the threshold either way, and above every transcript that
// exists. Nothing in the archive is trimmed at this size, which matters because
// trimming drops the middle of exactly the long sessions that hold the most.
var DefaultBudget = Budget{MaxChars: 750_000}

// EstimatedTokens converts the character budget using the conservative ratio.
// The real figure comes back from the API in usage.prompt_tokens; this is only
// for deciding what to send.
func (b Budget) EstimatedTokens() int { return b.MaxChars / charsPerToken }

// EstimateTokens applies the same ratio to an arbitrary amount of text, which
// is what a dry run has to work with before anything has been sent.
func EstimateTokens(chars int) int { return chars / charsPerToken }

// Batch pricing for the configured model in USD per million tokens. Batch is
// half the synchronous rate, which is the entire reason the asynchronous path
// exists. Input above LongContextThresholdTokens is billed at double, and the
// budget above is sized to stay clear of it.
const (
	BatchInputUSDPerMTok       = 0.10
	BatchOutputUSDPerMTok      = 0.60
	LongContextThresholdTokens = 272_000
)

// BatchCostUSD reports what a batch cost, or would cost. It assumes short
// context: a prompt above the threshold is a bug in the budget, not a price
// this function should quietly absorb.
func BatchCostUSD(promptTokens, completionTokens int) float64 {
	return float64(promptTokens)/1e6*BatchInputUSDPerMTok +
		float64(completionTokens)/1e6*BatchOutputUSDPerMTok
}

// SelectWithinBudget trims a transcript to fit, returning the chunks to send
// and how many were dropped.
//
// It takes from both ends and drops the middle. A session opens by stating the
// task and closes by stating the outcome; the middle is mostly tool traffic,
// which is the least likely place for durable knowledge and the most likely
// place for bulk. Order and sequence numbers are preserved because the model
// has to quote against them.
func SelectWithinBudget(chunks []store.Chunk, budget Budget) ([]store.Chunk, int) {
	if budget.MaxChars <= 0 {
		budget = DefaultBudget
	}
	total := 0
	for _, c := range chunks {
		total += len(c.Text)
	}
	if total <= budget.MaxChars {
		return chunks, 0
	}
	// A single chunk over budget is truncated rather than dropped: dropping it
	// would send an empty prompt and mark the session processed regardless.
	if len(chunks) == 1 {
		only := chunks[0]
		only.Text = only.Text[:budget.MaxChars]
		return []store.Chunk{only}, 0
	}

	keep := make([]bool, len(chunks))
	used, head, tail := 0, 0, len(chunks)-1
	for head <= tail {
		// Alternate ends so neither the opening nor the closing context is
		// starved when the budget runs out mid-way.
		next := head
		if (head+len(chunks)-tail)%2 == 1 {
			next = tail
		}
		if used+len(chunks[next].Text) > budget.MaxChars {
			break
		}
		keep[next] = true
		used += len(chunks[next].Text)
		if next == head {
			head++
		} else {
			tail--
		}
	}

	out := make([]store.Chunk, 0, len(chunks))
	dropped := 0
	for i, c := range chunks {
		if keep[i] {
			out = append(out, c)
		} else {
			dropped++
		}
	}
	return out, dropped
}
