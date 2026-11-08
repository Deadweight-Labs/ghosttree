package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func init() {
	accessRoutes["GET /api/inbox"] = classProject
	accessRoutes["POST /api/inbox/answer"] = classAdmin
}

func (a *api) registerInboxRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "GET /api/inbox", a.listInbox)
	a.routeFunc(mux, "POST /api/inbox/answer", a.inboxAnswerWebOnly)
}

// inboxAnswerWebOnly: eine Frage an einen Menschen beantwortet nur ein Mensch in
// einer Web-Sitzung (ProjectAccess.QuickAnswer). Ein Token liegt in der Konfiguration
// jeder Maschine, auf der ein Agent läuft; wer damit antworten könnte, bräuchte keinen
// Menschen, auch der Agent nicht, der die Frage selbst gestellt hat: jeder Token
// bekommt 403, bevor der Rumpf gelesen wird. Der Browser antwortet über POST
// /ui/inbox/answer der Weboberfläche (mit CSRF); hier kommt nur ein Principal der
// Web-Sitzung durch, den die Weboberfläche selbst einsetzt.
func (a *api) inboxAnswerWebOnly(w http.ResponseWriter, r *http.Request) {
	if principalOf(r).TokenKind != store.WebSessionKind {
		writeCoded(w, http.StatusForbidden, "web_session_required",
			"answering a question to people requires an interactive web session: pick the answer in the inbox of the web UI")
		return
	}
	var in struct {
		Kind            string `json:"kind"`
		ID              int64  `json:"id"`
		Option          string `json:"option"`
		AgentExternalID string `json:"agent_external_id"`
	}
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	res, err := a.access(r).QuickAnswer(a.coordAccess(r, in.AgentExternalID), store.QuickAnswerInput{Kind: in.Kind, ID: in.ID, Option: in.Option})
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, res)
	case errors.Is(err, store.ErrCoordNotFound), errors.Is(err, store.ErrCoordForbidden):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrAccessForbidden), errors.Is(err, store.ErrAccessNotFound), errors.Is(err, sql.ErrNoRows):
		writeErr(w, http.StatusNotFound, "not found")
	default:
		writeRequestError(w, err)
	}
}

// inboxReply ist ein Eintrag des Posteingangs mit seinem Link.
type inboxReply struct {
	store.InboxEntry
	Link string `json:"link,omitempty"`
}

// listInbox liefert den Posteingang des Aufrufers: offene und erledigte Einträge
// aus Aufgaben, Entscheidungen und (mit agent_external_id) Fragen an ihn. state
// ist open (Standard), done oder all; die erledigten reichen days Tage zurück
// (Standard 30). project und mine schränken ein.
func (a *api) listInbox(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.InboxFilter{State: q.Get("state"), Project: axesFromQuery(r).Project, Mine: q.Get("mine") == "1",
		Days: intParam(r, "days", 0), Limit: intParam(r, "limit", 0)}
	pa := a.access(r)
	pa.Filtered()
	var ca store.CoordAccess
	if agent := q.Get("agent_external_id"); agent != "" {
		ca = a.coordAccess(r, agent)
	}
	list, err := pa.Inbox(ca, f)
	if err != nil {
		writeCoded(w, http.StatusBadRequest, "invalid_inbox", err.Error())
		return
	}
	b := links.New(a.requestBaseURL(r), "")
	out := make([]inboxReply, 0, len(list))
	for _, e := range list {
		out = append(out, inboxReply{InboxEntry: e, Link: inboxLink(b, e)})
	}
	writeJSON(w, http.StatusOK, out)
}

// inboxLink ist die Adresse, zu der ein Eintrag führt: die Aufgabe, der Issue
// oder die Idee der Entscheidung, bei einer Frage der Raum.
func inboxLink(b links.Builder, e store.InboxEntry) string {
	pb := b.WithProject(e.Project)
	switch e.Kind {
	case store.InboxTask:
		return pb.Task(e.ID)
	case store.InboxDecision:
		return pb.Decision(e.ID)
	case store.InboxAttention:
		if e.DestinationKind == store.DestinationDiscussion && e.DestinationID != "" {
			if n, err := strconv.ParseInt(e.DestinationID, 10, 64); err == nil {
				if u := pb.Room(n, e.MessageID); u != "" {
					return u
				}
			}
		}
		return b.RoomChat(e.HomeRoomKey)
	}
	return ""
}
