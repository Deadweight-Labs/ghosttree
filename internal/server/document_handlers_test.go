package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	docwork "github.com/Deadweight-Labs/ghosttree/internal/doc"
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

// The refusal uses the same detection as the redaction of transcripts, so a
// password behind a label is refused too, and prose and code that merely use the
// word are not. The values are made up.
func TestDocumentWritesRefuseLabeledPasswordsButNotProse(t *testing.T) {
	srv, token, _ := newDocumentServer(t)
	for i, c := range []struct {
		body string
		want int
	}{
		{"Setup\n\nPasswort: Hx7!kQ2pz\n", http.StatusBadRequest},
		{"export DB_PASSWORD=\"s3cr3t-value\"", http.StatusBadRequest},
		{"clone https://deploy:Hx7!kQ2pz@git.example.com/org/repo.git", http.StatusBadRequest},
		{"Passwort zurücksetzen: password := r.FormValue(\"password\"); db: ${DB_PASSWORD}", http.StatusOK},
	} {
		res := req(t, "POST", srv.URL+"/api/documents", token, map[string]any{
			"project": "p", "slug": "labeled-" + strconv.Itoa(i), "kind": "spec", "title": "T", "body": c.body,
		})
		res.Body.Close()
		if res.StatusCode != c.want {
			t.Errorf("body %q: status %d, want %d", c.body, res.StatusCode, c.want)
		}
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

// Every revision has its own writer (REQ-620): creating, importing and pushing
// stamp the revision row with the caller, an agent with its session.
func TestDocumentRevisionsCarryTheirWriter(t *testing.T) {
	f, _ := guestRoomFixture(t, true)
	codexSession(t, f)
	created := decode[store.Document](t, actorCall(t, f, "mia", "codex-mia", 200, "POST", "/api/documents",
		map[string]any{"project": accProject, "slug": "stamped", "kind": "report", "title": "T", "body": "eins\n", "message": "m"}))
	pushed := decode[store.Document](t, actorCall(t, f, "lena", "", 200, "PUT", idPath("/api/documents/%d/revisions", created.ID),
		map[string]any{"base_revision": 1, "body": "zwei\n", "message": "m2"}))
	r1, err := f.st.DocumentRevision(created.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ea := stampOf(t, f.st, actor.DocRevision, r1.ID); !ea.IsAgent() || ea.Harness != "codex" {
		t.Errorf("revision 1: %+v", ea)
	}
	r2, err := f.st.DocumentRevision(created.ID, pushed.HeadRevision)
	if err != nil {
		t.Fatal(err)
	}
	if ea := stampOf(t, f.st, actor.DocRevision, r2.ID); ea.AccountID != 2 {
		t.Errorf("revision 2 is not lena's: %+v", ea)
	}
}

// Reading a document's revisions and finding it follows the project roles:
// owner, members and the guest read, whoever has no role gets 404 or nothing.
func TestDocumentRevisionsAndSearchByRole(t *testing.T) {
	f := accessAPI(t, true)
	doc := f.id["d-mia"]
	for _, who := range []string{"robin", "lena", "mia", "rex", "gus"} {
		f.expect(t, who, 200, "GET", idPath("/api/documents/%d/revisions", doc), nil)
		f.expect(t, who, 200, "GET", idPath("/api/documents/%d/revisions/1", doc), nil)
		if out := f.expect(t, who, 200, "GET", "/api/documents?q=text&project="+accProject, nil); !strings.Contains(out, `"slug":"d-mia"`) {
			t.Errorf("%s does not find the document: %s", who, out)
		}
	}
	f.expect(t, "nora", 404, "GET", idPath("/api/documents/%d/revisions", doc), nil)
	f.expect(t, "nora", 404, "GET", idPath("/api/documents/%d/revisions/1", doc), nil)
	if out := f.expect(t, "nora", 200, "GET", "/api/documents?q=text", nil); strings.Contains(out, "d-mia") {
		t.Errorf("stranger finds the document: %s", out)
	}
}

// A repeated import writes nothing, so it stamps nothing: revision 1 keeps the
// writer of the first import, whoever repeats it.
func TestRepeatedDocumentImportKeepsTheFirstWriter(t *testing.T) {
	f, _ := guestRoomFixture(t, true)
	codexSession(t, f)
	body := "imported\n"
	run := decode[struct{ ID int64 }](t, actorCall(t, f, "mia", "codex-mia", 200, "POST", "/api/migrations",
		map[string]any{"project": accProject, "artifacts": map[string]string{"docs/a.md": store.Digest(body)}}))
	in := store.MigratedDocument{Document: store.Document{Project: accProject, Slug: "imported", Kind: "spec", Title: "I"},
		Source: "docs/a.md", Digest: store.Digest(body), Body: body, Message: "import"}
	path := idPath("/api/migrations/%d/documents/import", run.ID)
	first := decode[store.Document](t, actorCall(t, f, "mia", "codex-mia", 200, "POST", path, in))
	// Someone else moves the head without a stamp (the store directly, as an
	// older server would have).
	if _, err := f.st.PushRevision(first.ID, 1, "edited\n", "edit", "robin"); err != nil {
		t.Fatal(err)
	}
	again := decode[store.Document](t, actorCall(t, f, "lena", "", 200, "POST", path, in))
	if again.ID != first.ID {
		t.Fatalf("repeat created another document: %d vs %d", again.ID, first.ID)
	}
	r1, err := f.st.DocumentRevision(first.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ea := stampOf(t, f.st, actor.DocRevision, r1.ID); ea.AccountID != 3 || ea.Harness != "codex" {
		t.Errorf("revision 1 changed its writer on the repeat: %+v", ea)
	}
	r2, err := f.st.DocumentRevision(first.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if ea, ok, _ := f.st.ActorOf(actor.DocRevision, r2.ID); ok {
		t.Errorf("the repeated import stamped robin's revision 2: %+v", ea)
	}
}

// Create and push refuse a text over the limit with 413 and the size, before
// anything is stored.
func TestDocumentWritesRefuseTooLargeTexts(t *testing.T) {
	srv, token, st := newDocumentServer(t)
	big := strings.Repeat("a", docwork.MaxBodyBytes+1)
	res := req(t, "POST", srv.URL+"/api/documents", token, map[string]any{"project": "github.com/x/y", "slug": "big", "kind": "spec", "title": "B", "body": big})
	out, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(out), "document_too_large") || !strings.Contains(string(out), "at most 5 MB") {
		t.Fatalf("create: %d %s", res.StatusCode, out)
	}
	d, err := st.CreateDocument(store.Document{Project: "github.com/x/y", Slug: "small", Kind: "spec", Title: "S", Person: "test"}, "s\n", "m")
	if err != nil {
		t.Fatal(err)
	}
	// Escapes make the request larger than the text: the reader stops it.
	huge := strings.Repeat("<", docwork.MaxBodyBytes)
	res = req(t, "PUT", srv.URL+"/api/documents/"+strconv.FormatInt(d.ID, 10)+"/revisions", token, map[string]any{"base_revision": 1, "body": huge, "message": "m"})
	out, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(out), "document_too_large") {
		t.Fatalf("push: %d %s", res.StatusCode, out)
	}
	if got, _ := st.DocumentByID(d.ID); got.HeadRevision != 1 {
		t.Fatalf("head moved to %d", got.HeadRevision)
	}
}

// PAIN-22: the refusal names line, column and the label, and never the value;
// a command name in Markdown code is no secret.
func TestDocumentBodyRefusalNamesLineColumnAndLabel(t *testing.T) {
	err := validateDocumentBody("# Spec\n\nSet it up.\n  pw: Xy7#abcQ9 is the login\n")
	if err == nil {
		t.Fatal("a labeled password was accepted")
	}
	for _, want := range []string{"line 4", "column 3", `"pw"`, "nothing was stored"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "Xy7") {
		t.Errorf("the refusal holds the value: %v", err)
	}
	if err := validateDocumentBody("Apart from `pw export`/`pw deploy` and then `pw live`.\n"); err != nil {
		t.Errorf("a command name was refused: %v", err)
	}
}
