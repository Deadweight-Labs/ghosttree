package web

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func nStr(s string) *string { return &s }

// featureFixture legt ein Feature mit zwei Fassungen, einer Idee und einem Paket an.
func featureFixture(t *testing.T, e shellEnv, pa *store.ProjectAccess, project string) (feature.Feature, idea.Idea) {
	t.Helper()
	i, err := pa.CreateIdea(store.CreateIdeaInput{Project: project, Title: "Voice answers", Body: "Answer the inbox by voice.", Actor: idea.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	f, err := pa.CreateFeature(store.CreateFeatureInput{Project: project, Message: "first description", Actor: feature.Actor{AccountID: 1, Person: "alice"},
		Content: feature.Patch{Name: nStr("Inbox"), Summary: nStr("One place for everything that waits for you."),
			Behavior: nStr("The inbox lists questions, tasks and mentions.\n\nA tick marks one as done."), Limits: nStr("No voice yet."),
			CLI: nStr("`ctx inbox`"), UI: nStr("The bell in the header."), Areas: &[]string{"internal/web"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pa.LinkFeature(f.ID, feature.KindIdea, i.ID, feature.RelAffects, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pa.UpdateFeature(f.ID, store.UpdateFeatureInput{Patch: feature.Patch{Limits: nStr("Voice answers are planned.")}, Message: "limits changed", PR: 12, Actor: feature.Actor{AccountID: 1, Person: "alice"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.St.CreatePackage(requestdomain.PackageInput{Project: project, Branch: "feat/voice", Name: "Voice package", PR: 12, Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	f, err = pa.FeatureView(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	return f, i
}

func TestFeatureTabListsTheFeaturesWithTheirStateIdeasAndPullRequests(t *testing.T) {
	e, pa := listEnv(t)
	f, i := featureFixture(t, e, pa, shellProject)
	base := e.Base + "/ui/x/shell/features"
	code, page := fetchPage(t, e.Owner, base)
	if code != http.StatusOK {
		t.Fatalf("feature list = %d", code)
	}
	for _, want := range []string{`href="/ui/x/shell/features"`, "Inbox", "One place for everything that waits for you.", "Version 2", `href="/ui/x/shell/features/` + strconv.FormatInt(f.ID, 10) + `"`,
		`href="/ui/x/shell/ideas/` + strconv.FormatInt(i.ID, 10) + `"`, "#12", "Active", "Retired", `class="rband`} {
		if !strings.Contains(page, want) {
			t.Errorf("feature list lacks %q", want)
		}
	}
	if _, retired := fetchPage(t, e.Owner, base+"?state=retired"); strings.Contains(retired, "One place for everything") || !strings.Contains(retired, "No retired features.") {
		t.Error("the retired tab shows an active feature or no empty line")
	}
	if _, hit := fetchPage(t, e.Owner, base+"?q=bell"); !strings.Contains(hit, "Inbox") {
		t.Error("the search misses the feature")
	}
	if _, miss := fetchPage(t, e.Owner, base+"?q=nothing-like-this"); strings.Contains(miss, "One place for everything") || !strings.Contains(miss, "No feature matches this search.") {
		t.Error("a search without a hit still lists the feature")
	}
	// A guest reads features too.
	if code, guest := fetchPage(t, e.Guest, base); code != http.StatusOK || !strings.Contains(guest, "Inbox") {
		t.Errorf("guest feature list = %d", code)
	}
	// What the viewer has no role in is unknown.
	if code, _ := fetchPage(t, e.Member, e.Base+"/ui/x/hidden/features"); code != http.StatusNotFound {
		t.Errorf("features of a hidden project = %d", code)
	}
}

func TestFeaturePageShowsDescriptionVersionsIdeasAndPullRequests(t *testing.T) {
	e, pa := listEnv(t)
	f, _ := featureFixture(t, e, pa, shellProject)
	path := "/ui/x/shell/features/" + strconv.FormatInt(f.ID, 10)
	code, page := fetchPage(t, e.Owner, e.Base+path)
	if code != http.StatusOK {
		t.Fatalf("feature page = %d", code)
	}
	for _, want := range []string{"Inbox", "#" + strconv.FormatInt(f.ID, 10), "One place for everything that waits for you.", "The inbox lists questions, tasks and mentions.", "A tick marks one as done.",
		"Voice answers are planned.", "Limits", "Usage", "CLI", "ctx inbox", "Interface", "The bell in the header.", "Areas", "internal/web",
		"wrote version 2", "limits changed", "described the feature", "first description", "Voice answers", "Pull requests", "#12", "Voice package", "Idea", "Ideas", `class="tlx"`} {
		if !strings.Contains(page, want) {
			t.Errorf("feature page lacks %q", want)
		}
	}
	for _, leak := range []string{"SECRET", "<script>"} {
		if strings.Contains(page, leak) {
			t.Errorf("feature page contains %q", leak)
		}
	}
	// An address with another repository does not find it, nor does a number that is none.
	for _, p := range []string{"/ui/x/hidden/features/" + strconv.FormatInt(f.ID, 10), "/ui/x/shell/features/9999", "/ui/x/shell/features/abc"} {
		if code, _ := fetchPage(t, e.Owner, e.Base+p); code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", p, code)
		}
	}
	// The same page reads for a guest, without anything to change.
	if code, guest := fetchPage(t, e.Guest, e.Base+path); code != http.StatusOK || strings.Contains(guest, "<form") && strings.Contains(guest, "/features/") && strings.Contains(guest, "method=\"post\"") {
		t.Errorf("guest feature page = %d", code)
	}
}

func TestFeatureRetiredShowsItsReason(t *testing.T) {
	e, pa := listEnv(t)
	f, _ := featureFixture(t, e, pa, shellProject)
	if _, err := pa.SetFeatureState(f.ID, feature.Retired, "Replaced by the new inbox"); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/features/"+strconv.FormatInt(f.ID, 10))
	for _, want := range []string{"Retired because", "Replaced by the new inbox", "retired the feature"} {
		if !strings.Contains(page, want) {
			t.Errorf("retired feature page lacks %q", want)
		}
	}
	_, list := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/features?state=retired")
	if !strings.Contains(list, "Inbox") || !strings.Contains(list, "Retired") {
		t.Error("the retired tab misses the feature")
	}
}

func TestFeatureShortFormLeadsToTheAddressInTheRepositoryAndHidesForeignOnes(t *testing.T) {
	e, pa := listEnv(t)
	f, _ := featureFixture(t, e, pa, shellProject)
	hidden, _ := featureFixture(t, e, pa, shellHiddenProject)
	resp, err := e.Owner.Get(e.Base + "/ui/f/" + strconv.FormatInt(f.ID, 10))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/x/shell/features/"+strconv.FormatInt(f.ID, 10) {
		t.Errorf("short form -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest, "lead": e.Lead} {
		if code, _ := fetchPage(t, c, e.Base+"/ui/f/"+strconv.FormatInt(hidden.ID, 10)); code != http.StatusNotFound {
			t.Errorf("%s reads the short form of a hidden feature: %d", name, code)
		}
	}
	for _, bad := range []string{"/ui/f/0", "/ui/f/abc", "/ui/f/99999"} {
		if code, _ := fetchPage(t, e.Owner, e.Base+bad); code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", bad, code)
		}
	}
}

func TestFeatureReferencePillHasItsOwnIcon(t *testing.T) {
	pill := string(refPillHTML(langEN, "feature", 12, "/ui/f/12"))
	for _, want := range []string{`class="ref pill k-feat"`, `data-ref="FEAT-12"`, `aria-label="Feature 12"`, "<svg"} {
		if !strings.Contains(pill, want) {
			t.Errorf("feature pill lacks %q: %s", want, pill)
		}
	}
	if !strings.Contains(string(refHTML(langEN, "feature", 12, "")), "#12") {
		t.Error("the short form of a feature reference lacks the number")
	}
}

func TestIdeaWorkshopShowsTodayNextToWantedForAffectedFeatures(t *testing.T) {
	e, pa := listEnv(t)
	f, i := featureFixture(t, e, pa, shellProject)
	actor := idea.Actor{AccountID: 1, Person: "alice"}
	if _, _, err := pa.PutIdeaConcept(i.ID, store.PutIdeaConceptInput{Patch: idea.ConceptPatch{What: nStr("The inbox answers by voice.")}, Actor: actor}); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/ideas/"+strconv.FormatInt(i.ID, 10))
	for _, want := range []string{"Changes these features", "Today", "Wanted", "One place for everything that waits for you.", "The inbox answers by voice.", `href="/ui/x/shell/features/` + strconv.FormatInt(f.ID, 10) + `"`} {
		if !strings.Contains(page, want) {
			t.Errorf("workshop lacks %q", want)
		}
	}
}
