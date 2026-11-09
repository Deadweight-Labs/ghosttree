package web

import (
	"context"
	"database/sql"
	"errors"
	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"html"
	"html/template"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Aufgaben (Human-Tasks) als eigene Seiten (REQ-599 Kriterium 16, Entscheidung
// #4331): die Liste des Repositories unter /ui/{org}/{repo}/tasks, die Aufgabe
// unter …/tasks/{n} mit Schritten als Checkliste und Prüfhinweis, die eigenen
// Aufgaben über alle Repositories unter /ui/u/tasks und die Kurzform /ui/t/{n}.
// Die Nummer ist die globale; das Repository im Pfad ordnet nur ein. Liest, was
// die Store-Funktionen dem Betrachter geben: eine Aufgabe, die er nicht lesen
// darf, ist so unbekannt wie eine, die es nicht gibt.

// taskStateKeys für die Seite: offen steht hier in der Sprache der Aufgaben.
var taskPageStates = map[string]string{humantask.Open: "tasks.state.open", humantask.Done: "tasks.state.done",
	humantask.Confirmed: "tasks.state.confirmed", humantask.Dropped: "tasks.state.dropped"}

var taskPageTone = map[string]string{humantask.Open: "p-ok", humantask.Done: "p-warn", humantask.Confirmed: "p-done", humantask.Dropped: "p-mute"}

var taskRowTone = map[string]string{humantask.Open: "open", humantask.Done: "open", humantask.Confirmed: "closed", humantask.Dropped: "drop"}

type tasksView struct {
	Base     string
	Tabs     []issueStateTab
	Rows     []uiRow
	EmptyKey string
	// Mine is true on the page of the viewer's own tasks (no repository above it).
	Mine bool
}

type taskStepView struct {
	N    int
	Text string
	URL  string
	Done bool
	// By and Age say who ticked the step and when; Action is the form that ticks it.
	By, Age string
	Action  string
}

type taskPageView struct {
	// First is the head of the bubble that opens the page: who asked, and when.
	First                           firstHead
	ID                              int64
	Title, KindLabel                string
	State, StateLabel, Tone         string
	Assignee, CreatedBy, Age, Where string
	// SameAsker: the person who asked is the person who is waited for; the head says it once.
	SameAsker                         bool
	Blocks                            []bodyBlock
	CheckHint                         string
	Steps                             []taskStepView
	StepsDone                         int
	Note                              *taskNote
	Request                           *ideaFact
	DoneBy, DoneAge, DoneNote, Reason string
	// Base is the list the task is in; CanDone shows the form that marks it done.
	Base       string
	CanDone    bool
	DoneAction string
	Token      string
	// Composer is the field that reports the task as done: what is written is the note for the agent.
	Composer *composerView
	// Result is the line after a refused report (?fail=).
	Result *requestResult
}

func init() {
	pageAssets["task"] = struct{ CSS, JS []string }{CSS: []string{"ui-labels.css", "ui-issues.css", "ui-issue.css", "ui-tasks.css"}}
	pageAssets["tasks"] = struct{ CSS, JS []string }{CSS: []string{"ui-labels.css", "ui-issues.css"}}
	for pattern, class := range map[string]webClass{
		"GET /ui/u/tasks":  webAccount,
		"GET /ui/u/issues": webAccount,
		"GET /ui/t/{n}":    webProject,
		"GET /ui/r/{n}":    webProject,
		// Ticking a step of a task: only from the web, with the right to do the task.
		"POST /ui/tasks/{id}/steps/{n}": webAdmin,
	} {
		webRoutes[pattern] = class
	}
}

// registerTaskPages hängt die Kurzformen und die persönliche Liste an den Router.
func (a *app) registerTaskPages(mux *http.ServeMux) {
	a.handle(mux, "GET /ui/u/tasks", a.requirePerson(http.HandlerFunc(a.myTasksPage)))
	a.handle(mux, "GET /ui/u/issues", a.requirePerson(http.HandlerFunc(a.myIssuesPage)))
	a.handle(mux, "GET /ui/t/{n}", a.requirePerson(http.HandlerFunc(a.taskShort)))
	a.handle(mux, "GET /ui/r/{n}", a.requirePerson(http.HandlerFunc(a.requestShort)))
	a.handle(mux, "POST /ui/tasks/{id}/steps/{n}", a.requirePerson(a.requireInteractive(limitBody(a.requireCSRF(http.HandlerFunc(a.taskStepSet))))))
}

// canOpenRepo sagt, ob der Betrachter die Repository-Seite des Projekts öffnen darf.
func (a *app) canOpenRepo(r *http.Request, pa *store.ProjectAccess, remote string) bool {
	if !repoPathOK(remote) {
		return false
	}
	opts, _ := a.projectOptions(r, pa)
	return slices.ContainsFunc(opts, func(o projectOption) bool { return o.Remote == remote })
}

// taskHrefOf ist die kanonische Adresse einer Aufgabe: unter ihrem Repository.
func taskHrefOf(t humantask.Task) string {
	return repoHref(t.Project) + "/tasks/" + strconv.FormatInt(t.ID, 10)
}

// taskShort ist /ui/t/{n}: die globale Nummer führt auf die Adresse im Repository
// der Aufgabe, wenn der Betrachter sie lesen und das Repository öffnen darf.
func (a *app) taskShort(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.ParseInt(r.PathValue("n"), 10, 64)
	if err != nil || n <= 0 {
		http.NotFound(w, r)
		return
	}
	pa := a.access(r)
	t, err := pa.GetHumanTask(n)
	if err != nil || !a.canOpenRepo(r, pa, t.Project) {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, taskHrefOf(t), http.StatusFound)
}

// requestShort ist /ui/r/{n}: die Nummer eines Requests führt auf seine Issue-Seite.
func (a *app) requestShort(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.ParseInt(r.PathValue("n"), 10, 64)
	if err != nil || n <= 0 {
		http.NotFound(w, r)
		return
	}
	r.SetPathValue("id", strconv.FormatInt(n, 10))
	a.requestPage(w, r)
}

// myIssuesPage ist /ui/u/issues: die Issues über alle Repositories sind der
// Abschnitt „Issues“ des Posteingangs.
func (a *app) myIssuesPage(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ui/inbox?f=issues", http.StatusFound)
}

func taskRowOf(l lang, t humantask.Task, now time.Time, withRepo bool) uiRow {
	row := uiRow{Icon: "tasks", Tone: taskRowTone[t.State], StateLabel: label(l, taskPageStates, t.State), Num: "#" + strconv.FormatInt(t.ID, 10),
		Title: t.Title, Href: taskHrefOf(t), Kind: label(l, taskKindKeys, t.Kind), Person: firstNonEmpty(t.Assignee, t.CreatedBy),
		Excerpt: ellipsize(oneLineText(firstNonEmpty(t.Location, t.Description)), 160)}
	if at := parseTime(t.CreatedAt); !at.IsZero() {
		row.Age, row.AgeTitle = shortAge(l, now, at), l.dateTime(at.Local())
	}
	// The open list says "open" in its tab; only a state that differs from it gets a pill.
	if t.State != humantask.Open {
		row.Pill, row.PillTone = label(l, taskPageStates, t.State), taskPageTone[t.State]
	}
	if t.State == humantask.Open && t.Assignee != "" {
		row.Waiting = []string{store.NormalizeAccountName(t.Assignee)}
	}
	if t.RequestID != 0 {
		row.Chip = refHTML(l, "request", t.RequestID, repoHref(t.Project)+"/issues/"+strconv.FormatInt(t.RequestID, 10))
	}
	if withRepo {
		row.Meta = append(row.Meta, hrefChip(repoHref(t.Project), lastSegment(t.Project)))
	}
	return row
}

// repoTasksPage ist /ui/{org}/{repo}/tasks[?state=closed].
func (a *app) repoTasksPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	view := repoFrom(r)
	project := view.Project
	pa := a.access(r)
	if _, stop := a.gateList(w, r, pa, project, store.ResTask); stop {
		return
	}
	state := humantask.Open
	if r.URL.Query().Get("state") == "closed" {
		state = "closed"
	}
	base := view.Href + "/tasks"
	count := func(state string) []humantask.Task {
		list, err := a.store.ListHumanTasks(pa, store.TaskFilter{Project: project, State: state, Limit: taskListMax})
		if err != nil {
			return nil
		}
		return list
	}
	open, closed := count(humantask.Open), count("closed")
	shown := open
	if state == "closed" {
		shown = closed
	}
	v := &tasksView{Base: base, EmptyKey: "tasks.empty"}
	v.Tabs = []issueStateTab{
		{Icon: "tasks", Label: "tasks.tab.open", Count: strconv.Itoa(len(open)), Href: base, On: state == humantask.Open},
		{Icon: "done", Label: "tasks.tab.closed", Count: strconv.Itoa(len(closed)), Href: pathWith(base, "state", "closed"), On: state == "closed"},
	}
	now := overviewNow().UTC()
	for _, t := range shown {
		v.Rows = append(v.Rows, taskRowOf(l, t, now, false))
	}
	if len(v.Rows) == 0 && state == "closed" {
		v.EmptyKey = "tasks.empty_closed"
	}
	a.renderBrowser(w, r, "tasks", pageData{Title: l.msg("repo.tab.tasks"), Project: project, TasksV: v})
}

// myTasksPage ist /ui/u/tasks: die offenen Aufgaben, die auf den Betrachter
// warten, über alle Repositories, jede mit der Adresse in ihrem Repository.
func (a *app) myTasksPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	pa := a.access(r)
	now := overviewNow().UTC()
	var all []humantask.Task
	for _, p := range visibleProjects(pa, "") {
		list, err := a.store.ListHumanTasks(pa, store.TaskFilter{Project: p, State: humantask.Open, Mine: true, Limit: taskListMax})
		if err != nil {
			continue
		}
		all = append(all, list...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].CreatedAt > all[j].CreatedAt })
	v := &tasksView{Base: "/ui/u/tasks", EmptyKey: "tasks.empty_mine", Mine: true}
	v.Tabs = []issueStateTab{{Icon: "tasks", Label: "tasks.tab.open", Count: strconv.Itoa(len(all)), Href: v.Base, On: true}}
	for _, t := range all {
		v.Rows = append(v.Rows, taskRowOf(l, t, now, true))
	}
	a.renderBrowser(w, r, "tasks", pageData{Title: l.msg("tasks.mine_title"), TasksV: v})
}

// taskDetail ist /ui/{org}/{repo}/tasks/{n}. Eine Aufgabe eines anderen
// Repositories führt auf dessen Adresse, wenn der Betrachter sie lesen und das
// Repository öffnen darf, sonst ist sie hier unbekannt.
func (a *app) taskDetail(w http.ResponseWriter, r *http.Request, remote, id string) {
	l := a.langFor(r)
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		http.NotFound(w, r)
		return
	}
	pa := a.access(r)
	t, err := pa.GetHumanTask(n)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if t.Project != remote {
		if a.canOpenRepo(r, pa, t.Project) {
			http.Redirect(w, r, taskHrefOf(t), http.StatusFound)
			return
		}
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	q.Set("project", remote)
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = q.Encode()
	view := a.repoViewFor(r2, pa, a.shellBaseFor(r2).who.kind, remote, "tasks")
	r2 = r2.WithContext(context.WithValue(r2.Context(), repoCtxKey{}, view))

	now := overviewNow().UTC()
	v := &taskPageView{ID: t.ID, Title: t.Title, KindLabel: label(l, taskKindKeys, t.Kind), State: t.State, StateLabel: label(l, taskPageStates, t.State),
		Tone: taskPageTone[t.State], Assignee: store.NormalizeAccountName(t.Assignee), CreatedBy: t.CreatedBy, Where: t.Location, CheckHint: t.CheckHint,
		Blocks: bodyBlocks(t.Description, 0), Reason: t.StateNote, DoneBy: t.DoneBy, DoneNote: t.DoneNote, Base: view.Href + "/tasks", Token: csrfOf(r)}
	v.SameAsker = v.Assignee != "" && strings.EqualFold(store.NormalizeAccountName(t.CreatedBy), v.Assignee)
	if at := parseTime(t.CreatedAt); !at.IsZero() {
		v.Age = shortAgo(l, now, at)
	}
	if at := parseTime(t.DoneAt); !at.IsZero() {
		v.DoneAge = shortAgo(l, now, at)
	}
	if t.RequestID != 0 && a.requestReadable(pa, t.RequestID) {
		if d, err := a.store.RequestByID(t.RequestID); err == nil {
			v.Request = &ideaFact{Num: "#" + strconv.FormatInt(t.RequestID, 10), Title: d.Request.Title, Brief: l.msg("ref.kind.issue"),
				Href: view.Href + "/issues/" + strconv.FormatInt(t.RequestID, 10)}
		}
	}
	v.Note = a.taskNoteFor(l, t.ID, nil)
	a.taskSteps(r2, l, pa, t, v)
	v.First = firstOf(l, a.actorViewOf(l, pa, actor.Task, t.ID, t.CreatedBy, true), t.CreatedBy, l.msg("tasks.asked_ch"), v.Age, "", "")
	if interactive(r) && t.State == humantask.Open && pa.CheckTask(t, store.ActWork) == nil {
		v.CanDone, v.DoneAction = true, "/ui/tasks/done"
		v.Composer = &composerView{Action: v.DoneAction, Token: v.Token, Name: "note", Avatar: browserPrincipal(r).Label, Placeholder: l.msg("tasks.note_placeholder"),
			Submit: l.msg("tasks.mark_done"), Hidden: []hiddenField{{"id", strconv.FormatInt(t.ID, 10)}, {"back", "task"}}}
	}
	if code := r.URL.Query().Get("fail"); code != "" {
		v.Result = &requestResult{Text: l.msg(taskFailKey(code)), Kind: "failed", Fail: true}
	}
	a.renderBrowser(w, r2, "task", pageData{Title: t.Title, Project: remote, TaskV: v})
}

// taskSteps füllt die Schritte der Aufgabe als Checkliste.
func (a *app) taskSteps(r *http.Request, l lang, pa *store.ProjectAccess, t humantask.Task, v *taskPageView) {
	marks, _ := a.store.TaskStepMarks(t.ID)
	canTick := interactive(r) && t.State == humantask.Open && pa.CheckTask(t, store.ActWork) == nil
	now := overviewNow().UTC()
	for i, s := range t.Steps {
		step := taskStepView{N: i + 1, Text: s.Text, URL: s.URL}
		if m, ok := marks[i+1]; ok {
			step.Done, step.By = true, store.NormalizeAccountName(m.By)
			if at := parseTime(m.At); !at.IsZero() {
				step.Age = shortAgo(l, now, at)
			}
			v.StepsDone++
		}
		if canTick {
			step.Action = "/ui/tasks/" + strconv.FormatInt(t.ID, 10) + "/steps/" + strconv.Itoa(i+1)
		}
		v.Steps = append(v.Steps, step)
	}
}

// taskStepSet is POST /ui/tasks/{id}/steps/{n}: it ticks a step of the checklist
// (done=1) or takes the tick back (done=0). The right is the one to do the task;
// the page of the task is where it returns.
func (a *app) taskStepSet(w http.ResponseWriter, r *http.Request) {
	id, err1 := strconv.ParseInt(r.PathValue("id"), 10, 64)
	n, err2 := strconv.Atoi(r.PathValue("n"))
	if err1 != nil || err2 != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	pa := a.access(r)
	accountID, _ := store.AccountNumericID(browserPrincipal(r).ID)
	actor := humantask.Actor{AccountID: accountID, Person: personOf(r), Web: interactive(r)}
	if err := pa.SetTaskStep(id, n, r.FormValue("done") == "1", actor); err != nil {
		switch {
		case errors.Is(err, store.ErrAccessForbidden):
			http.Error(w, "forbidden", http.StatusForbidden)
		case errors.Is(err, store.ErrAccessNotFound), errors.Is(err, sql.ErrNoRows):
			http.NotFound(w, r)
		case requestdomain.Code(err) != "":
			http.Error(w, "nothing to change", http.StatusConflict)
		default:
			http.Error(w, "could not save", http.StatusInternalServerError)
		}
		return
	}
	t, err := a.store.TaskByID(id)
	if err != nil || t.Project == "" {
		http.Redirect(w, r, "/ui/inbox", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, taskHrefOf(t)+"#step-"+strconv.Itoa(n), http.StatusSeeOther)
}

// hrefChip ist ein kleiner Verweis auf ein Repository in einer Zeile.
func hrefChip(href, text string) template.HTML {
	return template.HTML(`<a class="fm" href="` + html.EscapeString(href) + `">` + string(mockIcon("repo")) + html.EscapeString(text) + `</a>`)
}

const taskListMax = 200

// tasksTabCount ist die Zahl der offenen Aufgaben, die der Betrachter am Projekt
// lesen darf, für den Reiter.
func (a *app) tasksTabCount(pa *store.ProjectAccess, remote string) string {
	if pa == nil {
		return ""
	}
	list, err := a.store.ListHumanTasks(pa, store.TaskFilter{Project: remote, State: humantask.Open, Limit: repoCountAt})
	if err != nil {
		return ""
	}
	return countText(len(list))
}

// HasFirst says whether the page opens with the bubble: the text, where, the
// short explanation, the steps or how an agent checks it.
func (v *taskPageView) HasFirst() bool {
	return len(v.Blocks) > 0 || v.Where != "" || (v.Note != nil && v.Note.Text != "") || len(v.Steps) > 0 || v.CheckHint != ""
}
