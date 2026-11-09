package scope

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeRemote(t *testing.T) {
	cases := map[string]string{
		"git@github.com:Example/sample-project.git":           "github.com/example/sample-project",
		"https://github.com/Example/sample-project":           "github.com/example/sample-project",
		"https://github.com/Deadweight-Labs/ghosttree.git":    "github.com/deadweight-labs/ghosttree",
		" http://WWW.GitHub.com/Example/sample-project.git/ ": "github.com/example/sample-project",
		"ssh://git@github.com/Example/sample-project.git":     "github.com/example/sample-project",
		"git://github.com/Example/sample-project/":            "github.com/example/sample-project",
		"github.com/example/sample-project":                   "github.com/example/sample-project",
		// User and port only say how to reach the repository (review of REQ-623).
		"ssh://git@github.com:22/Example/sample-project.git": "github.com/example/sample-project",
		"https://github.com:443/Example/sample-project":      "github.com/example/sample-project",
		"https://robin@gitlab.example.org/Group/repo.git":    "gitlab.example.org/group/repo",
		// Only the standard ports go (third review, point 3); another port
		// can be another server and stays.
		"ssh://deploy@git.internal:2222/team/repo":            "git.internal:2222/team/repo",
		"git://git.example.org:9418/team/repo":                "git.example.org/team/repo",
		"http://git.example.org:80/team/repo":                 "git.example.org/team/repo",
		"ssh://git@ssh.github.com:443/Example/sample-project": "github.com/example/sample-project",
		"git@ssh.github.com:Example/sample-project.git":       "github.com/example/sample-project",
	}
	for in, want := range cases {
		if got := NormalizeRemote(in); got != want {
			t.Errorf("NormalizeRemote(%q) = %q, want %q", in, got, want)
		}
		if got := NormalizeRemote(want); got != want {
			t.Errorf("NormalizeRemote output %q is not stable: got %q", want, got)
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
	want := `((project = '' AND branch = '' AND machine = '') OR (project = '' AND branch = '' AND machine = ?) OR (project = ? AND branch = '') OR (project = ? AND branch = ?))`
	if sql != want {
		t.Errorf("sql = %s", sql)
	}
	if !reflect.DeepEqual(args, []any{"workstation-a", "github.com/x/y", "github.com/x/y", "main"}) {
		t.Errorf("args = %v", args)
	}
}

// An entry with project AND machine belongs to the project: the project clauses
// carry no machine condition, so a session of the project reads it on any machine.
// The only clause that names a machine is the one without a project, so a session
// on that machine WITHOUT the project does not.
func TestUnionWhereReadsProjectMachineEntriesOnAnyMachine(t *testing.T) {
	sql, _ := Axes{Project: "github.com/x/y", Machine: "build-1"}.UnionWhere()
	for _, clause := range []string{"(project = ? AND branch = '')"} {
		if !strings.Contains(sql, clause) {
			t.Errorf("clause %q missing: %s", clause, sql)
		}
	}
	if strings.Contains(sql, "project = ? AND branch = '' AND machine") {
		t.Errorf("a project clause still narrows by machine: %s", sql)
	}
	// Without the project in the context, no clause can reach a project entry.
	sql, _ = Axes{Machine: "build-1"}.UnionWhere()
	if strings.Contains(sql, "project = ?") {
		t.Errorf("a session without the project reaches project entries: %s", sql)
	}
	// Every branch of the project, with or without a machine.
	sql, args := Axes{Project: "github.com/x/y", AnyBranch: true}.UnionWhere()
	if !strings.Contains(sql, "(project = ?)") || len(args) != 1 {
		t.Errorf("any-branch sql = %s args = %v", sql, args)
	}
}

// Requests have no machine; their clauses stay exactly as they were.
func TestRequestUnionWhereKeepsTheMachineCondition(t *testing.T) {
	sql, _ := Axes{Project: "github.com/x/y", Machine: "build-1"}.RequestUnionWhere()
	if !strings.Contains(sql, "(project = ? AND branch = '' AND machine = '')") {
		t.Errorf("request sql = %s", sql)
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

// Normalizing twice changes nothing: gates and the store must see the same id.
func FuzzNormalizeRemoteIsIdempotent(f *testing.F) {
	for _, seed := range []string{
		"https://github.com:alice/repo", "ssh://git@github.com:22/a/b", "https://evil.com:abc/x",
		"https://a@b@c/x", "https://https://x/y", "git@host:1/x", "ssh://u@h:/p", "https://h:/x",
		"git://x:y:z/w", "https://github.com:443/a/b.git/", "  HTTP://WWW.X.ORG:8080/A/ ",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		once := NormalizeRemote(in)
		if twice := NormalizeRemote(once); twice != once {
			t.Fatalf("NormalizeRemote(%q) = %q, again %q", in, once, twice)
		}
	})
}
