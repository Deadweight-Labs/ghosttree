// Package server exposes the ghosttree store over a small REST API.
// Deployments provide the network perimeter; bearer tokens carry provenance.
package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type SnapshotMirror interface {
	Rebuild(context.Context, string) error
}

type Option func(*api)

func WithContextSnapshotLimits(limits snapshot.Limits) Option {
	return func(a *api) { a.snapshotLimits = limits }
}

func WithSnapshotMirror(mirror SnapshotMirror) Option {
	return func(a *api) { a.snapshotMirror = mirror }
}

type api struct {
	st             *store.Store
	snapshotLimits snapshot.Limits
	snapshotMirror SnapshotMirror
}

type personKey struct{}

func New(st *store.Store, options ...Option) http.Handler {
	a := newAPI(st, options...)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/whoami", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, principalOf(r))
	})
	mux.HandleFunc("POST /api/context-snapshots", a.createContextSnapshot)
	mux.HandleFunc("GET /api/context-snapshots", a.listContextSnapshots)
	mux.HandleFunc("GET /api/context-snapshots/{name}", a.getContextSnapshot)
	mux.HandleFunc("GET /api/context-snapshots/{name}/entries", a.contextSnapshotEntries)
	mux.HandleFunc("POST /api/sessions", a.createSession)
	mux.HandleFunc("GET /api/sessions", a.listSessions)
	mux.HandleFunc("POST /api/sessions/{id}/chunks", a.appendChunks)
	mux.HandleFunc("GET /api/sessions/{id}/raw", a.rawSession)
	mux.HandleFunc("GET /api/sessions/{id}", a.readSession)
	mux.HandleFunc("POST /api/requests", a.createRequest)
	mux.HandleFunc("GET /api/requests", a.searchRequests)
	mux.HandleFunc("GET /api/requests/search", a.searchRequests)
	mux.HandleFunc("GET /api/requests/{id}", a.getRequest)
	mux.HandleFunc("POST /api/requests/{id}/work", a.startRequestWork)
	mux.HandleFunc("PATCH /api/request-work/{id}", a.finishRequestWork)
	mux.HandleFunc("POST /api/requests/{id}/criteria", a.addRequestCriterion)
	mux.HandleFunc("PATCH /api/criteria/{id}", a.setRequestCriterion)
	mux.HandleFunc("POST /api/requests/{id}/complete", a.completeRequest)
	mux.HandleFunc("POST /api/requests/{id}/drop", a.dropRequest)
	mux.HandleFunc("POST /api/requests/{id}/relations", a.addRequestRelation)
	mux.HandleFunc("PATCH /api/requests/{id}", a.correctRequest)
	mux.HandleFunc("DELETE /api/request-relations/{id}", a.removeRequestRelation)
	mux.HandleFunc("POST /api/knowledge", a.createKnowledge)
	mux.HandleFunc("GET /api/knowledge", a.listKnowledge)
	mux.HandleFunc("GET /api/knowledge/pending", a.pendingKnowledge)
	mux.HandleFunc("GET /api/knowledge/{id}", a.getKnowledge)
	mux.HandleFunc("GET /api/knowledge/{id}/history", a.knowledgeHistory)
	mux.HandleFunc("PATCH /api/knowledge/{id}", a.patchKnowledge)
	mux.HandleFunc("PUT /api/knowledge/{id}/regression", a.setRegressionCover)
	mux.HandleFunc("GET /api/knowledge/regression-gaps", a.regressionGaps)
	mux.HandleFunc("POST /api/migrated-knowledge", a.insertMigratedKnowledge)
	mux.HandleFunc("GET /api/migrations", a.completedMigrationArtifacts)
	mux.HandleFunc("GET /api/migrations/documents", a.completedDocumentArtifacts)
	mux.HandleFunc("POST /api/migrations", a.beginMigration)
	mux.HandleFunc("PUT /api/migrations/{id}/complete", a.completeMigration)
	mux.HandleFunc("POST /api/migrations/{id}/documents", a.insertDocumentMigration)
	mux.HandleFunc("POST /api/migrations/{id}/documents/import", a.importDocumentMigration)
	mux.HandleFunc("GET /api/search", a.search)
	mux.HandleFunc("GET /api/context/bootstrap", a.bootstrap)
	mux.HandleFunc("GET /api/context/interrupted", a.interrupted)
	mux.HandleFunc("GET /api/context/relevant", a.relevant)
	mux.HandleFunc("POST /api/ghosts", a.putGhost)
	mux.HandleFunc("GET /api/ghosts", a.ghostsForPath)
	mux.HandleFunc("GET /api/ghosts/tree", a.ghostTree)
	mux.HandleFunc("GET /api/ghosts/history", a.ghostHistory)
	mux.HandleFunc("POST /api/ghosts/move", a.ghostsMove)
	mux.HandleFunc("GET /api/ghosts/search", a.searchGhosts)
	mux.HandleFunc("POST /api/ghosts/reviews", a.putGhostReview)
	mux.HandleFunc("GET /api/ghosts/reviews", a.ghostReviews)
	mux.HandleFunc("POST /api/documents", a.createDocument)
	mux.HandleFunc("GET /api/documents", a.listDocuments)
	mux.HandleFunc("GET /api/documents/{id}", a.getDocument)
	mux.HandleFunc("PATCH /api/documents/{id}", a.patchDocument)
	mux.HandleFunc("PUT /api/documents/{id}/revisions", a.pushDocumentRevision)
	mux.HandleFunc("GET /api/documents/{id}/revisions", a.documentRevisions)
	mux.HandleFunc("GET /api/documents/{id}/revisions/{rev}", a.documentRevision)
	return a.auth(mux)
}

func newAPI(st *store.Store, options ...Option) *api {
	a := &api{st: st, snapshotLimits: snapshot.DefaultLimits()}
	for _, option := range options {
		if option != nil {
			option(a)
		}
	}
	return a
}

// auth lets /api/health through unauthenticated so probes and setup checks
// work before a token exists.
func (a *api) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			next.ServeHTTP(w, r)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		principal, ok := a.st.AuthenticatePrincipal(strings.TrimSpace(token))
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), personKey{}, principal)))
	})
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

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}
