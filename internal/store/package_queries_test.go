package store

import (
	"errors"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func (w *painWorld) requestOn(t *testing.T, project, branch, title string) int64 {
	t.Helper()
	d, err := w.st.CreateRequest(requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: title, Scope: scope.Axes{Project: project, Branch: branch}, Person: "mia"},
		Criteria: []string{"a", "b"}, AuthorID: 3})
	if err != nil {
		t.Fatal(err)
	}
	return d.Request.ID
}

// Der Zähler zählt, was die Liste liefert: ein Request ohne Branch gilt für
// jeden Branch, einer auf anderem Branch nicht.
func TestCountRequestsFollowsTheListSemantics(t *testing.T) {
	w := newPainWorld(t)
	w.requestOn(t, roleProject, "", "ohne Branch")
	w.requestOn(t, roleProject, "feat/x", "auf x")
	w.requestOn(t, roleProject, "feat/y", "auf y")
	done := w.requestOn(t, roleProject, "", "erledigt")
	if err := w.st.DropRequest(done, "doppelt", "mia"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ax    scope.Axes
		state string
		want  int
	}{
		{scope.Axes{Project: roleProject}, "open", 3},
		{scope.Axes{Project: roleProject, Branch: "feat/x"}, "open", 2},
		{scope.Axes{Project: roleProject, Branch: "feat/z"}, "open", 1},
		{scope.Axes{Project: roleProject}, "dropped", 1},
		{scope.Axes{Project: roleProject}, "", 4},
	} {
		got, err := w.pa["mia"].CountRequests(tc.ax, tc.state)
		if err != nil || got != tc.want {
			t.Errorf("count %+v %q = %d, %v; want %d", tc.ax, tc.state, got, err, tc.want)
		}
		page, err := w.st.SearchRequests(w.pa["mia"].RequestFilter(requestdomain.SearchFilter{Scope: tc.ax, State: tc.state, Limit: 25}))
		if err != nil || len(page.Results) != tc.want {
			t.Errorf("list %+v %q has %d results, %v; the count must equal it", tc.ax, tc.state, len(page.Results), err)
		}
	}
	// Wer das Projekt nicht sehen darf, zählt nichts.
	if n, err := w.pa["nora"].CountRequests(scope.Axes{Project: roleProject}, "open"); err != nil || n != 0 {
		t.Errorf("a stranger counts %d, %v", n, err)
	}
}

func TestPackagesOfRequestListOnlyWhatTheViewerMayRead(t *testing.T) {
	w := newPainWorld(t)
	id := w.requestOn(t, roleProject, "", "in zwei Paketen")
	other := w.requestOn(t, roleProject, "", "allein")
	p1, err := w.st.CreatePackage(requestdomain.PackageInput{Project: roleProject, Branch: "feat/a", Members: []requestdomain.PackageMember{{RequestID: id}, {RequestID: other, Role: "optional"}}})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := w.st.CreatePackage(requestdomain.PackageInput{Project: roleProject, Branch: "feat/b", Members: []requestdomain.PackageMember{{RequestID: id, Role: "optional"}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := w.pa["gus"].PackagesOfRequest(id)
	if err != nil || len(got) != 2 || got[0].ID != p1.ID || got[1].ID != p2.ID || got[0].Role != "pflicht" || got[1].Role != "optional" {
		t.Fatalf("packages of the request: %+v %v", got, err)
	}
	// Ein Request, den es nicht gibt, und einer, den man nicht lesen darf,
	// antworten gleich.
	_, errMissing := w.pa["nora"].PackagesOfRequest(id + 1000)
	_, errForeign := w.pa["nora"].PackagesOfRequest(id)
	var m, f *requestdomain.RuleError
	if !errors.As(errMissing, &m) || !errors.As(errForeign, &f) || m.ErrorCode != f.ErrorCode {
		t.Fatalf("missing %v, foreign %v", errMissing, errForeign)
	}
	if _, err := w.pa["nora"].PackagesOfProject(roleProject, ""); !errors.Is(err, ErrAccessNotFound) {
		t.Fatalf("a stranger lists packages: %v", err)
	}
}

func TestPackagesOfProjectCountPerPackage(t *testing.T) {
	w := newPainWorld(t)
	a := w.requestOn(t, roleProject, "", "a")
	b := w.requestOn(t, roleProject, "", "b")
	open, _ := w.st.CreatePackage(requestdomain.PackageInput{Project: roleProject, Branch: "feat/o", Members: []requestdomain.PackageMember{{RequestID: a}, {RequestID: b}}})
	closed, _ := w.st.CreatePackage(requestdomain.PackageInput{Project: roleProject, Branch: "feat/c", Members: []requestdomain.PackageMember{{RequestID: a}}})
	st := requestdomain.PackageClosed
	if _, err := w.st.UpdatePackage(closed.ID, requestdomain.PackagePatch{State: &st}); err != nil {
		t.Fatal(err)
	}
	all, err := w.pa["gus"].PackagesOfProject(roleProject, "")
	if err != nil || len(all) != 2 || all[0].ID != closed.ID {
		t.Fatalf("all packages: %+v %v", all, err)
	}
	only, _ := w.pa["gus"].PackagesOfProject(roleProject, "open")
	if len(only) != 1 || only[0].ID != open.ID || only[0].Requests != 2 || only[0].OpenCriteria != 4 {
		t.Fatalf("open packages: %+v", only)
	}
	if _, err := w.pa["gus"].PackagesOfProject(roleProject, "weird"); ruleCode(err) != "invalid_package_state" {
		t.Fatalf("state check: %v", err)
	}
}

// The lean list for menus and suggestions names the same packages, in the same
// order, under the same names and with the same rights as the full list.
func TestPackageChoicesMatchPackagesOfProject(t *testing.T) {
	w := newPainWorld(t)
	a := w.requestOn(t, roleProject, "", "a")
	open, _ := w.st.CreatePackage(requestdomain.PackageInput{Project: roleProject, Branch: "feat/o", Members: []requestdomain.PackageMember{{RequestID: a}}})
	closed, _ := w.st.CreatePackage(requestdomain.PackageInput{Project: roleProject, Branch: "feat/c", Members: []requestdomain.PackageMember{{RequestID: a}}})
	st := requestdomain.PackageClosed
	if _, err := w.st.UpdatePackage(closed.ID, requestdomain.PackagePatch{State: &st}); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"gus", "mia"} {
		for _, state := range []string{"", "open", "closed"} {
			full, err := w.pa[who].PackagesOfProject(roleProject, state)
			if err != nil {
				t.Fatal(err)
			}
			lean, err := w.pa[who].PackageChoices(roleProject, state)
			if err != nil || len(lean) != len(full) {
				t.Fatalf("%s %q: lean %+v, full %+v, %v", who, state, lean, full, err)
			}
			for i := range full {
				if lean[i] != full[i].Package {
					t.Errorf("%s %q #%d: lean %+v, full %+v", who, state, i, lean[i], full[i].Package)
				}
			}
		}
	}
	if lean, _ := w.pa["gus"].PackageChoices(roleProject, "open"); len(lean) != 1 || lean[0].ID != open.ID {
		t.Errorf("open: %+v", lean)
	}
	if _, err := w.pa["nora"].PackageChoices(roleProject, ""); !errors.Is(err, ErrAccessNotFound) {
		t.Errorf("a stranger lists packages: %v", err)
	}
	if _, err := w.pa["gus"].PackageChoices(roleProject, "weird"); ruleCode(err) != "invalid_package_state" {
		t.Errorf("state check: %v", err)
	}
}
