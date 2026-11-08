package store

import (
	"strings"
	"testing"
)

// queryPlan gibt die Zeilen von EXPLAIN QUERY PLAN zurück.
func queryPlan(t *testing.T, s *Store, q string, args ...any) []string {
	t.Helper()
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// Die Commit-Abfragen fangen beim Projekt an: kein Durchlauf über origin_paths
// oder eine ganze Tabelle, und kein Lauf über den Anker-Index der ganzen Instanz
// (anchor_id>?), der die Zeilen aller Projekte liest und erst danach verwirft.
func TestCommitQueriesStartFromTheProject(t *testing.T) {
	s := openTest(t)
	shas := jsonList([]string{"aaaaaaa", "bbbbbbb"})
	origins := jsonList([]int64{1, 2})
	cases := map[string]struct {
		plan []string
		want string
	}{}
	add := func(name, want, q string, args ...any) {
		cases[name] = struct {
			plan []string
			want string
		}{queryPlan(t, s, q, args...), want}
	}
	for name, where := range map[string]string{
		"all":     "",
		"sha":     ` WHERE a.commit_sha IN (SELECT value FROM json_each(?2))`,
		"branch":  ` WHERE a.branch=?2`,
		"origins": ` WHERE r.origin_id IN (SELECT value FROM json_each(?2))`,
	} {
		args := []any{"p"}
		switch name {
		case "sha":
			args = append(args, shas)
		case "branch":
			args = append(args, "main")
		case "origins":
			args = append(args, origins)
		}
		q, qargs := anchoredRowsQuery(where, args)
		add("rows "+name, "", q, qargs...)
	}
	add("candidates", "git_pr_commits_sha (project=? AND sha>? AND sha<?)", commitCandidatesSQL, "p", "abcdef0%", 20, "abcdef0", "abcdef1", "")
	add("origins for path", "SEARCH p USING", originsForPathSQL, "p", "internal/store", `internal/store/%`, "internal/store0")
	for name, c := range cases {
		joined := strings.Join(c.plan, "\n")
		for _, line := range c.plan {
			if strings.Contains(line, "_anchor (anchor_id>?)") {
				t.Errorf("%s reads the anchor index of the whole instance: %q\n%s", name, line, joined)
			}
			// SCAN über eine Tabelle: erlaubt sind nur Zwischenergebnisse (r, json_each).
			if rest, ok := strings.CutPrefix(line, "SCAN "); ok {
				obj := strings.Fields(rest)[0]
				if obj != "r" && !strings.HasPrefix(obj, "json_each") && obj != "(subquery-1)" && !strings.HasPrefix(obj, "(") {
					t.Errorf("%s scans %s completely: %q\n%s", name, obj, line, joined)
				}
			}
		}
		if c.want != "" && !strings.Contains(joined, c.want) {
			t.Errorf("%s does not use %s:\n%s", name, c.want, joined)
		}
	}
	// Jede Teilabfrage der Union beginnt mit einem Index auf dem Projekt.
	for _, line := range queryPlan(t, s, anchoredRowsSQL, "p") {
		if strings.HasPrefix(line, "SEARCH ") && strings.Contains(line, "_anchor ") {
			t.Errorf("a union branch starts at the anchor index: %q", line)
		}
	}
}
