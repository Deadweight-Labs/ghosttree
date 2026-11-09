package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// inboxReply answers a message from the inbox: it posts the text to the room the
// message lives in (as a reply to it) and, when the row stood for an open
// question, closes that question as answered. The viewer is the signed-in
// person, never a form field.
func (a *app) inboxReply(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	room, body := strings.TrimSpace(r.FormValue("room")), strings.TrimSpace(r.FormValue("body"))
	if room == "" || body == "" {
		http.Error(w, "room and body are required", http.StatusBadRequest)
		return
	}
	if !a.mayEnter(w, r, room) {
		return
	}
	replyTo, err := coordOptionalReplyTo(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	access := a.browserCoord(r)
	_, err = access.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: newFormClientID(r), Body: body, ReplyTo: replyTo})
	if err != nil {
		coordHTTPError(l, w, err)
		return
	}
	// Answering is reading: the conversation falls out of the inbox. A failed
	// read mark only costs that; the answer is stored.
	if summaries, err := access.RoomSummaries(); err == nil {
		for _, s := range summaries {
			if s.Room.Key == room {
				_ = access.MarkRead(store.DestinationRoom, room, s.LastSequence)
			}
		}
	}
	if id, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("attention_id")), 10, 64); err == nil && id > 0 {
		if err := access.ResolveAttention(id, store.AttentionActionAnswer); err != nil {
			coordHTTPError(l, w, err)
			return
		}
	}
	http.Redirect(w, r, "/ui/inbox", http.StatusSeeOther)
}

// inboxDone closes an open question, approval or blocker as seen, without an
// answer. Only its recipient can; the store checks that.
func (a *app) inboxDone(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("attention_id")), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "attention item is required", http.StatusBadRequest)
		return
	}
	if err := a.browserCoord(r).ResolveAttention(id, store.AttentionActionDismiss); err != nil {
		coordHTTPError(l, w, err)
		return
	}
	http.Redirect(w, r, "/ui/inbox", http.StatusSeeOther)
}
