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
	taskNoteChars      = 500
)

var (
	taskKindKeys  = map[string]string{"secret": "tasks.kind.secret", "file": "tasks.kind.file", "input": "tasks.kind.input", "action": "tasks.kind.action"}
	taskStateKeys = map[string]string{"done": "tasks.state.done", "confirmed": "tasks.state.confirmed", "dropped": "tasks.state.dropped"}
)

// taskHref is where a task leads: its page in the repository (decision #4331).
func taskHref(t humantask.Task) string {
	if t.Project != "" {
		return taskHrefOf(t)
	}
	return "/ui/inbox?f=aufgaben#task-" + strconv.FormatInt(t.ID, 10)
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
	back = a.taskBack(r, ids, q)
	if tooLong(note, taskNoteChars) {
		a.tooLongForm(w, r, back, "note", note, taskNoteChars)
		return
	}
	pa := a.access(r)
	accountID, _ := store.AccountNumericID(browserPrincipal(r).ID)
	actor := humantask.Actor{AccountID: accountID, Person: personOf(r), Web: interactive(r)}
	if _, err := pa.MarkTasksDone(ids, note, actor); err != nil {
		a.taskFail(w, r, back, err)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// taskBack is the page a task form leads back to, on success and on refusal: the page
// of the task for the form on that page, else the list in the inbox.
func (a *app) taskBack(r *http.Request, ids []int64, q url.Values) string {
	if len(ids) == 1 && r.FormValue("back") == "task" {
		// Only a task the viewer may read names its repository in an address.
		if t, err := a.access(r).GetHumanTask(ids[0]); err == nil && t.Project != "" {
			return taskHrefOf(t)
		}
	}
	return "/ui/inbox" + queryOf(q)
}

func queryOf(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

// taskFail goes back to where the form was, with the reason over it and the note
// still in its field (a secret-like note is the one thing the person has to rewrite).
func (a *app) taskFail(w http.ResponseWriter, r *http.Request, back string, err error) {
	l := a.langFor(r)
	switch {
	case errors.Is(err, store.ErrAccessForbidden):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, store.ErrAccessNotFound):
		http.NotFound(w, r)
	case requestdomain.Code(err) == "secret_detected":
		a.failFormKeeping(w, r, back, http.StatusUnprocessableEntity, l.msg(taskFailKey("secret")), "note", "note")
	case requestdomain.Code(err) == "invalid_transition":
		a.failFormTo(w, r, back, http.StatusConflict, l.msg(taskFailKey("changed")), "")
	case requestdomain.Code(err) != "":
		a.failFormTo(w, r, back, http.StatusUnprocessableEntity, l.msg(taskFailKey("failed")), "")
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
	a.handle(mux, "POST /ui/pains/{id}/idea", a.requirePerson(a.requireInteractive(limitBody(a.requireCSRF(http.HandlerFunc(a.painIdea))))))
	a.handle(mux, "POST /ui/pains/{id}/correct", a.requirePerson(a.requireInteractive(limitBody(a.requireCSRF(http.HandlerFunc(a.painCorrect))))))
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
