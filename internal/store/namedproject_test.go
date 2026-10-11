package store

import (
	"errors"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestAMemberCreatesANamedProjectAndBecomesItsOwner(t *testing.T) {
	st := orgStore(t, "robin", "anna", "ben")
	org := mustOrg(t, st, "person:1", "Alpha", "alpha")
	code, _, err := st.CreateInvitation("person:1", org.ID, "", OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}

	// A plain member may create one; the id carries the organization.
	p, err := st.CreateNamedProject("person:2", "infra", "alpha")
	if err != nil {
		t.Fatalf("member creates: %v", err)
	}
	if p.Remote != "alpha/infra" || p.Org != "alpha" || p.Name != "infra" {
		t.Fatalf("project = %+v", p)
	}
	if role := st.ProjectRole("alpha/infra", "person:2").Role; role != RoleOwner {
		t.Errorf("creator role = %q, want owner", role)
	}
	if role := st.ProjectRole("alpha/infra", "person:1").Role; role != RoleOwner {
		t.Errorf("org owner role = %q, want owner", role)
	}
	if role := st.ProjectRole("alpha/infra", "person:3").Role; role != "" {
		t.Errorf("outsider role = %q", role)
	}
	list, err := st.Access(Principal{ID: "person:2"}).WritableProjects()
	if err != nil || len(list) != 1 || list[0] != "alpha/infra" {
		t.Errorf("writable = %v, %v", list, err)
	}
}

func TestANamedProjectNeedsAnOrganizationAndAValidName(t *testing.T) {
	st := orgStore(t, "robin", "anna", "ben")
	mustOrg(t, st, "person:1", "Alpha", "alpha")
	if _, err := st.CreateNamedProject("person:2", "notes", ""); !errors.Is(err, ErrNoOrg) {
		t.Errorf("no organization: %v", err)
	}
	if _, err := st.CreateNamedProject("person:2", "notes", "alpha"); !errors.Is(err, ErrNotOrgMember) {
		t.Errorf("not a member of the named organization: %v", err)
	}
	for _, bad := range []string{"", " ", "Infra", "a/b", "github.com/a/b", "a.b", "-x", "x-", "with space", "a\x1b[2J", "waytoolongwaytoolongwaytoolongwaytoolongwaytoolong"} {
		if _, err := st.CreateNamedProject("person:1", bad, ""); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("name %q: %v, want invalid input", bad, err)
		}
	}
}

// The name is taken per organization: two organizations may both have an infra,
// and a refusal inside one's own organization says nothing about another's.
func TestANamedProjectNameIsUniquePerOrganization(t *testing.T) {
	st := orgStore(t, "robin", "anna", "ben")
	mustOrg(t, st, "person:1", "Alpha", "alpha")
	other := mustOrg(t, st, "person:3", "Beta", "beta")
	if _, err := st.CreateNamedProject("person:1", "infra", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateNamedProject("person:1", "infra", ""); !errors.Is(err, ErrProjectNameTaken) {
		t.Errorf("duplicate in the same organization: %v", err)
	}
	p, err := st.CreateNamedProject("person:3", "infra", other.Slug)
	if err != nil || p.Remote != "beta/infra" {
		t.Errorf("same name in another organization: %+v, %v", p, err)
	}
	// Someone in neither organization learns nothing about the names.
	if _, err := st.CreateNamedProject("person:2", "infra", "alpha"); errors.Is(err, ErrProjectNameTaken) {
		t.Errorf("an outsider was told the name exists: %v", err)
	}
}

// A named project is a property of the project's row. A write never turns an
// existing named project into a plain one, and never claims it.
func TestANamedProjectIsNeverClaimedOrReassignedByAWrite(t *testing.T) {
	st := orgStore(t, "robin")
	mustOrg(t, st, "person:1", "Alpha", "alpha")
	if _, err := st.CreateNamedProject("person:1", "infra", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureProject("person:1", "alpha/infra"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("EnsureProject on a named project: %v", err)
	}
	if _, err := st.ClaimProject("person:1", "alpha/infra", "alpha"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("ClaimProject on a named project: %v", err)
	}
	if p, ok := st.ProjectByRemote("alpha/infra"); !ok || !p.Named {
		t.Errorf("project = %+v, %v", p, ok)
	}
	list, _ := st.ListProjects("person:1", 0)
	if len(list) != 1 || !list[0].Named {
		t.Errorf("ListProjects = %+v", list)
	}
}

// Remotes without a dotted host and with exactly two segments have the shape
// "a/b" but are repositories: they stay implicit remote projects, can be claimed,
// and a named project cannot take their id.
func TestHostlessTwoSegmentRemotesStayRemoteProjects(t *testing.T) {
	st := orgStore(t, "robin", "anna")
	mustOrg(t, st, "person:1", "Alpha", "alpha")
	for _, raw := range []string{"ssh://gitserver/repo.git", "git@gitserver:repo.git", "localhost/repo"} {
		id := scope.NormalizeRemote(raw)
		if id != "gitserver/repo" && id != "localhost/repo" {
			t.Fatalf("fixture: %q normalized to %q", raw, id)
		}
		p, err := st.EnsureProject("person:1", raw)
		if err != nil || p.Remote != id || p.Named {
			t.Errorf("EnsureProject(%q) = %+v, %v; want an implicit remote project", raw, p, err)
		}
		if again, err := st.ClaimProject("person:1", raw, "alpha"); err != nil || again.Remote != id {
			t.Errorf("ClaimProject(%q) = %+v, %v", raw, again, err)
		}
	}
	// A named project whose id equals a repository's is refused.
	st2 := orgStore(t, "robin")
	mustOrg(t, st2, "person:1", "Gitserver", "gitserver")
	if _, err := st2.EnsureProject("person:1", "git@gitserver:repo.git"); err != nil {
		t.Fatal(err)
	}
	if _, err := st2.CreateNamedProject("person:1", "repo", "gitserver"); !errors.Is(err, ErrProjectNameTaken) {
		t.Errorf("named project over a remote project: %v", err)
	}
	if p, _ := st2.ProjectByRemote("gitserver/repo"); p.Named {
		t.Error("the repository project was turned into a named one")
	}
}
