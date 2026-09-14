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

func (a *api) createThread(w http.ResponseWriter, r *http.Request) {
	var in store.Thread
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if in.Project == "" {
		writeErr(w, http.StatusBadRequest, "project is required")
		return
	}
	in.Person = personOf(r)
	id, err := a.st.CreateThread(in)
	if err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, 200, map[string]int64{"id": id})
}

func (a *api) listThreads(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("project") == "" {
		writeErr(w, http.StatusBadRequest, "project is required")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	out, err := a.st.SearchThreads(q.Get("project"), q.Get("q"), q.Get("archived") == "1", limit)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	if out == nil {
		out = []store.Thread{}
	}
	writeJSON(w, 200, out)
}

func (a *api) getThread(w http.ResponseWriter, r *http.Request) {
	id, ok := threadIDFrom(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "thread id is required")
		return
	}
	t, err := a.st.ThreadByID(id)
	if err != nil {
		writeStoreError(w, http.StatusNotFound, err)
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
		if err := a.st.SetThreadState(id, in.State); err != nil {
			writeStoreError(w, http.StatusBadRequest, err)
			return
		}
	}
	if in.Archived != nil {
		if err := a.st.SetThreadArchived(id, *in.Archived); err != nil {
			writeStoreError(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (a *api) linkThread(w http.ResponseWriter, r *http.Request) {
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
	if err := a.st.LinkThread(in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (a *api) threadLinks(w http.ResponseWriter, r *http.Request) {
	id, ok := threadIDFrom(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "thread id is required")
		return
	}
	out, err := a.st.ThreadLinks(id)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
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
	q := r.URL.Query()
	if q.Get("kind") == "" || q.Get("id") == "" {
		writeErr(w, http.StatusBadRequest, "kind and id are required")
		return
	}
	out, err := a.st.ThreadsForObject(q.Get("kind"), q.Get("id"))
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	if out == nil {
		out = []store.Thread{}
	}
	writeJSON(w, 200, out)
}

func (a *api) putThreadSummary(w http.ResponseWriter, r *http.Request) {
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
	in.Person = personOf(r)
	rev, err := a.st.PutThreadSummary(in)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, map[string]int{"revision": rev})
}

func (a *api) getThreadSummary(w http.ResponseWriter, r *http.Request) {
	id, ok := threadIDFrom(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "thread id is required")
		return
	}
	sum, found, err := a.st.LatestThreadSummary(id)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
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
	if err := a.st.PutThreadOutcome(in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (a *api) threadOutcomes(w http.ResponseWriter, r *http.Request) {
	id, ok := threadIDFrom(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "thread id is required")
		return
	}
	out, err := a.st.ThreadOutcomes(id)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
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
	id, ok := threadIDFrom(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "thread id is required")
		return
	}
	if err := a.st.TouchThread(id); err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
