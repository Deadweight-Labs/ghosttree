package store

import (
	"fmt"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// A cost report has to rank by cost, and output is six times the price of
// input. Ordering by input tokens alone put the cheaper project first: 891k
// input tokens beat 770k, while the project behind them had twice the output
// and the larger bill. Only the ratio matters here, so these are weights rather
// than prices — the money is computed in one place, in sessiondistill, and the
// store has no business knowing a currency.
const (
	inputRateWeight  = 1
	outputRateWeight = 6
)

// CostRow is one line of the spend report. Group is empty for the total.
type CostRow struct {
	Group            string `json:"group,omitempty"`
	Batches          int    `json:"batches"`
	Sessions         int    `json:"sessions"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
}

// DistillCost aggregates what the asynchronous distiller has actually been
// billed for. groupBy is "", "project", "model", "version" or "day".
//
// Only items with a recorded token count are counted. An item that was
// submitted and never came back cost nothing and produced nothing; counting it
// as a free session would understate the per-session average and with it every
// forecast built on it.
func (s *Store) DistillCost(groupBy, since string) ([]CostRow, error) {
	group := ""
	switch groupBy {
	case "":
	case "project":
		group = "se.project"
	case "model":
		group = "b.model"
	case "version":
		group = "i.prompt_version"
	case "day":
		group = "substr(b.created_at, 1, 10)"
	default:
		return nil, fmt.Errorf("unknown cost grouping %q; use project, model, version or day", groupBy)
	}
	selectGroup, groupClause := "''", ""
	if group != "" {
		selectGroup, groupClause = group, " GROUP BY "+group
	}
	rows, err := s.db.Query(`SELECT `+selectGroup+`,
			COUNT(DISTINCT b.id), COUNT(*), SUM(i.prompt_tokens), SUM(i.completion_tokens)
		FROM distill_batch_items i
		JOIN distill_batches b ON b.id = i.batch_id
		JOIN sessions se ON se.id = i.session_id
		WHERE i.prompt_tokens > 0 AND (? = '' OR b.created_at >= ?)`+groupClause+`
		ORDER BY SUM(i.prompt_tokens) * ? + SUM(i.completion_tokens) * ? DESC`,
		since, since, inputRateWeight, outputRateWeight)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CostRow{}
	for rows.Next() {
		var r CostRow
		if err := rows.Scan(&r.Group, &r.Batches, &r.Sessions, &r.PromptTokens, &r.CompletionTokens); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PendingDistillationSize reports how much work is left and how large it is, in
// transcript characters. It applies the same eligibility rules as the selection
// itself — no project means no distillation, so such a session must not appear
// in a forecast of what the backlog will cost.
func (s *Store) PendingDistillationSize(filter scope.Axes, idleBefore string) (sessions, chars int, err error) {
	where, args := filter.FilterWhere()
	args = append(args, idleBefore)
	err = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(size),0) FROM (
		SELECT (SELECT COALESCE(SUM(length(c.text)),0) FROM session_chunks c WHERE c.session_id = sessions.id) AS size
		FROM sessions
		WHERE `+where+` AND last_seen_at < ? AND project != ''
		  AND NOT EXISTS (SELECT 1 FROM session_distillations d WHERE d.session_id = sessions.id)
		  AND NOT EXISTS (SELECT 1 FROM distill_batch_items i
		                  JOIN distill_batches b ON b.id = i.batch_id
		                  WHERE i.session_id = sessions.id AND b.state = 'open'))`, args...).Scan(&sessions, &chars)
	return sessions, chars, err
}

// BilledTranscriptChars totals the transcript size of the sessions that were
// actually billed. Divided by the input tokens they cost, it gives the real
// characters-per-token ratio for this corpus — which is what a forecast needs.
// The pre-flight estimator is deliberately pessimistic and would overstate it.
func (s *Store) BilledTranscriptChars(since string) (int, error) {
	var chars int
	err := s.db.QueryRow(`SELECT COALESCE(SUM(size),0) FROM (
		SELECT (SELECT COALESCE(SUM(length(c.text)),0) FROM session_chunks c WHERE c.session_id = i.session_id) AS size
		FROM distill_batch_items i
		JOIN distill_batches b ON b.id = i.batch_id
		WHERE i.prompt_tokens > 0 AND (? = '' OR b.created_at >= ?))`, since, since).Scan(&chars)
	return chars, err
}
