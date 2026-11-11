package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-601 AC-13: the text of the pull request opens the conversation, and the
// knowledge tab lists the sessions of the branch and says why it is empty.

func recordPullText(t *testing.T, st *store.Store, n int64, head, state, body string) {
	t.Helper()
	res, err := st.RecordPullRequest(store.PullRequestChange{InstallationID: 77, Remote: shellProject, HeadRemote: shellProject, Number: n, Action: "edited",
		Title: "text pull", Head: head, Base: "main", State: state, Author: "alice-gh", Body: &body})
	if err != nil || !res.Applied {
		t.Fatalf("record pull %d: %+v %v", n, res, err)
	}
}

func TestPullPageShowsItsTextAsTheFirstPostOfTheConversation(t *testing.T) {
	e := sessLinksFixture(t)
	recordPullText(t, e.St, 12, "feat/x", "open", "## Why\n\nBecause **bold** and `code`.\n\n<script>alert(1)</script>\n\n[x](javascript:alert(2))")
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/pulls/12")
	mustContain(t, "owner", page, `class="cmt first"`, "Because", "bold", "opened the pull request")
	// The text is shown as text: no markup of its own gets through.
	mustNotContain(t, "owner", page, "<script>alert(1)</script>", `href="javascript:`)
	mustContain(t, "owner", page, "&lt;script&gt;alert(1)&lt;/script&gt;")
	// The bubble comes before the commits, and the opening is not said twice.
	if first, commits := strings.Index(page, `class="cmt first"`), strings.Index(page, "pulls.ev.commit"); commits >= 0 && first > commits {
		t.Error("the text comes after the events")
	}
	if n := strings.Count(page, "opened the pull request"); n != 1 {
		t.Errorf("the opening is said %d times", n)
	}
	// A guest reads what the title says: the same right, the same text.
	_, guest := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/pulls/12")
	mustContain(t, "guest", guest, `class="cmt first"`, "Because")
	// Without a text the conversation opens with the line, as before.
	recordPullText(t, e.St, 12, "feat/x", "open", "")
	_, bare := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/pulls/12")
	mustNotContain(t, "no text", bare, `class="cmt first"`)
	mustContain(t, "no text", bare, "opened the pull request")
}

func TestPullTextOfAHiddenProjectIsNotShownToAStranger(t *testing.T) {
	e := sessLinksFixture(t)
	body := "SECRET-BODY-TEXT"
	// The installation of the hidden project is 77 or 78, as the environment decides.
	for _, installation := range []int64{77, 78} {
		if res, _ := e.St.RecordPullRequest(store.PullRequestChange{InstallationID: installation, Remote: shellHiddenProject, HeadRemote: shellHiddenProject, Number: 99, Action: "edited",
			Title: "SECRET-PULL", Head: "feat/secret", Base: "main", State: "open", Body: &body}); res.Applied {
			break
		}
	}
	if p, err := e.St.PullRequestOf(shellHiddenProject, 99); err != nil || p.Body != body {
		t.Fatalf("the text of the hidden pull request was not stored: %+v %v", p, err)
	}
	for name, c := range map[string]*http.Client{"member": e.Member, "stranger": e.Stranger, "guest": e.Guest} {
		status, page := fetchPage(t, c, e.Base+"/ui/x/hidden/pulls/99")
		if status == http.StatusOK {
			t.Errorf("%s reads the page of a hidden project: %d", name, status)
		}
		mustNotContain(t, name, page, body, "SECRET-PULL")
	}
}

func TestPullKnowledgeTabListsTheSessionsOfTheBranchByWhatTheViewerMayRead(t *testing.T) {
	e := sessLinksFixture(t)
	tab := func(c *http.Client, n string) string {
		t.Helper()
		status, body := fetchPage(t, c, e.Base+"/ui/x/shell/pulls/"+n+"?tab=knowledge")
		if status != http.StatusOK {
			t.Fatalf("status %d", status)
		}
		return body
	}
	owner := tab(e.Owner, "12")
	mustContain(t, "owner", owner, "Anchored pitfall", "Built in", aliceTitle, `href="/ui/sessions/`+e.AliceRef+`"`, "1 more session, not shared")
	mustNotContain(t, "owner", owner, annaTitle, e.AnnaRef)
	// The list is on the tab, not twice: the side column leaves it out there.
	if n := strings.Count(owner, `id="sessions"`); n != 1 {
		t.Errorf("the sessions are listed %d times", n)
	}
	member := tab(e.Member, "12")
	mustContain(t, "member", member, annaTitle, ">weak<", "1 more session, not shared")
	mustNotContain(t, "member", member, aliceTitle, e.AliceRef)
	// A guest: the number, no title, no name, no address.
	guest := tab(e.Guest, "12")
	mustContain(t, "guest", guest, "Built in", "2 more sessions, not shared")
	mustNotContain(t, "guest", guest, aliceTitle, annaTitle, e.AliceRef, e.AnnaRef, `href="/ui/sessions/`)
	// A stranger does not get the page at all.
	status, body := fetchPage(t, e.Stranger, e.Base+"/ui/x/shell/pulls/12?tab=knowledge")
	if status == http.StatusOK {
		t.Errorf("a stranger reads the tab: %d", status)
	}
	mustNotContain(t, "stranger", body, "Built in", aliceTitle, annaTitle, "Anchored pitfall")
	// The other tabs keep the block in the side column.
	_, talk := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/pulls/12")
	mustContain(t, "conversation", talk, `class="sblk" id="sessions"`, aliceTitle)
}

// An empty tab says why: what was looked at and what is there instead.
func TestPullKnowledgeTabSaysWhyItIsEmpty(t *testing.T) {
	e := sessLinksFixture(t)
	// Nothing anchored and no session: a pull request of a branch nobody worked on.
	recordPullText(t, e.St, 20, "feat/none", "open", "")
	_, none := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/pulls/20?tab=knowledge")
	mustContain(t, "no knowledge, no session", none, "No entry carries the anchor of this pull request, and no session worked on its branch.")
	mustNotContain(t, "no knowledge, no session", none, "Built in", "The sessions below")
	// Sessions without knowledge: the commit of alice's session is in this pull request too.
	recordPullText(t, e.St, 21, "feat/q", "open", "")
	if res, err := e.St.RecordPullCommits(store.PullCommitsChange{InstallationID: 77, Remote: shellProject, Number: 21, Commits: []store.PullCommit{
		{SHA: pullShaA, Author: "alice-gh", Title: "feat(web): list of pull requests", CommittedAt: "2026-11-08T10:00:00Z"}}}); err != nil || !res.Applied {
		t.Fatalf("commits: %+v %v", res, err)
	}
	_, some := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/pulls/21?tab=knowledge")
	mustContain(t, "sessions without knowledge", some, "No entry carries the anchor of this pull request yet. The sessions below worked on its branch.", "Built in", aliceTitle)
	mustNotContain(t, "sessions without knowledge", some, "and no session worked")
}
