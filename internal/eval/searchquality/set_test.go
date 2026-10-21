package searchquality_test

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/eval/searchquality"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestParseSetReadsKindsAliasesAndTargets(t *testing.T) {
	qs, err := searchquality.ParseSet(strings.NewReader(`
{"query": " a question ", "kind": "Requests", "target": "12", "ignored": true}

{"query": "where", "kind": "files", "target": "github.com/x/y:dir/a:b.go"}
{"query": "q", "kind": "knowledge", "target": " 3 "}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 3 {
		t.Fatalf("%+v", qs)
	}
	if qs[0].Kind != store.KindRequest || qs[0].Query != "a question" || qs[0].Target != "12" {
		t.Fatalf("%+v", qs[0])
	}
	// A path may hold a colon; the project may not, so the first one separates them.
	if qs[1].Kind != store.KindGhost || qs[1].Target != "github.com/x/y:dir/a:b.go" {
		t.Fatalf("%+v", qs[1])
	}
	if qs[2].Kind != store.KindKnowledge || qs[2].Target != "3" {
		t.Fatalf("%+v", qs[2])
	}
}

// The error names the line and what is wrong, and never the text of a query.
func TestParseSetErrorsNameTheLineAndNeverTheQuery(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"not json":     {"{\"query\": \"GEHEIM\", \"kind\": \"pain\", \"target\": \"1\"}\nGEHEIM", "line 2"},
		"kind":         {`{"query": "GEHEIM", "kind": "widgets", "target": "1"}`, "line 1: unknown kind"},
		"target":       {`{"query": "GEHEIM", "kind": "pain", "target": "x"}`, "line 1: the target of a pain query is an id"},
		"ghost":        {`{"query": "GEHEIM", "kind": "ghost", "target": ":path"}`, `"project:path"`},
		"empty query":  {`{"query": "", "kind": "pain", "target": "1"}`, "must not be empty"},
		"empty target": {`{"query": "GEHEIM", "kind": "pain", "target": ""}`, "must not be empty"},
		"nothing":      {"\n\n", "holds no query"},
	} {
		_, err := searchquality.ParseSet(strings.NewReader(tc.in))
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "GEHEIM") {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}
