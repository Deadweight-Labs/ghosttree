package server

import (
	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"net/http"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func init() {
	for _, route := range []string{"PUT /api/reactions", "DELETE /api/reactions", "GET /api/reactions", "GET /api/comments/reactions"} {
		accessRoutes[route] = classProject
	}
}

func (a *api) registerReactionRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "PUT /api/reactions", a.putReaction)
	a.routeFunc(mux, "DELETE /api/reactions", a.deleteReaction)
	a.routeFunc(mux, "GET /api/reactions", a.getReactions)
	a.routeFunc(mux, "GET /api/comments/reactions", a.commentReactions)
}

type reactionBody struct {
	Kind    string `json:"kind"`
	ID      int64  `json:"id"`
	Content string `json:"content"`
}

// putReaction setzt die Reaktion des Aufrufers an einem Kommentar, einer Idee oder
// einem Issue (kind comment, idea, request; content einer der Schlüssel der festen
// Liste oder sein Zeichen). Setzt er sie zum zweiten Mal, ändert sich nichts.
func (a *api) putReaction(w http.ResponseWriter, r *http.Request) {
	var body reactionBody
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	s, err := a.access(r).React(commentKind(body.Kind), body.ID, body.Content)
	if err != nil {
		writePainError(w, err)
		return
	}
	a.stampActor(r, actor.Reaction, a.st.ReactionRowID(commentKind(body.Kind), body.ID, body.Content, authorIDOf(r)), 0)
	writeJSON(w, http.StatusOK, s)
}

// deleteReaction nimmt die Reaktion des Aufrufers zurück (kind, id und content als
// Anfrageparameter). Hat er sie nicht gesetzt, ändert sich nichts.
func (a *api) deleteReaction(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id, ok := idQuery(q.Get("id"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad object id")
		return
	}
	s, err := a.access(r).Unreact(commentKind(q.Get("kind")), id, q.Get("content"))
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// reactionReply hängt der Auswahl die feste Liste an, damit ein Client sie nicht
// nachbauen muss.
type reactionReply struct {
	store.ReactionSummary
	Choices []store.ReactionKind `json:"choices"`
}

func (a *api) getReactions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id, ok := idQuery(q.Get("id"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad object id")
		return
	}
	s, err := a.access(r).Reactions(commentKind(q.Get("kind")), id)
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, reactionReply{ReactionSummary: s, Choices: store.ReactionSet})
}

// commentReactions liefert die Reaktionen aller Kommentare eines Objekts (kind
// request, idea oder pain, id) nach Kommentar-Nummer.
func (a *api) commentReactions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id, ok := idQuery(q.Get("id"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad object id")
		return
	}
	m, err := a.access(r).CommentReactions(commentKind(q.Get("kind")), id)
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reactions": m, "choices": store.ReactionSet})
}
