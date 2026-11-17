package main

import (
	"bytes"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/mirror"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/workflow"
)

const wtProject = "github.com/alpha/app"

// A stage 1 file with comments, a stage 2 file with an instruction and a stage
// 3 file with its own prompt, tools and rounds: the three shapes a file has
// (KN-4803, KN-4821). Odd spacing, comments, a tab in the body and a final line
// without a newline must come back byte for byte.
var wtTexts = map[string]string{
	"stufe-eins": "---\n# nur ein Baustein\ndescription: Echo   nach dem Merge   # trailing comment\non: pr.merged\nuses: ghosttree/echo\n---\n",
	"stufe-zwei": "---\n\ndescription: Mit Zusatz\non: pr.merged\nuses: ghosttree/ask\nmodel: klein\n# Kommentar vor dem Budget\nbudget:\n  run_usd: 0.10\n---\nBeantworte kurz.\r\n\tEingerückt mit Tab, ä ö ü 🌳   \n\nLetzte Zeile ohne Zeilenende",
	"stufe-drei": "---\non: idea.created\ninputs: [idea.title]\n# Werkzeuge, nur lesend\ntools: [knowledge.search, issue.read]\nrounds: 6\nresults:\n  comment: 1\nmodel: mittel\nbudget:\n  run_usd: 0.40\n---\nPrüfe, ob es schon Wissen zu dieser Idee gibt.\n\n  Zweiter Absatz.\n",
}

type wtEnv struct {
	st    *store.Store
	c     *client.Client
	root  string
	token string
	url   string
}

func newWTEnv(t *testing.T) wtEnv {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, err := st.AddPerson("robin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOrg("person:1", "Alpha", "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimProject("person:1", wtProject, "alpha"); err != nil {
		t.Fatal(err)
	}
	// The first imported connection of the server gives it its default
	// aliases (klein, mittel, ...), which the check of a file needs.
	if _, err := st.ImportFileAIConnection(store.AIFileConnection{Name: "standard", Format: store.AIFormatAnthropic, BaseURL: "https://api.anthropic.com", Model: "claude-haiku-5-5", KeyLast4: "AAAA"}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.New(st))
	t.Cleanup(srv.Close)
	return wtEnv{st: st, c: client.New(config.Config{ServerURL: srv.URL, Token: token}), root: t.TempDir(), token: token, url: srv.URL}
}

func (e wtEnv) push(t *testing.T, name, note string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := workflowPush(e.root, wtProject, e.c, name, note, &out)
	return code, out.String()
}

func (e wtEnv) pull(t *testing.T, name string, force bool) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := workflowPull(e.root, wtProject, e.c, name, force, &out)
	return code, out.String()
}

func (e wtEnv) file(t *testing.T, name string) string {
	t.Helper()
	got, err := workflow.ReadWorktreeFile(e.root, name)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (e wtEnv) server(t *testing.T, name string) client.WorkflowDetail {
	t.Helper()
	d, err := e.c.GetWorkflow(wtProject, name, true)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestWorkflowPushThenPullKeepsEveryStageByteForByte(t *testing.T) {
	e := newWTEnv(t)
	for name, text := range wtTexts {
		var out bytes.Buffer
		if code := workflowNew(e.root, name, "", &out); code != 0 {
			t.Fatalf("new %s: %d %s", name, code, out.String())
		}
		if err := workflow.WriteWorktreeFile(e.root, name, text); err != nil {
			t.Fatal(err)
		}
		if code, out := e.push(t, name, "erste Fassung"); code != 0 || !strings.Contains(out, "created, v1") {
			t.Fatalf("push %s: %d %s", name, code, out)
		}
		if got := e.server(t, name).Workflow.Text; got != text {
			t.Fatalf("%s: the server holds %q, want %q", name, got, text)
		}
		if err := os.Remove(workflow.WorktreeFile(e.root, name)); err != nil {
			t.Fatal(err)
		}
		if code, out := e.pull(t, name, false); code != 0 {
			t.Fatalf("pull %s: %d %s", name, code, out)
		}
		if got := e.file(t, name); got != text {
			t.Fatalf("%s: round trip gave %q, want %q", name, got, text)
		}
		state, err := workflow.LoadWorktreeState(e.root)
		if err != nil || state[name].Version != 1 || state[name].Hash != store.WorkflowDefHash(text) {
			t.Fatalf("%s: state %+v %v", name, state[name], err)
		}
	}
}

// The way of the file is one of three (REQ-699): the same text through the
// worktree and through the MCP tool is the same version on the server, and the
// version records how it came.
func TestWorkflowWorktreeAndMCPSaveGiveTheSameVersion(t *testing.T) {
	e := newWTEnv(t)
	text := wtTexts["stufe-zwei"]
	if err := workflow.WriteWorktreeFile(e.root, "gleich", text); err != nil {
		t.Fatal(err)
	}
	if code, out := e.push(t, "gleich", "per Datei"); code != 0 {
		t.Fatalf("push: %d %s", code, out)
	}
	// What the MCP tool workflow_write calls, with the version the file was at.
	res, err := e.c.SaveWorkflow(wtProject, "gleich", text, "per MCP", 1)
	if err != nil || res.Saved == nil || res.Saved.Changed || res.Saved.Def.Version != 1 || res.Saved.Def.Hash != store.WorkflowDefHash(text) {
		t.Fatalf("the same text through MCP: %+v %v", res, err)
	}
	d := e.server(t, "gleich")
	if len(d.Versions) != 1 || d.Versions[0].Via != store.WorkflowViaAPI || d.Versions[0].Text != text || d.Workflow.Hash != store.WorkflowDefHash(text) {
		t.Fatalf("versions: %+v", d.Versions)
	}
	// A change over MCP shows up in the file by a pull, as the next version.
	changed := strings.Replace(text, "Beantworte kurz.", "Beantworte ausführlich.", 1)
	if res, err := e.c.SaveWorkflow(wtProject, "gleich", changed, "per MCP", 1); err != nil || res.Saved == nil || res.Saved.Def.Version != 2 {
		t.Fatalf("MCP save: %+v %v", res, err)
	}
	if code, out := e.pull(t, "gleich", false); code != 0 || e.file(t, "gleich") != changed {
		t.Fatalf("pull after MCP: %d %s", code, out)
	}
}

// Criterion 2: a workflow changed on the server since the pull is not
// overwritten by an upload, and the answer shows what changed.
func TestWorkflowPushRefusesWhenTheServerChangedAndShowsTheDifference(t *testing.T) {
	e := newWTEnv(t)
	base := "---\ndescription: alt\non: pr.merged\nuses: ghosttree/echo\n---\n"
	if res, err := e.c.SaveWorkflow(wtProject, "streit", base, "v1", 0); err != nil || res.Saved == nil {
		t.Fatalf("seed: %+v %v", res, err)
	}
	if code, out := e.pull(t, "streit", false); code != 0 {
		t.Fatalf("pull: %d %s", code, out)
	}
	theirs := strings.Replace(base, "description: alt", "description: neu vom Kollegen", 1)
	if res, err := e.c.SaveWorkflow(wtProject, "streit", theirs, "v2", 1); err != nil || res.Saved == nil || res.Saved.Def.Version != 2 {
		t.Fatalf("their save: %+v %v", res, err)
	}
	mine := strings.Replace(base, "description: alt", "description: meine Fassung", 1)
	if err := workflow.WriteWorktreeFile(e.root, "streit", mine); err != nil {
		t.Fatal(err)
	}
	code, out := e.push(t, "streit", "meine Änderung")
	if code != 1 || !strings.Contains(out, "CONFLICT streit") || !strings.Contains(out, "your base: version 1") || !strings.Contains(out, "server: version 2") ||
		!strings.Contains(out, "-description: alt") || !strings.Contains(out, "+description: neu vom Kollegen") {
		t.Fatalf("conflict: %d\n%s", code, out)
	}
	d := e.server(t, "streit")
	if d.Workflow.Version != 2 || d.Workflow.Text != theirs || len(d.Versions) != 2 {
		t.Fatalf("the newer version was overwritten: %+v", d)
	}
	if e.file(t, "streit") != mine {
		t.Fatal("a refused push changed the file")
	}
	// The way out: take the server's version, then push again from there.
	if code, out := e.pull(t, "streit", false); code != 1 || !strings.Contains(out, "not pushed") {
		t.Fatalf("pull over a modified file: %d %s", code, out)
	}
	if code, out := e.pull(t, "streit", true); code != 0 || e.file(t, "streit") != theirs {
		t.Fatalf("pull --force: %d %s", code, out)
	}
	merged := strings.Replace(theirs, "neu vom Kollegen", "beide", 1)
	if err := workflow.WriteWorktreeFile(e.root, "streit", merged); err != nil {
		t.Fatal(err)
	}
	if code, out := e.push(t, "streit", "zusammengeführt"); code != 0 || !strings.Contains(out, "v3") {
		t.Fatalf("push after the pull: %d %s", code, out)
	}
	if got := e.server(t, "streit").Workflow; got.Version != 3 || got.Text != merged {
		t.Fatalf("after the merge: %+v", got)
	}
}

func TestWorkflowPushOfANewFileWhoseNameTheServerHasIsRefused(t *testing.T) {
	e := newWTEnv(t)
	if res, err := e.c.SaveWorkflow(wtProject, "doppelt", wtTexts["stufe-eins"], "", 0); err != nil || res.Saved == nil {
		t.Fatalf("seed: %+v %v", res, err)
	}
	if code := workflowNew(e.root, "doppelt", "", &bytes.Buffer{}); code != 0 {
		t.Fatal("new")
	}
	code, out := e.push(t, "doppelt", "x")
	if code != 1 || !strings.Contains(out, "CONFLICT doppelt") || !strings.Contains(out, "never pulled") {
		t.Fatalf("push of a draft over an existing workflow: %d %s", code, out)
	}
	if e.server(t, "doppelt").Workflow.Text != wtTexts["stufe-eins"] {
		t.Fatal("the existing workflow was overwritten")
	}
}

func TestWorkflowPushRefusesAFileWithErrorsAndSendsNothing(t *testing.T) {
	e := newWTEnv(t)
	if err := workflow.WriteWorktreeFile(e.root, "kaputt", "---\non: pr.merged\nunbekannt: 1\nuses: ghosttree/echo\n---\n"); err != nil {
		t.Fatal(err)
	}
	code, out := e.push(t, "kaputt", "x")
	if code != 1 || !strings.Contains(out, "not saved") || !strings.Contains(out, "unbekannt") {
		t.Fatalf("push: %d %s", code, out)
	}
	if _, err := e.c.GetWorkflow(wtProject, "kaputt", false); err == nil {
		t.Fatal("a file with errors was saved")
	}
}

func TestWorkflowPushNeedsANoteAndSaysUnchanged(t *testing.T) {
	e := newWTEnv(t)
	if err := workflow.WriteWorktreeFile(e.root, "notiz", wtTexts["stufe-eins"]); err != nil {
		t.Fatal(err)
	}
	if code, out := e.push(t, "notiz", ""); code != 2 || !strings.Contains(out, "-m") {
		t.Fatalf("without a note: %d %s", code, out)
	}
	if code, _ := e.push(t, "notiz", "erste"); code != 0 {
		t.Fatal("push")
	}
	if code, out := e.push(t, "notiz", ""); code != 0 || !strings.Contains(out, "unchanged: notiz (v1)") {
		t.Fatalf("unchanged: %d %s", code, out)
	}
	if code, out := e.push(t, "nicht-da", "x"); code != 1 || !strings.Contains(out, "no file") {
		t.Fatalf("no file: %d %s", code, out)
	}
	if code, out := e.push(t, "../boese", "x"); code != 2 || !strings.Contains(out, "invalid workflow name") {
		t.Fatalf("a name that is a path: %d %s", code, out)
	}
}

func TestWorkflowPullAllAndLsShowWhereEachFileStands(t *testing.T) {
	e := newWTEnv(t)
	for _, n := range []string{"eins", "zwei"} {
		if res, err := e.c.SaveWorkflow(wtProject, n, wtTexts["stufe-eins"], "", 0); err != nil || res.Saved == nil {
			t.Fatalf("seed: %+v %v", res, err)
		}
	}
	var ls bytes.Buffer
	if code := workflowLs(e.root, wtProject, e.c, &ls); code != 0 || strings.Count(ls.String(), "not pulled") != 2 {
		t.Fatalf("ls before the pull: %d\n%s", code, ls.String())
	}
	if code, out := e.pull(t, "", false); code != 0 || e.file(t, "eins") != wtTexts["stufe-eins"] || e.file(t, "zwei") != wtTexts["stufe-eins"] {
		t.Fatalf("pull all: %d %s", code, out)
	}
	if err := workflow.WriteWorktreeFile(e.root, "eins", wtTexts["stufe-zwei"]); err != nil {
		t.Fatal(err)
	}
	if res, err := e.c.SaveWorkflow(wtProject, "zwei", wtTexts["stufe-zwei"], "", 1); err != nil || res.Saved == nil {
		t.Fatalf("second version: %+v %v", res, err)
	}
	if err := workflow.WriteWorktreeFile(e.root, "entwurf", wtTexts["stufe-eins"]); err != nil {
		t.Fatal(err)
	}
	ls.Reset()
	if code := workflowLs(e.root, wtProject, e.c, &ls); code != 0 {
		t.Fatalf("ls: %d", code)
	}
	for _, want := range []string{"eins", "modified", "zwei", "clean, behind v2", "entwurf", "draft, not on the server"} {
		if !strings.Contains(ls.String(), want) {
			t.Errorf("ls lacks %q:\n%s", want, ls.String())
		}
	}
	// Pull all leaves the modified file alone, takes the rest and says so.
	code, out := e.pull(t, "", false)
	if code != 1 || !strings.Contains(out, "eins.md has changes that were not pushed") || e.file(t, "eins") != wtTexts["stufe-zwei"] || e.file(t, "zwei") != wtTexts["stufe-zwei"] {
		t.Fatalf("pull all with a modified file: %d %s", code, out)
	}
}

func TestWorkflowDiffShowsLocalAndRemoteAgainstTheBase(t *testing.T) {
	e := newWTEnv(t)
	base := "---\ndescription: erste\non: pr.merged\nuses: ghosttree/echo\n---\n"
	if res, err := e.c.SaveWorkflow(wtProject, "unterschied", base, "", 0); err != nil || res.Saved == nil {
		t.Fatalf("seed: %+v %v", res, err)
	}
	if code, out := e.pull(t, "unterschied", false); code != 0 {
		t.Fatalf("pull: %d %s", code, out)
	}
	if err := workflow.WriteWorktreeFile(e.root, "unterschied", strings.Replace(base, "erste", "lokal", 1)); err != nil {
		t.Fatal(err)
	}
	if res, err := e.c.SaveWorkflow(wtProject, "unterschied", strings.Replace(base, "erste", "entfernt", 1), "", 1); err != nil || res.Saved == nil {
		t.Fatalf("server change: %+v %v", res, err)
	}
	var local, remote bytes.Buffer
	if code := workflowDiff(e.root, wtProject, e.c, "unterschied", false, &local); code != 0 || !strings.Contains(local.String(), "+description: lokal") || !strings.Contains(local.String(), "-description: erste") {
		t.Fatalf("diff: %d\n%s", code, local.String())
	}
	if code := workflowDiff(e.root, wtProject, e.c, "unterschied", true, &remote); code != 0 || !strings.Contains(remote.String(), "+description: entfernt") || !strings.Contains(remote.String(), "+++ server (v2)") {
		t.Fatalf("diff --remote: %d\n%s", code, remote.String())
	}
	if code := workflowDiff(e.root, wtProject, e.c, "ohne-basis", false, &bytes.Buffer{}); code != 1 {
		t.Fatal("diff of a file without a base")
	}
}

func TestWorkflowNewMakesAFileThatPassesTheCheckOrCopiesATemplate(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if code := workflowNew(root, "start", "", &out); code != 0 {
		t.Fatalf("new: %d %s", code, out.String())
	}
	text, err := workflow.ReadWorktreeFile(root, "start")
	if err != nil {
		t.Fatal(err)
	}
	if _, rep := workflow.Check(text, workflow.Env{}); !rep.OK() {
		t.Fatalf("the skeleton does not pass the check: %v", rep.Errors)
	}
	for _, tmpl := range workflow.Templates() {
		if code := workflowNew(root, "aus-"+tmpl.Name, tmpl.Name, &bytes.Buffer{}); code != 0 {
			t.Fatalf("template %s", tmpl.Name)
		}
		if got, _ := workflow.ReadWorktreeFile(root, "aus-"+tmpl.Name); got != tmpl.Text {
			t.Fatalf("template %s: got %q", tmpl.Name, got)
		}
	}
	state, err := workflow.LoadWorktreeState(root)
	if err != nil || state["start"] != (workflow.WorktreeEntry{}) || len(state) != 1+len(workflow.Templates()) {
		t.Fatalf("state: %+v %v", state, err)
	}
	if code := workflowNew(root, "start", "", &out); code != 1 {
		t.Fatal("new over an existing file")
	}
	if err := os.WriteFile(workflow.WorktreeFile(root, "start"), []byte("meins"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := workflowNew(root, "start", "", &out); code != 1 {
		t.Fatal("new over an edited file")
	}
	if got, _ := workflow.ReadWorktreeFile(root, "start"); got != "meins" {
		t.Fatal("new overwrote an edited file")
	}
	for _, bad := range []struct{ name, template string }{{"A", ""}, {"x", ""}, {"new", ""}, {"a/b", ""}, {"..", ""}, {"gut", "gibt-es-nicht"}} {
		if code := workflowNew(root, bad.name, bad.template, &bytes.Buffer{}); code != 2 {
			t.Errorf("new %q %q: exit %d, want 2", bad.name, bad.template, code)
		}
	}
}

// The help of ctx workflow names the new way and the templates the code has.
func TestWorkflowUsageNamesTheWorktreeAndEveryTemplate(t *testing.T) {
	var out bytes.Buffer
	if code := cmdWorkflow(nil, &out); code != 2 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"ctx workflow pull", "ctx workflow push", "ctx workflow new", "ctx workflow diff", "ctx workflow ls", ".ghosttree/workflows/<name>.md"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("usage lacks %q", want)
		}
	}
	for _, n := range workflow.TemplateNames {
		if !strings.Contains(workflowWorktreeUsage, n) {
			t.Errorf("usage lacks the template %q", n)
		}
	}
}

// The mirror writes beside the worktree and never into it: files that were not
// pushed yet survive every refresh, and INDEX.md names the directory.
func TestMirrorNeverTouchesTheWorkflowWorktree(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("no git: %v %s", err, out)
	}
	root := filepath.Join(repo, ".ghosttree")
	if err := workflow.WriteWorktreeFile(repo, "entwurf", wtTexts["stufe-drei"]); err != nil {
		t.Fatal(err)
	}
	if err := workflow.SaveWorktreeState(repo, workflow.WorktreeState{"entwurf": {Version: 2, Hash: "abc"}}); err != nil {
		t.Fatal(err)
	}
	docs := []mirror.Doc{{Path: "INDEX.md", Body: "index"}, {Path: "knowledge/note/1.md", Body: "k"}, {Path: "edit/ignored.md", Body: "e"}, {Path: "docs/specs/s.md", Body: "d"}}
	for i := 0; i < 2; i++ {
		if err := writeMirrorDocs(root, repo, docs); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := workflow.ReadWorktreeFile(repo, "entwurf"); err != nil || got != wtTexts["stufe-drei"] {
		t.Fatalf("the mirror changed the worktree: %q %v", got, err)
	}
	if state, _ := workflow.LoadWorktreeState(repo); state["entwurf"].Version != 2 {
		t.Fatalf("the mirror changed the state: %+v", state)
	}
	// Nothing of it is in the repository: git does not list the directory.
	status, err := exec.Command("git", "-C", repo, "status", "--porcelain", "--untracked-files=all").CombinedOutput()
	if err != nil || strings.Contains(string(status), "workflows") {
		t.Fatalf("the worktree shows in git status: %q %v", status, err)
	}
}

// KN-4825 m1: a pull does not write the server's text through a symbolic link
// a checkout brought into the worktree, also not with --force.
func TestWorkflowPullDoesNotWriteThroughALink(t *testing.T) {
	e := newWTEnv(t)
	if err := workflow.WriteWorktreeFile(e.root, "deploy", wtTexts["stufe-eins"]); err != nil {
		t.Fatal(err)
	}
	if code, out := e.push(t, "deploy", "erste Fassung"); code != 0 {
		t.Fatalf("push: %d %s", code, out)
	}
	outside := filepath.Join(t.TempDir(), ".bash_aliases")
	if err := os.Remove(workflow.WorktreeFile(e.root, "deploy")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, workflow.WorktreeFile(e.root, "deploy")); err != nil {
		t.Fatal(err)
	}
	for _, force := range []bool{false, true} {
		if code, out := e.pull(t, "deploy", force); code == 0 {
			t.Fatalf("pull (force %v) through a link: %s", force, out)
		}
		if _, err := os.Lstat(outside); !os.IsNotExist(err) {
			t.Fatalf("pull (force %v) wrote outside the worktree", force)
		}
	}
}

// KN-4825 m2: ctx workflow keeps its worktree out of git itself, in a fresh
// clone where the mirror never ran.
func TestWorkflowNewInAFreshCloneIsIgnoredByGit(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://github.com/alpha/app.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git %v: %v %s", args, err, out)
		}
	}
	t.Chdir(repo)
	var out bytes.Buffer
	if code := cmdWorkflowWorktree("new", []string{"entwurf"}, &out); code != 0 {
		t.Fatalf("new: %d %s", code, out.String())
	}
	status, err := exec.Command("git", "-C", repo, "status", "--porcelain", "--untracked-files=all").CombinedOutput()
	if err != nil || strings.Contains(string(status), "workflows") {
		t.Fatalf("git sees the worktree: %s %v", status, err)
	}
	exclude, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if !strings.Contains(string(exclude), ".ghosttree/workflows/") {
		t.Fatalf("exclude: %s", exclude)
	}
	// A worktree a checkout brought is refused.
	if out, err := exec.Command("git", "-C", repo, "add", "-f", ".ghosttree/workflows/entwurf.md").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	out.Reset()
	if code := cmdWorkflowWorktree("new", []string{"zweiter"}, &out); code != 1 || !strings.Contains(out.String(), "git tracks") {
		t.Fatalf("new beside a tracked file: %d %s", code, out.String())
	}
}
