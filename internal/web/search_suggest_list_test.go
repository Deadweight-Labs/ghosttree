package web

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-599 AC-10: the filter fields of the issue list and the pull list complete
// the qualifiers with the same component, the same rights and the same guest rule
// as the field of the header, but only with what their list reads.

func listSuggestEnv(t *testing.T) (shellEnv, string) {
	t.Helper()
	e, two := pullsWithSecondPackage(t)
	e.St.SetAccessMode(store.AccessMode{Enforce: true})
	return e, two
}

func listSuggest(t *testing.T, e shellEnv, c *http.Client, list, q string, extra ...string) string {
	t.Helper()
	u := e.Base + "/ui/search/suggest?q=" + url.QueryEscape(q) + "&list=" + list + "&project=" + url.QueryEscape(shellProject)
	for _, x := range extra {
		u += "&" + x
	}
	code, body := fetchPage(t, c, u)
	if code != http.StatusOK {
		t.Fatalf("suggest %s %q = %d", list, q, code)
	}
	return body
}

func TestListFieldsAskForSuggestionsWithTheirRepositoryAndList(t *testing.T) {
	e, _ := listSuggestEnv(t)
	for list, path := range map[string]string{"issues": "/ui/x/shell/issues", "pulls": "/ui/x/shell/pulls"} {
		_, page := fetchPage(t, e.Owner, e.Base+path)
		mustContain(t, list, page, `data-suggest="/ui/search/suggest"`, `data-suggest-project="`+shellProject+`"`, `data-suggest-list="`+list+`"`)
		// The field of the list sits in the filter form, beside the one of the header.
		if n := strings.Count(page, `data-suggest-list=`); n != 1 {
			t.Errorf("%s: %d fields carry a list", list, n)
		}
	}
}

func TestListSuggestionsOfferTheKeysOfTheListAndOnlyThose(t *testing.T) {
	e, _ := listSuggestEnv(t)
	issues := suggestInserts(listSuggest(t, e, e.Owner, "issues", "is:open "))
	if want := []string{"is:", "type:", "priority:", "label:", "author:", "branch:", "package:"}; !slices.Equal(issues, want) {
		t.Errorf("issues offer %v, want %v", issues, want)
	}
	pulls := suggestInserts(listSuggest(t, e, e.Owner, "pulls", "is:open "))
	if want := []string{"is:", "author:", "branch:", "package:"}; !slices.Equal(pulls, want) {
		t.Errorf("pulls offer %v, want %v", pulls, want)
	}
	// What only the global search knows is not offered, also not by value.
	for _, q := range []string{"kind:", "repo:", "area:", "for:", "created:"} {
		for _, list := range []string{"issues", "pulls"} {
			if got := suggestInserts(listSuggest(t, e, e.Owner, list, q)); len(got) != 0 {
				t.Errorf("%s %q offers %v", list, q, got)
			}
		}
	}
	// The word being typed is completed, as in the header.
	if got := suggestInserts(listSuggest(t, e, e.Owner, "issues", "is:open ty")); !slices.Equal(got, []string{"type:"}) {
		t.Errorf("ty: %v", got)
	}
	if got := suggestInserts(listSuggest(t, e, e.Owner, "issues", "pri")); !slices.Equal(got, []string{"priority:"}) {
		t.Errorf("pri: %v", got)
	}
	// priority: is no word of the global search.
	if got := suggestInserts(suggestFor(t, exactEnv{shellEnv: e}, e.Owner, "pri")); len(got) != 0 {
		t.Errorf("the header offers %v", got)
	}
}

func TestListSuggestionsOfferTheValuesOfTheList(t *testing.T) {
	e, _ := listSuggestEnv(t)
	has := func(list, q string, wants ...string) {
		t.Helper()
		got := suggestInserts(listSuggest(t, e, e.Owner, list, q))
		for _, w := range wants {
			if !slices.Contains(got, w) {
				t.Errorf("%s %q offers %v, want %s", list, q, got, w)
			}
		}
	}
	has("issues", "is:", "is:open", "is:closed", "is:issue", "is:idea", "is:pain")
	has("pulls", "is:", "is:open", "is:merged", "is:closed")
	if got := suggestInserts(listSuggest(t, e, e.Owner, "pulls", "is:")); slices.Contains(got, "is:issue") || slices.Contains(got, "is:pain") {
		t.Errorf("the pull list offers segments of the issue list: %v", got)
	}
	has("issues", "type:", "type:feature", "type:bug")
	has("issues", "branch:", "branch:main")
	// The pull list offers the heads of its pull requests, not every branch: main heads none.
	has("pulls", "branch:", "branch:feat/x", "branch:feat/y")
	if got := suggestInserts(listSuggest(t, e, e.Owner, "pulls", "branch:")); slices.Contains(got, "branch:main") {
		t.Errorf("the pull list offers a branch that no pull request has as its head: %v", got)
	}
	// The text after the colon narrows, a finished word offers nothing more.
	if got := suggestInserts(listSuggest(t, e, e.Owner, "pulls", "is:m")); !slices.Equal(got, []string{"is:merged"}) {
		t.Errorf("is:m offers %v", got)
	}
	if got := suggestInserts(listSuggest(t, e, e.Owner, "pulls", "is:merged")); len(got) != 0 {
		t.Errorf("is:merged offers %v", got)
	}
	// Author names are no value the list can complete: it compares names, not "@me".
	if got := suggestInserts(listSuggest(t, e, e.Owner, "issues", "author:")); len(got) != 0 {
		t.Errorf("author: offers %v", got)
	}
}

// The values of the lists with their rights: labels from the project, priorities
// from what its issues carry.
func TestListSuggestionsOfferLabelsAndPriorities(t *testing.T) {
	e, _ := listSuggestEnv(t)
	pa := e.St.Access(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind})
	if _, err := pa.CreateLabel(store.LabelInput{Project: shellProject, Name: "ui", Color: "0969da", By: "alice"}); err != nil {
		t.Fatal(err)
	}
	rqMake(t, e.St, rqSpec{Title: "Urgent one", Priority: "hoch", Criteria: []string{"a"}})
	got := suggestInserts(listSuggest(t, e, e.Owner, "issues", "label:"))
	if !slices.Contains(got, "label:ui") {
		t.Errorf("label: offers %v", got)
	}
	got = suggestInserts(listSuggest(t, e, e.Owner, "issues", "priority:"))
	if !slices.Contains(got, "priority:hoch") {
		t.Errorf("priority: offers %v", got)
	}
	// The pull list has no labels and no priorities.
	for _, q := range []string{"label:", "priority:"} {
		if got := suggestInserts(listSuggest(t, e, e.Owner, "pulls", q)); len(got) != 0 {
			t.Errorf("the pull list offers %q: %v", q, got)
		}
	}
}

// The packages come from the lean list (PackageChoices), and each list offers the
// ones it can show: the issue list the open ones, the pull list those with a pull request.
func TestListSuggestionsOfferThePackagesTheListCanShow(t *testing.T) {
	e, two := listSuggestEnv(t)
	issues := listSuggest(t, e, e.Owner, "issues", "package:")
	got := suggestInserts(issues)
	if !slices.Contains(got, "package:1") || !slices.Contains(got, "package:"+two) {
		t.Errorf("the issue list offers %v, want package 1 and %s", got, two)
	}
	mustContain(t, "issues", issues, "Git-native", "No pull request")
	pulls := listSuggest(t, e, e.Owner, "pulls", "package:")
	got = suggestInserts(pulls)
	if !slices.Contains(got, "package:1") || slices.Contains(got, "package:"+two) {
		t.Errorf("the pull list offers %v, want only package 1", got)
	}
	// The package of the hidden project is in neither list.
	mustNotContain(t, "pulls", pulls, "SECRET")
}

// A number is no jump in a list field: the field filters, it goes nowhere else.
func TestListSuggestionsHaveNoJumpAndNoSearchForTheWholeText(t *testing.T) {
	e, _ := listSuggestEnv(t)
	for _, list := range []string{"issues", "pulls"} {
		for _, q := range []string{"#12", "pr:12", "REQ-1", "deploy the thing"} {
			body := listSuggest(t, e, e.Owner, list, q)
			if strings.Contains(body, "sg-all") || strings.Contains(body, `class="sg-a"`) {
				t.Errorf("%s %q gives a row that leads away:\n%s", list, q, body)
			}
		}
	}
}

// The guest rule is the one of the header: a guest of the repository is offered
// neither branch: nor package:, in either list, and no value of them.
func TestListSuggestionsFollowTheGuestRuleAndTheRightsOfTheRepository(t *testing.T) {
	e, _ := listSuggestEnv(t)
	for _, list := range []string{"issues", "pulls"} {
		for _, q := range []string{"is:open ", "br", "pack", "branch:", "package:"} {
			got := suggestInserts(listSuggest(t, e, e.Guest, list, q))
			for _, g := range got {
				if strings.HasPrefix(g, "branch:") || strings.HasPrefix(g, "package:") {
					t.Errorf("%s %q: a guest is offered %s", list, q, g)
				}
			}
		}
		body := listSuggest(t, e, e.Guest, list, "is:open ")
		mustContain(t, "guest "+list, body, `data-insert="is:"`)
		mustNotContain(t, "guest "+list, body, "Git-native", "No pull request", "feat/x", "feat/z")
		// The member of the repository sees them.
		if got := suggestInserts(listSuggest(t, e, e.Member, list, "is:open ")); !slices.Contains(got, "branch:") || !slices.Contains(got, "package:") {
			t.Errorf("%s: a member is offered %v", list, got)
		}
	}
	// A member of one repository who is a guest in the other is a guest there.
	if err := e.St.SetProjectRole("person:1", shellHiddenProject, "person:2", store.RoleGuest, false, store.RoleViaCLI); err != nil {
		t.Fatal(err)
	}
	other := "list=issues&project=" + url.QueryEscape(shellHiddenProject)
	code, body := fetchPage(t, e.Member, e.Base+"/ui/search/suggest?q="+url.QueryEscape("pack")+"&"+other)
	if code != http.StatusOK || strings.Contains(body, "data-insert") {
		t.Errorf("a guest of the page's repository is offered %q (%d)", body, code)
	}
}

// What the viewer may not read answers as what does not exist, in the list field
// like in the header: nothing, byte for byte. A list that is none, and a list
// without a repository, offer nothing; a stranger and an anonymous reader get no rows.
func TestListSuggestionsGiveAwayNothingOfWhatTheViewerMayNotReadAndAnswerLikeMissing(t *testing.T) {
	e, _ := listSuggestEnv(t)
	ask := func(c *http.Client, project, list string) (int, string) {
		u := e.Base + "/ui/search/suggest?q=" + url.QueryEscape("branch:") + "&list=" + list
		if project != "" {
			u += "&project=" + url.QueryEscape(project)
		}
		return fetchPage(t, c, u)
	}
	for _, list := range []string{"issues", "pulls"} {
		for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest, "reviewer": e.Reviewer} {
			hidden, h := ask(c, shellHiddenProject, list)
			missing, m := ask(c, "github.com/x/nothing", list)
			if hidden != missing || h != m {
				t.Errorf("%s %s: hidden (%d %q) and missing (%d %q) are answered differently", name, list, hidden, h, missing, m)
			}
			if strings.Contains(h, "data-insert") || strings.Contains(h, "secret") {
				t.Errorf("%s %s: a hidden repository is offered values: %q", name, list, h)
			}
		}
		if _, body := ask(e.Owner, "", list); strings.Contains(body, "data-insert") {
			t.Errorf("%s without a repository offers %q", list, body)
		}
	}
	if _, body := fetchPage(t, e.Owner, e.Base+"/ui/search/suggest?q=is%3A&list=nothing&project="+url.QueryEscape(shellProject)); strings.Contains(body, "data-insert") {
		t.Errorf("a list that is none offers %q", body)
	}
	// Somebody who is not signed in is sent to the login, with no row.
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := anon.Get(e.Base + "/ui/search/suggest?q=is%3A&list=issues&project=" + url.QueryEscape(shellProject))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Errorf("an anonymous reader is answered with %d", res.StatusCode)
	}
}
