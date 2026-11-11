package mcpserver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// realWorktree makes a git repository with one commit and a linked worktree on
// a new branch, and returns the worktree's path.
func realWorktree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	main := filepath.Join(root, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	run(main, "init", "-q")
	run(main, "commit", "-q", "--allow-empty", "-m", "start")
	wt := filepath.Join(root, "wt-worker")
	run(main, "worktree", "add", "-q", "-b", "feat/worker", wt)
	return wt
}

// REQ-436 AC-5: request_start_work from a git worktree finds the session of the
// agent that works there, also when the project is crowded with newer sessions
// of other agents (an orchestrator with workers), and also after the worker
// moved to another branch. Before, only the 20 newest sessions of the project
// were looked at, and the own one was missing from them.
func TestStartWorkFromAWorktreeFindsItsSessionAmongManyNewerOnes(t *testing.T) {
	wt := realWorktree(t)
	s, st := pkgServer(t, "feat/worker")
	s.SetLive(func() Live { return Live{Branch: "feat/worker", CWD: wt} })
	req := pkgRequests(t, st, "r")[0].Request.ID

	upsert := func(ext, cwd, branch string) {
		t.Helper()
		if _, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: ext, CWD: cwd,
			Scope: scope.Axes{Project: pkgProject, Branch: branch, Machine: "workstation-a"}}); err != nil {
			t.Fatal(err)
		}
	}
	// The worker's session began on the branch the worktree was cut from.
	upsert("worker", wt, "main")
	for i := 0; i < 45; i++ {
		upsert("other-"+strconv.Itoa(i), "/work/other-"+strconv.Itoa(i), "feat/other-"+strconv.Itoa(i))
	}

	_, out, err := s.handleRequestStartWork(context.Background(), nil, RequestStartWorkInput{RequestID: req})
	if err != nil {
		t.Fatalf("start_work from the worktree: %v", err)
	}
	sessions, err := st.ListSessions(scope.Axes{Project: pkgProject}, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, sess := range sessions {
		if sess.ID == out.Work.SessionID && (sess.ExternalID != "worker" || sess.CWD != wt) {
			t.Fatalf("attached to %s in %s, want the worker in %s", sess.ExternalID, sess.CWD, wt)
		}
	}
	if out.Work.SessionID == 0 || out.Work.State != "active" {
		t.Fatalf("work %+v", out.Work)
	}
}

// A subagent calls through the MCP process of its parent; that process knows
// the harness session id, which is exact however crowded the project is.
func TestStartWorkOfASubagentUsesTheSessionIdOfItsParent(t *testing.T) {
	wt := realWorktree(t)
	s, st := pkgServer(t, "feat/worker")
	s.SetLive(func() Live { return Live{Branch: "feat/worker", CWD: filepath.Dir(wt)} }) // the parent works elsewhere
	s.sessionRef = "parent-session"
	req := pkgRequests(t, st, "r")[0].Request.ID
	if _, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "parent-session", CWD: "/work/orchestrator",
		Scope: scope.Axes{Project: pkgProject, Branch: "dev", Machine: "workstation-a"}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 45; i++ {
		if _, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "w-" + strconv.Itoa(i), CWD: wt,
			Scope: scope.Axes{Project: pkgProject, Branch: "feat/worker", Machine: "workstation-a"}}); err != nil {
			t.Fatal(err)
		}
	}
	_, out, err := s.handleRequestStartWork(context.Background(), nil, RequestStartWorkInput{RequestID: req})
	if err != nil {
		t.Fatalf("start_work of the subagent: %v", err)
	}
	sessions, _ := st.ListSessions(scope.Axes{Project: pkgProject}, 200)
	for _, sess := range sessions {
		if sess.ID == out.Work.SessionID && sess.ExternalID != "parent-session" {
			t.Fatalf("attached to %s, want the parent's session", sess.ExternalID)
		}
	}
}
