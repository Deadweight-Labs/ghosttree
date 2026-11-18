package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Jede Route, die etwas ändert, ist ausdrücklich zugeordnet: entweder darf sie auch
// eine Sitzung aus einem Einmal-Link (Owner-Link, ctx web), oder sie verlangt eine
// Anmeldung mit echtem Nachweis. Eine neue Route ohne Zuordnung bricht diesen Test.
func TestEveryChangingRouteHasALinkRule(t *testing.T) {
	for pattern, class := range webRoutes {
		if class == webPublic {
			continue
		}
		method, _, _ := strings.Cut(pattern, " ")
		if method == http.MethodGet && !linkSideEffectGET[pattern] {
			continue
		}
		if _, ok := linkRules[pattern]; !ok {
			t.Errorf("%s changes state but has no entry in linkRules (linkAllowed or linkProof)", pattern)
		}
	}
	for pattern := range linkRules {
		if _, ok := webRoutes[pattern]; !ok {
			t.Errorf("linkRules names %s, which is not a route", pattern)
		}
	}
}

// Was Rechte oder Zugänge verändert oder Geheimnisse berührt, darf nie "auch mit
// Link-Sitzung" heißen.
func TestLinkAllowedRoutesTouchNoAccess(t *testing.T) {
	for pattern, rule := range linkRules {
		if rule != linkAllowed {
			continue
		}
		for _, bad := range []string{"/ui/orgs/", "/ui/device", "/join/pair", "/join/{code}/accept", "/ui/profile", "/ui/account", "/ai/", "/ui/m/", "/ui/runbooks", "/ui/workflows/save", "/ui/handover", "/ui/gardener", "/ui/tasks/", "/ui/inbox/answer", "/ui/review/", "/ui/sessions/"} {
			if strings.Contains(pattern, bad) {
				t.Errorf("%s is allowed for a link session but sounds like access or secrets", pattern)
			}
		}
	}
}

func csrfOfPage(t *testing.T, page string) string {
	t.Helper()
	m := regexp.MustCompile(`name="csrf_token" value="([0-9a-f]+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("page has no csrf token: %.300s", page)
	}
	return m[1]
}

// Eine Link-Sitzung (hier: Owner-Link für ein Mitglied, also mit Menschen-Rechten)
// kommt an keine Route heran, die Rechte oder Zugänge ändert.
func TestLinkSessionIsRefusedOnProofRoutes(t *testing.T) {
	srv, st, _ := testWeb(t)
	org, _ := st.CreateOrg("person:1", "Alpha", "alpha")
	jonas, _ := st.AddAccount("jonas", "", false)
	invite, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, time.Hour)
	if _, err := st.AcceptInvitation(jonas.ID, invite); err != nil {
		t.Fatal(err)
	}
	code, _, err := st.IssueMemberLoginLink("person:1", org.ID, jonas.ID)
	if err != nil {
		t.Fatal(err)
	}
	client := noRedirect()
	resp := sameOriginPostForm(t, client, srv.URL+"/ui/login/code", url.Values{"code": {code}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login link: %d %s", resp.StatusCode, body(t, resp))
	}
	client.Jar = cookieJar{cookies: resp.Cookies()}
	csrf := csrfOfPage(t, body(t, getURL(t, client, srv.URL+"/ui/orgs")))
	checked := 0
	for pattern, rule := range linkRules {
		method, path, _ := strings.Cut(pattern, " ")
		if method != http.MethodPost || rule != linkProof || strings.Contains(path, "{") {
			continue
		}
		checked++
		resp := sameOriginPostForm(t, client, srv.URL+path, url.Values{"csrf_token": {csrf}})
		page := body(t, resp)
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(page, `href="/ui/login?relogin=1`) {
			t.Errorf("%s with a link session: %d, want 403 with a way to sign in again", pattern, resp.StatusCode)
		}
	}
	if checked < 20 {
		t.Fatalf("only %d proof routes were tried", checked)
	}
	// Ordinary work stays open.
	resp = sameOriginPostForm(t, client, srv.URL+"/ui/welcome/dismiss", url.Values{"csrf_token": {csrf}})
	resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("a link session may dismiss the welcome note: %d", resp.StatusCode)
	}
}

// Wer mit einem Link angemeldet ist und sich neu anmelden will, sieht das Formular
// statt einer Weiterleitung auf die Seite, auf der er schon ist.
func TestLinkSessionCanSignInAgain(t *testing.T) {
	srv, st, _ := testWeb(t)
	org, _ := st.CreateOrg("person:1", "Alpha", "alpha")
	jonas, _ := st.AddAccount("jonas", "", false)
	invite, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, time.Hour)
	if _, err := st.AcceptInvitation(jonas.ID, invite); err != nil {
		t.Fatal(err)
	}
	code, _, _ := st.IssueMemberLoginLink("person:1", org.ID, jonas.ID)
	client := noRedirect()
	resp := sameOriginPostForm(t, client, srv.URL+"/ui/login/code", url.Values{"code": {code}})
	resp.Body.Close()
	client.Jar = cookieJar{cookies: resp.Cookies()}
	resp = getURL(t, client, srv.URL+"/ui/login?relogin=1&next=/ui/orgs")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login page for a link session: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestLinkSessionIsShortAndNotSliding(t *testing.T) {
	s := newSessions()
	id, err := s.create(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind})
	if err != nil {
		t.Fatal(err)
	}
	s.markLink(id)
	v, _ := s.get(id)
	if left := time.Until(v.expires); left > linkSessionMaxAge || left < linkSessionMaxAge-time.Minute {
		t.Fatalf("a link session lasts %v, want about %v", left, linkSessionMaxAge)
	}
	time.Sleep(10 * time.Millisecond)
	v2, _ := s.get(id)
	if !v2.expires.Equal(v.expires) {
		t.Fatal("a link session must not slide: its end stays")
	}
}

func TestPageViewIsANavigationOfADocument(t *testing.T) {
	for name, tc := range map[string]struct {
		header map[string]string
		want   bool
	}{
		"a plain request":      {nil, true},
		"a navigation":         {map[string]string{"Sec-Fetch-Dest": "document", "Sec-Fetch-Mode": "navigate"}, true},
		"an image of a page":   {map[string]string{"Sec-Fetch-Dest": "image", "Sec-Fetch-Mode": "no-cors"}, false},
		"a fetch":              {map[string]string{"Sec-Fetch-Dest": "empty", "Sec-Fetch-Mode": "cors"}, false},
		"a prefetch":           {map[string]string{"Sec-Purpose": "prefetch"}, false},
		"an old prefetch":      {map[string]string{"Purpose": "prefetch"}, false},
		"an embedded document": {map[string]string{"Sec-Fetch-Dest": "iframe", "Sec-Fetch-Mode": "navigate"}, false},
	} {
		r := httptest.NewRequest(http.MethodGet, "/ui/orgs", nil)
		for k, v := range tc.header {
			r.Header.Set(k, v)
		}
		if got := pageView(r); got != tc.want {
			t.Errorf("%s: pageView = %v, want %v", name, got, tc.want)
		}
	}
}

// A pasted token does not take an invitation; a sign-in does.
func TestPastedTokenDoesNotAcceptAnInvitation(t *testing.T) {
	srv, st, _ := testWeb(t)
	org, _ := st.CreateOrg("person:1", "Alpha", "alpha")
	jonasToken, err := st.AddPerson("jonas")
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, time.Hour)
	pasted := login(t, srv, jonasToken)
	csrf := csrfOfPage(t, body(t, getURL(t, pasted, srv.URL+"/ui/orgs")))
	resp := sameOriginPostForm(t, pasted, srv.URL+"/ui/orgs/accept", url.Values{"csrf_token": {csrf}, "code": {code}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a pasted token took an invitation: %d", resp.StatusCode)
	}
	if orgs, _ := st.ListOrgs("person:2"); len(orgs) != 0 {
		t.Fatalf("the account is a member after a pasted token: %v", orgs)
	}
}
