// Package server exposes the ghosttree store over a small REST API.
// The perimeter is the private network; bearer tokens only carry provenance.
package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type api struct{ st *store.Store }

type personKey struct{}

func New(st *store.Store) http.Handler {
	a := &api{st: st}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
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
	mux.HandleFunc("PATCH /api/knowledge/{id}", a.patchKnowledge)
	mux.HandleFunc("POST /api/migrated-knowledge", a.insertMigratedKnowledge)
	mux.HandleFunc("GET /api/migrations", a.completedMigrationArtifacts)
	mux.HandleFunc("POST /api/migrations", a.beginMigration)
	mux.HandleFunc("PUT /api/migrations/{id}/complete", a.completeMigration)
	mux.HandleFunc("GET /api/search", a.search)
	mux.HandleFunc("GET /api/context/bootstrap", a.bootstrap)
	mux.HandleFunc("GET /api/context/relevant", a.relevant)
	mux.HandleFunc("POST /api/ghosts", a.putGhost)
	mux.HandleFunc("GET /api/ghosts", a.ghostsForPath)
	mux.HandleFunc("GET /api/ghosts/tree", a.ghostTree)
	mux.HandleFunc("GET /api/ghosts/history", a.ghostHistory)
	mux.HandleFunc("POST /api/ghosts/move", a.ghostsMove)
	mux.HandleFunc("GET /api/ghosts/search", a.searchGhosts)
	return a.auth(mux)
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
		person, ok := a.st.Authenticate(strings.TrimSpace(token))
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), personKey{}, person)))
	})
}

func personOf(r *http.Request) string {
	p, _ := r.Context().Value(personKey{}).(string)
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
