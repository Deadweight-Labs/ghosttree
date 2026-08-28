package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func newDocumentServer(t *testing.T) (*httptest.Server, string, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("test")
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	return srv, token, st
}

func TestPushRevisionConflictReturns409WithHead(t *testing.T) {
	srv, token, st := newDocumentServer(t)

	d, _ := st.CreateDocument(store.Document{
		Project: "p", Slug: "a", Kind: "spec", Title: "A", Person: "alice",
	}, "eins\n", "erste")
	if _, err := st.PushRevision(d.ID, 1, "zwei\n", "zweite", "bob"); err != nil {
		t.Fatalf("vorbereitender push: %v", err)
	}

	// req marshalliert den Body selbst (server_test.go:100) — ein []byte oder
	// string wuerde als JSON-String kodiert und nicht als Objekt ankommen.
	res := req(t, "PUT", srv.URL+"/api/documents/"+strconv.FormatInt(d.ID, 10)+"/revisions",
		token, map[string]any{"base_revision": 1, "body": "drei\n", "message": "meine"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status %d, erwartet 409", res.StatusCode)
	}
	var out struct {
		Head    int    `json:"head_revision"`
		Person  string `json:"person"`
		Message string `json:"message"`
		At      string `json:"at"`
	}
	json.NewDecoder(res.Body).Decode(&out)
	if out.Head != 2 || out.Person != "bob" || out.Message != "zweite" {
		t.Fatalf("konfliktantwort unvollstaendig: %+v", out)
	}
	if out.At == "" {
		t.Fatalf("konfliktantwort fehlt 'at': %+v", out)
	}
}

func TestPatchDocumentRejectsBody(t *testing.T) {
	srv, token, st := newDocumentServer(t)
	d, _ := st.CreateDocument(store.Document{Project: "p", Slug: "a", Kind: "spec", Title: "A"}, "eins\n", "")

	res := req(t, "PATCH", srv.URL+"/api/documents/"+strconv.FormatInt(d.ID, 10),
		token, map[string]any{"body": "geschmuggelt"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, erwartet 400", res.StatusCode)
	}
	rev, _ := st.DocumentRevision(d.ID, 1)
	if rev.Body != "eins\n" {
		t.Fatalf("body wurde veraendert: %q", rev.Body)
	}
}

func TestCreateDocumentAndGetIt(t *testing.T) {
	srv, token, _ := newDocumentServer(t)

	res := req(t, "POST", srv.URL+"/api/documents", token, map[string]any{
		"project": "github.com/x/y", "slug": "arch", "kind": "spec",
		"title": "Architektur", "body": "# Einleitung\n", "message": "erster Entwurf",
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST status %d", res.StatusCode)
	}
	var d store.Document
	json.NewDecoder(res.Body).Decode(&d)
	if d.ID == 0 || d.Slug != "arch" || d.HeadRevision != 1 {
		t.Fatalf("dokument unvollstaendig: %+v", d)
	}

	res2 := req(t, "GET", srv.URL+"/api/documents/"+strconv.FormatInt(d.ID, 10), token, nil)
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("GET status %d", res2.StatusCode)
	}
	var got store.Document
	json.NewDecoder(res2.Body).Decode(&got)
	if got.Title != "Architektur" {
		t.Fatalf("titel falsch: %q", got.Title)
	}
}

func TestCreateDocumentRequiresFields(t *testing.T) {
	srv, token, _ := newDocumentServer(t)

	// slug fehlt
	res := req(t, "POST", srv.URL+"/api/documents", token, map[string]any{
		"project": "github.com/x/y", "kind": "spec", "title": "A", "body": "b",
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, erwartet 400 wenn slug fehlt", res.StatusCode)
	}
}

func TestCreateAndPatchDocumentRejectUnsafeSlugs(t *testing.T) {
	srv, token, st := newDocumentServer(t)
	for _, slug := range []string{"../readme", "nested/name", "con", "has space"} {
		res := req(t, "POST", srv.URL+"/api/documents", token, map[string]any{
			"project": "p", "slug": slug, "kind": "spec", "title": "Unsafe", "body": "b",
		})
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("create slug %q status = %d, want 400", slug, res.StatusCode)
		}
	}
	d, err := st.CreateDocument(store.Document{Project: "p", Slug: "safe", Kind: "spec", Title: "Safe"}, "b", "")
	if err != nil {
		t.Fatal(err)
	}
	res := req(t, "PATCH", srv.URL+"/api/documents/"+strconv.FormatInt(d.ID, 10), token, map[string]any{"slug": "../../readme"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("patch status = %d, want 400", res.StatusCode)
	}
}

func TestDocumentWritesRejectSecretsInsteadOfRewritingThem(t *testing.T) {
	srv, token, st := newDocumentServer(t)
	secret := "token ghp_" + "AbCdEfGhIjKlMnOpQrStUvWxYz1234567890"
	res := req(t, "POST", srv.URL+"/api/documents", token, map[string]any{
		"project": "p", "slug": "secret", "kind": "spec", "title": "Secret", "body": secret,
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("create status = %d, want 400", res.StatusCode)
	}
	document, err := st.CreateDocument(store.Document{Project: "p", Slug: "safe", Kind: "spec", Title: "Safe"}, "safe", "")
	if err != nil {
		t.Fatal(err)
	}
	res = req(t, "PUT", srv.URL+"/api/documents/"+strconv.FormatInt(document.ID, 10)+"/revisions", token,
		map[string]any{"base_revision": 1, "body": secret})
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("push status = %d, want 400", res.StatusCode)
	}
	got, _ := st.DocumentByID(document.ID)
	if got.HeadRevision != 1 {
		t.Fatalf("rejected push moved head to %d", got.HeadRevision)
	}
}

func TestListDocuments(t *testing.T) {
	srv, token, st := newDocumentServer(t)

	st.CreateDocument(store.Document{Project: "p", Slug: "one", Kind: "spec", Title: "One"}, "b", "")
	st.CreateDocument(store.Document{Project: "p", Slug: "two", Kind: "plan", Title: "Two"}, "b", "")
	st.CreateDocument(store.Document{Project: "other", Slug: "three", Kind: "spec", Title: "Three"}, "b", "")

	res := req(t, "GET", srv.URL+"/api/documents?project=p", token, nil)
	defer res.Body.Close()
	var ds []store.Document
	json.NewDecoder(res.Body).Decode(&ds)
	if len(ds) != 2 {
		t.Fatalf("liste hat %d Eintraege, erwartet 2", len(ds))
	}
}

func TestListDocumentsFilterByKind(t *testing.T) {
	srv, token, st := newDocumentServer(t)

	st.CreateDocument(store.Document{Project: "p", Slug: "one", Kind: "spec", Title: "One"}, "b", "")
	st.CreateDocument(store.Document{Project: "p", Slug: "two", Kind: "plan", Title: "Two"}, "b", "")

	res := req(t, "GET", srv.URL+"/api/documents?project=p&kind=spec", token, nil)
	defer res.Body.Close()
	var ds []store.Document
	json.NewDecoder(res.Body).Decode(&ds)
	if len(ds) != 1 || ds[0].Slug != "one" {
		t.Fatalf("liste hat %d Eintraege: %+v", len(ds), ds)
	}
}

func TestPushRevisionAdvancesHead(t *testing.T) {
	srv, token, st := newDocumentServer(t)

	d, _ := st.CreateDocument(store.Document{
		Project: "p", Slug: "a", Kind: "spec", Title: "A",
	}, "version eins\n", "erste")

	res := req(t, "PUT", srv.URL+"/api/documents/"+strconv.FormatInt(d.ID, 10)+"/revisions",
		token, map[string]any{"base_revision": 1, "body": "version zwei\n", "message": "zweite"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, erwartet 200", res.StatusCode)
	}
	var updated store.Document
	json.NewDecoder(res.Body).Decode(&updated)
	if updated.HeadRevision != 2 {
		t.Fatalf("head_revision = %d, erwartet 2", updated.HeadRevision)
	}
}

func TestDocumentRevisionsListAndGet(t *testing.T) {
	srv, token, st := newDocumentServer(t)

	d, _ := st.CreateDocument(store.Document{
		Project: "p", Slug: "a", Kind: "spec", Title: "A",
	}, "eins\n", "erste")
	st.PushRevision(d.ID, 1, "zwei\n", "zweite", "test")

	// Liste (ohne Body)
	res := req(t, "GET", srv.URL+"/api/documents/"+strconv.FormatInt(d.ID, 10)+"/revisions", token, nil)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("revisions-liste status %d", res.StatusCode)
	}
	var revs []store.DocumentRevision
	json.NewDecoder(res.Body).Decode(&revs)
	if len(revs) != 2 {
		t.Fatalf("revisions = %d, erwartet 2", len(revs))
	}

	// Einzelne Revision mit Body
	res2 := req(t, "GET", srv.URL+"/api/documents/"+strconv.FormatInt(d.ID, 10)+"/revisions/1", token, nil)
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("revision/1 status %d", res2.StatusCode)
	}
	var rev store.DocumentRevision
	json.NewDecoder(res2.Body).Decode(&rev)
	if rev.Body != "eins\n" {
		t.Fatalf("body = %q, erwartet 'eins\\n'", rev.Body)
	}
}

func TestPatchDocumentUpdatesTitle(t *testing.T) {
	srv, token, st := newDocumentServer(t)
	d, _ := st.CreateDocument(store.Document{Project: "p", Slug: "a", Kind: "spec", Title: "Alt"}, "b", "")

	res := req(t, "PATCH", srv.URL+"/api/documents/"+strconv.FormatInt(d.ID, 10),
		token, map[string]any{"title": "Neu"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, erwartet 200", res.StatusCode)
	}
	var got store.Document
	json.NewDecoder(res.Body).Decode(&got)
	if got.Title != "Neu" {
		t.Fatalf("titel = %q, erwartet 'Neu'", got.Title)
	}
}

func TestPatchDocumentRejectsUnknownField(t *testing.T) {
	srv, token, st := newDocumentServer(t)
	d, _ := st.CreateDocument(store.Document{Project: "p", Slug: "a", Kind: "spec", Title: "A"}, "b", "")

	res := req(t, "PATCH", srv.URL+"/api/documents/"+strconv.FormatInt(d.ID, 10),
		token, map[string]any{"person": "gekapert"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, erwartet 400 fuer unbekanntes Feld", res.StatusCode)
	}
}

func TestGetDocumentNotFound(t *testing.T) {
	srv, token, _ := newDocumentServer(t)
	res := req(t, "GET", srv.URL+"/api/documents/9999", token, nil)
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, erwartet 404", res.StatusCode)
	}
}
