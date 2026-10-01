package main

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestProjectRolesCLIListsAndPointsToTheWebUI(t *testing.T) {
	f := newOrgCLI(t)
	f.ok(t, "robin", "org", "create", "Alpha", "--slug", "alpha")
	org, _ := f.st.OrgByRef("alpha")
	code, _, _ := f.st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	if _, err := f.st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	f.ok(t, "robin", "project", "claim", "github.com/dw/p", "--org", "alpha")

	out := f.ok(t, "robin", "project", "roles", "github.com/dw/p")
	if !strings.Contains(out, "you: owner") || !strings.Contains(out, "robin") || !strings.Contains(out, "implicit") {
		t.Fatalf("roles: %s", out)
	}
	if err := f.st.SetProjectRole("person:1", "github.com/dw/p", "person:2", "member", true, store.RoleViaWeb); err != nil {
		t.Fatal(err)
	}
	out = f.ok(t, "anna", "project", "roles", "github.com/dw/p")
	if !strings.Contains(out, "you: member") || !strings.Contains(out, "reviewer") {
		t.Fatalf("anna sees roles: %s", out)
	}
	if code, out := f.as(t, "robin", "project", "roles", "github.com/dw/nothing"); code == 0 || !strings.Contains(out, "project_not_found") {
		t.Fatalf("unknown project: %d %s", code, out)
	}

	// Rollen ändert ein Mensch im Browser; die CLI sagt, wo.
	for _, args := range [][]string{
		{"project", "role", "set", "github.com/dw/p", "anna", "owner"},
		{"project", "role", "set", "github.com/dw/p", "anna", "lead", "--review"},
		{"project", "role", "remove", "github.com/dw/p", "anna"},
	} {
		code, out := f.as(t, "robin", args...)
		if code == 0 || !strings.Contains(out, "web_session_required") || !strings.Contains(out, f.url+"/ui/orgs") || !strings.Contains(out, "--db") {
			t.Fatalf("%v: %d %s", args, code, out)
		}
	}
	// Auch aus einer Agentensitzung und mit gesetzter Agent-ID: derselbe Weg zu.
	t.Setenv(agentIDEnv, "claude:h:1")
	if code, _ := f.as(t, "robin", "project", "role", "set", "github.com/dw/p", "anna", "lead"); code == 0 {
		t.Fatal("role set over the API succeeded")
	}
	if got := f.st.ProjectRole("github.com/dw/p", "person:2"); got.Role != "member" {
		t.Fatalf("a refused change went through: %+v", got)
	}
	if code, _ := f.as(t, "robin", "project", "role", "set", "github.com/dw/p", "anna"); code != 2 {
		t.Fatalf("usage code = %d", code)
	}
}

// Der Notausgang des Betreibers: direkter Datenbankzugriff, protokolliert mit
// via=cli-db, im Namen des ältesten Owners der Organisation.
func TestRoleAndOrgCommandsViaDB(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "g.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"robin", "anna", "ben"} {
		if _, err := st.AddAccount(n, "", n == "robin"); err != nil {
			t.Fatal(err)
		}
	}
	org, err := st.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"person:2", "person:3"} {
		code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
		if _, err := st.AcceptInvitation(who, code); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.EnsureProject("person:1", "github.com/dw/p"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	runOK := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if code := run(args, &out); code != 0 {
			t.Fatalf("ctx %s = %d: %s", strings.Join(args, " "), code, out.String())
		}
		return out.String()
	}
	if out := runOK("project", "role", "set", "github.com/dw/p", "anna", "lead", "--review", "--db", path); !strings.Contains(out, "via cli-db") {
		t.Fatalf("set: %s", out)
	}
	runOK("project", "role", "set", "--db", path, "github.com/dw/p", "ben", "member")
	runOK("project", "role", "remove", "github.com/dw/p", "ben", "--db", path)
	if out := runOK("org", "invite", "alpha", "--db", path, "--email", "x@example.test"); !codeLine.MatchString(out) {
		t.Fatalf("invite: %s", out)
	}
	runOK("org", "members", "alpha", "set-role", "ben", "owner", "--db", path)
	runOK("org", "members", "alpha", "remove", "ben", "--db", path)

	// Die Regeln gelten weiter: Org-Owner sind implizit Owner.
	var out bytes.Buffer
	if code := run([]string{"project", "role", "set", "github.com/dw/p", "robin", "guest", "--db", path}, &out); code == 0 || !strings.Contains(out.String(), "implicit") {
		t.Fatalf("org owner: %d %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"project", "role", "set", "github.com/dw/p", "anna", "lead", "--db", filepath.Join(t.TempDir(), "missing", "x.db")}, &out); code == 0 {
		t.Fatalf("a database that cannot be opened must fail: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"org", "list", "--db", path}, &out); code != 2 {
		t.Fatalf("--db on an unsupported command: %d %s", code, out.String())
	}

	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if got := st.ProjectRole("github.com/dw/p", "person:2"); got.Role != "lead" || !got.CanReview {
		t.Fatalf("anna = %+v", got)
	}
	if got := st.ProjectRole("github.com/dw/p", "person:3"); got.Role != "" {
		t.Fatalf("ben = %+v", got)
	}
	if st.OrgRole(org.ID, "person:3") != "" {
		t.Fatal("ben still a member")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	rows, err := raw.Query(`SELECT via, actor, subject, old_role, new_role FROM role_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var via, actor, subject, old, new string
		if err := rows.Scan(&via, &actor, &subject, &old, &new); err != nil {
			t.Fatal(err)
		}
		got = append(got, via+" "+actor+" "+subject+" "+old+">"+new)
	}
	want := []string{
		"cli-db person:1 person:2 >lead+review",
		"cli-db person:1 person:3 >member",
		"cli-db person:1 person:3 member>",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("role_events = %q, want %q", got, want)
	}
}

func TestClaudeRoleFlagReachesChannelAndMCP(t *testing.T) {
	t.Setenv(claudeDryRunEnv, "")
	var out strings.Builder
	if code := cmdClaude([]string{"--dry-run", "--agent", "claude:h:7", "--role", "lead", "--resume", "x"}, &out); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	for _, want := range []string{"env: " + agentRoleEnv + "=lead", `"` + agentRoleEnv + `": "lead"`, "--resume x"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dry run lacks %q: %s", want, out.String())
		}
	}
	out.Reset()
	cmdClaude([]string{"--dry-run"}, &out)
	if strings.Contains(out.String(), agentRoleEnv) {
		t.Fatalf("no --role must not set a role: %s", out.String())
	}
	for _, bad := range [][]string{{"--role"}, {"--role", "owner"}, {"--role", "admin"}, {"--role", ""}, {"--role", "-x"}} {
		out.Reset()
		if code := cmdClaude(append([]string{"--dry-run"}, bad...), &out); code != 2 || !strings.Contains(out.String(), "--role needs") {
			t.Fatalf("%v: %d %s", bad, code, out.String())
		}
	}
	// Davor und danach: das Argument hinter --role gehört dem Launcher, der Rest claude.
	out.Reset()
	cmdClaude([]string{"--dry-run", "--role", "guest", "--", "--role", "member"}, &out)
	if !strings.Contains(out.String(), agentRoleEnv+"=guest") || !strings.Contains(out.String(), "--role member") {
		t.Fatalf("pass-through after --: %s", out.String())
	}
}

func TestAgentRoleFromEnvNeedsLauncherIdentity(t *testing.T) {
	t.Setenv(agentRoleEnv, "lead")
	t.Setenv(agentIDEnv, "")
	t.Setenv("CODEX_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("OPENCODE_SESSION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	if got := agentRoleFromEnv(); got != "" {
		t.Fatalf("a role without a launcher identity must be ignored: %q", got)
	}
	t.Setenv(agentIDEnv, "claude:h:1")
	if got := agentRoleFromEnv(); got != "lead" {
		t.Fatalf("role = %q", got)
	}
	t.Setenv(agentRoleEnv, "owner")
	if got := agentRoleFromEnv(); got != "" {
		t.Fatalf("owner must be rejected: %q", got)
	}
	// Eine fremde Harness-Identität macht auch die Rolle ungültig.
	t.Setenv(agentRoleEnv, "lead")
	t.Setenv("CODEX_SESSION_ID", "x")
	if got := agentRoleFromEnv(); got != "" {
		t.Fatalf("foreign harness: %q", got)
	}
}
