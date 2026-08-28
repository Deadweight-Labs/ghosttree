package scope

import (
	"reflect"
	"testing"
)

func TestNormalizeRemote(t *testing.T) {
	cases := map[string]string{
		"git@github.com:Example/sample-project.git":        "github.com/example/sample-project",
		"https://github.com/Example/sample-project":        "github.com/example/sample-project",
		"https://github.com/Deadweight-Labs/ghosttree.git": "github.com/deadweight-labs/ghosttree",
	}
	for in, want := range cases {
		if got := NormalizeRemote(in); got != want {
			t.Errorf("NormalizeRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalAxesNormalizesEveryClientBoundaryValue(t *testing.T) {
	got := CanonicalAxes(Axes{Project: " git@GitHub.com:Deadweight-Labs/Ghosttree.git ", Branch: " main ", Machine: "LAPTOP"})
	want := Axes{Project: "github.com/deadweight-labs/ghosttree", Branch: "main", Machine: "laptop"}
	if !got.Same(want) {
		t.Fatalf("axes=%+v want=%+v", got, want)
	}
}

func TestUnionWhere(t *testing.T) {
	sql, args := Axes{Project: "github.com/x/y", Branch: "main", Machine: "workstation-a"}.UnionWhere()
	want := `((project = '' AND branch = '' AND machine = '') OR (project = '' AND branch = '' AND machine = ?) OR (project = ? AND branch = '' AND machine = '') OR (project = ? AND branch = ? AND machine = '') OR (project = ? AND branch = '' AND machine = ?))`
	if sql != want {
		t.Errorf("sql = %s", sql)
	}
	if !reflect.DeepEqual(args, []any{"workstation-a", "github.com/x/y", "github.com/x/y", "main", "github.com/x/y", "workstation-a"}) {
		t.Errorf("args = %v", args)
	}
}

func TestUnionWhereNoProject(t *testing.T) {
	// Outside a repo: only global + machine knowledge applies.
	sql, args := Axes{Machine: "workstation-a"}.UnionWhere()
	want := `((project = '' AND branch = '' AND machine = '') OR (project = '' AND branch = '' AND machine = ?))`
	if sql != want || len(args) != 1 {
		t.Errorf("sql = %s args = %v", sql, args)
	}
}

func TestFilterWhere(t *testing.T) {
	sql, args := Axes{Project: "github.com/x/y"}.FilterWhere()
	if sql != `(project = ?)` || !reflect.DeepEqual(args, []any{"github.com/x/y"}) {
		t.Errorf("sql = %s args = %v", sql, args)
	}
	sql, _ = Axes{}.FilterWhere()
	if sql != `(1=1)` {
		t.Errorf("empty filter sql = %s", sql)
	}
}

// Until 2026-08-24 pitfalls, notes and plans defaulted to project+branch, and
// only decisions got the plain project. That was the wrong way round. Reads
// match a branch exactly, so a pitfall learnt on one branch was invisible on
// every other one and outlived the branch that named it: sixteen entries in
// this repository, including "ctx mcp per Shell-Pipe testen schlägt fehl", were
// stranded on v0 while the work had moved to main. A branch is a statement
// about unfinished work, not a validity range for what was learnt.
func TestDefaultAxesDoesNotBindToABranch(t *testing.T) {
	ctx := Axes{Project: "p", Branch: "b", Machine: "m"}
	for _, entryType := range []string{"pitfall", "note", "plan", "decision", "instruction"} {
		if got := DefaultAxes(entryType, ctx); !got.Same(Axes{Project: "p"}) {
			t.Errorf("%s = %+v, want project scope only", entryType, got)
		}
	}
	// No project context: everything an agent learns outside a repository is
	// about the machine it is standing on.
	if got := DefaultAxes("pitfall", Axes{Machine: "m"}); !got.Same(Axes{Machine: "m"}) {
		t.Errorf("no-project pitfall = %+v", got)
	}
}
