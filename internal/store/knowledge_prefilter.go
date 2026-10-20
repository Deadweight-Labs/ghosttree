package store

import "strings"

// KnowledgePrefilter is the part of the read rule for knowledge that fits in a
// query, for the hybrid search: an entry without a project (the rule for global
// and machine entries stays with the row rule and with withGlobalScope), an
// entry of a project the account has a role in, and an entry of a project that
// nobody has claimed, which only its author or an instance admin reads. Entries
// of every other project are out of the query, so a guest of one project does
// not make every search compare and check the stock of all the others.
//
// It is a superset of what CanSeeKnowledge allows, never a replacement: the
// exact rule (confidence by role, the machine axis, entries held back from a
// private session) is still applied to each row that gets through. Without
// enforcement there is nothing to filter.
func (a *ProjectAccess) KnowledgePrefilter() KnowledgePrefilter {
	if !a.st.AccessEnforced() {
		return KnowledgePrefilter{}
	}
	if !a.valid {
		return KnowledgePrefilter{SQL: "0"}
	}
	clauses := []string{`k.project = ''`}
	var args []any
	if projects := a.Projects(); len(projects) > 0 {
		clauses = append(clauses, `k.project IN (`+placeholders(len(projects))+`)`)
		for _, p := range projects {
			args = append(args, p)
		}
	}
	unclaimed := `(k.project != '' AND k.project NOT IN (SELECT remote FROM projects)`
	if a.IsAdmin() {
		unclaimed += `)`
	} else {
		unclaimed += ` AND k.id IN (SELECT knowledge_id FROM knowledge_authors WHERE account_id = ?))`
		args = append(args, a.account)
	}
	clauses = append(clauses, unclaimed)
	return KnowledgePrefilter{SQL: strings.Join(clauses, " OR "), Args: args}
}
