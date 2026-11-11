package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Alle Tests laufen mit einem echten git in temporären Repos. Die Zeiten sind fest:
// GIT_COMMITTER_DATE bestimmt auch die Zeit des Reflog-Eintrags, so liegt jeder
// Schritt in seiner eigenen Sekunde.

const t0 int64 = 1_780_000_000

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func unix(at int64) time.Time { return time.Unix(at, 0).UTC() }

// runGit führt git in dir zur Zeit at aus und gibt die Ausgabe zurück.
func runGit(t *testing.T, dir string, at int64, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(gitEnv(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.invalid",
		"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.invalid",
		fmt.Sprintf("GIT_AUTHOR_DATE=%d +0000", at), fmt.Sprintf("GIT_COMMITTER_DATE=%d +0000", at),
		"GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

func newRepo(t *testing.T) string {
	t.Helper()
	needGit(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, t0, "init", "-q")
	return dir
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// commitFile legt eine Datei an und committet sie leise zur Zeit at.
func commitFile(t *testing.T, dir string, at int64, name string) string {
	t.Helper()
	writeFile(t, dir, name, name+"\n")
	runGit(t, dir, at, "add", name)
	runGit(t, dir, at, "commit", "-q", "-m", "add "+name)
	return runGit(t, dir, at, "rev-parse", "HEAD")
}

// patchIDOf bildet die Patch-ID auf einem anderen Weg als der Collector (diff-tree
// statt log).
func patchIDOf(t *testing.T, dir, sha string) string {
	t.Helper()
	patch := runGit(t, dir, t0, "diff-tree", "-p", "--root", "--no-renames", sha)
	cmd := exec.Command("git", "patch-id", "--stable")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(patch + "\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	id, _, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	return id
}

func observeAt(t *testing.T, dir string, from, to int64) []store.ObservedCommit {
	t.Helper()
	got, ok := newReflogReader().observe(context.Background(), dir, unix(from), unix(to))
	if !ok {
		t.Fatalf("observe(%s) could not read the reflog", dir)
	}
	return got
}

func shas(c []store.ObservedCommit) []string {
	var out []string
	for _, o := range c {
		out = append(out, o.SHA)
	}
	return out
}

func TestObserveFindsTheShaOfAQuietCommit(t *testing.T) {
	dir := newRepo(t)
	sha := commitFile(t, dir, t0+10, "a")
	got := observeAt(t, dir, t0+10, t0+10)
	if len(got) != 1 || got[0].SHA != sha || got[0].Op != "commit" {
		t.Fatalf("want the one commit %s, got %+v", sha, got)
	}
	if got[0].PatchID == "" || got[0].PatchID != patchIDOf(t, dir, sha) {
		t.Errorf("patch id %q, want %q", got[0].PatchID, patchIDOf(t, dir, sha))
	}
	if want := unix(t0 + 10).Format(time.RFC3339); got[0].CommittedAt != want {
		t.Errorf("committed_at %q, want %q", got[0].CommittedAt, want)
	}
	// Eine Stunde später ist nichts: kein Fehler, nur kein Treffer.
	if got := observeAt(t, dir, t0+3600, t0+3601); len(got) != 0 {
		t.Errorf("a window without a commit finds nothing, got %+v", got)
	}
}

func TestObserveAmendReportsTheReplacementNotTheOriginal(t *testing.T) {
	dir := newRepo(t)
	first := commitFile(t, dir, t0+10, "a")
	writeFile(t, dir, "a", "changed\n")
	runGit(t, dir, t0+70, "commit", "-q", "-a", "--amend", "-m", "add a, reworded")
	amended := runGit(t, dir, t0+70, "rev-parse", "HEAD")
	got := observeAt(t, dir, t0+70, t0+70)
	if len(got) != 1 || got[0].SHA != amended || got[0].Op != "amend" {
		t.Fatalf("want the amended commit %s as amend, got %+v", amended, got)
	}
	if amended == first {
		t.Fatal("the test needs a new sha")
	}
	// Das Fenster des ersten Aufrufs nennt weiter den ersten Commit.
	if got := observeAt(t, dir, t0+10, t0+10); len(got) != 1 || got[0].SHA != first || got[0].Op != "commit" {
		t.Errorf("the first window keeps the first commit, got %+v", got)
	}
}

func TestObserveRebaseReportsTheNewCommitsWithTheirOldPatchIDs(t *testing.T) {
	dir := newRepo(t)
	commitFile(t, dir, t0+10, "base")
	runGit(t, dir, t0+11, "checkout", "-q", "-b", "feat")
	f1 := commitFile(t, dir, t0+20, "f1")
	f2 := commitFile(t, dir, t0+30, "f2")
	runGit(t, dir, t0+31, "checkout", "-q", "main")
	commitFile(t, dir, t0+40, "m1")
	runGit(t, dir, t0+41, "checkout", "-q", "feat")
	runGit(t, dir, t0+100, "rebase", "-q", "main")
	n1 := runGit(t, dir, t0+100, "rev-parse", "HEAD~1")
	n2 := runGit(t, dir, t0+100, "rev-parse", "HEAD")
	got := observeAt(t, dir, t0+100, t0+100)
	if len(got) != 2 || got[0].SHA != n1 || got[1].SHA != n2 {
		t.Fatalf("want the two picked commits %s %s (not the start or finish of the rebase), got %+v", n1, n2, got)
	}
	for _, g := range got {
		if g.Op != "rebase" {
			t.Errorf("%s: op %q, want rebase", g.SHA, g.Op)
		}
	}
	// Dieselbe Änderung unter anderer SHA hat dieselbe Patch-ID: so findet der Server
	// die Session des ursprünglichen Commits wieder.
	if got[0].PatchID == "" || got[0].PatchID != patchIDOf(t, dir, f1) || got[1].PatchID != patchIDOf(t, dir, f2) {
		t.Errorf("patch ids %q %q, want those of the originals %q %q", got[0].PatchID, got[1].PatchID, patchIDOf(t, dir, f1), patchIDOf(t, dir, f2))
	}
}

func TestObserveCherryPickAndMerge(t *testing.T) {
	dir := newRepo(t)
	commitFile(t, dir, t0+10, "base")
	runGit(t, dir, t0+11, "checkout", "-q", "-b", "side")
	picked := commitFile(t, dir, t0+20, "side")
	runGit(t, dir, t0+21, "checkout", "-q", "main")
	commitFile(t, dir, t0+30, "main")
	runGit(t, dir, t0+40, "cherry-pick", picked)
	cp := runGit(t, dir, t0+40, "rev-parse", "HEAD")
	got := observeAt(t, dir, t0+40, t0+40)
	if len(got) != 1 || got[0].SHA != cp || got[0].Op != "cherry-pick" || got[0].PatchID != patchIDOf(t, dir, picked) {
		t.Fatalf("want the cherry-pick %s with the patch id of %s, got %+v", cp, picked, got)
	}
}

func TestObserveMergeCommitButNotAFastForward(t *testing.T) {
	dir := newRepo(t)
	commitFile(t, dir, t0+10, "base")
	runGit(t, dir, t0+11, "checkout", "-q", "-b", "ff")
	commitFile(t, dir, t0+20, "ff")
	runGit(t, dir, t0+21, "checkout", "-q", "main")
	runGit(t, dir, t0+30, "merge", "-q", "ff")
	if got := observeAt(t, dir, t0+30, t0+30); len(got) != 0 {
		t.Errorf("a fast-forward creates no commit, got %+v", got)
	}
	runGit(t, dir, t0+40, "checkout", "-q", "-b", "diverged")
	commitFile(t, dir, t0+41, "d")
	runGit(t, dir, t0+42, "checkout", "-q", "main")
	commitFile(t, dir, t0+43, "e")
	runGit(t, dir, t0+50, "merge", "-q", "--no-ff", "-m", "merge diverged", "diverged")
	merge := runGit(t, dir, t0+50, "rev-parse", "HEAD")
	got := observeAt(t, dir, t0+50, t0+50)
	if len(got) != 1 || got[0].SHA != merge || got[0].Op != "merge" {
		t.Fatalf("want the merge commit %s, got %+v", merge, got)
	}
}

func TestObserveKeepsWorktreesApart(t *testing.T) {
	dir := newRepo(t)
	commitFile(t, dir, t0+5, "base")
	wt := filepath.Join(filepath.Dir(dir), filepath.Base(dir)+"-wt")
	runGit(t, dir, t0+6, "worktree", "add", "-q", "-b", "wtb", wt)
	inWT := commitFile(t, wt, t0+10, "wt")
	inMain := commitFile(t, dir, t0+10, "main")
	if got := observeAt(t, wt, t0+10, t0+10); len(got) != 1 || got[0].SHA != inWT {
		t.Errorf("the worktree sees its own commit %s, got %v", inWT, shas(got))
	}
	if got := observeAt(t, dir, t0+10, t0+10); len(got) != 1 || got[0].SHA != inMain {
		t.Errorf("the main checkout sees its own commit %s, got %v", inMain, shas(got))
	}
	// Ein Aufruf in einem Worktree des Repos darf aus dem Repo der Session gelesen werden.
	if got, ok := ownRepoDir(dir, wt); !ok || got != wt {
		t.Errorf("a worktree of the same repo is readable, got %q %v", got, ok)
	}
	if got, ok := ownRepoDir(wt, dir); !ok || got != dir {
		t.Errorf("and the other way round, got %q %v", got, ok)
	}
}

func TestOwnRepoDirReadsOnlyTheSessionsOwnRepo(t *testing.T) {
	repo := newRepo(t)
	other := newRepo(t)
	if err := os.Mkdir(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(repo, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, base, call string
		want             bool
	}{
		{"the tool's own directory", repo, "", true},
		{"a subdirectory, relative", repo, "sub", true},
		{"a subdirectory, absolute", repo, filepath.Join(repo, "sub"), true},
		{"another repo by git -C", repo, other, false},
		{"a sibling by a relative path", repo, "../" + filepath.Base(other), false},
		{"a symlink into another repo", repo, "escape", false},
		{"a home shortcut", repo, "~/x", false},
		{"a shell variable", repo, "$HOME", false},
		{"a directory that does not exist", repo, "nope", false},
		{"no working directory", "", "", false},
		{"a relative working directory", "relative/dir", "", false},
	} {
		if _, ok := ownRepoDir(c.base, c.call); ok != c.want {
			t.Errorf("%s: readable = %v, want %v", c.name, ok, c.want)
		}
	}
	// Ohne Repo unter der Session gilt nur, was unter ihrem Verzeichnis liegt.
	plain, _ := filepath.EvalSymlinks(t.TempDir())
	inner := filepath.Join(plain, "project")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := ownRepoDir(plain, "project"); !ok {
		t.Error("a directory below the session's working directory is its own")
	}
	if _, ok := ownRepoDir(plain, other); ok {
		t.Error("a repo elsewhere is not")
	}
}

// countingRunner zählt die git-Prozesse.
type countingRunner struct {
	n     int
	calls [][]string
}

func (c *countingRunner) run(ctx context.Context, dir string, stdin []byte, limit int, args ...string) ([]byte, error) {
	c.n++
	c.calls = append(c.calls, args)
	return execGit(ctx, dir, stdin, limit, args...)
}

func TestObserveStartsAtMostThreeProcesses(t *testing.T) {
	dir := newRepo(t)
	for i := 0; i < 5; i++ {
		commitFile(t, dir, t0+10, fmt.Sprintf("f%d", i))
	}
	c := &countingRunner{}
	r := &reflogReader{run: c.run}
	got, ok := r.observe(context.Background(), dir, unix(t0+10), unix(t0+10))
	if !ok || len(got) != 5 {
		t.Fatalf("want five commits, got %d (ok %v)", len(got), ok)
	}
	if c.n != 3 {
		t.Errorf("five commits cost %d git processes, want 3 (log -g, log -p, patch-id): %v", c.n, c.calls)
	}
	c.n = 0
	if got, ok := r.observe(context.Background(), dir, unix(t0+9000), unix(t0+9001)); !ok || len(got) != 0 {
		t.Fatalf("an empty window finds nothing, got %v %v", got, ok)
	}
	if c.n != 1 {
		t.Errorf("an empty window costs %d processes, want 1", c.n)
	}
	for _, args := range c.calls {
		for _, a := range args {
			for _, net := range []string{"fetch", "pull", "push", "clone", "ls-remote", "remote"} {
				if a == net {
					t.Errorf("a network command: %v", args)
				}
			}
		}
	}
}

func TestObserveWithoutARepoIsSilent(t *testing.T) {
	needGit(t)
	plain, _ := filepath.EvalSymlinks(t.TempDir())
	c := &countingRunner{}
	o := &observer{reader: &reflogReader{run: c.run}, now: func() time.Time { return unix(t0 + 60) }}
	if got := o.observeCall(plain, "", unix(t0).Format(time.RFC3339Nano), unix(t0+1).Format(time.RFC3339Nano)); got != nil {
		t.Errorf("no repo, no commits: %+v", got)
	}
	// Ein Verzeichnis, das es nicht mehr gibt (ein gelöschter Worktree), kostet gar nichts.
	c.n = 0
	if got := o.observeCall(filepath.Join(plain, "gone"), "", unix(t0).Format(time.RFC3339Nano), unix(t0).Format(time.RFC3339Nano)); got != nil || c.n != 0 {
		t.Errorf("a directory that is gone: %+v after %d processes", got, c.n)
	}
}

func TestObserverBounds(t *testing.T) {
	needGit(t)
	repo := newRepo(t)
	commitFile(t, repo, t0+10, "a")
	now := unix(t0 + 60)
	c := &countingRunner{}
	o := &observer{reader: &reflogReader{run: c.run}, now: func() time.Time { return now }}
	started := unix(t0 + 10).Format(time.RFC3339Nano)
	// Ein Aufruf, der älter als reflogMaxAge ist, wird nicht mehr beobachtet.
	now = unix(t0 + 10).Add(reflogMaxAge + time.Hour)
	if got := o.observeCall(repo, "", started, started); got != nil || c.n != 0 {
		t.Errorf("an old call is not observed: %+v after %d processes", got, c.n)
	}
	// Je Fenster höchstens observeBurst.
	now = unix(t0 + 60)
	for i := 0; i < observeBurst+20; i++ {
		o.observeCall(repo, "", started, started)
	}
	if c.n > observeBurst*3 {
		t.Errorf("a burst is capped at %d observations, started %d processes", observeBurst, c.n)
	}
	used := c.n
	o.observeCall(repo, "", started, started)
	if c.n != used {
		t.Error("over the burst nothing more is started")
	}
	now = now.Add(observeWindow + time.Second)
	o.observeCall(repo, "", started, started)
	if c.n == used {
		t.Error("a new window allows again")
	}
}

// -------------------------------------------- im Transkript, von Anfang bis Ende

func claudeCall(id, cmd, ts, cwd string) string {
	b, _ := json.Marshal(cmd)
	return `{"type":"assistant","timestamp":"` + ts + `","cwd":"` + cwd + `","gitBranch":"feat/x","message":{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"Bash","input":{"command":` + string(b) + `}}]}}`
}

func claudeResult(id, ts, cwd string) string {
	return `{"type":"user","timestamp":"` + ts + `","cwd":"` + cwd + `","message":{"role":"user","content":[{"tool_use_id":"` + id + `","type":"tool_result","content":"(Bash completed with no output)","is_error":false}]}}`
}

func stamp(at int64) string { return unix(at).Format("2006-01-02T15:04:05.000Z") }

func TestGitScanObservesAQuietCommit(t *testing.T) {
	repo := newRepo(t)
	sha := commitFile(t, repo, t0+10, "a")
	c := &countingRunner{}
	g := newGitScan("claude-code", "s-1")
	g.observer = &observer{reader: &reflogReader{run: c.run}, now: func() time.Time { return unix(t0 + 120) }}
	g.line(1, claudeCall("toolu_1", `git add -A && git commit -qm "add a"`, stamp(t0+10), repo))
	g.line(2, claudeResult("toolu_1", stamp(t0+10), repo))
	if len(g.queue) != 1 {
		t.Fatalf("want one event, got %+v", g.queue)
	}
	ev := g.queue[0]
	if len(ev.SHAs) != 0 {
		t.Errorf("a quiet commit prints no sha, got %v", ev.SHAs)
	}
	if len(ev.Observed) != 1 || ev.Observed[0].SHA != sha || ev.Observed[0].Op != "commit" || ev.Observed[0].PatchID == "" {
		t.Fatalf("the reflog names the commit %s, got %+v", sha, ev.Observed)
	}
	if c.n != 3 {
		t.Errorf("one commit call, %d processes, want 3", c.n)
	}
	// Auf der Leitung heißt es observed.
	b, _ := json.Marshal(ev)
	if !strings.Contains(string(b), `"observed":[{"sha":"`+sha+`"`) {
		t.Errorf("the wire form carries observed: %s", b)
	}
}

func TestGitScanObservesOnlyWhatCanHaveMadeACommit(t *testing.T) {
	repo := newRepo(t)
	other := newRepo(t)
	commitFile(t, repo, t0+10, "a")
	commitFile(t, other, t0+10, "b")
	c := &countingRunner{}
	newScan := func() *gitScan {
		g := newGitScan("claude-code", "s-1")
		g.observer = &observer{reader: &reflogReader{run: c.run}, now: func() time.Time { return unix(t0 + 120) }}
		return g
	}
	run := func(g *gitScan, cmd, result string) {
		g.line(1, claudeCall("toolu_1", cmd, stamp(t0+10), repo))
		g.line(2, result)
	}
	// Ein fehlgeschlagener Commit.
	g := newScan()
	run(g, `git commit -qm x`, `{"type":"user","timestamp":"`+stamp(t0+10)+`","message":{"role":"user","content":[{"tool_use_id":"toolu_1","type":"tool_result","content":"Exit code 1\nnothing to commit","is_error":true}]}}`)
	if len(g.queue) != 1 || g.queue[0].OK || len(g.queue[0].Observed) != 0 || c.n != 0 {
		t.Errorf("a failed commit is an event without a reflog read: %+v after %d processes", g.queue, c.n)
	}
	// Push und gh pr create sind keine Commits.
	for _, cmd := range []string{"git push origin feat/x", "gh pr create --fill", "git status", "git log --oneline"} {
		g := newScan()
		run(g, cmd, claudeResult("toolu_1", stamp(t0+10), repo))
		for _, e := range g.queue {
			if len(e.Observed) != 0 {
				t.Errorf("%q: observed %+v", cmd, e.Observed)
			}
		}
	}
	if c.n != 0 {
		t.Errorf("without a commit call no git process starts, %d did", c.n)
	}
	// git -C in ein fremdes Repo: der Vorgang bleibt, gelesen wird dort nicht.
	g = newScan()
	run(g, "git -C "+other+" commit -qm b", claudeResult("toolu_1", stamp(t0+10), repo))
	if len(g.queue) != 1 || len(g.queue[0].Observed) != 0 || c.n != 0 {
		t.Errorf("another repo is not read: %+v after %d processes", g.queue, c.n)
	}
	// Ein Auftrag an einen Sub-Agenten, der den Befehl nur nennt: weder Vorgang noch Prozess.
	g = newScan()
	g.line(1, `{"type":"assistant","timestamp":"`+stamp(t0+10)+`","cwd":"`+repo+`","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_9","name":"Task","input":{"prompt":"run git commit -m x when done"}}]}}`)
	if len(g.queue) != 0 || c.n != 0 {
		t.Errorf("a mention is no call: %+v after %d processes", g.queue, c.n)
	}
}

func TestGitScanFallsBackToTheSessionsWorkingDirectory(t *testing.T) {
	repo := newRepo(t)
	sha := commitFile(t, repo, t0+10, "a")
	g := newGitScan("codex", "s-1")
	g.sessionCwd = repo
	g.observer = &observer{reader: newReflogReader(), now: func() time.Time { return unix(t0 + 120) }}
	// Codex nennt kein Verzeichnis je Zeile: es gilt das der Session.
	call := `{"timestamp":"` + stamp(t0+10) + `","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"call_1","arguments":"{\"cmd\":\"git commit -qm a\"}"}}`
	out := `{"timestamp":"` + stamp(t0+10) + `","type":"response_item","payload":{"type":"function_call_output","call_id":"call_1","output":"Process exited with code 0\nOutput:\n"}}`
	g.line(1, call)
	g.line(2, out)
	if len(g.queue) != 1 || len(g.queue[0].Observed) != 1 || g.queue[0].Observed[0].SHA != sha {
		t.Fatalf("want the session's repo read, got %+v", g.queue)
	}
}
