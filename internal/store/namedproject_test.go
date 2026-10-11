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

	// A plain member may create one; it lands in the organization they name.
	p, err := st.CreateNamedProject("person:2", "infra", "alpha")
	if err != nil {
		t.Fatalf("member creates: %v", err)
	}
	if p.Remote != "infra" || p.Org != "alpha" {
		t.Fatalf("project = %+v", p)
	}
	if role := st.ProjectRole("infra", "person:2").Role; role != RoleOwner {
		t.Errorf("creator role = %q, want owner", role)
	}
	// The organization owner is an implicit owner, like in any project of the org.
	if role := st.ProjectRole("infra", "person:1").Role; role != RoleOwner {
		t.Errorf("org owner role = %q, want owner", role)
	}
	// An organization member who was given nothing sees nothing.
	if role := st.ProjectRole("infra", "person:3").Role; role != "" {
		t.Errorf("outsider role = %q", role)
	}

	// Like a repository project it is writable for those with a role.
	list, err := st.Access(Principal{ID: "person:2"}).WritableProjects()
	if err != nil || len(list) != 1 || list[0] != "infra" {
		t.Errorf("writable = %v, %v", list, err)
	}
}

func TestANamedProjectNeedsAnOrganizationAndAValidUniqueName(t *testing.T) {
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
	if _, err := st.CreateNamedProject("person:1", "infra", ""); err != nil {
		t.Fatal(err)
	}
	// Unique across the instance, also towards another organization.
	other := mustOrg(t, st, "person:3", "Beta", "beta")
	if _, err := st.CreateNamedProject("person:3", "infra", other.Slug); !errors.Is(err, ErrProjectNameTaken) {
		t.Errorf("duplicate across organizations: %v", err)
	}
	if _, err := st.CreateNamedProject("person:1", "infra", ""); !errors.Is(err, ErrProjectNameTaken) {
		t.Errorf("duplicate: %v", err)
	}
}

func TestANamedProjectIsNeverCreatedImplicitlyOrClaimed(t *testing.T) {
	st := orgStore(t, "robin")
	mustOrg(t, st, "person:1", "Alpha", "alpha")
	// A stale marker or a typo must not conjure a project out of a write.
	if _, err := st.EnsureProject("person:1", "ghost"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("EnsureProject on a name: %v", err)
	}
	if _, err := st.ClaimProject("person:1", "ghost", "alpha"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("ClaimProject on a name: %v", err)
	}
	if _, ok := st.ProjectByRemote("ghost"); ok {
		t.Error("a project appeared")
	}
}
