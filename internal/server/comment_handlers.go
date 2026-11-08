package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func init() {
	for _, route := range []string{
		"POST /api/comments", "GET /api/comments", "PATCH /api/comments/{id}", "DELETE /api/comments/{id}", "GET /api/comments/{id}/versions",
	} {
		accessRoutes[route] = classProject
	}
}

func (a *api) registerCommentRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "POST /api/comments", a.addComment)
	a.routeFunc(mux, "GET /api/comments", a.listComments)
	a.routeFunc(mux, "PATCH /api/comments/{id}", a.editComment)
	a.routeFunc(mux, "DELETE /api/comments/{id}", a.deleteComment)
	a.routeFunc(mux, "GET /api/comments/{id}/versions", a.commentVersions)
}

// commentActor ist der Schreiber: Person und Konto aus dem Token, der Anker aus
// dem Header. Der Rumpf ist nie die Quelle dafür.
func (a *api) commentActor(r *http.Request, sessionID int64) store.CommentActor {
	sid := a.callerSession(r, sessionID)
	return store.CommentActor{AccountID: authorIDOf(r), Person: personOf(r), SessionID: sid, GitAnchor: anchorFromRequest(r),
		Origin: a.originFor(r, a.st.SessionExternalByID(sid))}
}

func idQuery(raw string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return n, err == nil && n > 0
}

func commentKind(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// addComment schreibt einen Kommentar an ein Objekt (kind request, idea oder
// pain). Lesen darf, wer das Objekt liest; schreiben ab member.
func (a *api) addComment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind      string `json:"kind"`
		ID        int64  `json:"id"`
		Body      string `json:"body"`
		SessionID int64  `json:"session_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	c, err := a.access(r).AddComment(commentKind(body.Kind), body.ID, body.Body, a.commentActor(r, body.SessionID))
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (a *api) listComments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id, ok := idQuery(q.Get("id"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad object id")
		return
	}
	list, err := a.access(r).Comments(commentKind(q.Get("kind")), id)
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *api) editComment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad comment id")
		return
	}
	var body struct {
		Body      string `json:"body"`
		SessionID int64  `json:"session_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	c, err := a.access(r).EditComment(id, body.Body, a.commentActor(r, body.SessionID))
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (a *api) deleteComment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad comment id")
		return
	}
	c, err := a.access(r).DeleteComment(id, a.commentActor(r, 0))
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (a *api) commentVersions(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad comment id")
		return
	}
	list, err := a.access(r).CommentVersions(id)
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}
