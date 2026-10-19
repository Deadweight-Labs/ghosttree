package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/garden"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func idStr(id int64) string { return strconv.FormatInt(id, 10) }

// gardenEntry files an entry of the project the way a person or an agent would.
func gardenEntry(t *testing.T, st *store.Store, project, typ, title, confidence string) int64 {
	t.Helper()
	id, err := st.InsertKnowledge(store.Knowledge{Type: typ, Title: title, Body: "Der Dienst läuft auf dem Server " + title,
		Scope: scope.Axes{Project: project}, Person: "alice", Confidence: confidence})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// gardenDuplicateNote files the note the gardener writes for entries that say the same.
func gardenDuplicateNote(t *testing.T, st *store.Store, project string, ids ...int64) int64 {
	t.Helper()
	titles := map[int64]string{}
	quotes := map[int64]string{}
	for _, id := range ids {
		k, err := st.KnowledgeByID(id)
		if err != nil {
			t.Fatal(err)
		}
		titles[id], quotes[id] = k.Title, "Der Dienst läuft auf dem Server"
	}
	note := garden.RelationProposal(project, garden.Relation{Kind: garden.Duplicate, IDs: ids, Reason: "Beide sagen dasselbe.", Quotes: quotes}, titles)
	note.Person = "gardener"
	got, err := st.InsertKnowledge(note)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// gardenCoverNote files the note the gardener writes for the regression question.
func gardenCoverNote(t *testing.T, st *store.Store, project string, pitfall int64, c garden.Cover) int64 {
	t.Helper()
	k, err := st.KnowledgeByID(pitfall)
	if err != nil {
		t.Fatal(err)
	}
	c.Entry = garden.Entry{ID: pitfall, Title: k.Title}
	note := garden.CoverProposal(project, c)
	note.Person = "gardener"
	got, err := st.InsertKnowledge(note)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func postGardenAction(t *testing.T, e shellEnv, c *http.Client, path string) int {
	t.Helper()
	resp := sameOriginPostForm(t, c, e.Base+path, url.Values{"csrf_token": {renderedCSRFToken(t, c, e.Base+"/ui/review")}})
	resp.Body.Close()
	return resp.StatusCode
}

// The web package reads the session references the gardener writes without
// importing it; this keeps the two in step.
func TestGardenRefsMatchWhatTheWebReads(t *testing.T) {
	rel := garden.Relation{Kind: garden.Duplicate, IDs: []int64{12, 15}}
	kind, ids, ok := parseRelationRef(garden.RelationRef(rel))
	if !ok || kind != gardenKindDuplicate || len(ids) != 2 || ids[0] != 12 || ids[1] != 15 {
		t.Errorf("relation ref %q read as %v %v %v", garden.RelationRef(rel), kind, ids, ok)
	}
	for _, c := range []struct{ kind, want string }{{garden.CoverExisting, gardenKindExisting}, {garden.CoverSketch, gardenKindSketch}, {garden.CoverUntestable, gardenKindUntestable}} {
		id, kind, ok := parseRegressionRef(garden.CoverRef(77, c.kind))
		if !ok || id != 77 || kind != c.want {
			t.Errorf("cover ref %q read as %v %v %v", garden.CoverRef(77, c.kind), id, kind, ok)
		}
	}
	if _, _, ok := parseRelationRef("gardener:check-knowledge:12"); ok {
		t.Error("a stale note read as a relation")
	}
	if _, _, ok := parseRegressionRef("gardener:relation:duplicate:1-2"); ok {
		t.Error("a relation read as a regression note")
	}
	sketch := garden.CoverProposal("github.com/o/r", garden.Cover{Entry: garden.Entry{ID: 3, Title: "t"}, Kind: garden.CoverSketch,
		File: "internal/x/x_test.go", Test: "TestX", Steps: "lege an\nrufe auf\nprüfe", Reason: "weil"})
	if got := gardenNoteLine(sketch.Body, "Test"); !strings.HasPrefix(got, "internal/x/x_test.go:TestX") {
		t.Errorf("test line = %q", got)
	}
	if got := gardenSketchLines(sketch.Body); len(got) != 3 || got[1] != "rufe auf" {
		t.Errorf("sketch lines = %q", got)
	}
	existing := garden.CoverProposal("github.com/o/r", garden.Cover{Entry: garden.Entry{ID: 3, Title: "t"}, Kind: garden.CoverExisting, File: "a/b_test.go", Test: "TestB"})
	if got := gardenNoteLine(existing.Body, "Test"); got != "a/b_test.go:TestB" {
		t.Errorf("test line = %q", got)
	}
}

func TestDuplicateNoteOffersMergeOnlyToWhoMayDoWhatItDoes(t *testing.T) {
	e := ovEnv(t)
	older := gardenEntry(t, e.St, shellProject, "pitfall", "Dienst alt", "trusted")
	newer := gardenEntry(t, e.St, shellProject, "pitfall", "Dienst neu", "trusted")
	note := gardenDuplicateNote(t, e.St, shellProject, older, newer)

	_, lead := fetchPage(t, e.Lead, e.Base+"/ui/review")
	form := `action="/ui/review/` + idStr(note) + `/merge"`
	if !strings.Contains(lead, form) {
		t.Fatalf("a lead is not offered to merge:\n%s", lead)
	}
	for _, want := range []string{"Duplicate?", "Merge", "Stays:", "#" + idStr(newer) + " Dienst neu", "Replaced:", "#" + idStr(older) + " Dienst alt"} {
		if !strings.Contains(lead, want) {
			t.Errorf("the card lacks %q", want)
		}
	}
	// Merging is the one way to act on it, so plain approval is not offered.
	if strings.Contains(lead, `/review/`+idStr(note)+`/approve`) {
		t.Error("a duplicate note offers plain approval next to merging")
	}
	if !strings.Contains(lead, `/review/`+idStr(note)+`/reject`) {
		t.Error("a lead cannot reject the duplicate note")
	}
	// Superseding needs the right to change every entry; the reviewer holds the
	// verify right only, a member neither.
	for name, c := range map[string]*http.Client{"reviewer": e.Reviewer, "member": e.Member} {
		_, page := fetchPage(t, c, e.Base+"/ui/review")
		if strings.Contains(page, form) {
			t.Errorf("a %s is offered to merge", name)
		}
	}
	// Nor does the page offer to approve the whole bundle: merging is not a bulk action.
	if strings.Contains(lead, "Approve all") {
		t.Errorf("the bundle button covers a note that is merged, not approved:\n%s", lead)
	}
}

func TestMergeSupersedesTheOlderEntryByTheNewerAndClosesTheNote(t *testing.T) {
	e := ovEnv(t)
	older := gardenEntry(t, e.St, shellProject, "pitfall", "Dienst alt", "trusted")
	newer := gardenEntry(t, e.St, shellProject, "pitfall", "Dienst neu", "verified")
	note := gardenDuplicateNote(t, e.St, shellProject, older, newer)

	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/merge"); code != http.StatusSeeOther {
		t.Fatalf("merge = %d", code)
	}
	if got := entryState(t, e.St, older); got != "trusted/superseded" {
		t.Errorf("older entry = %s, want it superseded", got)
	}
	if k, _ := e.St.KnowledgeByID(older); k.SupersededBy != newer {
		t.Errorf("the older entry is superseded by #%d, want #%d", k.SupersededBy, newer)
	}
	if got := entryState(t, e.St, newer); got != "verified/active" {
		t.Errorf("the newer entry changed: %s", got)
	}
	rels, err := e.St.RelationsOf(older)
	if err != nil || len(rels) != 1 {
		t.Fatalf("relations of the older entry: %v %v", rels, err)
	}
	if r := rels[0]; r.Kind != store.RelSupersedes || r.FromID != newer || r.ToID != older ||
		r.Reason != "Gärtner-Vorschlag #"+idStr(note)+", bestätigt von lars" {
		t.Errorf("supersede relation = %+v", r)
	}
	// The note is closed as confirmed by the person, which is what leaves the queue.
	if k, _ := e.St.KnowledgeByID(note); k.Confidence != "verified" || k.ConfirmedBy != "lars" || k.Status != "active" {
		t.Errorf("note after the merge = %+v", k)
	}
	_, page := fetchPage(t, e.Lead, e.Base+"/ui/review?done=merged&k="+idStr(newer)+"&source=gardener")
	if !strings.Contains(page, "Merged: Dienst neu now replaces the older entry") {
		t.Errorf("no result line: %s", page)
	}
	if strings.Contains(page, `/review/`+idStr(note)+`/merge`) {
		t.Error("the merged note is still offered")
	}
	// Twice is a conflict, not a second merge.
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/merge"); code != http.StatusConflict {
		t.Errorf("second merge = %d, want 409", code)
	}
}

func TestMergeKeepsTheNewestOfSeveralDuplicates(t *testing.T) {
	e := ovEnv(t)
	a := gardenEntry(t, e.St, shellProject, "pitfall", "Eins", "trusted")
	b := gardenEntry(t, e.St, shellProject, "pitfall", "Zwei", "trusted")
	c := gardenEntry(t, e.St, shellProject, "pitfall", "Drei", "trusted")
	note := gardenDuplicateNote(t, e.St, shellProject, b, a, c)
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/merge"); code != http.StatusSeeOther {
		t.Fatalf("merge = %d", code)
	}
	for id, want := range map[int64]string{a: "trusted/superseded", b: "trusted/superseded", c: "trusted/active"} {
		if got := entryState(t, e.St, id); got != want {
			t.Errorf("#%d = %s, want %s", id, got, want)
		}
	}
}

func TestMergeNeedsEveryRightOfSupersedeAndAPerson(t *testing.T) {
	e := ovEnv(t)
	older := gardenEntry(t, e.St, shellProject, "pitfall", "Dienst alt", "trusted")
	newer := gardenEntry(t, e.St, shellProject, "pitfall", "Dienst neu", "trusted")
	note := gardenDuplicateNote(t, e.St, shellProject, older, newer)
	if _, err := e.St.AddPerson("olga"); err != nil {
		t.Fatal(err)
	}
	stranger := loginInteractive(t, e.Srv, e.St, "olga")
	path := "/ui/review/" + idStr(note) + "/merge"
	unchanged := func(who string) {
		t.Helper()
		if got := entryState(t, e.St, older); got != "trusted/active" {
			t.Errorf("%s: the older entry changed: %s", who, got)
		}
		if got := entryState(t, e.St, note); got != "staged/active" {
			t.Errorf("%s: the note changed: %s", who, got)
		}
	}
	// A guest does not see a staged note at all, a stranger does not see the
	// project: both answer as for an unknown number. A member and a reviewer see
	// it and may not change the entries.
	for who, want := range map[string]int{"guest": http.StatusNotFound, "stranger": http.StatusNotFound, "member": http.StatusForbidden, "reviewer": http.StatusForbidden} {
		c := map[string]*http.Client{"guest": e.Guest, "stranger": stranger, "member": e.Member, "reviewer": e.Reviewer}[who]
		if code := postGardenAction(t, e, c, path); code != want {
			t.Errorf("%s merging = %d, want %d", who, code, want)
		}
		unchanged(who)
	}
	// Without the form's token nothing happens, whoever asks.
	resp := sameOriginPostForm(t, e.Lead, e.Base+path, url.Values{})
	resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther || resp.StatusCode == http.StatusOK {
		t.Errorf("merge without a token = %d", resp.StatusCode)
	}
	unchanged("lead without token")
	// The plain verdict route does not do it either.
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/approve"); code != http.StatusSeeOther {
		t.Fatalf("approve = %d", code)
	}
	if got := entryState(t, e.St, older); got != "trusted/active" {
		t.Errorf("approving the note changed the entry: %s", got)
	}
}

func TestMergeRefusesWhatCannotBeSuperseded(t *testing.T) {
	e := ovEnv(t)
	post := func(note int64) int {
		return postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/merge")
	}
	// The newer entry is not delivered yet (staged): it cannot take the place of the older.
	older := gardenEntry(t, e.St, shellProject, "pitfall", "Alt", "trusted")
	staged := gardenEntry(t, e.St, shellProject, "pitfall", "Neu, ungeprüft", "staged")
	note := gardenDuplicateNote(t, e.St, shellProject, older, staged)
	if code := post(note); code != http.StatusConflict {
		t.Errorf("a staged replacement = %d, want 409", code)
	}
	if _, page := fetchPage(t, e.Lead, e.Base+"/ui/review"); strings.Contains(page, `/review/`+idStr(note)+`/merge`) {
		t.Error("a merge that cannot work is offered")
	}
	// Another scope: the replacement must live exactly where the old one lives.
	other, err := e.St.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "Anderer Zweig", Body: "b",
		Scope: scope.Axes{Project: shellProject, Branch: "feature"}, Person: "alice", Confidence: "trusted"})
	if err != nil {
		t.Fatal(err)
	}
	note = gardenDuplicateNote(t, e.St, shellProject, older, other)
	if code := post(note); code != http.StatusConflict {
		t.Errorf("entries of different scope = %d, want 409", code)
	}
	// An entry that is gone from circulation meanwhile.
	gone := gardenEntry(t, e.St, shellProject, "pitfall", "Schon weg", "trusted")
	live := gardenEntry(t, e.St, shellProject, "pitfall", "Noch da", "trusted")
	note = gardenDuplicateNote(t, e.St, shellProject, gone, live)
	if err := e.St.UpdateKnowledgeBy(gone, map[string]string{"status": "deprecated"}, "lars"); err != nil {
		t.Fatal(err)
	}
	if code := post(note); code != http.StatusConflict {
		t.Errorf("a retired entry = %d, want 409", code)
	}
	// A note of another kind has no merge.
	stale := gardenNote(t, e.St, shellProject, 99, "Veraltet?", "Egal", "weg")
	if code := post(stale); code != http.StatusConflict {
		t.Errorf("a stale note merged = %d, want 409", code)
	}
	for _, id := range []int64{older, staged, other, live} {
		if got := entryState(t, e.St, id); strings.HasSuffix(got, "superseded") {
			t.Errorf("#%d was superseded by a refused merge", id)
		}
	}
}

func TestRegressionNoteRecordsTheVerdictOnlyOnConfirmation(t *testing.T) {
	e := ovEnv(t)
	covered := gardenEntry(t, e.St, shellProject, "pitfall", "Pitfall mit Test", "trusted")
	tool := gardenEntry(t, e.St, shellProject, "pitfall", "Pitfall über ein Werkzeug", "trusted")
	missing := gardenEntry(t, e.St, shellProject, "pitfall", "Pitfall ohne Test", "trusted")
	existing := gardenCoverNote(t, e.St, shellProject, covered, garden.Cover{Kind: garden.CoverExisting, File: "internal/store/x_test.go", Test: "TestHoldsX", Reason: "Er prüft genau das."})
	untestable := gardenCoverNote(t, e.St, shellProject, tool, garden.Cover{Kind: garden.CoverUntestable, Reason: "Eine Eigenschaft eines Werkzeugs."})
	sketch := gardenCoverNote(t, e.St, shellProject, missing, garden.Cover{Kind: garden.CoverSketch, File: "internal/store/y_test.go", Test: "TestNewY", Steps: "lege einen Eintrag an\nrufe die Funktion\nprüfe das Ergebnis"})

	// Filing the notes judged nothing.
	for _, id := range []int64{covered, tool, missing} {
		if k, _ := e.St.KnowledgeByID(id); k.RegressionState != "" {
			t.Fatalf("#%d was judged before a person confirmed: %q", id, k.RegressionState)
		}
	}
	_, page := fetchPage(t, e.Lead, e.Base+"/ui/review")
	for _, want := range []string{
		`action="/ui/review/` + idStr(existing) + `/cover"`, "Record as covered", "internal/store/x_test.go:TestHoldsX",
		`action="/ui/review/` + idStr(untestable) + `/cover"`, "Record as not testable",
		"Test exists?", "Not testable?", "Test missing?", "internal/store/y_test.go:TestNewY", "rufe die Funktion",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// A sketch records nothing: it only offers the plain verdicts.
	if strings.Contains(page, `/review/`+idStr(sketch)+`/cover`) || !strings.Contains(page, `/review/`+idStr(sketch)+`/approve`) {
		t.Error("a sketch offers a verdict it cannot give, or no plain approval")
	}
	if strings.Contains(page, `/review/`+idStr(existing)+`/approve`) {
		t.Error("a note that records a verdict offers plain approval")
	}
	// The gardener's bundle approval covers the sketch only.
	if !strings.Contains(page, "Approve all 1") {
		t.Errorf("bulk approval should cover the sketch alone:\n%s", page)
	}

	// A member may not judge; a reviewer may.
	if code := postGardenAction(t, e, e.Member, "/ui/review/"+idStr(existing)+"/cover"); code != http.StatusForbidden {
		t.Errorf("member = %d, want 403", code)
	}
	if k, _ := e.St.KnowledgeByID(covered); k.RegressionState != "" {
		t.Error("a member judged the pitfall")
	}
	if code := postGardenAction(t, e, e.Reviewer, "/ui/review/"+idStr(existing)+"/cover"); code != http.StatusSeeOther {
		t.Fatalf("reviewer = %d", code)
	}
	if k, _ := e.St.KnowledgeByID(covered); k.RegressionState != "covered" || k.RegressionTest != "internal/store/x_test.go:TestHoldsX" {
		t.Errorf("pitfall after confirming = %q %q", k.RegressionState, k.RegressionTest)
	}
	if got := entryState(t, e.St, existing); got != "verified/active" {
		t.Errorf("the note after confirming = %s", got)
	}
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(untestable)+"/cover"); code != http.StatusSeeOther {
		t.Fatalf("lead = %d", code)
	}
	if k, _ := e.St.KnowledgeByID(tool); k.RegressionState != "not_applicable" || k.RegressionTest != "" {
		t.Errorf("pitfall after confirming = %q %q", k.RegressionState, k.RegressionTest)
	}
	_, page = fetchPage(t, e.Lead, e.Base+"/ui/review?done=covered&k="+idStr(covered)+"&source=gardener")
	if !strings.Contains(page, "Recorded as covered: Pitfall mit Test") {
		t.Errorf("no result line: %s", page)
	}
	// Judged already: the note is out of date and cannot overwrite the verdict.
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(existing)+"/cover"); code != http.StatusConflict {
		t.Errorf("second confirmation = %d, want 409", code)
	}
	// The sketch route does not exist for a sketch, and the verdict is not touched.
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(sketch)+"/cover"); code != http.StatusConflict {
		t.Errorf("a sketch recorded as a verdict = %d, want 409", code)
	}
	if k, _ := e.St.KnowledgeByID(missing); k.RegressionState != "" {
		t.Errorf("the sketched pitfall was judged: %q", k.RegressionState)
	}
}

func TestRegressionNoteDoesNotOverwriteAVerdictSomebodyGaveMeanwhile(t *testing.T) {
	e := ovEnv(t)
	p := gardenEntry(t, e.St, shellProject, "pitfall", "Pitfall", "trusted")
	note := gardenCoverNote(t, e.St, shellProject, p, garden.Cover{Kind: garden.CoverExisting, File: "a/b_test.go", Test: "TestB"})
	if err := e.St.SetRegressionCover(p, "uncovered", ""); err != nil {
		t.Fatal(err)
	}
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/cover"); code != http.StatusConflict {
		t.Fatalf("= %d, want 409", code)
	}
	if k, _ := e.St.KnowledgeByID(p); k.RegressionState != "uncovered" {
		t.Errorf("verdict = %q, a person's verdict was overwritten", k.RegressionState)
	}
}

// A note about a project the viewer cannot read never reaches the page or an action.
func TestGardenActionsStayInsideTheViewersProjects(t *testing.T) {
	e := ovEnv(t)
	older := gardenEntry(t, e.St, shellHiddenProject, "pitfall", "SECRET-ALT", "trusted")
	newer := gardenEntry(t, e.St, shellHiddenProject, "pitfall", "SECRET-NEU", "trusted")
	note := gardenDuplicateNote(t, e.St, shellHiddenProject, older, newer)
	_, page := fetchPage(t, e.Lead, e.Base+"/ui/review")
	if strings.Contains(page, "SECRET") {
		t.Error("a hidden note is on the page")
	}
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/merge"); code != http.StatusNotFound {
		t.Errorf("merging a hidden note = %d, want 404", code)
	}
	if got := entryState(t, e.St, older); got != "trusted/active" {
		t.Errorf("a hidden entry changed: %s", got)
	}
}
