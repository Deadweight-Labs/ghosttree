package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func ctxRun(args ...string) (int, string) {
	var out bytes.Buffer
	code := run(args, &out)
	return code, out.String()
}

// inFolder wechselt in einen frischen Ordner ausserhalb des (Test-)Homes.
func inFolder(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return dir
}

func TestProjectCreateByCLI(t *testing.T) {
	st := moveCLIFixture(t)
	if code, out := ctxRun("project", "create", "infra"); code != 0 || !strings.Contains(out, "infra") {
		t.Fatalf("create = %d: %s", code, out)
	}
	p, ok := st.ProjectByRemote("infra")
	if !ok || p.Org != "acme" {
		t.Fatalf("project = %+v, %v", p, ok)
	}
	if role := st.ProjectRole("infra", "person:1").Role; role != store.RoleOwner {
		t.Errorf("creator role = %q", role)
	}
	if code, out := ctxRun("project", "create", "infra"); code != 1 || !strings.Contains(out, "already exists") {
		t.Errorf("duplicate = %d: %s", code, out)
	}
	if code, out := ctxRun("project", "create", "github.com/a/b"); code != 1 || !strings.Contains(out, "invalid_input") {
		t.Errorf("a remote as a name = %d: %s", code, out)
	}
	if code, _ := ctxRun("project", "create"); code != 2 {
		t.Errorf("no name = %d", code)
	}
	// --org picks the organization explicitly; an organization one is not in is refused.
	if _, err := st.CreateOrg("person:1", "Other", "other"); err != nil {
		t.Fatal(err)
	}
	if code, out := ctxRun("project", "create", "sky-go", "--org", "other"); code != 0 {
		t.Fatalf("create --org = %d: %s", code, out)
	}
	if p, _ := st.ProjectByRemote("sky-go"); p.Org != "other" {
		t.Errorf("sky-go belongs to %q", p.Org)
	}
}

func TestProjectCreateNeedsAnOrganization(t *testing.T) {
	st := moveCLIFixture(t)
	if _, err := st.AddAccount("bob", "", false); err != nil {
		t.Fatal(err)
	}
	token, _, err := st.CreateToken("bob", store.TokenSpec{Label: "t"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load()
	cfg.Token = token
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if code, out := ctxRun("project", "create", "mine"); code != 1 || !strings.Contains(out, "no_org") {
		t.Errorf("outsider = %d: %s", code, out)
	}
	if _, ok := st.ProjectByRemote("mine"); ok {
		t.Error("a project appeared")
	}
}

func TestProjectLinkWritesTheMarkerAndUnlinkRemovesIt(t *testing.T) {
	moveCLIFixture(t)
	if code, out := ctxRun("project", "create", "infra"); code != 0 {
		t.Fatalf("create: %s", out)
	}
	dir := inFolder(t)
	if code, out := ctxRun("project", "link", "infra"); code != 0 {
		t.Fatalf("link = %d: %s", code, out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".ghosttree-project"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if lines[len(lines)-1] != "infra" || strings.Count(string(raw), "\n") > 3 {
		t.Errorf("marker = %q", raw)
	}
	if id, err := collector.ReadProjectMarker(filepath.Join(dir, ".ghosttree-project")); err != nil || id != "infra" {
		t.Errorf("marker unreadable: %q %v", id, err)
	}
	// Linking again to the same project is fine, to another one is not silent.
	if code, _ := ctxRun("project", "link", "infra"); code != 0 {
		t.Errorf("relink to the same project = %d", code)
	}
	if code, out := ctxRun("project", "create", "sky-go"); code != 0 {
		t.Fatalf("create: %s", out)
	}
	if code, out := ctxRun("project", "link", "sky-go"); code != 1 || !strings.Contains(out, "unlink") {
		t.Errorf("relink to another = %d: %s", code, out)
	}
	if code, out := ctxRun("project", "unlink"); code != 0 {
		t.Fatalf("unlink = %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".ghosttree-project")); !os.IsNotExist(err) {
		t.Errorf("marker still there: %v", err)
	}
	if code, out := ctxRun("project", "unlink"); code != 1 || !strings.Contains(out, "not linked") {
		t.Errorf("unlink without marker = %d: %s", code, out)
	}
}

func TestProjectLinkChecksWriteAccessAndRefusesHomeAndRoot(t *testing.T) {
	st := moveCLIFixture(t)
	// A project the account has no role in, and one that does not exist,
	// look the same and leave no marker.
	if _, err := st.AddAccount("bob", "", false); err != nil {
		t.Fatal(err)
	}
	code, out := ctxRun("project", "create", "secret")
	if code != 0 {
		t.Fatalf("create: %s", out)
	}
	token, _, _ := st.CreateToken("bob", store.TokenSpec{Label: "t"})
	cfg, _ := config.Load()
	owner := cfg.Token
	cfg.Token = token
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	dir := inFolder(t)
	for _, name := range []string{"secret", "nothing"} {
		if code, out := ctxRun("project", "link", name); code != 1 || !strings.Contains(out, "cannot write") {
			t.Errorf("link %s = %d: %s", name, code, out)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".ghosttree-project")); !os.IsNotExist(err) {
		t.Error("a refused link left a marker")
	}
	cfg.Token = owner
	_ = config.Save(cfg)

	// A git remote is not a name to link to.
	if code, out := ctxRun("project", "link", "github.com/acme/ghosttree"); code != 2 && code != 1 {
		t.Errorf("link a remote = %d: %s", code, out)
	}

	home, _ := os.UserHomeDir()
	t.Chdir(home)
	if code, out := ctxRun("project", "link", "secret"); code != 1 || !strings.Contains(out, "home") {
		t.Errorf("link in $HOME = %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".ghosttree-project")); !os.IsNotExist(err) {
		t.Error("marker in $HOME")
	}
	t.Chdir("/")
	if code, out := ctxRun("project", "link", "secret"); code != 1 || !strings.Contains(out, "/") {
		t.Errorf("link in / = %d: %s", code, out)
	}
	if _, err := os.Stat("/.ghosttree-project"); !os.IsNotExist(err) {
		t.Error("marker in /")
	}
}

func TestALinkedFolderResolvesForTheMCPServerAndFindsTheNamedProject(t *testing.T) {
	moveCLIFixture(t)
	if code, out := ctxRun("project", "create", "infra"); code != 0 {
		t.Fatalf("create: %s", out)
	}
	dir := inFolder(t)
	if code, out := ctxRun("project", "link", "infra"); code != 0 {
		t.Fatalf("link: %s", out)
	}
	sub := filepath.Join(dir, "hosts", "mainex")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	h := currentGitContext("mainex")
	if h.axes.Project != "infra" || h.axes.Branch != "" || h.root != "" || h.activation.RepoPath != "" {
		t.Errorf("harness context = %+v", h)
	}
	cfg, _ := config.Load()
	c := client.New(cfg)
	if got, err := c.ResolveProject("infra"); err != nil || got != "infra" {
		t.Errorf("ResolveProject(infra) = %q, %v", got, err)
	}
	// Writing without any project names the way out for a folder without Git.
	t.Chdir(inFolder(t))
	if _, err := c.WriteTarget("", currentGitContext("mainex").axes.Project); err == nil || !strings.Contains(err.Error(), "ctx project link <name>") || !strings.Contains(err.Error(), "infra") {
		t.Errorf("no-project error = %v", err)
	}
}

func TestBootstrapInALinkedFolderDeliversTheProjectsKnowledgeAndWritesNoMirror(t *testing.T) {
	st := moveCLIFixture(t)
	if code, out := ctxRun("project", "create", "infra"); code != 0 {
		t.Fatalf("create: %s", out)
	}
	if _, err := st.InsertKnowledge(store.Knowledge{Type: "instruction", Title: "backups run at night", Body: "the nas snapshots nightly",
		Scope: scope.Axes{Project: "infra"}, Person: "alice", Confidence: "trusted"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertKnowledge(store.Knowledge{Type: "instruction", Title: "other project secret", Body: "unrelated",
		Scope: scope.Axes{Project: cliHere}, Person: "alice", Confidence: "trusted"}); err != nil {
		t.Fatal(err)
	}
	dir := inFolder(t)
	if code, out := ctxRun("project", "link", "infra"); code != 0 {
		t.Fatalf("link: %s", out)
	}
	sub := filepath.Join(dir, "nas")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	md := bootstrapContext(strings.NewReader(`{"cwd":"` + sub + `","session_id":"s1"}`))
	if !strings.Contains(md, "backups run at night") {
		t.Errorf("bootstrap lacks the project's knowledge: %q", md)
	}
	if strings.Contains(md, "other project secret") {
		t.Errorf("bootstrap leaked another project: %q", md)
	}
	for _, p := range []string{dir, sub} {
		if _, err := os.Stat(filepath.Join(p, ".ghosttree")); !os.IsNotExist(err) {
			t.Errorf("a mirror was written in %s (%v)", p, err)
		}
	}
}
