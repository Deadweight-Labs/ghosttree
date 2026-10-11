package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Verschieben (REQ-458): ein Eintrag, der am falschen Ort liegt, geht in ein
// anderes Projekt, auf eine andere Maschine oder nach global. Das Formular
// steht auf der Detailseite; Quelle und Ziel prüft der Server wie in der API.

type moveRow struct{ Text, By, Age string }

type moveView struct {
	Projects       []filterOption
	GlobalSelected bool
	// Machine ist der Maschinenname des Eintrags (leer, wenn er keiner Maschine
	// gehört). Ohne Änderung bleibt die Auswahl ein Nichts-zu-tun.
	Machine string
	Token   string
}

// writableOptions listet die Projekte, in die der Betrachter schreiben darf,
// mit dem aktuellen vorgewählt.
func writableOptions(pa *store.ProjectAccess, current string) []filterOption {
	var out []filterOption
	list, err := pa.WritableProjects()
	if err != nil {
		return out
	}
	for _, p := range list {
		out = append(out, filterOption{Value: p, Label: p, Selected: p == current})
	}
	return out
}

func (a *app) knowledgeMoveView(r *http.Request, pa *store.ProjectAccess, k store.Knowledge) *moveView {
	return &moveView{Projects: writableOptions(pa, k.Scope.Project), GlobalSelected: k.Scope.Project == "", Machine: k.Scope.Machine, Token: csrfOf(r)}
}

func (a *app) knowledgeMove(w http.ResponseWriter, r *http.Request) {
	k, ok := a.lookupKnowledge(w, r, store.ActEdit)
	if !ok {
		return
	}
	pa := a.access(r)
	project := strings.TrimSpace(r.FormValue("project"))
	machine := strings.TrimSpace(r.FormValue("machine"))
	// The machine field is prefilled with the entry's machine; a chosen project
	// wins over it instead of failing the form.
	if project != "" {
		machine = ""
	}
	var to scope.Axes
	switch {
	case project != "":
		list, err := pa.WritableProjects()
		if err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		resolved, err := scope.ResolveProject(project, list)
		if err != nil {
			// Unknown and not writable answer alike.
			http.NotFound(w, r)
			return
		}
		to.Project = resolved
	case machine != "":
		if err := a.store.MachineClaimable(machine, browserPrincipal(r).ID); err != nil {
			http.Error(w, "nothing to change", http.StatusConflict)
			return
		}
		to.Machine = scope.CanonicalAxes(scope.Axes{Machine: machine}).Machine
	}
	dest := k
	dest.Scope = to
	if a.accessDenied(w, r, pa.CheckKnowledgeCreate(dest)) {
		return
	}
	if to.IsGlobal() && !pa.MayMakeGlobal(k) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := a.store.MoveKnowledge(k.ID, to, personOf(r)); err != nil {
		if errors.Is(err, store.ErrNothingToMove) {
			http.Error(w, "nothing to change", http.StatusConflict)
			return
		}
		http.Error(w, "could not save", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/ui/knowledge/"+strconv.FormatInt(k.ID, 10), http.StatusSeeOther)
}

func (a *app) requestMove(w http.ResponseWriter, r *http.Request) {
	detail, ok := a.lookupRequest(w, r, store.ActEdit)
	if !ok {
		return
	}
	pa := a.access(r)
	reason, valid := cleanText(r.FormValue("reason"), maxFieldBytes)
	if !valid {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	list, err := pa.WritableProjects()
	if err != nil {
		http.Error(w, "could not save", http.StatusInternalServerError)
		return
	}
	project, err := scope.ResolveProject(strings.TrimSpace(r.FormValue("project")), list)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := a.store.MoveRequest(detail.Request.ID, project, personOf(r), reason); err != nil {
		if errors.Is(err, store.ErrNothingToMove) {
			http.Error(w, "nothing to change", http.StatusConflict)
			return
		}
		a.requestFail(w, r, detail.Request.ID, err)
		return
	}
	// Der Auftrag liegt jetzt in einem anderen Projekt; die Seite bleibt
	// erreichbar, solange der Betrachter es lesen darf.
	http.Redirect(w, r, requestURL(detail.Request.ID)+"?"+url.Values{"done": {"moved"}}.Encode(), http.StatusSeeOther)
}

func (a *app) knowledgeMoveRows(pa *store.ProjectAccess, k store.Knowledge) []moveRow {
	moves, err := a.store.KnowledgeMoves(k.ID)
	if err != nil || !pa.CanSeeKnowledgeHistory(k) {
		return nil
	}
	now := overviewNow().UTC()
	out := make([]moveRow, 0, len(moves))
	for _, m := range moves {
		out = append(out, moveRow{Text: m.From + " → " + m.To, By: m.MovedBy, Age: shortAge(now, parseTime(m.MovedAt))})
	}
	return out
}
