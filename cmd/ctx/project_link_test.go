package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/projectlink"
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
	if code, out := ctxRun("project", "create", "infra"); code != 0 || !strings.Contains(out, "acme/infra") {
		t.Fatalf("create = %d: %s", code, out)
	}
	p, ok := st.ProjectByRemote("acme/infra")
	if !ok || p.Org != "acme" {
		t.Fatalf("project = %+v, %v", p, ok)
	}
	if role := st.ProjectRole("acme/infra", "person:1").Role; role != store.RoleOwner {
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
	// --org picks the organization explicitly; the same name may exist in another one.
	if _, err := st.CreateOrg("person:1", "Other", "other"); err != nil {
		t.Fatal(err)
	}
	if code, out := ctxRun("project", "create", "infra", "--org", "other"); code != 0 || !strings.Contains(out, "other/infra") {
		t.Fatalf("create --org = %d: %s", code, out)
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
}

func TestProjectLinkRecordsTheFolderLocallyAndUnlinkRemovesIt(t *testing.T) {
	moveCLIFixture(t)
	if code, out := ctxRun("project", "create", "docs"); code != 0 {
		t.Fatalf("create: %s", out)
	}
	dir := inFolder(t)
	if code, out := ctxRun("project", "link", "docs"); code != 0 || !strings.Contains(out, "acme/docs") {
		t.Fatalf("link = %d: %s", code, out)
	}
	// The assignment lives in the registry; the folder itself stays untouched.
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("link wrote into the folder: %v", entries)
	}
	if d, id := projectlink.Find(dir); d != dir || id != "acme/docs" {
		t.Errorf("registry: %q %q", d, id)
	}
	if st, err := os.Stat(projectlink.Path()); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("registry mode: %v %v", st, err)
	}
	if code, out := ctxRun("project", "links"); code != 0 || !strings.Contains(out, dir) || !strings.Contains(out, "acme/docs") {
		t.Errorf("links = %d: %s", code, out)
	}
	if code, _ := ctxRun("project", "link", "docs"); code != 0 {
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
	if d, _ := projectlink.Find(dir); d != "" {
		t.Errorf("still linked: %q", d)
	}
	if code, out := ctxRun("project", "unlink"); code != 1 || !strings.Contains(out, "not linked") {
		t.Errorf("unlink without link = %d: %s", code, out)
	}
	// A subfolder is part of the link but cannot unlink it by itself.
	if code, _ := ctxRun("project", "link", "docs"); code != 0 {
		t.Fatal("relink")
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	if code, out := ctxRun("project", "unlink"); code != 1 || !strings.Contains(out, "through") {
		t.Errorf("unlink in a subfolder = %d: %s", code, out)
	}
}

func TestProjectLinkChecksWriteAccessAndRefusesHomeAndRoot(t *testing.T) {
	st := moveCLIFixture(t)
	if _, err := st.AddAccount("bob", "", false); err != nil {
		t.Fatal(err)
	}
	if code, out := ctxRun("project", "create", "secret"); code != 0 {
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
	// A project the account has no role in and one that does not exist look the same.
	for _, name := range []string{"secret", "acme/secret", "nothing"} {
		if code, out := ctxRun("project", "link", name); code != 1 || !strings.Contains(out, "cannot write") {
			t.Errorf("link %s = %d: %s", name, code, out)
		}
	}
	if d, _ := projectlink.Find(dir); d != "" {
		t.Error("a refused link was recorded")
	}
	cfg.Token = owner
	_ = config.Save(cfg)

	// A repository project needs no link.
	if code, out := ctxRun("project", "link", cliHere); code != 2 || !strings.Contains(out, "needs no link") {
		t.Errorf("link a remote = %d: %s", code, out)
	}

	home, _ := os.UserHomeDir()
	t.Chdir(home)
	if code, out := ctxRun("project", "link", "secret"); code != 1 || !strings.Contains(out, "home") {
		t.Errorf("link in $HOME = %d: %s", code, out)
	}
	t.Chdir("/")
	if code, out := ctxRun("project", "link", "secret"); code != 1 || !strings.Contains(out, "/") {
		t.Errorf("link in / = %d: %s", code, out)
	}
	if n := len(projectlink.List()); n != 0 {
		t.Errorf("%d links recorded", n)
	}
}

// infra exists as a named project and as a repository (the fixture's
// github.com/acme/infra): the short name is ambiguous and nothing is picked.
func TestProjectLinkRefusesAnAmbiguousShortName(t *testing.T) {
	moveCLIFixture(t)
	if code, out := ctxRun("project", "create", "infra"); code != 0 {
		t.Fatalf("create: %s", out)
	}
	dir := inFolder(t)
	if code, out := ctxRun("project", "link", "infra"); code != 1 || !strings.Contains(out, "matches several") {
		t.Errorf("ambiguous link = %d: %s", code, out)
	}
	if d, _ := projectlink.Find(dir); d != "" {
		t.Error("an ambiguous link was recorded")
	}
	if code, out := ctxRun("project", "link", "acme/infra"); code != 1 || !strings.Contains(out, "matches several") {
		t.Errorf("acme/infra is also the tail of the remote: %d %s", code, out)
	}
}

// gitserver/repo has the shape of a named id but is a repository: no link.
func TestProjectLinkTreatsAHostlessRemoteAsARepository(t *testing.T) {
	st := moveCLIFixture(t)
	if _, err := st.EnsureProject("person:1", "git@gitserver:repo.git"); err != nil {
		t.Fatal(err)
	}
	dir := inFolder(t)
	if code, out := ctxRun("project", "link", "gitserver/repo"); code != 2 || !strings.Contains(out, "needs no link") {
		t.Errorf("link = %d: %s", code, out)
	}
	if d, _ := projectlink.Find(dir); d != "" {
		t.Error("a repository project was linked")
	}
}

func TestProjectUnlinkMissingAndDoctorReportStaleLinks(t *testing.T) {
	moveCLIFixture(t)
	if code, out := ctxRun("project", "create", "notes"); code != 0 {
		t.Fatalf("create: %s", out)
	}
	if checks := linkChecks(); len(checks) != 0 {
		t.Errorf("doctor speaks without links: %+v", checks)
	}
	dir := inFolder(t)
	if code, out := ctxRun("project", "link", "notes"); code != 0 {
		t.Fatalf("link: %s", out)
	}
	if checks := linkChecks(); len(checks) != 1 || !checks[0].OK {
		t.Errorf("healthy link: %+v", checks)
	}
	t.Chdir(filepath.Dir(dir))
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	checks := linkChecks()
	if len(checks) != 1 || checks[0].OK || !strings.Contains(checks[0].Detail, dir) || !strings.Contains(checks[0].Fix, "unlink --missing") {
		t.Errorf("stale link: %+v", checks)
	}
	if code, out := ctxRun("project", "links"); code != 0 || !strings.Contains(out, "gone") {
		t.Errorf("links = %d: %s", code, out)
	}
	if code, out := ctxRun("project", "unlink", "--missing"); code != 0 || !strings.Contains(out, "removed 1") {
		t.Errorf("unlink --missing = %d: %s", code, out)
	}
	if n := len(projectlink.List()); n != 0 {
		t.Errorf("%d links left", n)
	}
}

func TestALinkedFolderResolvesForTheMCPServerAndFindsTheNamedProject(t *testing.T) {
	moveCLIFixture(t)
	if code, out := ctxRun("project", "create", "notes"); code != 0 {
		t.Fatalf("create: %s", out)
	}
	dir := inFolder(t)
	if code, out := ctxRun("project", "link", "notes"); code != 0 {
		t.Fatalf("link: %s", out)
	}
	sub := filepath.Join(dir, "hosts", "mainex")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	h := currentGitContext("mainex")
	if h.axes.Project != "acme/notes" || h.axes.Branch != "" || h.root != "" || h.activation.RepoPath != "" {
		t.Errorf("harness context = %+v", h)
	}
	cfg, _ := config.Load()
	c := client.New(cfg)
	for _, in := range []string{"notes", "acme/notes"} {
		if got, err := c.ResolveProject(in); err != nil || got != "acme/notes" {
			t.Errorf("ResolveProject(%s) = %q, %v", in, got, err)
		}
	}
	// Writing without any project names the way out for a folder without Git.
	t.Chdir(inFolder(t))
	if _, err := c.WriteTarget("", currentGitContext("mainex").axes.Project); err == nil || !strings.Contains(err.Error(), "ctx project link <name>") || !strings.Contains(err.Error(), "acme/notes") {
		t.Errorf("no-project error = %v", err)
	}
}

func TestBootstrapInALinkedFolderDeliversTheProjectsKnowledgeAndWritesNoMirror(t *testing.T) {
	st := moveCLIFixture(t)
	if code, out := ctxRun("project", "create", "notes"); code != 0 {
		t.Fatalf("create: %s", out)
	}
	if _, err := st.InsertKnowledge(store.Knowledge{Type: "instruction", Title: "backups run at night", Body: "the nas snapshots nightly",
		Scope: scope.Axes{Project: "acme/notes"}, Person: "alice", Confidence: "trusted"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertKnowledge(store.Knowledge{Type: "instruction", Title: "other project secret", Body: "unrelated",
		Scope: scope.Axes{Project: cliHere}, Person: "alice", Confidence: "trusted"}); err != nil {
		t.Fatal(err)
	}
	dir := inFolder(t)
	if code, out := ctxRun("project", "link", "notes"); code != 0 {
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

// The attack this design exists for: a repository that carries a marker file for
// a project the user can read. It must neither move the session nor deliver
// that project's knowledge.
func TestAMarkerFileInsideAForeignRepoDoesNotPullInAProjectsKnowledge(t *testing.T) {
	st := moveCLIFixture(t)
	if code, out := ctxRun("project", "create", "notes"); code != 0 {
		t.Fatalf("create: %s", out)
	}
	if _, err := st.InsertKnowledge(store.Knowledge{Type: "instruction", Title: "victim secret", Body: "private",
		Scope: scope.Axes{Project: "acme/notes"}, Person: "alice", Confidence: "trusted"}); err != nil {
		t.Fatal(err)
	}
	dir := inFolder(t)
	for _, name := range []string{".ghosttree-project", ".ghosttree-link"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("# x\nacme/notes\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	md := bootstrapContext(strings.NewReader(`{"cwd":"` + dir + `","session_id":"s2"}`))
	if strings.Contains(md, "victim secret") {
		t.Errorf("a file in the folder pulled in a project's knowledge: %q", md)
	}
	if h := currentGitContext("m"); h.axes.Project != "" {
		t.Errorf("a file in the folder assigned %q", h.axes.Project)
	}
}
