package main

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestProjectRolesCLI(t *testing.T) {
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
	// Flag hinter den Positionsargumenten.
	f.ok(t, "robin", "project", "role", "set", "github.com/dw/p", "anna", "member", "--review")
	out = f.ok(t, "anna", "project", "roles", "github.com/dw/p")
	if !strings.Contains(out, "you: member") {
		t.Fatalf("anna sees her own role: %s", out)
	}
	out = f.ok(t, "robin", "project", "roles", "github.com/dw/p")
	if !strings.Contains(out, "anna") || !strings.Contains(out, "member") || !strings.Contains(out, "reviewer") {
		t.Fatalf("roles after set: %s", out)
	}

	// Ein Member erhöht sich nicht und vergibt nichts.
	if code, out := f.as(t, "anna", "project", "role", "set", "github.com/dw/p", "anna", "lead"); code == 0 || !strings.Contains(out, "not_grantor") {
		t.Fatalf("member self-promotion: %d %s", code, out)
	}
	f.ok(t, "robin", "project", "role", "set", "github.com/dw/p", "anna", "lead")
	if code, out := f.as(t, "anna", "project", "role", "set", "github.com/dw/p", "anna", "owner"); code == 0 || !strings.Contains(out, "self_promotion") {
		t.Fatalf("lead self-promotion: %d %s", code, out)
	}
	if code, out := f.as(t, "anna", "project", "role", "set", "github.com/dw/p", "robin", "guest"); code == 0 || !strings.Contains(out, "implicit_owner") {
		t.Fatalf("touching the org owner: %d %s", code, out)
	}
	if code, out := f.as(t, "robin", "project", "role", "set", "github.com/dw/p", "anna", "reviewer"); code == 0 || !strings.Contains(out, "invalid_input") {
		t.Fatalf("reviewer as role: %d %s", code, out)
	}
	if code, out := f.as(t, "robin", "project", "role", "set", "github.com/dw/nothing", "anna", "guest"); code == 0 || !strings.Contains(out, "project_not_found") {
		t.Fatalf("unknown project: %d %s", code, out)
	}
	if code, _ := f.as(t, "robin", "project", "role", "set", "github.com/dw/p", "anna"); code != 2 {
		t.Fatalf("usage code = %d", code)
	}
	f.ok(t, "robin", "project", "role", "remove", "github.com/dw/p", "anna")
	if got := f.st.ProjectRole("github.com/dw/p", "person:2"); got.Role != "" {
		t.Fatalf("removed: %+v", got)
	}

	// Aus einer Agentensitzung heraus vergibt die CLI nichts.
	t.Setenv(agentIDEnv, "claude:h:1")
	if code, out := f.as(t, "robin", "project", "role", "set", "github.com/dw/p", "anna", "lead"); code == 0 || !strings.Contains(out, "agents cannot") {
		t.Fatalf("agent session granted a role: %d %s", code, out)
	}
	if got := f.st.ProjectRole("github.com/dw/p", "person:2"); got.Role != "" {
		t.Fatalf("agent session changed a role: %+v", got)
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
