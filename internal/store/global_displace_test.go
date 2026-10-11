package store

import (
	"fmt"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Other people's global entries are cut out in SQL, so they cannot push a
// visible hit out of a limit or inflate a count (security review of PR #101,
// findings 4 and 5).
func TestForeignGlobalEntriesDoNotDisplaceVisibleOnes(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	project := scope.Axes{Project: roleProject}
	own, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "zebrafinch ownentry", Body: "zebrafinch", Person: "mia", AuthorID: 3, Scope: project, Confidence: "staged"})
	if err != nil {
		t.Fatal(err)
	}
	// Newer, and many more than any limit below: robin's global entries.
	for i := 0; i < 30; i++ {
		if _, err := st.InsertKnowledge(Knowledge{Type: "note", Title: fmt.Sprintf("zebrafinch foreign %d", i), Body: "zebrafinch", Person: "robin", AuthorID: 1, Confidence: "staged"}); err != nil {
			t.Fatal(err)
		}
	}
	mia := viewer(st, "person:3", "mia")
	v := mia.Viewer()
	has := func(ks []Knowledge, err error) bool {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range ks {
			if k.Scope.Project == "" {
				t.Errorf("a foreign global entry came back: %q", k.Title)
			}
		}
		for _, k := range ks {
			if k.ID == own {
				return true
			}
		}
		return false
	}
	if !has(st.BrowseKnowledge("", "", "", 5, v)) {
		t.Error("BrowseKnowledge lost her entry")
	}
	if !has(st.SearchAllKnowledge("zebrafinch", scope.Axes{}, 5, v)) {
		t.Error("SearchAllKnowledge lost her entry")
	}
	if !has(st.PendingKnowledge("", 5, v)) {
		t.Error("PendingKnowledge lost her entry")
	}
	if !has(st.SearchKnowledge("zebrafinch", scope.Axes{}, 5, v)) {
		t.Error("SearchKnowledge lost her entry")
	}
	if !has(st.SearchKnowledgeForContext("zebrafinch", project, 5, v)) {
		t.Error("SearchKnowledgeForContext lost her entry")
	}
	// Nothing was recorded as a search hit for the foreign entries.
	var hits int
	if err := st.db.QueryRow(`SELECT COALESCE(SUM(search_hits),0) FROM knowledge WHERE project='' AND person='robin'`).Scan(&hits); err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Errorf("foreign entries were counted as search hits: %d", hits)
	}
	// The author still gets all of his.
	robin, err := st.BrowseKnowledge("", "", "", 400, viewer(st, "person:1", "robin").Viewer())
	if err != nil || len(robin) < 31 {
		t.Errorf("robin browses %d entries: %v", len(robin), err)
	}
	// Without enforcement nothing changes.
	st.SetAccessMode(AccessMode{})
	all, _ := st.BrowseKnowledge("", "", "", 400, viewer(st, "person:3", "mia").Viewer())
	if len(all) < 31 {
		t.Errorf("log mode hides entries: %d", len(all))
	}
}

func TestRelevantKnowledgeIgnoresForeignGlobalEntries(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	if _, err := st.InsertKnowledge(Knowledge{Type: "pitfall", Title: "quokkaquirk deployment", Body: "b", Person: "robin", AuthorID: 1}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id, label string
		want      int
	}{{"person:3", "mia", 0}, {"person:6", "nora", 0}, {"person:1", "robin", 1}} {
		ks, err := st.RelevantKnowledge("quokkaquirk please", scope.Axes{}, 3, viewer(st, c.id, c.label).Viewer())
		if err != nil || len(ks) != c.want {
			t.Errorf("%s: %d relevant entries, want %d (%v)", c.label, len(ks), c.want, err)
		}
	}
	var hits int
	_ = st.db.QueryRow(`SELECT COALESCE(SUM(search_hits),0) FROM knowledge`).Scan(&hits)
	if hits != 1 {
		t.Errorf("search hits = %d, want only the author's", hits)
	}
}

func TestOpenRequestCountIgnoresForeignGlobalRequests(t *testing.T) {
	f := newGlobalFixture(t)
	f.st.SetAccessMode(AccessMode{Enforce: true})
	for _, c := range []struct {
		id, label string
		want      int
	}{{"person:6", "nora", 0}, {"person:2", "lena", 0}, {"person:3", "mia", 1}, {"person:1", "robin", 2}} {
		n, err := f.st.CountOpenRequestsFor(viewer(f.st, c.id, c.label).Viewer(), scope.Axes{})
		if err != nil || n != c.want {
			t.Errorf("%s counts %d open requests, want %d (%v)", c.label, n, c.want, err)
		}
	}
	// Created later by the author, the counter of the author moves, nobody else's.
	if _, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "another", Person: "mia"}}); err != nil {
		t.Fatal(err)
	}
	if n, _ := f.st.CountOpenRequestsFor(viewer(f.st, "person:3", "mia").Viewer(), scope.Axes{}); n != 2 {
		t.Errorf("author counts %d, want 2", n)
	}
}
