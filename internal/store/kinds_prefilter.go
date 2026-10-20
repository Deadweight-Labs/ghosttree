package store

import "strings"

// projectPrefilter is the part of the read rule of an object that belongs to a
// project and has an author label, as far as it fits in a query: an object of a
// project the account has a role in, and an object of a project nobody has
// claimed, which only its author or an instance admin reads. Objects of every
// other project are out of the query, so a guest of one project does not make
// every search compare and check the stock of all the others. withoutProject adds
// the objects that belong to no project, for the kinds whose row rule decides
// those (documents).
//
// It is a superset of what the row rule allows, never a replacement: the exact
// rule is still applied to each row that gets through. Without enforcement there
// is nothing to filter.
func (a *ProjectAccess) projectPrefilter(alias string, withoutProject bool) Prefilter {
	if !a.st.AccessEnforced() {
		return Prefilter{}
	}
	if !a.valid {
		return Prefilter{SQL: "0"}
	}
	var clauses []string
	var args []any
	if withoutProject {
		clauses = append(clauses, alias+`.project = ''`)
	}
	if projects := a.Projects(); len(projects) > 0 {
		clauses = append(clauses, alias+`.project IN (`+placeholders(len(projects))+`)`)
		for _, p := range projects {
			args = append(args, p)
		}
	}
	unclaimed := `(` + alias + `.project != '' AND ` + alias + `.project NOT IN (SELECT remote FROM projects)`
	if a.IsAdmin() {
		unclaimed += `)`
	} else {
		unclaimed += ` AND ` + alias + `.person != '' AND ` + alias + `.person = ?)`
		args = append(args, a.principal.Label)
	}
	clauses = append(clauses, unclaimed)
	return Prefilter{SQL: strings.Join(clauses, " OR "), Args: args}
}

// GhostPrefilter is the part of the read rule for ghost descriptions that fits in
// a query, on the alias g of ghost_files (see projectPrefilter). The exact rule
// (CanSeeGhost and CanDeliverGhost) still decides each row.
func (a *ProjectAccess) GhostPrefilter() Prefilter { return a.projectPrefilter("g", false) }

// DocumentPrefilter is the part of the read rule for documents that fits in a
// query, on the alias d of documents (see projectPrefilter). The exact rule
// (CanSeeDocument) still decides each row.
func (a *ProjectAccess) DocumentPrefilter() Prefilter { return a.projectPrefilter("d", true) }
