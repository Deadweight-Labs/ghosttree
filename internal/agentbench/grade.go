package agentbench

import "strings"

type Score struct {
	TotalWeight    int     `json:"total_weight"`
	EarnedWeight   int     `json:"earned_weight"`
	FactRecall     float64 `json:"fact_recall"`
	TotalSlots     int     `json:"total_slots"`
	AnsweredSlots  int     `json:"answered_slots"`
	CorrectSlots   int     `json:"correct_slots"`
	Contradictions int     `json:"contradictions"`
	ClaimPrecision float64 `json:"claim_precision"`
	ContradictRate float64 `json:"contradiction_rate"`
	AbstentionRate float64 `json:"abstention_rate"`
	// Die Trefferquote getrennt nach Wissensquelle. RepoRecall misst, wie gut
	// ein Gedaechtnis die Suche fuehrt — dort kann jeder Arm die Antwort
	// finden. MemoryRecall misst etwas anderes: was es wert ist, die Sache
	// ueberhaupt aufgeschrieben zu haben. Zusammengezaehlt ergeben die beiden
	// eine Zahl, die keine Frage beantwortet.
	RepoWeight   int     `json:"repo_weight"`
	RepoEarned   int     `json:"repo_earned"`
	RepoRecall   float64 `json:"repo_recall"`
	MemoryWeight int     `json:"memory_weight"`
	MemoryEarned int     `json:"memory_earned"`
	MemoryRecall float64 `json:"memory_recall"`
	// Rejected keeps what the agent actually said where the key said no. It
	// costs a few bytes per run and buys the only cheap check there is against
	// the dominant threat to this benchmark: a wrong ground truth. Two arms
	// that independently name the same rejected answer are saying something
	// about the question, not about their memory.
	Rejected []RejectedClaim `json:"rejected,omitempty"`
}

// RejectedClaim is one answer the grader did not accept.
type RejectedClaim struct {
	Slot  string `json:"slot"`
	Value string `json:"value"`
}

func Grade(task Task, form ResponseForm) Score {
	score := Score{TotalSlots: len(task.Facts)}
	for _, slot := range task.Facts {
		score.TotalWeight += slot.Weight
		if slot.OnlyInMemory {
			score.MemoryWeight += slot.Weight
		} else {
			score.RepoWeight += slot.Weight
		}
		value, present := form.Slots[slot.ID]
		if !present || value.IsAbstention() {
			continue
		}
		score.AnsweredSlots++
		if slot.matches(value) {
			score.CorrectSlots++
			score.EarnedWeight += slot.Weight
			if slot.OnlyInMemory {
				score.MemoryEarned += slot.Weight
			} else {
				score.RepoEarned += slot.Weight
			}
			continue
		}
		if slot.contradicts(value) {
			score.Contradictions++
		}
		score.Rejected = append(score.Rejected, RejectedClaim{Slot: slot.ID, Value: value.plain()})
	}
	if score.TotalWeight > 0 {
		score.FactRecall = float64(score.EarnedWeight) / float64(score.TotalWeight)
	}
	if score.RepoWeight > 0 {
		score.RepoRecall = float64(score.RepoEarned) / float64(score.RepoWeight)
	}
	if score.MemoryWeight > 0 {
		score.MemoryRecall = float64(score.MemoryEarned) / float64(score.MemoryWeight)
	}
	if score.AnsweredSlots > 0 {
		score.ClaimPrecision = float64(score.CorrectSlots) / float64(score.AnsweredSlots)
		score.ContradictRate = float64(score.Contradictions) / float64(score.AnsweredSlots)
	}
	if score.TotalSlots > 0 {
		score.AbstentionRate = float64(score.TotalSlots-score.AnsweredSlots) / float64(score.TotalSlots)
	}
	return score
}

func (s FactSlot) matches(v SlotValue) bool {
	switch s.Type {
	case SlotPath:
		return v.Path != nil && containsFold(normalisePaths(s.Accepted), normalisePath(*v.Path))
	case SlotString:
		return v.String != nil && containsFold(s.Accepted, *v.String)
	case SlotInteger:
		return v.Integer != nil && s.ExpectedInt != nil && *v.Integer == *s.ExpectedInt
	case SlotBoolean:
		return v.Boolean != nil && s.ExpectedBool != nil && *v.Boolean == *s.ExpectedBool
	}
	return false
}

func (s FactSlot) contradicts(v SlotValue) bool {
	if len(s.Contradicts) == 0 {
		return false
	}
	switch {
	case v.Path != nil:
		return containsFold(normalisePaths(s.Contradicts), normalisePath(*v.Path))
	case v.String != nil:
		return containsFold(s.Contradicts, *v.String)
	}
	return false
}

// normalisePath removes the differences between two spellings of the same
// path. "./internal/x.go", "internal/x.go" and "/internal/x.go" name the same
// file, and an agent that knows the answer should not lose the point to a
// leading dot. The normalisation is fixed and mechanical — no model decides
// what counts as the same path.
func normalisePath(path string) string {
	path = strings.TrimSpace(strings.ReplaceAll(path, `\`, "/"))
	path = strings.TrimPrefix(path, "./")
	return strings.TrimPrefix(path, "/")
}

func normalisePaths(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = normalisePath(p)
	}
	return out
}

func containsFold(candidates []string, value string) bool {
	value = strings.TrimSpace(value)
	for _, candidate := range candidates {
		if strings.EqualFold(strings.TrimSpace(candidate), value) {
			return true
		}
	}
	return false
}
