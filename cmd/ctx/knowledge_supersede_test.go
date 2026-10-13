package main

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestKnowledgeAddSupersedesAndRetireCLI(t *testing.T) {
	st := moveCLIFixture(t)
	ctx := func(args ...string) (int, string) {
		var out bytes.Buffer
		code := run(append([]string{"knowledge"}, args...), &out)
		return code, out.String()
	}
	oldA, _ := st.InsertKnowledge(store.Knowledge{Type: "note", Title: "share created", Body: "b", Scope: scope.Axes{Project: cliHere}, Person: "alice"})
	oldB, _ := st.InsertKnowledge(store.Knowledge{Type: "note", Title: "share location set", Body: "b", Scope: scope.Axes{Project: cliHere}, Person: "alice"})
	ida, idb := strconv.FormatInt(oldA, 10), strconv.FormatInt(oldB, 10)

	// The reason is required, and a refused call writes nothing.
	if code, out := ctx("add", "--type", "note", "--title", "share removed", "--scope", "project", "--project", cliHere, "--supersedes", ida); code != 2 || !strings.Contains(out, "--reason") {
		t.Errorf("without reason: %d %s", code, out)
	}
	if rows, _ := st.SearchKnowledge("removed", scope.Axes{}, 10); len(rows) != 0 {
		t.Errorf("refused call stored %+v", rows)
	}

	code, out := ctx("add", "--type", "note", "--title", "share removed", "--body", "gone", "--scope", "project", "--project", cliHere,
		"--supersedes", ida+",#"+idb, "--reason", "the share was removed")
	if code != 0 || !strings.Contains(out, "superseded #"+ida+", #"+idb) {
		t.Fatalf("add: %d %s", code, out)
	}
	for _, id := range []int64{oldA, oldB} {
		if k, _ := st.KnowledgeByID(id); k.Status != "superseded" {
			t.Errorf("#%d = %+v", id, k)
		}
	}
	hits, _ := st.SearchKnowledgeForContext("share", scope.Axes{Project: cliHere}, 10)
	if len(hits) != 1 || hits[0].Title != "share removed" {
		t.Errorf("search = %v", titles(hits))
	}

	// retire
	gone, _ := st.InsertKnowledge(store.Knowledge{Type: "note", Title: "temporary tunnel", Body: "b", Scope: scope.Axes{Project: cliHere}, Person: "alice"})
	id := strconv.FormatInt(gone, 10)
	if code, out := ctx("retire", id); code != 2 || !strings.Contains(out, "--reason") {
		t.Errorf("retire without reason: %d %s", code, out)
	}
	if code, out := ctx("retire", id, "--reason", "tunnel closed"); code != 0 || !strings.Contains(out, "retired #"+id) {
		t.Errorf("retire: %d %s", code, out)
	}
	if k, _ := st.KnowledgeByID(gone); k.Status != "deprecated" {
		t.Errorf("entry = %+v", k)
	}
	if code, _ := ctx("retire", id, "--reason", "again"); code != 1 {
		t.Errorf("second retire: %d", code)
	}
}

func titles(ks []store.Knowledge) []string {
	var out []string
	for _, k := range ks {
		out = append(out, k.Title)
	}
	return out
}
