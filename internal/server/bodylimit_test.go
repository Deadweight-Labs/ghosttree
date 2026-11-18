package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Ein Körper über der Standardgrenze wird mit 413 abgewiesen, auch auf einer
// Route, deren Handler selbst keine Grenze setzt.
func TestAPIBodyOverDefaultLimitIs413(t *testing.T) {
	srv, token := newTestServer(t)
	for _, route := range []string{"/api/knowledge", "/api/requests", "/api/pains", "/api/ideas/1/state"} {
		huge := `{"title":"t","body":"` + strings.Repeat("a", defaultBodyLimit+10) + `"}`
		r, _ := http.NewRequest("POST", srv.URL+route, strings.NewReader(huge))
		r.Header.Set("Authorization", "Bearer "+token)
		// Ohne Content-Length (chunked): die Grenze muss beim Lesen greifen.
		r.ContentLength = -1
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatalf("%s: %v", route, err)
		}
		var out map[string]string
		_ = json.NewDecoder(res.Body).Decode(&out)
		res.Body.Close()
		if res.StatusCode != http.StatusRequestEntityTooLarge || out["code"] != "body_too_large" {
			t.Errorf("%s: status %d %v, want 413 body_too_large", route, res.StatusCode, out)
		}
	}
}

// Mit angekündigter Länge antwortet der Server, ohne zu lesen.
func TestAPIBodyAnnouncedTooLargeIsRefusedBeforeReading(t *testing.T) {
	srv, token := newTestServer(t)
	huge := bytes.Repeat([]byte("a"), defaultBodyLimit+10)
	r, _ := http.NewRequest("POST", srv.URL+"/api/knowledge", bytes.NewReader(huge))
	r.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", res.StatusCode)
	}
}

// Eine Route aus der Tabelle nimmt mehr als die Standardgrenze.
func TestAPIBodyLimitTableAllowsLargerBodies(t *testing.T) {
	a := &api{}
	limits := a.bodyLimits()
	for _, pattern := range []string{"POST /api/sessions/{id}/chunks", "POST /api/documents", "POST /api/attachments"} {
		if limits[pattern] <= defaultBodyLimit {
			t.Errorf("%s: limit %d, want more than the default", pattern, limits[pattern])
		}
	}
}

// Liste mit limit weit über der Obergrenze wird gekappt.
func TestListLimitParamIsCapped(t *testing.T) {
	r, _ := http.NewRequest("GET", "/x?limit=100000000", nil)
	if got := intParam(r, "limit", 50); got != maxListLimit {
		t.Fatalf("limit = %d, want %d", got, maxListLimit)
	}
	r, _ = http.NewRequest("GET", "/x?limit=7", nil)
	if got := intParam(r, "limit", 50); got != 7 {
		t.Fatalf("limit = %d, want 7", got)
	}
}

// Ein Fehler des Servers gibt keinen SQL-Text an den Client.
func TestInternalErrorHidesDetail(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "req-123")
	writeStoreError(w, http.StatusInternalServerError, errors.New("UNIQUE constraint failed: knowledge.id /var/lib/x.db"))
	body := w.Body.String()
	if strings.Contains(body, "UNIQUE") || strings.Contains(body, "/var/lib") {
		t.Fatalf("detail leaked: %s", body)
	}
	if !strings.Contains(body, "req-123") || w.Code != 500 {
		t.Fatalf("want request id and 500: %d %s", w.Code, body)
	}
	// Client-Fehler behalten ihre Meldung.
	w = httptest.NewRecorder()
	writeStoreError(w, http.StatusBadRequest, errors.New("title is required"))
	if !strings.Contains(w.Body.String(), "title is required") {
		t.Fatalf("400 lost its message: %s", w.Body.String())
	}
}
