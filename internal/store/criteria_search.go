package store

import (
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// CriterionHit ist ein Kriterium als Treffer der Suchsprache (REQ-600): "was
// wurde in PR #N erledigt" fragt nach erfüllten Kriterien samt ihrem Nachweis.
type CriterionHit struct {
	ID           int64               `json:"id"`
	RequestID    int64               `json:"request_id"`
	Number       int                 `json:"number"`
	Description  string              `json:"description"`
	State        string              `json:"state"`
	RequestTitle string              `json:"request_title"`
	RequestState string              `json:"request_state"`
	UpdatedAt    string              `json:"updated_at"`
	Evidence     []CriterionEvidence `json:"evidence,omitempty"`
}

// CriterionEvidence ist ein Nachweis an einem Kriterium. Sessions stehen nicht
// darin: ihre Adresse verrät ein Transkript, das der Betrachter vielleicht nicht
// lesen darf.
type CriterionEvidence struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// HumanID ist AC-<request>.<nummer>.
func (c CriterionHit) HumanID() string { return fmt.Sprintf("AC-%d.%d", c.RequestID, c.Number) }

// SearchCriteria liefert die Kriterien der Requests eines Projekts, die der
// Betrachter lesen darf, die zuletzt geänderten zuerst. state "" oder "all"
// heißt jeden Zustand; text sind Wörter, die alle in der Beschreibung oder im
// Titel des Requests vorkommen müssen. Höchstens limit (bis 2000).
func (a *ProjectAccess) SearchCriteria(project, state, text string, limit int) ([]CriterionHit, error) {
	project = scope.NormalizeRemote(project)
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	if err := a.GateList(project, ResRequest, true); err != nil {
		return nil, err
	}
	rows, err := a.st.criteriaRows(project, state)
	if err != nil {
		return nil, err
	}
	words := strings.Fields(strings.ToLower(text))
	seen := map[int64]bool{}
	out := make([]CriterionHit, 0, limit)
	for _, c := range rows {
		ok, cached := seen[c.RequestID]
		if !cached {
			ref, err := a.st.RequestRef(RefRequest, c.RequestID)
			ok = err == nil && requestReadable(a, ref)
			seen[c.RequestID] = ok
		}
		if !ok {
			continue
		}
		hay := strings.ToLower(c.Description + " " + c.RequestTitle)
		match := true
		for _, w := range words {
			if !strings.Contains(hay, strings.Trim(w, `"`)) {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		out = append(out, c)
		if len(out) >= limit {
			break
		}
	}
	if err := a.st.attachCriterionEvidence(out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) criteriaRows(project, state string) ([]CriterionHit, error) {
	if s.reader != nil {
		return s.reader.criteriaRows(project, state)
	}
	q := `SELECT c.id,c.request_id,c.number,c.description,c.state,r.title,r.state,c.updated_at
		FROM request_criteria c JOIN requests r ON r.id=c.request_id WHERE r.project=?`
	args := []any{project}
	if state != "" && state != "all" {
		q += ` AND c.state=?`
		args = append(args, state)
	}
	rows, err := s.db.Query(q+` ORDER BY c.updated_at DESC, c.id DESC LIMIT 5000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CriterionHit
	for rows.Next() {
		var c CriterionHit
		if err := rows.Scan(&c.ID, &c.RequestID, &c.Number, &c.Description, &c.State, &c.RequestTitle, &c.RequestState, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) attachCriterionEvidence(list []CriterionHit) error {
	if s.reader != nil {
		return s.reader.attachCriterionEvidence(list)
	}
	idx := map[int64]int{}
	ids := make([]int64, 0, len(list))
	for i, c := range list {
		idx[c.ID] = i
		ids = append(ids, c.ID)
	}
	return chunked(ids, func(part []int64) error {
		rows, err := s.db.Query(`SELECT criterion_id, kind, ref FROM request_evidence
			WHERE kind<>'session' AND criterion_id IN (`+placeholders(len(part))+`) ORDER BY id`, factIDArgs(part)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var e CriterionEvidence
			if err := rows.Scan(&id, &e.Kind, &e.Ref); err != nil {
				return err
			}
			list[idx[id]].Evidence = append(list[idx[id]].Evidence, e)
		}
		return rows.Err()
	})
}
