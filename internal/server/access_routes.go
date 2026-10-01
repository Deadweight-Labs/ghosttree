package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Die Routentabelle klassifiziert jede registrierte Route. Eine Route, die hier
// fehlt, kann nicht registriert werden (route() bricht beim Start ab), und ein
// Test hält die Tabelle und die Registrierung deckungsgleich. So umgeht keine
// neue Route die Zugriffsprüfung unbemerkt.

// routeClass sagt, WER über den Zugriff auf die Route entscheidet.
type routeClass string

const (
	// classPublic: ohne Token erreichbar (Health, Metriken, Geräte-Login).
	classPublic routeClass = "public"
	// classAccount: verlangt ein Konto und liefert nur Kontodaten (eigene Tokens,
	// Organisationen, eigene Maschinen); die Regeln stehen im Store.
	classAccount routeClass = "account"
	// classProject: Daten eines Projekts. Der Handler muss über ProjectAccess
	// entscheiden (a.access(r)); der Routentest prüft, dass er es tut.
	classProject routeClass = "project"
	// classCoord: Koordination. CoordAccess entscheidet (Mitgliedschaft im Raum,
	// beim Projektraum zusätzlich die Projektrolle).
	classCoord routeClass = "coord"
	// classACL: eigene ACL je Person und Projekt (Kontext-Snapshots).
	classACL routeClass = "acl"
	// classAdmin: Verwaltung, nur aus einer interaktiven Web-Sitzung (#2411);
	// die API antwortet web_session_required.
	classAdmin routeClass = "admin"
)

var accessRoutes = map[string]routeClass{
	"GET /api/health":                             classPublic,
	"GET /metrics":                                classPublic,
	"GET /api/whoami":                             classAccount,
	"POST /api/auth/device":                       classPublic,
	"POST /api/auth/device/token":                 classPublic,
	"GET /api/orgs":                               classAccount,
	"POST /api/orgs":                              classAccount,
	"PATCH /api/orgs/{org}":                       classAccount,
	"GET /api/orgs/{org}/members":                 classAccount,
	"PUT /api/orgs/{org}/members/{account}":       classAccount,
	"DELETE /api/orgs/{org}/members/{account}":    classAccount,
	"GET /api/orgs/{org}/invitations":             classAccount,
	"POST /api/orgs/{org}/invitations":            classAccount,
	"DELETE /api/orgs/{org}/invitations/{id}":     classAccount,
	"POST /api/invitations/accept":                classAccount,
	"PUT /api/account/default-org":                classAccount,
	"GET /api/projects":                           classProject,
	"GET /api/projects/{id}/members":              classProject,
	"PUT /api/projects/{id}/members/{account}":    classAdmin,
	"DELETE /api/projects/{id}/members/{account}": classAdmin,
	"POST /api/projects/claim":                    classAccount,
	"POST /api/projects/move":                     classAccount,
	"POST /api/context-snapshots":                 classACL,
	"GET /api/context-snapshots":                  classACL,
	"GET /api/context-snapshots/{name}":           classACL,
	"GET /api/context-snapshots/{name}/entries":   classACL,
	"POST /api/sessions":                          classProject,
	"GET /api/sessions":                           classProject,
	"GET /api/machines":                           classAccount,
	"POST /api/sessions/{id}/chunks":              classProject,
	"GET /api/sessions/{id}/raw":                  classProject,
	"GET /api/sessions/{id}":                      classProject,
	"POST /api/requests":                          classProject,
	"GET /api/requests":                           classProject,
	"GET /api/requests/search":                    classProject,
	"GET /api/requests/{id}":                      classProject,
	"POST /api/requests/{id}/work":                classProject,
	"PATCH /api/request-work/{id}":                classProject,
	"POST /api/requests/{id}/criteria":            classProject,
	"PATCH /api/criteria/{id}":                    classProject,
	"POST /api/requests/{id}/complete":            classProject,
	"POST /api/requests/{id}/drop":                classProject,
	"POST /api/requests/{id}/relations":           classProject,
	"PATCH /api/requests/{id}":                    classProject,
	"DELETE /api/request-relations/{id}":          classProject,
	"POST /api/knowledge":                         classProject,
	"GET /api/knowledge":                          classProject,
	"GET /api/knowledge/pending":                  classProject,
	"GET /api/knowledge/{id}":                     classProject,
	"GET /api/knowledge/{id}/history":             classProject,
	"PATCH /api/knowledge/{id}":                   classProject,
	"PUT /api/knowledge/{id}/regression":          classProject,
	"GET /api/knowledge/regression-gaps":          classProject,
	"POST /api/migrated-knowledge":                classProject,
	"GET /api/migrations":                         classProject,
	"GET /api/migrations/documents":               classProject,
	"POST /api/migrations":                        classProject,
	"PUT /api/migrations/{id}/complete":           classProject,
	"POST /api/migrations/{id}/documents":         classProject,
	"POST /api/migrations/{id}/documents/import":  classProject,
	"GET /api/search":                             classProject,
	"GET /api/context/bootstrap":                  classProject,
	"GET /api/context/interrupted":                classProject,
	"GET /api/context/relevant":                   classProject,
	"POST /api/ghosts":                            classProject,
	"GET /api/ghosts":                             classProject,
	"GET /api/ghosts/tree":                        classProject,
	"GET /api/ghosts/history":                     classProject,
	"POST /api/ghosts/move":                       classProject,
	"POST /api/ghosts/archive":                    classProject,
	"GET /api/ghosts/archive-candidate":           classProject,
	"GET /api/ghosts/search":                      classProject,
	"POST /api/ghosts/reviews":                    classProject,
	"GET /api/ghosts/reviews":                     classProject,
	"POST /api/coord/agents":                      classCoord,
	"GET /api/coord/agents":                       classCoord,
	"POST /api/coord/messages":                    classCoord,
	"GET /api/coord/messages":                     classCoord,
	"GET /api/coord/messages/{id}/mentions":       classCoord,
	"GET /api/coord/attention":                    classCoord,
	"POST /api/coord/attention/action":            classCoord,
	"GET /api/activity/path":                      classProject,
	"GET /api/activity/session":                   classAccount,
	"POST /api/activity":                          classAccount,
	"POST /api/coord/deliveries":                  classCoord,
	"POST /api/coord/deliveries/claim":            classCoord,
	"GET /api/coord/deliveries/injected":          classCoord,
	"POST /api/coord/rooms":                       classCoord,
	"GET /api/coord/rooms":                        classCoord,
	"POST /api/coord/groups":                      classCoord,
	"POST /api/coord/cursor":                      classCoord,
	"GET /api/coord/cursor":                       classCoord,
	"POST /api/threads":                           classCoord,
	"POST /api/threads/from-message":              classCoord,
	"GET /api/threads/home":                       classCoord,
	"GET /api/threads":                            classCoord,
	"GET /api/threads/for":                        classCoord,
	"GET /api/threads/{id}":                       classCoord,
	"GET /api/threads/{id}/home":                  classCoord,
	"POST /api/threads/{id}/state":                classCoord,
	"POST /api/threads/{id}/touch":                classCoord,
	"POST /api/threads/{id}/links":                classCoord,
	"GET /api/threads/{id}/links":                 classCoord,
	"POST /api/threads/{id}/summary":              classCoord,
	"GET /api/threads/{id}/summary":               classCoord,
	"POST /api/threads/{id}/outcomes":             classCoord,
	"GET /api/threads/{id}/outcomes":              classCoord,
	"POST /api/documents":                         classProject,
	"GET /api/documents":                          classProject,
	"GET /api/documents/{id}":                     classProject,
	"PATCH /api/documents/{id}":                   classProject,
	"PUT /api/documents/{id}/revisions":           classProject,
	"GET /api/documents/{id}/revisions":           classProject,
	"GET /api/documents/{id}/revisions/{rev}":     classProject,
	// Neu in Paket 7: Freigabe einer Session durch ihren Besitzer.
	"PUT /api/sessions/{id}/share": classProject,
}

// AccessRouteClasses gibt die Tabelle zur Ansicht her (Tests, Doku).
func AccessRouteClasses() map[string]string {
	out := make(map[string]string, len(accessRoutes))
	for pattern, class := range accessRoutes {
		out[pattern] = string(class)
	}
	return out
}

// route registriert eine Route und bricht ab, wenn sie nicht klassifiziert ist.
func (a *api) route(mux *http.ServeMux, pattern string, h http.Handler) {
	class, ok := accessRoutes[pattern]
	if !ok {
		panic("server: route " + pattern + " is not classified in accessRoutes")
	}
	a.registered = append(a.registered, pattern)
	if class != classProject {
		mux.Handle(pattern, h)
		return
	}
	mux.Handle(pattern, a.guardChecked(pattern, h))
}

func (a *api) routeFunc(mux *http.ServeMux, pattern string, h http.HandlerFunc) {
	a.route(mux, pattern, h)
}

// accessHolder trägt die Zugriffsprüfung einer Anfrage, damit jede Stelle
// dieselben Rollen benutzt (einmal geladen) und der Wächter zählen kann.
type accessHolder struct {
	once sync.Once
	pa   *store.ProjectAccess
	p    store.Principal
	st   *store.Store
}

type accessKey struct{}

func withAccessHolder(ctx context.Context, st *store.Store, p store.Principal) context.Context {
	return context.WithValue(ctx, accessKey{}, &accessHolder{st: st, p: p})
}

func (h *accessHolder) get() *store.ProjectAccess {
	h.once.Do(func() { h.pa = h.st.Access(h.p) })
	return h.pa
}

// access liefert die ProjectAccess dieser Anfrage.
func (a *api) access(r *http.Request) *store.ProjectAccess {
	if h, ok := r.Context().Value(accessKey{}).(*accessHolder); ok {
		return h.get()
	}
	return a.st.Access(principalOf(r))
}

// statusWriter merkt sich den Status für den Wächter und reicht alles andere
// durch (auch das Fehlerprotokoll der Telemetrie).
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) recordError(class, message string) {
	recordResponseError(w.ResponseWriter, class, message)
}

// guardChecked sorgt dafür, dass eine Projekt-Route nie erfolgreich antwortet,
// ohne dass ProjectAccess gefragt wurde. In der Produktion protokolliert der
// Wächter den Verstoß; die Tests lassen ihn fehlschlagen (uncheckedHook).
func (a *api) guardChecked(pattern string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		h, ok := r.Context().Value(accessKey{}).(*accessHolder)
		if !ok || sw.status >= 400 || sw.status == 0 {
			return
		}
		if h.pa == nil || h.pa.Checks() == 0 {
			a.logger.Error("access: project route answered without an access check", "route", pattern)
			if a.uncheckedHook != nil {
				a.uncheckedHook(pattern)
			}
		}
	})
}

// denyAccess übersetzt einen Zugriffsfehler in 404 oder 403. Gibt true zurück,
// wenn geantwortet wurde.
func denyAccess(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrAccessNotFound):
		writeCoded(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, store.ErrAccessForbidden):
		writeCoded(w, http.StatusForbidden, "forbidden", "your role in this project does not allow that")
	default:
		return false
	}
	return true
}

// overfetch holt bei Durchsetzung mehr Zeilen, als die Seite braucht, weil der
// Filter danach Zeilen entfernt. Im Log-Modus bleibt die Abfrage wie bisher.
func (a *api) overfetch(limit int) int {
	if limit <= 0 {
		limit = 20
	}
	if a.st.AccessEnforced() {
		return limit * 4
	}
	return limit
}

// filterTo behält die Einträge, die keep zulässt, höchstens limit viele.
func filterTo[T any](in []T, limit int, keep func(T) bool) []T {
	out := make([]T, 0, len(in))
	for _, item := range in {
		if keep(item) {
			out = append(out, item)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out
}

// listGate verlangt für eine Liste mit ausdrücklichem Projekt, dass der Aufrufer
// das Projekt lesen darf; sonst 404. Ohne Projekt gilt der Filter auf den
// Einträgen. Der Aufruf zählt auch ohne Projekt als Prüfung.
func (a *api) listGate(w http.ResponseWriter, r *http.Request, project string, res store.Resource) bool {
	return !denyAccess(w, a.access(r).Check(project, res, store.ActRead, store.Object{Confidence: "verified"}))
}

// checkTranscript lädt die Session und prüft Lesen. Eine unbekannte Id verhält
// sich wie ohne Durchsetzung (leere Antwort), mit Durchsetzung wie 404.
func (a *api) checkTranscript(w http.ResponseWriter, r *http.Request, id int64) bool {
	sess, err := a.st.SessionByID(id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return !denyAccess(w, a.access(r).Check("", store.ResTranscript, store.ActRead, store.Object{}))
	case err != nil:
		writeStoreError(w, http.StatusInternalServerError, err)
		return false
	}
	return !denyAccess(w, a.access(r).CheckTranscript(sess, store.ActRead))
}

// shareSession gibt ein Transkript für die Mitglieder des Projekts frei oder
// nimmt die Freigabe zurück. Nur der Besitzer.
func (a *api) shareSession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad session id")
		return
	}
	var body struct {
		Shared bool `json:"shared"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	sess, err := a.st.SessionByID(id)
	if errors.Is(err, sql.ErrNoRows) {
		denyAccess(w, store.ErrAccessNotFound)
		a.access(r).Filtered()
		return
	}
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	// Teilen ist Sache des Besitzers, auch im Log-Modus: die Freigabe ändert,
	// was andere lesen dürfen.
	if denyAccess(w, a.access(r).CheckTranscript(sess, store.ActShare)) {
		return
	}
	if err := a.st.SetSessionShared(id, principalOf(r).ID, body.Shared); err != nil {
		if errors.Is(err, store.ErrNotSessionOwner) {
			denyAccess(w, store.ErrAccessForbidden)
			return
		}
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "shared": body.Shared})
}

// checkKnowledgeRef prüft eine Aktion auf einen Eintrag, ohne den Text zu laden.
// Ein unbekannter Eintrag liefert 404.
func (a *api) checkKnowledgeRef(w http.ResponseWriter, r *http.Request, id int64, act store.Action) bool {
	k, err := a.st.KnowledgeRef(id)
	if errors.Is(err, sql.ErrNoRows) {
		a.access(r).Filtered()
		writeErr(w, http.StatusNotFound, "no such knowledge entry")
		return false
	}
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return false
	}
	return !denyAccess(w, a.access(r).CheckKnowledge(k, act))
}

// checkMigration prüft, dass der Aufrufer im Projekt eines Migrationslaufs
// schreiben darf.
func (a *api) checkMigration(w http.ResponseWriter, r *http.Request, id int64) bool {
	project, err := a.st.MigrationProject(id)
	if errors.Is(err, sql.ErrNoRows) {
		a.access(r).Filtered()
		writeErr(w, http.StatusNotFound, "no such migration run")
		return false
	}
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return false
	}
	return !denyAccess(w, a.access(r).Check(project, store.ResKnowledge, store.ActCreate, store.Object{Own: true}))
}

func (a *api) requestFilter(r *http.Request, f requestdomain.SearchFilter) requestdomain.SearchFilter {
	return a.access(r).RequestFilter(f)
}

// checkRequest prüft eine Aktion auf einen Auftrag oder ein Teilobjekt (Kriterium,
// Arbeit, Relation) anhand seines Projekts und Autors. Eine unbekannte Id
// antwortet wie bisher über den Store (404 aus writeRequestError).
func (a *api) checkRequest(w http.ResponseWriter, r *http.Request, kind store.RequestRefKind, id int64, act store.Action) bool {
	pa := a.access(r)
	ref, err := a.st.RequestRef(kind, id)
	if errors.Is(err, sql.ErrNoRows) {
		pa.Filtered()
		writeRequestError(w, err)
		return false
	}
	if err != nil {
		writeRequestError(w, err)
		return false
	}
	return !denyAccess(w, pa.Check(ref.Project, store.ResRequest, act, store.Object{Own: pa.IsAuthor(ref.Person)}))
}

func (a *api) noteRequestHits(r *http.Request, hits []requestdomain.SearchHit) {
	a.access(r).NoteRequestHits(hits)
}

// checkDocument prüft eine Aktion auf ein vorhandenes Dokument anhand seines
// Projekts und Autors. Ein unbekanntes Dokument antwortet wie bisher.
func (a *api) checkDocument(w http.ResponseWriter, r *http.Request, id int64, act store.Action) bool {
	pa := a.access(r)
	d, err := a.st.DocumentByID(id)
	if err != nil {
		pa.Filtered()
		writeStoreError(w, http.StatusNotFound, err)
		return false
	}
	return !denyAccess(w, pa.Check(d.Project, store.ResDocument, act, store.Object{Own: pa.IsAuthor(d.Person)}))
}

// listGateEntries ist listGate für Handler, die jeden Eintrag danach mit Own
// filtern: eine unbeanspruchte Remote geht dann durch, und der Filter zeigt dem
// Autor das Seine, allen anderen nichts.
func (a *api) listGateEntries(w http.ResponseWriter, r *http.Request, project string, res store.Resource) bool {
	return !denyAccess(w, a.access(r).GateList(project, res, true))
}
