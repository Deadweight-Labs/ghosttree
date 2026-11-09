package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Gast in A, Mitglied in B: die Gast-Sperre der Suche gilt je Projekt. Wer in A nur
// Gast ist, darf mit branch:/package:/for: nicht raten, nur weil er in B Mitglied ist.
func TestSearchGuestLockIsPerProjectNotPerHighestRole(t *testing.T) {
	e, _ := pullsEnv(t)
	// gina ist Gast in shellProject (A) und Mitglied im anderen Projekt (B).
	if err := e.St.SetProjectRole("person:1", shellHiddenProject, "person:3", store.RoleMember, false, store.RoleViaCLI); err != nil {
		t.Fatal(err)
	}
	norm := func(page, q string) string {
		return strings.ReplaceAll(strings.ReplaceAll(page, q, "X"), url.QueryEscape(q), "X")
	}
	ask := func(c string, project, q string) string {
		cl := e.Guest
		if c == "member" {
			cl = e.Member
		}
		u := e.Base + "/ui/search?q=" + url.QueryEscape(q)
		if project != "" {
			u += "&project=" + url.QueryEscape(project)
		}
		_, page := fetchPage(t, cl, u)
		return page
	}
	for _, tc := range []struct{ yes, no string }{
		{"branch:feat/x", "branch:feat/nope"},
		{"package:1", "package:77"},
		{"for:alice", "for:nobody"},
	} {
		for _, project := range []string{shellProject, ""} {
			yes, no := ask("guest", project, "Pull "+tc.yes), ask("guest", project, "Pull "+tc.no)
			if norm(yes, tc.yes) != norm(no, tc.no) {
				t.Errorf("project %q: %s and %s give the guest of A different answers", project, tc.yes, tc.no)
			}
			if project == shellProject && !strings.Contains(yes, "not offered to guests") {
				t.Errorf("project A: the guest is not told that %s is not offered", tc.yes)
			}
		}
	}
	// Ein Mitglied von A findet weiter.
	if p := ask("member", shellProject, "Pull branch:feat/x"); !strings.Contains(p, "Pull request tab") {
		t.Error("the member of A no longer finds by branch")
	}
	if p := ask("member", shellProject, "Pull branch:feat/nope"); strings.Contains(p, "Pull request tab") {
		t.Error("the member of A finds a request on a branch it is not on")
	}
}
