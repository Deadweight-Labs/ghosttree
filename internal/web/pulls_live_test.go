package web

import (
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// liveOnPullRequest lets an agent work on the issue of the package of pull
// request 12 (issue 1 of pullsEnv); ago is how long its last sign of life is.
func liveOnPullRequest(t *testing.T, ago time.Duration) shellEnv {
	t.Helper()
	e, _ := pullsEnv(t)
	sid, err := e.St.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "ext-pr", AccountID: 1, Scope: scope.Axes{Project: shellProject, Machine: "mainex"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.SetSessionVisibility(sid, e.St.Access(store.Principal{ID: "person:1", Label: "alice"}), store.VisProject); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.St.StartRequestWork(1, sid, "primary", "alice"); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-ago).UTC().Format(time.RFC3339)
	if err := e.St.RecordPathActivity([]store.PathActivity{{Project: shellProject, SessionExternalID: "ext-pr", Tool: "Edit", Path: "internal/web/pulls_ui.go", Writes: true, Quality: store.ActivityObserved, At: at, AccountID: 1}}); err != nil {
		t.Fatal(err)
	}
	return e
}

// summaryOf is the line of the issue in the checklist of the pull request.
func summaryOf(t *testing.T, page string) string {
	t.Helper()
	i := strings.Index(page, `<details class="rq"`)
	if i < 0 {
		t.Fatalf("the checklist has no issue:\n%.600s", page)
	}
	rest := page[i:]
	return rest[:strings.Index(rest, "</summary>")]
}

// REQ-599 AC-15: the checklist of a pull request shows the agent that works on
// an issue right now as a pulse at the line of the issue, and the page asks
// the live stream for it so the pulse comes and goes without a reload.
func TestPullChecklistShowsTheAgentWorkingOnAnIssueAsALivePoint(t *testing.T) {
	e := liveOnPullRequest(t, 20*time.Second)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/pulls/12?tab=checklist")
	line := summaryOf(t, page)
	for _, want := range []string{`data-live-row="1"`, `class="live"`, "claude@mainex is working on this right now"} {
		if !strings.Contains(line, want) {
			t.Errorf("the line of the issue lacks %q:\n%s", want, line)
		}
	}
	if !strings.Contains(page, `<main class="g2 pr" data-live-ids="1"`) {
		t.Error("the checklist does not name its issues for the live stream")
	}
	// The other tabs have no issues to follow.
	if _, talk := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/pulls/12"); strings.Contains(talk, "data-live-ids") {
		t.Error("the conversation opens a live stream")
	}
}

func TestPullChecklistHasNoLivePointWhenTheLastSignIsOld(t *testing.T) {
	e := liveOnPullRequest(t, 20*time.Second)
	// Ten minutes on, the sign of life of the session is old.
	atNow(t, time.Now().Add(10*time.Minute))
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/pulls/12?tab=checklist")
	line := summaryOf(t, page)
	if strings.Contains(line, `class="live"`) || strings.Contains(line, "is working on this") {
		t.Errorf("a stale agent is shown at work:\n%s", line)
	}
	// The slot stays, so the stream can fill it when the agent comes back.
	if !strings.Contains(line, `data-live-row="1"`) {
		t.Errorf("the line has no slot for the live point:\n%s", line)
	}
}

// Who may not read the issue is not told that somebody works on it.
func TestPullChecklistShowsALiveGuestNothingAboutASessionTheyCannotRead(t *testing.T) {
	e := liveOnPullRequest(t, 20*time.Second)
	_, page := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/pulls/12?tab=checklist")
	if strings.Contains(page, "claude@mainex") || strings.Contains(page, "ext-pr") {
		t.Error("a guest learns about the agent that works on the issue")
	}
}
