package store

import (
	"database/sql"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// maxLineageDepth begrenzt die Kette, falls ein PR-Stapel (oder Fremddaten) sehr
// tief oder zyklisch ist.
const maxLineageDepth = 16

// BranchLineage nennt die Branches, von denen ein Branch abstammt, soweit der
// Server das aus den gemeldeten PRs weiß: ein PR mit Head dieses Branches und
// Base B sagt, dass der Branch von B abgezweigt ist, und so weiter die Kette
// hinauf. Der Server hat die Git-Repositorien nicht; die Abstammung aus dem
// lokalen Repo kennt nur der Client. Die Kette endet am Standard-Branch (der ist
// die Projektebene, kein eigener Layer), an einem Zyklus oder bei
// maxLineageDepth. Der nächste Vorfahr steht vorn; ohne PR ist die Liste leer.
func (s *Store) BranchLineage(project, branch string) ([]string, error) {
	if s.reader != nil {
		return s.reader.BranchLineage(project, branch)
	}
	project = scope.NormalizeRemote(project)
	out := []string{}
	seen := map[string]bool{branch: true}
	cur := branch
	for len(out) < maxLineageDepth && cur != "" {
		var base, def string
		err := s.db.QueryRow(`SELECT base_ref, default_branch FROM git_pull_requests
			WHERE project=? AND head_ref=? AND base_ref<>''
			ORDER BY CASE state WHEN 'open' THEN 0 ELSE 1 END, updated_at DESC, number DESC LIMIT 1`, project, cur).Scan(&base, &def)
		if err == sql.ErrNoRows {
			break
		}
		if err != nil {
			return nil, err
		}
		if base == "" || seen[base] || base == def {
			break
		}
		seen[base] = true
		out = append(out, base)
		cur = base
	}
	return out, nil
}
