package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestProfilePageShowsTheNameAndChangesIt(t *testing.T) {
	srv, st, _ := testWeb(t)
	c := loginInteractive(t, srv, st, "alice")
	resp, _ := c.Get(srv.URL + "/ui/profile")
	page := body(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(page, `value="alice"`) || !strings.Contains(page, `name="name"`) {
		t.Fatalf("profile page %d: %s", resp.StatusCode, page)
	}
	form := url.Values{"name": {"  Alice   Wonder "}, "csrf_token": {renderedCSRFToken(t, c, srv.URL+"/ui/profile")}}
	resp = sameOriginPostForm(t, c, srv.URL+"/ui/profile", form)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/profile?notice=saved" {
		t.Fatalf("post: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if a, _ := st.AccountByPrincipalID("person:1"); a.Name != "Alice Wonder" {
		t.Fatalf("stored %q", a.Name)
	}
	resp, _ = c.Get(srv.URL + "/ui/profile?notice=saved")
	page = body(t, resp)
	if !strings.Contains(page, `value="Alice Wonder"`) || !strings.Contains(page, `role="status"`) {
		t.Fatalf("after save: %s", page)
	}
	// The shell shows the new name everywhere the account chip is rendered.
	resp, _ = c.Get(srv.URL + "/ui/overview")
	if !strings.Contains(body(t, resp), "Alice Wonder") {
		t.Fatal("account chip shows the old name")
	}
}

func TestProfileRejectsBadNamesAndTakenOnes(t *testing.T) {
	srv, st, _ := testWeb(t)
	if _, err := st.AddPerson("bob"); err != nil {
		t.Fatal(err)
	}
	c := loginInteractive(t, srv, st, "alice")
	csrf := renderedCSRFToken(t, c, srv.URL+"/ui/profile")
	for name, want := range map[string]int{
		"":                                http.StatusBadRequest,
		"...":                             http.StatusBadRequest,
		strings.Repeat("a", 65):           http.StatusBadRequest,
		"BOB":                             http.StatusConflict,
		"Alice <script>alert(1)</script>": http.StatusSeeOther, // cleaned, never echoed raw
	} {
		resp := sameOriginPostForm(t, c, srv.URL+"/ui/profile", url.Values{"name": {name}, "csrf_token": {csrf}})
		page := body(t, resp)
		if resp.StatusCode != want {
			t.Errorf("%q: status %d, want %d", name, resp.StatusCode, want)
		}
		if strings.Contains(page, "<script>alert") {
			t.Errorf("%q echoed raw", name)
		}
	}
	if a, _ := st.AccountByPrincipalID("person:1"); strings.ContainsAny(a.Name, "<>") {
		t.Fatalf("stored %q", a.Name)
	}
}

func TestProfileNeedsCSRFOriginAndAnInteractiveSession(t *testing.T) {
	srv, st, token := testWeb(t)
	c := loginInteractive(t, srv, st, "alice")
	good := renderedCSRFToken(t, c, srv.URL+"/ui/profile")
	for name, tok := range map[string]string{"missing": "", "wrong": "nope"} {
		resp := sameOriginPostForm(t, c, srv.URL+"/ui/profile", url.Values{"name": {"X"}, "csrf_token": {tok}})
		if resp.StatusCode == http.StatusSeeOther {
			t.Errorf("%s csrf accepted", name)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/ui/profile", strings.NewReader(url.Values{"name": {"X"}, "csrf_token": {good}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	if resp, err := c.Do(req); err != nil || resp.StatusCode == http.StatusSeeOther {
		t.Fatalf("foreign origin accepted: %v %v", resp, err)
	}
	// A pasted token is a read-only session: the page shows, the form does not post.
	paste := login(t, srv, token)
	resp, _ := paste.Get(srv.URL + "/ui/profile")
	if page := body(t, resp); strings.Contains(page, `name="name"`) {
		t.Fatalf("read-only session sees the form: %s", page)
	}
	resp = sameOriginPostForm(t, paste, srv.URL+"/ui/profile", url.Values{"name": {"X"}, "csrf_token": {renderedCSRFToken(t, paste, srv.URL+"/ui/overview")}})
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatal("token session changed the name")
	}
	if a, _ := st.AccountByPrincipalID("person:1"); a.Name != "alice" {
		t.Fatalf("name changed: %q", a.Name)
	}
}
