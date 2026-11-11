package store

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const anchorTestProject = "github.com/dw/anchors"

func anchorAt(commit, branch string) *gitanchor.Anchor {
	return &gitanchor.Anchor{Commit: commit, Branch: branch, MergeBase: "0000000aaaaaaa", Dirty: true}
}

const (
	commitA = "aaaaaaa1111111111111111111111111111111aa"
	commitB = "bbbbbbb2222222222222222222222222222222bb"
)

func openAnchorStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestKnowledgeStoresTheAnchorOfItsRevision(t *testing.T) {
	st := openAnchorStore(t)
	id, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "mit Anker", Body: "eins",
		Scope: scope.Axes{Project: anchorTestProject}, Person: "robin", GitAnchor: anchorAt(commitA, "feat/a")})
	if err != nil {
		t.Fatal(err)
	}
	k, err := st.KnowledgeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	a := k.GitAnchor
	if a == nil || a.Commit != commitA || a.Branch != "feat/a" || a.MergeBase != "0000000aaaaaaa" || !a.Dirty || a.PR != nil || a.CapturedAt == "" {
		t.Fatalf("anchor = %+v", a)
	}
}

func TestKnowledgeWithoutAnchorStaysEmptyAndCostsNoRow(t *testing.T) {
	st := openAnchorStore(t)
	for _, a := range []*gitanchor.Anchor{nil, {}, {Branch: "nur-ein-name"}} {
		id, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "ohne", Body: "x", Person: "robin", GitAnchor: a})
		if err != nil {
			t.Fatal(err)
		}
		if k, _ := st.KnowledgeByID(id); k.GitAnchor != nil {
			t.Fatalf("anchor %+v must stay empty, got %+v", a, k.GitAnchor)
		}
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM git_anchors`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("anchor rows = %d (%v), want 0", n, err)
	}
}

// Der Anker gehört zur Fassung: eine Bearbeitung hebt die alte Fassung samt
// ihrem Anker auf, die neue bekommt keinen geerbten.
func TestEditingKnowledgeKeepsTheOldAnchorWithTheOldVersion(t *testing.T) {
	st := openAnchorStore(t)
	id, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "t", Body: "eins", Person: "robin", GitAnchor: anchorAt(commitA, "main")})
	if err != nil {
		t.Fatal(err)
	}
	// Eine Statusänderung ist keine neue inhaltliche Fassung: der Anker bleibt.
	if err := st.UpdateKnowledgeBy(id, map[string]string{"confidence": "verified"}, "lena"); err != nil {
		t.Fatal(err)
	}
	if k, _ := st.KnowledgeByID(id); k.GitAnchor == nil || k.GitAnchor.Commit != commitA {
		t.Fatalf("a status change must keep the anchor: %+v", k.GitAnchor)
	}
	if err := st.UpdateKnowledgeBy(id, map[string]string{"body": "zwei"}, "lena"); err != nil {
		t.Fatal(err)
	}
	k, _ := st.KnowledgeByID(id)
	if k.Body != "zwei" || k.GitAnchor != nil {
		t.Fatalf("a new text written without an anchor must not claim the old one: %+v", k.GitAnchor)
	}
	hist, err := st.KnowledgeHistory(id)
	if err != nil || len(hist) != 2 {
		t.Fatalf("history = %d (%v), want 2", len(hist), err)
	}
	for _, v := range hist {
		if v.GitAnchor == nil || v.GitAnchor.Commit != commitA || v.GitAnchor.Branch != "main" {
			t.Fatalf("archived version lost its anchor: %+v", v.GitAnchor)
		}
	}
}

// Eine neue Fassung über den Superseding-Pfad ist ein neuer Eintrag mit
// eigenem Anker; der alte behält seinen.
func TestSupersedingEntryGetsItsOwnAnchor(t *testing.T) {
	st := openAnchorStore(t)
	oldID, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "alt", Body: "x", Person: "robin", GitAnchor: anchorAt(commitA, "main")})
	if err != nil {
		t.Fatal(err)
	}
	newID, err := st.InsertKnowledgeSuperseding(Knowledge{Type: "note", Title: "neu", Body: "y", Person: "robin", GitAnchor: anchorAt(commitB, "main")},
		[]int64{oldID}, "überholt", RelationActor{Name: "robin"})
	if err != nil {
		t.Fatal(err)
	}
	oldK, _ := st.KnowledgeByID(oldID)
	newK, _ := st.KnowledgeByID(newID)
	if oldK.GitAnchor == nil || oldK.GitAnchor.Commit != commitA || newK.GitAnchor == nil || newK.GitAnchor.Commit != commitB {
		t.Fatalf("old %+v new %+v", oldK.GitAnchor, newK.GitAnchor)
	}
}

func TestAnchorRowsAreImmutableExceptTheFirstPR(t *testing.T) {
	st := openAnchorStore(t)
	if _, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "t", Body: "x", Person: "robin", GitAnchor: anchorAt(commitA, "main")}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE git_anchors SET commit_sha='ccccccc'`,
		`UPDATE git_anchors SET branch='anders'`,
		`UPDATE git_anchors SET dirty=0`,
		`UPDATE git_anchors SET captured_at='2000-01-01T00:00:00Z'`,
		`DELETE FROM git_anchors`,
	} {
		if _, err := st.db.Exec(q); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Errorf("%s: want immutable error, got %v", q, err)
		}
	}
	if _, err := st.db.Exec(`UPDATE git_anchors SET pr=42`); err != nil {
		t.Fatalf("the pull request number may be set once: %v", err)
	}
	if _, err := st.db.Exec(`UPDATE git_anchors SET pr=43`); err == nil {
		t.Fatal("the pull request number may not be changed once set")
	}
}

func TestSearchFiltersByCommitPrefixAndPR(t *testing.T) {
	st := openAnchorStore(t)
	p := scope.Axes{Project: anchorTestProject}
	add := func(title string, a *gitanchor.Anchor) {
		if _, err := st.InsertKnowledge(Knowledge{Type: "note", Title: title, Body: "gemeinsamer suchbegriff " + title, Scope: p, Person: "robin", GitAnchor: a}); err != nil {
			t.Fatal(err)
		}
	}
	add("eins", anchorAt(commitA, "main"))
	add("zwei", anchorAt(commitB, "feat/b"))
	add("drei", nil)
	titles := func(f AnchorFilter) string {
		t.Helper()
		ks, err := st.SearchKnowledgeWith("suchbegriff", KnowledgeSearch{Axes: p, Union: true, Anchor: f, NoRecord: true})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, k := range ks {
			out = append(out, k.Title)
		}
		return strings.Join(out, ",")
	}
	if got := titles(AnchorFilter{}); !strings.Contains(got, "eins") || !strings.Contains(got, "zwei") || !strings.Contains(got, "drei") {
		t.Fatalf("no filter must find everything, got %q", got)
	}
	if got := titles(AnchorFilter{Commit: commitA[:7]}); got != "eins" {
		t.Fatalf("commit prefix a: %q", got)
	}
	if got := titles(AnchorFilter{Commit: strings.ToUpper(commitB)}); got != "zwei" {
		t.Fatalf("full commit, upper case: %q", got)
	}
	if got := titles(AnchorFilter{Commit: "ddddddd"}); got != "" {
		t.Fatalf("unknown commit must find nothing, got %q", got)
	}
	// Solange niemand die PR-Spalte füllt, findet der pr-Filter nichts.
	if got := titles(AnchorFilter{PR: 7}); got != "" {
		t.Fatalf("pr filter without any pr set: %q", got)
	}
	if _, err := st.db.Exec(`UPDATE git_anchors SET pr=7 WHERE commit_sha=?`, commitB); err != nil {
		t.Fatal(err)
	}
	if got := titles(AnchorFilter{PR: 7}); got != "zwei" {
		t.Fatalf("pr filter with pr set: %q", got)
	}
	if got := titles(AnchorFilter{Commit: commitA[:7], PR: 7}); got != "" {
		t.Fatalf("commit and pr combine with AND: %q", got)
	}
}

func TestAnchorFilterValidation(t *testing.T) {
	for _, f := range []AnchorFilter{{Commit: "abc"}, {Commit: "xyz1234"}, {PR: -1}} {
		if f.Valid() == nil {
			t.Errorf("%+v must be invalid", f)
		}
	}
	for _, f := range []AnchorFilter{{}, {Commit: "ABC1234"}, {PR: 5}} {
		if err := f.Valid(); err != nil {
			t.Errorf("%+v: %v", f, err)
		}
	}
}

func TestGhostDescriptionKeepsItsAnchorWhenReplaced(t *testing.T) {
	st := openAnchorStore(t)
	put := func(desc string, a *gitanchor.Anchor) {
		if _, err := st.PutGhostFile(GhostFile{Project: anchorTestProject, Path: "internal/x.go", Description: desc, Person: "robin", GitAnchor: a}); err != nil {
			t.Fatal(err)
		}
	}
	put("erste Fassung", anchorAt(commitA, "main"))
	put("zweite Fassung", anchorAt(commitB, "feat/b"))
	put("dritte ohne Anker", nil)
	chain, err := st.GhostFileChain(anchorTestProject, "internal/x.go", 0)
	if err != nil || len(chain) != 3 {
		t.Fatalf("chain = %d (%v)", len(chain), err)
	}
	got := map[string]string{}
	for _, v := range chain {
		c := ""
		if v.GitAnchor != nil {
			c = v.GitAnchor.Commit
		}
		got[v.Description] = c
	}
	want := map[string]string{"dritte ohne Anker": "", "zweite Fassung": commitB, "erste Fassung": commitA}
	for d, c := range want {
		if got[d] != c {
			t.Errorf("%q anchored at %q, want %q (all: %v)", d, got[d], c, got)
		}
	}
	put("vierte", anchorAt(commitB, "feat/b"))
	g, err := st.GhostFileByPath(anchorTestProject, "internal/x.go")
	if err != nil || g.GitAnchor == nil || g.GitAnchor.Commit != commitB {
		t.Fatalf("head anchor: %+v (%v)", g.GitAnchor, err)
	}
	hits, err := st.SearchGhostFilesWith("vierte", GhostSearch{Project: anchorTestProject, Anchor: AnchorFilter{Commit: commitB[:8]}})
	if err != nil || len(hits) != 1 {
		t.Fatalf("ghost commit filter: %d hits (%v)", len(hits), err)
	}
	hits, err = st.SearchGhostFilesWith("vierte", GhostSearch{Project: anchorTestProject, Anchor: AnchorFilter{Commit: commitA[:8]}})
	if err != nil || len(hits) != 0 {
		t.Fatalf("ghost commit filter must not match another commit: %d hits (%v)", len(hits), err)
	}
}

func TestRequestProgressCarriesAnAnchor(t *testing.T) {
	st := openAnchorStore(t)
	d, err := st.CreateRequest(requestdomain.CreateInput{
		Request:   requestdomain.Request{Type: "feature", Title: "anker", Scope: scope.Axes{Project: anchorTestProject}, Person: "robin"},
		Criteria:  []string{"eins", "zwei"},
		GitAnchor: anchorAt(commitA, "main"),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := d.Request.ID
	if err := st.SetCriterionState(d.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "test", Ref: "go test", Person: "robin", GitAnchor: anchorAt(commitB, "feat/b")}); err != nil {
		t.Fatal(err)
	}
	// Ein Nachweis ohne Anker ist erlaubt und bleibt ohne.
	if err := st.SetCriterionState(d.Criteria[1].ID, "waived", requestdomain.Evidence{Kind: "decision", Ref: "x", Person: "robin"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteRequest(id, requestdomain.Evidence{Kind: "commit", Ref: "abc", Person: "robin", GitAnchor: anchorAt(commitB, "feat/b")}); err != nil {
		t.Fatal(err)
	}
	got, err := st.RequestByID(id)
	if err != nil {
		t.Fatal(err)
	}
	commitOf := func(a *gitanchor.Anchor) string {
		if a == nil {
			return "-"
		}
		return a.Commit[:7]
	}
	if c := commitOf(got.Criteria[0].Evidence[0].GitAnchor); c != "bbbbbbb" {
		t.Errorf("evidence anchor = %s", c)
	}
	if c := commitOf(got.Criteria[1].Evidence[0].GitAnchor); c != "-" {
		t.Errorf("unanchored evidence got %s", c)
	}
	var kinds []string
	for _, a := range got.Activity {
		kinds = append(kinds, a.Kind+"="+commitOf(a.GitAnchor))
	}
	want := "request.created=aaaaaaa,criterion.met=bbbbbbb,criterion.waived=-,request.done=bbbbbbb"
	if strings.Join(kinds, ",") != want {
		t.Errorf("activity anchors:\n got %s\nwant %s", strings.Join(kinds, ","), want)
	}
	// Der Request-Filter findet Aufträge mit einem Ereignis auf diesem Stand.
	page, err := st.SearchRequestsWith(requestdomain.SearchFilter{Scope: scope.Axes{Project: anchorTestProject}, Commit: commitA[:7]}, SearchAuto)
	if err != nil || len(page.Results) != 1 {
		t.Fatalf("request commit filter: %d (%v)", len(page.Results), err)
	}
	page, err = st.SearchRequestsWith(requestdomain.SearchFilter{Scope: scope.Axes{Project: anchorTestProject}, Commit: "ddddddd"}, SearchAuto)
	if err != nil || len(page.Results) != 0 {
		t.Fatalf("request commit filter, other commit: %d (%v)", len(page.Results), err)
	}
}

func TestPainEventsCarryAnAnchor(t *testing.T) {
	st := openAnchorStore(t)
	pa := st.Access(Principal{ID: "person:1", Label: "robin"})
	d, err := pa.CreatePain(CreatePainInput{Project: anchorTestProject, Title: "Schmerz", AuthorID: 1,
		Actor: pain.Actor{Person: "robin", GitAnchor: anchorAt(commitA, "main")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pa.SetPainState(d.Pain.ID, pain.Mitigated, "Rest", pain.Evidence{Kind: "commit", Ref: "abc"}, "",
		pain.Actor{Person: "robin", GitAnchor: anchorAt(commitB, "feat/b")}); err != nil {
		t.Fatal(err)
	}
	got, err := pa.PainDetailView(d.Pain.ID)
	if err != nil {
		t.Fatal(err)
	}
	var commits []string
	for _, e := range got.Events {
		if e.GitAnchor == nil {
			commits = append(commits, e.Kind+"=-")
			continue
		}
		commits = append(commits, e.Kind+"="+e.GitAnchor.Commit[:7])
	}
	if want := "created=aaaaaaa,state=bbbbbbb"; strings.Join(commits, ",") != want {
		t.Fatalf("pain events %v, want %s", commits, want)
	}
}

func TestRoomMessagesCarryAnAnchor(t *testing.T) {
	st := openAnchorStore(t)
	room := RoomKeyForProject(anchorTestProject)
	if _, err := st.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room,
		SenderExternalID: "sess-a", ClientID: "c-1", Body: "mit", GitAnchor: anchorAt(commitA, "main")}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room,
		SenderExternalID: "sess-a", ClientID: "c-2", Body: "ohne"}); err != nil {
		t.Fatal(err)
	}
	msgs, err := st.CoordMessagesSince(DestinationRoom, room, 0, 10)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("messages = %d (%v)", len(msgs), err)
	}
	if msgs[0].GitAnchor == nil || msgs[0].GitAnchor.Commit != commitA || msgs[1].GitAnchor != nil {
		t.Fatalf("anchors: %+v / %+v", msgs[0].GitAnchor, msgs[1].GitAnchor)
	}
}

// Eine Datenbank von vor REQ-597 bekommt Tabelle und Spalten beim Öffnen, und
// ein zweites Öffnen ändert nichts.
func TestOpeningALegacyDatabaseAddsTheAnchorColumns(t *testing.T) {
	path := t.TempDir() + "/legacy.db"
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range anchoredTables {
		if _, err := st.db.Exec(`DROP INDEX IF EXISTS ` + table + `_anchor`); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`ALTER TABLE ` + table + ` DROP COLUMN anchor_id`); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
	}
	if _, err := st.db.Exec(`DROP TABLE git_anchors`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		st, err = Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		id, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "t", Body: "x", Person: "robin", GitAnchor: anchorAt(commitA, "main")})
		if err != nil {
			t.Fatal(err)
		}
		if k, _ := st.KnowledgeByID(id); k.GitAnchor == nil || k.GitAnchor.Commit != commitA {
			t.Fatalf("open %d: anchor %+v", i, k.GitAnchor)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
