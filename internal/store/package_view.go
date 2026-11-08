package store

import (
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// PackageView füllt die Mitglieder eines Pakets mit dem, was der Betrachter vom
// jeweiligen Request lesen darf: Titel, Stand, Kriterien-Zahlen und die
// Human-Tasks, die noch nicht bestätigt sind. Mit full kommen die Kriterien mit
// Nachweisen und Git-Ankern dazu (Prüfansicht).
//
// Einen Request, den der Betrachter nicht lesen darf (fremde, noch gehaltene
// Distillation), lässt die Ansicht weg, ohne Platzhalter und ohne Zahl: ein
// Platzhalter ließe sich mitzählen. Nachweise und Tasks laufen durch dieselbe
// Sichtprüfung wie request_get, es entsteht kein zweiter Weg um sie herum.
func (a *ProjectAccess) PackageView(d requestdomain.PackageDetail, full bool) requestdomain.PackageDetail {
	members := make([]requestdomain.PackageMemberView, 0, len(d.Members))
	var viewed []int64
	for _, m := range d.Members {
		ref, err := a.st.RequestRef(RefRequest, m.RequestID)
		if err != nil || !requestReadable(a, ref) {
			continue
		}
		detail, err := a.st.RequestByID(m.RequestID)
		if err != nil {
			continue
		}
		v := a.RequestDetailView(detail)
		m.Title, m.Type, m.State, m.Priority = v.Request.Title, v.Request.Type, v.Request.State, v.Request.Priority
		for _, c := range v.Criteria {
			switch c.State {
			case "open":
				m.Open++
			case "met":
				m.Met++
			case "waived":
				m.Waived++
			}
		}
		for _, t := range v.Tasks {
			if t.State == humantask.Open || t.State == humantask.Done {
				m.Tasks = append(m.Tasks, t)
			}
		}
		if full {
			m.Criteria = v.Criteria
		}
		members = append(members, m)
		viewed = append(viewed, m.RequestID)
	}
	d.Members = members
	// Die Punkte „Feature X: Beschreibung angepasst?“ für den PR des Pakets
	// (REQ-612).
	d.Features = a.packageFeatureChecks(d.Project, d.PR)
	if full {
		// Anforderungen der Ideen, aus denen die Requests stammen und die noch
		// kein erfülltes Kriterium haben (REQ-611).
		d.Uncovered = a.uncoveredForMembers(viewed)
	}
	return d
}
