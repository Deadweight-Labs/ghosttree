package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/forge"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func (f *fakeAppProver) InstallURL(state string) string {
	return "https://forge.example/install?state=" + url.QueryEscape(state)
}

func callbackPick(t *testing.T, c *http.Client, base string) string {
	t.Helper()
	state := startFlow(t, c, base)
	back, err := c.Get(base + "/ui/orgs/source/callback?code=good&state=" + state)
	if err != nil {
		t.Fatal(err)
	}
	loc := back.Header.Get("Location")
	if !strings.Contains(loc, "pick=") {
		t.Fatalf("callback: %q", loc)
	}
	return pickToken(loc)
}

func TestAnAlreadyConnectedOrgIsAdoptedInsteadOfHidden(t *testing.T) {
	prover := &fakeAppProver{fakeProver: fakeProver{configured: true, accounts: []forge.Account{{Login: "deadweight-labs", Repos: []string{"github.com/deadweight-labs/a"}}}}}
	base, st, alice, _ := sharingWeb(t, prover)
	org, _ := st.OrgByRef("alpha")
	// Erst ohne Installation verbunden (Rückfall über die OAuth-App), Präfix privat.
	pick := callbackPick(t, alice, base)
	if r := postOrg(t, alice, base, "/ui/orgs/source/add", url.Values{"org": {"alpha"}, "pick": {pick}, "login": {"deadweight-labs"}, "visibility": {"private"}}); !strings.Contains(r.Header.Get("Location"), "source_added") {
		t.Fatalf("first connect: %q", r.Header.Get("Location"))
	}
	if srcs, _ := st.ListSources("person:1", org.ID); len(srcs) != 1 || srcs[0].InstallationID != 0 {
		t.Fatalf("setup: %+v", srcs)
	}
	// Jetzt weist die App die Installation nach.
	prover.accounts = []forge.Account{{Login: "deadweight-labs", InstallationID: 5, Repos: []string{"github.com/deadweight-labs/a", "github.com/deadweight-labs/b"}}}
	pick = callbackPick(t, alice, base)
	page := getOrgs(t, alice, base, "?org=alpha&pick="+pick)
	if !strings.Contains(page, `name="login" value="deadweight-labs"`) || !strings.Contains(page, `action="/ui/orgs/source/add"`) {
		t.Fatalf("the connected org must stay on offer: %s", page)
	}
	if r := postOrg(t, alice, base, "/ui/orgs/source/add", url.Values{"org": {"alpha"}, "pick": {pick}, "login": {"deadweight-labs"}, "visibility": {"shared"}}); !strings.Contains(r.Header.Get("Location"), "source_added") {
		t.Fatalf("adopt: %q", r.Header.Get("Location"))
	}
	srcs, _ := st.ListSources("person:1", org.ID)
	if len(srcs) != 1 || srcs[0].InstallationID != 5 || srcs[0].Repos != 2 {
		t.Fatalf("the proven installation belongs to the existing source: %+v", srcs)
	}
	prefixes, _ := st.ListOrgPrefixes("person:1", org.ID)
	if len(prefixes) != 1 || prefixes[0].Visibility != store.VisibilityPrivate {
		t.Fatalf("adopting must not change prefix or visibility: %+v", prefixes)
	}
}

func TestPickMarksAdoptableOrgsAndOffersTheInstallLink(t *testing.T) {
	prover := &fakeAppProver{fakeProver: fakeProver{configured: true}}
	_, st, _, _ := sharingWeb(t, prover)
	org, _ := st.OrgByRef("alpha")
	if _, err := st.ConnectSourceWithInstallation("person:1", org.ID, store.SourceGitHub, "deadweight-labs", 0, nil); err != nil {
		t.Fatal(err)
	}
	a := &app{store: st, prover: prover, sourceFlows: newSourceFlows()}
	pickFor := func(accounts []forge.Account) *sourcePick {
		tok, _ := a.sourceFlows.start("person:1", "alpha")
		a.sourceFlows.put(tok, sourceFlow{account: "person:1", org: "alpha", until: a.sourceFlows.now().Add(sourceFlowTTL), accounts: accounts})
		v := orgsView{Selected: org, Owner: true}
		a.fillSources(httptest.NewRequest("GET", "/ui/orgs?pick="+tok, nil), &v, "person:1")
		if v.Pick == nil {
			t.Fatal("no pick")
		}
		return v.Pick
	}
	// Keine Installation für eine Admin-Org: die Seite bietet die Installation an.
	p := pickFor(nil)
	if len(p.Accounts) != 0 || !strings.HasPrefix(p.InstallURL, "https://forge.example/install?state=") {
		t.Fatalf("empty proof: %+v", p)
	}
	// Eine verbundene Org mit Nachweis bleibt in der Liste und ist als Übernahme markiert, eine neue nicht.
	p = pickFor([]forge.Account{{Login: "deadweight-labs", InstallationID: 5}, {Login: "other", InstallationID: 6}})
	if len(p.Accounts) != 2 || !p.Adopt["deadweight-labs"] || p.Adopt["other"] {
		t.Fatalf("adopt marks: %+v", p)
	}
}

func TestReadingReposAgainWorksWhenEveryKnownRepoIsAlreadyAProject(t *testing.T) {
	prover := &fakeAppProver{fakeProver: fakeProver{configured: true, accounts: []forge.Account{{Login: "deadweight-labs", InstallationID: 5, Repos: []string{"github.com/deadweight-labs/a"}}}}}
	base, st, alice, _ := sharingWeb(t, prover)
	pick := callbackPick(t, alice, base)
	if r := postOrg(t, alice, base, "/ui/orgs/source/add", url.Values{"org": {"alpha"}, "pick": {pick}, "login": {"deadweight-labs"}, "visibility": {"shared"}}); !strings.Contains(r.Header.Get("Location"), "source_added") {
		t.Fatal(r.Header.Get("Location"))
	}
	post := func() {
		t.Helper()
		if r := postOrg(t, alice, base, "/ui/orgs/source/import", url.Values{"org": {"alpha"}, "login": {"deadweight-labs"}}); r.StatusCode != http.StatusSeeOther || !strings.Contains(r.Header.Get("Location"), "imported") {
			t.Fatalf("import: %d %q", r.StatusCode, r.Header.Get("Location"))
		}
	}
	post() // legt a an
	if _, known := st.ProjectByRemote("github.com/deadweight-labs/a"); !known {
		t.Fatal("a is a project now")
	}
	prover.refresh = []string{"github.com/deadweight-labs/a", "github.com/deadweight-labs/late"}
	post() // alle bekannten Repos sind Projekte, trotzdem wird neu gelesen
	if _, known := st.ProjectByRemote("github.com/deadweight-labs/late"); !known {
		t.Fatal("reading again must find the late repo")
	}
}
