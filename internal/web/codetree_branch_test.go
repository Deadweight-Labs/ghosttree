package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// branchGhosts legt im Projekt „shell“ eine Beschreibung auf der Projektebene und
// auf Branches an: feat/a überschreibt sie und kennt einen eigenen Pfad, feat/a-x
// ist laut PR von feat/a abgezweigt, feat/b ist ein Geschwister ohne PR.
func branchGhosts(t *testing.T, e shellEnv) {
	t.Helper()
	shell, _ := e.St.ProjectByRemote(shellProject)
	if _, err := e.St.ConnectSourceWithInstallation("person:1", shell.OrgID, "github", "x", 77, nil); err != nil {
		t.Fatal(err)
	}
	put := func(branch, path, text string) {
		t.Helper()
		g := store.GhostFile{Project: shellProject, Path: path, Kind: "file", Description: text, Branch: branch, Person: "robin"}
		if branch != "" {
			g.GitAnchor = &gitanchor.Anchor{Commit: "abcdef1234567890", Branch: branch}
		}
		if _, err := e.St.PutGhostFile(g); err != nil {
			t.Fatal(err)
		}
	}
	put("", "internal/store/x.go", "Beschreibung der Projektebene")
	put("feat/a", "internal/store/x.go", "Beschreibung auf feat/a")
	put("feat/a", "internal/store/neu.go", "Nur auf feat/a")
	put("feat/a-x", "internal/store/anderes.go", "Nur auf dem Abzweig")
	put("feat/b", "internal/store/b.go", "Nur auf dem Geschwister")
	res, err := e.St.RecordPullRequest(store.PullRequestChange{InstallationID: 77, Remote: shellProject, HeadRemote: shellProject, Number: 31,
		Action: "opened", Title: "Abzweig", Head: "feat/a-x", Base: "feat/a", DefaultBranch: "main", State: "open"})
	if err != nil || !res.Applied {
		t.Fatalf("pull: %+v %v", res, err)
	}
}

func TestCodeTreeFollowsTheBranchAndItsAncestors(t *testing.T) {
	e := ovEnv(t)
	branchGhosts(t, e)
	page := func(c *http.Client, path string) (int, string) {
		t.Helper()
		return fetchPage(t, c, e.Base+"/ui/x/shell"+path)
	}

	_, onMain := page(e.Member, "/tree/main/internal/store")
	for _, want := range []string{"Beschreibung der Projektebene", `/blob/main/internal/store/x.go`} {
		if !strings.Contains(onMain, want) {
			t.Errorf("main lacks %q", want)
		}
	}
	for _, not := range []string{"Beschreibung auf feat/a", "neu.go", "b.go", "Nur auf"} {
		if strings.Contains(onMain, not) {
			t.Errorf("main shows what is only on a branch: %q", not)
		}
	}

	_, onA := page(e.Member, "/tree/feat/a/internal/store")
	for _, want := range []string{"Beschreibung auf feat/a", "neu.go", "Nur auf feat/a"} {
		if !strings.Contains(onA, want) {
			t.Errorf("feat/a lacks %q", want)
		}
	}
	for _, not := range []string{"Beschreibung der Projektebene", "anderes.go", "b.go"} {
		if strings.Contains(onA, not) {
			t.Errorf("feat/a shows %q", not)
		}
	}

	// The branch cut from feat/a (known by its pull request) inherits and adds its own.
	_, onX := page(e.Member, "/tree/feat/a-x/internal/store")
	for _, want := range []string{"Beschreibung auf feat/a", "neu.go", "anderes.go"} {
		if !strings.Contains(onX, want) {
			t.Errorf("feat/a-x lacks %q", want)
		}
	}
	if strings.Contains(onX, "b.go") {
		t.Error("feat/a-x shows a sibling branch")
	}

	// A sibling without a pull request sees the project level only.
	_, onB := page(e.Member, "/tree/feat/b/internal/store")
	if !strings.Contains(onB, "Beschreibung der Projektebene") || strings.Contains(onB, "Beschreibung auf feat/a") || strings.Contains(onB, "neu.go") {
		t.Error("a sibling must see the project level and nothing of feat/a")
	}

	// The file page follows the branch as well, and shows the text it replaced.
	_, fileA := page(e.Member, "/blob/feat/a/internal/store/x.go")
	if !strings.Contains(fileA, "Beschreibung auf feat/a") || !strings.Contains(fileA, "Beschreibung der Projektebene") {
		t.Errorf("file page on feat/a:\n%s", fileA)
	}
	_, fileMain := page(e.Member, "/blob/main/internal/store/x.go")
	if !strings.Contains(fileMain, "Beschreibung der Projektebene") || strings.Contains(fileMain, "Beschreibung auf feat/a") {
		t.Error("file page on main shows the branch text")
	}
	if status, _ := page(e.Member, "/blob/main/internal/store/neu.go"); status != http.StatusNotFound {
		t.Errorf("a path that exists only on a branch answers %d on main", status)
	}
	if status, _ := page(e.Member, "/blob/feat/a/internal/store/neu.go"); status != http.StatusOK {
		t.Errorf("the path on its own branch answers %d", status)
	}
}

func TestCodeTreeGuestSeesOnlyTheDefaultBranchState(t *testing.T) {
	e := ovEnv(t)
	branchGhosts(t, e)
	_, top := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/tree/main/internal/store")
	if !strings.Contains(top, "Beschreibung der Projektebene") {
		t.Error("the guest does not see the default state")
	}
	for _, not := range []string{"Beschreibung auf feat/a", "neu.go", "feat/a"} {
		if strings.Contains(top, not) {
			t.Errorf("the guest sees %q", not)
		}
	}
	for _, path := range []string{"/tree/feat/a/internal/store", "/blob/feat/a/internal/store/x.go"} {
		if status, _ := fetchPage(t, e.Guest, e.Base+"/ui/x/shell"+path); status != http.StatusNotFound {
			t.Errorf("guest at %s: %d", path, status)
		}
	}
}
