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
	// The organization comes later and takes the first segment as its slug: the
	// repository project stays what it was, and the name is simply taken.
	mustOrg(t, st, "person:1", "Gitserver", "gitserver")
	if _, err := st.CreateNamedProject("person:1", "repo", "gitserver"); !errors.Is(err, ErrProjectNameTaken) {
		t.Errorf("named project over a remote project: %v", err)
	}
	if p, ok := st.ProjectByRemote("gitserver/repo"); !ok || p.Named {
		t.Errorf("the repository project changed: %+v, %v", p, ok)
	}
	// Now the namespace belongs to the organization: no more implicit projects there.
	if _, err := st.EnsureProject("person:1", "git@gitserver:another.git"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("implicit project in an organization's namespace: %v", err)
	}
	// The project that existed before keeps working for writes.
	if p, err := st.EnsureProject("person:1", "git@gitserver:repo.git"); err != nil || p.Remote != "gitserver/repo" || p.Named {
		t.Errorf("existing repository project: %+v, %v", p, err)
	}
}

// The first segment of an organization's slug is its namespace: neither an
// outsider nor a member creates a repository project there by writing, so nobody
// can occupy a name an organization may want or probe which names it uses.
func TestAnOrganizationsNamespaceIsNotOpenToImplicitProjects(t *testing.T) {
	st := orgStore(t, "robin", "anna", "ben")
	org := mustOrg(t, st, "person:1", "Alpha", "alpha")
	code, _, err := st.CreateInvitation("person:1", org.ID, "", OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	mustOrg(t, st, "person:3", "Beta", "beta")
	if _, err := st.CreateNamedProject("person:1", "infra", "alpha"); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"person:1", "person:2", "person:3"} { // owner, member, outsider
		for _, id := range []string{"alpha/infra", "alpha/probe", "git@alpha:probe.git"} {
			if _, err := st.EnsureProject(who, id); !errors.Is(err, ErrProjectNotFound) {
				t.Errorf("%s EnsureProject(%s) = %v, want not found", who, id, err)
			}
		}
		if _, err := st.ClaimProject(who, "alpha/probe", "alpha"); !errors.Is(err, ErrProjectNotFound) {
			t.Errorf("%s ClaimProject(alpha/probe) = %v, want not found", who, err)
		}
	}
	if _, ok := st.ProjectByRemote("alpha/probe"); ok {
		t.Error("a project appeared in the namespace")
	}
	// Claiming an existing named project: a member is told what it is, an outsider only "not found".
	if _, err := st.ClaimProject("person:2", "alpha/infra", "alpha"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("member claims a named project: %v", err)
	}
	if _, err := st.ClaimProject("person:3", "alpha/infra", "beta"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("outsider claims a named project: %v", err)
	}
	// Other values are untouched: hosts with a dot and unrelated hostless names.
	for _, id := range []string{"github.com/alpha/probe", "elsewhere/probe"} {
		if p, err := st.EnsureProject("person:1", id); err != nil || p.Remote != id {
			t.Errorf("EnsureProject(%s) = %+v, %v", id, p, err)
		}
	}
}
