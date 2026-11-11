package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// withCommitMeta gives the second commit of the pull what GitHub says about it:
// message, parents, co-author, changed files and the state of its checks.
func withCommitMeta(t *testing.T, e shellEnv) {
	t.Helper()
	if res, err := e.St.RecordPullCommits(store.PullCommitsChange{InstallationID: 77, Remote: shellProject, Number: 12, Commits: []store.PullCommit{
		{SHA: pullShaA, Author: "alice-gh", AuthorName: "Alice", Title: "feat(web): list of pull requests", CommittedAt: "2026-11-08T10:00:00Z", Message: "feat(web): list of pull requests"},
		{SHA: pullShaB, Author: "claude", Title: "feat(web): page of a pull request", CommittedAt: "2026-11-08T11:00:00Z", Parents: []string{pullShaA},
			Message: "feat(web): page of a pull request\n\nThe page shows the checklist.\nSecond line of the body.\n\nCo-authored-by: Mia Muster <mia-secret@example.org>"},
	}}); err != nil || res.Recorded != 2 {
		t.Fatalf("record: %+v %v", res, err)
	}
	for sha, ci := range map[string]string{pullShaA: store.CommitCISuccess, pullShaB: store.CommitCIFailure} {
		if ok, err := e.St.RecordCommitDetails(store.CommitDetailsChange{InstallationID: 77, Remote: shellProject, SHA: sha, HaveCI: true, CI: ci}); err != nil || !ok {
			t.Fatalf("ci: %v %v", ok, err)
		}
	}
	if ok, err := e.St.RecordCommitDetails(store.CommitDetailsChange{InstallationID: 77, Remote: shellProject, SHA: pullShaB, HaveFiles: true, FilesTotal: 2, Additions: 30, Deletions: 10,
		Files: []store.CommitFileStat{{Path: "internal/web/pulls_ui.go", Additions: 30, Deletions: 2}, {Path: "docs/<b>x</b>.md", Additions: 0, Deletions: 8}}}); err != nil || !ok {
		t.Fatalf("files: %v %v", ok, err)
	}
}

func TestCommitPageShowsWhatGitHubSaysAndTheListShowsTheChecks(t *testing.T) {
	e, _ := pullsEnv(t)
	withCommitMeta(t, e)
	status, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/commits/"+pullShaB)
	if status != http.StatusOK {
		t.Fatalf("commit page: %d", status)
	}
	mustContain(t, "commit page", page,
		"The page shows the checklist.", "Second line of the body.", // the whole message, not only the title
		"Mia Muster",                      // the co-author
		`class="parents"`, "aaaaaaa11111", // the parent as a pill
		"Checks failed",                                                       // the state of the checks in words
		"internal/web/pulls_ui.go", "&#43;30", "docs/&lt;b&gt;x&lt;/b&gt;.md", // the changed files, escaped
		`class="dbar"`, "https://github.com/x/shell/tree/"+pullShaB, // the way to the code
	)
	mustNotContain(t, "commit page", page, "mia-secret", "Co-authored-by", "<b>x</b>.md")
	// The parent is a commit the server knows: its pill leads to its page here.
	mustContain(t, "parent link", page, `/ui/x/shell/commits/`+pullShaA)

	_, list := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/commits")
	mustContain(t, "list", list, `class="ci ok"`, `class="ci bad"`, "Checks passed", "https://github.com/x/shell/tree/"+pullShaA)
	if !strings.Contains(list, "Mia Muster") {
		t.Errorf("the co-author is missing in the list")
	}
}

func TestCommitPageWithoutFetchedDataShowsNoEmptyBlocks(t *testing.T) {
	e, _ := pullsEnv(t)
	status, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/commits/"+pullShaB)
	if status != http.StatusOK {
		t.Fatalf("commit page: %d", status)
	}
	mustNotContain(t, "commit page", page, `class="cbody"`, `class="parents"`, `class="ci `, `class="dbar"`, "Changed files", "Geänderte Dateien", "Checks")
	_, list := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/commits")
	mustNotContain(t, "list", list, `class="ci `)
}

// A guest may read the commit but gets neither message, nor files, nor checks:
// paths and texts show what is worked on at a branch the guest may not read.
func TestCommitMetaDoesNotReachAGuest(t *testing.T) {
	e, _ := pullsEnv(t)
	withCommitMeta(t, e)
	for _, path := range []string{"/ui/x/shell/commits/" + pullShaB, "/ui/x/shell/commits"} {
		status, page := fetchPage(t, e.Guest, e.Base+path)
		if status != http.StatusOK || !strings.Contains(page, "page of a pull request") {
			t.Fatalf("a guest reads the commit at %s: %d", path, status)
		}
		mustNotContain(t, "guest "+path, page, "The page shows the checklist", "Mia Muster", "pulls_ui.go", `class="ci `, `class="dbar"`, `class="parents"`)
	}
	_, member := fetchPage(t, e.Member, e.Base+"/ui/x/shell/commits/"+pullShaB)
	mustContain(t, "member", member, "The page shows the checklist", "pulls_ui.go")
}
