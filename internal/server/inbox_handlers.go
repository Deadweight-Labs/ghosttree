package server

import (
	"net/http"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func init() { accessRoutes["GET /api/inbox"] = classProject }

func (a *api) registerInboxRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "GET /api/inbox", a.listInbox)
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
