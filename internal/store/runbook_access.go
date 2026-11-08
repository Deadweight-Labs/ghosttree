package store

import (
	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// runbookObject beschreibt einen vorhandenen Ablauf für die Matrix. Eigen ist er
// für das Konto, das ihn angelegt hat; auf der Maschinen-Achse zählt die
// Maschine wie bei Wissen.
func (a *ProjectAccess) runbookObject(ref RunbookRef) Object {
	obj := Object{Own: a.valid && ref.AuthorID > 0 && ref.AuthorID == a.account, Machine: ref.Machine}
	if ref.Machine != "" {
		obj.SharedOrgs = a.sharedOrgsOf(EntryRunbook, ref.ID, ref.Machine)
	}
	return obj
}

// OwnsRunbook: das Konto hat den Ablauf angelegt.
func (a *ProjectAccess) OwnsRunbook(ref RunbookRef) bool { return a.runbookObject(ref).Own }

// CanSeeRunbook: Lesefilter für Listen und Auslöser.
func (a *ProjectAccess) CanSeeRunbook(ref RunbookRef) bool {
	return a.Allow(ref.Project, ResRunbook, ActRead, a.runbookObject(ref))
}

// CheckRunbook prüft eine Aktion auf einen vorhandenen Ablauf. Ein Ablauf, den
// es nicht gibt, und einer, den der Aufrufer nicht sehen darf, bekommen vom
// Aufrufer dieselbe Antwort (ErrAccessNotFound).
//
// Wer den Ablauf nicht lesen darf, bekommt auch bei Schreib- und
// Freigabeaktionen "gibt es nicht", nie "verboten": ein Gast erführe sonst durch
// den Unterschied, dass es den Ablauf gibt.
func (a *ProjectAccess) CheckRunbook(ref RunbookRef, act Action) error {
	obj := a.runbookObject(ref)
	if act == ActApprove {
		// Die Freigabe macht eine Fassung ausführbar und ist deshalb nie vom
		// Durchsetzungsmodus abhängig: im Log-Modus gibt Check() alles frei, hier
		// zählt allein die Entscheidung der Matrix. Wer den Ablauf nicht lesen
		// darf, bekommt "gibt es nicht", wer ihn liest und nicht freigeben darf,
		// "verboten".
		a.checks.Add(1)
		if !a.Decide(ref.Project, ResRunbook, ActRead, obj).Allowed {
			return ErrAccessNotFound
		}
		if !a.MayApproveRunbook(ref.Project, ref.Machine, obj.Own) {
			return ErrAccessForbidden
		}
		return nil
	}
	if act != ActRead {
		if err := a.Check(ref.Project, ResRunbook, ActRead, obj); err != nil {
			return err
		}
	}
	return a.Check(ref.Project, ResRunbook, act, obj)
}

// CheckRunbookCreate prüft das Anlegen eines neuen Ablaufs in dieser Geltung.
func (a *ProjectAccess) CheckRunbookCreate(project, machine string) error {
	project = scope.NormalizeRemote(project)
	obj := Object{Own: true, Machine: canonicalMachine(machine)}
	// Wer in diesem Projekt keine Abläufe lesen darf (ein Gast), bekommt "gibt es
	// nicht" und erfährt nichts über das, was dort liegt.
	if err := a.Check(project, ResRunbook, ActRead, obj); err != nil {
		return err
	}
	return a.Check(project, ResRunbook, ActCreate, obj)
}

// MayApproveRunbook sagt, ob der Aufrufer eine Fassung dieses Ablaufs freigeben
// darf (Owner oder Lead des Projekts; ohne Projekt der Autor). Ohne
// Durchsetzung und ohne Entscheidung im Log-Modus: die Freigabe ist ein
// Sicherheitsmerkmal und gilt auch dann, wenn die Zugriffsprüfung nur
// mitprotokolliert.
func (a *ProjectAccess) MayApproveRunbook(project, machine string, ownRunbook bool) bool {
	project = scope.NormalizeRemote(project)
	d := a.Decide(project, ResRunbook, ActApprove, Object{Own: ownRunbook, Machine: canonicalMachine(machine)})
	if !d.Allowed {
		return false
	}
	if project == "" {
		return ownRunbook && a.valid
	}
	return RoleRank(a.Role(project).Role) >= 3
}

// RunbookTitles löst die verlinkten Fallen eines Ablaufs zu Titeln auf, soweit
// der Betrachter sie sehen darf. Was er nicht sehen darf oder was es nicht gibt,
// fehlt ohne Spur.
func (a *ProjectAccess) RunbookTitles(ids []int64) []runbook.PitfallRef {
	var out []runbook.PitfallRef
	for _, id := range ids {
		k, err := a.st.KnowledgeByID(id)
		if err != nil || !a.CanSeeKnowledge(k) {
			continue
		}
		title := []rune(k.Title)
		if len(title) > 160 {
			title = title[:160]
		}
		out = append(out, runbook.PitfallRef{ID: id, Title: runbookOneLine(string(title))})
	}
	return out
}

func runbookOneLine(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if runbook.BadRune(r) {
			r = ' '
		}
		out = append(out, r)
	}
	return string(out)
}
