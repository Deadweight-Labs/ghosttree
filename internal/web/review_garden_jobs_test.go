package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/garden"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-457, jobs 4 and 1: the two notes of the gardener that change something when
// a person confirms them, a criterion that is met and a description. Everything
// they act on is read from the server's own data: the number of the request and
// criterion, or the path, from the reference of a note only the gardener's
// account wrote and nobody edited; the proof, text and hashes from that note; the
// rights from the viewer. Nothing the browser sends takes part.

const proofCommitRef = "4584fb7a1c2d (#149)"

type jobRequest struct {
	req          int64
	crit, number [2]int64
	title        string
}

func newJobRequest(t *testing.T, e shellEnv, project string) jobRequest {
	t.Helper()
	d, err := e.St.CreateRequest(requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: "Der Gärtner pflegt das Ledger", Scope: scope.Axes{Project: project}, Person: "alice"},
		Criteria: []string{"Erfüllte Kriterien werden mit Nachweis vorgeschlagen", "Ein zweites Kriterium"},
	})
	if err != nil {
		t.Fatal(err)
	}
	j := jobRequest{req: d.Request.ID, title: d.Request.Title}
	for i := 0; i < 2; i++ {
		j.crit[i], j.number[i] = d.Criteria[i].ID, int64(d.Criteria[i].Number)
	}
	return j
}

func (j jobRequest) proof(items ...garden.ProofItem) garden.Proof {
	if len(items) == 0 {
		items = []garden.ProofItem{{Kind: garden.ProofCommit, Ref: proofCommitRef, Detail: "2026-10-19 feat: x (#149)"}}
	}
	return garden.Proof{Request: garden.LedgerRequest{ID: j.req, Title: j.title},
		Criterion: garden.LedgerCriterion{ID: j.crit[0], Number: int(j.number[0]), Description: "Erfüllte Kriterien werden mit Nachweis vorgeschlagen"},
		Items:     items, Reason: "Der Commit führt es ein."}
}

// criterionNote files the note the gardener writes for a proof, as its account.
func criterionNote(t *testing.T, e shellEnv, project string, p garden.Proof, edit func(*store.Knowledge)) int64 {
	t.Helper()
	n := garden.CriterionProposal(project, p)
	if edit != nil {
		edit(&n)
	}
	return gardenerWrote(t, e.St, n, e.St.GardenerAccountID())
}

func criterionOf(t *testing.T, st *store.Store, j jobRequest, i int) requestdomain.Criterion {
	t.Helper()
	d, err := st.RequestByID(j.req)
	if err != nil {
		t.Fatal(err)
	}
	return d.Criteria[i]
}

func postWith(t *testing.T, e shellEnv, c *http.Client, path string, extra url.Values) int {
	t.Helper()
	form := url.Values{"csrf_token": {renderedCSRFToken(t, c, e.Base+"/ui/review")}}
	for k, v := range extra {
		form[k] = v
	}
	resp := sameOriginPostForm(t, c, e.Base+path, form)
	resp.Body.Close()
	return resp.StatusCode
}

func TestCriterionNoteRecordsTheProofOnlyWhenAPersonConfirmsIt(t *testing.T) {
	e, _ := gardenActEnv(t)
	j := newJobRequest(t, e, shellProject)
	note := criterionNote(t, e, shellProject, j.proof(garden.ProofItem{Kind: garden.ProofCommit, Ref: proofCommitRef}, garden.ProofItem{Kind: garden.ProofTest, Ref: "cmd/ctx/garden_test.go:TestX"}), nil)

	// Filing the note settled nothing.
	if c := criterionOf(t, e.St, j, 0); c.State != "open" || len(c.Evidence) != 0 {
		t.Fatalf("criterion before a person confirmed: %+v", c)
	}
	_, page := fetchPage(t, e.Lead, e.Base+"/ui/review")
	for _, want := range []string{
		`action="/ui/review/` + idStr(note) + `/criterion"`, "Record criterion as met", "Met?",
		"Der Gärtner pflegt das Ledger", `href="/ui/requests/` + idStr(j.req) + `"`,
		"Criterion 1: Erfüllte Kriterien werden mit Nachweis vorgeschlagen",
		"Proof (commit): " + proofCommitRef, "Proof (test): cmd/ctx/garden_test.go:TestX",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the card lacks %q:\n%s", want, page)
		}
	}
	// It is the one way to act on it: no plain approval, no bundle approval.
	if strings.Contains(page, `/review/`+idStr(note)+`/approve`) || strings.Contains(page, "Approve all") {
		t.Error("a criterion note offers plain approval")
	}

	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/criterion"); code != http.StatusSeeOther {
		t.Fatalf("confirming = %d", code)
	}
	c := criterionOf(t, e.St, j, 0)
	if c.State != "met" || len(c.Evidence) != 1 {
		t.Fatalf("criterion = %+v", c)
	}
	ev := c.Evidence[0]
	if ev.Kind != "commit" || ev.Person != "lars" ||
		ev.Ref != proofCommitRef+"; test cmd/ctx/garden_test.go:TestX (Gärtner-Vorschlag #"+idStr(note)+", bestätigt von lars)" {
		t.Errorf("evidence = %+v", ev)
	}
	// Only the criterion: the request stays open, the other criterion too, and the note is closed as confirmed.
	d, _ := e.St.RequestByID(j.req)
	if d.Request.State != "open" || d.Criteria[1].State != "open" {
		t.Errorf("request %s, second criterion %s: the gardener's note closed more than its criterion", d.Request.State, d.Criteria[1].State)
	}
	if k, _ := e.St.KnowledgeByID(note); k.Confidence != "verified" || k.ConfirmedBy != "lars" {
		t.Errorf("note after confirming = %+v", k)
	}
	_, page = fetchPage(t, e.Lead, e.Base+"/ui/review?done=criterion&k="+idStr(note)+"&source=gardener")
	if !strings.Contains(page, "Criterion recorded as met") {
		t.Errorf("no result line:\n%s", page)
	}
	// Twice is a conflict, not a second evidence.
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/criterion"); code != http.StatusConflict {
		t.Errorf("second confirmation = %d, want 409", code)
	}
	if c := criterionOf(t, e.St, j, 0); len(c.Evidence) != 1 {
		t.Errorf("%d evidence rows after the second press", len(c.Evidence))
	}
}

// Lead, owner, reviewer, member, guest and a stranger. The right is that of working
// on the request, and that of verifying the note, as for every action of the gardener.
func TestCriterionActionIsOfferedAndTakenByThoseWhoMayDoWhatItDoes(t *testing.T) {
	e, _ := gardenActEnv(t)
	if _, err := e.St.AddPerson("sven"); err != nil {
		t.Fatal(err)
	}
	stranger := loginInteractive(t, e.Srv, e.St, "sven")
	cases := []struct {
		who     string
		c       *http.Client
		offered bool
		code    int
	}{
		{"lead", e.Lead, true, http.StatusSeeOther},
		{"owner", e.Owner, true, http.StatusSeeOther},
		{"reviewer", e.Reviewer, true, http.StatusSeeOther},
		{"member", e.Member, false, http.StatusForbidden},
		{"guest", e.Guest, false, http.StatusNotFound},
		{"stranger", stranger, false, http.StatusNotFound},
	}
	for _, tc := range cases {
		j := newJobRequest(t, e, shellProject)
		note := criterionNote(t, e, shellProject, j.proof(), nil)
		_, page := fetchPage(t, tc.c, e.Base+"/ui/review")
		if got := strings.Contains(page, `action="/ui/review/`+idStr(note)+`/criterion"`); got != tc.offered {
			t.Errorf("%s: the button is offered=%v, want %v", tc.who, got, tc.offered)
		}
		if code := postGardenAction(t, e, tc.c, "/ui/review/"+idStr(note)+"/criterion"); code != tc.code {
			t.Errorf("%s: confirming = %d, want %d", tc.who, code, tc.code)
		}
		wantState := "open"
		if tc.code == http.StatusSeeOther {
			wantState = "met"
		}
		if got := criterionOf(t, e.St, j, 0).State; got != wantState {
			t.Errorf("%s: criterion is %s, want %s", tc.who, got, wantState)
		}
		if tc.code != http.StatusSeeOther {
			if got := entryState(t, e.St, note); got != "staged/active" {
				t.Errorf("%s: the note changed: %s", tc.who, got)
			}
		}
	}
}

// What takes part is the server's data, not the form: whatever the request carries
// besides its token changes nothing.
func TestCriterionActionIgnoresEveryValueOfTheForm(t *testing.T) {
	e, _ := gardenActEnv(t)
	j := newJobRequest(t, e, shellProject)
	other := newJobRequest(t, e, shellProject)
	note := criterionNote(t, e, shellProject, j.proof(), nil)
	code := postWith(t, e, e.Lead, "/ui/review/"+idStr(note)+"/criterion", url.Values{
		"evidence_ref": {"gefälscht"}, "evidence_kind": {"url"}, "state": {"waived"}, "request": {idStr(other.req)},
		"criterion": {idStr(other.crit[0])}, "id": {idStr(other.crit[1])}, "cid": {idStr(other.crit[1])}, "ref": {"4584fb7a1c2d"}, "person": {"alice"},
	})
	if code != http.StatusSeeOther {
		t.Fatalf("= %d", code)
	}
	c := criterionOf(t, e.St, j, 0)
	if c.State != "met" || len(c.Evidence) != 1 || c.Evidence[0].Kind != "commit" || c.Evidence[0].Person != "lars" || strings.Contains(c.Evidence[0].Ref, "gefälscht") {
		t.Errorf("criterion = %+v", c)
	}
	for _, o := range []jobRequest{other} {
		for i := 0; i < 2; i++ {
			if c := criterionOf(t, e.St, o, i); c.State != "open" || len(c.Evidence) != 0 {
				t.Errorf("another request's criterion %d changed: %+v", i, c)
			}
		}
	}
	if d, _ := e.St.RequestByID(j.req); d.Criteria[1].State != "open" {
		t.Error("the other criterion of the same request changed")
	}
}

// A note that only looks like the gardener's: nothing is offered, 403, nothing recorded.
func TestCriterionNoteThatOnlyLooksLikeTheGardenersIsNotActedOn(t *testing.T) {
	e, gardener := gardenActEnv(t)
	other := newJobRequest(t, e, shellProject)
	cases := []struct {
		name string
		file func(j jobRequest) int64
	}{
		{"written by the account of a lead", func(j jobRequest) int64 {
			n := garden.CriterionProposal(shellProject, j.proof())
			return gardenerWrote(t, e.St, n, 4)
		}},
		{"written by the account of the owner", func(j jobRequest) int64 {
			return gardenerWrote(t, e.St, garden.CriterionProposal(shellProject, j.proof()), 1)
		}},
		{"written by no account", func(j jobRequest) int64 {
			return gardenerWrote(t, e.St, garden.CriterionProposal(shellProject, j.proof()), 999)
		}},
		{"a title that names another request", func(j jobRequest) int64 {
			return criterionNote(t, e, shellProject, j.proof(), func(n *store.Knowledge) {
				n.Title = strings.Replace(n.Title, "#"+idStr(j.req)+" ", "#"+idStr(other.req)+" ", 1)
			})
		}},
		{"a title that names another criterion number", func(j jobRequest) int64 {
			return criterionNote(t, e, shellProject, j.proof(), func(n *store.Knowledge) { n.Title = strings.Replace(n.Title, "Kriterium 1:", "Kriterium 2:", 1) })
		}},
		{"a reference that names a criterion of another request", func(j jobRequest) int64 {
			return criterionNote(t, e, shellProject, j.proof(), func(n *store.Knowledge) { n.SessionRef = garden.CriterionRef(j.req, other.crit[0]) })
		}},
		{"a proof of a shape no note has", func(j jobRequest) int64 {
			return criterionNote(t, e, shellProject, j.proof(garden.ProofItem{Kind: garden.ProofFile, Ref: "../../etc/passwd"}), nil)
		}},
		{"a proof of a kind no note has", func(j jobRequest) int64 {
			return criterionNote(t, e, shellProject, j.proof(), func(n *store.Knowledge) {
				n.Body = strings.Replace(n.Body, "Beleg: commit ", "Beleg: url https://example.invalid/", 1)
			})
		}},
		{"no proof", func(j jobRequest) int64 {
			return criterionNote(t, e, shellProject, j.proof(), func(n *store.Knowledge) { n.Body = strings.ReplaceAll(n.Body, "Beleg: ", "Hinweis: ") })
		}},
	}
	for _, tc := range cases {
		j := newJobRequest(t, e, shellProject)
		note := tc.file(j)
		_, page := fetchPage(t, e.Lead, e.Base+"/ui/review")
		if strings.Contains(page, `action="/ui/review/`+idStr(note)+`/criterion"`) {
			t.Errorf("%s: the card offers the action", tc.name)
		}
		if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/criterion"); code != http.StatusForbidden {
			t.Errorf("%s: = %d, want 403", tc.name, code)
		}
		if c := criterionOf(t, e.St, j, 0); c.State != "open" || len(c.Evidence) != 0 {
			t.Errorf("%s: the criterion changed: %+v", tc.name, c)
		}
		if got := entryState(t, e.St, note); got != "staged/active" {
			t.Errorf("%s: the note changed: %s", tc.name, got)
		}
	}
	// Edited after the gardener filed it.
	j := newJobRequest(t, e, shellProject)
	note := criterionNote(t, e, shellProject, j.proof(), nil)
	n, _ := e.St.KnowledgeByID(note)
	if err := e.St.UpdateKnowledgeBy(note, map[string]string{"body": strings.Replace(n.Body, "4584fb7a1c2d", "aaaaaaaaaaaa", 1)}, "lars"); err != nil {
		t.Fatal(err)
	}
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/criterion"); code != http.StatusForbidden {
		t.Errorf("an edited note = %d, want 403", code)
	}
	// No gardener account at all: nothing is vouched for.
	j = newJobRequest(t, e, shellProject)
	note = criterionNote(t, e, shellProject, j.proof(), nil)
	if err := e.St.ClearGardenerAccount(); err != nil {
		t.Fatal(err)
	}
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/criterion"); code != http.StatusForbidden {
		t.Errorf("without a gardener account = %d, want 403", code)
	}
	if err := e.St.SetGardenerAccount(gardener); err != nil {
		t.Fatal(err)
	}
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/criterion"); code != http.StatusSeeOther {
		t.Errorf("with the account back = %d", code)
	}
}

func TestCriterionNoteIsOutOfDateOnceTheCriterionOrTheRequestIsSettled(t *testing.T) {
	e, _ := gardenActEnv(t)
	// A person recorded the criterion meanwhile.
	j := newJobRequest(t, e, shellProject)
	note := criterionNote(t, e, shellProject, j.proof(), nil)
	if err := e.St.SetCriterionState(j.crit[0], "met", requestdomain.Evidence{Kind: "test", Ref: "go test", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/criterion"); code != http.StatusConflict {
		t.Errorf("a criterion settled meanwhile = %d, want 409", code)
	}
	if c := criterionOf(t, e.St, j, 0); len(c.Evidence) != 1 || c.Evidence[0].Person != "alice" {
		t.Errorf("the person's evidence was overwritten or doubled: %+v", c.Evidence)
	}
	// The request was dropped.
	j = newJobRequest(t, e, shellProject)
	note = criterionNote(t, e, shellProject, j.proof(), nil)
	if err := e.St.DropRequest(j.req, "nicht mehr nötig", "alice"); err != nil {
		t.Fatal(err)
	}
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/criterion"); code != http.StatusConflict {
		t.Errorf("a dropped request = %d, want 409", code)
	}
	// The criterion is gone from the ledger's view (it never existed).
	j = newJobRequest(t, e, shellProject)
	note = criterionNote(t, e, shellProject, j.proof(), func(n *store.Knowledge) { n.SessionRef = garden.CriterionRef(j.req, 987654) })
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/criterion"); code != http.StatusConflict {
		t.Errorf("a criterion that does not exist = %d, want 409", code)
	}
}

func TestCriterionNoteAboutAProjectTheViewerCannotReadNeverReachesThePageOrAnAction(t *testing.T) {
	e, _ := gardenActEnv(t)
	j := newJobRequest(t, e, shellHiddenProject)
	hidden := criterionNote(t, e, shellHiddenProject, j.proof(), nil)
	// A note in the viewer's project that names a request of the hidden one.
	crossed := criterionNote(t, e, shellProject, j.proof(), nil)
	_, page := fetchPage(t, e.Lead, e.Base+"/ui/review")
	if strings.Contains(page, "Der Gärtner pflegt das Ledger") {
		t.Errorf("the title of a request of a hidden project is on the page:\n%s", page)
	}
	for _, note := range []int64{hidden, crossed} {
		if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/criterion"); code != http.StatusNotFound {
			t.Errorf("note %d = %d, want 404", note, code)
		}
	}
	if c := criterionOf(t, e.St, j, 0); c.State != "open" {
		t.Errorf("a criterion of a hidden project changed: %+v", c)
	}
}

// ------------------------------------------------------------------ descriptions

const ghostOld = "Beschreibt die Datei, wie sie war, und hängt mit anderen Dateien zusammen."
const ghostNew = "Hält den Zustand des Dienstes.\nInvariante: genau ein Schreiber.\nHängt mit store/writer.go zusammen."

func ghostPath() garden.GhostPath {
	return garden.GhostPath{Path: "internal/store/x.go", State: garden.GhostStale, Percent: 42, Old: ghostOld,
		SHA: strings.Repeat("a", 64), Blob: strings.Repeat("b", 40), Lines: 120}
}

func describeNote(t *testing.T, e shellEnv, project string, g garden.GhostPath, text string, edit func(*store.Knowledge)) int64 {
	t.Helper()
	n := garden.GhostProposal(project, garden.GhostProposalData{GhostPath: g, Description: text, Reason: "umgeschrieben"})
	if edit != nil {
		edit(&n)
	}
	return gardenerWrote(t, e.St, n, e.St.GardenerAccountID())
}

func seedGhost(t *testing.T, st *store.Store, project, path, text string) {
	t.Helper()
	if _, err := st.PutGhostFile(store.GhostFile{Project: project, Path: path, Kind: "file", Description: text, ContentSHA: strings.Repeat("c", 64), GitBlob: strings.Repeat("d", 40), LineCount: 90, Person: "alice", Harness: "mcp"}); err != nil {
		t.Fatal(err)
	}
}

func TestDescriptionNoteWritesTheGhostOnlyWhenAPersonAdoptsIt(t *testing.T) {
	e, _ := gardenActEnv(t)
	g := ghostPath()
	seedGhost(t, e.St, shellProject, g.Path, ghostOld)
	note := describeNote(t, e, shellProject, g, ghostNew, nil)

	if got, _ := e.St.GhostFileByPath(shellProject, g.Path); got.Description != ghostOld {
		t.Fatalf("filing the note changed the ghost: %q", got.Description)
	}
	_, page := fetchPage(t, e.Lead, e.Base+"/ui/review")
	for _, want := range []string{
		`action="/ui/review/` + idStr(note) + `/describe"`, "Adopt description", "Description?", g.Path,
		"Until now", ghostOld, "Proposed", "Invariante: genau ein Schreiber.",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the card lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, `/review/`+idStr(note)+`/approve`) || strings.Contains(page, "Approve all") {
		t.Error("a description note offers plain approval")
	}

	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/describe"); code != http.StatusSeeOther {
		t.Fatalf("adopting = %d", code)
	}
	got, err := e.St.GhostFileByPath(shellProject, g.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != ghostNew || got.ContentSHA != g.SHA || got.GitBlob != g.Blob || got.LineCount != g.Lines || got.Kind != "file" {
		t.Errorf("ghost = %+v", got)
	}
	// Who proposed it and who confirmed it.
	if got.Harness != "gardener" || got.Person != "lars" || got.SessionRef != "gardener:ghost-note:"+idStr(note) {
		t.Errorf("origin of the ghost: harness %q, person %q, ref %q", got.Harness, got.Person, got.SessionRef)
	}
	// The old text is not gone: it is the earlier version of the ghost.
	hist, err := e.St.GhostFileHistory(shellProject, g.Path, 5)
	if err != nil || len(hist) != 1 || hist[0].Description != ghostOld {
		t.Errorf("history = %+v (%v)", hist, err)
	}
	if k, _ := e.St.KnowledgeByID(note); k.Confidence != "verified" || k.ConfirmedBy != "lars" {
		t.Errorf("note after adopting = %+v", k)
	}
	if _, page := fetchPage(t, e.Lead, e.Base+"/ui/review?done=described&k="+idStr(note)+"&source=gardener"); !strings.Contains(page, "Description adopted") {
		t.Errorf("no result line:\n%s", page)
	}
	// Twice: the description the note was written against is not the one that stands.
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/describe"); code != http.StatusConflict {
		t.Errorf("second adoption = %d, want 409", code)
	}
}

func TestDescriptionNoteFillsAPathWithoutADescription(t *testing.T) {
	e, _ := gardenActEnv(t)
	g := ghostPath()
	g.State, g.Old = garden.GhostUndescribed, ""
	note := describeNote(t, e, shellProject, g, ghostNew, nil)
	_, page := fetchPage(t, e.Lead, e.Base+"/ui/review")
	if !strings.Contains(page, "No description yet.") {
		t.Errorf("the card does not say there was none:\n%s", page)
	}
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/describe"); code != http.StatusSeeOther {
		t.Fatalf("= %d", code)
	}
	if got, err := e.St.GhostFileByPath(shellProject, g.Path); err != nil || got.Description != ghostNew || got.Harness != "gardener" {
		t.Errorf("ghost = %+v (%v)", got, err)
	}
}

func TestDescriptionNoteNeverOverwritesADescriptionSomebodyWroteMeanwhile(t *testing.T) {
	e, _ := gardenActEnv(t)
	g := ghostPath()
	seedGhost(t, e.St, shellProject, g.Path, ghostOld)
	note := describeNote(t, e, shellProject, g, ghostNew, nil)
	seedGhost(t, e.St, shellProject, g.Path, "Eine Beschreibung, die ein Mensch inzwischen geschrieben hat, und die mehr weiß.")
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/describe"); code != http.StatusConflict {
		t.Fatalf("= %d, want 409", code)
	}
	if got, _ := e.St.GhostFileByPath(shellProject, g.Path); !strings.HasPrefix(got.Description, "Eine Beschreibung, die ein Mensch") || got.Harness != "mcp" {
		t.Errorf("the person's description was overwritten: %+v", got)
	}
	// The note was about "no description" and now there is one.
	g2 := ghostPath()
	g2.Path, g2.Old = "internal/store/y.go", ""
	note2 := describeNote(t, e, shellProject, g2, ghostNew, nil)
	seedGhost(t, e.St, shellProject, g2.Path, ghostOld)
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note2)+"/describe"); code != http.StatusConflict {
		t.Errorf("a note about an empty path where one was written = %d, want 409", code)
	}
}

func TestDescriptionActionIsOfferedAndTakenByThoseWhoMayDoWhatItDoes(t *testing.T) {
	e, _ := gardenActEnv(t)
	if _, err := e.St.AddPerson("sven"); err != nil {
		t.Fatal(err)
	}
	stranger := loginInteractive(t, e.Srv, e.St, "sven")
	cases := []struct {
		who     string
		c       *http.Client
		offered bool
		code    int
	}{
		{"lead", e.Lead, true, http.StatusSeeOther},
		{"owner", e.Owner, true, http.StatusSeeOther},
		{"reviewer", e.Reviewer, true, http.StatusSeeOther},
		{"member", e.Member, false, http.StatusForbidden},
		{"guest", e.Guest, false, http.StatusNotFound},
		{"stranger", stranger, false, http.StatusNotFound},
	}
	for i, tc := range cases {
		g := ghostPath()
		g.Path = "internal/store/f" + idStr(int64(i)) + ".go"
		g.State, g.Old = garden.GhostUndescribed, ""
		note := describeNote(t, e, shellProject, g, ghostNew, nil)
		_, page := fetchPage(t, tc.c, e.Base+"/ui/review")
		if got := strings.Contains(page, `action="/ui/review/`+idStr(note)+`/describe"`); got != tc.offered {
			t.Errorf("%s: the button is offered=%v, want %v", tc.who, got, tc.offered)
		}
		if code := postGardenAction(t, e, tc.c, "/ui/review/"+idStr(note)+"/describe"); code != tc.code {
			t.Errorf("%s: adopting = %d, want %d", tc.who, code, tc.code)
		}
		_, err := e.St.GhostFileByPath(shellProject, g.Path)
		if written := err == nil; written != (tc.code == http.StatusSeeOther) {
			t.Errorf("%s: description written=%v", tc.who, written)
		}
	}
}

func TestDescriptionNoteThatOnlyLooksLikeTheGardenersIsNotActedOn(t *testing.T) {
	e, gardener := gardenActEnv(t)
	cases := []struct {
		name string
		path string
		file func(g garden.GhostPath) int64
	}{
		{"written by the account of a lead", "a/1.go", func(g garden.GhostPath) int64 {
			return gardenerWrote(t, e.St, garden.GhostProposal(shellProject, garden.GhostProposalData{GhostPath: g, Description: ghostNew}), 4)
		}},
		{"written by no account", "a/2.go", func(g garden.GhostPath) int64 {
			return gardenerWrote(t, e.St, garden.GhostProposal(shellProject, garden.GhostProposalData{GhostPath: g, Description: ghostNew}), 999)
		}},
		{"a title that names another path", "a/3.go", func(g garden.GhostPath) int64 {
			return describeNote(t, e, shellProject, g, ghostNew, func(n *store.Knowledge) { n.Title = "[Gärtner] Beschreibung? a/other.go" })
		}},
		{"a path that leaves the repository", "../../etc/passwd", func(g garden.GhostPath) int64 {
			return describeNote(t, e, shellProject, g, ghostNew, nil)
		}},
		{"a path inside .git", ".git/hooks/pre-commit", func(g garden.GhostPath) int64 {
			return describeNote(t, e, shellProject, g, ghostNew, nil)
		}},
		{"a text with control characters", "a/4.go", func(g garden.GhostPath) int64 {
			return describeNote(t, e, shellProject, g, ghostNew+"\x1b[31m", nil)
		}},
		{"a text with a right-to-left override", "a/5.go", func(g garden.GhostPath) int64 {
			return describeNote(t, e, shellProject, g, ghostNew+"\u202e", nil)
		}},
		{"a text that says nothing", "a/6.go", func(g garden.GhostPath) int64 { return describeNote(t, e, shellProject, g, "ok", nil) }},
		{"a text that is a document", "a/7.go", func(g garden.GhostPath) int64 {
			return describeNote(t, e, shellProject, g, strings.Repeat("Wort ", 900), nil)
		}},
		{"hashes of another shape", "a/8.go", func(g garden.GhostPath) int64 {
			return describeNote(t, e, shellProject, g, ghostNew, func(n *store.Knowledge) { n.Body = strings.Replace(n.Body, "blob="+g.Blob, "blob=nicht-ein-hash", 1) })
		}},
		{"no hashes", "a/9.go", func(g garden.GhostPath) int64 {
			return describeNote(t, e, shellProject, g, ghostNew, func(n *store.Knowledge) { n.Body = strings.Replace(n.Body, "Stand: ", "Zustand: ", 1) })
		}},
		{"no text", "b/1.go", func(g garden.GhostPath) int64 {
			return describeNote(t, e, shellProject, g, ghostNew, func(n *store.Knowledge) { n.Body = strings.Replace(n.Body, "\nBeschreibung:\n", "\nText:\n", 1) })
		}},
	}
	for _, tc := range cases {
		g := ghostPath()
		g.Path, g.State, g.Old = tc.path, garden.GhostUndescribed, ""
		note := tc.file(g)
		_, page := fetchPage(t, e.Lead, e.Base+"/ui/review")
		if strings.Contains(page, `action="/ui/review/`+idStr(note)+`/describe"`) {
			t.Errorf("%s: the card offers the action", tc.name)
		}
		if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/describe"); code != http.StatusForbidden && code != http.StatusConflict {
			t.Errorf("%s: = %d, want a refusal", tc.name, code)
		}
		if gs, _ := e.St.GhostFilesUnder(shellProject, ""); len(gs) != 0 {
			t.Fatalf("%s: a ghost was written: %+v", tc.name, gs)
		}
		if got := entryState(t, e.St, note); got != "staged/active" {
			t.Errorf("%s: the note changed: %s", tc.name, got)
		}
	}
	// Edited after the gardener filed it.
	g := ghostPath()
	g.Path, g.Old = "c/1.go", ""
	note := describeNote(t, e, shellProject, g, ghostNew, nil)
	n, _ := e.St.KnowledgeByID(note)
	if err := e.St.UpdateKnowledgeBy(note, map[string]string{"body": strings.Replace(n.Body, "genau ein Schreiber", "keine Regeln", 1)}, "lars"); err != nil {
		t.Fatal(err)
	}
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/describe"); code != http.StatusForbidden {
		t.Errorf("an edited note = %d, want 403", code)
	}
	// No gardener account at all.
	g.Path = "c/2.go"
	note = describeNote(t, e, shellProject, g, ghostNew, nil)
	if err := e.St.ClearGardenerAccount(); err != nil {
		t.Fatal(err)
	}
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/describe"); code != http.StatusForbidden {
		t.Errorf("without a gardener account = %d, want 403", code)
	}
	if err := e.St.SetGardenerAccount(gardener); err != nil {
		t.Fatal(err)
	}
	if gs, _ := e.St.GhostFilesUnder(shellProject, ""); len(gs) != 0 {
		t.Errorf("a ghost was written by a refused action: %+v", gs)
	}
}

func TestDescriptionNoteAboutAHiddenProjectNeverReachesThePageOrAnAction(t *testing.T) {
	e, _ := gardenActEnv(t)
	g := ghostPath()
	g.State, g.Old = garden.GhostUndescribed, ""
	note := describeNote(t, e, shellHiddenProject, g, ghostNew+" SECRET-TEXT", nil)
	_, page := fetchPage(t, e.Lead, e.Base+"/ui/review")
	if strings.Contains(page, "SECRET") {
		t.Error("a note of a hidden project is on the page")
	}
	if code := postGardenAction(t, e, e.Lead, "/ui/review/"+idStr(note)+"/describe"); code != http.StatusNotFound {
		t.Errorf("= %d, want 404", code)
	}
	if gs, _ := e.St.GhostFilesUnder(shellHiddenProject, ""); len(gs) != 0 {
		t.Error("a ghost of a hidden project was written")
	}
}

func TestDescriptionActionIgnoresEveryValueOfTheForm(t *testing.T) {
	e, _ := gardenActEnv(t)
	g := ghostPath()
	g.State, g.Old = garden.GhostUndescribed, ""
	note := describeNote(t, e, shellProject, g, ghostNew, nil)
	code := postWith(t, e, e.Lead, "/ui/review/"+idStr(note)+"/describe", url.Values{
		"path": {"etc/passwd"}, "description": {"gefälscht"}, "project": {shellHiddenProject}, "content_sha": {"x"}, "git_blob": {"y"}, "person": {"alice"}, "harness": {"mcp"},
	})
	if code != http.StatusSeeOther {
		t.Fatalf("= %d", code)
	}
	gs, _ := e.St.GhostFilesUnder(shellProject, "")
	if hidden, _ := e.St.GhostFilesUnder(shellHiddenProject, ""); len(hidden) != 0 {
		t.Errorf("a ghost was written in the project the form named: %+v", hidden)
	}
	if len(gs) != 1 || gs[0].Path != g.Path || gs[0].Project != shellProject || gs[0].Description != ghostNew || gs[0].Person != "lars" || gs[0].Harness != "gardener" {
		t.Errorf("ghosts = %+v", gs)
	}
}

// The web package reads what the gardener writes without importing it; this keeps
// the two in step for the notes of jobs 4 and 1.
func TestWebReadsWhatTheGardenerWritesForTheLedgerAndTheGhosts(t *testing.T) {
	if garden.CriterionRef(0, 0) != "gardener:criterion:0:0" || !strings.HasPrefix(garden.CriterionRef(1, 2), gardenCriterionRef) || !strings.HasPrefix(garden.GhostRef("a/b.go"), gardenGhostRef) {
		t.Error("the references of the notes have another prefix than the web reads")
	}
	req, crit, ok := parseCriterionRef(garden.CriterionRef(457, 1793))
	if !ok || req != 457 || crit != 1793 {
		t.Errorf("criterion ref read as %d %d %v", req, crit, ok)
	}
	if _, _, ok := parseCriterionRef("gardener:regression:5:existing"); ok {
		t.Error("a regression note read as a criterion note")
	}
	if r, n, ok := parseCriterionTitle(garden.CriterionTitle(457, 6, "Text mit: Doppelpunkt und #1")); !ok || r != 457 || n != 6 {
		t.Errorf("criterion title read as %d %d %v", r, n, ok)
	}
	if garden.GhostTitle("a/b.go") != ghostTitleOf("a/b.go") {
		t.Error("the title of a description note differs")
	}
	for _, c := range []struct{ kind, ref string }{
		{"commit", "4584fb7a1c2d (#149)"}, {"commit", "4584fb7a1c2d"}, {"commit", "4584fb7"}, {"commit", "HEAD"}, {"commit", "4584fb7a1c2d (#)"}, {"commit", "4584FB7A1C2D"},
		{"test", "cmd/x_test.go:TestX"}, {"test", "x_test.go"}, {"test", "../x_test.go:TestX"}, {"test", "a/.git/x_test.go:TestX"},
		{"file", "internal/garden/ledger.go"}, {"file", "../x.go"}, {"file", "a b.go"}, {"file", ".git/config"}, {"file", "a/./b.go"}, {"url", "https://example.invalid"},
	} {
		if validProofRef(c.kind, c.ref) != garden.ValidProofRef(c.kind, c.ref) {
			t.Errorf("%s %q: the web and the gardener differ on its shape", c.kind, c.ref)
		}
	}
	// A description note round-trips through the parser, with a text that holds the marker and headers.
	g := garden.GhostProposalData{GhostPath: ghostPath(), Description: "Erste Zeile des Textes, lang genug.\nBegründung: steht im Text.\nBeschreibung:\nStand: sha=nichts"}
	f, ok := parseGhostNote(garden.GhostProposal("p", g).Body)
	if !ok || f.Description != g.Description || f.SHA != g.SHA || f.Blob != g.Blob || f.Lines != g.Lines || f.Was != store.GhostDescriptionHash(g.Old) {
		t.Errorf("parsed %+v %v", f, ok)
	}
	g.Old = ""
	if f, ok := parseGhostNote(garden.GhostProposal("p", g).Body); !ok || f.Was != "keine" {
		t.Errorf("a note without an earlier description parsed as %+v %v", f, ok)
	}
}
