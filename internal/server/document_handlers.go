package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"unicode/utf8"

	docwork "github.com/Deadweight-Labs/ghosttree/internal/doc"
	"github.com/Deadweight-Labs/ghosttree/internal/redact"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type documentRevisionRequest struct {
	BaseRevision int    `json:"base_revision"`
	Body         string `json:"body"`
	Message      string `json:"message"`
}

type createDocumentRequest struct {
	store.Document
	Body    string `json:"body"`
	Message string `json:"message"`
}

func (a *api) createDocument(w http.ResponseWriter, r *http.Request) {
	var req createDocumentRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	d := req.Document
	d.Project = scope.NormalizeRemote(d.Project)
	if d.Project == "" || d.Slug == "" || d.Kind == "" || d.Title == "" {
		writeErr(w, http.StatusBadRequest, "project, slug, kind and title are required")
		return
	}
	if err := docwork.ValidateSlug(d.Slug); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateDocumentBody(req.Body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	d.Person = personOf(r)
	saved, err := a.st.CreateDocument(d, req.Body, req.Message)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, 200, saved)
}

func (a *api) importDocumentMigration(w http.ResponseWriter, r *http.Request) {
	runID, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad migration id")
		return
	}
	var in store.MigratedDocument
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	in.RunID = runID
	in.Document.Project = scope.NormalizeRemote(in.Document.Project)
	in.Document.Person = personOf(r)
	if in.Document.Project == "" || in.Document.Slug == "" || in.Document.Kind == "" || in.Document.Title == "" || in.Source == "" || in.Digest == "" {
		writeErr(w, http.StatusBadRequest, "project, slug, kind, title, source and digest are required")
		return
	}
	if err := docwork.ValidateSlug(in.Document.Slug); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateDocumentBody(in.Body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := a.st.ImportDocument(in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (a *api) pushDocumentRevision(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	var req documentRevisionRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateDocumentBody(req.Body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := a.st.PushRevision(id, req.BaseRevision, req.Body, req.Message, personOf(r))
	if err == store.ErrRevisionConflict {
		writeDocumentConflict(w, a.st, id)
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, 200, saved)
}

func validateDocumentBody(body string) error {
	if !utf8.ValidString(body) {
		return fmt.Errorf("body is not valid UTF-8")
	}
	if matches := redact.FindSecrets(body); len(matches) > 0 {
		return fmt.Errorf("body contains a possible %s secret on line %d; nothing was stored", matches[0].Label, matches[0].Line)
	}
	return nil
}

// writeDocumentConflict sagt nicht nur, DASS der Kopf weitergezogen ist,
// sondern wer ihn bewegt hat und mit welcher Begründung. Ohne das bleibt dem
// Aufrufer nur, blind zu ziehen — und er weiss hinterher nicht, wessen Arbeit
// er gerade überschrieben hätte.
// Entweder die Antwort trägt den vollständigen Grund, oder sie sagt ehrlich
// nur „Konflikt". Eine halb gefüllte Antwort wäre die schlechteste Variante,
// weil sie vollständig aussähe.
func writeDocumentConflict(w http.ResponseWriter, st *store.Store, id int64) {
	d, err := st.DocumentByID(id)
	if err != nil {
		writeErr(w, http.StatusConflict, "revision conflict")
		return
	}
	rev, err := st.DocumentRevision(id, d.HeadRevision)
	if err != nil {
		writeErr(w, http.StatusConflict, "revision conflict")
		return
	}
	out := map[string]any{
		"error":         "revision conflict",
		"head_revision": d.HeadRevision,
		"person":        rev.Person,
		"at":            rev.CreatedAt,
		"message":       rev.Message,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	json.NewEncoder(w).Encode(out)
}

func (a *api) patchDocument(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	var patch map[string]string
	if err := readJSON(r, &patch); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, ok := patch["body"]; ok {
		writeErr(w, http.StatusBadRequest, "body cannot be patched; push a revision instead")
		return
	}
	if slug, ok := patch["slug"]; ok {
		if err := docwork.ValidateSlug(slug); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := a.st.PatchDocument(id, patch); err != nil {
		status := http.StatusBadRequest
		if err == sql.ErrNoRows {
			status = http.StatusNotFound
		}
		writeErr(w, status, err.Error())
		return
	}
	d, err := a.st.DocumentByID(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, 200, d)
}

func (a *api) listDocuments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ds, err := a.st.Documents(scope.NormalizeRemote(q.Get("project")), q.Get("kind"), q.Get("include_archived") == "1")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if slug := q.Get("slug"); slug != "" {
		for _, d := range ds {
			if d.Slug == slug {
				writeJSON(w, 200, []store.Document{d})
				return
			}
		}
		writeJSON(w, 200, []store.Document{})
		return
	}
	writeJSON(w, 200, ds)
}

func (a *api) getDocument(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	d, err := a.st.DocumentByID(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, 200, d)
}

func (a *api) documentRevisions(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	revs, err := a.st.DocumentRevisions(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, 200, revs)
}

func (a *api) documentRevision(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	n, err := strconv.Atoi(r.PathValue("rev"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad revision")
		return
	}
	rev, err := a.st.DocumentRevision(id, n)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, 200, rev)
}
