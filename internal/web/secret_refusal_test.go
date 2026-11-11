package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A person who types a password into a form gets one sentence back, on the form,
// and nothing is stored; the text they typed is not written onto the page again
// (PAIN-17). The value is invented.
func TestASecretInAFormIsRefusedOnTheFormWithoutBeingWrittenBack(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Talk about it", Desc: "alt"})
	id := d.Request.ID
	leaked := "Hx7!kQ2pzWm"
	secretText := "Mail Passwort: " + leaked + " gilt"

	cases := []struct {
		name, path string
		form       url.Values
	}{
		{"comment", "/ui/requests/" + itoa(id) + "/close", url.Values{"mode": {"comment"}, "reason": {secretText}}},
		{"correction", "/ui/requests/" + itoa(id) + "/correct", url.Values{"title": {"Talk about it"}, "description": {secretText}, "type": {d.Request.Type}, "reason": {"genauer"}}},
	}
	for _, c := range cases {
		resp := tpPost(t, e, asBrowser(e.Owner), c.path, c.form)
		resp.Body.Close()
		code, page := followToForm(t, e.Owner, e.Base, resp)
		if code != http.StatusOK && code != http.StatusUnprocessableEntity {
			t.Errorf("%s: the form came back with %d", c.name, code)
		}
		if strings.Contains(page, leaked) {
			t.Errorf("%s: the page repeats the value", c.name)
		}
		if !strings.Contains(page, "contains a secret") {
			t.Errorf("%s: the page lacks the refusal", c.name)
		}
	}
	list, err := e.St.Access(store.Principal{ID: "person:1"}).Comments(store.CommentOnRequest, id)
	if err != nil || len(list) != 0 {
		t.Errorf("a refused comment was stored: %v %v", list, err)
	}
	if got, _ := e.St.RequestByID(id); got.Request.Description != "alt" {
		t.Errorf("a refused correction changed the request: %q", got.Request.Description)
	}
	// Harmless text goes through the same form.
	resp := tpPost(t, e, e.Owner, cases[0].path, url.Values{"mode": {"comment"}, "reason": {"Das Passwort liegt im Vault unter secrets/mail."}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "#comment-") {
		t.Errorf("harmless comment -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// followToForm is followRefusal for a form whose address is an old one that
// redirects once more to the page of the request; the browser takes the
// message cookie along.
func followToForm(t *testing.T, c *http.Client, base string, resp *http.Response) (int, string) {
	t.Helper()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || loc == "" {
		t.Fatalf("the answer %d is no redirect to the form", resp.StatusCode)
	}
	var flash *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == flashCookie {
			flash = ck
		}
	}
	for hops := 0; hops < 3; hops++ {
		req, err := http.NewRequest(http.MethodGet, base+loc, nil)
		if err != nil {
			t.Fatal(err)
		}
		if flash != nil {
			req.AddCookie(flash)
		}
		got, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		text := body(t, got)
		got.Body.Close()
		if next := got.Header.Get("Location"); got.StatusCode/100 == 3 && next != "" {
			loc = next
			continue
		}
		return got.StatusCode, text
	}
	t.Fatal("the form is behind too many redirects")
	return 0, ""
}
