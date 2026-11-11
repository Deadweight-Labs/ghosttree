package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const (
	pullShaA = "aaaaaaa1111111111111111111111111111111aa"
	pullShaB = "bbbbbbb2222222222222222222222222222222bb"
	pullShaC = "ccccccc3333333333333333333333333333333cc"
)

// pullsEnv legt im Projekt „shell“ drei PRs an (offen, gemergt, geschlossen), zwei
// Commits am offenen, ein Arbeitspaket mit einem Request und Wissen am Commit.
// Im Projekt „hidden“, in dem nur alice eine Rolle hat, liegt ein PR mit einem
// geheimen Titel.
func pullsEnv(t *testing.T) (shellEnv, *store.ProjectAccess) {
	t.Helper()
	e, pa := listEnv(t)
	shell, _ := e.St.ProjectByRemote(shellProject)
	hidden, _ := e.St.ProjectByRemote(shellHiddenProject)
	if _, err := e.St.ConnectSourceWithInstallation("person:1", shell.OrgID, "github", "x", 77, nil); err != nil {
		t.Fatal(err)
	}
	installHidden := int64(77)
	if hidden.OrgID != shell.OrgID {
		installHidden = 78
		if _, err := e.St.ConnectSourceWithInstallation("person:1", hidden.OrgID, "github", "x", 78, nil); err != nil {
			t.Fatal(err)
		}
	}
	record := func(project string, installation int64, c store.PullRequestChange) {
		t.Helper()
		c.InstallationID, c.Remote, c.HeadRemote = installation, project, project
		res, err := e.St.RecordPullRequest(c)
		if err != nil || !res.Applied {
			t.Fatalf("record pull %d: %+v %v", c.Number, res, err)
		}
	}
	// The knowledge comes first: opening the pull request tags the anchors of its branch.
	if _, err := e.St.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "Anchored pitfall", Body: "text", Person: "alice", Confidence: "trusted",
		Scope: scope.Axes{Project: shellProject, Branch: "feat/x"}, GitAnchor: &gitanchor.Anchor{Commit: pullShaA, Branch: "feat/x"}}); err != nil {
		t.Fatal(err)
	}
	record(shellProject, 77, store.PullRequestChange{Number: 12, Action: "opened", Title: "feat(web): Pull requests tab", Head: "feat/x", Base: "main", State: "open", Author: "alice-gh"})
	record(shellProject, 77, store.PullRequestChange{Number: 13, Action: "closed", Title: "fix(store): count guests", Head: "feat/y", Base: "main", State: "merged", Author: "Anna-GH"})
	record(shellProject, 77, store.PullRequestChange{Number: 14, Action: "closed", Title: "docs: old plan", Head: "feat/x", Base: "main", State: "closed", Author: "alice-gh"})
	record(shellHiddenProject, installHidden, store.PullRequestChange{Number: 99, Action: "opened", Title: "SECRET-PULL", Head: "feat/secret", Base: "main", State: "open", Author: "alice-gh"})
	if res, err := e.St.RecordPullCommits(store.PullCommitsChange{InstallationID: 77, Remote: shellProject, Number: 12, Commits: []store.PullCommit{
		{SHA: pullShaA, Author: "alice-gh", AuthorName: "Alice", Title: "feat(web): list of pull requests", CommittedAt: "2026-11-08T10:00:00Z"},
		{SHA: pullShaB, Author: "claude", Title: "feat(web): page of a pull request", CommittedAt: "2026-11-08T11:00:00Z"},
	}}); err != nil || !res.Applied {
		t.Fatalf("record commits: %+v %v", res, err)
	}
	req := rqMake(t, e.St, rqSpec{Title: "Pull request tab", Criteria: []string{"Lists the pull requests", "Shows the checklist"}})
	if _, err := e.St.CreatePackage(requestdomain.PackageInput{Project: shellProject, Branch: "feat/x", Name: "Git-native", PR: 12, Person: "alice",
		Members: []requestdomain.PackageMember{{RequestID: req.Request.ID, Role: requestdomain.RolePflicht}}}); err != nil {
		t.Fatal(err)
	}
	if err := e.St.SetCriterionState(req.Criteria[0].ID, "met", requestdomain.Evidence{RequestID: req.Request.ID, Kind: "commit", Ref: pullShaA, Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	return e, pa
}

func mustContain(t *testing.T, what, page string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(page, w) {
			t.Errorf("%s lacks %q", what, w)
		}
	}
}

func mustNotContain(t *testing.T, what, page string, nots ...string) {
	t.Helper()
	for _, w := range nots {
		if strings.Contains(page, w) {
			t.Errorf("%s contains %q", what, w)
		}
	}
}

func TestPullListShowsTabsRowsProgressAndFilters(t *testing.T) {
	e, _ := pullsEnv(t)
	base := e.Base + "/ui/x/shell/pulls"
	code, page := fetchPage(t, e.Owner, base)
	if code != http.StatusOK {
		t.Fatalf("pull list = %d", code)
	}
	mustContain(t, "pull list", page, `value="is:pr is:open"`, "feat(web): Pull requests tab", "#12", `href="/ui/x/shell/pulls/12"`, "Feature", "alice-gh",
		`k-branch`, "feat/x", "→ main", `aria-label="Pull request, open"`, "1 of 2 criteria met", "1/2",
		`<span class="n">1</span> Open`, `<span class="n">1</span> Merged`, `<span class="n">1</span> Closed`,
		`href="https://github.com/x/shell/pulls"`, `aria-label="Open on GitHub: pull requests"`, `href="https://github.com/x/shell/compare"`, "New pull request")
	mustNotContain(t, "open tab", page, "fix(store): count guests", "docs: old plan", "SECRET")
	// The tab of the repository carries the number of open pull requests.
	mustContain(t, "repo band", page, `data-tab="pulls"`)

	_, merged := fetchPage(t, e.Owner, base+"?q="+url.QueryEscape("is:merged"))
	mustContain(t, "merged tab", merged, "fix(store): count guests", `aria-label="Pull request, merged"`, "Anna-GH")
	mustNotContain(t, "merged tab", merged, "feat(web): Pull requests tab")
	_, all := fetchPage(t, e.Owner, base+"?q="+url.QueryEscape("is:any"))
	mustContain(t, "all", all, "feat(web): Pull requests tab", "fix(store): count guests", "docs: old plan")
	_, byAuthor := fetchPage(t, e.Owner, base+"?q="+url.QueryEscape("is:any author:anna-gh"))
	mustContain(t, "author filter", byAuthor, "fix(store): count guests")
	mustNotContain(t, "author filter", byAuthor, "feat(web): Pull requests tab")
	_, byBranch := fetchPage(t, e.Owner, base+"?q="+url.QueryEscape("is:any branch:feat/y"))
	mustContain(t, "branch filter", byBranch, "fix(store): count guests")
	mustNotContain(t, "branch filter", byBranch, "docs: old plan")
	_, byText := fetchPage(t, e.Owner, base+"?q="+url.QueryEscape("is:any plan"))
	mustContain(t, "text filter", byText, "docs: old plan", `<span class="n">1</span> Closed`)
	mustNotContain(t, "text filter", byText, "fix(store): count guests")
	_, none := fetchPage(t, e.Owner, base+"?q="+url.QueryEscape("nothing-like-this"))
	mustContain(t, "no match", none, "No pull request matches this search.")
	_, unsupported := fetchPage(t, e.Owner, base+"?q="+url.QueryEscape("is:open label:ui"))
	mustContain(t, "unsupported", unsupported, "is-unsupported", "label:ui")
	// The dropdowns list the authors and branches of the project.
	mustContain(t, "menus", page, "Author", "Branch", "Any author", "Anna-GH", "feat/y")
	// A guest reads the list; a pull request of a project without a role is unknown.
	if code, guest := fetchPage(t, e.Guest, base); code != http.StatusOK || !strings.Contains(guest, "feat(web): Pull requests tab") {
		t.Errorf("guest pull list = %d", code)
	} else if strings.Contains(guest, "compare") {
		t.Error("a guest gets the button for a new pull request")
	}
	if code, hidden := fetchPage(t, e.Member, e.Base+"/ui/x/hidden/pulls"); code != http.StatusNotFound || strings.Contains(hidden, "SECRET") {
		t.Errorf("pulls of a hidden project = %d", code)
	}
}

func TestPullListPagesAndNoPullsYet(t *testing.T) {
	e, _ := pullsEnv(t)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/pulls?offset=500")
	mustContain(t, "past the end", page, "No pull request matches this search.")
	if code, _ := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/pulls?offset=abc"); code != http.StatusOK {
		t.Errorf("a bad offset breaks the page: %d", code)
	}
	plain, _ := listEnv(t)
	_, empty := fetchPage(t, plain.Owner, plain.Base+"/ui/x/shell/pulls")
	mustContain(t, "empty list", empty, "No pull requests yet.")
}

func TestPullPageShowsConversationChecklistKnowledgeAndCommits(t *testing.T) {
	e, _ := pullsEnv(t)
	base := e.Base + "/ui/x/shell/pulls/12"
	code, page := fetchPage(t, e.Owner, base)
	if code != http.StatusOK {
		t.Fatalf("pull page = %d", code)
	}
	mustContain(t, "pull page", page, "feat(web): Pull requests tab", "#12", `class="state open"`, "Open", "alice-gh", "wants to merge 2 commits from", "feat/x", "into", "main",
		"opened the pull request", "committed 1 commit", `href="/ui/x/shell/commits/`+pullShaA+`"`, "aaaaaaa", `class="tlx"`,
		"Git-native", "1 of 2 criteria met", "Checklist", `href="https://github.com/x/shell/pull/12"`, `aria-label="Open on GitHub: pull request #12"`,
		`data-tab="talk"`, `data-tab="checklist"`, `data-tab="knowledge"`, `data-tab="commits"`, "Work package", "1/2 criteria")
	mustNotContain(t, "pull page", page, "SECRET", "<script>", "Merge pull request")

	_, list := fetchPage(t, e.Owner, base+"?tab=checklist")
	mustContain(t, "checklist tab", list, "Pull request tab", "Lists the pull requests", "Shows the checklist", `k-commit`, "aaaaaaa",
		`href="/ui/x/shell/commits/`+pullShaA+`"`, "cbx on", `href="/ui/x/shell/issues/`)
	_, know := fetchPage(t, e.Owner, base+"?tab=knowledge")
	mustContain(t, "knowledge tab", know, "Anchored pitfall", `href="/ui/x/shell/knowledge/`, "Pitfall")
	_, commits := fetchPage(t, e.Owner, base+"?tab=commits")
	mustContain(t, "commits tab", commits, "2 commits", "feat(web): list of pull requests", "feat(web): page of a pull request", "aaaaaaa", "bbbbbbb")
	// A tab that does not exist falls back to the conversation.
	_, odd := fetchPage(t, e.Owner, base+"?tab=nonsense")
	mustContain(t, "unknown tab", odd, "opened the pull request")

	// A merged pull request shows the merge in the conversation.
	_, merged := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/pulls/13")
	mustContain(t, "merged pull", merged, `class="state done"`, "Merged", "Pull request merged into", "proposed a merge from")
	_, closed := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/pulls/14")
	mustContain(t, "closed pull", closed, `class="state gray"`, "Pull request closed without a merge")

	// Guest reads; nothing to change on the page.
	if code, guest := fetchPage(t, e.Guest, base); code != http.StatusOK || strings.Contains(guest, "compare") {
		t.Errorf("guest pull page = %d", code)
	}
}

func TestPullPageIsUnknownForWhatTheViewerMayNotRead(t *testing.T) {
	e, _ := pullsEnv(t)
	for name, c := range map[string]*http.Client{"owner": e.Owner, "member": e.Member, "guest": e.Guest} {
		for _, p := range []string{"/ui/x/shell/pulls/999", "/ui/x/shell/pulls/abc", "/ui/x/shell/pulls/0", "/ui/x/shell/pulls/99", "/ui/x/hidden/pulls/99"} {
			code, page := fetchPage(t, c, e.Base+p)
			want := http.StatusNotFound
			if name == "owner" && p == "/ui/x/hidden/pulls/99" {
				want = http.StatusOK
			}
			if code != want {
				t.Errorf("%s: %s = %d, want %d", name, p, code, want)
			}
			if name != "owner" && strings.Contains(page, "SECRET") {
				t.Errorf("%s: %s leaks the title of a hidden pull request", name, p)
			}
		}
	}
	// The same pull request does not appear under another repository's address.
	if code, _ := fetchPage(t, e.Owner, e.Base+"/ui/x/hidden/pulls/12"); code != http.StatusNotFound {
		t.Errorf("pull 12 under the wrong repository = %d", code)
	}
}

func TestCommitListGroupsByDayFiltersByBranchAndPages(t *testing.T) {
	e, _ := pullsEnv(t)
	if _, err := e.St.InsertKnowledge(store.Knowledge{Type: "note", Title: "Anchored on main", Body: "text", Person: "alice", Confidence: "trusted",
		Scope: scope.Axes{Project: shellProject}, GitAnchor: &gitanchor.Anchor{Commit: pullShaC, Branch: "main"}}); err != nil {
		t.Fatal(err)
	}
	base := e.Base + "/ui/x/shell/commits"
	code, page := fetchPage(t, e.Owner, base)
	if code != http.StatusOK {
		t.Fatalf("commit list = %d", code)
	}
	mustContain(t, "commit list", page, "Commits on 8 Nov 2026", "feat(web): list of pull requests", "feat(web): page of a pull request", `href="/ui/x/shell/commits/`+pullShaA+`"`, "aaaaaaa", "bbbbbbb", "ccccccc",
		`data-copy-text="`+pullShaA+`"`, "Copy SHA", `href="/ui/x/shell/pulls/12"`, "#12", "committed", "about this commit", "All branches", "feat/x",
		`href="https://github.com/x/shell/commits"`, `aria-label="Open on GitHub: commits"`, `class="pager"`, "Commit ccccccc")
	mustNotContain(t, "commit list", page, "SECRET")
	_, byBranch := fetchPage(t, e.Owner, base+"?branch="+url.QueryEscape("feat/x"))
	mustContain(t, "branch filter", byBranch, "feat(web): list of pull requests", `href="https://github.com/x/shell/commits/feat/x"`, `aria-label="Open on GitHub: commits of feat/x"`)
	mustNotContain(t, "branch filter", byBranch, "ccccccc")
	// A branch that is none is ignored, as is a bad offset.
	_, ghost := fetchPage(t, e.Owner, base+"?branch=nope&offset=abc")
	mustContain(t, "unknown branch", ghost, "All branches")
	_, past := fetchPage(t, e.Owner, base+"?offset=500")
	mustContain(t, "past the end", past, "No more commits here.", "Back to the newest")
	// A guest sees the list, but only the default branch by name.
	code, guest := fetchPage(t, e.Guest, base)
	if code != http.StatusOK {
		t.Fatalf("guest commit list = %d", code)
	}
	mustNotContain(t, "guest commit list", guest, "feat/x")
	if code, _ := fetchPage(t, e.Member, e.Base+"/ui/x/hidden/commits"); code != http.StatusNotFound {
		t.Errorf("commits of a hidden project = %d", code)
	}
}

func TestCommitPageShowsWhatWasSaidAndAcceptsAPrefix(t *testing.T) {
	e, _ := pullsEnv(t)
	base := e.Base + "/ui/x/shell/commits/"
	code, page := fetchPage(t, e.Owner, base+pullShaA)
	if code != http.StatusOK {
		t.Fatalf("commit page = %d", code)
	}
	mustContain(t, "commit page", page, "feat(web): list of pull requests", "aaaaaaa", "alice-gh", "What was said about it", "wrote knowledge", "Anchored pitfall", `href="/ui/x/shell/knowledge/`,
		`href="https://github.com/x/shell/commit/`+pullShaA+`"`, `aria-label="Open on GitHub: commit aaaaaaa"`, `k-branch`, "feat/x", `href="/ui/x/shell/pulls/12"`, "committed")
	_, prefix := fetchPage(t, e.Owner, base+pullShaA[:8])
	mustContain(t, "prefix", prefix, "feat(web): list of pull requests")
	// A commit without anything said says so; one that is none is unknown.
	_, quiet := fetchPage(t, e.Owner, base+pullShaB)
	mustContain(t, "quiet commit", quiet, "feat(web): page of a pull request", "Nothing written about this commit yet.")
	for _, p := range []string{"zzzzzzz", "abc", "ddddddd", pullShaC} {
		if code, _ := fetchPage(t, e.Owner, base+p); code != http.StatusNotFound {
			t.Errorf("commit %q = %d, want 404", p, code)
		}
	}
	if code, _ := fetchPage(t, e.Member, e.Base+"/ui/x/hidden/commits/"+pullShaA); code != http.StatusNotFound {
		t.Errorf("commit under a hidden repository = %d", code)
	}
}

func TestCodePageNamesTheNumberOfCommits(t *testing.T) {
	e, _ := pullsEnv(t)
	if _, err := e.St.PutGhostFile(store.GhostFile{Project: shellProject, Path: "internal/web/pulls_ui.go", Kind: "file", Description: "Pull requests.",
		GitAnchor: &gitanchor.Anchor{Commit: pullShaA, Branch: "feat/x"}}); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/tree/main/internal/web")
	mustContain(t, "folder page", page, `class="ct-cc ct-nw ct-hide-m"`, "2 commits", `href="/ui/x/shell/commits"`, `href="/ui/x/shell/commits/`+pullShaA+`"`)
}

// A guest sees the default branch by name and nothing of the others: not in the
// rows, not in the page of a pull request, not as a filter.
func TestGuestDoesNotLearnBranchNamesFromPullRequests(t *testing.T) {
	e, _ := pullsEnv(t)
	base := e.Base + "/ui/x/shell/pulls"
	_, list := fetchPage(t, e.Guest, base+"?q="+url.QueryEscape("is:any"))
	mustContain(t, "guest list", list, "feat(web): Pull requests tab", "private branch")
	mustNotContain(t, "guest list", list, "feat/x", "feat/y", "Any branch")
	_, filtered := fetchPage(t, e.Guest, base+"?q="+url.QueryEscape("is:any branch:feat/x"))
	mustContain(t, "guest branch filter", filtered, "is-unsupported")
	_, page := fetchPage(t, e.Guest, base+"/12")
	mustContain(t, "guest pull page", page, "private branch")
	mustNotContain(t, "guest pull page", page, "feat/x")
	// A member sees them.
	_, member := fetchPage(t, e.Member, base+"/12")
	mustContain(t, "member pull page", member, "feat/x")
}

// The search text does not look into the names of branches a guest may not see.
func TestGuestSearchDoesNotMatchHiddenBranchNames(t *testing.T) {
	e, _ := pullsEnv(t)
	base := e.Base + "/ui/x/shell/pulls?q="
	_, guest := fetchPage(t, e.Guest, base+url.QueryEscape("is:any feat/y"))
	mustContain(t, "guest search", guest, "No pull request matches this search.")
	_, member := fetchPage(t, e.Member, base+url.QueryEscape("is:any feat/y"))
	mustContain(t, "member search", member, "fix(store): count guests")
}
