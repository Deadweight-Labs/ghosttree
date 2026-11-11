package store

import (
	"errors"
	"testing"
)

// Robin's decision E10, linking a person's GitHub login: an unverified
// namespace with that name yields to "<slug>-<n>" without a redirect, its
// owner gets a notice and its named projects move along; the person's
// namespace takes the login; afterwards nobody takes it.
func TestVerifiedLoginTakesTheSlugFromAnUnverifiedNamespace(t *testing.T) {
	st := orgStore(t, "philipp", "carl")
	if _, err := st.EnsureUserNamespace("person:1"); err != nil {
		t.Fatal(err)
	}
	squat := mustOrg(t, st, "person:2", "Squat", "philipp-gh")
	if _, err := st.CreateNamedProject("person:2", "notes", "philipp-gh"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountGitHub("person:1", "Philipp-GH", 900); err != nil {
		t.Fatal(err)
	}
	ns, _ := st.UserNamespace("person:1")
	if ns.Slug != "philipp-gh" {
		t.Fatalf("the verified person's namespace = %q", ns.Slug)
	}
	moved, _ := st.OrgByRef("philipp-gh-2")
	if moved.ID != squat.ID {
		t.Fatalf("the unverified namespace did not yield: %+v", moved)
	}
	if _, ok := st.ProjectByRemote("philipp-gh-2/notes"); !ok {
		t.Fatal("its named project kept the old address")
	}
	if cur, ok := st.OrgSlugTarget("philipp-gh"); ok && cur == "philipp-gh-2" {
		t.Fatal("the old address redirects to the namespace that yielded")
	}
	notices, err := st.NamespaceNotices("person:2")
	if err != nil || len(notices) != 1 || notices[0].OldSlug != "philipp-gh" || notices[0].NewSlug != "philipp-gh-2" {
		t.Fatalf("notices = %+v %v", notices, err)
	}
	if err := st.MarkNamespaceNoticesSeen("person:2"); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.NamespaceNotices("person:2"); len(n) != 0 {
		t.Fatalf("seen notices stay: %+v", n)
	}
	var events int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM org_events WHERE action='slug_yielded' AND org_id=?`, squat.ID).Scan(&events)
	if events != 1 {
		t.Fatalf("slug_yielded events = %d", events)
	}
	// Nobody unverified takes it back.
	if _, err := st.CreateOrg("person:2", "Again", "philipp-gh"); !errors.Is(err, ErrOrgSlugTaken) {
		t.Fatalf("an unverified org took a verified login: %v", err)
	}
	if _, err := st.RenameOrg("person:2", squat.ID, "Squat", "philipp-gh"); !errors.Is(err, ErrOrgSlugTaken) {
		t.Fatalf("a rename took a verified login: %v", err)
	}
}

// E10, connecting an organization's GitHub source: an unverified namespace
// with the GitHub organization's name yields; following GitHub, the connected
// organization takes the address. A former slug reserves nothing against it.
func TestConnectedGitHubOrganizationTakesTheSlug(t *testing.T) {
	st := orgStore(t, "philipp", "carl")
	other := mustOrg(t, st, "person:2", "Holder", "lightco")
	// A former slug of somebody else's.
	hist := mustOrg(t, st, "person:2", "T3", "t3code")
	if _, err := st.RenameOrg("person:2", hist.ID, "T3", "t3-old"); err != nil {
		t.Fatal(err)
	}
	acme := mustOrg(t, st, "person:1", "Acme", "acme")
	if _, err := st.ConnectSourceWithInstallation("person:1", acme.ID, SourceGitHub, "lightco", 77, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.OrgByRef("lightco-2"); got.ID != other.ID {
		t.Fatalf("the unverified holder did not yield: %+v", got)
	}
	if err := st.SetOrgFollowGitHub("person:1", acme.ID, true); err != nil {
		t.Fatal(err)
	}
	got, err := st.MirrorGitHubOrg(acme.ID, "lightco", "lightco", "Lightco")
	if err != nil || got.Slug != "lightco" {
		t.Fatalf("mirror = %+v %v", got, err)
	}
	if n, _ := st.NamespaceNotices("person:2"); len(n) != 1 {
		t.Fatalf("notices of the owner who yielded = %+v", n)
	}
	if _, err := st.ConnectSourceWithInstallation("person:1", acme.ID, SourceGitHub, "t3code", 0, nil); err != nil {
		t.Fatal(err)
	}
	if cur, ok := st.OrgSlugTarget("t3code"); ok {
		t.Fatalf("a former slug still redirects against a verified owner: %q", cur)
	}
	if _, err := st.CreateOrg("person:2", "T3 again", "t3code"); !errors.Is(err, ErrOrgSlugTaken) {
		t.Fatalf("an unverified org took a connected organization's name: %v", err)
	}
	// A namespace verified for its slug keeps it: a person whose login is the
	// same name does not take it from the connected organization.
	if err := st.SetAccountGitHub("person:2", "lightco", 555); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.OrgByRef("lightco"); got.ID != acme.ID {
		t.Fatalf("a verified organization lost its address: %+v", got)
	}
}
