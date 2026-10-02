package main

import (
	"bytes"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type orgCLI struct {
	st  *store.Store
	url string
	tok map[string]string
}

func newOrgCLI(t *testing.T) *orgCLI {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &orgCLI{st: st, tok: map[string]string{}}
	for _, n := range []string{"robin", "anna"} {
		if _, err := st.AddAccount(n, "", n == "robin"); err != nil {
			t.Fatal(err)
		}
		if f.tok[n], _, err = st.CreateToken(n, store.TokenSpec{Label: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

// as führt ctx als dieses Konto aus.
func (f *orgCLI) as(t *testing.T, who string, args ...string) (int, string) {
	t.Helper()
	if err := config.Save(config.Config{ServerURL: f.url, Token: f.tok[who]}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := run(args, &out)
	return code, out.String()
}

func (f *orgCLI) ok(t *testing.T, who string, args ...string) string {
	t.Helper()
	code, out := f.as(t, who, args...)
	if code != 0 {
		t.Fatalf("ctx %s as %s = %d: %s", strings.Join(args, " "), who, code, out)
	}
	return out
}

var codeLine = regexp.MustCompile(`(?m)^[0-9a-f]{64}$`)

func TestOrgCLICreateInviteAcceptAndMembers(t *testing.T) {
	f := newOrgCLI(t)
	if code, out := f.as(t, "anna", "org", "create", "Nope"); code == 0 || !strings.Contains(out, "admin_only") {
		t.Fatalf("non-admin create: %d %s", code, out)
	}
	f.ok(t, "robin", "org", "create", "Alpha Team", "--slug", "alpha")
	if out := f.ok(t, "robin", "org", "list"); !strings.Contains(out, "alpha") || !strings.Contains(out, "owner") || !strings.Contains(out, "default") {
		t.Fatalf("list: %s", out)
	}

	// Einladungen stellt die Weboberfläche aus; die CLI weist darauf hin.
	c0, out := f.as(t, "robin", "org", "invite", "alpha", "--days", "3")
	if c0 == 0 || !strings.Contains(out, "web_session_required") || !strings.Contains(out, f.url+"/ui/orgs") || !strings.Contains(out, "--db") {
		t.Fatalf("invite hint: %d %s", c0, out)
	}
	org, _ := f.st.OrgByRef("alpha")
	code, _, err := f.st.CreateInvitation("person:1", org.ID, "", "member", 3*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if c, o := f.as(t, "anna", "org", "accept", "wrong"); c == 0 || !strings.Contains(o, "invalid_code") {
		t.Fatalf("wrong code: %d %s", c, o)
	}
	f.ok(t, "anna", "org", "accept", code)
	if c, _ := f.as(t, "anna", "org", "accept", code); c == 0 {
		t.Fatal("a code must work once")
	}
	if out := f.ok(t, "anna", "org", "members", "alpha"); !strings.Contains(out, "robin") || !strings.Contains(out, "anna") {
		t.Fatalf("members: %s", out)
	}
	if c, o := f.as(t, "anna", "org", "invite", "alpha"); c == 0 || !strings.Contains(o, "web_session_required") {
		t.Fatalf("member inviting: %d %s", c, o)
	}
	if c, o := f.as(t, "robin", "org", "members", "alpha", "set-role", "anna", "owner"); c == 0 || !strings.Contains(o, "web_session_required") || !strings.Contains(o, "/ui/orgs") {
		t.Fatalf("set-role over the API: %d %s", c, o)
	}
	if f.st.OrgRole(org.ID, "person:2") != "member" {
		t.Fatal("a refused set-role changed the role")
	}
	// Was die Weboberfläche tut (hier: Store); der letzte Owner bleibt.
	if err := f.st.SetOrgRole("person:1", org.ID, "person:2", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetOrgRole("person:1", org.ID, "person:1", "member"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetOrgRole("person:2", org.ID, "person:2", "member"); err == nil {
		t.Fatal("last owner demoted")
	}
	if _, _, err := f.st.CreateInvitation("person:2", org.ID, "x@example.test", "", 0); err != nil {
		t.Fatal(err)
	}
	list := f.ok(t, "anna", "org", "invitations", "alpha")
	if !strings.Contains(list, "pending") || !strings.Contains(list, "x@example.test") || strings.Contains(list, code) {
		t.Fatalf("invitations: %s", list)
	}
	f.ok(t, "anna", "org", "invitations", "alpha", "revoke", "2")
	if c, o := f.as(t, "anna", "org", "members", "alpha", "remove", "robin"); c == 0 || !strings.Contains(o, "web_session_required") {
		t.Fatalf("removing another member over the API: %d %s", c, o)
	}
	f.ok(t, "robin", "org", "members", "alpha", "remove", "robin") // sich selbst verlassen
	if c, _ := f.as(t, "robin", "org", "members", "alpha"); c == 0 {
		t.Fatal("a removed member must lose access")
	}
	if c, _ := f.as(t, "anna", "org"); c != 2 {
		t.Fatalf("usage code = %d", c)
	}
	if c, _ := f.as(t, "anna", "org", "bogus"); c != 2 {
		t.Fatalf("unknown subcommand code = %d", c)
	}
}

func TestProjectCLIClaimMoveListAndDefaults(t *testing.T) {
	f := newOrgCLI(t)
	f.ok(t, "robin", "org", "create", "Alpha", "--slug", "alpha")
	f.ok(t, "robin", "org", "create", "Beta", "--slug", "beta")

	if out := f.ok(t, "robin", "project", "claim", "https://github.com/X/y.git", "--org", "alpha"); !strings.Contains(out, "github.com/x/y belongs to alpha") {
		t.Fatalf("claim: %s", out)
	}
	if out := f.ok(t, "robin", "project", "list"); !strings.Contains(out, "github.com/x/y") || !strings.Contains(out, "alpha") {
		t.Fatalf("list: %s", out)
	}
	if c, o := f.as(t, "anna", "project", "claim", "github.com/x/y"); c == 0 || !strings.Contains(o, "no_org") && !strings.Contains(o, "project_claimed") {
		t.Fatalf("foreign claim: %d %s", c, o)
	}
	if c, o := f.as(t, "robin", "project", "move", "github.com/x/y", "--org", "beta"); c == 0 || !strings.Contains(o, "web_session_required") {
		t.Fatalf("move over the API: %d %s", c, o)
	}
	if _, err := f.st.MoveProject("person:1", "github.com/x/y", "beta"); err != nil {
		t.Fatal(err)
	}
	if out := f.ok(t, "robin", "project", "list", "--org", "beta"); !strings.Contains(out, "github.com/x/y") {
		t.Fatalf("list after move: %s", out)
	}
	if out := f.ok(t, "robin", "project", "list", "--org", "alpha"); strings.Contains(out, "github.com/x/y") {
		t.Fatalf("project still in alpha: %s", out)
	}
	if c, _ := f.as(t, "robin", "project", "move", "github.com/x/y"); c != 2 {
		t.Fatalf("move without --org: %d", c)
	}

	// Mehrere Orgs ohne Standard: Claim nennt die Auswahl.
	f.ok(t, "robin", "org", "default", "none")
	if c, o := f.as(t, "robin", "project", "claim", "github.com/x/z"); c == 0 || !strings.Contains(o, "project_unclaimed") || !strings.Contains(o, "alpha, beta") {
		t.Fatalf("unclaimed: %d %s", c, o)
	}
	f.ok(t, "robin", "org", "default", "beta")
	if out := f.ok(t, "robin", "project", "claim", "github.com/x/z"); !strings.Contains(out, "belongs to beta") {
		t.Fatalf("claim with default: %s", out)
	}
}

func TestOrgRenameAndForceMove(t *testing.T) {
	f := newOrgCLI(t)
	f.ok(t, "robin", "org", "create", "Alpha", "--slug", "alpha")
	if out := f.ok(t, "robin", "org", "rename", "alpha", "Deadweight Labs", "--slug", "deadweight"); !strings.Contains(out, "Deadweight Labs (deadweight)") {
		t.Fatalf("rename: %s", out)
	}
	if c, o := f.as(t, "anna", "org", "rename", "deadweight", "Mine"); c == 0 || !strings.Contains(o, "org_not_found") {
		t.Fatalf("rename by a non-member: %d %s", c, o)
	}

	// Admin-Weg mit Datenbankzugriff: ein besetztes Projekt zurückgeben.
	db := t.TempDir() + "/force.db"
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	st.AddPerson("robin")
	st.AddPerson("anna")
	st.CreateOrg("person:1", "Home", "home")
	st.CreateOrg("person:2", "Squat", "squat")
	if _, err := st.ClaimProject("person:2", "github.com/x/y", "squat"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	var out bytes.Buffer
	if code := run([]string{"project", "move", "--force", "github.com/x/y", "--org", "home", "--db", db}, &out); code != 0 || !strings.Contains(out.String(), "now belongs to home") {
		t.Fatalf("force move: %d %s", code, out.String())
	}
	if code := run([]string{"project", "claim", "--force", "x", "--db", db}, &out); code != 2 {
		t.Fatalf("--force is only for move: %d", code)
	}
}
