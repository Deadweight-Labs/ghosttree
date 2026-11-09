package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/forge"
)

// pickToken is the pick= value of a redirect, without the section anchor.
func pickToken(loc string) string {
	tok := loc[strings.Index(loc, "pick=")+5:]
	if i := strings.IndexAny(tok, "&#"); i >= 0 {
		tok = tok[:i]
	}
	return tok
}

func TestGitHubCallbackReturnsToTheSourcesWithANotice(t *testing.T) {
	prover := &fakeProver{configured: true, accounts: []forge.Account{{Login: "deadweight-labs", Repos: []string{"github.com/deadweight-labs/a"}}}}
	base, _, alice, anna := sharingWeb(t, prover)

	// A callback GitHub cannot confirm lands on the sources, as a failure.
	state := startFlow(t, alice, base)
	r, _ := anna.Get(base + "/ui/orgs/source/callback?code=good&state=" + state)
	loc := r.Header.Get("Location")
	if !strings.HasSuffix(loc, "#sources") || !strings.Contains(loc, "notice=source_failed") {
		t.Fatalf("a failed callback must return to the sources: %q", loc)
	}
	page := getOrgs(t, alice, base, loc[len("/ui/orgs"):strings.Index(loc, "#")])
	if !strings.Contains(page, `class="flash err"`) || !strings.Contains(page, "GitHub could not confirm that") {
		t.Fatalf("the failure is shown as one: %s", page)
	}

	// A good callback returns to the sources with the choice and a notice.
	state = startFlow(t, alice, base)
	r, _ = alice.Get(base + "/ui/orgs/source/callback?code=good&state=" + state)
	loc = r.Header.Get("Location")
	if !strings.HasSuffix(loc, "#sources") || !strings.Contains(loc, "notice=source_proved") || !strings.Contains(loc, "org=alpha") || pickToken(loc) == "" {
		t.Fatalf("a good callback must return to the sources: %q", loc)
	}
	page = getOrgs(t, alice, base, loc[len("/ui/orgs"):strings.Index(loc, "#")])
	if !strings.Contains(page, "GitHub confirmed it") || strings.Contains(page, `class="flash err"`) || !strings.Contains(page, "github.com/deadweight-labs") {
		t.Fatalf("the success notice and the choice are shown: %s", page)
	}

	// Connecting, disconnecting and importing also return to the sources.
	pick := pickToken(loc)
	r = postOrg(t, alice, base, "/ui/orgs/source/add", url.Values{"org": {"alpha"}, "pick": {pick}, "login": {"deadweight-labs"}, "visibility": {"shared"}})
	if got := r.Header.Get("Location"); !strings.HasSuffix(got, "#sources") || !strings.Contains(got, "source_added") {
		t.Fatalf("add: %q", got)
	}
	r = postOrg(t, alice, base, "/ui/orgs/source/remove", url.Values{"org": {"alpha"}, "login": {"deadweight-labs"}})
	if got := r.Header.Get("Location"); !strings.HasSuffix(got, "#sources") || !strings.Contains(got, "source_removed") {
		t.Fatalf("remove: %q", got)
	}
	// Other notices keep their plain address.
	r = postOrg(t, alice, base, "/ui/orgs/default", url.Values{"org": {"alpha"}})
	if got := r.Header.Get("Location"); strings.Contains(got, "#") {
		t.Fatalf("a notice of another section must not jump: %q", got)
	}
}

func TestANewOrganizationIsNotCreatedFromTheSettings(t *testing.T) {
	base, _, alice, _, _ := orgWeb(t)
	page := getOrgs(t, alice, base, "?org=alpha")
	for _, gone := range []string{`href="#neworg"`, `action="/ui/orgs/create"`, `id="neworg"`} {
		if strings.Contains(page, gone) {
			t.Fatalf("the settings of an organization offer a new one (%s)", gone)
		}
	}
	// The ways in: the "+" menu and the organization menu of the header.
	if strings.Count(page, `data-new="org" href="/ui/orgs/new"`) != 2 {
		t.Fatalf("the header leads to the form twice (+ menu, organization menu): %s", page)
	}
	resp, err := alice.Get(base + "/ui/orgs/new")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("the form: %v %d", err, resp.StatusCode)
	}
	form := body(t, resp)
	if !strings.Contains(form, `action="/ui/orgs/create"`) || !strings.Contains(form, "New organization") {
		t.Fatalf("the form is missing: %s", form)
	}
	if strings.Contains(form, `href="#general"`) {
		t.Fatal("the form is not the settings")
	}

	// Creating works and lands on the new organization.
	r := postOrg(t, alice, base, "/ui/orgs/create", url.Values{"name": {"Beta"}})
	if loc := r.Header.Get("Location"); r.StatusCode != http.StatusSeeOther || !strings.Contains(loc, "org=beta") || !strings.Contains(loc, "notice=created") {
		t.Fatalf("create: %d %q", r.StatusCode, loc)
	}
	// A failure stays on the form and says why.
	r = postOrg(t, alice, base, "/ui/orgs/create", url.Values{"name": {"Beta"}})
	got := body(t, r)
	if r.StatusCode != http.StatusConflict || !strings.Contains(got, `action="/ui/orgs/create"`) || !strings.Contains(got, `class="flash err"`) {
		t.Fatalf("a taken name: %d %s", r.StatusCode, got)
	}
}

func TestSomeoneWithoutAnOrganizationCanStillCreateOne(t *testing.T) {
	srv, st, _ := testWeb(t)
	alone := loginInteractive(t, srv, st, "alice")
	page := getOrgs(t, alone, srv.URL, "")
	if !strings.Contains(page, `action="/ui/orgs/create"`) || !strings.Contains(page, `data-new="org" href="/ui/orgs/new"`) {
		t.Fatalf("without an organization the way in must remain: %s", page)
	}
}

func TestTheGardenerAccountIsMarkedAsAServiceInThePeopleList(t *testing.T) {
	base, st, alice, _, org := orgWeb(t)
	if _, err := st.AddPerson("gardener"); err != nil {
		t.Fatal(err)
	}
	code, _, _ := st.CreateInvitation("person:1", org.ID, "", "member", 0)
	if _, err := st.AcceptInvitation("person:3", code); err != nil {
		t.Fatal(err)
	}
	rows := func() []string {
		page := getOrgs(t, alice, base, "?org=alpha")
		return strings.Split(page, `<div class="row mrow">`)
	}
	service := func(row string) bool { return strings.Contains(row, `<span class="lb neu">Service</span>`) }
	for _, row := range rows() {
		if service(row) {
			t.Fatalf("nobody is a service before the gardener is set: %s", row)
		}
	}
	if err := st.SetGardenerAccount(3); err != nil {
		t.Fatal(err)
	}
	marked := 0
	for _, row := range rows() {
		if service(row) {
			marked++
			if !strings.Contains(row, "<b>gardener</b>") {
				t.Fatalf("the wrong row is marked: %s", row)
			}
		} else if strings.Contains(row, "<b>gardener</b>") {
			t.Fatalf("the gardener row is not marked: %s", row)
		}
	}
	if marked != 1 {
		t.Fatalf("exactly the gardener is marked, got %d", marked)
	}
}
