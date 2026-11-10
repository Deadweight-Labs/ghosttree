package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func orgStore(t *testing.T, names ...string) *Store {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, n := range names {
		if _, err := st.AddPerson(n); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func mustOrg(t *testing.T, st *Store, owner, name, slug string) Org {
	t.Helper()
	o, err := st.CreateOrg(owner, name, slug)
	if err != nil {
		t.Fatalf("create org %s: %v", name, err)
	}
	return o
}

func TestOrgCreateAndMembers(t *testing.T) {
	st := orgStore(t, "robin", "anna", "ben")
	o := mustOrg(t, st, "person:1", "Deadweight Labs", "")
	if o.Slug != "deadweight-labs" {
		t.Fatalf("slug derived from name: %q", o.Slug)
	}
	if _, err := st.CreateOrg("person:2", "Other", "deadweight-labs"); !errors.Is(err, ErrOrgSlugTaken) {
		t.Fatalf("duplicate slug: %v", err)
	}
	for _, bad := range []string{"Bad Slug", "-x", "x-", "a/b", "ü"} {
		if _, err := st.CreateOrg("person:1", "X", bad); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("slug %q accepted: %v", bad, err)
		}
	}
	if _, err := st.CreateOrg("person:1", "  ", "x1"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty name: %v", err)
	}
	if st.OrgRole(o.ID, "person:1") != OrgOwner || st.OrgRole(o.ID, "person:2") != "" {
		t.Fatal("creator must be owner, others nothing")
	}

	code, _, err := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	code, _, _ = st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	if _, err := st.AcceptInvitation("person:3", code); err != nil {
		t.Fatal(err)
	}
	members, err := st.ListOrgMembers(o.ID)
	if err != nil || len(members) != 3 {
		t.Fatalf("members = %+v %v", members, err)
	}

	// Nur Owner ändern Rollen und entfernen fremde Mitglieder.
	if err := st.SetOrgRole("person:2", o.ID, "person:3", OrgOwner); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("member promoting: %v", err)
	}
	if err := st.RemoveOrgMember("person:2", o.ID, "person:3"); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("member removing another: %v", err)
	}
	if err := st.SetOrgRole("person:1", o.ID, "person:2", OrgOwner); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgRole("person:2", o.ID, "person:1", OrgMember); err != nil {
		t.Fatalf("owner demoting another owner: %v", err)
	}
	if err := st.SetOrgRole("person:2", o.ID, "person:2", OrgMember); !errors.Is(err, ErrLastOrgOwner) {
		t.Fatalf("last owner demoted: %v", err)
	}
	if err := st.RemoveOrgMember("person:2", o.ID, "person:2"); !errors.Is(err, ErrLastOrgOwner) {
		t.Fatalf("last owner removed: %v", err)
	}
	if err := st.SetOrgRole("person:2", o.ID, "person:3", "admin"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown role: %v", err)
	}
	// Ein Mitglied darf sich selbst entfernen; der Standard fällt weg.
	if err := st.RemoveOrgMember("person:3", o.ID, "person:3"); err != nil {
		t.Fatal(err)
	}
	if orgs, _ := st.ListOrgs("person:3"); len(orgs) != 0 {
		t.Fatalf("left org still listed: %+v", orgs)
	}
	var def int64
	st.db.QueryRow(`SELECT default_org_id FROM persons WHERE id=3`).Scan(&def)
	if def != 0 {
		t.Fatalf("default org kept after leaving: %d", def)
	}
	if err := st.RemoveOrgMember("person:2", o.ID, "person:3"); !errors.Is(err, ErrNotOrgMember) {
		t.Fatalf("removing a non-member: %v", err)
	}
}

func TestAccountInSeveralOrgsAndDefault(t *testing.T) {
	st := orgStore(t, "robin", "anna")
	a := mustOrg(t, st, "person:1", "Alpha", "alpha")
	b := mustOrg(t, st, "person:1", "Beta", "beta")
	for _, o := range []Org{a, b} {
		code, _, _ := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
		if _, err := st.AcceptInvitation("person:2", code); err != nil {
			t.Fatal(err)
		}
	}
	orgs, err := st.ListOrgs("person:2")
	if err != nil || len(orgs) != 2 || orgs[0].Slug != "alpha" || !orgs[0].Default || orgs[1].Default {
		t.Fatalf("first joined org becomes the default: %+v %v", orgs, err)
	}
	if err := st.SetDefaultOrg("person:2", b.ID); err != nil {
		t.Fatal(err)
	}
	if orgs, _ = st.ListOrgs("person:2"); !orgs[1].Default || orgs[0].Default {
		t.Fatalf("default not switched: %+v", orgs)
	}
	c := mustOrg(t, st, "person:1", "Gamma", "gamma")
	if err := st.SetDefaultOrg("person:2", c.ID); !errors.Is(err, ErrNotOrgMember) {
		t.Fatalf("default into foreign org: %v", err)
	}
}

func TestProjectAssignmentRule(t *testing.T) {
	st := orgStore(t, "robin", "anna", "ben", "carl", "gus")
	st.SetAccessMode(AccessMode{Enforce: true})
	alpha := mustOrg(t, st, "person:1", "Alpha", "alpha")
	beta := mustOrg(t, st, "person:1", "Beta", "beta")
	_ = beta
	code, _, _ := st.CreateInvitation("person:1", alpha.ID, "", OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	// alpha verbindet acme mit Präfix; anna ist dort Mitglied.
	if _, err := st.ConnectSourceWithInstallation("person:1", alpha.ID, SourceGitHub, "acme", 9, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimPrefix("person:1", alpha.ID, "github.com/acme/*", VisibilityShared); err != nil {
		t.Fatal(err)
	}
	// Robins GitHub-Login ist von GitHub bestätigt (github_id > 0), Carls nicht.
	if err := st.SetAccountGitHub("person:1", "robin-gh", 42); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountGitHub("person:4", "carl-gh", 0); err != nil {
		t.Fatal(err)
	}

	// 2a: ein Schreibzugriff beansprucht nur über das Präfix einer Org, in der man
	// Mitglied ist, oder über das eigene bestätigte Login.
	if p, err := st.EnsureProject("person:2", "github.com/acme/app"); err != nil || p.Org != "alpha" {
		t.Fatalf("member write under the prefix: %+v %v", p, err)
	}
	if p, err := st.EnsureProject("person:1", "github.com/robin-gh/tool"); err != nil || p.Org != "robin-gh" || p.OrgKind != OrgKindUser {
		t.Fatalf("own verified login: %+v %v", p, err)
	}
	if _, err := st.EnsureProject("person:2", "github.com/robin-gh/other"); !errors.Is(err, ErrProjectClaimed) {
		t.Fatalf("somebody else's verified login: %v", err)
	}
	// 2b: sonst bleibt die Remote unbeansprucht, der Schreibzugriff geht durch.
	for who, remote := range map[string]string{"person:4": "github.com/carl-gh/x", "person:2": "github.com/some-one/tool", "person:3": "gitlab.com/x/y"} {
		if p, err := st.EnsureProject(who, remote); err != nil || p.ID != 0 {
			t.Fatalf("%s writing %s: %+v %v", who, remote, p, err)
		}
		if _, ok := st.ProjectByRemote(remote); ok {
			t.Fatalf("%s claimed %s by writing", who, remote)
		}
	}
	// 2c: ausdrücklich nur ohne fremde Einträge, sonst strittig mit Arten und Anzahl.
	if _, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "t", Body: "b", Scope: scope.Axes{Project: "gitlab.com/some-one/tool"}, Person: "anna"}); err != nil {
		t.Fatal(err)
	}
	var contested *ContestedError
	if _, err := st.ClaimProject("person:3", "gitlab.com/some-one/tool", ""); !errors.As(err, &contested) || contested.Kinds["knowledge"] != 1 {
		t.Fatalf("claim over anna's entry: %v", err)
	}
	if p, err := st.ClaimProject("person:2", "gitlab.com/some-one/tool", ""); err != nil || p.Org != "anna" {
		t.Fatalf("anna claims her own: %+v %v", p, err)
	}
	// Review point 1: a github.com repository under a login nobody here
	// verified is claimed explicitly by nobody but the instance admin.
	for _, who := range []string{"person:2", "person:3"} {
		if _, err := st.ClaimProject(who, "github.com/some-one/tool", ""); !errors.Is(err, ErrNotGitHubOwner) {
			t.Fatalf("%s claimed github.com/some-one/tool: %v", who, err)
		}
	}
	if _, err := st.ClaimContested("person:2", "gitlab.com/x/y", "alpha"); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("a non-admin decided: %v", err)
	}
	if p, err := st.ClaimContested("person:1", "gitlab.com/x/y", "alpha"); err != nil || p.Org != "alpha" {
		t.Fatalf("the admin decides: %+v %v", p, err)
	}
	// Ausdrücklich in eine Org: Mitglieder ja (sie werden Projekt-Owner), Fremde nicht.
	if p, err := st.ClaimProject("person:2", "gitlab.com/x/member", "alpha"); err != nil || p.Org != "alpha" {
		t.Fatalf("explicit claim by a member: %+v %v", p, err)
	}
	if role := st.ProjectRole("gitlab.com/x/member", "person:2"); role.Role != RoleOwner || role.Implicit {
		t.Fatalf("the member who created it = %+v", role)
	}
	if _, err := st.ClaimProject("person:4", "gitlab.com/x/mine", "alpha"); !errors.Is(err, ErrNotOrgMember) {
		t.Fatalf("claim into a foreign org: %v", err)
	}
	if _, err := st.ClaimProject("person:2", "bad remote", ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid remote: %v", err)
	}
	// Wegwerf-Remotes werden nie Projekte, auch nicht ausdrücklich.
	for _, raw := range []string{"/home/u/.cache/run-1/origin", "https://benchmark.invalid/x", "file:///tmp/r"} {
		if _, err := st.EnsureProject("person:1", raw); !errors.Is(err, ErrInvalidRemote) {
			t.Fatalf("throwaway %q: %v", raw, err)
		}
		if _, err := st.ClaimProject("person:1", raw, "alpha"); !errors.Is(err, ErrInvalidRemote) && !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("throwaway claim %q: %v", raw, err)
		}
	}
	// Befund 1: ein Nur-Gast bekommt keinen Namensraum, weder durch Schreiben
	// noch ausdrücklich, und bleibt Gast.
	pi, _, err := st.CreateProjectInvitation("person:1", alpha.ID, "github.com/acme/app", RoleGuest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:5", pi); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountGitHub("person:5", "gus-gh", 77); err != nil {
		t.Fatal(err)
	}
	for i, try := range []func() error{
		func() error { _, err := st.EnsureProject("person:5", "github.com/gus-gh/x"); return err },
		func() error { _, err := st.ClaimProject("person:5", "github.com/elsewhere/y", ""); return err },
		func() error { _, err := st.CreateNamedProject("person:5", "notes", ""); return err },
	} {
		if err := try(); !errors.Is(err, ErrGuestOnly) {
			t.Fatalf("try %d: a guest got a project: %v", i, err)
		}
	}
	if _, ok := st.UserNamespace("person:5"); ok {
		t.Fatal("a guest got a namespace")
	}
	if got := st.OrgViewerKind(alpha.ID, "person:5"); got != OrgViewerGuest {
		t.Fatalf("the guest is now %q", got)
	}
	// Ein Dienst legt nichts an und hat keinen Namensraum.
	other := orgStore(t, "robin", "bot")
	mustOrg(t, other, "person:1", "Alpha", "alpha")
	if err := other.SetAccountKindAsOperator(2, AccountKindService, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := other.ClaimProject("person:2", "github.com/x/bot", ""); !errors.Is(err, ErrServiceAccount) {
		t.Fatalf("service creates a project: %v", err)
	}
	if _, err := other.EnsureUserNamespace("person:2"); !errors.Is(err, ErrServiceAccount) {
		t.Fatalf("service namespace: %v", err)
	}
}

func TestMoveProject(t *testing.T) {
	st := orgStore(t, "robin", "anna")
	alpha := mustOrg(t, st, "person:1", "Alpha", "alpha")
	beta := mustOrg(t, st, "person:1", "Beta", "beta")
	code, _, _ := st.CreateInvitation("person:1", alpha.ID, "", OrgMember, 0)
	st.AcceptInvitation("person:2", code)
	if _, err := st.ClaimProject("person:1", "github.com/x/y", "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MoveProject("person:2", "github.com/x/y", "beta"); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("member moving: %v", err)
	}
	p, err := st.MoveProject("person:1", "github.com/x/y", "beta")
	if err != nil || p.Org != "beta" || p.OrgID != beta.ID {
		t.Fatalf("move: %+v %v", p, err)
	}
	// Anna ist in Beta nichts: sie sieht das Projekt nicht mehr in ihrer Liste.
	if list, _ := st.ListProjects("person:2", 0); len(list) != 0 {
		t.Fatalf("projects of another org listed: %+v", list)
	}
	if list, _ := st.ListProjects("person:1", 0); len(list) != 1 {
		t.Fatalf("owner's projects: %+v", list)
	}
	// Owner nur der Quelle, nicht des Ziels.
	gamma := mustOrg(t, st, "person:2", "Gamma", "gamma")
	if _, err := st.MoveProject("person:2", "github.com/x/y", gamma.Slug); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("moving without owning the source: %v", err)
	}
	if _, err := st.MoveProject("person:1", "github.com/x/y", gamma.Slug); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("moving without owning the target: %v", err)
	}
	if _, err := st.MoveProject("person:1", "github.com/x/none", "alpha"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
	if _, err := st.MoveProject("person:1", "github.com/x/y", "nope"); !errors.Is(err, ErrOrgNotFound) {
		t.Fatalf("unknown org: %v", err)
	}
}

func TestInvitationLifecycle(t *testing.T) {
	st := orgStore(t, "robin", "anna", "ben")
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")

	if _, _, err := st.CreateInvitation("person:2", o.ID, "", OrgMember, 0); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("non-owner inviting: %v", err)
	}
	if _, _, err := st.CreateInvitation("person:1", o.ID, "", "boss", 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad role: %v", err)
	}
	if _, _, err := st.CreateInvitation("person:1", o.ID, "not-an-email", OrgMember, 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad email: %v", err)
	}
	if _, _, err := st.CreateInvitation("person:1", o.ID, "", OrgMember, MaxInvitationTTL+time.Hour); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ttl above the cap: %v", err)
	}

	code, inv, err := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	if err != nil || inv.Status != "pending" || code == "" {
		t.Fatalf("invite: %+v %v", inv, err)
	}
	var stored string
	st.db.QueryRow(`SELECT code_hash FROM invitations WHERE id=?`, inv.ID).Scan(&stored)
	if stored == code || stored != hashToken(code) {
		t.Fatal("only the hash may be stored")
	}
	// Falscher Code.
	if _, err := st.AcceptInvitation("person:2", "wrong"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("wrong code: %v", err)
	}
	// Einmalig.
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:3", code); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("second use: %v", err)
	}
	if st.OrgRole(o.ID, "person:3") != "" {
		t.Fatal("a used code must not admit anyone else")
	}
	// Abgelaufen.
	code, inv, _ = st.CreateInvitation("person:1", o.ID, "", OrgMember, time.Hour)
	st.db.Exec(`UPDATE invitations SET expires_at=? WHERE id=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), inv.ID)
	if _, err := st.AcceptInvitation("person:3", code); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("expired: %v", err)
	}
	list, _ := st.ListInvitations("person:1", o.ID)
	if list[0].Status != "expired" || list[1].Status != "accepted" {
		t.Fatalf("statuses: %+v", list)
	}
	// Zurückgezogen.
	code, inv, _ = st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	if err := st.RevokeInvitation("person:2", o.ID, inv.ID); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("member revoking: %v", err)
	}
	if err := st.RevokeInvitation("person:1", o.ID, inv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:3", code); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("revoked: %v", err)
	}
	// Bereits Mitglied: Fehler, Code bleibt für den Richtigen.
	code, _, _ = st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); !errors.Is(err, ErrAlreadyMember) {
		t.Fatalf("already member: %v", err)
	}
	if _, err := st.AcceptInvitation("person:3", code); err != nil {
		t.Fatalf("the unused code must still work: %v", err)
	}
	// Wer eingeladen hat, verliert das Owner-Recht: offene Einladungen sind tot.
	code, _, _ = st.CreateInvitation("person:1", o.ID, "", OrgOwner, 0)
	st.SetOrgRole("person:1", o.ID, "person:2", OrgOwner)
	st.SetOrgRole("person:2", o.ID, "person:1", OrgMember)
	st.RemoveOrgMember("person:2", o.ID, "person:3")
	if _, err := st.AcceptInvitation("person:3", code); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("invitation of a demoted inviter: %v", err)
	}
}

func TestInvitationCap(t *testing.T) {
	st := orgStore(t, "robin")
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")
	for i := 0; i < maxPendingInvitations; i++ {
		if _, _, err := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0); err != nil {
			t.Fatalf("invite %d: %v", i, err)
		}
	}
	if _, _, err := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0); !errors.Is(err, ErrTooManyInvites) {
		t.Fatalf("pending cap: %v", err)
	}
}

// Eine an eine E-Mail gebundene Einladung geht nur mit einer verifizierten,
// gleichen Adresse. Unverifiziert (leer) oder fremd verbraucht sie nicht und
// legt kein Konto an. Ohne gebundene E-Mail zählt allein der Code.
func TestInvitationEmailBinding(t *testing.T) {
	st := orgStore(t, "robin")
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")
	code, _, err := st.CreateInvitation("person:1", o.ID, "Anna@Example.com", OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	in := IdentityLogin{Issuer: "https://idp", Subject: "s-anna", Name: "anna", Code: code}
	// Unverifizierte E-Mail kommt als leerer String an (siehe oidcCallback).
	if _, _, err := st.LoginIdentity(in); !errors.Is(err, ErrInvitationEmail) {
		t.Fatalf("unverified email: %v", err)
	}
	in.Email = "mallory@example.com"
	if _, _, err := st.LoginIdentity(in); !errors.Is(err, ErrInvitationEmail) {
		t.Fatalf("other email: %v", err)
	}
	if accts, _ := st.ListAccounts(); len(accts) != 1 {
		t.Fatalf("a refused invitation must not create an account: %+v", accts)
	}
	if _, err := st.InviteLocal(code, "anna"); !errors.Is(err, ErrInvitationEmail) {
		t.Fatalf("local flow cannot verify an email: %v", err)
	}
	in.Email = "ANNA@example.com"
	acct, outcome, err := st.LoginIdentity(in)
	if err != nil || outcome != LoginInvited || acct.Name != "anna" || acct.Email != "ANNA@example.com" {
		t.Fatalf("matching verified email: %+v %v %v", acct, outcome, err)
	}
	if st.OrgRole(o.ID, acct.ID) != OrgMember {
		t.Fatal("membership missing")
	}

	// Ohne gebundene E-Mail: nur der Code zählt, auch ohne E-Mail-Claim.
	code, _, _ = st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	acct, outcome, err = st.LoginIdentity(IdentityLogin{Issuer: "https://idp", Subject: "s-ben", Name: "ben", Code: code})
	if err != nil || outcome != LoginInvited || st.OrgRole(o.ID, acct.ID) != OrgMember {
		t.Fatalf("code-only invitation: %+v %v %v", acct, outcome, err)
	}
	if acct.Admin {
		t.Fatal("an invited account is never an instance admin")
	}
}

func TestLoginWithInvitation(t *testing.T) {
	st := orgStore(t, "robin")
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")

	// Ohne Code oder mit falschem Code entsteht kein Konto.
	if _, _, err := st.LoginIdentity(IdentityLogin{Issuer: "i", Subject: "x"}); !errors.Is(err, ErrNoAccountForIdentity) {
		t.Fatalf("no code: %v", err)
	}
	if _, _, err := st.LoginIdentity(IdentityLogin{Issuer: "i", Subject: "x", Code: "nope"}); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("wrong code: %v", err)
	}
	code, _, _ := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	acct, outcome, err := st.LoginIdentity(IdentityLogin{Issuer: "i", Subject: "x", Name: "robin", Code: code})
	if err != nil || outcome != LoginInvited || acct.Name != "robin-2" {
		t.Fatalf("name clash must get a suffix: %+v %v %v", acct, outcome, err)
	}
	// Dieselbe Identität meldet sich wieder an: bestehend.
	if _, outcome, err := st.LoginIdentity(IdentityLogin{Issuer: "i", Subject: "x"}); err != nil || outcome != LoginExisting {
		t.Fatalf("second login: %v %v", outcome, err)
	}
	// Und tritt per zweiter Einladung einer weiteren Org bei.
	b := mustOrg(t, st, "person:1", "Beta", "beta")
	code, _, _ = st.CreateInvitation("person:1", b.ID, "", OrgMember, 0)
	_, outcome, err = st.LoginIdentity(IdentityLogin{Issuer: "i", Subject: "x", Code: code})
	if err != nil || outcome != LoginJoined || st.OrgRole(b.ID, acct.ID) != OrgMember {
		t.Fatalf("known identity joining: %v %v", outcome, err)
	}
	// Ein erneuter Klick auf einen Link der eigenen Org meldet nur an.
	code, _, _ = st.CreateInvitation("person:1", b.ID, "", OrgMember, 0)
	if _, outcome, err := st.LoginIdentity(IdentityLogin{Issuer: "i", Subject: "x", Code: code}); err != nil || outcome != LoginExisting {
		t.Fatalf("already a member: %v %v", outcome, err)
	}
}

func TestInvitationBruteForceLock(t *testing.T) {
	st := orgStore(t, "robin")
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")
	code, _, _ := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	in := IdentityLogin{Issuer: "i", Subject: "guesser", Name: "g", Code: "guess"}
	for i := 0; i < maxInviteFailures; i++ {
		if _, _, err := st.LoginIdentity(in); !errors.Is(err, ErrCodeInvalid) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// Gesperrt, auch mit dem richtigen Code.
	in.Code = code
	if _, _, err := st.LoginIdentity(in); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("locked identity: %v", err)
	}
	// Eine andere Identität ist nicht betroffen.
	in.Subject = "bystander"
	if _, _, err := st.LoginIdentity(in); err != nil {
		t.Fatalf("other identity: %v", err)
	}
	// Das Fenster läuft ab.
	l := st.attemptLimiter()
	l.now = func() time.Time { return time.Now().Add(inviteFailureWindow + time.Second) }
	if l.blocked("i\x00guesser") {
		t.Fatal("lock must expire")
	}
}

func TestAttemptLimiterIsBounded(t *testing.T) {
	st := orgStore(t)
	l := st.attemptLimiter()
	for i := 0; i < maxLimiterKeys*3; i++ {
		l.note("k"+time.Duration(i).String(), ErrCodeInvalid)
	}
	if len(l.failures) > maxLimiterKeys {
		t.Fatalf("limiter holds %d keys", len(l.failures))
	}
	l.note("x", errors.New("other"))
	if _, ok := l.failures["x"]; ok {
		t.Fatal("only code failures count")
	}
}

func TestBootstrapCreatesDefaultOrg(t *testing.T) {
	st := orgStore(t)
	code, ok, err := st.EnsureBootstrapCode()
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	acct, err := st.BootstrapLocal(code, "robin")
	if err != nil {
		t.Fatal(err)
	}
	orgs, err := st.ListOrgs(acct.ID)
	if err != nil || len(orgs) != 1 || orgs[0].Slug != "default" || orgs[0].Role != OrgOwner {
		t.Fatalf("orgs = %+v %v", orgs, err)
	}
}

func TestInviteLocalCreatesAccount(t *testing.T) {
	st := orgStore(t, "robin")
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")
	code, _, _ := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	if st.CodeKindFor(code) != CodeInvitation {
		t.Fatalf("kind = %q", st.CodeKindFor(code))
	}
	acct, err := st.InviteLocal(code, "anna")
	if err != nil || st.OrgRole(o.ID, acct.ID) != OrgMember {
		t.Fatalf("local invite: %+v %v", acct, err)
	}
	if st.CodeKindFor(code) != "" {
		t.Fatal("a used invitation must not look valid")
	}
	if _, err := st.InviteLocal(code, "again"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("reuse: %v", err)
	}
}

// Eine Datenbank vor Paket 5 hat keine Org-Tabellen. Öffnen legt die
// Default-Organisation an, macht person:1 zum Owner und ordnet alle Projekte
// zu; ein zweites Öffnen ändert nichts, und ein später entferntes Projekt oder
// Mitglied kehrt nicht zurück.
func TestOrgBackfillOnOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"robin", "philipp"} {
		if _, err := st.AddPerson(n); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.InsertKnowledge(Knowledge{Type: "pitfall", Title: "k", Body: "b", Scope: scope.Axes{Project: "github.com/dw/knowledge-only"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertSession(Session{Harness: "claude-code", ExternalID: "s", Scope: scope.Axes{Project: "github.com/dw/session-only", Machine: "m"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO coord_rooms(room_key, kind, label, created_at) VALUES('project:github.com/dw/room-only','project','x','t'),('machine:m','machine','x','t')`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// Zustand vor Paket 5: keine Org-Tabellen, kein Standard.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP TABLE org_events`, `DROP TABLE invitations`, `DROP TABLE projects`, `DROP TABLE org_members`, `DROP TABLE orgs`, `DROP TABLE org_state`,
		`UPDATE persons SET default_org_id=0`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	raw.Close()

	check := func(label string, want ...string) {
		t.Helper()
		st, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		orgs, err := st.ListOrgs("person:1")
		if err != nil || len(orgs) != 1 || orgs[0].Slug != "default" || orgs[0].Role != OrgOwner {
			t.Fatalf("%s: robin's orgs = %+v %v", label, orgs, err)
		}
		if orgs, _ := st.ListOrgs("person:2"); len(orgs) != 0 {
			t.Fatalf("%s: test person must stay without membership: %+v", label, orgs)
		}
		projects, err := st.ListProjects("person:1", 0)
		if err != nil || len(projects) != len(want) {
			t.Fatalf("%s: projects = %+v %v", label, projects, err)
		}
		for i, p := range projects {
			if p.Org != "default" || p.Remote != want[i] {
				t.Fatalf("%s: project %d = %+v, want %s in default", label, i, p, want[i])
			}
		}
	}
	all := []string{"github.com/dw/knowledge-only", "github.com/dw/room-only", "github.com/dw/session-only"}
	check("first open", all...)
	check("second open", all...)

	// Was der Betreiber danach ändert, überlebt den nächsten Start.
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM projects WHERE remote='github.com/dw/room-only'`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	check("after manual delete", all[0], all[2])
}

// Der Server läuft über die Schreibwarteschlange, die für jeden Auftrag eine
// frische Store-Kopie benutzt. Die Sperre nach Fehlversuchen muss das
// überstehen.
func TestOrgsThroughRuntimeWriter(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "rt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.writer == nil {
		t.Fatal("file-backed store must use the runtime writer")
	}
	st.AddPerson("robin")
	st.AddPerson("anna")
	o, err := st.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxInviteFailures; i++ {
		if _, err := st.AcceptInvitation("person:2", "wrong"); !errors.Is(err, ErrCodeInvalid) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := st.AcceptInvitation("person:2", code); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("lock must survive the writer's store copies: %v", err)
	}
	if _, err := claimInOrg(st, "person:1", "github.com/x/y"); err != nil {
		t.Fatal(err)
	}
	if p, ok := st.ProjectByRemote("github.com/x/y"); !ok || p.Org != "alpha" {
		t.Fatalf("project through the writer: %+v %v", p, ok)
	}
}

func TestForceMoveAndRename(t *testing.T) {
	st := orgStore(t, "robin", "anna")
	a := mustOrg(t, st, "person:1", "Alpha", "alpha")
	b := mustOrg(t, st, "person:2", "Beta", "beta")
	if _, err := claimOrPlace(st, "person:2", "github.com/x/y", "beta"); err != nil {
		t.Fatal(err)
	}
	// Admin-Weg ohne Rechteprüfung, protokolliert in beiden Orgs.
	p, err := st.ForceMoveProject("https://github.com/X/y.git", "alpha")
	if err != nil || p.OrgID != a.ID {
		t.Fatalf("force move: %+v %v", p, err)
	}
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM org_events WHERE action='force_move_project' AND subject='github.com/x/y'`).Scan(&n)
	if n != 2 {
		t.Fatalf("org_events entries = %d", n)
	}
	if _, err := st.ForceMoveProject("github.com/x/none", "alpha"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
	_ = b

	if _, err := st.RenameOrg("person:2", a.ID, "Hijack", ""); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("rename by a non-member: %v", err)
	}
	o, err := st.RenameOrg("person:1", a.ID, "Deadweight Labs", "")
	if err != nil || o.Name != "Deadweight Labs" || o.Slug != "alpha" {
		t.Fatalf("rename: %+v %v", o, err)
	}
	if o, err = st.RenameOrg("person:1", a.ID, "Deadweight Labs", "deadweight"); err != nil || o.Slug != "deadweight" {
		t.Fatalf("rename with slug: %+v %v", o, err)
	}
	if _, err := st.RenameOrg("person:1", a.ID, "X", "beta"); !errors.Is(err, ErrOrgSlugTaken) {
		t.Fatalf("slug clash: %v", err)
	}
	if _, err := st.RenameOrg("person:1", a.ID, " ", ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty name: %v", err)
	}
}
