package server

import "net/http"

var painTaskRoutes = map[string]routeClass{
	"POST /api/pains":              classProject,
	"GET /api/pains":               classProject,
	"GET /api/pains/{id}":          classProject,
	"POST /api/pains/{id}/state":   classProject,
	"PATCH /api/pains/{id}":        classProject,
	"POST /api/pains/{id}/links":   classProject,
	"DELETE /api/pain-links/{id}":  classProject,
	"POST /api/tasks":              classProject,
	"GET /api/tasks":               classProject,
	"GET /api/tasks/{id}":          classProject,
	"POST /api/tasks/done":         classProject,
	"POST /api/tasks/{id}/confirm": classProject,
	"POST /api/tasks/{id}/reopen":  classProject,
	"POST /api/tasks/{id}/drop":    classProject,
}

func init() {
	for pattern, class := range painTaskRoutes {
		accessRoutes[pattern] = class
	}
}

func (a *api) registerPainTaskRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "POST /api/pains", a.createPain)
	a.routeFunc(mux, "GET /api/pains", a.searchPains)
	a.routeFunc(mux, "GET /api/pains/{id}", a.getPain)
	a.routeFunc(mux, "POST /api/pains/{id}/state", a.setPainState)
	a.routeFunc(mux, "PATCH /api/pains/{id}", a.correctPain)
	a.routeFunc(mux, "POST /api/pains/{id}/links", a.addPainLink)
	a.routeFunc(mux, "DELETE /api/pain-links/{id}", a.removePainLink)
	a.routeFunc(mux, "POST /api/tasks", a.createHumanTask)
	a.routeFunc(mux, "GET /api/tasks", a.listHumanTasks)
	a.routeFunc(mux, "GET /api/tasks/{id}", a.getHumanTask)
	a.routeFunc(mux, "POST /api/tasks/done", a.markTasksDone)
	a.routeFunc(mux, "POST /api/tasks/{id}/confirm", a.taskTransition("confirm"))
	a.routeFunc(mux, "POST /api/tasks/{id}/reopen", a.taskTransition("reopen"))
	a.routeFunc(mux, "POST /api/tasks/{id}/drop", a.taskTransition("drop"))
}
