package store

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestParseEmbedKinds(t *testing.T) {
	for in, want := range map[string][]string{
		"":                     nil,
		"knowledge":            {KindKnowledge},
		" Requests , pains ":   {KindRequest, KindPain},
		"ghosts,docs,requests": {KindRequest, KindDocument, KindGhost},
		"files,files":          {KindGhost},
		"all":                  EmbedKinds(),
		"ghost,knowledge":      {KindKnowledge, KindGhost},
		"sessions":             {KindSession},
		"Session, pain":        {KindPain, KindSession},
	} {
		got, err := ParseEmbedKinds(in)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%q: %v %v, want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"widgets", "knowledge,nope", "request;pain"} {
		if _, err := ParseEmbedKinds(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if k, err := ParseEmbedKind("Files"); err != nil || k != KindGhost {
		t.Errorf("%q %v", k, err)
	}
	// The sessions come after every other kind: the job serves them last.
	if kinds := EmbedKinds(); kinds[len(kinds)-1] != KindSession {
		t.Errorf("%v", kinds)
	}
}

// kindFixtureOps makes, edits and touches an object of every kind except
// knowledge, which has its own tests.
type kindOps struct {
	kind string
	make func(t *testing.T, st *Store) int64
	// edit changes the text that is embedded.
	edit func(t *testing.T, st *Store, id int64)
	// touch changes the object without changing that text.
	touch func(t *testing.T, st *Store, id int64)
}

func headOf(t *testing.T, st *Store, id int64) int {
	t.Helper()
	d, err := st.DocumentByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return d.HeadRevision
}

func kindOpsList() []kindOps {
	return []kindOps{
		{
			kind: KindRequest,
			make: func(t *testing.T, st *Store) int64 { return addRequest(t, st, "wagen", "kaputt") },
			edit: func(t *testing.T, st *Store, id int64) {
				if err := st.UpdateRequest(id, map[string]string{"description": "ganz anders"}, "mia", "weil"); err != nil {
					t.Fatal(err)
				}
			},
			touch: func(t *testing.T, st *Store, id int64) {
				if err := st.UpdateRequest(id, map[string]string{"priority": "hoch"}, "mia", "weil"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			kind: KindPain,
			make: func(t *testing.T, st *Store) int64 {
				res, err := st.db.Exec(`INSERT INTO pains(project,title,body,state,created_at,updated_at) VALUES('p','wagen','kaputt','active',?,?)`, now(), now())
				if err != nil {
					t.Fatal(err)
				}
				id, _ := res.LastInsertId()
				return id
			},
			edit: func(t *testing.T, st *Store, id int64) {
				if _, err := st.db.Exec(`UPDATE pains SET body='ganz anders', updated_at=? WHERE id=?`, now(), id); err != nil {
					t.Fatal(err)
				}
			},
			touch: func(t *testing.T, st *Store, id int64) {
				if _, err := st.db.Exec(`UPDATE pains SET remaining='noch offen', updated_at=? WHERE id=?`, now(), id); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			kind: KindGhost,
			make: func(t *testing.T, st *Store) int64 {
				id, err := st.PutGhostFile(GhostFile{Project: "p", Path: "a/wagen.go", Kind: "file", Description: "kaputt"})
				if err != nil {
					t.Fatal(err)
				}
				return id
			},
			edit: func(t *testing.T, st *Store, id int64) {
				if _, err := st.PutGhostFile(GhostFile{Project: "p", Path: "a/wagen.go", Kind: "file", Description: "ganz anders"}); err != nil {
					t.Fatal(err)
				}
			},
			touch: func(t *testing.T, st *Store, id int64) {
				if _, err := st.PutGhostFile(GhostFile{Project: "p", Path: "a/wagen.go", Kind: "file", Description: "kaputt", ContentSHA: "abc", LineCount: 7}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			kind: KindDocument,
			make: func(t *testing.T, st *Store) int64 {
				d, err := st.CreateDocument(Document{Project: "p", Slug: "wagen", Kind: "report", Title: "wagen"}, "kaputt", "m")
				if err != nil {
					t.Fatal(err)
				}
				return d.ID
			},
			edit: func(t *testing.T, st *Store, id int64) {
				if _, err := st.PushRevision(id, headOf(t, st, id), "ganz anders", "m", "mia"); err != nil {
					t.Fatal(err)
				}
			},
			touch: func(t *testing.T, st *Store, id int64) {
				if _, err := st.PushRevision(id, headOf(t, st, id), "kaputt", "same text again", "mia"); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
}

// Every kind follows the same rules: an object without a vector is due, a vector
// of the current text is current, a changed text is due again, a change that
// leaves the text alone is not, and a parked text is left alone until it changes.
func TestEmbeddingDueAndStatusFollowTheTextOfEveryKind(t *testing.T) {
	for _, ops := range kindOpsList() {
		t.Run(ops.kind, func(t *testing.T) {
			st := openTest(t)
			_, c := newFakeEmbedder(t)
			m := modelOf(c)
			id := ops.make(t, st)
			due := func(full bool) EmbedDue {
				d, err := st.EmbeddingDue(ops.kind, m, 8, time.Now(), full)
				if err != nil {
					t.Fatal(err)
				}
				return d
			}
			status := func() EmbedStatus {
				s, err := st.EmbeddingStatus(ops.kind, m)
				if err != nil {
					t.Fatal(err)
				}
				return s
			}
			if d := due(false); len(d.Items) != 1 || d.Items[0].ID != id || !strings.HasPrefix(d.Items[0].Text, "title: ") {
				t.Fatalf("a new object is due, with a finished text: %+v", d)
			}
			if s := status(); s.Active != 1 || s.Current != 0 || s.Due != 1 {
				t.Fatalf("%+v", s)
			}
			embedKindAll(t, st, c, ops.kind)
			if s := status(); s.Current != 1 || s.Due != 0 {
				t.Fatalf("%+v", s)
			}

			ops.touch(t, st, id)
			if d := due(false); len(d.Items) != 0 || len(d.Unchanged) != 1 {
				t.Fatalf("a change that leaves the text alone is looked at, not embedded: %+v", d)
			}
			// The job passes the time it read the text; a second later than the edit is
			// what a real run has.
			later := time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
			if err := st.TouchEmbeddings(ops.kind, due(false).Unchanged, later); err != nil {
				t.Fatal(err)
			}
			if d := due(false); len(d.Items) != 0 || len(d.Unchanged) != 0 {
				t.Fatalf("once looked at it is not looked at again: %+v", d)
			}
			if _, err := st.db.Exec(`UPDATE embeddings SET read_at='2000-01-01T00:00:00Z'`); err != nil {
				t.Fatal(err)
			}

			ops.edit(t, st, id)
			d := due(false)
			if len(d.Items) != 1 || !strings.Contains(d.Items[0].Text, "ganz anders") {
				t.Fatalf("an edited text is due again: %+v", d)
			}
			if s := status(); s.Current != 0 || s.Due != 1 {
				t.Fatalf("%+v", s)
			}

			// Parked for good: left alone until the text changes.
			it := d.Items[0]
			if err := st.RecordEmbedFailure(EmbedFailure{Kind: ops.kind, ObjectID: id, Model: m.Model, PrefixVersion: m.PrefixVersion,
				TextHash: it.Hash, Attempts: 1, Permanent: true, RetryAt: "2099-01-01T00:00:00Z", Error: "refused"}); err != nil {
				t.Fatal(err)
			}
			if d := due(true); len(d.Items) != 0 {
				t.Fatalf("a parked text is not due: %+v", d)
			}
			if s := status(); s.Parked != 1 || s.Due != 1 {
				t.Fatalf("%+v", s)
			}
			ops.edit(t, st, id) // same text again: still parked
			if d := due(true); len(d.Items) != 0 {
				t.Fatalf("the same text stays parked: %+v", d)
			}

			// Another model: no vector counts as current.
			other := EmbedModel{Model: "another", PrefixVersion: m.PrefixVersion}
			if s, err := st.EmbeddingStatus(ops.kind, other); err != nil || s.Current != 0 {
				t.Fatalf("%+v %v", s, err)
			}
		})
	}
}

func TestEmbeddingStatusSeesVectorsOfAnotherModelPerKind(t *testing.T) {
	for _, ops := range kindOpsList() {
		t.Run(ops.kind, func(t *testing.T) {
			st := openTest(t)
			_, c := newFakeEmbedder(t)
			ops.make(t, st)
			embedKindAll(t, st, c, ops.kind)
			s, err := st.EmbeddingStatus(ops.kind, EmbedModel{Model: "another", PrefixVersion: c.PrefixVersion()})
			if err != nil || s.Active != 1 || s.Current != 0 || s.OtherModel != 1 || s.Due != 1 {
				t.Fatalf("%+v %v", s, err)
			}
		})
	}
}

// The knowledge tables and the tables of the other kinds do not see each other: an
// id of a request is not the id of an entry.
func TestVectorsOfDifferentKindsWithTheSameIdDoNotCollide(t *testing.T) {
	st := openTest(t)
	_, c := newFakeEmbedder(t)
	k := addK(t, st, "wagen", "kaputt", scope.Axes{}, "trusted", "")
	r := addRequest(t, st, "wagen", "kaputt")
	g, err := st.PutGhostFile(GhostFile{Project: "p", Path: "x", Kind: "file", Description: "kaputt"})
	if err != nil {
		t.Fatal(err)
	}
	if k != r || r != g {
		t.Fatalf("the test wants equal ids: %d %d %d", k, r, g)
	}
	embedAll(t, st, c)
	embedKindAll(t, st, c, KindRequest)
	embedKindAll(t, st, c, KindGhost)
	for kind, table := range map[string]string{KindKnowledge: "knowledge_embeddings", KindRequest: "embeddings", KindGhost: "embeddings"} {
		var n int
		q := `SELECT COUNT(*) FROM ` + table
		if table == "embeddings" {
			q += ` WHERE kind='` + kind + `'`
		}
		if err := st.db.QueryRow(q).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s: %d vectors, %v", kind, n, err)
		}
	}
	// Replacing one does not touch the others.
	if _, err := st.PutGhostFile(GhostFile{Project: "p", Path: "x", Kind: "file", Description: "ganz anders"}); err != nil {
		t.Fatal(err)
	}
	embedKindAll(t, st, c, KindGhost)
	if s, err := st.EmbeddingStatus(KindRequest, modelOf(c)); err != nil || s.Current != 1 {
		t.Fatalf("%+v %v", s, err)
	}
	if s, err := st.KnowledgeEmbeddingStatus(modelOf(c)); err != nil || s.Current != 1 {
		t.Fatalf("%+v %v", s, err)
	}
	if err := st.UpsertEmbedding(Embedding{Kind: "widgets", ObjectID: 1, Model: "m", PrefixVersion: "v", TextHash: "h", Dim: 1, Scale: 1, Vec: []byte{1}, ReadAt: "x"}); err == nil {
		t.Fatal("an unknown kind was accepted")
	}
}

// A document is embedded by its title and the beginning of its text, not all of it.
func TestDocumentVectorsHoldTheBeginningOfTheTextOnly(t *testing.T) {
	st := openTest(t)
	_, c := newFakeEmbedder(t)
	body := "anfangswort " + strings.Repeat("fuellwort ", 400) + "endwort"
	d, err := st.CreateDocument(Document{Project: "p", Slug: "lang", Kind: "report", Title: "Ein langer Bericht"}, body, "m")
	if err != nil {
		t.Fatal(err)
	}
	due, err := st.EmbeddingDue(KindDocument, modelOf(c), 8, time.Now(), true)
	if err != nil || len(due.Items) != 1 || due.Items[0].ID != d.ID {
		t.Fatalf("%+v %v", due, err)
	}
	text := due.Items[0].Text
	if !strings.HasPrefix(text, "title: Ein langer Bericht | text: anfangswort") || strings.Contains(text, "endwort") {
		t.Fatalf("the text that is embedded: %q", text)
	}
	if n := len([]rune(text)); n > embed.MaxDocumentRunes || n < embed.MaxDocumentHeadRunes {
		t.Fatalf("%d characters embedded", n)
	}
	// The ghost description of a directory carries a slash, the root a name.
	if got := embed.GhostText("", "dir", "Wurzel"); !strings.HasPrefix(got, "title: repository root |") {
		t.Fatal(got)
	}
	if got := embed.GhostText("internal/store", "dir", "x"); !strings.HasPrefix(got, "title: internal/store/ |") {
		t.Fatal(got)
	}
}

func TestPurgeEmbeddingsRemovesWhatNoObjectCountsFor(t *testing.T) {
	st := openTest(t)
	_, c := newFakeEmbedder(t)
	k := addK(t, st, "archiviert", "kaputt", scope.Axes{}, "trusted", "")
	embedAll(t, st, c)
	if _, err := st.db.Exec(`UPDATE knowledge SET status='archived' WHERE id=?`, k); err != nil {
		t.Fatal(err)
	}
	// Knowledge keeps what it has always kept.
	if n, err := st.PurgeEmbeddings(KindKnowledge); err != nil || n != 0 {
		t.Fatalf("knowledge: %d %v", n, err)
	}
	if n, err := st.PurgeEmbeddings("nope"); err == nil || n != 0 {
		t.Fatalf("an unknown kind: %d %v", n, err)
	}
	live := addRequest(t, st, "lebt", "kaputt")
	gone := addRequest(t, st, "weg", "kaputt")
	embedKindAll(t, st, c, KindRequest)
	if err := st.RecordEmbedFailure(EmbedFailure{Kind: KindRequest, ObjectID: gone, Model: "m", PrefixVersion: "v", TextHash: "h", Attempts: 1, RetryAt: "2099-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM request_work WHERE request_id=?`, gone); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM request_activity WHERE request_id=?`, gone); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM request_authors WHERE request_id=?`, gone); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM request_criteria WHERE request_id=?`, gone); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM requests WHERE id=?`, gone); err != nil {
		t.Fatal(err)
	}
	if n, err := st.PurgeEmbeddings(KindRequest); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	var left, failures int
	if err := st.db.QueryRow(`SELECT (SELECT COUNT(*) FROM embeddings WHERE kind='request' AND object_id=?), (SELECT COUNT(*) FROM embedding_failures)`, live).Scan(&left, &failures); err != nil || left != 1 || failures != 0 {
		t.Fatalf("%d %d %v", left, failures, err)
	}
	if n, err := st.PurgeEmbeddings(KindRequest); err != nil || n != 0 {
		t.Fatalf("a second purge has nothing to do: %d %v", n, err)
	}
}

// The index of the documents follows the head revision, the title and the slug,
// and leaves an archived document out.
func TestDocumentFullTextIndexFollowsTheDocument(t *testing.T) {
	st := openTest(t)
	find := func(q string) []string {
		ds, err := st.SearchDocuments(q, DocumentSearch{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, d := range ds {
			out = append(out, d.Slug)
		}
		return out
	}
	d, err := st.CreateDocument(Document{Project: "p", Slug: "erster", Kind: "plan", Title: "Der Plan"}, "zebrawort im ersten text", "m")
	if err != nil {
		t.Fatal(err)
	}
	if got := find("zebrawort"); !slices.Equal(got, []string{"erster"}) {
		t.Fatalf("a new document: %v", got)
	}
	if got := find("Plan"); !slices.Equal(got, []string{"erster"}) {
		t.Fatalf("by its title: %v", got)
	}
	if _, err := st.PushRevision(d.ID, 1, "giraffenwort im zweiten text", "m", "mia"); err != nil {
		t.Fatal(err)
	}
	if got := find("zebrawort"); len(got) != 0 {
		t.Fatalf("the old head is still in the index: %v", got)
	}
	if got := find("giraffenwort"); !slices.Equal(got, []string{"erster"}) {
		t.Fatalf("the new head: %v", got)
	}
	if err := st.PatchDocument(d.ID, map[string]string{"slug": "umbenannt", "title": "Neuer Titel"}); err != nil {
		t.Fatal(err)
	}
	if got := find("Titel"); !slices.Equal(got, []string{"umbenannt"}) {
		t.Fatalf("a new title: %v", got)
	}
	if got := find("giraffenwort"); !slices.Equal(got, []string{"umbenannt"}) {
		t.Fatalf("a rename keeps the text: %v", got)
	}
	// The kind filter and the project filter.
	if ds, err := st.SearchDocuments("giraffenwort", DocumentSearch{Project: "other", Limit: 10}); err != nil || len(ds) != 0 {
		t.Fatalf("%v %v", ds, err)
	}
	if ds, err := st.SearchDocuments("giraffenwort", DocumentSearch{Project: "p", Kind: "report", Limit: 10}); err != nil || len(ds) != 0 {
		t.Fatalf("%v %v", ds, err)
	}
	if err := st.PatchDocument(d.ID, map[string]string{"status": "archived"}); err != nil {
		t.Fatal(err)
	}
	if got := find("giraffenwort"); len(got) != 0 {
		t.Fatalf("an archived document is no hit: %v", got)
	}

	// A document that was there before the index: indexed when the database is
	// opened with this version, once.
	if _, err := st.db.Exec(`DELETE FROM documents_fts`); err != nil {
		t.Fatal(err)
	}
	if err := st.PatchDocument(d.ID, map[string]string{"status": "active"}); err != nil {
		t.Fatal(err)
	}
	if got := find("giraffenwort"); len(got) != 0 {
		t.Fatalf("the fixture has no index: %v", got)
	}
	if err := ensureEmbeddings(st.db); err != nil {
		t.Fatal(err)
	}
	if got := find("giraffenwort"); !slices.Equal(got, []string{"umbenannt"}) {
		t.Fatalf("after the backfill: %v", got)
	}
	if err := ensureEmbeddings(st.db); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM documents_fts_docsize`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("a second run indexes nothing again: %d %v", n, err)
	}
}

// Words in a question that the text does not share are the embedding's to find; a
// question that is made of ordinary words answers with nothing from the words.
func TestHybridKindsFuseWordsAndMeaningOnEveryKind(t *testing.T) {
	for _, a := range kindAdapters() {
		t.Run(a.kind, func(t *testing.T) {
			st := openTest(t)
			_, c := newFakeEmbedder(t)
			// The objects are in project p; the search names it where the kind has to.
			proj := ""
			if a.needsProject {
				proj = "p"
			}
			// "car" is found by words, "wagen" only by meaning, "boat" by neither.
			a.add(t, st, "car", "auto defekt ersatzteil", "p", "")
			a.add(t, st, "wagen", "wagen kaputt", "p", "")
			a.add(t, st, "boat", "segel mast", "p", "")
			embedKindAll(t, st, c, a.kind)
			plain := a.search(t, st, robin, proj, "auto defekt", SearchAuto, 10)
			if got := hitNames(plain); !slices.Equal(got, []string{"car"}) {
				t.Fatalf("full text alone: %v", got)
			}
			st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{a.kind}})
			got := a.search(t, st, robin, proj, "auto defekt", SearchAuto, 10)
			if want := []string{"car", "wagen", "boat"}; !slices.Equal(hitNames(got), want) {
				t.Fatalf("hybrid order %v, want %v", hitNames(got), want)
			}
			if got[0].semantic || !got[1].semantic || !got[2].semantic {
				t.Fatalf("only the hits the words missed carry the marker: %+v", got)
			}
			// The limit cuts the fused list.
			if one := a.search(t, st, robin, proj, "auto defekt", SearchAuto, 1); len(one) != 1 || one[0].name != "car" {
				t.Fatalf("limit 1: %+v", one)
			}
			// The ranking alone is not capped and has no word hit marker.
			vec := a.search(t, st, robin, proj, "auto defekt", SearchVectorOnly, 10)
			if len(vec) != 3 {
				t.Fatalf("vector alone: %+v", vec)
			}
			for _, h := range vec {
				if !h.semantic {
					t.Fatalf("every hit of the vector ranking alone is a hit of meaning: %+v", vec)
				}
			}
			// A query of ordinary words is a request for the list, not for a ranking.
			if listing := a.search(t, st, robin, proj, "was ist noch zu tun", SearchAuto, 10); len(listing) != 0 {
				for _, h := range listing {
					if h.semantic {
						t.Fatalf("a listing query was ranked by meaning: %+v", listing)
					}
				}
			}
		})
	}
}
