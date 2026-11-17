package web

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func openRun(t *testing.T, e sessEnv, name string, lines ...string) string {
	t.Helper()
	id, err := e.St.UpsertSession(store.Session{Harness: "claude-code", ExternalID: name, AccountID: 2, Scope: scope.Axes{Project: shellProject, Machine: "laptop", Branch: "main"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) > 0 {
		if err := e.St.AppendChunks(id, chunks(lines...)); err != nil {
			t.Fatal(err)
		}
	}
	sess, _ := e.St.SessionByID(id)
	return sess.PublicID
}

// REQ-688 AC-2 (N10): a run that wrote nothing into ghosttree is not an empty page: its
// tool calls stand in the ghosttree view by kind, and the title is the first request.
func TestSessionViewShowsTheToolCallsOfARunAndTitlesItByItsFirstRequest(t *testing.T) {
	e := seedSessions(t)
	call := func(id, name string, in map[string]any) map[string]any {
		return map[string]any{"type": "tool_use", "id": id, "name": name, "input": in}
	}
	pid := openRun(t, e, "busy-run",
		uLine("Ok"),
		uLine("Base directory for this skill: /home/x/.claude/skills/ghost\n# Ghost"),
		uLine("Räume den Spool auf und prüfe die Tests"),
		aLine(call("a", "Bash", map[string]any{"command": "go test ./..."}), call("b", "Read", map[string]any{"file_path": "/r/spool.go"})),
		aLine(call("c", "Grep", map[string]any{"pattern": "spool"}), call("d", "Bash", map[string]any{"command": "ls"})),
	)
	code, page := e.get(t, e.Member, "/ui/sessions/"+pid)
	if code != 200 {
		t.Fatalf("session page = %d", code)
	}
	for _, want := range []string{"made 4 tool calls", "2 commands", "1 reads", "1 searches", "Räume den Spool auf und prüfe die Tests"} {
		if !strings.Contains(page, want) {
			t.Errorf("the ghosttree view lacks %q:\n%.1200s", want, page)
		}
	}
	if strings.Contains(page, "Base directory for this skill") {
		t.Errorf("the body of a skill is shown as the title")
	}
	if strings.Contains(page, "Nothing from this run is in ghosttree yet.") {
		t.Errorf("a run with 4 tool calls says it left nothing")
	}
	// A run without anything says so and leads to its history.
	quiet := openRun(t, e, "quiet-run", uLine("Hallo"))
	_, empty := e.get(t, e.Member, "/ui/sessions/"+quiet)
	if !strings.Contains(empty, "Nothing from this run is in ghosttree yet.") || !strings.Contains(empty, "view=raw") {
		t.Errorf("the empty view does not lead to the raw history:\n%.800s", empty)
	}
}

// REQ-688 AC-2 (N10): sharing asks before more people may read, and says who.
func TestSharingAsksBeforeMorePeopleMayRead(t *testing.T) {
	e := seedSessions(t)
	pid := openRun(t, e, "to-share", uLine("Prüfe den Hook"))
	token := renderedCSRFToken(t, e.Member, e.Base+"/ui/sessions")
	resp := sameOriginPostForm(t, e.Member, e.Base+"/ui/sessions/"+pid+"/share", map[string][]string{"csrf_token": {token}, "level": {store.VisProject}})
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "confirm=project") {
		t.Fatalf("sharing with the project did not ask: %q", loc)
	}
	_, ask := e.get(t, e.Member, resp.Header.Get("Location"))
	if !strings.Contains(ask, "Everyone in the project can then read this session") {
		t.Errorf("the question does not say who may read:\n%.600s", ask)
	}
	// Taking the session back asks nothing.
	e.St.DB().Exec(`UPDATE sessions SET visibility='project', shared=1 WHERE public_id=?`, pid)
	resp = sameOriginPostForm(t, e.Member, e.Base+"/ui/sessions/"+pid+"/share", map[string][]string{"csrf_token": {renderedCSRFToken(t, e.Member, e.Base+"/ui/sessions")}, "level": {store.VisPrivate}})
	if strings.Contains(resp.Header.Get("Location"), "confirm=") {
		t.Errorf("narrowing asked a question: %q", resp.Header.Get("Location"))
	}
}
