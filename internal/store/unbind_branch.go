package store

import (
	"fmt"
	"strings"
)

// BranchBoundKnowledge lists active entries filed against a branch. Until
// 2026-08-24 that was the write default for pitfalls, notes and plans, so most
// of what it returns was never a deliberate choice — it is the branch whichever
// session happened to write the entry was standing on.
func (s *Store) BranchBoundKnowledge() ([]Knowledge, error) {
	rows, err := s.db.Query(`SELECT ` + knowledgeCols + ` FROM knowledge
		WHERE status = 'active' AND branch != ''
		ORDER BY project, branch, id`)
	if err != nil {
		return nil, err
	}
	return s.scanKnowledge(rows)
}

// UnbindBranchScope lifts the named entries from project+branch to project.
//
// It takes explicit ids rather than a filter because the two cases are not
// distinguishable from the data: an entry on a feature branch may be there
// because nobody chose otherwise, or because it genuinely stops being true when
// the branch is gone. Only a reader of the entry can tell, so the caller names
// what it means, and dry is what makes that decision reviewable first.
func (s *Store) UnbindBranchScope(ids []int64, dry bool) ([]Knowledge, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	in := "(" + strings.Join(placeholders, ",") + ")"

	rows, err := s.db.Query(`SELECT `+knowledgeCols+` FROM knowledge
		WHERE id IN `+in+` AND branch != '' ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	affected, err := s.scanKnowledge(rows)
	if err != nil {
		return nil, err
	}
	if dry || len(affected) == 0 {
		return affected, nil
	}
	// An entry with no project cannot be lifted: dropping its branch would make
	// it global, which is a wider claim than anyone made.
	for _, k := range affected {
		if k.Scope.Project == "" {
			return nil, fmt.Errorf("knowledge %d has a branch but no project; lifting it would make it global", k.ID)
		}
	}
	if _, err := s.db.Exec(`UPDATE knowledge SET branch = '', updated_at = ?
		WHERE id IN `+in+` AND branch != '' AND project != ''`, append([]any{now()}, args...)...); err != nil {
		return nil, err
	}
	return affected, nil
}
