package store

import (
	"strings"
	"testing"
)

func newDocStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/documents.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPushRevisionIsAtomic(t *testing.T) {
	s := newDocStore(t)
	d, err := s.CreateDocument(Document{
		Project: "p", Slug: "a", Kind: "spec", Title: "A", Person: "alice",
	}, "eins\n", "erste")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if d.HeadRevision != 1 {
		t.Fatalf("head nach create = %d, erwartet 1", d.HeadRevision)
	}

	// Ein Revisions-Insert, der scheitern MUSS: die Nummer ist schon vergeben.
	// Der Head darf sich dadurch nicht bewegen.
	if _, err := s.db.Exec(
		`INSERT INTO document_revisions(document_id,revision,body,digest,message,person,created_at)
		 VALUES(?,?,?,?,?,?,?)`, d.ID, 2, "kollision", "x", "", "", now()); err != nil {
		t.Fatalf("vorbereitender insert: %v", err)
	}
	if _, err := s.PushRevision(d.ID, 1, "zwei\n", "zweite", "alice"); err == nil {
		t.Fatal("push haette scheitern muessen")
	}
	got, err := s.DocumentByID(d.ID)
	if err != nil {
		t.Fatalf("nachlesen: %v", err)
	}
	if got.HeadRevision != 1 {
		t.Fatalf("head nach gescheitertem push = %d, erwartet 1", got.HeadRevision)
	}
}

func TestPushRevisionRejectsStaleBase(t *testing.T) {
	s := newDocStore(t)
	d, _ := s.CreateDocument(Document{Project: "p", Slug: "a", Kind: "spec", Title: "A"}, "eins\n", "")
	if _, err := s.PushRevision(d.ID, 1, "zwei\n", "", "alice"); err != nil {
		t.Fatalf("erster push: %v", err)
	}
	_, err := s.PushRevision(d.ID, 1, "drei\n", "", "bob")
	if err != ErrRevisionConflict {
		t.Fatalf("erwartet ErrRevisionConflict, bekam %v", err)
	}
	got, _ := s.DocumentByID(d.ID)
	if got.HeadRevision != 2 {
		t.Fatalf("head = %d, erwartet 2", got.HeadRevision)
	}
}

func TestDocumentBodyIsPreservedByteForByte(t *testing.T) {
	body := "# Titel\r\n\ttab\tund\tumlaut ä ö ü — Emoji 🌳\r\nletzte Zeile ohne Umbruch"
	s := newDocStore(t)
	d, err := s.CreateDocument(Document{Project: "p", Slug: "a", Kind: "spec", Title: "A"}, body, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rev, err := s.DocumentRevision(d.ID, 1)
	if err != nil {
		t.Fatalf("lesen: %v", err)
	}
	if rev.Body != body {
		t.Fatalf("body veraendert:\nvorher %q\nnachher %q", body, rev.Body)
	}
	if rev.Digest != Digest(body) {
		t.Fatalf("digest passt nicht zum body")
	}
}

func TestSlugIsUniquePerProjectAcrossStatus(t *testing.T) {
	s := newDocStore(t)
	if _, err := s.CreateDocument(Document{Project: "p", Slug: "a", Kind: "spec", Title: "A"}, "x", ""); err != nil {
		t.Fatalf("erstes create: %v", err)
	}
	_, err := s.CreateDocument(Document{Project: "p", Slug: "a", Kind: "plan", Title: "B"}, "y", "")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "unique") {
		t.Fatalf("erwartet UNIQUE-Verletzung, bekam %v", err)
	}
}

func TestDocumentRevisionsOmitBody(t *testing.T) {
	s := newDocStore(t)
	d, _ := s.CreateDocument(Document{Project: "p", Slug: "a", Kind: "spec", Title: "A"}, "eins\n", "erste")
	if _, err := s.PushRevision(d.ID, 1, "zwei\n", "zweite", "alice"); err != nil {
		t.Fatalf("push: %v", err)
	}
	revs, err := s.DocumentRevisions(d.ID)
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("%d Revisionen, erwartet 2", len(revs))
	}
	if revs[0].Revision != 2 || revs[1].Revision != 1 {
		t.Fatalf("falsche Reihenfolge: %d, %d", revs[0].Revision, revs[1].Revision)
	}
	// Das Log ist eine Übersicht. Wer den Body will, holt die Fassung einzeln —
	// sonst zieht ein Log über zwanzig Revisionen zwanzig Dokumente in den Speicher.
	if revs[0].Body != "" {
		t.Fatalf("log traegt den body: %q", revs[0].Body)
	}
	if revs[0].Digest == "" {
		t.Fatal("log ohne digest")
	}
}

func TestPatchDocumentRefusesBody(t *testing.T) {
	s := newDocStore(t)
	d, _ := s.CreateDocument(Document{Project: "p", Slug: "a", Kind: "spec", Title: "A"}, "eins\n", "")
	if err := s.PatchDocument(d.ID, map[string]string{"body": "geschmuggelt"}); err == nil {
		t.Fatal("patch mit body haette abgewiesen werden muessen")
	}
	if err := s.PatchDocument(d.ID, map[string]string{"slug": "b", "status": "archived"}); err != nil {
		t.Fatalf("patch: %v", err)
	}
	got, _ := s.DocumentByID(d.ID)
	if got.Slug != "b" || got.Status != "archived" {
		t.Fatalf("patch wirkungslos: %+v", got)
	}
	if got.HeadRevision != 1 {
		t.Fatalf("patch hat den kopf bewegt: %d", got.HeadRevision)
	}
}

func TestDocumentsListFiltersArchivedAndKind(t *testing.T) {
	s := newDocStore(t)
	s.CreateDocument(Document{Project: "p", Slug: "a", Kind: "spec", Title: "A"}, "x", "")
	s.CreateDocument(Document{Project: "p", Slug: "b", Kind: "plan", Title: "B"}, "x", "")
	c, _ := s.CreateDocument(Document{Project: "p", Slug: "c", Kind: "spec", Title: "C"}, "x", "")
	s.PatchDocument(c.ID, map[string]string{"status": "archived"})
	s.CreateDocument(Document{Project: "andere", Slug: "a", Kind: "spec", Title: "fremd"}, "x", "")

	active, err := s.Documents("p", "", false)
	if err != nil {
		t.Fatalf("liste: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("%d aktive, erwartet 2", len(active))
	}
	all, _ := s.Documents("p", "", true)
	if len(all) != 3 {
		t.Fatalf("%d gesamt, erwartet 3", len(all))
	}
	specs, _ := s.Documents("p", "spec", true)
	if len(specs) != 2 {
		t.Fatalf("%d specs, erwartet 2", len(specs))
	}
}
