package server

import (
	"net/http"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// actorOf says who writes with this request (REQ-620). A person is only a
// caller from an interactive web session (the same rule as the idea workshop,
// store.IdeaActorFor); every token call is an agent. For an agent the session
// comes from the client's session header or a numeric session id in the body,
// and counts only when it belongs to the calling account; harness and machine
// come from that session, never from the body. Without a session the machine
// of a device token stays, the harness is unknown, and a session id the
// server does not know yet is kept to be resolved once it is uploaded.
func (a *api) actorOf(r *http.Request, bodySession int64) actor.Actor {
	p := principalOf(r)
	act := actor.Actor{AccountID: store.AccountIDOf(p.ID), Person: p.Label}
	if p.TokenKind == store.WebSessionKind {
		act.Kind = actor.Human
		return act
	}
	act.Kind = actor.Agent
	act.Machine = strings.TrimSpace(p.Machine)
	if sess, ok := a.callerSessionRow(r, bodySession); ok {
		act.SessionID, act.Harness = sess.ID, sess.Harness
		if m := strings.TrimSpace(sess.Scope.Machine); m != "" {
			act.Machine = m
		}
		return act
	}
	if ref := sessionHeader(r); ref != "" && !a.st.SessionForeignTo(ref, p.ID) {
		act.SessionRef = ref
	}
	return act
}

// sessionHeader is the session the client names in originctx.SessionHeader,
// empty when absent or malformed.
func sessionHeader(r *http.Request) string {
	v := strings.TrimSpace(r.Header.Get(originctx.SessionHeader))
	if !originctx.ValidSession(v) {
		return ""
	}
	return v
}

// callerSessionRow is the caller's own session: the numeric id from the body
// if it is theirs, else the one named in the session header.
func (a *api) callerSessionRow(r *http.Request, bodySession int64) (store.Session, bool) {
	if bodySession > 0 {
		if sess, err := a.st.SessionByID(bodySession); err == nil && a.access(r).OwnsSession(sess) {
			return sess, true
		}
	}
	if ref := sessionHeader(r); ref != "" {
		if sess, ok := a.st.OwnSessionByExternal(ref, authorIDOf(r)); ok {
			return sess, true
		}
	}
	return store.Session{}, false
}

// stampActor records who wrote an entry, after the entry is stored. A failure
// is logged and leaves the entry without actor (shown as unknown); the write
// itself has already succeeded and is not undone for it.
func (a *api) stampActor(r *http.Request, kind string, id int64, bodySession int64) {
	if id <= 0 {
		return
	}
	if err := a.st.RecordActor(kind, id, a.actorOf(r, bodySession)); err != nil && a.logger != nil {
		a.logger.Warn("actor not recorded", "kind", kind, "id", id, "err", err)
	}
}

func init() {
	accessRoutes["GET /api/stats/actors"] = classProject
}

func (a *api) registerActorRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "GET /api/stats/actors", a.actorStats)
}

// actorStats answers GET /api/stats/actors[?project=]: per harness and model,
// how many entries agents created, criteria they met and requests and features
// they finished, over the projects the caller may see in full (REQ-620).
func (a *api) actorStats(w http.ResponseWriter, r *http.Request) {
	project := scope.CanonicalAxes(scope.Axes{Project: r.URL.Query().Get("project")}).Project
	stats, projects, err := a.access(r).ActorStats(project)
	if denyAccess(w, err) {
		return
	}
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	if projects == nil {
		projects = []string{}
	}
	if stats == nil {
		stats = []store.ActorStat{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects, "stats": stats})
}
