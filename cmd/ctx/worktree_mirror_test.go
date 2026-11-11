package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// worktreeOf cuts a linked git worktree from a repository whose origin names
// the test project, and returns the repository and the worktree.
func worktreeOf(t *testing.T) (repo, wt string) {
	t.Helper()
	repo = newRepo(t)
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git(repo, "remote", "set-url", "origin", "https://"+cliHere+".git")
	git(repo, "commit", "-q", "-m", "start")
	wt = filepath.Join(t.TempDir(), "worker")
	git(repo, "worktree", "add", "-q", "-b", "feat/worker", wt)
	return repo, wt
}

// mirrorHas says whether a file under .ghosttree/knowledge of root names title.
func mirrorHas(t *testing.T, root, title string) bool {
	t.Helper()
	found := false
	_ = filepath.WalkDir(filepath.Join(root, ".ghosttree", "knowledge"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if raw, err := os.ReadFile(path); err == nil && strings.Contains(string(raw), title) {
				found = true
			}
		}
		return nil
	})
	return found
}

// REQ-436 AC-1: in a git worktree that was just created, what ghosttree knows
// is readable without a manual step. The session that starts there writes
// .ghosttree/ for the worktree, because it has none yet, and waits for it.
func TestFreshWorktreeIsReadableAfterTheSessionStart(t *testing.T) {
	st := moveCLIFixture(t)
	if _, err := st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "Wissen vor dem Worktree", Body: "text", Confidence: "trusted",
		Scope: scope.Axes{Project: cliHere}, Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	repo, wt := worktreeOf(t)
	if _, err := os.Stat(filepath.Join(wt, ".ghosttree")); err == nil {
		t.Fatal("a fresh worktree has a mirror before anything ran")
	}
	if _, err := os.Stat(filepath.Join(repo, ".ghosttree")); err == nil {
		t.Fatal("the main checkout has a mirror before anything ran")
	}

	sessionStart(t, wt, "s-worktree-1")

	if _, err := os.Stat(filepath.Join(wt, ".ghosttree", "INDEX.md")); err != nil {
		t.Fatalf("no mirror in the worktree after its session started: %v", err)
	}
	if !mirrorHas(t, wt, "Wissen vor dem Worktree") {
		t.Fatalf("the worktree's mirror lacks the entry that existed before it")
	}
	out, err := exec.Command("git", "-C", wt, "status", "--porcelain").CombinedOutput()
	if err != nil || strings.Contains(string(out), ".ghosttree") {
		t.Fatalf("the mirror shows up in the worktree's git status: %v\n%s", err, out)
	}
}

func subagentStartIn(t *testing.T, cwd string) string {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"session_id": "sess-parent", "cwd": cwd, "agent_id": "agent-1", "agent_type": "general-purpose", "hook_event_name": "SubagentStart"})
	var out bytes.Buffer
	if code := cmdHookWith(bytes.NewReader(raw), []string{"subagent-start", "--harness", "claude"}, &out); code != 0 {
		t.Fatalf("subagent-start exit %d", code)
	}
	var got struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output %q: %v", out.String(), err)
	}
	return got.HookSpecificOutput.AdditionalContext
}

// A subagent that works in a worktree its orchestrator cut has no session
// start of its own. Its briefing starts the write of the worktree's mirror in a
// process of its own, says that the files are on their way, and that process
// leaves the mirror behind.
func TestSubagentInAFreshWorktreeGetsTheMirrorWritten(t *testing.T) {
	st := moveCLIFixture(t)
	if _, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: "Merkzettel fuer Worker", Body: "text", Confidence: "trusted",
		Scope: scope.Axes{Project: cliHere}, Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	_, wt := worktreeOf(t)
	started := swapProjectionRefresh(t, nil)

	brief := subagentStartIn(t, wt)
	if len(*started) != 1 || !sameDir(t, (*started)[0], wt) {
		t.Fatalf("the mirror write was started for %v, want the worktree %s", *started, wt)
	}
	if !strings.Contains(brief, "being written") || !strings.Contains(brief, filepath.Join(wt, ".ghosttree")) {
		t.Fatalf("the briefing does not say where the mirror will be:\n%s", brief)
	}

	// What the started process does (ctx hook refresh-files <root>).
	if code := refreshFiles([]string{wt}); code != 0 {
		t.Fatalf("refresh-files exit %d", code)
	}
	if !mirrorHas(t, wt, "Merkzettel fuer Worker") {
		t.Fatal("the process left no mirror with the entry")
	}

	// With the mirror there, a second subagent is told where it is, and nothing
	// is started again.
	*started = nil
	brief = subagentStartIn(t, wt)
	if len(*started) != 0 || !strings.Contains(brief, filepath.Join(wt, ".ghosttree", "INDEX.md")) {
		t.Fatalf("second start: started %v\n%s", *started, brief)
	}
}

func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// REQ-436 AC-2: an entry stored during the session reaches the mirror, and the
// mirror says when it was written.
func TestStoredEntryReachesTheMirrorAndTheMirrorSaysHowOldItIs(t *testing.T) {
	st := moveCLIFixture(t)
	_, wt := worktreeOf(t)
	sessionStart(t, wt, "s-worktree-2")
	if mirrorHas(t, wt, "Eben erst gemerkt") {
		t.Fatal("the entry exists before it was stored")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	c := client.New(cfg)

	// The agent stores an entry; the session's ctx mcp calls this after the quiet
	// time (treeSettle).
	if _, err := st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "Eben erst gemerkt", Body: "text", Confidence: "trusted",
		Scope: scope.Axes{Project: cliHere}, Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(wt)
	refreshMirrorAfterWrite(c, cfg.Machine)
	if !mirrorHas(t, wt, "Eben erst gemerkt") {
		t.Fatal("the mirror does not have the entry the session just stored")
	}
	if treeSettle > 5*time.Second {
		t.Fatalf("the mirror waits %v after the last write, more than a few seconds", treeSettle)
	}

	// The index says how old the state is, and the time is the time of this run.
	raw, err := os.ReadFile(filepath.Join(wt, ".ghosttree", "INDEX.md"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`Written (\S+) `).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatalf("the index states no age:\n%s", raw)
	}
	at, err := time.Parse(time.RFC3339, m[1])
	if err != nil || time.Since(at) > time.Minute || time.Since(at) < -time.Minute {
		t.Fatalf("the index says %q (%v), not a time of this run", m[1], err)
	}
}

// Where no session start made a mirror, the refresh after a write does not
// create one: it would put files into a directory nobody asked for.
func TestMirrorRefreshAfterAWriteLeavesACheckoutWithoutMirrorAlone(t *testing.T) {
	moveCLIFixture(t)
	_, wt := worktreeOf(t)
	cfg, _ := config.Load()
	t.Chdir(wt)
	refreshMirrorAfterWrite(client.New(cfg), cfg.Machine)
	if _, err := os.Stat(filepath.Join(wt, ".ghosttree")); err == nil {
		t.Fatal("the refresh created a mirror in a checkout without one")
	}
}
