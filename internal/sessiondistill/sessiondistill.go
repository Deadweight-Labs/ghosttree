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
chunk_seq, and quote. The quote must be an exact contiguous substring of that
single chunk. Exclude task requests/backlog, routine progress, guesses, secrets,
tool noise, and facts derivable from the repository. Prefer no item over a weak one.`

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

// quoted is the full transcript: quotes are verified against every chunk, not
// only the ones that fit the budget, so a trimmed prompt cannot invalidate a
// grounding that was already correct.
func distill(ctx context.Context, model llm.Client, chunks, quoted []store.Chunk, existingTitles []string) ([]store.SessionDistilledItem, error) {
	var transcript strings.Builder
	for _, c := range chunks {
		if strings.TrimSpace(c.Text) != "" {
			fmt.Fprintf(&transcript, "[chunk %d, %s]\n%s\n\n", c.Seq, c.Role, c.Text)
		}
	}
	user := "Existing titles (do not duplicate):\n- " + strings.Join(existingTitles, "\n- ") + "\n\nTranscript:\n" + transcript.String()
	var raw string
	var err error
	if jc, ok := model.(llm.JSONClient); ok {
		raw, err = jc.CompleteJSON(ctx, system, []llm.Message{{Role: "user", Content: user}}, 2500)
	} else {
		raw, err = model.Complete(ctx, system, []llm.Message{{Role: "user", Content: user}}, 2500)
	}
	if err != nil {
		return nil, err
	}
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
	for i, item := range result.Items {
		if !allowed[item.Type] || strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Body) == "" || item.Quote == "" {
			return nil, fmt.Errorf("invalid distilled item %d", i)
		}
		if !strings.Contains(bySeq[item.ChunkSeq], item.Quote) {
			return nil, fmt.Errorf("item %d quote is not grounded in chunk %d", i, item.ChunkSeq)
		}
	}
	return result.Items, nil
}
