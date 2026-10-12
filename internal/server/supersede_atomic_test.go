package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func projectEntries(t *testing.T, f *accessAPIFixture) int {
	t.Helper()
	ks, err := f.st.KnowledgeForProject(accProject)
	if err != nil {
		t.Fatal(err)
	}
	return len(ks)
}

func supersedeBody(title string, olds ...int64) map[string]any {
	return newEntry(map[string]any{"title": title, "supersedes": olds, "supersede_reason": "r"})
}

// The new entry and the supersede are one transaction: whatever fails, no
// follow-up entry is left behind.
func TestFailedSupersedeLeavesNoStrayEntry(t *testing.T) {
	f := accessAPI(t, true)
	old := f.id["k-mia"]
	before := projectEntries(t, f)

	// Effective confidence: origin distilled would make it quarantined.
	body := supersedeBody("distilled", old)
	body["origin"] = "distilled"
	if code, out := f.call(t, "mia", "POST", "/api/knowledge", body); code != 400 || !strings.Contains(out, "not_deliverable") {
		t.Errorf("distilled replacement = %d %s", code, out)
	}
	if projectEntries(t, f) != before {
		t.Error("the refused distilled replacement left an entry behind")
	}

	// The body cannot choose the status: the replacement is always live.
	for _, status := range []string{"stale", "deprecated", "superseded"} {
		body := supersedeBody("live "+status, old)
		body["status"], body["superseded_by"] = status, 999
		out := f.expect(t, "mia", 200, "POST", "/api/knowledge", body)
		var saved store.Knowledge
		if err := json.Unmarshal([]byte(out), &saved); err != nil || saved.Status != "active" || saved.SupersededBy != 0 {
			t.Fatalf("status %q in the body: %+v (%v)", status, saved, err)
		}
		// The old entry is superseded now; the next round starts from a new one.
		old = saved.ID
	}

	// A writer that loses the race for an already superseded entry is refused
	// and its entry rolled back.
	target := f.id["k-robin-staged"]
	f.expect(t, "lena", 200, "POST", "/api/knowledge", supersedeBody("winner", target))
	before = projectEntries(t, f)
	if code, out := f.call(t, "lena", "POST", "/api/knowledge", supersedeBody("loser", target)); code != 409 {
		t.Errorf("racing writer = %d %s", code, out)
	}
	if projectEntries(t, f) != before {
		t.Error("the losing writer left an entry behind")
	}
}

func TestSupersedeNeedsTheExactScope(t *testing.T) {
	f := accessAPI(t, true)
	old := f.id["k-mia"] // project scope
	before := projectEntries(t, f)
	body := supersedeBody("branch only", old)
	body["scope"] = map[string]string{"project": accProject, "branch": "feature"}
	if code, out := f.call(t, "mia", "POST", "/api/knowledge", body); code != 400 || !strings.Contains(out, "scope_mismatch") {
		t.Errorf("branch replacement = %d %s", code, out)
	}
	if k, _ := f.st.KnowledgeByID(old); k.Status != "active" {
		t.Errorf("a project entry was hidden by a branch entry: %+v", k)
	}
	if projectEntries(t, f) != before {
		t.Error("a refused call left an entry behind")
	}
}

func TestRetireRefusesTheHeadOfAChain(t *testing.T) {
	f := accessAPI(t, true)
	out := f.expect(t, "mia", 200, "POST", "/api/knowledge", supersedeBody("head", f.id["k-mia"]))
	var head store.Knowledge
	if err := json.Unmarshal([]byte(out), &head); err != nil {
		t.Fatal(err)
	}
	code, body := f.call(t, "mia", "POST", idPath("/api/knowledge/%d/retire", head.ID), map[string]string{"reason": "gone"})
	if code != 409 || !strings.Contains(body, "has_predecessors") {
		t.Errorf("retire head = %d %s", code, body)
	}
	if k, _ := f.st.KnowledgeByID(head.ID); k.Status != "active" {
		t.Errorf("the head was retired: %+v", k)
	}
}

// Somebody who may write to the project but cannot read an entry gets the same
// answer as for an id that does not exist.
func TestRetireAnswersLikeUnknownForAnEntryYouMayNotRead(t *testing.T) {
	f := accessAPI(t, true)
	hidden := f.id["k-machine"] // mia's machine entry; rex writes to the project but does not own mia-box
	if code, _ := f.call(t, "rex", "GET", idPath("/api/knowledge/%d", hidden), nil); code == 200 {
		t.Fatal("fixture: rex can read the machine entry, so this case proves nothing")
	}
	c1, b1 := f.call(t, "rex", "POST", idPath("/api/knowledge/%d/retire", hidden), map[string]string{"reason": "x"})
	c2, b2 := f.call(t, "rex", "POST", "/api/knowledge/99999/retire", map[string]string{"reason": "x"})
	if c1 != 404 || c1 != c2 || b1 != b2 {
		t.Errorf("hidden %d %q, unknown %d %q", c1, b1, c2, b2)
	}
	if k, _ := f.st.KnowledgeByID(hidden); k.Status != "active" {
		t.Errorf("retired: %+v", k)
	}
}
