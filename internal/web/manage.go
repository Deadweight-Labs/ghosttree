package web

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Pakete und Features im Browser (REQ-712): Anlegen, Ändern und Verknüpfen sind
// Lead-Aktionen. Die Seiten zeigen den Knopf dem, der ihn benutzen darf; allen
// anderen steht derselbe Knopf gesperrt da, mit der Rolle, die er braucht, und
// den Menschen, die sie vergeben (Muster aus REQ-704, roleneeded.go). Die
// Matrix liefert beides: Check(…, ActEdit) ist für Requests wie für Features
// ab Lead.

// manageBar ist, was eine Seite über die Lead-Knöpfe weiß.
type manageBar struct {
	// Can: der Betrachter darf die Knöpfe benutzen.
	Can bool
	// Need: für alle anderen die Zeile „Ab Lead · frag …“; leer, wo keine Rolle hilft.
	Need string
	// Token is the CSRF token the forms carry.
	Token   string
	Project string
}

// leadBar baut die manageBar für act auf res in project: dieselbe Prüfung wie
// beim Absenden des Formulars und wie über die API.
func (a *app) leadBar(r *http.Request, l lang, pa *store.ProjectAccess, project string, res store.Resource, act store.Action) manageBar {
	b := manageBar{Token: csrfOf(r), Project: project}
	err := pa.Check(project, res, act, store.Object{})
	if err == nil {
		b.Can = true
		return b
	}
	b.Need = roleNeededShort(l, roleNeededOf(err))
	return b
}

// leadProject liest das Projekt eines Formulars und prüft, dass der Betrachter es
// kennt und act auf res dort darf. Es antwortet selbst, wenn nicht (false).
func (a *app) leadProject(w http.ResponseWriter, r *http.Request, res store.Resource, act store.Action) (string, *store.ProjectAccess, bool) {
	remote := scope.NormalizeRemote(strings.TrimSpace(r.FormValue("project")))
	pa := a.access(r)
	if remote == "" {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return "", nil, false
	}
	opts, _ := a.projectOptions(r, pa)
	if !slices.ContainsFunc(opts, func(o projectOption) bool { return o.Remote == remote }) {
		http.NotFound(w, r)
		return "", nil, false
	}
	if a.accessDenied(w, r, pa.Check(remote, res, act, store.Object{})) {
		return "", nil, false
	}
	return remote, pa, true
}

// formNumber reads a positive number from a form value; "#12" and " 12 " read as 12.
func formNumber(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "#")), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// openIssueChoices are the open issues of a project a work package can take, newest first.
type issueChoice struct {
	N     int64
	Title string
}

func (a *app) openIssueChoices(pa *store.ProjectAccess, project string, skip map[int64]bool, limit int) []issueChoice {
	page, err := a.store.SearchRequestsWith(pa.RequestFilter(requestdomain.SearchFilter{Scope: scope.Axes{Project: project}, State: "open", Limit: limit + len(skip)}), store.SearchAuto)
	if err != nil {
		return nil
	}
	var out []issueChoice
	for _, hit := range page.Results {
		if skip[hit.Request.ID] || len(out) >= limit {
			continue
		}
		out = append(out, issueChoice{N: hit.Request.ID, Title: hit.Request.Title})
	}
	return out
}

func init() {
	add := func(key, en, de string) {
		messages[key] = en
		messagesDE[key] = de
	}
	add("issue.move_none", "No other project you may write to", "Kein anderes Projekt, in das du schreiben darfst")
	add("labels.make_btn", "Create and add", "Anlegen und setzen")
	add("labels.make_fail", "The label could not be made. A label name is one line of at most 50 characters.", "Das Label konnte nicht angelegt werden. Ein Labelname ist eine Zeile mit höchstens 50 Zeichen.")
	add("newissue.new_label_ph", "New label, several with commas", "Neues Label, mehrere mit Komma")
	add("newissue.err.label", "A label could not be made. A label name is one line of at most 50 characters.", "Ein Label konnte nicht angelegt werden. Ein Labelname ist eine Zeile mit höchstens 50 Zeichen.")
	add("newissue.err.labels_many", "Make at most five labels at once.", "Lege höchstens fünf Labels auf einmal an.")
}
