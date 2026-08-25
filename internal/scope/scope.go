// Package scope implements ghosttree's three optional context axes
// (project, branch, machine). Empty string means "axis not set" and
// an entry with an unset axis applies everywhere along that axis.
package scope

import "strings"

type Axes struct {
	Project string `json:"project,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Machine string `json:"machine,omitempty"`
	// Lineage names the branches the current one descends from, nearest last
	// and excluding the branch itself. It is read context, never an address: an
	// entry is filed against one branch, and Lineage only widens what a session
	// on a descendant may see.
	//
	// Without it the branch axis compared names for equality, so a branch cut
	// from develop saw nothing of develop. That, not the write default, is what
	// stranded 127 entries — and it is what the project is named after: the
	// ghost tree grows the way the git tree does, and a branch keeps what it
	// was cut from.
	Lineage []string `json:"lineage,omitempty"`
	// AnyBranch drops the branch constraint instead of narrowing it, for the
	// caller who deliberately wants to look past their own line.
	//
	// It exists because clearing Branch does the opposite. With no branch there
	// are no branch clauses at all, so "search every branch" returned strictly
	// fewer entries than the default and none of the branch-scoped ones — the
	// flag promised width and delivered a blind spot.
	AnyBranch bool `json:"any_branch,omitempty"`
}

func CanonicalAxes(a Axes) Axes {
	a.Project = NormalizeRemote(a.Project)
	a.Branch = strings.TrimSpace(a.Branch)
	a.Machine = strings.ToLower(strings.TrimSpace(a.Machine))
	a.Lineage = canonicalLineage(a.Branch, a.Lineage)
	return a
}

// canonicalLineage drops blanks, the branch itself, and repeats. The branch
// itself would only duplicate a clause it already has, and a repeat would do
// the same — both harmless in SQL and confusing to read in a log.
func canonicalLineage(branch string, lineage []string) []string {
	if len(lineage) == 0 {
		return nil
	}
	seen := map[string]bool{branch: true}
	out := make([]string, 0, len(lineage))
	for _, name := range lineage {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// NormalizeRemote canonicalizes a git remote URL to host/owner/repo, lowercase.
func NormalizeRemote(remote string) string {
	r := strings.TrimSpace(remote)
	r = strings.TrimSuffix(r, ".git")
	for _, p := range []string{"https://", "http://", "ssh://", "git://"} {
		r = strings.TrimPrefix(r, p)
	}
	r = strings.TrimPrefix(r, "git@")
	r = strings.Replace(r, ":", "/", 1)
	r = strings.TrimPrefix(r, "www.")
	return strings.ToLower(strings.TrimSuffix(r, "/"))
}

// IsGlobal reports that nothing narrows this placement — the entry applies
// everywhere. Asked as a question rather than compared against an empty struct,
// because Lineage is read context and must not count towards the address.
func (a Axes) IsGlobal() bool {
	return a.Project == "" && a.Branch == "" && a.Machine == ""
}

// Same reports that two placements address the same spot, ignoring lineage.
func (a Axes) Same(b Axes) bool {
	return a.Project == b.Project && a.Branch == b.Branch && a.Machine == b.Machine
}

// UnionWhere returns the read-default WHERE fragment: the union of all
// scope combinations that apply to the given context.
//
// A branch clause is emitted for the current branch and for every branch it
// descends from. Equality alone meant a branch cut from develop could not see
// develop, which is the opposite of what a working tree does — it carries the
// files it was cut from, and the ghost tree is supposed to grow the same way.
func (a Axes) UnionWhere() (string, []any) {
	clauses := []string{`(project = '' AND branch = '' AND machine = '')`}
	var args []any
	if a.Machine != "" {
		clauses = append(clauses, `(project = '' AND branch = '' AND machine = ?)`)
		args = append(args, a.Machine)
	}
	if a.Project != "" {
		if a.AnyBranch {
			// One clause instead of an enumeration: every branch of this
			// project, plus the unbranched entries it subsumes.
			clauses = append(clauses, `(project = ? AND machine = '')`)
			args = append(args, a.Project)
		} else {
			clauses = append(clauses, `(project = ? AND branch = '' AND machine = '')`)
			args = append(args, a.Project)
			for _, branch := range a.readableBranches() {
				clauses = append(clauses, `(project = ? AND branch = ? AND machine = '')`)
				args = append(args, a.Project, branch)
			}
		}
		if a.Machine != "" {
			clauses = append(clauses, `(project = ? AND branch = '' AND machine = ?)`)
			args = append(args, a.Project, a.Machine)
		}
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args
}

// readableBranches is the current branch plus its ancestors. A sibling branch
// is in neither list, so what somebody deliberately bound to their own branch
// stays theirs.
func (a Axes) readableBranches() []string {
	if a.Branch == "" {
		return nil
	}
	return append([]string{a.Branch}, canonicalLineage(a.Branch, a.Lineage)...)
}

// FilterWhere compares only the axes that are set (explicit queries).
func (a Axes) FilterWhere() (string, []any) {
	var clauses []string
	var args []any
	for _, pair := range []struct{ col, v string }{
		{"project", a.Project}, {"branch", a.Branch}, {"machine", a.Machine},
	} {
		if pair.v != "" {
			clauses = append(clauses, pair.col+" = ?")
			args = append(args, pair.v)
		}
	}
	if len(clauses) == 0 {
		return `(1=1)`, nil
	}
	return "(" + strings.Join(clauses, " AND ") + ")", args
}

// DefaultAxes implements the write defaults: the agent names only the
// entry type; placement follows these rules. The distiller (V1) fixes
// misfiled entries, sessions never manage placement themselves.
//
// Nothing is bound to a branch by default. Reads match a branch exactly, so a
// default branch binding hides what was learnt from every other branch and
// survives the branch itself — knowledge stays attached to a name that no
// longer exists after the merge. Branch scope is the exception (a migration in
// flight, a feature flag), and an exception has to be asked for: the MCP tool
// takes scope_hint: branch for exactly that.
func DefaultAxes(entryType string, ctx Axes) Axes {
	if ctx.Project == "" {
		// Outside a repo everything is either machine-scoped or global.
		return Axes{Machine: ctx.Machine}
	}
	return Axes{Project: ctx.Project}
}
