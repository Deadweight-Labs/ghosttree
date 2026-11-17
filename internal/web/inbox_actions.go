package web

import (
	"errors"
	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"net/http"
	"net/url"
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
	message := store.CoordMessage{ClientID: newFormClientID(r), Body: body, ReplyTo: replyTo}
	var mid int64
	// A mention in a thread is answered in that thread, and reading it is reading the thread.
	thread, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("thread")), 10, 64)
	// The answer to a message somebody started a thread on is a post in that thread:
	// where the asker follows the question, not a loose line in the room (REQ-689).
	answeredInThread := false
	if thread == 0 && replyTo > 0 {
		if thread = threadAnchoredAt(access, room, replyTo); thread > 0 {
			answeredInThread = true
			// The question stands in the room, the answer in the thread: nothing to quote there.
			message.ReplyTo = 0
		}
	}
	if thread > 0 {
		mid, err = access.ThreadPost(thread, message)
	} else {
		message.DestinationKind, message.DestinationID = store.DestinationRoom, room
		mid, err = access.Send(message)
	}
	if err != nil {
		coordHTTPError(l, w, err)
		return
	}
	a.stampActor(r, actor.Message, mid)
	// Answering is reading: the conversation falls out of the inbox. A failed
	// read mark only costs that; the answer is stored.
	if thread > 0 {
		if list, err := access.UnreadThreadMentions(room); err == nil {
			var through int64
			for _, tm := range list {
				if tm.ThreadID == thread {
					through = max(through, tm.Sequence)
				}
			}
			if through > 0 {
				_ = access.MarkRead(store.DestinationDiscussion, store.ThreadDestinationID(thread), through)
			}
		}
	}
	if summaries, err := access.RoomSummaries(); err == nil {
		for _, s := range summaries {
			if s.Room.Key == room && (thread == 0 || answeredInThread) {
				_ = access.MarkRead(store.DestinationRoom, room, s.LastSequence)
			}
		}
	}
	if id, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("attention_id")), 10, 64); err == nil && id > 0 {
		// A reply to the question has closed it already; that is not a failure.
		if err := access.ResolveAttention(id, store.AttentionActionAnswer); err != nil && !errors.Is(err, store.ErrAttentionClosed) {
			coordHTTPError(l, w, err)
			return
		}
	}
	// The answer is confirmed on the inbox: to whom it went, in one line.
	back := "/ui/inbox?sent=1"
	if to := strings.TrimSpace(r.FormValue("to")); to != "" {
		back = "/ui/inbox?sent=" + url.QueryEscape(cleanLineWeb(to, 80))
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// cleanLineWeb is a name from a form cut to one line of at most max characters.
func cleanLineWeb(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
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

// threadAnchoredAt is the thread of a room that was started from the message, 0 when there is none
// or the viewer may not see the room's threads.
func threadAnchoredAt(access store.CoordAccess, room string, messageID int64) int64 {
	threads, err := access.RoomThreads(room)
	if err != nil {
		return 0
	}
	for _, t := range threads {
		if t.Home.AnchorMessageID == messageID {
			return t.Thread.ID
		}
	}
	return 0
}
