package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func backfillGitDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "s-1", AccountID: 1,
		Scope: scope.Axes{Project: "github.com/example-org/example-repo", Branch: "feat/x"}, StartedAt: "2026-11-08T16:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	call := `{"type":"assistant","timestamp":"2026-11-08T16:06:33.854Z","cwd":"/x","gitBranch":"feat/x","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"git commit -qm fix"}}]}}`
	res := `{"type":"user","timestamp":"2026-11-08T16:06:33.965Z","message":{"role":"user","content":[{"tool_use_id":"toolu_1","type":"tool_result","content":"(Bash completed with no output)","is_error":false}]}}`
	if err := st.AppendChunks(id, []store.Chunk{{Seq: 0, Raw: call}, {Seq: 1, Raw: res}}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBackfillSessionGitCommand(t *testing.T) {
	path := backfillGitDB(t)
	var out bytes.Buffer
	if code := cmdDB([]string{"backfill-session-git", "--db", path, "--dry-run", "--pause", "1ms"}, &out); code != 0 {
		t.Fatalf("dry run exit %d:\n%s", code, out.String())
	}
	for _, want := range []string{"dry run: nothing is written", "read 1 sessions", "commit 1", "that would be written: 1 events",
		"coverage of the whole database", "commit calls",
		"reported 0, observed 0, message+pushed 0, message 0, pushed 0 (0 of these three weak), matched 0 (0 weak), none 1",
		"message and push range: 0 commits with a message checked, 0 pushes checked", "write lock per transaction"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry run output lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if code := cmdDB([]string{"backfill-session-git", "--db", path, "--pause", "1ms", "--json"}, &out); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	var rep store.BackfillGitReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("json: %v\n%s", err, out.String())
	}
	if rep.Recorded != 1 || rep.DryRun || rep.Coverage.CommitCalls.None != 1 || rep.Coverage.SessionsWithCalls != 1 {
		t.Errorf("report: %+v", rep)
	}
	out.Reset()
	if code := cmdDB([]string{"backfill-session-git", "--db", path, "--pause", "1ms"}, &out); code != 0 || !strings.Contains(out.String(), "already known: 1") {
		t.Errorf("a second run knows the event (exit %d):\n%s", code, out.String())
	}
}

func TestBackfillSessionGitCommandRefusesBadArguments(t *testing.T) {
	var out bytes.Buffer
	if code := cmdDB([]string{"backfill-session-git"}, &out); code != 2 || !strings.Contains(out.String(), "usage: ctx db backfill-session-git") {
		t.Errorf("no --db: exit %d\n%s", code, out.String())
	}
	out.Reset()
	if code := cmdDB([]string{"backfill-session-git", "--db", "/nonexistent/x.db"}, &out); code != 1 {
		t.Errorf("a missing file fails: exit %d\n%s", code, out.String())
	}
	out.Reset()
	if code := cmdDB([]string{"backfill-session-git", "--db", "x", "--batch", "0"}, &out); code != 2 {
		t.Errorf("a batch of 0: exit %d", code)
	}
}
