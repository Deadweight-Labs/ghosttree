package web

import (
	"errors"
	"hash/fnv"
	"net/http"
	"slices"
	"strconv"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
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
	// NewHref is the page where a label is made (the labels of the project); set for whoever may make one.
	NewHref string
	// CanMake: the block offers a field that makes a label and puts it on the object in one step.
	CanMake bool
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
		v.NewHref = newLabelHref(pa, project)
		v.CanMake = v.NewHref != ""
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

// newLabelHref is the address of the page where labels are made, for whoever may make one ("" for the others).
func newLabelHref(pa *store.ProjectAccess, project string) string {
	if project == "" || !pa.Allow(project, store.ResLabel, store.ActCreate, store.Object{Own: true}) {
		return ""
	}
	return repoHref(project) + "/settings#labels"
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
		case errors.Is(err, store.ErrAccessNotFound):
			http.NotFound(w, r)
		case errors.Is(err, store.ErrAccessForbidden):
			// Wer kein Recht hat, sieht das Formular nicht; das hier ist ein gebautes.
			http.Error(w, "forbidden", http.StatusForbidden)
		default:
			// Ein Name, den es im Projekt nicht gibt: die Seite war älter als eine
			// Umbenennung. Die Antwort steht auf der Seite des Objekts, statt einer
			// leeren Fehlerseite: ein Fehlschlag ohne Meldung sah aus wie „es passiert
			// nichts“ (R04).
			if page := a.objectPage(pa, kind, id); page != "/ui/inbox" {
				http.Redirect(w, r, page+"?fail=label#labels", http.StatusSeeOther)
				return
			}
			http.Error(w, "invalid input", http.StatusBadRequest)
		}
		return
	}
	http.Redirect(w, r, a.objectPage(pa, kind, id)+"?done=label#labels", http.StatusSeeOther)
}

// labelAssignResult is the line a page shows after the label form: the refusals as
// ?fail= and the success as ?done=label. Nil for anything else.
func labelAssignResult(r *http.Request, l lang) *requestResult {
	q := r.URL.Query()
	switch {
	case q.Get("fail") == "label":
		return &requestResult{Text: l.msg("labels.fail"), Kind: "failed", Fail: true}
	case q.Get("done") == "label":
		return &requestResult{Text: l.msg("labels.done"), Kind: "done"}
	}
	return nil
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

// labelMakeColors are the colours a label made in place starts with; the name picks one, so labels made
// one after the other do not all look alike. The colour can be changed in the settings of the repository.
var labelMakeColors = []string{"#0969da", "#1a7f37", "#8250df", "#d73a4a", "#bf8700", "#008672", "#bc4c00", "#cf222e"}

func labelMakeColor(name string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(name)))
	return labelMakeColors[int(h.Sum32())%len(labelMakeColors)]
}

// makeLabels creates the labels of the names that the project does not have yet, so that
// the caller can put them on an object. A name that exists is used as it is.
func makeLabels(pa *store.ProjectAccess, project string, names []string) error {
	for _, name := range names {
		if _, err := pa.CreateLabel(store.LabelInput{Project: project, Name: name, Color: labelMakeColor(name)}); err != nil && requestdomain.Code(err) != "label_exists" {
			return err
		}
	}
	return nil
}

// labelMake is POST /ui/labels/make (kind, id, name): makes the label in the project of the object and
// puts it on the object. A name that exists is just put on.
func (a *app) labelMake(w http.ResponseWriter, r *http.Request) {
	kind := r.FormValue("kind")
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	names := formNames([]string{r.FormValue("name")})
	if err != nil || id <= 0 || !slices.Contains(labelKinds, kind) || len(names) != 1 {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	pa := a.access(r)
	project, err := pa.LabelTarget(kind, id, store.ActWork)
	if err != nil {
		if errors.Is(err, store.ErrAccessNotFound) {
			http.NotFound(w, r)
			return
		}
		a.accessDenied(w, r, err)
		return
	}
	page := a.objectPage(pa, kind, id)
	if err := makeLabels(pa, project, names); err != nil {
		if a.accessDenied(w, r, err) {
			return
		}
		a.failFormTo(w, r, page+"#labels", http.StatusUnprocessableEntity, a.langFor(r).msg("labels.make_fail"), "name")
		return
	}
	if _, err := pa.SetLabels(kind, id, names, nil); err != nil {
		a.failFormTo(w, r, page+"#labels", http.StatusUnprocessableEntity, a.langFor(r).msg("labels.make_fail"), "name")
		return
	}
	http.Redirect(w, r, page+"?done=label#labels", http.StatusSeeOther)
}
