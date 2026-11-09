package web

import (
	"net/http"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// Der Name eines Pakets ist oft sein Branch-Name; ein Gast sieht statt dessen
// „Package #n“, in der Liste, auf dem Board und an der Seite des Pull Requests.
func TestPackageNameIsNoBranchLeakForGuestsInTheWebViews(t *testing.T) {
	e, pkg, _ := packageFixture(t)
	req := rqMake(t, e.St, rqSpec{Title: "Second issue", Criteria: []string{"c"}})
	same, err := e.St.CreatePackage(requestdomain.PackageInput{Project: shellProject, Branch: "feat/same-as-name", Person: "alice",
		Members: []requestdomain.PackageMember{{RequestID: req.Request.ID, Role: requestdomain.RolePflicht}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/ui/x/shell/packages", "/ui/x/shell/packages/" + itoa(pkg.ID), "/ui/x/shell/packages/" + itoa(same.ID), "/ui/x/shell/pulls/12"} {
		status, page := fetchPage(t, e.Guest, e.Base+p)
		if status != http.StatusOK {
			t.Fatalf("guest at %s: %d", p, status)
		}
		for _, leak := range []string{"Git-native", "feat/x", "same-as-name"} {
			if strings.Contains(page, leak) {
				t.Errorf("guest at %s sees %q", p, leak)
			}
		}
	}
	_, list := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/packages")
	mustContain(t, "guest list", list, "Package #"+itoa(pkg.ID), "Package #"+itoa(same.ID))
	// Ein Mitglied sieht die Namen weiter.
	_, mine := fetchPage(t, e.Member, e.Base+"/ui/x/shell/packages")
	mustContain(t, "member list", mine, "Git-native", "feat/same-as-name")
}

func TestHiddenPackageNameIsWordedInTheLanguageOfTheViewer(t *testing.T) {
	p := requestdomain.Package{ID: 5, Name: "Package #5", NameHidden: true}
	if got := packageName(langDE, p); got != "Paket #5" {
		t.Errorf("de: %q", got)
	}
	if got := packageName(langEN, p); got != "Package #5" {
		t.Errorf("en: %q", got)
	}
}

// Der Chip „Paket“ an der Seite eines Issues auf dem Standard-Branch: heißt das Paket
// wie der Branch, steht für den Gast „Package #n“ da.
func TestPackageChipOnTheIssueHidesTheBranchNameForGuests(t *testing.T) {
	e, _, _ := packageFixture(t)
	req := rqMake(t, e.St, rqSpec{Title: "On the default branch", Criteria: []string{"c"}})
	pk, err := e.St.CreatePackage(requestdomain.PackageInput{Project: shellProject, Branch: "main", Person: "alice",
		Members: []requestdomain.PackageMember{{RequestID: req.Request.ID, Role: requestdomain.RolePflicht}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.St.DB().Exec(`UPDATE requests SET branch='main' WHERE id=?`, req.Request.ID); err != nil {
		t.Fatal(err)
	}
	path := "/ui/x/shell/issues/" + itoa(req.Request.ID)
	chip := func(page string) string {
		i := strings.Index(page, `<h3>Work package</h3>`)
		if i < 0 {
			return ""
		}
		return page[i:min(i+200, len(page))]
	}
	if _, page := fetchPage(t, e.Member, e.Base+path); !strings.Contains(chip(page), "<span>main</span>") {
		t.Errorf("the member sees no package chip with the name: %q", chip(page))
	}
	if _, page := fetchPage(t, e.Guest, e.Base+path); !strings.Contains(chip(page), "<span>Package #"+itoa(pk.ID)+"</span>") {
		t.Errorf("the guest sees %q", chip(page))
	}
}
