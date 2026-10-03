package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Zeitstempel erscheinen als <time> mit relativem Text, nie als roher RFC3339.
var rawStampRE = regexp.MustCompile(`>\s*20\d\d-\d\d-\d\dT\d\d:\d\d`)

func adminSeed(t *testing.T) (base string, st *store.Store, alice, anna *http.Client) {
	t.Helper()
	base, st, alice, anna, org := orgWeb(t)
	if _, err := st.ClaimProject("person:1", "github.com/x/one", "alpha"); err != nil {
		t.Fatal(err)
	}
	code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	return base, st, alice, anna
}

func TestAdminPagesShowRelativeTimesWithFullDate(t *testing.T) {
	base, st, alice, _ := adminSeed(t)
	if _, _, err := st.CreateInvitation("person:1", 1, "", store.OrgMember, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateToken("alice", store.TokenSpec{Label: "ci"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/ui/orgs", "/ui/account/tokens"} {
		resp, _ := alice.Get(base + path)
		page := body(t, resp)
		if !regexp.MustCompile(`<time datetime="20\d\d-[^"]+Z" title="20\d\d-\d\d-\d\d \d\d:\d\d UTC">[^<]+</time>`).MatchString(page) {
			t.Errorf("%s: no <time> element with full date: %s", path, page)
		}
		if rawStampRE.MatchString(page) {
			t.Errorf("%s shows a raw timestamp", path)
		}
	}
}

func TestAdminTimeTagIsRelativeBothWays(t *testing.T) {
	now := overviewNow().UTC()
	for _, c := range []struct {
		stamp string
		want  string
	}{
		{now.Add(-3 * 60 * 60 * 1e9).Format("2006-01-02T15:04:05Z"), "3h ago"},
		{now.Add(5*24*60*60*1e9 + 60*1e9).Format("2006-01-02T15:04:05Z"), "in 5d"},
		{now.Format("2006-01-02T15:04:05Z"), ">now<"},
	} {
		if got := string(timeTag(c.stamp)); !strings.Contains(got, c.want) {
			t.Errorf("timeTag(%s) = %s, want %s", c.stamp, got, c.want)
		}
	}
	if timeTag("garbage") != "" || timeTag("") != "" {
		t.Error("an unreadable stamp must render nothing")
	}
}

func TestOrgInviteResultIsFormAndLinkOnly(t *testing.T) {
	base, _, alice, _ := adminSeed(t)
	resp, _ := alice.Get(base + "/ui/orgs")
	page := body(t, resp)
	for _, gone := range []string{"identity provider", "Shown once", "single use, valid until", "optional;"} {
		if strings.Contains(page, gone) {
			t.Errorf("help text %q is back", gone)
		}
	}
	resp = postOrg(t, alice, base, "/ui/orgs/invite", url.Values{"org": {"alpha"}, "role": {"member"}, "days": {"2"}})
	page = body(t, resp)
	if !strings.Contains(page, `class="ad-link"`) || !strings.Contains(page, "Invitation ready") || !strings.Contains(page, "data-copy") {
		t.Fatalf("invite result lacks the link and copy action: %s", page)
	}
	if !regexp.MustCompile(`>in \d+[hd]<`).MatchString(page) {
		t.Errorf("expiry is not relative: %s", page)
	}
}

func TestOrgNoticeComesFromAKnownKeyOnly(t *testing.T) {
	base, _, alice, _ := adminSeed(t)
	resp, _ := alice.Get(base + "/ui/orgs?org=alpha&notice=revoked")
	if page := body(t, resp); !strings.Contains(page, "Invitation revoked.") {
		t.Fatalf("known notice missing: %s", page)
	}
	resp, _ = alice.Get(base + "/ui/orgs?org=alpha&notice=Your+account+is+compromised")
	if page := body(t, resp); strings.Contains(page, "compromised") || strings.Contains(page, `role="status"`) {
		t.Fatalf("a link injected its own text: %s", page)
	}
}

// Jede Aktion verlangt CSRF-Token und interaktive Sitzung; ein Member darf
// nichts, was dem Owner gehört.
func TestAdminActionsRefuseMissingCSRFPastedTokensAndNonOwners(t *testing.T) {
	base, st, alice, anna := adminSeed(t)
	srvTok, _ := st.AddPerson("pasteuser")
	_ = srvTok
	ownerOnly := map[string]url.Values{
		"/ui/orgs/invite":        {"org": {"alpha"}, "role": {"member"}, "days": {"1"}},
		"/ui/orgs/invite/revoke": {"org": {"alpha"}, "id": {"1"}},
		"/ui/orgs/member/role":   {"org": {"alpha"}, "account": {"person:2"}, "role": {"owner"}},
		"/ui/orgs/member/remove": {"org": {"alpha"}, "account": {"person:1"}},
		"/ui/orgs/project/move":  {"org": {"alpha"}, "remote": {"github.com/x/one"}, "to": {"alpha"}},
		"/ui/orgs/project/role":  {"org": {"alpha"}, "remote": {"github.com/x/one"}, "account": {"person:2"}, "role": {"member"}},
	}
	for path, form := range ownerOnly {
		// ohne CSRF-Token
		if resp := sameOriginPostForm(t, alice, base+path, form); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s without csrf: %d", path, resp.StatusCode)
		}
		// fremde Herkunft
		f := url.Values{"csrf_token": {renderedCSRFToken(t, alice, base+"/ui/orgs")}}
		for k, v := range form {
			f[k] = v
		}
		req, _ := http.NewRequest(http.MethodPost, base+path, strings.NewReader(f.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "https://evil.example")
		if resp, err := alice.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s from a foreign origin: %v %v", path, err, resp)
		}
		// Member
		resp := postOrg(t, anna, base, path, cloneForm(form))
		if resp.StatusCode < 400 {
			t.Errorf("member may not %s: %d", path, resp.StatusCode)
		}
	}
	// Das Mitglied bleibt Member, das Konto des Owners bleibt bestehen.
	if st.OrgRole(1, "person:2") != store.OrgMember || st.OrgRole(1, "person:1") != store.OrgOwner {
		t.Fatal("a refused action changed roles")
	}
}

func cloneForm(f url.Values) url.Values {
	c := url.Values{}
	for k, v := range f {
		c[k] = append([]string(nil), v...)
	}
	return c
}

// Aussperr-Schutz: die letzte Owner-Rolle lässt sich weder abgeben noch
// verlassen, auch nicht über die Weboberfläche.
func TestLastOrgOwnerCannotLeaveOrStepDownInTheBrowser(t *testing.T) {
	base, st, alice, _ := adminSeed(t)
	resp := postOrg(t, alice, base, "/ui/orgs/member/remove", url.Values{"org": {"alpha"}, "account": {"person:1"}})
	if page := body(t, resp); resp.StatusCode != http.StatusConflict || !strings.Contains(page, "at least one owner") {
		t.Fatalf("last owner leaves: %d %s", resp.StatusCode, page)
	}
	resp = postOrg(t, alice, base, "/ui/orgs/member/role", url.Values{"org": {"alpha"}, "account": {"person:1"}, "role": {"member"}})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("last owner steps down: %d", resp.StatusCode)
	}
	if st.OrgRole(1, "person:1") != store.OrgOwner {
		t.Fatal("the last owner lost the role")
	}
	// Mit einem zweiten Owner geht es.
	resp = postOrg(t, alice, base, "/ui/orgs/member/role", url.Values{"org": {"alpha"}, "account": {"person:2"}, "role": {"owner"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("promote: %d", resp.StatusCode)
	}
	resp = postOrg(t, alice, base, "/ui/orgs/member/remove", url.Values{"org": {"alpha"}, "account": {"person:1"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("leave with a second owner: %d", resp.StatusCode)
	}
}

func TestOrgPageGivesAMemberOnlyTheirOwnControls(t *testing.T) {
	base, _, _, anna := adminSeed(t)
	resp, _ := anna.Get(base + "/ui/orgs")
	page := body(t, resp)
	for _, hidden := range []string{"/ui/orgs/invite", "/ui/orgs/member/role", "/ui/orgs/project/move", "Invitations", "Make owner", "Remove"} {
		if strings.Contains(page, hidden) {
			t.Errorf("member sees %q", hidden)
		}
	}
	if !strings.Contains(page, "Leave") || !strings.Contains(page, "/ui/orgs/default") && !strings.Contains(page, "Default") {
		t.Errorf("member lacks own controls: %s", page)
	}
}

func TestOrgPageGuestSeesOnlyTheirProjectAndNoOwnerControls(t *testing.T) {
	base, st, alice, anna := adminSeed(t)
	if _, err := st.ClaimProject("person:1", "github.com/x/secret", "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectRole("person:1", "github.com/x/one", "person:2", store.RoleGuest, false, store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	st.SetAccessMode(store.AccessMode{Enforce: true})
	resp, _ := anna.Get(base + "/ui/orgs")
	page := body(t, resp)
	if strings.Contains(page, "github.com/x/secret") || strings.Contains(page, "alice") && strings.Contains(page, "/ui/orgs/project/role") {
		t.Fatalf("guest sees foreign project data: %s", page)
	}
	if strings.Contains(page, "/ui/orgs/invite") || strings.Contains(page, "/ui/orgs/project/role") {
		t.Fatalf("guest sees management forms: %s", page)
	}
	if strings.Contains(page, "github.com/x/secret") {
		t.Fatal("guest sees a hidden project")
	}
	// Der Owner sieht beides.
	resp, _ = alice.Get(base + "/ui/orgs")
	if page := body(t, resp); !strings.Contains(page, "github.com/x/secret") {
		t.Fatal("owner lost the project")
	}
}

func TestAdminEmptyStatesAreOneLineAndOneAction(t *testing.T) {
	srv, st, _ := testWeb(t)
	if _, err := st.CreateOrg("person:1", "Alpha", "alpha"); err != nil {
		t.Fatal(err)
	}
	alice := loginInteractive(t, srv, st, "alice")
	resp, _ := alice.Get(srv.URL + "/ui/orgs")
	page := body(t, resp)
	if !strings.Contains(page, "No projects yet.") || !strings.Contains(page, "Connect an agent") {
		t.Errorf("projects empty state: %s", page)
	}
	if strings.Contains(page, "A project joins your default organization") {
		t.Error("the old explanation is back")
	}
	var sb strings.Builder
	if err := pages.ExecuteTemplate(&sb, "tokens", pageData{Interactive: true}); err != nil {
		t.Fatal(err)
	}
	if page := sb.String(); !strings.Contains(page, "No tokens yet.") || !strings.Contains(page, `href="/ui/device"`) {
		t.Errorf("tokens empty state: %s", page)
	}
}

func TestTokensPageIsRowsNotATableAndKeepsRevoke(t *testing.T) {
	base, st, alice, anna := adminSeed(t)
	if _, _, err := st.CreateToken("anna", store.TokenSpec{Label: "annas"}); err != nil {
		t.Fatal(err)
	}
	resp, _ := alice.Get(base + "/ui/account/tokens")
	page := body(t, resp)
	if strings.Contains(page, "<table") || !strings.Contains(page, "annas") || !strings.Contains(page, "/ui/account/tokens/revoke") {
		t.Fatalf("admin tokens: %s", page)
	}
	resp, _ = anna.Get(base + "/ui/account/tokens")
	if page := body(t, resp); !strings.Contains(page, "annas") {
		t.Fatalf("member lacks own token: %s", page)
	}
}

func TestDevicePagesUseTheCatalogAndTheCodeField(t *testing.T) {
	base, _, alice, _ := adminSeed(t)
	resp, _ := alice.Get(base + "/ui/device?user_code=ABCD-EFGH")
	page := body(t, resp)
	if !strings.Contains(page, `class="ov-code"`) || !strings.Contains(page, `value="ABCD-EFGH"`) || strings.Contains(page, "Only approve") {
		t.Fatalf("device page: %s", page)
	}
	resp = sameOriginPostForm(t, alice, base+"/ui/device", url.Values{"csrf_token": {renderedCSRFToken(t, alice, base+"/ui/device")}, "user_code": {"ZZZZ-ZZZZ"}})
	if page := body(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(page, "not valid or has expired") {
		t.Fatalf("bad code: %d %s", resp.StatusCode, page)
	}
}

func TestLoginRequiredPageIsEnglishAndShort(t *testing.T) {
	srv, st, tok := testWeb(t)
	_ = st
	pasted := login(t, srv, tok)
	resp := sameOriginPostForm(t, pasted, srv.URL+"/ui/device", url.Values{"csrf_token": {renderedCSRFToken(t, pasted, srv.URL+"/ui/overview")}, "user_code": {"x"}})
	page := body(t, resp)
	if resp.StatusCode != http.StatusForbidden || strings.Contains(page, "Diese Aktion") || strings.Contains(page, "Anmeldung per Login") || !strings.Contains(page, "read-only session") {
		t.Fatalf("login required page: %d %s", resp.StatusCode, page)
	}
}
