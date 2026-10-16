package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func (a *api) taskActor(r *http.Request, sessionID int64) humantask.Actor {
	return humantask.Actor{
		AccountID: authorIDOf(r), Person: personOf(r),
		Web:       principalOf(r).TokenKind == store.WebSessionKind,
		SessionID: a.callerSession(r, sessionID),
	}
}

func (a *api) createHumanTask(w http.ResponseWriter, r *http.Request) {
	var in humantask.CreateInput
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	in.Project = scope.CanonicalAxes(scope.Axes{Project: in.Project}).Project
	if in.Project != "" && !a.gateProject(w, r, in.Project) {
		return
	}
	in.SessionID = a.callerSession(r, in.SessionID)
	task, err := a.access(r).CreateHumanTask(in, a.taskActor(r, in.SessionID))
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, task)
}

func (a *api) listHumanTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	project := axesFromQuery(r).Project
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	// Any role may ask; the filter on every row decides what shows.
	pa := a.access(r)
	if denyAccess(w, pa.Check(project, store.ResProject, store.ActRead, store.Object{})) {
		return
	}
	request, _ := strconv.ParseInt(strings.TrimPrefix(strings.ToUpper(q.Get("request_id")), "REQ-"), 10, 64)
	list, err := a.st.ListHumanTasks(pa, store.TaskFilter{
		Project: project, State: q.Get("state"), Mine: q.Get("mine") == "1", RequestID: request, Limit: intParam(r, "limit", 200),
	})
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *api) getHumanTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad task id")
		return
	}
	task, err := a.access(r).GetHumanTask(id)
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (a *api) markTasksDone(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs       []int64 `json:"ids"`
		Note      string  `json:"note"`
		SessionID int64   `json:"session_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	tasks, err := a.access(r).MarkTasksDone(body.IDs, body.Note, a.taskActor(r, body.SessionID))
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (a *api) taskTransition(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r)
		if !ok {
			writeErr(w, http.StatusBadRequest, "bad task id")
			return
		}
		var body struct {
			Reason    string `json:"reason"`
			SessionID int64  `json:"session_id"`
		}
		if r.ContentLength != 0 {
			if err := readJSON(r, &body); err != nil {
				writeStoreError(w, http.StatusBadRequest, err)
				return
			}
		}
		pa, actor := a.access(r), a.taskActor(r, body.SessionID)
		var task humantask.Task
		var err error
		switch op {
		case "confirm":
			task, err = pa.ConfirmHumanTask(id, actor)
		case "reopen":
			task, err = pa.ReopenHumanTask(id, body.Reason, actor)
		default:
			task, err = pa.DropHumanTask(id, body.Reason, actor)
		}
		if err != nil {
			writePainError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, task)
	}
}
