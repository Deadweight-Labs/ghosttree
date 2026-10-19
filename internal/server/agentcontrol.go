package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Pause und Unterbrechung eines Agenten (REQ-361, AC-1229). Ausloesen und
// Aufheben sind Verwaltung und laufen nur in der Weboberflaeche; die API liest
// den Vorgang und nimmt die Belege des Agentenkontos an.

func (a *api) agentControlWebOnly(w http.ResponseWriter, r *http.Request) {
	webSessionOnly(w, "pausing, interrupting and resuming an agent")
}

// getAgentControl liefert dem Channel den aktiven Vorgang seines Agenten. Fremde
// Konten und unbekannte Agenten bekommen dieselbe 404.
func (a *api) getAgentControl(w http.ResponseWriter, r *http.Request) {
	agent := strings.TrimSpace(r.URL.Query().Get("agent"))
	if agent == "" {
		writeErr(w, http.StatusBadRequest, "agent is required")
		return
	}
	ok, err := a.mayActAsRegistered(r, agent)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	c, found, err := a.st.ActiveAgentControl(agent)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		// Ohne aktiven Vorgang nennt die Antwort den jüngsten, wenn er
		// aufgehoben wurde. Daran erkennt der Channel, wer die Pause beendet
		// hat, und kann der Session sagen, dass sie weiterarbeiten darf.
		out := map[string]any{"control": nil}
		if last, ok, err := a.st.LatestAgentControl(agent); err != nil {
			writeStoreError(w, http.StatusInternalServerError, err)
			return
		} else if ok && last.ResumedAt != "" {
			out["resumed"] = last
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"control": c})
}

// recordAgentControlEvent nimmt Hook-Acks und Transkript-Belege an. Ein
// unbekannter Vorgang oder ein fremdes Konto ergibt recorded=false, damit der
// Collector nicht an einem verwaisten Beleg haengen bleibt.
func (a *api) recordAgentControlEvent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid control id")
		return
	}
	var in store.ControlEvent
	if err := readJSON(r, &in); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	recorded, err := a.st.RecordControlEvent(principalOf(r).ID, id, in)
	switch {
	case errors.Is(err, store.ErrControlNotFound), errors.Is(err, store.ErrControlForbidden):
		writeJSON(w, http.StatusOK, map[string]bool{"recorded": false})
	case errors.Is(err, store.ErrInvalidInput):
		writeErr(w, http.StatusBadRequest, err.Error())
	case err != nil:
		writeStoreError(w, http.StatusInternalServerError, err)
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"recorded": recorded})
	}
}
