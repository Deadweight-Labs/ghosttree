// Package web serves Ghosttree's operator-facing HTML interface.
package web

import (
	"database/sql"
	"embed"
	"html/template"
	"net/http"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

//go:embed templates static
var files embed.FS

var pages = template.Must(template.ParseFS(files, "templates/*.html"))

type app struct {
	store    *store.Store
	sessions *sessions
}
type pageData struct {
	Title, Person, Error string
	Requests             []requestdomain.SearchHit
	Request              requestdomain.Detail
	Knowledge            []store.Knowledge
	Sessions             []store.Session
	Chunks               []store.Chunk
	SessionID            int64
	Project, Preview     string
	Review               []reviewEntry
}
type reviewEntry struct {
	Knowledge         store.Knowledge
	Evidence          []store.Evidence
	MigrationEvidence *store.MigrationEvidence
	Recurrence        int
}

func New(st *store.Store) http.Handler {
	a := &app{store: st, sessions: newSessions()}
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.FileServerFS(files))
	mux.HandleFunc("GET /ui/login", a.loginPage)
	mux.HandleFunc("POST /ui/login", a.loginSubmit)
	mux.HandleFunc("POST /ui/logout", a.logout)
	mux.Handle("GET /ui/requests", a.requirePerson(http.HandlerFunc(a.requestsPage)))
	mux.Handle("GET /ui/requests/{id}", a.requirePerson(http.HandlerFunc(a.requestPage)))
	mux.Handle("GET /ui/knowledge", a.requirePerson(http.HandlerFunc(a.knowledgePage)))
	mux.Handle("GET /ui/review", a.requirePerson(http.HandlerFunc(a.reviewPage)))
	mux.Handle("GET /ui/sessions", a.requirePerson(http.HandlerFunc(a.sessionsPage)))
	mux.Handle("GET /ui/sessions/{id}", a.requirePerson(http.HandlerFunc(a.sessionPage)))
	mux.Handle("GET /ui/context", a.requirePerson(http.HandlerFunc(a.contextPage)))
	mux.HandleFunc("GET /ui/{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ui/requests", http.StatusSeeOther) })
	return mux
}

func (a *app) render(w http.ResponseWriter, name string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pages.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
func (a *app) requestsPage(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if _, exists := r.URL.Query()["state"]; !exists {
		state = "open"
	}
	page, err := a.store.SearchRequests(requestdomain.SearchFilter{State: state, Query: r.URL.Query().Get("q"), Limit: 25})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.render(w, "requests", pageData{Title: "Requests", Person: personOf(r), Requests: page.Results})
}
func (a *app) requestPage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	detail, err := a.store.RequestByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a.render(w, "request", pageData{Title: detail.Request.HumanID(), Person: personOf(r), Request: detail})
}

func (a *app) knowledgePage(w http.ResponseWriter, r *http.Request) {
	q, project := r.URL.Query().Get("q"), scope.NormalizeRemote(r.URL.Query().Get("project"))
	var entries []store.Knowledge
	var err error
	if q != "" {
		entries, err = a.store.SearchAllKnowledge(q, scope.Axes{Project: project}, 50)
	} else {
		entries, err = a.store.KnowledgeForProject(project)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.render(w, "knowledge", pageData{Title: "Knowledge", Person: personOf(r), Knowledge: entries, Project: project})
}

func (a *app) reviewPage(w http.ResponseWriter, r *http.Request) {
	entries, err := a.store.PendingKnowledge("", 50)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	items := make([]reviewEntry, 0, len(entries))
	for _, k := range entries {
		evidence, err := a.store.EvidenceFor(k.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		recurrence, err := a.store.Recurrence(k.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		proof, err := a.store.MigrationEvidenceForKnowledge(k.ID)
		var migrationProof *store.MigrationEvidence
		if err == nil {
			migrationProof = &proof
		} else if err != sql.ErrNoRows {
			http.Error(w, err.Error(), 500)
			return
		}
		items = append(items, reviewEntry{Knowledge: k, Evidence: evidence, MigrationEvidence: migrationProof, Recurrence: recurrence})
	}
	a.render(w, "review", pageData{Title: "Review", Person: personOf(r), Review: items})
}

func (a *app) sessionsPage(w http.ResponseWriter, r *http.Request) {
	entries, err := a.store.ListSessions(scope.Axes{Project: scope.NormalizeRemote(r.URL.Query().Get("project"))}, 50)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.render(w, "sessions", pageData{Title: "Sessions", Person: personOf(r), Sessions: entries})
}

func (a *app) sessionPage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	chunks, err := a.store.ReadSession(id, 0, 500)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.render(w, "session", pageData{Title: "Session " + strconv.FormatInt(id, 10), Person: personOf(r), SessionID: id, Chunks: chunks})
}

func (a *app) contextPage(w http.ResponseWriter, r *http.Request) {
	project := scope.NormalizeRemote(r.URL.Query().Get("project"))
	preview := r.URL.Query().Get("preview") == "1"
	var entries []store.Knowledge
	var err error
	if preview {
		entries, err = a.store.KnowledgeForActivatedPreview(scope.Axes{Project: project}, activation.Context{})
	} else {
		entries, err = a.store.KnowledgeForContext(scope.Axes{Project: project})
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	output := server.RenderBootstrap(entries, 12000)
	if preview {
		output = server.RenderBootstrapPreview(entries, 12000)
	}
	a.render(w, "context", pageData{Title: "Agent Context", Person: personOf(r), Project: project, Preview: output})
}
