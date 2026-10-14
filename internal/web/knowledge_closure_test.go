package web

import (
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestKnowledgePageShowsWhyAnEntryWasRetiredOrReplaced(t *testing.T) {
	e := seedSessions(t)
	page := func(id int64) string {
		t.Helper()
		_, body := fetchPage(t, e.Owner, e.Base+"/ui/knowledge/"+strconv.FormatInt(id, 10))
		return body
	}
	gone := knInsert(t, e.St, store.Knowledge{Title: "Temporary tunnel"})
	old := knInsert(t, e.St, store.Knowledge{Title: "Share created"})
	fresh := knInsert(t, e.St, store.Knowledge{Title: "Share removed again"})
	live := knInsert(t, e.St, store.Knowledge{Title: "Still true"})

	if err := e.St.RetireKnowledge(gone, "the tunnel was closed", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := e.St.SupersedeKnowledge(fresh, []int64{old}, "the share was removed", store.RelationActor{Name: "alice", Via: "cli"}); err != nil {
		t.Fatal(err)
	}

	retired := page(gone)
	for _, want := range []string{"Retired", "the tunnel was closed", "alice"} {
		if !strings.Contains(retired, want) {
			t.Errorf("page of the retired entry lacks %q", want)
		}
	}
	replaced := page(old)
	for _, want := range []string{"Replaced by", "Share removed again", `href="/ui/knowledge/` + strconv.FormatInt(fresh, 10) + `"`, "the share was removed"} {
		if !strings.Contains(replaced, want) {
			t.Errorf("page of the superseded entry lacks %q", want)
		}
	}
	if body := page(live); strings.Contains(body, "kn-closure") {
		t.Error("an entry in use shows a closure")
	}
}
