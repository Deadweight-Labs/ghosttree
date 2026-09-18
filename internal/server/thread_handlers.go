package server

import (
	"net/http"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func threadIDFrom(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (a *api) threadAccess(w http.ResponseWriter, r *http.Request) (store.CoordAccess, bool) {
	if r.URL.Query().Get("agent_external_id") != "" && r.URL.Query().Get("public_only") == "1" {
		writeErr(w, http.StatusBadRequest, "choose agent_external_id or public_only")
		return store.CoordAccess{}, false
	}
	if agent := r.URL.Query().Get("agent_external_id"); agent != "" {
		return a.coordAccess(r, agent), true
	}
	if r.URL.Query().Get("public_only") == "1" {
		return a.st.CoordinationPublicFor(principalOf(r)), true
	}
	writeErr(w, http.StatusBadRequest, "agent_external_id is required")
	return store.CoordAccess{}, false
}

func (a *api) createThread(w http.ResponseWriter, r *http.Request) {
	access, ok := a.threadAccess(w, r)
	if !ok {
		return
	}
	var in store.Thread
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if in.Project == "" {
		writeErr(w, http.StatusBadRequest, "project is required")
		return
	}
	id, err := access.CreateThread(in)
	if err != nil {
		if err == store.ErrCoordForbidden || err == store.ErrCoordNotFound {
			writeCoordAccessError(w, err)
			return
		}
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, 200, map[string]int64{"id": id})
}

type taskThreadInput struct {
	AnchorMessageID int64  `json:"anchor_message_id"`
	RoomKey         string `json:"room_key,omitempty"`
	Title           string `json:"title"`
	Question        string `json:"question,omitempty"`
	RequestID       string `json:"request_id,omitempty"`
}

func (a *api) createTaskThreadFromMessage(w http.ResponseWriter, r *http.Request) {
	access, ok := a.threadAccess(w, r)
	if !ok {
		return
	}
	var in taskThreadInput
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	var id int64
	var err error
	if in.AnchorMessageID < 0 || (in.AnchorMessageID > 0 && in.RoomKey != "") {
		writeErr(w, http.StatusBadRequest, "choose an anchor message or a home room")
		return
	}
	if in.AnchorMessageID > 0 {
		id, err = access.PromoteRoomMessageToTaskThread(in.AnchorMessageID, in.Title, in.Question, in.RequestID)
	} else {
		id, err = access.CreateTaskThreadInRoom(in.RoomKey, in.Title, in.Question, in.RequestID)
	}
	if err != nil {
		if err == store.ErrCoordForbidden || err == store.ErrCoordNotFound {
			writeCoordAccessError(w, err)
			return
		}
		if err == store.ErrAnchorAlreadyThreaded {
			writeErr(w, http.StatusConflict, "coordination message already has a different task thread")
			return
		}
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (a *api) listRoomThreads(w http.ResponseWriter, r *http.Request) {
	access, ok := a.threadAccess(w, r)
	if !ok {
		return
	}
	roomKey := r.URL.Query().Get("room_key")
	if roomKey == "" {
		writeErr(w, http.StatusBadRequest, "room_key is required")
		return
	}
	threads, err := access.RoomThreads(roomKey)
	if err != nil {
		writeCoordAccessError(w, err)
		return
	}
	if threads == nil {
		threads = []store.RoomThread{}
	}
	writeJSON(w, http.StatusOK, threads)
}

func (a *api) getThreadHome(w http.ResponseWriter, r *http.Request) {
	access, ok := a.threadAccess(w, r)
	if !ok {
		return
	}
	id, ok := threadIDFrom(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "thread id is required")
		return
	}
	home, err := access.ThreadHome(id)
	if err != nil {
		writeCoordAccessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, home)
}

func (a *api) listThreads(w http.ResponseWriter, r *http.Request) {
	access, ok := a.threadAccess(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	if q.Get("project") == "" {
		writeErr(w, http.StatusBadRequest, "project is required")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	out, err := access.SearchThreads(q.Get("project"), q.Get("q"), q.Get("archived") == "1", limit)
	if err != nil {
		writeCoordAccessError(w, err)
		return
	}
	if out == nil {
		out = []store.Thread{}
	}
	writeJSON(w, 200, out)
}

func (a *api) getThread(w http.ResponseWriter, r *http.Request) {
	access, allowed := a.threadAccess(w, r)
	if !allowed {
		return
	}
	id, ok := threadIDFrom(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "thread id is required")
		return
	}
	t, err := access.Thread(id)
	if err != nil {
		writeCoordAccessError(w, err)
		return
	}
	writeJSON(w, 200, t)
}

type threadStateInput struct {
	State    string `json:"state"`
	Archived *bool  `json:"archived,omitempty"`
}

// setThreadState trennt den fachlichen Zustand von der Archivierung. Beides
// im selben Aufruf zu erlauben ist Bequemlichkeit; sie in dasselbe Feld zu
// legen wäre der Fehler, den Spec §B5 benennt — Archivierung sagt nichts
// darüber, ob die Frage beantwortet ist.
func (a *api) setThreadState(w http.ResponseWriter, r *http.Request) {
	access, allowed := a.threadAccess(w, r)
	if !allowed {
		return
	}
	id, ok := threadIDFrom(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "thread id is required")
		return
	}
	var in threadStateInput
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if in.State != "" {
		if err := access.SetThreadState(id, in.State); err != nil {
			if err == store.ErrCoordForbidden || err == store.ErrCoordNotFound {
				writeCoordAccessError(w, err)
				return
			}
			writeStoreError(w, http.StatusBadRequest, err)
			return
		}
	}
	if in.Archived != nil {
		if err := access.SetThreadArchived(id, *in.Archived); err != nil {
			writeCoordAccessError(w, err)
			return
		}
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (a *api) linkThread(w http.ResponseWriter, r *http.Request) {
	access, allowed := a.threadAccess(w, r)
	if !allowed {
		return
	}
	id, ok := threadIDFrom(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "thread id is required")
		return
	}
	var in store.ThreadLink
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	in.ThreadID = id
	if err := access.LinkThread(in); err != nil {
		if err == store.ErrCoordForbidden || err == store.ErrCoordNotFound {
			writeCoordAccessError(w, err)
			return
		}
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (a *api) threadLinks(w http.ResponseWriter, r *http.Request) {
	access, allowed := a.threadAccess(w, r)
	if !allowed {
		return
	}
	id, ok := threadIDFrom(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "thread id is required")
		return
	}
	out, err := access.ThreadLinks(id)
	if err != nil {
		writeCoordAccessError(w, err)
		return
	}
	if out == nil {
		out = []store.ThreadLink{}
	}
	writeJSON(w, 200, out)
}

// threadsForObject beantwortet die Frage aus Sicht eines Pitfalls, Requests
// oder Dokuments: welche Diskussionen hängen hier? Ohne diesen Weg wäre eine
// Verknüpfung nur in einer Richtung nutzbar, und die Knowledge-Ansicht
// könnte nicht "Diskussionen: 2" zeigen.
func (a *api) threadsForObject(w http.ResponseWriter, r *http.Request) {
	access, allowed := a.threadAccess(w, r)
	if !allowed {
		return
	}
	q := r.URL.Query()
	if q.Get("kind") == "" || q.Get("id") == "" {
		writeErr(w, http.StatusBadRequest, "kind and id are required")
		return
	}
	out, err := access.ThreadsForObject(q.Get("kind"), q.Get("id"))
	if err != nil {
		writeCoordAccessError(w, err)
		return
	}
	if out == nil {
		out = []store.Thread{}
	}
	writeJSON(w, 200, out)
}

func (a *api) putThreadSummary(w http.ResponseWriter, r *http.Request) {
	access, allowed := a.threadAccess(w, r)
	if !allowed {
		return
	}
	id, ok := threadIDFrom(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "thread id is required")
		return
	}
	var in store.ThreadSummary
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	in.ThreadID = id
	rev, err := access.PutThreadSummary(in)
	if err != nil {
		writeCoordAccessError(w, err)
		return
	}
	writeJSON(w, 200, map[string]int{"revision": rev})
}

func (a *api) getThreadSummary(w http.ResponseWriter, r *http.Request) {
	access, allowed := a.threadAccess(w, r)
	if !allowed {
		return
	}
	id, ok := threadIDFrom(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "thread id is required")
		return
	}
	sum, found, err := access.ThreadSummary(id)
	if err != nil {
		writeCoordAccessError(w, err)
		return
	}
	if !found {
		// 204 und nicht 404: der Thread existiert, er hat nur noch keine
		// Karte. Ein 404 läse sich, als gäbe es den Thread nicht.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, 200, sum)
}

func (a *api) putThreadOutcome(w http.ResponseWriter, r *http.Request) {
	access, allowed := a.threadAccess(w, r)
	if !allowed {
		return
	}
	id, ok := threadIDFrom(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "thread id is required")
		return
	}
	var in store.ThreadOutcome
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	in.ThreadID = id
	if err := access.PutThreadOutcome(in); err != nil {
		if err == store.ErrCoordForbidden || err == store.ErrCoordNotFound {
			writeCoordAccessError(w, err)
			return
		}
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (a *api) threadOutcomes(w http.ResponseWriter, r *http.Request) {
	access, allowed := a.threadAccess(w, r)
	if !allowed {
		return
	}
	id, ok := threadIDFrom(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "thread id is required")
		return
	}
	out, err := access.ThreadOutcomes(id)
	if err != nil {
		writeCoordAccessError(w, err)
		return
	}
	if out == nil {
		out = []store.ThreadOutcome{}
	}
	writeJSON(w, 200, out)
}

// touchThread schreibt die letzte Aktivität fort, ohne den Zustand zu
// ändern. Eigener Endpunkt, weil ein Beitrag über die Nachrichtenroute
// läuft: der Thread erführe sonst nichts davon und erschiene nach 14 Tagen
// als ruhend, obwohl gerade jemand geschrieben hat.
func (a *api) touchThread(w http.ResponseWriter, r *http.Request) {
	access, allowed := a.threadAccess(w, r)
	if !allowed {
		return
	}
	id, ok := threadIDFrom(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "thread id is required")
		return
	}
	if err := access.TouchThread(id); err != nil {
		writeCoordAccessError(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
