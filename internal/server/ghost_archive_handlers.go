package server

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func (a *api) ghostArchiveCandidate(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := a.st.PrepareGhostArchive(q.Get("project"), q.Get("path"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrGhostArchiveInvalid) {
			status = http.StatusBadRequest
		}
		if err == sql.ErrNoRows {
			status = http.StatusNotFound
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) archiveGhosts(w http.ResponseWriter, r *http.Request) {
	var in store.GhostArchiveInput
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	in.Person = personOf(r)
	out, err := a.st.ArchiveGhostFiles(in)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrGhostArchiveInvalid) {
			status = http.StatusBadRequest
		}
		if errors.Is(err, store.ErrGhostArchiveConflict) {
			status = http.StatusConflict
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
