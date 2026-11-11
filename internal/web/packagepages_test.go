package web

import (
	"net/http"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// packageFixture setzt auf pullsEnv auf: das Paket „Git-native“ am PR 12 (Branch
// feat/x) trägt schon einen Pflicht-Request; dazu kommen ein optionaler Request,
// eine Session an der Arbeit und ein Paket im Projekt „hidden“.
func packageFixture(t *testing.T) (shellEnv, requestdomain.PackageDetail, requestdomain.PackageDetail) {
	t.Helper()
	e, _ := pullsEnv(t)
	pkgs, err := e.St.Access(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}).PackagesOfProject(shellProject, "")
	if err != nil || len(pkgs) != 1 {
		t.Fatalf("packages: %+v %v", pkgs, err)
	}
	pkg, err := e.St.PackageByID(pkgs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	opt := rqMake(t, e.St, rqSpec{Title: "Nice to have", Criteria: []string{"Looks fine"}})
	if _, err := e.St.AddPackageRequests(pkg.ID, []requestdomain.PackageMember{{RequestID: opt.Request.ID, Role: requestdomain.RoleOptional}}, "", "alice"); err != nil {
		t.Fatal(err)
	}
	sid, err := e.St.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "pkg-sess", AccountID: store.AccountIDOf("person:1"), Scope: scope.Axes{Project: shellProject, Machine: "build-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.St.StartRequestWork(pkg.Members[0].RequestID, sid, "primary", "alice"); err != nil {
		t.Fatal(err)
	}
	hiddenReq := rqMake(t, e.St, rqSpec{Title: "SECRET-REQUEST", Project: shellHiddenProject})
	// PR 99 of the hidden repository is open, so its branch has a package already (package_auto.go).
	auto, err := e.St.PackageForPR(shellHiddenProject, 99)
	if err != nil {
		t.Fatal(err)
	}
	name := "SECRET-PACKAGE"
	if _, err := e.St.UpdatePackage(auto.ID, requestdomain.PackagePatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	other, err := e.St.AddPackageRequests(auto.ID, []requestdomain.PackageMember{{RequestID: hiddenReq.Request.ID, Role: requestdomain.RolePflicht}}, "", "alice")
	if err != nil {
		t.Fatal(err)
	}
	pkg, _ = e.St.PackageByID(pkg.ID)
	return e, pkg, other
}

func TestPackageListAndBoardShowRequestsRolesCriteriaPullAndSessions(t *testing.T) {
	e, pkg, _ := packageFixture(t)
	path := "/ui/x/shell/packages/" + itoa(pkg.ID)
	_, list := fetchPage(t, e.Member, e.Base+"/ui/x/shell/packages")
	mustContain(t, "package list", list, "Git-native", `href="`+path+`"`, `href="/ui/x/shell/pulls/12"`, "2 issues", "feat/x", `>1</span> Open`)
	if strings.Contains(list, "SECRET") {
		t.Error("the list names a package of another repository")
	}
	status, board := fetchPage(t, e.Owner, e.Base+path)
	if status != http.StatusOK {
		t.Fatalf("board: %d", status)
	}
	mustContain(t, "package board", board, "Git-native", "Pull request tab", "Nice to have", "Required", "Optional",
		"Lists the pull requests", "Shows the checklist", pullShaA[:7], `href="/ui/x/shell/pulls/12"`, "feat/x", "Sessions", "claude@build-1", `href="/ui/x/shell/issues/`)
	if strings.Contains(board, "SECRET") {
		t.Error("the board names something of another repository")
	}
	// A pull request page carries the same package.
	if _, pull := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/pulls/12"); !strings.Contains(pull, "Git-native") {
		t.Error("the pull request page lost its package")
	}
}

func TestPackageBranchFilterAndGuestRule(t *testing.T) {
	e, pkg, _ := packageFixture(t)
	_, byBranch := fetchPage(t, e.Member, e.Base+"/ui/x/shell/packages?branch=feat%2Fx")
	mustContain(t, "package list by branch", byBranch, "Git-native", "Show all packages")
	_, none := fetchPage(t, e.Member, e.Base+"/ui/x/shell/packages?branch=feat%2Fnone")
	mustContain(t, "package list by an unknown branch", none, "No work package holds for this branch.")
	// A guest gets no branch name, neither in the list nor on the board nor through the filter.
	path := "/ui/x/shell/packages/" + itoa(pkg.ID)
	for _, p := range []string{"/ui/x/shell/packages", path, "/ui/x/shell/packages?branch=feat%2Fx"} {
		status, page := fetchPage(t, e.Guest, e.Base+p)
		if status != http.StatusOK {
			t.Fatalf("guest at %s: %d", p, status)
		}
		if strings.Contains(page, "feat/x") {
			t.Errorf("a guest sees the branch name at %s", p)
		}
	}
}

func TestPackageRightsAndForeignNumbers(t *testing.T) {
	e, pkg, other := packageFixture(t)
	own := "/ui/x/shell/packages/" + itoa(pkg.ID)
	hiddenPath := "/ui/x/hidden/packages/" + itoa(other.ID)
	// A member without a role in the other repository gets 404 everywhere the number can be put.
	var want string
	for _, p := range []string{hiddenPath, "/ui/x/shell/packages/" + itoa(other.ID), "/ui/x/hidden/packages", "/ui/x/shell/packages/999999"} {
		status, body := fetchPage(t, e.Member, e.Base+p)
		if status != http.StatusNotFound {
			t.Errorf("member at %s: %d, want 404", p, status)
		}
		if strings.Contains(body, "SECRET") {
			t.Errorf("member at %s sees the foreign package", p)
		}
		if want == "" {
			want = body
		}
	}
	if _, unknown := fetchPage(t, e.Member, e.Base+"/ui/x/nothere/packages/"+itoa(other.ID)); stripCSRF(unknown) != stripCSRF(want) {
		t.Error("a foreign package answers differently from an unknown address")
	}
	// The owner has both: the number in the wrong repository leads to the right one.
	status, loc := locationOf(t, e.Owner, e.Base+"/ui/x/hidden/packages/"+itoa(pkg.ID))
	if status != http.StatusFound || loc != own {
		t.Errorf("wrong repository -> %d %q, want 302 %s", status, loc, own)
	}
	if status, _ := fetchPage(t, e.Owner, e.Base+loc); status != http.StatusOK {
		t.Errorf("the target answers %d", status)
	}
	if status, _ := fetchPage(t, e.Owner, e.Base+hiddenPath); status != http.StatusOK {
		t.Errorf("owner at the hidden package: %d", status)
	}
}
