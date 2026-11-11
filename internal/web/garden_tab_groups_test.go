package web

import (
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/garden"
)

// pillCount reads the number beside a pill of the tab ("Description?" → 3).
func pillCount(page, label string) string {
	re := regexp.MustCompile(`(?s)<a class="gbtn sm[^"]*" href="[^"]*">(?:<svg.*?</svg>)?` + regexp.QuoteMeta(html.EscapeString(label)) + `<span class="counter">(\d+)</span></a>`)
	if m := re.FindStringSubmatch(page); m != nil {
		return m[1]
	}
	return ""
}

// gardenGroupsEnv files four proposals: two descriptions and a duplicate in one
// repository and a description in the other.
func gardenGroupsEnv(t *testing.T) shellEnv {
	t.Helper()
	e, _ := gardenActEnv(t)
	switchGardener(t, e.St)
	for _, path := range []string{"internal/store/a.go", "internal/store/b.go"} {
		g := ghostPath()
		g.Path, g.State, g.Old = path, garden.GhostUndescribed, ""
		describeNote(t, e, shellProject, g, ghostNew, nil)
	}
	g := ghostPath()
	g.Path, g.State, g.Old = "cmd/other/main.go", garden.GhostUndescribed, ""
	describeNote(t, e, shellHiddenProject, g, ghostNew, nil)
	one := gardenEntry(t, e.St, shellProject, "note", "Dienst eins", "trusted")
	two := gardenEntry(t, e.St, shellProject, "note", "Dienst zwei", "trusted")
	gardenDuplicateNote(t, e.St, shellProject, one, two)
	return e
}

// REQ-624 AC-3: the tab lists the open proposals grouped by verdict with the
// number of each group; the groups can be narrowed to one repository and the
// number on the tab is the number of open proposals.
func TestGardenerTabGroupsTheOpenProposalsByVerdictAndRepository(t *testing.T) {
	e := gardenGroupsEnv(t)
	status, page := fetchPage(t, e.Owner, e.Base+"/ui/alpha/gardener")
	if status != 200 {
		t.Fatalf("tab = %d", status)
	}
	if !strings.Contains(page, "4 open proposals") {
		t.Errorf("the heading does not count the open proposals:\n%s", page)
	}
	if got := pillCount(page, "Description?"); got != "3" {
		t.Errorf("Description? group = %q, want 3", got)
	}
	if got := pillCount(page, "Duplicate?"); got != "1" {
		t.Errorf("Duplicate? group = %q, want 1", got)
	}
	// The biggest group comes first.
	if strings.Index(page, "Description?<span") > strings.Index(page, "Duplicate?<span") {
		t.Error("the groups are not ordered by size")
	}
	for repo, want := range map[string]string{"shell": "3", "hidden": "1"} {
		if got := pillCount(page, repo); got != want {
			t.Errorf("repository %s = %q, want %s", repo, got, want)
		}
	}
	if _, band := fetchPage(t, e.Owner, e.Base+"/ui/alpha"); !regexp.MustCompile(`data-tab="gardener"[^>]*>(?s:.*?)<span class="counter">4</span>`).MatchString(band) {
		t.Errorf("the tab does not carry the number of open proposals:\n%s", band)
	}

	// A verdict narrows the list to its group; the other groups keep their numbers.
	_, dup := fetchPage(t, e.Owner, e.Base+"/ui/alpha/gardener?v=Duplicate%3F")
	if strings.Contains(dup, "internal/store/a.go") || !strings.Contains(dup, "Dienst eins") {
		t.Errorf("the Duplicate? view shows the wrong rows:\n%s", dup)
	}
	if got := pillCount(dup, "Description?"); got != "3" {
		t.Errorf("a verdict hides the number of the other group: %q", got)
	}

	// A repository narrows the list, and the verdict counts follow the same list.
	_, hidden := fetchPage(t, e.Owner, e.Base+"/ui/alpha/gardener?repo=hidden")
	if !strings.Contains(hidden, "cmd/other/main.go") || strings.Contains(hidden, "internal/store/a.go") || strings.Contains(hidden, "Dienst eins") {
		t.Errorf("the repository view shows the wrong rows:\n%s", hidden)
	}
	// Both filters together.
	_, both := fetchPage(t, e.Owner, e.Base+"/ui/alpha/gardener?repo=shell&v=Description%3F")
	if !strings.Contains(both, "internal/store/a.go") || !strings.Contains(both, "internal/store/b.go") || strings.Contains(both, "cmd/other/main.go") || strings.Contains(both, "Dienst eins") {
		t.Errorf("both filters together show the wrong rows:\n%s", both)
	}
	// The pill of the chosen group is marked and keeps the other filter.
	if !strings.Contains(both, `class="gbtn sm on"`) || !strings.Contains(both, "repo=shell&amp;v=Duplicate%3F") {
		t.Errorf("the pills do not mark the choice or keep the other filter:\n%s", both)
	}
	// A verdict nobody has lists nothing.
	if _, none := fetchPage(t, e.Owner, e.Base+"/ui/alpha/gardener?v=Nothing%3F"); !strings.Contains(none, "No open proposals.") || strings.Contains(none, "internal/store/a.go") {
		t.Errorf("an unknown verdict lists rows:\n%s", none)
	}
}

// REQ-624 AC-5: the tab shows the latest runs of the gardener (when, result,
// proposals, cost), the next run, and the way to the settings.
func TestGardenerTabShowsTheLatestRunsTheNextRunAndTheSettings(t *testing.T) {
	e := gardenGroupsEnv(t)
	org, _ := e.St.OrgByRef("alpha")
	account := "person:" + idStr(e.St.GardenerAccountID())
	done, err := e.St.StartGardenRun(account, org.ID, shellProject)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.FinishGardenRun(account, done, "done", 0.42, ""); err != nil {
		t.Fatal(err)
	}
	over, err := e.St.StartGardenRun(account, org.ID, shellHiddenProject)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.FinishGardenRun(account, over, "budget", 1.5, "Budget erreicht"); err != nil {
		t.Fatal(err)
	}

	_, page := fetchPage(t, e.Owner, e.Base+"/ui/alpha/gardener")
	runs := page[strings.Index(page, `class="gt-sec">Runs`):]
	for _, want := range []string{"shell", "done", "0.42 $", "hidden", "budget reached", "1.50 $"} {
		if !strings.Contains(runs, want) {
			t.Errorf("the runs lack %q:\n%s", want, runs)
		}
	}
	// The newest run is on top, and one that ran into the budget is a warning.
	if strings.Index(runs, "hidden") > strings.Index(runs, "shell") || !strings.Contains(runs, `class="lb warn">budget reached`) {
		t.Errorf("the runs are not newest first, or the budget run is not a warning:\n%s", runs)
	}
	if !strings.Contains(page, "Next run") || !strings.Contains(page, `href="/ui/orgs?org=alpha#gardener"`) {
		t.Errorf("the next run or the way to the settings is missing:\n%s", page)
	}
	// Only the owner changes the settings; a lead who reviews sees the runs but no link.
	_, lead := fetchPage(t, e.Lead, e.Base+"/ui/alpha/gardener")
	if !strings.Contains(lead, "0.42 $") || strings.Contains(lead, `#gardener"`) {
		t.Errorf("the lead sees the runs and no link to the settings: %v", strings.Contains(lead, "0.42 $"))
	}
}

// Without a run, the box says so instead of staying empty.
func TestGardenerTabWithoutARunSaysSo(t *testing.T) {
	e, _ := gardenActEnv(t)
	switchGardener(t, e.St)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/alpha/gardener")
	if !strings.Contains(page, `class="box-empty"`) || !strings.Contains(page, "No open proposals") {
		t.Errorf("an empty tab says nothing:\n%s", page)
	}
}

// The tab is for those who review the gardener's proposals, and it shows each
// of them only what their roles read: a lead or reviewer of one repository gets
// neither the proposals, nor the runs, nor the repository's name, nor its share
// of any number from the repository they have no role in ("hidden").
func TestGardenerTabFollowsTheRolesOfTheViewer(t *testing.T) {
	e := gardenGroupsEnv(t)
	org, _ := e.St.OrgByRef("alpha")
	account := "person:" + idStr(e.St.GardenerAccountID())
	over, err := e.St.StartGardenRun(account, org.ID, shellHiddenProject)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.FinishGardenRun(account, over, "budget", 1.5, "Budget erreicht"); err != nil {
		t.Fatal(err)
	}
	done, err := e.St.StartGardenRun(account, org.ID, shellProject)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.FinishGardenRun(account, done, "done", 0.42, ""); err != nil {
		t.Fatal(err)
	}

	_, owner := fetchPage(t, e.Owner, e.Base+"/ui/alpha/gardener")
	for _, want := range []string{"cmd/other/main.go", "1.50 $", "4 open proposals"} {
		if !strings.Contains(owner, want) {
			t.Fatalf("the owner lacks %q, so the check below proves nothing:\n%s", want, owner)
		}
	}
	for name, c := range map[string]*http.Client{"lead": e.Lead, "reviewer": e.Reviewer} {
		status, page := fetchPage(t, c, e.Base+"/ui/alpha/gardener")
		if status != http.StatusOK {
			t.Fatalf("%s: tab = %d", name, status)
		}
		for _, leak := range []string{"cmd/other/main.go", "1.50 $", "budget reached", ">hidden<", "repo=hidden", "x/hidden", "1.92 $", "4 open proposals"} {
			if strings.Contains(page, leak) {
				t.Errorf("%s: the tab gives away the repository without a role (%q):\n%s", name, leak, page)
			}
		}
		if !strings.Contains(page, "3 open proposals") || !strings.Contains(page, "0.42 $") {
			t.Errorf("%s: the tab lacks the proposals and the run of the repository they work in:\n%s", name, page)
		}
		if got := pillCount(page, "Description?"); got != "2" {
			t.Errorf("%s: Description? group = %q, want 2", name, got)
		}
		if got := pillCount(page, "Duplicate?"); got != "1" {
			t.Errorf("%s: Duplicate? group = %q, want 1", name, got)
		}
		if got := pillCount(page, "shell"); got != "3" || pillCount(page, "hidden") != "" {
			t.Errorf("%s: repository pills: shell=%q hidden=%q", name, got, pillCount(page, "hidden"))
		}
		_, band := fetchPage(t, c, e.Base+"/ui/alpha")
		if !regexp.MustCompile(`data-tab="gardener"[^>]*>(?s:.*?)<span class="counter">3</span>`).MatchString(band) {
			t.Errorf("%s: the number on the tab counts what they may not see:\n%s", name, band)
		}
		// The repository they have no role in is as unknown as one that does not exist.
		_, narrowed := fetchPage(t, c, e.Base+"/ui/alpha/gardener?repo=hidden")
		_, missing := fetchPage(t, c, e.Base+"/ui/alpha/gardener?repo=nothere")
		same := func(page, repo string) string {
			page = regexp.MustCompile(`name="csrf_token" value="[0-9a-f]+"`).ReplaceAllString(page, "")
			return strings.ReplaceAll(page, "repo="+repo, "repo=REPO")
		}
		if strings.Contains(narrowed, "cmd/other/main.go") || same(narrowed, "hidden") != same(missing, "nothere") {
			t.Errorf("%s: ?repo=hidden is answered differently from ?repo=nothere", name)
		}
	}
	// The owner sees the month's spending, the others do not: it adds up every repository.
	if !strings.Contains(owner, "1.92 $") {
		t.Errorf("the owner does not see the spending of the month:\n%s", owner)
	}
	// A member without the right to review and a guest get no tab at all.
	for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest} {
		status, page := fetchPage(t, c, e.Base+"/ui/alpha/gardener")
		if status != http.StatusNotFound || strings.Contains(page, "cmd/other/main.go") || strings.Contains(page, "internal/store/a.go") {
			t.Errorf("%s: tab = %d, want 404 without proposals", name, status)
		}
	}
}
