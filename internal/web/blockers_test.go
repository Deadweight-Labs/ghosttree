package web

import (
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
)

// blockedEnv is the environment of the pull request tests with an open task for
// anna on the issue of the work package of the branch feat/x.
func blockedEnv(t *testing.T) shellEnv {
	t.Helper()
	e, _ := pullsEnv(t)
	req, err := e.St.RequestByID(1)
	if err != nil || req.Request.Title != "Pull request tab" {
		t.Fatalf("the issue of the package is not request 1: %+v %v", req.Request.Title, err)
	}
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "Approve the plan", Assignee: "anna"})
	return e
}

var waitsForAnna = regexp.MustCompile(`class="waits[^"]*"[^>]*aria-label="[^"]*anna`)

// REQ-599 AC-9: whoever is waited for shows on the branch and at the repository,
// as the same red-ringed picture as on the issue and the pull request.
func TestBlockerShowsOnTheBranchTheRepositoryTheIssueAndThePullRequest(t *testing.T) {
	e := blockedEnv(t)

	_, branches := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/branches")
	rows := strings.Split(branches, `<div class="row cmrow brrow">`)[1:]
	with, without := 0, 0
	for _, row := range rows {
		switch {
		case !strings.Contains(row, `tree/feat/x"`) && !strings.Contains(row, `>main<`) && !strings.Contains(row, `tree/feat/y"`):
		case waitsForAnna.MatchString(row):
			with++
			if !strings.Contains(row, `tree/feat/x"`) {
				t.Errorf("a branch without the package shows the blocker:\n%s", row)
			}
		default:
			without++
		}
	}
	if with != 1 || without == 0 {
		t.Errorf("the branch with the package shows the blocker %d times and %d other rows show none; the list:\n%s", with, without, "")
	}

	_, code := fetchPage(t, e.Owner, e.Base+"/ui/x/shell")
	band := code[strings.Index(code, `class="rband`):]
	band = band[:strings.Index(band, "<main")]
	if !strings.Contains(band, `class="band-wait" href="/ui/x/shell/tasks"`) || !waitsForAnna.MatchString(band) {
		t.Errorf("the repository band does not show who is waited for:\n%s", band)
	}
	// The same on every page of the repository, not only on the code page.
	for _, path := range []string{"/issues", "/pulls", "/branches", "/tasks"} {
		if _, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell"+path); !strings.Contains(page, `class="band-wait"`) {
			t.Errorf("%s: no blocker in the band", path)
		}
	}
	// It was there before: the issue list and the pull request list.
	for _, path := range []string{"/issues", "/pulls"} {
		_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell"+path)
		if !waitsForAnna.MatchString(page[strings.Index(page, `class="box"`):]) {
			t.Errorf("%s: the list rows lost the blocker", path)
		}
	}
}

// A repository with no open task has no stack, and a viewer who may not list the
// tasks of a project is not shown who waits there.
func TestBlockerIsNotShownWhereNothingWaitsOrTheViewerMayNotSee(t *testing.T) {
	e, _ := pullsEnv(t)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell")
	if strings.Contains(page, "band-wait") {
		t.Error("a stack of nobody")
	}
	e = blockedEnv(t)
	if _, page := fetchPage(t, e.Guest, e.Base+"/ui/x/shell"); strings.Contains(page, "anna") {
		t.Error("a guest is told who is waited for")
	}
}
