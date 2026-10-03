// Package server exposes the ghosttree store over a small REST API.
// Deployments provide the network perimeter; bearer tokens carry provenance.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/proxytrust"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type SnapshotMirror interface {
	Rebuild(context.Context, string) error
}

type Option func(*api)

type OperationIDGenerator func() (string, error)

type snapshotErrorLogger func(operationID string, err, generatorErr error)

func WithContextSnapshotLimits(limits snapshot.Limits) Option {
	return func(a *api) { a.snapshotLimits = limits }
}

func WithSnapshotMirror(mirror SnapshotMirror) Option {
	return func(a *api) { a.snapshotMirror = mirror }
}

func WithOperationIDGenerator(generator OperationIDGenerator) Option {
	return func(a *api) { a.operationIDGenerator = generator }
}

func WithLogger(logger *slog.Logger) Option {
	return func(a *api) { a.logger = logger }
}

// WithTrustedProxies names the networks whose forwarding headers are believed
// (loopback is always included).
func WithTrustedProxies(s proxytrust.Set) Option { return func(a *api) { a.proxies = s } }

// WithPublicURL fixes the externally visible base URL (scheme://host, no path).
func WithPublicURL(raw string) Option {
	return func(a *api) { a.publicURL = strings.TrimRight(raw, "/") }
}

func WithBuildVersion(version string) Option {
	return func(a *api) { a.buildVersion = version }
}

func withRequestIDGenerator(generator requestIDGenerator) Option {
	return func(a *api) { a.requestIDGenerator = generator }
}

func withSnapshotErrorLogger(logger snapshotErrorLogger) Option {
	return func(a *api) { a.snapshotErrorLogger = logger }
}

type api struct {
	proxies              proxytrust.Set
	publicURL            string
	st                   *store.Store
	snapshotLimits       snapshot.Limits
	snapshotMirror       SnapshotMirror
	operationIDGenerator OperationIDGenerator
	snapshotErrorLogger  snapshotErrorLogger
	logger               *slog.Logger
	buildVersion         string
	requestIDGenerator   requestIDGenerator
	metrics              *metricsRegistry
	registered           []string
	uncheckedHook        func(route string)
}

type personKey struct{}

func New(st *store.Store, options ...Option) http.Handler {
	a := newAPI(st, options...)
	mux := http.NewServeMux()
	a.registerRoutes(mux)
	return a.telemetry(a.auth(a.captureRoute(mux)))
}

func (a *api) registerRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	a.route(mux, "GET /metrics", a.metrics)
	a.routeFunc(mux, "GET /api/whoami", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, a.whoAmI(r))
	})
	a.routeFunc(mux, "POST /api/auth/device", a.startDeviceLogin)
	a.routeFunc(mux, "POST /api/auth/device/token", a.pollDeviceLogin)
	a.routeFunc(mux, "POST /api/join/claim", a.claimJoin)
	a.routeFunc(mux, "POST /api/join/token", a.exchangeJoin)
	a.routeFunc(mux, "DELETE /api/tokens/self", a.revokeOwnToken)
	a.routeFunc(mux, "GET /api/orgs", a.listOrgs)
	a.routeFunc(mux, "POST /api/orgs", a.createOrg)
	a.routeFunc(mux, "PATCH /api/orgs/{org}", a.renameOrg)
	a.routeFunc(mux, "GET /api/orgs/{org}/members", a.listOrgMembers)
	a.routeFunc(mux, "PUT /api/orgs/{org}/members/{account}", a.setOrgMemberRole)
	a.routeFunc(mux, "DELETE /api/orgs/{org}/members/{account}", a.removeOrgMember)
	a.routeFunc(mux, "GET /api/orgs/{org}/invitations", a.listOrgInvitations)
	a.routeFunc(mux, "POST /api/orgs/{org}/invitations", a.createOrgInvitation)
	a.routeFunc(mux, "DELETE /api/orgs/{org}/invitations/{id}", a.revokeOrgInvitation)
	a.routeFunc(mux, "POST /api/invitations/accept", a.acceptInvitation)
	a.routeFunc(mux, "PUT /api/account/default-org", a.setDefaultOrg)
	a.routeFunc(mux, "GET /api/projects", a.listProjects)
	a.routeFunc(mux, "GET /api/projects/{id}/members", a.listProjectMembers)
	a.routeFunc(mux, "PUT /api/projects/{id}/members/{account}", a.setProjectMemberRole)
	a.routeFunc(mux, "DELETE /api/projects/{id}/members/{account}", a.removeProjectMemberRole)
	a.routeFunc(mux, "POST /api/projects/claim", a.claimProject)
	a.routeFunc(mux, "POST /api/projects/move", a.moveProject)
	a.routeFunc(mux, "POST /api/context-snapshots", a.createContextSnapshot)
	a.routeFunc(mux, "GET /api/context-snapshots", a.listContextSnapshots)
	a.routeFunc(mux, "GET /api/context-snapshots/{name}", a.getContextSnapshot)
	a.routeFunc(mux, "GET /api/context-snapshots/{name}/entries", a.contextSnapshotEntries)
	a.routeFunc(mux, "POST /api/sessions", a.createSession)
	a.routeFunc(mux, "GET /api/sessions", a.listSessions)
	a.routeFunc(mux, "GET /api/machines", a.listMachines)
	a.routeFunc(mux, "POST /api/sessions/{id}/chunks", a.appendChunks)
	a.routeFunc(mux, "GET /api/sessions/{id}/raw", a.rawSession)
	a.routeFunc(mux, "PUT /api/sessions/{id}/share", a.shareSession)
	a.routeFunc(mux, "GET /api/sessions/{id}", a.readSession)
	a.routeFunc(mux, "POST /api/requests", a.createRequest)
	a.routeFunc(mux, "GET /api/requests", a.searchRequests)
	a.routeFunc(mux, "GET /api/requests/search", a.searchRequests)
	a.routeFunc(mux, "GET /api/requests/{id}", a.getRequest)
	a.routeFunc(mux, "POST /api/requests/{id}/work", a.startRequestWork)
	a.routeFunc(mux, "PATCH /api/request-work/{id}", a.finishRequestWork)
	a.routeFunc(mux, "POST /api/requests/{id}/criteria", a.addRequestCriterion)
	a.routeFunc(mux, "PATCH /api/criteria/{id}", a.setRequestCriterion)
	a.routeFunc(mux, "POST /api/requests/{id}/complete", a.completeRequest)
	a.routeFunc(mux, "POST /api/requests/{id}/drop", a.dropRequest)
	a.routeFunc(mux, "POST /api/requests/{id}/relations", a.addRequestRelation)
	a.routeFunc(mux, "PATCH /api/requests/{id}", a.correctRequest)
	a.routeFunc(mux, "DELETE /api/request-relations/{id}", a.removeRequestRelation)
	a.routeFunc(mux, "POST /api/knowledge", a.createKnowledge)
	a.routeFunc(mux, "GET /api/knowledge", a.listKnowledge)
	a.routeFunc(mux, "GET /api/knowledge/pending", a.pendingKnowledge)
	a.routeFunc(mux, "GET /api/knowledge/{id}", a.getKnowledge)
	a.routeFunc(mux, "GET /api/knowledge/{id}/history", a.knowledgeHistory)
	a.routeFunc(mux, "PATCH /api/knowledge/{id}", a.patchKnowledge)
	a.routeFunc(mux, "PUT /api/knowledge/{id}/regression", a.setRegressionCover)
	a.routeFunc(mux, "GET /api/knowledge/regression-gaps", a.regressionGaps)
	a.routeFunc(mux, "POST /api/migrated-knowledge", a.insertMigratedKnowledge)
	a.routeFunc(mux, "GET /api/migrations", a.completedMigrationArtifacts)
	a.routeFunc(mux, "GET /api/migrations/documents", a.completedDocumentArtifacts)
	a.routeFunc(mux, "POST /api/migrations", a.beginMigration)
	a.routeFunc(mux, "PUT /api/migrations/{id}/complete", a.completeMigration)
	a.routeFunc(mux, "POST /api/migrations/{id}/documents", a.insertDocumentMigration)
	a.routeFunc(mux, "POST /api/migrations/{id}/documents/import", a.importDocumentMigration)
	a.routeFunc(mux, "GET /api/search", a.search)
	a.routeFunc(mux, "GET /api/context/bootstrap", a.bootstrap)
	a.routeFunc(mux, "GET /api/context/interrupted", a.interrupted)
	a.routeFunc(mux, "GET /api/context/relevant", a.relevant)
	a.routeFunc(mux, "POST /api/ghosts", a.putGhost)
	a.routeFunc(mux, "GET /api/ghosts", a.ghostsForPath)
	a.routeFunc(mux, "GET /api/ghosts/tree", a.ghostTree)
	a.routeFunc(mux, "GET /api/ghosts/history", a.ghostHistory)
	a.routeFunc(mux, "POST /api/ghosts/move", a.ghostsMove)
	a.routeFunc(mux, "POST /api/ghosts/archive", a.archiveGhosts)
	a.routeFunc(mux, "GET /api/ghosts/archive-candidate", a.ghostArchiveCandidate)
	a.routeFunc(mux, "GET /api/ghosts/search", a.searchGhosts)
	a.routeFunc(mux, "POST /api/ghosts/reviews", a.putGhostReview)
	a.routeFunc(mux, "GET /api/ghosts/reviews", a.ghostReviews)
	a.routeFunc(mux, "POST /api/coord/agents", a.registerCoordAgent)
	a.routeFunc(mux, "GET /api/coord/agents", a.coordPeers)
	a.routeFunc(mux, "POST /api/coord/messages", a.sendCoordMessage)
	a.routeFunc(mux, "GET /api/coord/messages", a.coordInbox)
	a.routeFunc(mux, "GET /api/coord/messages/{id}/mentions", a.coordMessageMentions)
	a.routeFunc(mux, "GET /api/coord/attention", a.coordAttention)
	a.routeFunc(mux, "POST /api/coord/attention/action", a.coordAttentionAction)
	a.routeFunc(mux, "GET /api/activity/path", a.pathActivity)
	a.routeFunc(mux, "GET /api/activity/session", a.sessionActivity)
	a.routeFunc(mux, "POST /api/activity", a.recordPathActivity)
	a.routeFunc(mux, "GET /api/agent-control", a.getAgentControl)
	a.routeFunc(mux, "POST /api/agent-control", a.agentControlWebOnly)
	a.routeFunc(mux, "POST /api/agent-control/resume", a.agentControlWebOnly)
	a.routeFunc(mux, "POST /api/agent-control/{id}/events", a.recordAgentControlEvent)
	a.routeFunc(mux, "POST /api/coord/deliveries", a.markCoordDelivery)
	a.routeFunc(mux, "POST /api/coord/heartbeat", a.coordHeartbeat)
	a.routeFunc(mux, "POST /api/coord/deliveries/claim", a.claimCoordDelivery)
	a.routeFunc(mux, "GET /api/coord/deliveries/injected", a.coordInjectedMessages)
	a.routeFunc(mux, "POST /api/coord/rooms", a.ensureCoordRoom)
	a.routeFunc(mux, "GET /api/coord/rooms", a.coordRooms)
	a.routeFunc(mux, "POST /api/coord/groups", a.createCoordGroup)
	a.routeFunc(mux, "POST /api/coord/cursor", a.coordCursorSet)
	a.routeFunc(mux, "GET /api/coord/cursor", a.coordCursorGet)
	a.routeFunc(mux, "POST /api/threads", a.createThread)
	a.routeFunc(mux, "POST /api/threads/from-message", a.createTaskThreadFromMessage)
	a.routeFunc(mux, "GET /api/threads/home", a.listRoomThreads)
	a.routeFunc(mux, "GET /api/threads", a.listThreads)
	a.routeFunc(mux, "GET /api/threads/for", a.threadsForObject)
	a.routeFunc(mux, "GET /api/threads/{id}", a.getThread)
	a.routeFunc(mux, "GET /api/threads/{id}/home", a.getThreadHome)
	a.routeFunc(mux, "POST /api/threads/{id}/state", a.setThreadState)
	a.routeFunc(mux, "POST /api/threads/{id}/touch", a.touchThread)
	a.routeFunc(mux, "POST /api/threads/{id}/links", a.linkThread)
	a.routeFunc(mux, "GET /api/threads/{id}/links", a.threadLinks)
	a.routeFunc(mux, "POST /api/threads/{id}/summary", a.putThreadSummary)
	a.routeFunc(mux, "GET /api/threads/{id}/summary", a.getThreadSummary)
	a.routeFunc(mux, "POST /api/threads/{id}/outcomes", a.putThreadOutcome)
	a.routeFunc(mux, "GET /api/threads/{id}/outcomes", a.threadOutcomes)
	a.routeFunc(mux, "POST /api/documents", a.createDocument)
	a.routeFunc(mux, "GET /api/documents", a.listDocuments)
	a.routeFunc(mux, "GET /api/documents/{id}", a.getDocument)
	a.routeFunc(mux, "PATCH /api/documents/{id}", a.patchDocument)
	a.routeFunc(mux, "PUT /api/documents/{id}/revisions", a.pushDocumentRevision)
	a.routeFunc(mux, "GET /api/documents/{id}/revisions", a.documentRevisions)
	a.routeFunc(mux, "GET /api/documents/{id}/revisions/{rev}", a.documentRevision)
}

func newAPI(st *store.Store, options ...Option) *api {
	a := &api{
		st: st, snapshotLimits: snapshot.DefaultLimits(),
		operationIDGenerator: randomOperationID,
		snapshotErrorLogger: func(operationID string, err, generatorErr error) {
			if generatorErr != nil {
				log.Printf("snapshot_internal_error operation_id=%q error=%q operation_id_error=%q", operationID, err, generatorErr)
				return
			}
			log.Printf("snapshot_internal_error operation_id=%q error=%q", operationID, err)
		},
	}
	for _, option := range options {
		if option != nil {
			option(a)
		}
	}
	if a.operationIDGenerator == nil {
		a.operationIDGenerator = randomOperationID
	}
	if a.snapshotErrorLogger == nil {
		a.snapshotErrorLogger = func(operationID string, err, generatorErr error) {
			log.Printf("snapshot_internal_error operation_id=%q error=%q operation_id_error=%q", operationID, err, generatorErr)
		}
	}
	if a.logger == nil {
		a.logger = discardLogger()
	}
	if a.buildVersion == "" {
		a.buildVersion = "unknown"
	}
	if a.requestIDGenerator == nil {
		a.requestIDGenerator = randomOperationID
	}
	a.metrics = newMetricsRegistry(st, a.buildVersion)
	return a
}

func randomOperationID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

var fallbackOperationIDCounter atomic.Uint64

func fallbackOperationID() string {
	return fmt.Sprintf("fallback-%x-%x", time.Now().UnixNano(), fallbackOperationIDCounter.Add(1))
}

func (a *api) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" || r.URL.Path == "/metrics" || (r.Method == http.MethodPost && isDevicePath(r.URL.Path)) {
			next.ServeHTTP(w, r)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		principal, ok := a.st.AuthenticatePrincipal(strings.TrimSpace(token))
		if !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if record := requestRecordFromContext(r.Context()); record != nil {
			record.actor = principal.Label
		}
		ctx := context.WithValue(r.Context(), personKey{}, principal)
		next.ServeHTTP(w, r.WithContext(withAccessHolder(ctx, a.st, principal)))
	})
}

// whoAmIResponse ist der Principal plus die Kontodaten. Die Principal-Felder
// bleiben flach, damit Clients, die nur store.Principal lesen, weiterlaufen.
type whoAmIResponse struct {
	store.Principal
	Email string `json:"email,omitempty"`
	Admin bool   `json:"admin"`
	State string `json:"state,omitempty"`
}

func (a *api) whoAmI(r *http.Request) whoAmIResponse {
	p := principalOf(r)
	out := whoAmIResponse{Principal: p}
	if acct, err := a.st.AccountByPrincipalID(p.ID); err == nil {
		out.Email, out.Admin, out.State = acct.Email, acct.Admin, acct.State
	}
	return out
}

func personOf(r *http.Request) string {
	return principalOf(r).Label
}

func principalOf(r *http.Request) store.Principal {
	p, _ := r.Context().Value(personKey{}).(store.Principal)
	return p
}

func axesFromQuery(r *http.Request) scope.Axes {
	q := r.URL.Query()
	return scope.CanonicalAxes(scope.Axes{
		Project:   q.Get("project"),
		Branch:    q.Get("branch"),
		Machine:   q.Get("machine"),
		Lineage:   q["lineage"],
		AnyBranch: q.Get("any_branch") == "1",
	})
}

func intParam(r *http.Request, name string, def int) int {
	if v := r.URL.Query().Get(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

// sessionPathID liest die Session aus dem Pfad: die laufende Nummer (nur
// Mitglieder und ältere Collector von Mitgliedern) oder die zufällige Adresse (Gäste kennen nur diese). Eine
// unbekannte Adresse liefert eine Nummer, die es nicht gibt, und verhält sich
// damit wie jede andere unbekannte Session.
func (a *api) sessionPathID(r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil && len(raw) < 12 {
		// Die Nummer gilt nur für den, der sie kennen darf. Für einen Gast ist
		// jede Nummer unbekannt, auch die einer Session, die er lesen darf:
		// sonst zählte er sie durch (#2447).
		// Ausnahme: die eigene Session, deren Nummer der Server beim Anlegen
		// genannt hat (GetsOwnSessionNumber); sonst liefe ein älterer Collector
		// von Mitgliedern in Projekten ohne Rolle in 403 session_owned.
		if sess, err := a.st.SessionByID(n); err == nil {
			pa := a.access(r)
			if !pa.SeesSessionNumbers(sess.Scope.Project) && !(pa.OwnsSession(sess) && pa.GetsOwnSessionNumber(sess.Scope.Project)) {
				return -1, true
			}
		}
		return n, true
	}
	if raw == "" || len(raw) > 64 {
		return 0, false
	}
	sess, err := a.st.SessionByPublicID(raw)
	if err != nil {
		return -1, true
	}
	return sess.ID, true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	recordResponseError(w, classifyRequestError(code, "", msg), msg)
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}
