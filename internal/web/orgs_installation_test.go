package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/forge"
)

// fakeAppProver ist die GitHub-App: sie prüft die Installation aus der Adresse
// und liest die Repo-Liste einer Installation neu.
type fakeAppProver struct {
	fakeProver
	installs []int64
	refresh  []string
	reject   bool
}

func (f *fakeAppProver) ProveInstallation(_ context.Context, code, _ string, id int64) ([]forge.Account, error) {
	f.installs = append(f.installs, id)
	if code != "good" || f.reject {
		return nil, forge.ErrInstallationNotYours
	}
	return f.accounts, nil
}

func (f *fakeAppProver) InstallationRepos(_ context.Context, id int64) ([]string, error) {
	return f.refresh, nil
}

func startFlow(t *testing.T, c *http.Client, base string) string {
	t.Helper()
	resp := postOrg(t, c, base, "/ui/orgs/source/connect", url.Values{"org": {"alpha"}})
	u, _ := url.Parse(resp.Header.Get("Location"))
	return u.Query().Get("state")
}

func TestInstallationFromTheCallbackIsCheckedAndStoredOnTheSource(t *testing.T) {
	prover := &fakeAppProver{fakeProver: fakeProver{configured: true, accounts: []forge.Account{{
		Login: "deadweight-labs", InstallationID: 5, Repos: []string{"github.com/deadweight-labs/a"}}}}}
	base, st, alice, _ := sharingWeb(t, prover)
	org, _ := st.OrgByRef("alpha")

	// Eine unlesbare Installations-ID wird nicht an GitHub weitergereicht.
	state := startFlow(t, alice, base)
	if r, _ := alice.Get(base + "/ui/orgs/source/callback?code=good&installation_id=abc&state=" + state); !strings.Contains(r.Header.Get("Location"), "source_failed") || len(prover.installs) != 0 {
		t.Fatalf("garbage id: %q %v", r.Header.Get("Location"), prover.installs)
	}
	// Eine fremde Installation lehnt die App ab, nichts bleibt zurück.
	prover.reject = true
	state = startFlow(t, alice, base)
	r, _ := alice.Get(base + "/ui/orgs/source/callback?code=good&installation_id=999&state=" + state)
	if !strings.Contains(r.Header.Get("Location"), "source_failed") || strings.Contains(r.Header.Get("Location"), "pick=") {
		t.Fatalf("rejected installation: %q", r.Header.Get("Location"))
	}
	prover.reject = false

	state = startFlow(t, alice, base)
	back, _ := alice.Get(base + "/ui/orgs/source/callback?code=good&installation_id=5&setup_action=install&state=" + state)
	pickLoc := back.Header.Get("Location")
	if !strings.Contains(pickLoc, "pick=") || len(prover.installs) != 2 || prover.installs[1] != 5 {
		t.Fatalf("callback: %q %v", pickLoc, prover.installs)
	}
	pick := pickToken(pickLoc)
	if r := postOrg(t, alice, base, "/ui/orgs/source/add", url.Values{"org": {"alpha"}, "pick": {pick}, "login": {"deadweight-labs"}, "visibility": {"shared"}}); !strings.Contains(r.Header.Get("Location"), "source_added") {
		t.Fatalf("add: %q", r.Header.Get("Location"))
	}
	srcs, _ := st.ListSources("person:1", org.ID)
	if len(srcs) != 1 || srcs[0].InstallationID != 5 {
		t.Fatalf("the installation belongs to the source: %+v", srcs)
	}

	// "Projekte anlegen" liest die Liste über die Installation neu (auch ein Repo, dessen Webhook ausblieb).
	prover.refresh = []string{"github.com/deadweight-labs/a", "github.com/deadweight-labs/private-new"}
	if r := postOrg(t, alice, base, "/ui/orgs/source/import", url.Values{"org": {"alpha"}, "login": {"deadweight-labs"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("import: %d", r.StatusCode)
	}
	if _, known := st.ProjectByRemote("github.com/deadweight-labs/private-new"); !known || !st.ProjectShared("github.com/deadweight-labs/private-new") {
		t.Fatal("the refreshed repo becomes a shared project")
	}
}

// profilingProver also reads the GitHub organization's profile.
type profilingProver struct {
	fakeAppProver
	profile forge.OrgProfile
}

func (p *profilingProver) OrgProfile(_ context.Context, _ int64, _ string) (forge.OrgProfile, error) {
	return p.profile, nil
}

// Security review 4: connecting GitHub takes over the picture only; name and
// address follow after the owner switches it on, and a member cannot.
func TestNameAndAddressFollowGitHubOnlyAfterTheOwnerSwitchesItOn(t *testing.T) {
	prover := &profilingProver{fakeAppProver: fakeAppProver{fakeProver: fakeProver{configured: true, accounts: []forge.Account{{
		Login: "deadweight-labs", InstallationID: 5, Repos: []string{"github.com/deadweight-labs/a"}}}}},
		profile: forge.OrgProfile{Login: "deadweight-labs", Name: "Deadweight Labs"}}
	base, st, alice, anna := sharingWeb(t, prover)
	org, _ := st.OrgByRef("alpha")
	if _, err := st.ConnectSourceWithInstallation("person:1", org.ID, "github", "deadweight-labs", 5, nil); err != nil {
		t.Fatal(err)
	}
	page := getOrgs(t, alice, base, "?org=alpha")
	if !strings.Contains(page, `action="/ui/orgs/follow"`) || !strings.Contains(page, "Take from GitHub") {
		t.Fatalf("no switch for the owner:\n%s", page)
	}
	if r := postOrg(t, anna, base, "/ui/orgs/follow", url.Values{"org": {"alpha"}, "follow": {"1"}}); strings.Contains(r.Header.Get("Location"), "notice=follow") {
		t.Fatal("a member switched it on")
	}
	if got, _ := st.OrgByRef("alpha"); got.DisplaySource == "github" || got.Slug != "alpha" {
		t.Fatalf("after the member: %+v", got)
	}
	r := postOrg(t, alice, base, "/ui/orgs/follow", url.Values{"org": {"alpha"}, "follow": {"1"}})
	if !strings.Contains(r.Header.Get("Location"), "org=deadweight-labs") {
		t.Fatalf("switched on: %q", r.Header.Get("Location"))
	}
	if got, _ := st.OrgByRef("deadweight-labs"); got.Name != "Deadweight Labs" || got.DisplaySource != "github" {
		t.Fatalf("after switching on: %+v", got)
	}
	postOrg(t, alice, base, "/ui/orgs/follow", url.Values{"org": {"deadweight-labs"}, "follow": {"0"}})
	if got, _ := st.OrgByRef("deadweight-labs"); got.DisplaySource != "user" {
		t.Fatalf("after switching off: %+v", got)
	}
}
