package web

import (
	"database/sql"
	"errors"
	"fmt"
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

// taskRow is one line of a task list. Where is the place a secret or file lives,
// never its content: a task has no field for a value.
type taskRow struct {
	ID                 int64
	Human, Kind, Title string
	KindLabel, Where   string
	Req, ReqHref       string
	Desc               string
	State, StateLabel  string
	Note, By, Age      string
	Project            string
	CanDone            bool
}

type tasksView struct {
	Rows    []taskRow
	Project string
	Token   string
	Result  *requestResult
	Back    string
	// Any: at least one row can be ticked; without it the form has no button.
	Any bool
	// Everyone: the list shows every task the viewer may see, not only those
	// for them. Switch is the link to the other list.
	Everyone bool
	// Who is the filter value that goes back with the done form, "all" for Everyone.
	Who    string
	Switch string
}

// visibleProjects names the projects a list covers: the selected one, or every
// project the viewer has a role in.
func visibleProjects(pa *store.ProjectAccess, project string) []string {
	if project != "" {
		return []string{project}
	}
	return slices.Sorted(slices.Values(pa.Projects()))
}

// taskRows turns tasks into rows. Everything passed in already went through
// ProjectAccess.CanSeeTask (ListHumanTasks); the request link shows only when
// the viewer may read that request.
func (a *app) taskRows(l lang, pa *store.ProjectAccess, tasks []humantask.Task) []taskRow {
	rows := make([]taskRow, 0, len(tasks))
	readable := map[int64]bool{}
	for _, t := range tasks {
		row := taskRow{ID: t.ID, Human: t.HumanID(), Kind: t.Kind, Title: t.Title, KindLabel: label(l, taskKindKeys, t.Kind),
			Where: t.Location, Desc: ellipsize(oneLineText(t.Description), 240), State: t.State, Project: t.Project, Age: shortAge(l, overviewNow().UTC(), parseTime(t.UpdatedAt)),
			CanDone: t.State == humantask.Open && pa.CheckTask(t, store.ActWork) == nil}
		if t.State != humantask.Open {
			row.StateLabel = label(l, taskStateKeys, t.State)
		}
		if t.State == humantask.Done {
			row.Note, row.By = t.DoneNote, t.DoneBy
		}
		ok, seen := readable[t.RequestID]
		if !seen {
			ok = a.requestReadable(pa, t.RequestID)
			readable[t.RequestID] = ok
		}
		if ok {
			row.Req, row.ReqHref = fmt.Sprintf("REQ-%d", t.RequestID), requestURL(t.RequestID)
		}
		rows = append(rows, row)
	}
	return rows
}

// overviewWaiting is the block "Waiting on you": open tasks the viewer can mark
// done. A guest sees only what is assigned to them; the store filter decides.
func (a *app) overviewWaiting(r *http.Request, pa *store.ProjectAccess, project string) (*tasksView, error) {
	l := a.langFor(r)
	var out []taskRow
	for _, p := range visibleProjects(pa, project) {
		tasks, err := a.store.ListHumanTasks(pa, store.TaskFilter{Project: p, State: humantask.Open, Mine: true, Limit: tasksShown})
		if err != nil {
			return nil, err
		}
		for _, row := range a.taskRows(l, pa, tasks) {
			if row.CanDone {
				out = append(out, row)
			}
		}
	}
	if len(out) > overviewMaxWaiting {
		out = out[:overviewMaxWaiting]
	}
	// The block is there with nothing in it too: it is where a person looks.
	return &tasksView{Rows: out, Project: project, Token: csrfOf(r), Back: "overview", Any: len(out) > 0}, nil
}

func anyDoable(rows []taskRow) bool {
	for _, row := range rows {
		if row.CanDone {
			return true
		}
	}
	return false
}

func (a *app) tasksPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	w.Header().Set("Cache-Control", "no-store")
	pa := a.access(r)
	project := selectedProject(a.shellFor(r, "tasks"))
	// Mine is the default: what is assigned to the viewer plus what is for
	// everyone. "Everyone" lists every task the viewer may see.
	everyone := r.URL.Query().Get("who") == "all"
	me, _ := store.AccountNumericID(browserPrincipal(r).ID)
	var all []humantask.Task
	for _, p := range visibleProjects(pa, project) {
		tasks, err := a.store.ListHumanTasks(pa, store.TaskFilter{Project: p, State: "all", Limit: tasksShown})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, t := range tasks {
			// Mine keeps finished tasks too, so the note of a task for everyone
			// that was ticked off stays where the person looks.
			if everyone || t.AssigneeAccount == 0 || t.AssigneeAccount == me {
				all = append(all, t)
			}
		}
	}
	order := map[string]int{humantask.Open: 0, humantask.Done: 1, humantask.Confirmed: 2, humantask.Dropped: 3}
	slices.SortStableFunc(all, func(x, y humantask.Task) int { return order[x.State] - order[y.State] })
	v := &tasksView{Rows: a.taskRows(l, pa, all), Project: project, Token: csrfOf(r), Back: "tasks"}
	v.Everyone = everyone
	if everyone {
		v.Who = "all"
	}
	v.Any = anyDoable(v.Rows)
	switch {
	case r.URL.Query().Get("fail") != "":
		v.Result = &requestResult{Text: l.msg(taskFailKey(r.URL.Query().Get("fail"))), Kind: "failed", Fail: true}
	case r.URL.Query().Get("done") == "1":
		v.Result = &requestResult{Text: l.msg("tasks.result.done"), Kind: "done"}
	}
	a.renderBrowser(w, r, "tasks", pageData{Title: l.msg("tasks.title"), TasksV: v})
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
	back := "/ui/tasks"
	if r.FormValue("back") == "overview" {
		back = "/ui/overview"
	}
	q := url.Values{}
	if project := r.FormValue("project"); project != "" {
		q.Set("project", project)
	}
	if r.FormValue("who") == "all" && back == "/ui/tasks" {
		q.Set("who", "all")
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
	if back == "/ui/tasks" {
		q.Set("done", "1")
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
		http.Redirect(w, r, "/ui/tasks"+queryOf(q), http.StatusSeeOther)
	case requestdomain.Code(err) == "invalid_transition":
		q.Set("fail", "changed")
		http.Redirect(w, r, "/ui/tasks"+queryOf(q), http.StatusSeeOther)
	case requestdomain.Code(err) != "":
		q.Set("fail", "failed")
		http.Redirect(w, r, "/ui/tasks"+queryOf(q), http.StatusSeeOther)
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
