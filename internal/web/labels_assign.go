package web

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Labels setzen und entfernen an Issue, Pain und Idee (REQ-600 K10). Die Rechte
// sind die des Stores (SetLabels: Arbeiten am Objekt, also ab member), die Namen
// wählt man nur aus den Labels des Projekts und seiner Org. Das Formular ist die
// Seitenleiste des Objekts; ohne Recht zeigt sie die Labels ohne Formular.

// labelBoxView ist der Block „Labels“ in der Seitenleiste eines Objekts.
type labelBoxView struct {
	Kind, Token string
	ID          int64
	Items       []rowLabel
	// Options sind die Labels des Projekts, die das Objekt noch nicht trägt.
	Options []string
	CanEdit bool
}

// labelKinds sind die Objekte, an denen die Seiten Labels bedienen.
var labelKinds = []string{store.CommentOnRequest, store.CommentOnPain, store.CommentOnIdea}

// labelBox baut den Block. nil heißt: nichts zu zeigen (keine Labels und kein
// Recht, welche zu setzen).
func (a *app) labelBox(r *http.Request, pa *store.ProjectAccess, project, kind string, id int64, current []rowLabel) *labelBoxView {
	v := &labelBoxView{Kind: kind, ID: id, Items: current, Token: csrfOf(r)}
	if interactive(r) && pa.Check(project, store.ResLabel, store.ActRead, store.Object{}) == nil {
		if _, err := pa.LabelTarget(kind, id, store.ActWork); err == nil {
			v.CanEdit = true
		}
	}
	if v.CanEdit {
		have := map[string]bool{}
		for _, l := range current {
			have[l.Text] = true
		}
		if all, err := pa.ListLabels(project); err == nil {
			for _, l := range all {
				if !have[l.Name] {
					v.Options = append(v.Options, l.Name)
				}
			}
		}
	}
	if len(v.Items) == 0 && !v.CanEdit {
		return nil
	}
	return v
}

// labelAssign ist POST /ui/labels/assign (kind, id, add, remove): setzt Labels
// an ein Objekt und nimmt welche weg. Was der Betrachter nicht lesen darf, ist
// 404, was er lesen, aber nicht ändern darf, 403. Das Ziel der Weiterleitung
// berechnet der Server, es kommt nie aus der Anfrage.
func (a *app) labelAssign(w http.ResponseWriter, r *http.Request) {
	kind := r.FormValue("kind")
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	add, remove := formNames(r.Form["add"]), formNames(r.Form["remove"])
	if err != nil || id <= 0 || !slices.Contains(labelKinds, kind) || len(add)+len(remove) == 0 || len(add) > 20 || len(remove) > 20 {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	pa := a.access(r)
	if _, err := pa.SetLabels(kind, id, add, remove); err != nil {
		switch {
		case errors.Is(err, store.ErrAccessForbidden):
			http.Error(w, "forbidden", http.StatusForbidden)
		case errors.Is(err, store.ErrAccessNotFound):
			http.NotFound(w, r)
		default:
			// Ein Name, den es im Projekt nicht gibt, kommt nur aus einem
			// gefälschten Formular: die Auswahl bietet ihn nie an.
			http.Error(w, "invalid input", http.StatusBadRequest)
		}
		return
	}
	http.Redirect(w, r, a.objectPage(pa, kind, id)+"#labels", http.StatusSeeOther)
}

// formNames macht aus Formularwerten saubere Labelnamen: ohne Leeres, ohne
// Doppelte, jeder höchstens 80 Zeichen lang.
func formNames(in []string) []string {
	var out []string
	for _, n := range in {
		n = strings.TrimSpace(n)
		if n == "" || len(n) > 80 || slices.Contains(out, n) {
			continue
		}
		out = append(out, n)
	}
	return out
}
