// Package web serves Ghosttree's operator-facing HTML interface.
package web

import (
	"bytes"
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
	Title, Person, Principal, CSRFToken, Error string
	Requests                                   []requestdomain.SearchHit
	Request                                    requestdomain.Detail
	RequestThreads                             []coordThreadView
	Knowledge                                  []store.Knowledge
	Sessions                                   []store.Session
	Chunks                                     []store.Chunk
	SessionID                                  int64
	Project, Preview                           string
	Review                                     []reviewEntry
	Coord                                      coordPageView
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
	mux.Handle("POST /ui/login", requireSameOrigin(http.HandlerFunc(a.loginSubmit)))
	mux.Handle("POST /ui/logout", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.logout))))
	mux.Handle("GET /ui/requests", a.requirePerson(http.HandlerFunc(a.requestsPage)))
	mux.Handle("GET /ui/requests/{id}", a.requirePerson(http.HandlerFunc(a.requestPage)))
	mux.Handle("GET /ui/knowledge", a.requirePerson(http.HandlerFunc(a.knowledgePage)))
	mux.Handle("GET /ui/review", a.requirePerson(http.HandlerFunc(a.reviewPage)))
	mux.Handle("GET /ui/sessions", a.requirePerson(http.HandlerFunc(a.sessionsPage)))
	mux.Handle("GET /ui/sessions/{id}", a.requirePerson(http.HandlerFunc(a.sessionPage)))
	mux.Handle("GET /ui/context", a.requirePerson(http.HandlerFunc(a.contextPage)))
	mux.Handle("GET /ui/coord", a.requirePerson(http.HandlerFunc(a.coordRoomPage)))
	mux.Handle("GET /ui/coord/events", a.requirePerson(http.HandlerFunc(a.coordEvents)))
	mux.Handle("GET /ui/coord/thread/{id}", a.requirePerson(http.HandlerFunc(a.coordThreadPage)))
	mux.Handle("POST /ui/coord/send", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordSend))))
	mux.Handle("POST /ui/coord/thread/create", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordCreateThread))))
	mux.Handle("POST /ui/coord/thread/post", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordPostThread))))
	mux.Handle("POST /ui/coord/thread/state", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordSetThreadState))))
	mux.Handle("POST /ui/coord/read", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordMarkRead))))
	mux.Handle("POST /ui/coord/unread", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordMarkUnread))))
	mux.Handle("POST /ui/coord/attention/action", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordAttentionAction))))
	mux.Handle("POST /ui/coord/standing/end", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordEndStanding))))
	mux.Handle("POST /ui/coord/standing/create", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordCreateStanding))))
	mux.Handle("POST /ui/coord/direct/start", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordStartDirect))))
	mux.Handle("POST /ui/coord/group/create", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordCreateGroup))))
	mux.Handle("POST /ui/coord/group/update", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordUpdateGroup))))
	mux.Handle("POST /ui/coord/group/leave", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordLeaveGroup))))
	mux.HandleFunc("GET /ui/{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ui/requests", http.StatusSeeOther) })
	return mux
}

func (a *app) render(w http.ResponseWriter, name string, data pageData) {
	var output bytes.Buffer
	if err := pages.ExecuteTemplate(&output, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = output.WriteTo(w)
}
func (a *app) renderBrowser(w http.ResponseWriter, r *http.Request, name string, data pageData) {
	principal := browserPrincipal(r)
	data.Person = principal.Label
	data.Principal = principal.ID
	data.CSRFToken = csrfOf(r)
	a.render(w, name, data)
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
	a.renderBrowser(w, r, "requests", pageData{Title: "Requests", Requests: page.Results})
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
	linked, err := a.browserCoord(r).ThreadsForObject("request", detail.Request.HumanID())
	if err != nil {
		coordHTTPError(w, err)
		return
	}
	threadViews := make([]coordThreadView, 0, len(linked))
	for _, thread := range linked {
		home, homeErr := a.browserCoord(r).ThreadHome(thread.ID)
		if homeErr != nil {
			continue
		}
		threadViews = append(threadViews, coordThreadView{ID: thread.ID, Title: thread.Title, Question: thread.Question, State: thread.State, URL: coordThreadURL(home.RoomKey, thread.ID)})
	}
	a.renderBrowser(w, r, "request", pageData{Title: detail.Request.HumanID(), Request: detail, RequestThreads: threadViews})
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
	a.renderBrowser(w, r, "knowledge", pageData{Title: "Knowledge", Knowledge: entries, Project: project})
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
	a.renderBrowser(w, r, "review", pageData{Title: "Review", Review: items})
}

func (a *app) sessionsPage(w http.ResponseWriter, r *http.Request) {
	entries, err := a.store.ListSessions(scope.Axes{Project: scope.NormalizeRemote(r.URL.Query().Get("project"))}, 50)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.renderBrowser(w, r, "sessions", pageData{Title: "Sessions", Sessions: entries})
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
	a.renderBrowser(w, r, "session", pageData{Title: "Session " + strconv.FormatInt(id, 10), SessionID: id, Chunks: chunks})
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
	a.renderBrowser(w, r, "context", pageData{Title: "Agent Context", Project: project, Preview: output})
}
