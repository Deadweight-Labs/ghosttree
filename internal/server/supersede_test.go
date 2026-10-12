package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func newEntry(extra map[string]any) map[string]any {
	body := map[string]any{
		"type": "note", "title": "share removed again", "body": "the share is gone",
		"scope": map[string]string{"project": accProject}, "harness": "mcp",
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func TestWritingCanSupersedeAndTheOldEntryLeavesSearchBriefingAndList(t *testing.T) {
	f := accessAPI(t, true)
	old := f.id["k-mia"]
	out := f.expect(t, "mia", 200, "POST", "/api/knowledge", newEntry(map[string]any{
		"supersedes": []int64{old}, "supersede_reason": "the share was removed again",
	}))
	var saved store.Knowledge
	if err := json.Unmarshal([]byte(out), &saved); err != nil || saved.Status != "active" {
		t.Fatalf("new entry = %s (%v)", out, err)
	}

	k, err := f.st.KnowledgeByID(old)
	if err != nil || k.Status != "superseded" || k.SupersededBy != saved.ID {
		t.Fatalf("old entry = %+v, %v", k, err)
	}
	search := f.expect(t, "mia", 200, "GET", "/api/search?kind=knowledge&scope=union&project="+accProject+"&q=pitfall", nil)
	if strings.Contains(search, `"mia pitfall"`) {
		t.Errorf("search still returns the superseded entry: %s", search)
	}
	list := f.expect(t, "mia", 200, "GET", "/api/knowledge?project="+accProject, nil)
	if strings.Contains(list, `"mia pitfall"`) || !strings.Contains(list, "share removed again") {
		t.Errorf("briefing list: %s", list)
	}
	// Still reachable: detail and history.
	f.expect(t, "mia", 200, "GET", idPath("/api/knowledge/%d", old), nil)
	f.expect(t, "mia", 200, "GET", idPath("/api/knowledge/%d/history", old), nil)
	rels, err := f.st.RelationsOf(old)
	if err != nil || len(rels) != 1 || rels[0].FromID != saved.ID || rels[0].Origin != store.RelOriginAgent || rels[0].Reason != "the share was removed again" {
		t.Errorf("relations = %+v, %v", rels, err)
	}
}

func TestSupersedeNeedsEditRightAndLeavesNoFollowUpBehind(t *testing.T) {
	f := accessAPI(t, true)
	count := func() int {
		ks, err := f.st.KnowledgeForProject(accProject)
		if err != nil {
			t.Fatal(err)
		}
		return len(ks)
	}
	before := count()
	robins := f.id["k-robin-staged"]
	reason := "replaced"
	body := func(ids ...int64) map[string]any {
		return newEntry(map[string]any{"supersedes": ids, "supersede_reason": reason})
	}
	// A member may not edit somebody else's entry, so may not supersede it.
	if code, out := f.call(t, "mia", "POST", "/api/knowledge", body(robins)); code != 403 {
		t.Errorf("member superseding a foreign entry = %d %s", code, out)
	}
	// A guest cannot write at all.
	if code, _ := f.call(t, "gus", "POST", "/api/knowledge", body(f.id["k-mia"])); code != 403 && code != 404 {
		t.Errorf("guest superseding = %d", code)
	}
	// One entry out of two is not allowed: none is superseded, none is written.
	if code, _ := f.call(t, "mia", "POST", "/api/knowledge", body(f.id["k-mia"], robins)); code != 403 {
		t.Errorf("mixed list = %d", code)
	}
	// No reason, other project, held-back replacement.
	if code, out := f.call(t, "mia", "POST", "/api/knowledge", newEntry(map[string]any{"supersedes": []int64{f.id["k-mia"]}})); code != 400 || !strings.Contains(out, "reason_required") {
		t.Errorf("without reason = %d %s", code, out)
	}
	if code, out := f.call(t, "robin", "POST", "/api/knowledge", body(f.id["k-other"])); code != 400 || !strings.Contains(out, "other_project") {
		t.Errorf("other project = %d %s", code, out)
	}
	if code, out := f.call(t, "mia", "POST", "/api/knowledge", newEntry(map[string]any{"supersedes": []int64{f.id["k-mia"]}, "supersede_reason": "x", "confidence": "staged"})); code != 400 || !strings.Contains(out, "not_deliverable") {
		t.Errorf("held-back replacement = %d %s", code, out)
	}
	if got := count(); got != before {
		t.Errorf("rejected calls left %d new entries behind", got-before)
	}
	if k, _ := f.st.KnowledgeByID(f.id["k-mia"]); k.Status != "active" {
		t.Errorf("entry changed: %+v", k)
	}
}

// What the caller may not read looks like what does not exist: same status and
// same text, so the answer leaks nothing about the entry.
func TestSupersedeAndRetireDoNotRevealHiddenEntries(t *testing.T) {
	f := accessAPI(t, true)
	staged := f.id["k-robin-staged"] // a guest cannot see staged entries
	for _, who := range []string{"gus", "nora"} {
		hidden, hiddenBody := f.call(t, who, "POST", idPath("/api/knowledge/%d/retire", staged), map[string]string{"reason": "x"})
		unknown, unknownBody := f.call(t, who, "POST", "/api/knowledge/99999/retire", map[string]string{"reason": "x"})
		if hidden != unknown || hiddenBody != unknownBody {
			t.Errorf("%s retire: hidden %d %q, unknown %d %q", who, hidden, hiddenBody, unknown, unknownBody)
		}
	}
	if k, _ := f.st.KnowledgeByID(staged); k.Status != "active" {
		t.Fatalf("retired by an outsider: %+v", k)
	}
	hidden, hiddenBody := f.call(t, "nora", "POST", "/api/knowledge", newEntry(map[string]any{"supersedes": []int64{f.id["k-mia"]}, "supersede_reason": "x"}))
	if hidden == 200 {
		t.Errorf("stranger writes: %d %s", hidden, hiddenBody)
	}
	if k, _ := f.st.KnowledgeByID(f.id["k-mia"]); k.Status != "active" {
		t.Fatalf("superseded by an outsider: %+v", k)
	}
}

func TestRetireNeedsReasonAndTheRightOfRetireInTheWeb(t *testing.T) {
	f := accessAPI(t, true)
	mia, robins := f.id["k-mia"], f.id["k-robin-staged"]
	path := func(id int64) string { return idPath("/api/knowledge/%d/retire", id) }
	why := map[string]string{"reason": "the share no longer exists"}

	if code, out := f.call(t, "mia", "POST", path(mia), map[string]string{"reason": "  "}); code != 400 || !strings.Contains(out, "reason_required") {
		t.Errorf("no reason = %d %s", code, out)
	}
	// Same right as editing: a member retires her own entry, not a foreign one.
	f.expect(t, "mia", 403, "POST", path(robins), why)
	f.expect(t, "gus", 403, "POST", path(mia), why)
	f.expect(t, "nora", 404, "POST", path(mia), why)
	if k, _ := f.st.KnowledgeByID(robins); k.Status != "active" {
		t.Fatalf("foreign entry retired: %+v", k)
	}

	f.expect(t, "mia", 200, "POST", path(mia), why)
	k, _ := f.st.KnowledgeByID(mia)
	if k.Status != "deprecated" || k.LastModifiedBy != "mia" {
		t.Fatalf("entry = %+v", k)
	}
	rs, err := f.st.KnowledgeRetirements(mia)
	if err != nil || len(rs) != 1 || rs[0].Reason != "the share no longer exists" || rs[0].RetiredBy != "mia" {
		t.Errorf("retirements = %+v, %v", rs, err)
	}
	if out := f.expect(t, "mia", 200, "GET", "/api/knowledge?project="+accProject, nil); strings.Contains(out, "mia pitfall") {
		t.Errorf("retired entry still in the list")
	}
	f.expect(t, "mia", 200, "GET", idPath("/api/knowledge/%d", mia), nil)
	f.expect(t, "mia", 409, "POST", path(mia), why)

	// A lead retires any entry of the project, as in the web.
	f.expect(t, "lena", 200, "POST", path(robins), why)
	if k, _ := f.st.KnowledgeByID(robins); k.Status != "deprecated" {
		t.Errorf("lead could not retire: %+v", k)
	}
}

func TestBriefingTellsAgentsToSupersedeInsteadOfAFollowUp(t *testing.T) {
	out := RenderBootstrap([]store.Knowledge{{Type: "pitfall", Title: "t", Body: "b", Confidence: "trusted", Status: "active"}}, 0)
	for _, want := range []string{"context_search", "supersedes", "follow-up", "context_retire"} {
		if !strings.Contains(out, want) {
			t.Errorf("the briefing does not say %q:\n%s", want, out)
		}
	}
}
