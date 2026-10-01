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
	store         *store.Store
	sessions      *sessions
	oidc          *oidcClient
	bootstrapFile string
}
type pageData struct {
	Title, NavSection, Person, CSRFToken, Error, Code string
	OIDC, Paste, NeedsName                            bool
	Admin, Approved, Interactive                      bool
	DeviceMachine, DeviceRemote, DeviceStarted        string
	Tokens                                            []tokenRow
	Requests                                          []requestdomain.SearchHit
	Request                                           requestdomain.Detail
	RequestThreads                                    []coordThreadView
	Knowledge                                         []store.Knowledge
	Sessions                                          []store.Session
	Chunks                                            []store.Chunk
	SessionID                                         int64
	Project, Preview                                  string
	Review                                            []reviewEntry
	Coord                                             coordPageView
	Orgs                                              orgsView
	Invite                                            bool
}
type reviewEntry struct {
	Knowledge         store.Knowledge
	Evidence          []store.Evidence
	MigrationEvidence *store.MigrationEvidence
	Recurrence        int
}

func New(st *store.Store, opts ...Option) http.Handler {
	return newApp(st, opts...)
}

// appHandler gibt Tests Zugriff auf den app-Zustand hinter dem Handler.
type appHandler struct {
	http.Handler
	app *app
}

func newApp(st *store.Store, opts ...Option) http.Handler {
	a := &app{store: st, sessions: newSessions()}
	for _, opt := range opts {
		opt(a)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.FileServerFS(files))
	mux.HandleFunc("GET /ui/login", a.loginPage)
	mux.Handle("POST /ui/login", requireSameOrigin(http.HandlerFunc(a.loginSubmit)))
	mux.Handle("POST /ui/login/oidc", requireSameOrigin(http.HandlerFunc(a.oidcStart)))
	mux.HandleFunc("GET /ui/login/oidc/callback", a.oidcCallback)
	mux.HandleFunc("GET /ui/login/code", a.codePage)
	mux.Handle("POST /ui/login/code", requireSameOrigin(http.HandlerFunc(a.codeSubmit)))
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
	mux.Handle("GET /ui/device", a.requirePerson(http.HandlerFunc(a.devicePage)))
	mux.Handle("POST /ui/device", a.requirePerson(a.requireInteractive(limitBody(a.requireCSRF(http.HandlerFunc(a.deviceLookup))))))
	mux.Handle("POST /ui/device/decide", a.requirePerson(a.requireInteractive(limitBody(a.requireCSRF(http.HandlerFunc(a.deviceDecide))))))
	mux.Handle("GET /ui/account/tokens", a.requirePerson(http.HandlerFunc(a.tokensPage)))
	mux.Handle("POST /ui/account/tokens/revoke", a.requirePerson(limitBody(a.requireCSRF(http.HandlerFunc(a.tokenRevoke)))))
	mux.Handle("GET /ui/orgs", a.requirePerson(http.HandlerFunc(a.orgsPage)))
	// Verwaltung nur aus einer interaktiven Sitzung, nicht aus eingefügtem Token.
	for path, h := range map[string]http.HandlerFunc{
		"/ui/orgs/invite": a.orgInvite, "/ui/orgs/invite/revoke": a.orgInviteRevoke,
		"/ui/orgs/member/role": a.orgMemberRole, "/ui/orgs/member/remove": a.orgMemberRemove,
		"/ui/orgs/project/move": a.orgProjectMove, "/ui/orgs/project/role": a.orgProjectRole,
	} {
		mux.Handle("POST "+path, a.requirePerson(a.requireInteractive(limitBody(a.requireCSRF(h)))))
	}
	for path, h := range map[string]http.HandlerFunc{
		"/ui/orgs/accept": a.orgAccept, "/ui/orgs/default": a.orgDefault,
	} {
		mux.Handle("POST "+path, a.requirePerson(limitBody(a.requireCSRF(h))))
	}
	mux.HandleFunc("GET /ui/{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ui/requests", http.StatusSeeOther) })
	return &appHandler{Handler: mux, app: a}
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
	data.CSRFToken = csrfOf(r)
	data.Interactive = interactive(r)
	switch name {
	case "requests", "request":
		data.NavSection = "requests"
	case "knowledge":
		data.NavSection = "knowledge"
	case "review":
		data.NavSection = "review"
	case "sessions", "session":
		data.NavSection = "sessions"
	case "coord":
		data.NavSection = "coord"
	case "context":
		data.NavSection = "context"
	case "tokens", "device", "devicecheck", "devicedone", "interactive":
		data.NavSection = "tokens"
	case "orgs":
		data.NavSection = "orgs"
	}
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
