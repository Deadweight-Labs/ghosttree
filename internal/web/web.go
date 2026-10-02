// Package web serves Ghosttree's operator-facing HTML interface.
package web

import (
	"bytes"
	"database/sql"
	"embed"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"sync"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/proxytrust"
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
	registered    []string
	cursorOnce    sync.Once
	cursorSeal    *flowSealer
	proxies       proxytrust.Set
	publicOrigin  string // scheme://host of GHOSTTREE_PUBLIC_URL, or empty
	publicHTTPS   bool
	joinLimits    *joinLimiter
	distSums      distCache
	distDir       string // ctx archives + checksums.txt served at /dist/; empty = off
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
	a := &app{store: st, sessions: newSessions(), joinLimits: newJoinLimiter()}
	for _, opt := range opts {
		opt(a)
	}
	mux := http.NewServeMux()
	a.handle(mux, "GET /static/", http.FileServerFS(files))
	a.handle(mux, "GET /ui/login", a.loginPage)
	a.handle(mux, "POST /ui/login", a.requireSameOrigin(http.HandlerFunc(a.loginSubmit)))
	a.handle(mux, "POST /ui/login/oidc", a.requireSameOrigin(http.HandlerFunc(a.oidcStart)))
	a.handle(mux, "GET /ui/login/oidc/callback", a.oidcCallback)
	a.handle(mux, "GET /ui/login/code", a.codePage)
	a.handle(mux, "POST /ui/login/code", a.requireSameOrigin(http.HandlerFunc(a.codeSubmit)))
	a.handle(mux, "GET /install.sh", a.installSh)
	a.handle(mux, "GET /dist/{name}", a.distFile)
	a.handle(mux, "GET /join/{code}", a.joinPage)
	a.handle(mux, "GET /join/", func(w http.ResponseWriter, r *http.Request) {
		if a.joinGate(w, r) {
			a.joinNotFound(w)
		}
	})
	a.handle(mux, "GET /join/pair", a.requirePerson(a.requireInteractive(http.HandlerFunc(a.joinPairPage))))
	a.handle(mux, "POST /join/pair", a.requirePerson(a.requireInteractive(limitBody(a.requireCSRF(http.HandlerFunc(a.joinPairCreate))))))
	a.handle(mux, "POST /join/pair/decide", a.requirePerson(a.requireInteractive(limitBody(a.requireCSRF(http.HandlerFunc(a.joinPairDecide))))))
	a.handle(mux, "POST /join/{code}/accept", a.requirePerson(a.requireInteractive(limitBody(a.requireCSRF(http.HandlerFunc(a.joinAccept))))))
	a.handle(mux, "POST /join/{code}/signout", a.requirePerson(limitBody(a.requireCSRF(http.HandlerFunc(a.joinSignOut)))))
	a.handle(mux, "POST /ui/logout", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.logout))))
	a.handle(mux, "GET /ui/requests", a.requirePerson(http.HandlerFunc(a.requestsPage)))
	a.handle(mux, "GET /ui/requests/{id}", a.requirePerson(http.HandlerFunc(a.requestPage)))
	a.handle(mux, "GET /ui/knowledge", a.requirePerson(http.HandlerFunc(a.knowledgePage)))
	a.handle(mux, "GET /ui/review", a.requirePerson(http.HandlerFunc(a.reviewPage)))
	a.handle(mux, "GET /ui/sessions", a.requirePerson(http.HandlerFunc(a.sessionsPage)))
	a.handle(mux, "GET /ui/sessions/{id}", a.requirePerson(http.HandlerFunc(a.sessionPage)))
	a.handle(mux, "GET /ui/context", a.requirePerson(http.HandlerFunc(a.contextPage)))
	a.handle(mux, "GET /ui/coord", a.requirePerson(http.HandlerFunc(a.coordRoomPage)))
	a.handle(mux, "GET /ui/coord/events", a.requirePerson(http.HandlerFunc(a.coordEvents)))
	a.handle(mux, "GET /ui/coord/thread/{id}", a.requirePerson(http.HandlerFunc(a.coordThreadPage)))
	a.handle(mux, "POST /ui/coord/send", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordSend))))
	a.handle(mux, "POST /ui/coord/thread/create", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordCreateThread))))
	a.handle(mux, "POST /ui/coord/thread/post", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordPostThread))))
	a.handle(mux, "POST /ui/coord/thread/state", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordSetThreadState))))
	a.handle(mux, "POST /ui/coord/read", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordMarkRead))))
	a.handle(mux, "POST /ui/coord/unread", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordMarkUnread))))
	a.handle(mux, "POST /ui/coord/attention/action", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordAttentionAction))))
	a.handle(mux, "POST /ui/coord/standing/end", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordEndStanding))))
	a.handle(mux, "POST /ui/coord/standing/create", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordCreateStanding))))
	a.handle(mux, "POST /ui/coord/direct/start", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordStartDirect))))
	a.handle(mux, "POST /ui/coord/group/create", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordCreateGroup))))
	a.handle(mux, "POST /ui/coord/group/update", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordUpdateGroup))))
	a.handle(mux, "POST /ui/coord/group/leave", a.requirePerson(a.requireCSRF(http.HandlerFunc(a.coordLeaveGroup))))
	a.handle(mux, "POST /ui/coord/agent/control", a.requirePerson(a.requireInteractive(limitBody(a.requireCSRF(http.HandlerFunc(a.coordAgentControl))))))
	a.handle(mux, "GET /ui/device", a.requirePerson(http.HandlerFunc(a.devicePage)))
	a.handle(mux, "POST /ui/device", a.requirePerson(a.requireInteractive(limitBody(a.requireCSRF(http.HandlerFunc(a.deviceLookup))))))
	a.handle(mux, "POST /ui/device/decide", a.requirePerson(a.requireInteractive(limitBody(a.requireCSRF(http.HandlerFunc(a.deviceDecide))))))
	a.handle(mux, "GET /ui/account/tokens", a.requirePerson(http.HandlerFunc(a.tokensPage)))
	a.handle(mux, "POST /ui/account/tokens/revoke", a.requirePerson(limitBody(a.requireCSRF(http.HandlerFunc(a.tokenRevoke)))))
	a.handle(mux, "GET /ui/orgs", a.requirePerson(http.HandlerFunc(a.orgsPage)))
	// Verwaltung nur aus einer interaktiven Sitzung, nicht aus eingefügtem Token.
	for path, h := range map[string]http.HandlerFunc{
		"/ui/orgs/invite": a.orgInvite, "/ui/orgs/invite/revoke": a.orgInviteRevoke,
		"/ui/orgs/member/role": a.orgMemberRole, "/ui/orgs/member/remove": a.orgMemberRemove,
		"/ui/orgs/project/move": a.orgProjectMove, "/ui/orgs/project/role": a.orgProjectRole,
	} {
		a.handle(mux, "POST "+path, a.requirePerson(a.requireInteractive(limitBody(a.requireCSRF(h)))))
	}
	for path, h := range map[string]http.HandlerFunc{
		"/ui/orgs/accept": a.orgAccept, "/ui/orgs/default": a.orgDefault,
	} {
		a.handle(mux, "POST "+path, a.requirePerson(limitBody(a.requireCSRF(h))))
	}
	a.handle(mux, "GET /ui/{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ui/requests", http.StatusSeeOther) })
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
	pa := a.access(r)
	page, err := a.store.SearchRequests(pa.RequestFilter(requestdomain.SearchFilter{State: state, Query: r.URL.Query().Get("q"), Limit: 25}))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pa.NoteRequestHits(page.Results)
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
	if a.accessDenied(w, r, a.access(r).Check(detail.Request.Scope.Project, store.ResRequest, store.ActRead, store.Object{})) {
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
	pa := a.access(r)
	if project != "" && a.accessDenied(w, r, pa.GateList(project, store.ResKnowledge, true)) {
		return
	}
	limit := 0
	if q != "" {
		limit = 50
		entries, err = a.store.SearchAllKnowledge(q, scope.Axes{Project: project}, a.overfetch(limit))
	} else {
		entries, err = a.store.KnowledgeForProject(project)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	entries = keep(entries, limit, pa.CanSeeKnowledge)
	a.renderBrowser(w, r, "knowledge", pageData{Title: "Knowledge", Knowledge: entries, Project: project})
}

func (a *app) reviewPage(w http.ResponseWriter, r *http.Request) {
	pa := a.access(r)
	entries, err := a.store.PendingKnowledge("", a.overfetch(50))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	entries = keep(entries, 50, pa.CanSeeKnowledge)
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
	pa := a.access(r)
	entries, err := a.store.ListSessions(scope.Axes{Project: scope.NormalizeRemote(r.URL.Query().Get("project"))}, a.overfetch(50))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	entries = keep(entries, 50, pa.CanSeeSessionMeta)
	a.renderBrowser(w, r, "sessions", pageData{Title: "Sessions", Sessions: entries})
}

func (a *app) sessionPage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sess, err := a.store.SessionByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if a.accessDenied(w, r, a.access(r).CheckTranscript(sess, store.ActRead)) {
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
	pa := a.access(r)
	entries = keep(entries, 0, func(k store.Knowledge) bool { return pa.CanSeeKnowledge(k) && pa.CanDeliverKnowledge(k) })
	output := server.RenderBootstrap(entries, 12000)
	if preview {
		output = server.RenderBootstrapPreview(entries, 12000)
	}
	a.renderBrowser(w, r, "context", pageData{Title: "Agent Context", Project: project, Preview: output})
}

// access ist die Zugriffsprüfung der Browser-Sitzung. Sie nutzt denselben Store
// und damit denselben Schalter (GHOSTTREE_ENFORCE_ACCESS) wie die API.
func (a *app) access(r *http.Request) *store.ProjectAccess {
	return a.store.Access(browserPrincipal(r))
}

func (a *app) overfetch(limit int) int {
	if a.store.AccessEnforced() {
		return limit * 4
	}
	return limit
}

// accessDenied antwortet auf einen Zugriffsfehler mit 404 oder 403.
func (a *app) accessDenied(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrAccessForbidden):
		http.Error(w, "forbidden", http.StatusForbidden)
	default:
		http.NotFound(w, r)
	}
	return true
}

// keep behält die Einträge, die allow zulässt, höchstens limit viele (0 = alle).
func keep[T any](in []T, limit int, allow func(T) bool) []T {
	out := make([]T, 0, len(in))
	for _, item := range in {
		if allow(item) {
			out = append(out, item)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out
}

// Die Routentabelle der Weboberfläche: jede Route ist klassifiziert, und ein Test
// hält die Tabelle und die Registrierung deckungsgleich.
type webClass string

const (
	// webPublic: ohne Sitzung erreichbar (Login, statische Dateien).
	webPublic webClass = "public"
	// webAccount: braucht eine Sitzung und zeigt nur Kontodaten.
	webAccount webClass = "account"
	// webProject: Daten eines Projekts; die Seite filtert über ProjectAccess.
	webProject webClass = "project"
	// webCoord: Koordination; CoordAccess entscheidet.
	webCoord webClass = "coord"
	// webAdmin: Verwaltung, nur aus einer interaktiven Sitzung (#2411).
	webAdmin webClass = "admin"
)

var webRoutes = map[string]webClass{
	"GET /static/":                    webPublic,
	"GET /install.sh":                 webPublic,
	"GET /dist/{name}":                webPublic,
	"GET /join/{code}":                webPublic,
	"GET /join/":                      webPublic,
	"GET /join/pair":                  webAccount,
	"POST /join/pair":                 webAdmin,
	"POST /join/pair/decide":          webAdmin,
	"POST /join/{code}/accept":        webAdmin,
	"POST /join/{code}/signout":       webAccount,
	"GET /ui/login":                   webPublic,
	"POST /ui/login":                  webPublic,
	"POST /ui/login/oidc":             webPublic,
	"GET /ui/login/oidc/callback":     webPublic,
	"GET /ui/login/code":              webPublic,
	"POST /ui/login/code":             webPublic,
	"GET /ui/{$}":                     webPublic,
	"POST /ui/logout":                 webAccount,
	"GET /ui/requests":                webProject,
	"GET /ui/requests/{id}":           webProject,
	"GET /ui/knowledge":               webProject,
	"GET /ui/review":                  webProject,
	"GET /ui/sessions":                webProject,
	"GET /ui/sessions/{id}":           webProject,
	"GET /ui/context":                 webProject,
	"GET /ui/coord":                   webCoord,
	"GET /ui/coord/events":            webCoord,
	"GET /ui/coord/thread/{id}":       webCoord,
	"POST /ui/coord/send":             webCoord,
	"POST /ui/coord/thread/create":    webCoord,
	"POST /ui/coord/thread/post":      webCoord,
	"POST /ui/coord/thread/state":     webCoord,
	"POST /ui/coord/read":             webCoord,
	"POST /ui/coord/unread":           webCoord,
	"POST /ui/coord/attention/action": webCoord,
	"POST /ui/coord/standing/end":     webCoord,
	"POST /ui/coord/standing/create":  webCoord,
	"POST /ui/coord/direct/start":     webCoord,
	"POST /ui/coord/group/create":     webCoord,
	"POST /ui/coord/group/update":     webCoord,
	"POST /ui/coord/group/leave":      webCoord,
	"GET /ui/device":                  webAccount,
	"POST /ui/device":                 webAdmin,
	"POST /ui/device/decide":          webAdmin,
	"POST /ui/coord/agent/control":    webAdmin,
	"GET /ui/account/tokens":          webAccount,
	"POST /ui/account/tokens/revoke":  webAccount,
	"GET /ui/orgs":                    webAccount,
	"POST /ui/orgs/invite":            webAdmin,
	"POST /ui/orgs/invite/revoke":     webAdmin,
	"POST /ui/orgs/member/role":       webAdmin,
	"POST /ui/orgs/member/remove":     webAdmin,
	"POST /ui/orgs/project/move":      webAdmin,
	"POST /ui/orgs/project/role":      webAdmin,
	"POST /ui/orgs/accept":            webAccount,
	"POST /ui/orgs/default":           webAccount,
}

// handle registriert eine Route und bricht ab, wenn sie nicht klassifiziert ist.
// h ist ein http.Handler oder eine Funktion (w, r).
func (a *app) handle(mux *http.ServeMux, pattern string, h any) {
	if _, ok := webRoutes[pattern]; !ok {
		panic("web: route " + pattern + " is not classified in webRoutes")
	}
	a.registered = append(a.registered, pattern)
	switch h := h.(type) {
	case http.Handler:
		mux.Handle(pattern, h)
	case func(http.ResponseWriter, *http.Request):
		mux.HandleFunc(pattern, h)
	default:
		panic("web: unsupported handler type for " + pattern)
	}
}

// WebRouteClasses gibt die Tabelle zur Ansicht her.
func WebRouteClasses() map[string]string {
	out := make(map[string]string, len(webRoutes))
	for pattern, class := range webRoutes {
		out[pattern] = string(class)
	}
	return out
}
