package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// affinityFixture: mia is a member of accProject and owns the machine mia-box.
// Her entries on that machine arose in sessions of two projects.
type affinityFixture struct {
	*accessAPIFixture
	here, elsewhere, everywhere int64
}

func newAffinityFixture(t *testing.T) *affinityFixture {
	t.Helper()
	f := &affinityFixture{accessAPIFixture: accessAPI(t, true)}
	for ext, project := range map[string]string{"s-aff-here": accProject, "s-aff-other": accOther} {
		if _, err := f.st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: ext, AccountID: 3, Scope: scope.Axes{Project: project, Machine: "mia-box"}}); err != nil {
			t.Fatal(err)
		}
	}
	machine := func(title, body, session string) int64 {
		id, err := f.st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: title, Body: body, Scope: scope.Axes{Machine: "mia-box"},
			Person: "mia", OriginContext: &originctx.Context{SessionRef: session}})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	f.here = machine("arose in this project", "from a session here", "s-aff-here")
	f.elsewhere = machine("zorblaxen arose elsewhere", "from a session of another project", "s-aff-other")
	f.everywhere = machine("holds everywhere", "flagged by a person", "s-aff-other")
	if err := f.st.SetKnowledgeEverywhere(f.everywhere, true); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *affinityFixture) bootstrap(t *testing.T, project string) string {
	t.Helper()
	return f.expect(t, "mia", 200, "GET", "/api/context/bootstrap?project="+project+"&machine=mia-box&session=aff-boot", nil)
}

// REQ-641: of three machine entries the one that arose in the project and the
// one flagged "applies everywhere" are in the briefing, the one from another
// project is not, and the project's own knowledge stands in front of both.
func TestBootstrapDeliversBroadKnowledgeOnlyWithATieToTheProject(t *testing.T) {
	f := newAffinityFixture(t)
	out := f.bootstrap(t, accProject)
	for _, want := range []string{"arose in this project", "holds everywhere", "mia pitfall"} {
		if !strings.Contains(out, want) {
			t.Errorf("briefing lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "zorblaxen arose elsewhere") {
		t.Errorf("an entry from another project is in the briefing:\n%s", out)
	}
	proj, broad := strings.Index(out, "mia pitfall"), strings.Index(out, "arose in this project")
	if proj < 0 || broad < 0 || proj > broad {
		t.Errorf("project knowledge (at %d) must stand before the machine entries (at %d):\n%s", proj, broad, out)
	}
	// Two: the one from the other project and the machine entry of the shared
	// fixture, which arose in no session at all.
	if !strings.Contains(out, "2 machine or global entries without a link to this project") {
		t.Errorf("the withheld entry is not announced:\n%s", out)
	}
	// Seen from the other project it is the other way around.
	other := f.expect(t, "mia", 200, "GET", "/api/context/bootstrap?project="+accOther+"&machine=mia-box", nil)
	if strings.Contains(other, "arose in this project") || !strings.Contains(other, "zorblaxen arose elsewhere") {
		t.Errorf("briefing of the other project:\n%s", other)
	}
}

// What the briefing leaves out is still reachable: a word of the prompt delivers
// it, and the search finds it.
func TestWithheldBroadEntryStaysReachable(t *testing.T) {
	f := newAffinityFixture(t)
	if out := f.bootstrap(t, accProject); strings.Contains(out, "zorblaxen") {
		t.Fatalf("setup: the entry is in the briefing:\n%s", out)
	}
	out := f.expect(t, "mia", 200, "GET", "/api/context/relevant?project="+accProject+"&machine=mia-box&q=zorblaxen&session=aff-boot", nil)
	if !strings.Contains(out, "zorblaxen arose elsewhere") {
		t.Errorf("a word of the prompt did not deliver the entry:\n%s", out)
	}
	res := f.expect(t, "mia", 200, "GET", "/api/search?project="+accProject+"&machine=mia-box&scope=union&kind=knowledge&q=zorblaxen", nil)
	if !strings.Contains(res, "zorblaxen arose elsewhere") {
		t.Errorf("the search does not find the entry:\n%s", res)
	}
}

// Without a project there is nothing a tie could refer to: nothing is left out.
func TestBootstrapWithoutProjectKeepsBroadKnowledge(t *testing.T) {
	f := newAffinityFixture(t)
	out := f.expect(t, "mia", 200, "GET", "/api/context/bootstrap?machine=mia-box", nil)
	if !strings.Contains(out, "zorblaxen arose elsewhere") {
		t.Errorf("a briefing outside any project lost a machine entry:\n%s", out)
	}
}

// A search in a project with at least 15 project hits returns at most 5 broad
// ones of 20, each with its scope.
func TestSearchInProjectCapsBroadHits(t *testing.T) {
	f := newAffinityFixture(t)
	for i := range 16 {
		if _, err := f.st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: fmt.Sprintf("quotaword project %02d", i), Body: "quotaword",
			Scope: scope.Axes{Project: accProject}, Person: "mia"}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 10 {
		if _, err := f.st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: fmt.Sprintf("quotaword machine %02d", i), Body: "quotaword quotaword quotaword",
			Scope: scope.Axes{Machine: "mia-box"}, Person: "mia"}); err != nil {
			t.Fatal(err)
		}
	}
	count := func(q string) (project, broad int) {
		var res struct {
			Knowledge []store.Knowledge `json:"knowledge"`
		}
		out := f.expect(t, "mia", 200, "GET", "/api/search?kind=knowledge&limit=20&project="+accProject+"&machine=mia-box&"+q, nil)
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatal(err)
		}
		for _, k := range res.Knowledge {
			switch {
			case k.Scope.Project != "":
				project++
			case k.Scope.Machine != "":
				broad++
			default:
				t.Errorf("a hit without a scope: %+v", k)
			}
		}
		return project, broad
	}
	project, broad := count("scope=union&q=quotaword")
	if broad > 5 || project+broad != 20 {
		t.Errorf("search in the project: %d project and %d broad hits, want 20 with at most 5 broad", project, broad)
	}
	if broad == 0 {
		t.Errorf("no broad hit at all: the quota must cap, not exclude")
	}
	// Across projects the union does not apply and so neither does the quota.
	var res struct {
		Knowledge []store.Knowledge `json:"knowledge"`
	}
	out := f.expect(t, "mia", 200, "GET", "/api/search?kind=knowledge&limit=20&machine=mia-box&q=quotaword", nil)
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Knowledge) == 0 {
		t.Error("the unscoped search found nothing")
	}
}

func hits(ids ...int) []store.Knowledge {
	var out []store.Knowledge
	for _, id := range ids {
		k := store.Knowledge{ID: int64(id), Scope: scope.Axes{Project: "p"}}
		if id < 0 {
			k = store.Knowledge{ID: int64(-id), Scope: scope.Axes{Machine: "m"}}
		}
		out = append(out, k)
	}
	return out
}

func ids(ks []store.Knowledge) []int64 {
	out := make([]int64, len(ks))
	for i, k := range ks {
		out[i] = k.ID
	}
	return out
}

func TestCapBroadHits(t *testing.T) {
	// Enough project hits: broad ones beyond the quota make way for them.
	in := hits(-1, -2, 3, -4, 5, 6, -7, 8)
	if got := fmt.Sprint(ids(capBroadHits(in, 8))); got != "[1 2 3 4 5 6 7 8]" {
		// Nothing to cut: eight hits fit into eight.
		t.Errorf("all fit: %s", got)
	}
	// Limit 4 allows one broad hit, the best ranked; the rest is project.
	if got := fmt.Sprint(ids(capBroadHits(in, 4))); got != "[1 3 5 6]" {
		t.Errorf("limit 4: %s", got)
	}
	// Too few project hits: the broad ones fill the page up, rank order kept.
	few := hits(-1, -2, -3, 4, -5, -6)
	got := ids(capBroadHits(few, 6))
	if fmt.Sprint(got) != "[1 2 3 4 5 6]" {
		t.Errorf("short of project hits: %v", got)
	}
	got = ids(capBroadHits(few, 4))
	if len(got) != 4 || got[0] != 1 {
		t.Errorf("limit 4: %v", got)
	}
}

// The flag and the review: a person marks an entry "applies everywhere" and the
// review lists what arose in exactly one project.
func TestEverywhereAndTriageEndpoints(t *testing.T) {
	f := newAffinityFixture(t)
	f.expect(t, "mia", 204, "PUT", idPath("/api/knowledge/%d/everywhere", f.elsewhere), map[string]bool{"everywhere": true})
	if out := f.bootstrap(t, accProject); !strings.Contains(out, "zorblaxen arose elsewhere") {
		t.Errorf("a flagged entry is not in the briefing:\n%s", out)
	}
	f.expect(t, "mia", 204, "PUT", idPath("/api/knowledge/%d/everywhere", f.elsewhere), map[string]bool{"everywhere": false})
	// Someone who may not see the entry cannot flag it.
	f.expect(t, "nora", 404, "PUT", idPath("/api/knowledge/%d/everywhere", f.elsewhere), map[string]bool{"everywhere": true})
	// A project entry is not a candidate.
	f.expect(t, "robin", 400, "PUT", idPath("/api/knowledge/%d/everywhere", f.id["k-mia"]), map[string]bool{"everywhere": true})

	var res triageResult
	out := f.expect(t, "mia", 200, "GET", "/api/knowledge-triage?project="+accProject, nil)
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	var moves []int64
	for _, it := range res.Items {
		if it.Kind == store.TriageMove {
			moves = append(moves, it.Knowledge.ID)
			if it.Project != accProject {
				t.Errorf("move proposal for %q in the review of %q", it.Project, accProject)
			}
		}
	}
	if len(moves) != 1 || moves[0] != f.here {
		t.Errorf("review of %s proposes moving %v, want only %d (arose in exactly this project)", accProject, moves, f.here)
	}
	// Another account sees nothing of mia's machine.
	out = f.expect(t, "robin", 200, "GET", "/api/knowledge-triage?project="+accProject, nil)
	if strings.Contains(out, "arose in this project") {
		t.Errorf("the review shows a machine entry to someone else: %s", out)
	}
}
