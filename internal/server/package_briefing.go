package server

import (
	"fmt"
	"html"
	"net/http"
	"sort"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// packageBriefingChars begrenzt den Abschnitt im Briefing. Er steht in jeder
// Session auf dem Branch, also kostet jede Zeile bei jedem Start; wer mehr
// wissen will, ruft package_get oder package_review.
const packageBriefingChars = 1800

// writePackageBriefing hängt das Arbeitspaket des Branches an das Briefing:
// Name, die Requests mit der Zahl offener Kriterien und die Human-Tasks, die
// noch auf jemanden warten. Ohne Branch, ohne Paket oder ohne Leserecht an
// Requests bleibt es still; ein Fehler kostet nur die Auskunft.
func (a *api) writePackageBriefing(w http.ResponseWriter, r *http.Request, pa *store.ProjectAccess) {
	ax := axesFromQuery(r)
	if ax.Project == "" || ax.Branch == "" || !pa.CanSeeProject(ax.Project, store.ResRequest) {
		return
	}
	detail, err := a.st.PackageForBranch(ax.Project, ax.Branch, ax.Lineage)
	if err != nil {
		// Auch ohne Paket kehren vertagte Entscheidungen zurück, deren Auslöser
		// zum Branch passt.
		a.writeDueDecisions(w, pa, ax.Project, ax.Branch, "", nil)
		return
	}
	view := pa.PackageView(detail, false)
	fmt.Fprint(w, html.EscapeString(renderPackageBriefing(view, packageBriefingChars)))
	titles := make([]string, 0, len(view.Members))
	for _, m := range view.Members {
		titles = append(titles, m.Title)
	}
	a.writeDueDecisions(w, pa, ax.Project, ax.Branch, view.Name, titles)
}

// renderPackageBriefing ist der kompakte Text für den Sessionstart. Pflicht
// vor optional, innerhalb davon zuerst, was noch offene Kriterien hat;
// Requests ohne offenes Kriterium stehen nur als Zahl da. Die Zeilen werden gezählt, während
// sie entstehen: was nicht mehr passt, steht als "und n weitere" da, nie als
// stilles Abschneiden.
func renderPackageBriefing(d requestdomain.PackageDetail, budget int) string {
	members := append([]requestdomain.PackageMemberView(nil), d.Members...)
	sort.SliceStable(members, func(i, j int) bool {
		a, b := members[i], members[j]
		if (a.Role == requestdomain.RolePflicht) != (b.Role == requestdomain.RolePflicht) {
			return a.Role == requestdomain.RolePflicht
		}
		return a.Open > 0 && b.Open == 0
	})
	var b strings.Builder
	fmt.Fprintf(&b, "\n## Work package (ghosttree)\n\n%s: %s, on branch %s", d.HumanID(), shorten(d.Name, 120), shorten(d.Branch, 120))
	if d.PR > 0 {
		fmt.Fprintf(&b, ", PR #%d", d.PR)
	}
	if d.InheritedFrom != "" {
		fmt.Fprintf(&b, " (inherited from %s)", shorten(d.InheritedFrom, 120))
	}
	b.WriteString("\n")
	openTotal, finished := 0, 0
	var tasks []string
	var lines []string
	for _, m := range members {
		openTotal += m.Open
		if m.State != "open" || m.Open == 0 {
			finished++
			continue
		}
		role := ""
		if m.Role == requestdomain.RoleOptional {
			role = " (optional)"
		}
		lines = append(lines, fmt.Sprintf("- REQ-%d%s %s: %d open, %d met\n", m.RequestID, role, shorten(m.Title, 90), m.Open, m.Met))
	}
	for _, m := range members {
		for _, t := range m.Tasks {
			tasks = append(tasks, fmt.Sprintf("- TASK-%d [%s] %s (REQ-%d)\n", t.ID, t.State, shorten(t.Title, 90), m.RequestID))
		}
	}
	fmt.Fprintf(&b, "%d requests, %d open criteria", len(members), openTotal)
	if finished > 0 {
		fmt.Fprintf(&b, ", %d with nothing open", finished)
	}
	b.WriteString(". Requirements still open:\n")
	writeBudgeted(&b, lines, budget, "package_get")
	if len(tasks) > 0 {
		b.WriteString("Waiting on a human:\n")
		writeBudgeted(&b, tasks, budget, "package_get")
	}
	b.WriteString("Work for this package is recorded on its requests; package action=review lists every criterion with its evidence.\n")
	return b.String()
}

// writeBudgeted schreibt Zeilen, solange das Budget des ganzen Abschnitts reicht,
// und nennt den Rest mit Zahl.
func writeBudgeted(b *strings.Builder, lines []string, budget int, tool string) {
	for i, line := range lines {
		if b.Len()+len(line) > budget && i > 0 {
			fmt.Fprintf(b, "- and %d more (%s)\n", len(lines)-i, tool)
			return
		}
		b.WriteString(line)
	}
}

func shorten(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
