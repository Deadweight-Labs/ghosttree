package web

import (
	"net/http"
	"strings"
	"testing"
)

// The list of all branches: a member sees every branch the server knows, each as
// a pill that opens its row, the default one marked, with the knowledge filed on
// it; a guest gets the default branch alone, as in the menu; the Code tab offers
// the list as the last entry of its branch menu.
func TestBranchesPageListsBranchesAsRowsAndHidesThemFromAGuest(t *testing.T) {
	e := ovEnv(t)
	codeSeed(t, e.St, shellProject)

	status, page := fetchPage(t, e.Member, e.Base+"/ui/x/shell/branches")
	if status != http.StatusOK {
		t.Fatalf("branches = %d", status)
	}
	for _, want := range []string{"All branches", "feat/git-native", ">main<", "Default", `class="ref pill k-branch mono rl"`, `href="/ui/x/shell/tree/feat/git-native"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the list lacks %q", want)
		}
	}
	// The knowledge of the branch is counted: one entry filed on feat/git-native.
	if !strings.Contains(page, `aria-label="1 knowledge entry on this branch"`) {
		t.Errorf("the branch with one entry does not say so")
	}
	// The Code tab leads to the list from its branch menu, below a rule.
	_, code := fetchPage(t, e.Member, e.Base+"/ui/x/shell")
	if !strings.Contains(code, `<hr><a class="ct-opt" href="/ui/x/shell/branches">`) {
		t.Error("the branch menu has no entry for all branches")
	}
	// The Code tab stays the open tab on that page.
	if !strings.Contains(page, `aria-current="page"`) {
		t.Error("no tab is open on the branch list")
	}

	_, guest := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/branches")
	if strings.Contains(guest, "feat/git-native") {
		t.Error("a guest sees a branch name that only knowledge carries")
	}
	if !strings.Contains(guest, ">main<") {
		t.Error("a guest does not see the default branch")
	}
	// No right in the project: as unknown as no project.
	if status, _ := fetchPage(t, e.Guest, e.Base+"/ui/x/hidden/branches"); status != http.StatusNotFound {
		t.Errorf("a project without a role has a branch list: %d", status)
	}
}
