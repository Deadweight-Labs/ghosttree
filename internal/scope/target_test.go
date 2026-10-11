package scope

import (
	"errors"
	"strings"
	"testing"
)

func TestResolveProject(t *testing.T) {
	writable := []string{"github.com/acme/ghosttree", "github.com/acme/infra", "gitlab.com/other/infra", "notes"}
	for _, tc := range []struct {
		in, want string
		err      error
	}{
		{"github.com/acme/ghosttree", "github.com/acme/ghosttree", nil},
		{"https://GitHub.com/acme/ghosttree.git", "github.com/acme/ghosttree", nil},
		{"git@github.com:acme/ghosttree.git", "github.com/acme/ghosttree", nil},
		{"ghosttree", "github.com/acme/ghosttree", nil},
		{"acme/ghosttree", "github.com/acme/ghosttree", nil},
		{"notes", "notes", nil}, // a project without a host is a project like any other
		{"github.com/nobody/x", "", ErrProjectUnresolved},
		{"nothing", "", ErrProjectUnresolved},
		{"", "", ErrProjectUnresolved},
		{"host", "", ErrProjectUnresolved}, // a short name is the last path part, not any substring
	} {
		got, err := ResolveProject(tc.in, writable)
		if got != tc.want || !errors.Is(err, tc.err) {
			t.Errorf("ResolveProject(%q) = %q, %v; want %q, %v", tc.in, got, err, tc.want, tc.err)
		}
	}
	_, err := ResolveProject("infra", writable)
	var amb *ErrProjectAmbiguous
	if !errors.As(err, &amb) || len(amb.Matches) != 2 {
		t.Errorf("an ambiguous short name must say so: %v", err)
	}
	if got, err := ResolveProject("github.com/acme/infra", writable); err != nil || got != "github.com/acme/infra" {
		t.Errorf("the full remote disambiguates: %q %v", got, err)
	}
}

func TestResolveProjectFindsANamedProjectByItsShortName(t *testing.T) {
	writable := []string{"acme/infra", "acme/sky-go", "github.com/acme/ghosttree"}
	for in, want := range map[string]string{"infra": "acme/infra", "sky-go": "acme/sky-go", "acme/infra": "acme/infra", "ghosttree": "github.com/acme/ghosttree"} {
		got, err := ResolveProject(in, writable)
		if err != nil || got != want {
			t.Errorf("ResolveProject(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

// A named project and a repository that end in the same name are never told
// apart silently, and neither are two named projects of different organizations.
func TestResolveProjectIsAmbiguousBetweenANamedProjectAndARemote(t *testing.T) {
	var amb *ErrProjectAmbiguous
	for _, tc := range []struct {
		in       string
		writable []string
	}{
		{"infra", []string{"acme/infra", "github.com/acme/infra"}},
		{"infra", []string{"acme/infra", "beta/infra"}},
		{"acme/infra", []string{"acme/infra", "github.com/acme/infra"}},
	} {
		if _, err := ResolveProject(tc.in, tc.writable); !errors.As(err, &amb) || len(amb.Matches) != 2 {
			t.Errorf("ResolveProject(%q, %v) = %v, want ambiguity", tc.in, tc.writable, err)
		}
	}
	// The full remote and the full id each name exactly one.
	w := []string{"acme/infra", "github.com/acme/infra"}
	if got, err := ResolveProject("github.com/acme/infra", w); err != nil || got != "github.com/acme/infra" {
		t.Errorf("full remote = %q %v", got, err)
	}
}

func TestIsNamedProjectNeverMatchesARemote(t *testing.T) {
	for in, want := range map[string]bool{
		"acme/infra": true, "a/b": true, "my-org/sky-go": true,
		"infra": false, "github.com/acme/infra": false, "github.com/infra": false,
		"acme/infra.local": false, "": false, "-x/y": false, "x/y-": false,
		"Acme/infra": false, "acme/in fra": false, "acme/": false, "/infra": false,
		"acme/" + strings.Repeat("a", 41): false,
	} {
		if got := IsNamedProject(in); got != want {
			t.Errorf("IsNamedProject(%q) = %v, want %v", in, got, want)
		}
	}
	// Every normalized remote with a host carries a dot in its first segment.
	for _, remote := range []string{"https://github.com/a/b.git", "git@host.example:a/b", "ssh://host.example/a", "git@gitlab.com:g/s/p.git"} {
		if n := NormalizeRemote(remote); IsNamedProject(n) {
			t.Errorf("remote %q normalizes to %q, which looks like a named project", remote, n)
		}
	}
	if !IsProjectName("infra") || IsProjectName("a/b") || IsProjectName("A") {
		t.Error("IsProjectName")
	}
}

func TestRequestUnionWhereLeavesProjectlessRequestsOut(t *testing.T) {
	ax := Axes{Project: "github.com/x/y", Branch: "main", Machine: "box"}
	where, _ := ax.RequestUnionWhere()
	if strings.Contains(where, "project = ''") {
		t.Errorf("a request without a project is in the union: %s", where)
	}
	if !strings.Contains(where, "project = ?") {
		t.Errorf("the project's own requests are gone: %s", where)
	}
	// Knowledge keeps the union, machine facts included.
	kw, _ := ax.UnionWhere()
	if !strings.Contains(kw, "project = '' AND branch = '' AND machine = ?") {
		t.Errorf("the knowledge union lost the machine clause: %s", kw)
	}
	// Without a project in the context nothing narrows the ledger.
	if w, _ := (Axes{Machine: "box"}).RequestUnionWhere(); !strings.Contains(w, "project = ''") {
		t.Errorf("no project, no ledger at all: %s", w)
	}
}
