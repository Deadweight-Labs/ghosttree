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
	"strings"
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

var pages = template.Must(template.New("").Funcs(template.FuncMap{"t": msg}).ParseFS(files, "templates/*.html"))

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
	oidcName      string // display name of the identity provider; empty = unnamed
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
	SessionLinks                                      map[int64]string
	SessionsV                                         *sessionsView
	SessionV                                          *sessionView
	Project, Preview                                  string
	Review                                            []reviewEntry
	Coord                                             coordPageView
	Orgs                                              orgsView
	Invite                                            bool
	ProviderName                                      string
	Bootstrap, TokenOpen                              bool
	Shell                                             shellView
	Overview                                          overviewView
	Agents                                            agentsView
	// Refresh: Sekunden bis zum automatischen Neuladen (0 = nie).
	Refresh int
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

// ServeHTTP setzt die Sicherheitsköpfe aller /ui/-Seiten, bevor ein Handler
// antwortet; Handler dürfen sie überschreiben (join.go setzt eigene).
func (h *appHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/ui" || strings.HasPrefix(r.URL.Path, "/ui/") {
		uiHeaders(w.Header())
	}
	h.Handler.ServeHTTP(w, r)
}

// uiHeaders: Skripte, Stile, Schriften und Bilder nur von dieser Herkunft,
// keine fremden Ressourcen, nicht einbettbar. form-action bleibt offen, weil der
// Weg zum Identitätsanbieter eine Weiterleitung ist.
func uiHeaders(h http.Header) {
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "strict-origin")
	h.Set("Cache-Control", "no-store")
}

func newApp(st *store.Store, opts ...Option) http.Handler {
	a := &app{store: st, sessions: newSessions(), joinLimits: newJoinLimiter()}
	for _, opt := range opts {
		opt(a)
	}
	mux := http.NewServeMux()
	a.handle(mux, "GET /static/", a.staticFiles())
	a.handle(mux, "GET /{$}", a.rootRedirect)
	a.handle(mux, "GET /favicon.ico", a.favicon)
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
	a.handle(mux, "GET /ui/overview", a.requirePerson(http.HandlerFunc(a.overviewPage)))
	a.handle(mux, "GET /ui/agents", a.requirePerson(http.HandlerFunc(a.agentsPage)))
	a.handle(mux, "GET /ui/rooms", a.requirePerson(http.HandlerFunc(a.roomsAlias)))
	a.handle(mux, "GET /ui/requests", a.requirePerson(http.HandlerFunc(a.requestsPage)))
	a.handle(mux, "GET /ui/requests/{id}", a.requirePerson(http.HandlerFunc(a.requestPage)))
	a.handle(mux, "GET /ui/knowledge", a.requirePerson(http.HandlerFunc(a.knowledgePage)))
	a.handle(mux, "GET /ui/review", a.requirePerson(http.HandlerFunc(a.reviewPage)))
	a.handle(mux, "GET /ui/sessions", a.requirePerson(http.HandlerFunc(a.sessionsPage)))
	a.handle(mux, "GET /ui/sessions/{id}", a.requirePerson(http.HandlerFunc(a.sessionPage)))
	a.handle(mux, "POST /ui/sessions/{id}/share", a.requirePerson(a.requireInteractive(limitBody(a.requireCSRF(http.HandlerFunc(a.sessionShare))))))
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
	a.handle(mux, "GET /ui/{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ui/overview", http.StatusSeeOther) })
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
	data.Shell = a.shellFor(r, name)
	switch name {
	case "overview":
		data.NavSection = "overview"
	case "agents":
		data.NavSection = "agents"
	case "requests", "request":
		data.NavSection = "requests"
	case "knowledge":
		data.NavSection = "knowledge"
	case "review":
		data.NavSection = "review"
	case "sessions", "session":
		data.NavSection = "sessions"
	case "coord", "roomforbidden":
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
	for i := range page.Results {
		page.Results[i] = pa.RequestHitView(page.Results[i])
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
	detail = a.access(r).RequestDetailView(detail)
	a.renderBrowser(w, r, "request", pageData{Title: detail.Request.HumanID(), Request: detail, RequestThreads: threadViews})
}

// sessionLinks nennt zu Sessionnummern aus Verweisen die Adresse in der
// Weboberfläche, nur für Sessions, die der Betrachter lesen darf. Ohne Eintrag
// steht kein Link; die Nummer selbst erscheint nie in einer Adresse.
func (a *app) sessionLinks(pa *store.ProjectAccess, ids []int64) map[int64]string {
	out := map[int64]string{}
	for _, id := range ids {
		if _, done := out[id]; done {
			continue
		}
		sess, err := a.store.SessionByID(id)
		if err != nil || sess.PublicID == "" || !pa.CanSeeTranscript(sess) {
			continue
		}
		out[id] = "/ui/sessions/" + sess.PublicID
	}
	return out
}

func (a *app) knowledgePage(w http.ResponseWriter, r *http.Request) {
	q, project := r.URL.Query().Get("q"), a.projectParam(r)
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
	entries = pa.KnowledgeViews(keep(entries, limit, pa.CanSeeKnowledge))
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
	var evidenceIDs []int64
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
		evidence, recurrence = pa.ReadableEvidence(k.Scope.Project, evidence, recurrence)
		for _, ev := range evidence {
			evidenceIDs = append(evidenceIDs, ev.SessionID)
		}
		proof, err := a.store.MigrationEvidenceForKnowledge(k.ID)
		var migrationProof *store.MigrationEvidence
		if err == nil {
			migrationProof = &proof
		} else if err != sql.ErrNoRows {
			http.Error(w, err.Error(), 500)
			return
		}
		items = append(items, reviewEntry{Knowledge: pa.KnowledgeView(k), Evidence: evidence, MigrationEvidence: pa.MigrationEvidenceView(k.Scope.Project, migrationProof), Recurrence: recurrence})
	}
	a.renderBrowser(w, r, "review", pageData{Title: "Review", Review: items, SessionLinks: a.sessionLinks(pa, evidenceIDs)})
}

// projectParam ist das Projekt einer projektbezogenen Seite: das genannte,
// sonst das in der Hülle vorgewählte (nur Nicht-Owner haben eines).
func (a *app) projectParam(r *http.Request) string {
	if p := scope.NormalizeRemote(r.URL.Query().Get("project")); p != "" {
		return p
	}
	return selectedProject(a.shellFor(r, "knowledge"))
}

func (a *app) contextPage(w http.ResponseWriter, r *http.Request) {
	project := a.projectParam(r)
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
	"GET /{$}":                        webPublic,
	"GET /favicon.ico":                webPublic,
	"GET /ui/{$}":                     webPublic,
	"GET /ui/overview":                webAccount,
	"GET /ui/agents":                  webCoord,
	"GET /ui/rooms":                   webCoord,
	"POST /ui/logout":                 webAccount,
	"GET /ui/requests":                webProject,
	"GET /ui/requests/{id}":           webProject,
	"GET /ui/knowledge":               webProject,
	"GET /ui/review":                  webProject,
	"GET /ui/sessions":                webProject,
	"GET /ui/sessions/{id}":           webProject,
	"POST /ui/sessions/{id}/share":    webAdmin,
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

// staticFiles liefert die eingebetteten Dateien. Die Schriften ändern sich nur
// mit einer neuen Schriftversion (Ordner v5) und liegen lange im Cache.
func (a *app) staticFiles() http.Handler {
	fileServer := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/fonts/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	})
}
