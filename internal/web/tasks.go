package web

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const (
	overviewMaxWaiting = 8
	tasksShown         = 200
	taskNoteBytes      = 500
)

var (
	taskKindKeys  = map[string]string{"secret": "tasks.kind.secret", "file": "tasks.kind.file", "input": "tasks.kind.input", "action": "tasks.kind.action"}
	taskStateKeys = map[string]string{"done": "tasks.state.done", "confirmed": "tasks.state.confirmed", "dropped": "tasks.state.dropped"}
)

// taskHref is where a task leads: the issue it blocks, else the tasks of the inbox.
func taskHref(t humantask.Task) string {
	anchor := "#task-" + strconv.FormatInt(t.ID, 10)
	if t.RequestID != 0 && t.Project != "" {
		return repoHref(t.Project) + "/issues/" + strconv.FormatInt(t.RequestID, 10) + anchor
	}
	return "/ui/inbox?f=aufgaben" + anchor
}

// visibleProjects names the projects a list covers: the selected one, or every
// project the viewer has a role in.
func visibleProjects(pa *store.ProjectAccess, project string) []string {
	if project != "" {
		return []string{project}
	}
	return slices.Sorted(slices.Values(pa.Projects()))
}

// tasksDone marks the ticked tasks done in one go: all or none. The form has a
// note and no value field.
func (a *app) tasksDone(w http.ResponseWriter, r *http.Request) {
	var ids []int64
	for _, raw := range r.Form["id"] {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	back := "/ui/inbox"
	q := url.Values{"f": {"aufgaben"}}
	if project := r.FormValue("project"); project != "" {
		q.Set("project", project)
	}
	if len(ids) == 0 {
		http.Redirect(w, r, back+queryOf(q), http.StatusSeeOther)
		return
	}
	note := r.FormValue("note")
	if len(note) > taskNoteBytes {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	pa := a.access(r)
	accountID, _ := store.AccountNumericID(browserPrincipal(r).ID)
	actor := humantask.Actor{AccountID: accountID, Person: personOf(r), Web: interactive(r)}
	if _, err := pa.MarkTasksDone(ids, note, actor); err != nil {
		a.taskFail(w, r, q, err)
		return
	}
	http.Redirect(w, r, back+queryOf(q), http.StatusSeeOther)
}

func queryOf(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

func (a *app) taskFail(w http.ResponseWriter, r *http.Request, q url.Values, err error) {
	switch {
	case errors.Is(err, store.ErrAccessForbidden):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, store.ErrAccessNotFound):
		http.NotFound(w, r)
	case requestdomain.Code(err) == "secret_detected":
		q.Set("fail", "secret")
		http.Redirect(w, r, "/ui/inbox"+queryOf(q), http.StatusSeeOther)
	case requestdomain.Code(err) == "invalid_transition":
		q.Set("fail", "changed")
		http.Redirect(w, r, "/ui/inbox"+queryOf(q), http.StatusSeeOther)
	case requestdomain.Code(err) != "":
		q.Set("fail", "failed")
		http.Redirect(w, r, "/ui/inbox"+queryOf(q), http.StatusSeeOther)
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	default:
		http.Error(w, "could not save", http.StatusInternalServerError)
	}
}

// registerPainTaskPages registers the pages for human tasks and pains.
func (a *app) registerPainTaskPages(mux *http.ServeMux) {
	a.handle(mux, "GET /ui/tasks", a.requirePerson(http.HandlerFunc(a.tasksPage)))
	a.handle(mux, "POST /ui/tasks/done", a.requirePerson(a.requireInteractive(limitBody(a.requireCSRF(http.HandlerFunc(a.tasksDone))))))
	a.registerInboxAnswer(mux)
	a.handle(mux, "GET /ui/pains", a.requirePerson(http.HandlerFunc(a.painsPage)))
	a.handle(mux, "GET /ui/pains/{id}", a.requirePerson(http.HandlerFunc(a.painPage)))
	a.handle(mux, "POST /ui/pains/{id}/state", a.requirePerson(a.requireInteractive(limitBody(a.requireCSRF(http.HandlerFunc(a.painState))))))
}

// taskFailKey maps the fail= value of a redirect to its message; anything else
// reads as a plain failure, so the query string cannot inject text.
func taskFailKey(code string) string {
	switch code {
	case "secret":
		return "tasks.result.secret"
	case "changed":
		return "tasks.result.changed"
	}
	return "tasks.result.failed"
}

func oneLineText(s string) string { return strings.Join(strings.Fields(s), " ") }
