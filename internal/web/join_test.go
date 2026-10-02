package web

import (
	"bytes"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const joinProject = "github.com/alpha/app"

// joinWeb: Org "Alpha" (alice Owner), Projekt, ein anderer Account "anna".
func joinWeb(t *testing.T) (srv *httptest.Server, st *store.Store, alice *http.Client, org store.Org, aliceTok string) {
	t.Helper()
	srv, st, aliceTok = testWeb(t)
	var err error
	if org, err = st.CreateOrg("person:1", "Alpha", "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.EnsureProject("person:1", joinProject); err != nil {
		t.Fatal(err)
	}
	return srv, st, loginInteractive(t, srv, st, "alice"), org, aliceTok
}

func projectInvite(t *testing.T, st *store.Store, org store.Org, role string) string {
	t.Helper()
	code, _, err := st.CreateProjectInvitation("person:1", org.ID, joinProject, role, 0)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// snapshot macht aus einer Antwort alles, was ein Fremder sieht, als Text.
func snapshot(t *testing.T, resp *http.Response) string {
	t.Helper()
	var keys []string
	for k := range resp.Header {
		if k != "Date" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "%d\n", resp.StatusCode)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %s\n", k, strings.Join(resp.Header[k], ","))
	}
	b.WriteString("\n" + body(t, resp))
	return b.String()
}

func anonClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Pitfall #2447: jeder Grund, aus dem ein Code nicht taugt, liefert byteidentisch
// dieselbe Antwort, Header eingeschlossen.
func TestJoinEveryInvalidVariantIsByteIdentical(t *testing.T) {
	srv, st, _, org, _ := joinWeb(t)
	for _, n := range []string{"anna", "ben"} {
		if _, err := st.AddPerson(n); err != nil {
			t.Fatal(err)
		}
	}
	short, _, err := st.CreateProjectInvitation("person:1", org.ID, joinProject, store.RoleMember, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	used := projectInvite(t, st, org, store.RoleMember)
	if _, err := st.AcceptInvitation("person:2", used); err != nil {
		t.Fatal(err)
	}
	revoked, revokedInv, _ := st.CreateProjectInvitation("person:1", org.ID, joinProject, store.RoleMember, 0)
	if err := st.RevokeInvitation("person:1", org.ID, revokedInv.ID); err != nil {
		t.Fatal(err)
	}
	// Einlader ben war Owner und ist es nicht mehr.
	if _, err := st.AcceptInvitation("person:3", mustOrgInvite(t, st, org)); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgRole("person:1", org.ID, "person:3", store.OrgOwner); err != nil {
		t.Fatal(err)
	}
	demoted, _, err := st.CreateProjectInvitation("person:3", org.ID, joinProject, store.RoleGuest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgRole("person:1", org.ID, "person:3", store.OrgMember); err != nil {
		t.Fatal(err)
	}
	// Projekt in eine andere Organisation verschoben.
	other, err := st.CreateOrg("person:1", "Beta", "beta")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureProject("person:1", "github.com/alpha/moving"); err != nil {
		t.Fatal(err)
	}
	moved, _, err := st.CreateProjectInvitation("person:1", org.ID, "github.com/alpha/moving", store.RoleMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MoveProject("person:1", "github.com/alpha/moving", other.Slug); err != nil {
		t.Fatal(err)
	}
	ownerInv, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgOwner, 0)
	orgWide, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	emailBound, _, _ := st.CreateInvitation("person:1", org.ID, "x@example.test", store.OrgMember, 0)
	time.Sleep(1100 * time.Millisecond)

	secrets := map[string]string{"expired": short, "used": used, "revoked": revoked, "demoted inviter": demoted,
		"moved project": moved, "org wide owner": ownerInv, "org wide member": orgWide, "email bound": emailBound}
	cases := map[string]string{
		"unknown": "/join/" + strings.Repeat("ab", 32), "short": "/join/abc", "upper case": "/join/" + strings.Repeat("AB", 32),
		"non hex": "/join/" + strings.Repeat("zz", 32), "too long": "/join/" + strings.Repeat("ab", 200), "empty": "/join/",
		"extra segment": "/join/" + strings.Repeat("ab", 32) + "/x", "traversal": "/join/..%2f..%2fui", "encoded space": "/join/%20",
		"query is ignored": "/join/" + strings.Repeat("ab", 32) + "?code=" + used,
	}
	for name, code := range secrets {
		cases[name] = "/join/" + code
	}
	names := make([]string, 0, len(cases))
	for n := range cases {
		names = append(names, n)
	}
	sort.Strings(names)
	var ref, refName string
	for _, name := range names {
		resp, err := anonClient().Get(srv.URL + cases[name])
		if err != nil {
			t.Fatal(err)
		}
		snap := snapshot(t, resp)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: status %d", name, resp.StatusCode)
		}
		if ref == "" {
			ref, refName = snap, name
		} else if snap != ref {
			t.Errorf("%s differs from %s:\n--- %s\n%s\n--- %s\n%s", name, refName, refName, ref, name, snap)
		}
		for _, code := range secrets {
			if strings.Contains(snap, code) {
				t.Errorf("%s: the response echoes a code", name)
			}
		}
	}
	// HEAD fällt nicht aus der Reihe.
	req, _ := http.NewRequest(http.MethodHead, srv.URL+"/join/"+demoted, nil)
	if resp, err := anonClient().Do(req); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("HEAD: %v %v", resp, err)
	}
}

func mustOrgInvite(t *testing.T, st *store.Store, org store.Org) string {
	t.Helper()
	code, _, err := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestJoinPageShowsOnlyWhatTheInviteeNeedsAndSetsSafeHeaders(t *testing.T) {
	srv, st, _, org, _ := joinWeb(t)
	st.AddPerson("anna")
	st.AddPerson("ben")
	if _, err := st.AcceptInvitation("person:2", mustOrgInvite(t, st, org)); err != nil {
		t.Fatal(err)
	}
	code := projectInvite(t, st, org, store.RoleGuest)
	resp, err := anonClient().Get(srv.URL + "/join/" + code)
	if err != nil {
		t.Fatal(err)
	}
	h := resp.Header
	page := body(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, page)
	}
	for _, want := range []string{"Alpha", joinProject, "guest", "does not use the invitation"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q: %s", want, page)
		}
	}
	// Keine Namen anderer Personen, keine Zähler, keine Ids, kein Einlader.
	for _, leak := range []string{"alice", "anna", "ben", "person:", "Beta"} {
		if strings.Contains(page, leak) {
			t.Errorf("page leaks %q", leak)
		}
	}
	for k, want := range map[string]string{"Referrer-Policy": "no-referrer", "Cache-Control": "no-store", "X-Robots-Tag": "noindex, nofollow", "X-Content-Type-Options": "nosniff"} {
		if h.Get(k) != want {
			t.Errorf("%s=%q want %q", k, h.Get(k), want)
		}
	}
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "frame-ancestors 'none'", "base-uri 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %s", csp, want)
		}
	}
	if strings.Contains(csp, "script-src") || strings.Contains(csp, "unsafe") {
		t.Errorf("CSP allows scripts: %s", csp)
	}
	if len(h.Values("Set-Cookie")) != 0 {
		t.Errorf("the page sets cookies: %v", h.Values("Set-Cookie"))
	}
	if regexp.MustCompile(`(?i)(src|href|action)="(https?:)?//`).MatchString(page) {
		t.Errorf("page loads or links something external: %s", page)
	}
	if strings.Contains(page, "<script") {
		t.Error("the page carries a script")
	}
}

func TestJoinPageNeverConsumesTheInvitation(t *testing.T) {
	srv, st, _, org, _ := joinWeb(t)
	st.AddPerson("anna")
	code := projectInvite(t, st, org, store.RoleMember)
	for i := 0; i < 5; i++ {
		resp, _ := anonClient().Get(srv.URL + "/join/" + code)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("view %d: %d", i, resp.StatusCode)
		}
		resp.Body.Close()
		head, _ := http.NewRequest(http.MethodHead, srv.URL+"/join/"+code, nil)
		r2, _ := anonClient().Do(head)
		r2.Body.Close()
	}
	if _, err := st.PreviewInvitation(code); err != nil {
		t.Fatalf("invitation gone after previews: %v", err)
	}
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatalf("accept after previews: %v", err)
	}
}

func TestJoinAcceptNeedsInteractiveLoginAndCSRFAndHappensOnce(t *testing.T) {
	srv, st, _, org, aliceTok := joinWeb(t)
	annaTok, _ := st.AddPerson("anna")
	anna := loginInteractive(t, srv, st, "anna")
	code := projectInvite(t, st, org, store.RoleGuest)
	target := srv.URL + "/join/" + code + "/accept"

	// Ohne Anmeldung: Weiterleitung zum Login, nichts verbraucht.
	resp := sameOriginPostForm(t, anonClient(), target, url.Values{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/login" {
		t.Fatalf("anonymous accept: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	// Mit eingefügtem Token (keine interaktive Sitzung): 403.
	pasted := login(t, srv, annaTok)
	resp = sameOriginPostForm(t, pasted, target, url.Values{"csrf_token": {renderedCSRFToken(t, pasted, srv.URL+"/ui/orgs")}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("token session accept: %d", resp.StatusCode)
	}
	_ = aliceTok
	// Ohne und mit falschem CSRF-Token: 403.
	for _, token := range []string{"", "nope"} {
		resp = sameOriginPostForm(t, anna, target, url.Values{"csrf_token": {token}})
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("csrf %q accept: %d", token, resp.StatusCode)
		}
	}
	if _, err := st.PreviewInvitation(code); err != nil {
		t.Fatalf("a refused accept consumed the invitation: %v", err)
	}
	// Die Seite für ein angemeldetes Konto bietet den Beitritt an.
	page, _ := anna.Get(srv.URL + "/join/" + code)
	text := body(t, page)
	if !strings.Contains(text, "Join as anna") || !strings.Contains(text, `action="/join/`+code+`/accept"`) {
		t.Fatalf("signed-in page: %s", text)
	}
	csrf := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(text)[1]
	resp = sameOriginPostForm(t, anna, target, url.Values{"csrf_token": {csrf}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("accept: %d", resp.StatusCode)
	}
	if got := st.ProjectRole(joinProject, "person:2").Role; got != store.RoleGuest {
		t.Fatalf("role %q", got)
	}
	// Einmalig: ein zweiter Versuch sieht die gleiche 404-Seite wie ein unbekannter Code.
	resp = sameOriginPostForm(t, anna, target, url.Values{"csrf_token": {csrf}})
	second := snapshot(t, resp)
	unknown, _ := anonClient().Get(srv.URL + "/join/" + strings.Repeat("ab", 32))
	if second != snapshot(t, unknown) {
		t.Fatalf("a used code answers differently from an unknown one:\n%s", second)
	}
}

func TestJoinLimiterIsPerAddressAndNeverPerCode(t *testing.T) {
	_, st, _, org, _ := joinWeb(t)
	code := projectInvite(t, st, org, store.RoleMember)
	h := New(st)
	get := func(path, remote, xff string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = remote
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	wrong := "/join/" + strings.Repeat("cd", 32)
	for i := 0; i < joinLimit; i++ {
		if rec := get(wrong, "198.51.100.7:1000", ""); rec.Code != http.StatusNotFound {
			t.Fatalf("request %d: %d", i, rec.Code)
		}
	}
	over := get(wrong, "198.51.100.7:1001", "")
	if over.Code != http.StatusTooManyRequests || over.Header().Get("Retry-After") == "" {
		t.Fatalf("over the limit: %d", over.Code)
	}
	// Dieselbe Adresse, echter Code: ebenfalls begrenzt, mit derselben Antwort.
	real := get("/join/"+code, "198.51.100.7:1002", "")
	if real.Code != over.Code || real.Body.String() != over.Body.String() {
		t.Fatalf("a valid code is answered differently once limited: %d", real.Code)
	}
	// Eine andere Adresse ist nicht betroffen: Fremde sperren keine echte Einladung.
	if rec := get("/join/"+code, "198.51.100.8:1000", ""); rec.Code != http.StatusOK {
		t.Fatalf("other address: %d", rec.Code)
	}
	// Ein vom Client mitgeschickter X-Forwarded-For zählt ohne vertrauenswürdigen Proxy nicht.
	if rec := get("/join/"+code, "198.51.100.7:1003", "203.0.113.50"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("spoofed X-Forwarded-For escaped the limit: %d", rec.Code)
	}
}

func TestJoinLimiterWindowResetsAndIsBounded(t *testing.T) {
	now := time.Unix(1000, 0)
	l := &joinLimiter{now: func() time.Time { return now }, counts: map[string]int{}}
	for i := 0; i < joinLimit; i++ {
		if !l.allow("a") {
			t.Fatal("early refusal")
		}
	}
	if l.allow("a") || !l.allow("b") {
		t.Fatal("per-key counting is broken")
	}
	now = now.Add(joinWindow)
	if !l.allow("a") {
		t.Fatal("the window did not reset")
	}
	for i := 0; i < joinLimiterMax+10; i++ {
		l.allow(fmt.Sprint("k", i))
	}
	if len(l.counts) > joinLimiterMax {
		t.Fatalf("the limiter grew to %d keys", len(l.counts))
	}
}

func TestJoinIPv6AddressesShareTheirPrefix(t *testing.T) {
	a := &app{}
	k := func(addr string) string {
		r := httptest.NewRequest(http.MethodGet, "/join/x", nil)
		r.RemoteAddr = addr
		return a.joinClientKey(r)
	}
	if k("[2001:db8:1:2::1]:1") != k("[2001:db8:1:2:ffff::9]:1") || k("[2001:db8:1:2::1]:1") == k("[2001:db8:1:3::1]:1") {
		t.Fatal("IPv6 clients are not bucketed by /64")
	}
}

func TestJoinWritesNoLogLine(t *testing.T) {
	srv, st, _, org, _ := joinWeb(t)
	st.AddPerson("anna")
	code := projectInvite(t, st, org, store.RoleMember)
	var logs bytes.Buffer
	prevSlog, prevOut := slog.Default(), log.Writer()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	log.SetOutput(&logs)
	t.Cleanup(func() { slog.SetDefault(prevSlog); log.SetOutput(prevOut) })
	for _, path := range []string{"/join/" + code, "/join/" + code + "/accept", "/join/" + strings.Repeat("ab", 32)} {
		resp, _ := anonClient().Get(srv.URL + path)
		resp.Body.Close()
	}
	if strings.Contains(logs.String(), code) {
		t.Fatalf("the invitation code reached a log: %s", logs.String())
	}
}

func TestJoinLocalSignInCreatesAccountWithProjectRole(t *testing.T) {
	srv, st, _, org, _ := joinWeb(t)
	code := projectInvite(t, st, org, store.RoleMember)
	page, _ := anonClient().Get(srv.URL + "/join/" + code)
	text := body(t, page)
	if !strings.Contains(text, `action="/ui/login/code"`) || !strings.Contains(text, `name="name"`) {
		t.Fatalf("local mode page: %s", text)
	}
	resp := sameOriginPostForm(t, anonClient(), srv.URL+"/ui/login/code", url.Values{"code": {code}, "name": {"philipp"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign-in: %d", resp.StatusCode)
	}
	acct, err := st.AccountByName("philipp")
	if err != nil {
		t.Fatal(err)
	}
	principal := acct.ID
	if st.OrgRole(org.ID, principal) != store.OrgMember || st.ProjectRole(joinProject, principal).Role != store.RoleMember {
		t.Fatalf("roles: org=%q project=%q", st.OrgRole(org.ID, principal), st.ProjectRole(joinProject, principal).Role)
	}
	if _, err := st.PreviewInvitation(code); err == nil {
		t.Fatal("the invitation stayed valid after use")
	}
}

func TestJoinOIDCSignInCreatesAccountWithProjectRole(t *testing.T) {
	env := newOIDCEnv(t, true)
	org, err := env.store.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.EnsureProject("person:1", joinProject); err != nil {
		t.Fatal(err)
	}
	code, _, err := env.store.CreateProjectInvitation("person:1", org.ID, joinProject, store.RoleGuest, 0)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := anonClient().Get(env.web.URL + "/join/" + code)
	if text := body(t, page); !strings.Contains(text, `action="/ui/login/oidc"`) {
		t.Fatalf("oidc page: %s", text)
	}
	env.idp.subject, env.idp.username, env.idp.email = "sub-philipp", "philipp", "philipp@example.test"
	b := newBrowser(t)
	resp := env.callback(t, b, env.startFlow(t, b, code))
	resp.Body.Close()
	if !env.signedIn(t, b) {
		t.Fatal("not signed in")
	}
	acct, err := env.store.AccountByName("philipp")
	if err != nil {
		t.Fatal(err)
	}
	principal := acct.ID
	if got := env.store.ProjectRole(joinProject, principal).Role; got != store.RoleGuest {
		t.Fatalf("project role %q", got)
	}
}

func TestOrgPageCreatesProjectInvitationLinks(t *testing.T) {
	base, st, alice, anna, org := orgWeb(t)
	_ = anna
	if _, err := st.EnsureProject("person:1", joinProject); err != nil {
		t.Fatal(err)
	}
	resp := postOrg(t, alice, base, "/ui/orgs/invite", url.Values{"org": {org.Slug}, "project": {joinProject}, "project_role": {"guest"}})
	page := body(t, resp)
	m := regexp.MustCompile(`/join/([0-9a-f]{64})`).FindStringSubmatch(page)
	if resp.StatusCode != http.StatusOK || m == nil {
		t.Fatalf("link not shown: %d %s", resp.StatusCode, page)
	}
	if _, err := st.PreviewInvitation(m[1]); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"owner", "lead", ""} {
		resp = postOrg(t, alice, base, "/ui/orgs/invite", url.Values{"org": {org.Slug}, "project": {joinProject}, "project_role": {role}})
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("a link granted role %q", role)
		}
	}
}
