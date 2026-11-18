package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	docwork "github.com/Deadweight-Labs/ghosttree/internal/doc"
)

// defaultBodyLimit gilt für jeden Körper einer API-Anfrage, der nicht in
// bodyLimits steht. Ein JSON-Körper wird ganz in den Speicher gelesen; ohne
// Grenze bringt ein einziges Token den Server mit einem Körper von mehreren GB
// zum Absturz.
const defaultBodyLimit = 1 << 20

// documentBodyLimit lässt Platz für die JSON-Maskierung eines Dokumenttexts.
const documentBodyLimit = 2*docwork.MaxBodyBytes + 64<<10

// bodyLimits nennt die Routen, die mehr als defaultBodyLimit brauchen: Uploads
// von Transkripten, Dokumente, Anhänge, Snapshots und Konzepttexte. Wer eine
// Route hier größer macht, begründet es; die Handler setzen ihre eigene, engere
// Grenze zusätzlich (readLimitedJSON).
func (a *api) bodyLimits() map[string]int64 {
	return map[string]int64{
		"POST /api/sessions/{id}/chunks":             maxChunkBody,
		"POST /api/session-git-events":               maxSessionGitBody,
		"POST /api/context-log":                      smallBody,
		"POST /api/context-snapshots":                contextSnapshotCreateBodyLimit(a.snapshotLimits),
		"POST /api/documents":                        documentBodyLimit,
		"PUT /api/documents/{id}/revisions":          documentBodyLimit,
		"POST /api/migrations/{id}/documents":        documentBodyLimit,
		"POST /api/migrations/{id}/documents/import": documentBodyLimit,
		"POST /api/ideas":                            maxWorkshopBody,
		"POST /api/ideas/{id}/texts":                 maxWorkshopBody,
		"POST /api/ideas/{id}/concept":               maxWorkshopBody,
		"POST /api/attachments":                      maxAttachmentBody,
	}
}

// limitBody setzt die Grenze des Körpers vor den Handler. Mit early wird eine
// Anfrage, die sich als zu groß ankündigt, abgewiesen, ohne dass ein Byte
// gelesen wird; die Routen aus bodyLimits lassen ihren Handler den Fehler mit
// eigenem Code beantworten (document_too_large, snapshot_request_too_large).
func limitBody(limit int64, early bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.Body == http.NoBody {
			next.ServeHTTP(w, r)
			return
		}
		if early && r.ContentLength > limit {
			writeBodyTooLarge(w, limit)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

// writeBodyTooLarge antwortet 413 und nennt die Grenze.
func writeBodyTooLarge(w http.ResponseWriter, limit int64) {
	writeCoded(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body is too large: this route takes at most "+byteSize(limit))
}

// isBodyTooLarge sagt, ob err vom Überschreiten einer Körpergrenze kommt.
func isBodyTooLarge(err error) bool {
	var tooBig *http.MaxBytesError
	return errors.As(err, &tooBig)
}

func byteSize(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return strconv.FormatInt(n>>20, 10) + " MiB"
	case n >= 1<<10:
		return strconv.FormatInt(n>>10, 10) + " KiB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

// withBodyLimit gibt der Route ihre Körpergrenze: die aus bodyLimits oder
// defaultBodyLimit. Routen ohne Körper (GET, HEAD) bleiben unberührt.
func (a *api) withBodyLimit(pattern string, h http.Handler) http.Handler {
	method, _, _ := strings.Cut(pattern, " ")
	if method == http.MethodGet || method == http.MethodHead {
		return h
	}
	limit, ok := a.bodyLimits()[pattern]
	if !ok {
		return limitBody(defaultBodyLimit, true, h)
	}
	return limitBody(limit, false, h)
}
