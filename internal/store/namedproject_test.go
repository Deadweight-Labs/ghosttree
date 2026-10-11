package store

import (
	"errors"
	"testing"
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

func TestANamedProjectIsNeverCreatedImplicitlyOrClaimed(t *testing.T) {
	st := orgStore(t, "robin")
	mustOrg(t, st, "person:1", "Alpha", "alpha")
	// A stale link or a typo must not conjure a project out of a write.
	if _, err := st.EnsureProject("person:1", "alpha/ghost"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("EnsureProject on a name: %v", err)
	}
	if _, err := st.ClaimProject("person:1", "alpha/ghost", "alpha"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("ClaimProject on a name: %v", err)
	}
	if _, ok := st.ProjectByRemote("alpha/ghost"); ok {
		t.Error("a project appeared")
	}
}
