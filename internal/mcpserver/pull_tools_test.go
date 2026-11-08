package mcpserver

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Pull Requests und Commits über MCP: Liste, Detail mit Karte, Commit mit dem,
// was dazu gesagt wurde, und Fehler für Unbekanntes.
func TestPullAndCommitToolsOverTheWire(t *testing.T) {
	c, st := newTestClient(t)
	org, err := st.CreateOrg("person:1", "X", "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConnectSourceWithInstallation("person:1", org.ID, "github", "x", 5, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimProject("person:1", linkProject, "x"); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("c", 40)
	if _, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: "am Commit gesagt", Body: "t", Person: "alice", Confidence: "trusted",
		Scope: scope.Axes{Project: linkProject, Branch: "feat/a"}, GitAnchor: &gitanchor.Anchor{Commit: sha, Branch: "feat/a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordPullRequest(store.PullRequestChange{InstallationID: 5, Remote: linkProject, HeadRemote: linkProject, Number: 9, Action: "opened",
		Title: "Der PR", Head: "feat/a", Base: "main", State: "open", Author: "alice-gh"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordPullCommits(store.PullCommitsChange{InstallationID: 5, Remote: linkProject, Number: 9, Commits: []store.PullCommit{{SHA: sha, Title: "Titel"}}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{client: c, ctxAxes: scope.Axes{Project: linkProject, Branch: "main", Machine: "workstation-a"}}
	session := connect(t, s)

	got := wireText(t, s, "pull_list", map[string]any{"state": "open"})
	mustHave(t, "pull_list", got, "1 pull requests (open 1, merged 0, closed 0)", "#9 [open] Der PR (feat/a → main, alice-gh, 1 commits)")
	got = wireText(t, s, "pull_get", map[string]any{"pr": 9})
	mustHave(t, "pull_get", got, "PR #9 [open] Der PR", "package: none bound", "1 knowledge entries", "Pull request #9", "link: "+c.ServerURL()+"/ui/x/y/pulls/9")
	got = wireText(t, s, "commit_get", map[string]any{"commit": sha[:8]})
	mustHave(t, "commit_get", got, "commit "+sha[:12]+" Titel", "1 entries were written", "#1 am Commit gesagt", "link: "+c.ServerURL()+"/ui/x/y/commit/"+sha)

	for tool, args := range map[string]map[string]any{"pull_get": {"pr": 77}, "commit_get": {"commit": strings.Repeat("e", 40)}} {
		if got, failed := callTool(t, session, tool, args); !failed || !strings.Contains(got, "not found") {
			t.Errorf("%s unknown: %v %s", tool, failed, got)
		}
	}
	if got, failed := callTool(t, session, "commit_get", map[string]any{"commit": "abc"}); !failed || !strings.Contains(got, "hexadecimal") {
		t.Errorf("short commit: %v %s", failed, got)
	}
}
