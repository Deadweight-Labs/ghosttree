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
