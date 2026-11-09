package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestOrgPageHasTheBandAndEveryTabAnswers(t *testing.T) {
	e := ovEnv(t)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/alpha")
	for _, want := range []string{`class="rband`, `data-tab="repos"`, `data-tab="agents"`, `data-tab="people"`, `data-tab="rooms"`, `data-tab="settings"`, `href="/ui/alpha/agents"`, "Knowledge "} {
		if !strings.Contains(page, want) {
			t.Errorf("org page lacks %q", want)
		}
	}
	for _, tab := range []string{"agents", "people", "rooms"} {
		if status, p := fetchPage(t, e.Owner, e.Base+"/ui/alpha/"+tab); status != http.StatusOK || !strings.Contains(p, `aria-current="page"`) {
			t.Errorf("tab %s: status %d", tab, status)
		}
	}
	// Settings are the owner's; they lead to the administration page.
	resp, err := e.Owner.Get(e.Base + "/ui/alpha/settings")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/orgs?org=alpha" {
		t.Errorf("settings -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if status, _ := fetchPage(t, e.Member, e.Base+"/ui/alpha/settings"); status != http.StatusNotFound {
		t.Errorf("a member opens the settings: %d", status)
	}
	// Somebody else's organisation is as unknown as one that does not exist.
	if status, _ := fetchPage(t, e.Owner, e.Base+"/ui/nothere/people"); status != http.StatusNotFound {
		t.Errorf("an unknown organisation answers %d", status)
	}
}
