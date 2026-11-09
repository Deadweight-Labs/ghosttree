package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// machineEnv: alice (person:1) besitzt "mainex" mit Geräteanmeldung, anna (person:2)
// besitzt "laptop". Die Org alpha hat alice als Owner, die anderen als Mitglieder.
func machineEnv(t *testing.T) sessEnv {
	t.Helper()
	e := seedSessions(t)
	if _, _, err := e.St.CreateDeviceToken("person:1", "mainex"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.St.CreateDeviceToken("person:2", "laptop"); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e sessEnv) post(t *testing.T, c *http.Client, path string, form url.Values) *http.Response {
	t.Helper()
	form.Set("csrf_token", renderedCSRFToken(t, c, e.Base+"/ui/overview"))
	resp := sameOriginPostForm(t, c, e.Base+path, form)
	resp.Body.Close()
	return resp
}

func TestMachinePageOfTheOwnerShowsAgentsSessionsAndManagementForms(t *testing.T) {
	e := machineEnv(t)
	ovAgent(t, e.St, shellProject, "claude:mainex:one", "mainex-agent", "person:1")
	if _, err := e.St.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:mainex:one", PrincipalID: "person:1", Person: "alice", Provider: "claude", DisplayName: "mainex-agent", Machine: "mainex", RoomKey: store.RoomKeyForProject(shellProject)}); err != nil {
		t.Fatal(err)
	}
	code, page := fetchPage(t, e.Owner, e.Base+"/ui/m/mainex")
	if code != http.StatusOK {
		t.Fatalf("machine page = %d", code)
	}
	for _, want := range []string{"mainex", "Your machine", "Knowledge", "Agents", "Sessions", "Fix the zebrafish login", `href="/ui/sessions/` + e.pid["alice-private"] + `"`,
		`href="/ui/sessions?machine=mainex"`, `action="/ui/m/mainex/share"`, `action="/ui/m/mainex/owner"`, "Hand over to an organization", `class="rp"`} {
		if !strings.Contains(page, want) {
			t.Errorf("machine page lacks %q", want)
		}
	}
	// The sessions of other machines are not listed here.
	if strings.Contains(page, e.pid["anna-project"]) {
		t.Error("the machine page lists a session of another machine")
	}
	// The same page stands under the organisation once the machine is shared with it;
	// before that the address leads to the machine's own page.
	if resp, err := e.Owner.Get(e.Base + "/ui/alpha/machines/mainex"); err != nil || resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/m/mainex" {
		t.Errorf("an unshared machine under the organisation: %v %v", err, resp)
	}
	e.post(t, e.Owner, "/ui/m/mainex/share", url.Values{"org": {"alpha"}, "role": {"read"}})
	code, inOrg := fetchPage(t, e.Owner, e.Base+"/ui/alpha/machines/mainex")
	if code != http.StatusOK || !strings.Contains(inOrg, `class="rband`) || !strings.Contains(inOrg, `href="/ui/alpha/machines"`) {
		t.Errorf("machine page in the organisation = %d", code)
	}
}

func TestMachineIsUnknownToThoseItDoesNotConcernAndOpenedByAShare(t *testing.T) {
	e := machineEnv(t)
	for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest, "lead": e.Lead, "reviewer": e.Reviewer} {
		if code, _ := fetchPage(t, c, e.Base+"/ui/m/mainex"); code != http.StatusNotFound {
			t.Errorf("%s reads a machine that is not shared: %d", name, code)
		}
		if code, _ := fetchPage(t, c, e.Base+"/ui/alpha/machines/mainex"); code == http.StatusOK {
			t.Errorf("%s reads the machine under the organisation before it is shared", name)
		}
	}
	if code, _ := fetchPage(t, e.Owner, e.Base+"/ui/m/does-not-exist"); code != http.StatusNotFound {
		t.Errorf("unknown machine = %d", code)
	}
	if code, _ := fetchPage(t, e.Owner, e.Base+"/ui/m/laptop"); code != http.StatusNotFound {
		t.Errorf("the owner reads the machine of a member: %d", code)
	}
	if resp := e.post(t, e.Owner, "/ui/m/mainex/share", url.Values{"org": {"alpha"}, "role": {"read"}}); resp.StatusCode != http.StatusSeeOther || !strings.HasSuffix(resp.Header.Get("Location"), "?done=share") {
		t.Fatalf("share -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	code, page := fetchPage(t, e.Member, e.Base+"/ui/m/mainex")
	if code != http.StatusOK || !strings.Contains(page, "Shared with you, read") {
		t.Fatalf("member after the share = %d", code)
	}
	if strings.Contains(page, `action="/ui/m/mainex/share"`) || strings.Contains(page, `action="/ui/m/mainex/unshare"`) || strings.Contains(page, `action="/ui/m/mainex/owner"`) {
		t.Error("a member gets the management forms")
	}
	if _, orgPage := fetchPage(t, e.Member, e.Base+"/ui/alpha/machines"); !strings.Contains(orgPage, "mainex") || !strings.Contains(orgPage, `href="/ui/alpha/machines/mainex"`) {
		t.Error("the machine tab of the organisation lacks the shared machine")
	}
	if _, band := fetchPage(t, e.Member, e.Base+"/ui/alpha"); !strings.Contains(band, `href="/ui/alpha/machines"`) {
		t.Error("the band of the organisation has no machine tab")
	}
}

func TestMachineManagementNeedsTheOwnerAnInteractiveSessionAndAToken(t *testing.T) {
	e := machineEnv(t)
	e.post(t, e.Owner, "/ui/m/mainex/share", url.Values{"org": {"alpha"}, "role": {"read"}})
	// Without the token of the form nothing changes.
	resp := sameOriginPostForm(t, e.Owner, e.Base+"/ui/m/mainex/unshare", url.Values{"org": {"alpha"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("unshare without a token -> %d", resp.StatusCode)
	}
	// A member who reaches the machine may not manage it.
	for _, p := range []struct {
		path string
		form url.Values
	}{{"/ui/m/mainex/share", url.Values{"org": {"alpha"}, "role": {"write"}}}, {"/ui/m/mainex/unshare", url.Values{"org": {"alpha"}}},
		{"/ui/m/mainex/owner", url.Values{"org": {"alpha"}}}, {"/ui/m/mainex/entry", url.Values{"kind": {"knowledge"}, "id": {"1"}, "org": {"alpha"}, "do": {"share"}}}} {
		if resp := e.post(t, e.Member, p.path, p.form); resp.StatusCode != http.StatusForbidden {
			t.Errorf("member %s -> %d, want 403", p.path, resp.StatusCode)
		}
	}
	// Whoever the machine does not concern gets a plain 404.
	if resp := e.post(t, e.Reviewer, "/ui/m/laptop/share", url.Values{"org": {"alpha"}, "role": {"read"}}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a stranger posts to a foreign machine -> %d, want 404", resp.StatusCode)
	}
	if resp := e.post(t, e.Owner, "/ui/m/nowhere/share", url.Values{"org": {"alpha"}, "role": {"read"}}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown machine -> %d, want 404", resp.StatusCode)
	}
	if got, _ := e.St.MachineFor("person:1", "mainex"); len(got.Shares) != 1 || got.Shares[0].Role != store.ShareRead {
		t.Errorf("the refused posts changed the shares: %+v", got.Shares)
	}
	// The log of the owner shows who shared what with whom.
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/m/mainex")
	for _, want := range []string{"Sharing log", "shared the machine with", "alpha"} {
		if !strings.Contains(page, want) {
			t.Errorf("the owner's page lacks %q", want)
		}
	}
	_, memberPage := fetchPage(t, e.Member, e.Base+"/ui/m/mainex")
	if strings.Contains(memberPage, "Sharing log") {
		t.Error("a member reads the log of the shares")
	}
	// Taking the share back and a refused one are told on the page.
	if resp := e.post(t, e.Owner, "/ui/m/mainex/unshare", url.Values{"org": {"alpha"}}); !strings.HasSuffix(resp.Header.Get("Location"), "?done=unshare") {
		t.Errorf("unshare -> %q", resp.Header.Get("Location"))
	}
	if resp := e.post(t, e.Owner, "/ui/m/mainex/unshare", url.Values{"org": {"alpha"}}); !strings.Contains(resp.Header.Get("Location"), "?fail=") {
		t.Errorf("unsharing twice -> %q", resp.Header.Get("Location"))
	}
	if _, failed := fetchPage(t, e.Owner, e.Base+"/ui/m/mainex?fail=not_shared"); !strings.Contains(failed, "not shared with that organization") {
		t.Error("the refusal is not told")
	}
	if code, _ := fetchPage(t, e.Member, e.Base+"/ui/m/mainex"); code != http.StatusNotFound {
		t.Errorf("after the share ended the member still reaches the machine: %d", code)
	}
}

func TestPastedTokenSessionCannotManageMachines(t *testing.T) {
	e := machineEnv(t)
	token, _, err := e.St.CreateDeviceToken("person:1", "mainex")
	if err != nil {
		t.Fatal(err)
	}
	pasted := login(t, e.Srv, token)
	// A pasted token reads, but its forms are shut.
	if code, page := fetchPage(t, pasted, e.Base+"/ui/m/mainex"); code != http.StatusOK || strings.Contains(page, `action="/ui/m/mainex/share"`) {
		t.Errorf("pasted-token machine page = %d", code)
	}
	resp := sameOriginPostForm(t, pasted, e.Base+"/ui/m/mainex/share", url.Values{"org": {"alpha"}, "role": {"read"}, "csrf_token": {renderedCSRFToken(t, pasted, e.Base+"/ui/overview")}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("pasted token shares a machine -> %d", resp.StatusCode)
	}
	if got, _ := e.St.MachineFor("person:1", "mainex"); len(got.Shares) != 0 {
		t.Error("a pasted token changed the shares")
	}
}

func TestMachineEntriesFollowTheProjectRoleAndHideTheMachineFromGuests(t *testing.T) {
	e := machineEnv(t)
	// Project knowledge that names the machine, and knowledge of the machine alone.
	if _, err := e.St.InsertKnowledge(store.Knowledge{Type: "note", Title: "Deploy needs the build user", Body: "body", Scope: scope.Axes{Project: shellProject, Machine: "mainex"}, Person: "alice", Confidence: "trusted"}); err != nil {
		t.Fatal(err)
	}
	soloID, err := e.St.InsertKnowledge(store.Knowledge{Type: "note", Title: "Disk fills on Mondays", Body: "body", Scope: scope.Axes{Machine: "mainex"}, Person: "alice", Confidence: "trusted"})
	if err != nil {
		t.Fatal(err)
	}
	e.post(t, e.Owner, "/ui/m/mainex/share", url.Values{"org": {"alpha"}, "role": {"read"}})
	_, owner := fetchPage(t, e.Owner, e.Base+"/ui/m/mainex")
	for _, want := range []string{"Deploy needs the build user", "Disk fills on Mondays"} {
		if !strings.Contains(owner, want) {
			t.Errorf("the owner's machine page lacks %q", want)
		}
	}
	// The entry of the machine alone is private until the owner shares it.
	_, member := fetchPage(t, e.Member, e.Base+"/ui/m/mainex")
	if strings.Contains(member, "Disk fills on Mondays") {
		t.Error("a member reads an entry of the machine that was not shared")
	}
	if !strings.Contains(member, "Deploy needs the build user") {
		t.Error("project knowledge that names the machine is hidden from a project member")
	}
	// The guest of the project reaches the machine through the organisation, but
	// the knowledge of the project never names the machine to them.
	code, guest := fetchPage(t, e.Guest, e.Base+"/ui/m/mainex")
	if code == http.StatusOK && strings.Contains(guest, "Deploy needs the build user") {
		t.Error("a guest of the project learns of knowledge tied to the machine")
	}
	if resp := e.post(t, e.Owner, "/ui/m/mainex/entry", url.Values{"kind": {"knowledge"}, "id": {strconv.FormatInt(soloID, 10)}, "org": {"alpha"}, "do": {"share"}}); !strings.HasSuffix(resp.Header.Get("Location"), "?done=entry_share") {
		t.Fatalf("share entry -> %q", resp.Header.Get("Location"))
	}
	if _, member = fetchPage(t, e.Member, e.Base+"/ui/m/mainex"); !strings.Contains(member, "Disk fills on Mondays") {
		t.Error("the shared entry does not reach the member")
	}
	if resp := e.post(t, e.Owner, "/ui/m/mainex/entry", url.Values{"kind": {"knowledge"}, "id": {strconv.FormatInt(soloID, 10)}, "org": {"alpha"}, "do": {"unshare"}}); !strings.HasSuffix(resp.Header.Get("Location"), "?done=entry_unshare") {
		t.Errorf("unshare entry -> %q", resp.Header.Get("Location"))
	}
	if _, member = fetchPage(t, e.Member, e.Base+"/ui/m/mainex"); strings.Contains(member, "Disk fills on Mondays") {
		t.Error("the entry stays visible after it was withdrawn")
	}
	// An entry that belongs to a project cannot be shared through the machine.
	if resp := e.post(t, e.Owner, "/ui/m/mainex/entry", url.Values{"kind": {"knowledge"}, "id": {"1"}, "org": {"alpha"}, "do": {"share"}}); !strings.Contains(resp.Header.Get("Location"), "fail=") {
		t.Errorf("sharing a project entry -> %q", resp.Header.Get("Location"))
	}
}

func TestMachineLinksAppearWhereTheViewerReachesThePage(t *testing.T) {
	e := machineEnv(t)
	if _, err := e.St.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:mainex:one", PrincipalID: "person:1", Person: "alice", Provider: "claude", DisplayName: "mainex-agent", Machine: "mainex", RoomKey: store.RoomKeyForProject(shellProject)}); err != nil {
		t.Fatal(err)
	}
	_, sessions := fetchPage(t, e.Owner, e.Base+"/ui/sessions")
	if !strings.Contains(sessions, `href="/ui/m/mainex"`) {
		t.Error("the session list does not link the machine of the owner")
	}
	_, session := fetchPage(t, e.Owner, e.Base+"/ui/sessions/"+e.pid["alice-private"])
	if !strings.Contains(session, `href="/ui/m/mainex"`) {
		t.Error("the session page does not link the machine of the owner")
	}
	_, agents := fetchPage(t, e.Owner, e.Base+"/ui/agents")
	if !strings.Contains(agents, `href="/ui/m/mainex"`) {
		t.Error("the agent list does not link the machine")
	}
	// A viewer who does not reach the machine sees its name, never a link.
	_, member := fetchPage(t, e.Member, e.Base+"/ui/agents")
	if strings.Contains(member, `/ui/m/mainex`) {
		t.Error("an agent row links a machine the member does not reach")
	}
	_, memberSessions := fetchPage(t, e.Member, e.Base+"/ui/sessions")
	if strings.Contains(memberSessions, `/ui/m/mainex`) || strings.Contains(memberSessions, `/ui/m/laptop"`) && !strings.Contains(memberSessions, `href="/ui/m/laptop"`) {
		t.Error("the member's session list links a machine they do not reach")
	}
	if !strings.Contains(memberSessions, `href="/ui/m/laptop"`) {
		t.Error("the member's own machine is not linked")
	}
}

func TestMachineHandedToAnOrganisationBelongsToItsMembers(t *testing.T) {
	e := machineEnv(t)
	if resp := e.post(t, e.Owner, "/ui/m/mainex/owner", url.Values{"org": {"alpha"}}); !strings.HasSuffix(resp.Header.Get("Location"), "?done=owner") {
		t.Fatalf("hand over -> %q", resp.Header.Get("Location"))
	}
	code, page := fetchPage(t, e.Member, e.Base+"/ui/m/mainex")
	if code != http.StatusOK || !strings.Contains(page, "Machine of your organization") || !strings.Contains(page, `href="/ui/alpha"`) {
		t.Errorf("member of the owning organisation = %d", code)
	}
	// An organisation machine cannot be handed over a second time.
	if resp := e.post(t, e.Owner, "/ui/m/mainex/owner", url.Values{"org": {"alpha"}}); !strings.Contains(resp.Header.Get("Location"), "fail=") {
		t.Errorf("handing over twice -> %q", resp.Header.Get("Location"))
	}
	// A wrong address under another organisation leads to the machine's own page.
	if _, err := e.St.CreateOrg("person:1", "Beta", "beta"); err != nil {
		t.Fatal(err)
	}
	resp, err := e.Owner.Get(e.Base + "/ui/beta/machines/mainex")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/m/mainex" {
		t.Errorf("machine under the wrong organisation -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}
