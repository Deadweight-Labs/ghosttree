// Package scope implements ghosttree's three optional context axes
// (project, branch, machine). Empty string means "axis not set" and
// an entry with an unset axis applies everywhere along that axis.
package scope

import "strings"

type Axes struct {
	Project string `json:"project,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Machine string `json:"machine,omitempty"`
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

// UnionWhere returns the read-default WHERE fragment: the union of all
// scope combinations that apply to the given context.
func (a Axes) UnionWhere() (string, []any) {
	clauses := []string{`(project = '' AND branch = '' AND machine = '')`}
	var args []any
	if a.Machine != "" {
		clauses = append(clauses, `(project = '' AND branch = '' AND machine = ?)`)
		args = append(args, a.Machine)
	}
	if a.Project != "" {
		clauses = append(clauses, `(project = ? AND branch = '' AND machine = '')`)
		args = append(args, a.Project)
		if a.Branch != "" {
			clauses = append(clauses, `(project = ? AND branch = ? AND machine = '')`)
			args = append(args, a.Project, a.Branch)
		}
		if a.Machine != "" {
			clauses = append(clauses, `(project = ? AND branch = '' AND machine = ?)`)
			args = append(args, a.Project, a.Machine)
		}
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args
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
func DefaultAxes(entryType string, ctx Axes) Axes {
	if ctx.Project == "" {
		// Outside a repo everything is either machine-scoped or global.
		return Axes{Machine: ctx.Machine}
	}
	switch entryType {
	case "decision":
		return Axes{Project: ctx.Project}
	default: // pitfall, note, plan
		return Axes{Project: ctx.Project, Branch: ctx.Branch}
	}
}
