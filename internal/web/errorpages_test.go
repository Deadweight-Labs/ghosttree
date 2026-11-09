package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestUnknownLinkAnswersWithAPageInTheShellAndSaysNothingAboutRights(t *testing.T) {
	e := ovEnv(t)
	status, page := fetchPage(t, e.Owner, e.Base+"/ui/knowledge/999999")
	if status != http.StatusNotFound {
		t.Fatalf("status %d", status)
	}
	for _, want := range []string{`class="gh-header"`, `class="errbox"`, `<p class="errcode">404</p>`, "This page does not exist", `href="/ui/knowledge"`, "ui-error.css"} {
		if !strings.Contains(page, want) {
			t.Errorf("404 page lacks %q", want)
		}
	}
	for _, leak := range []string{"permission", "allowed", "access", "rights", "open to you"} {
		if strings.Contains(strings.ToLower(visibleText(page)), leak) {
			t.Errorf("the 404 says something about rights: %q", leak)
		}
	}
	// A room speaks of the room and leads back to the rooms.
	status, room := fetchPage(t, e.Owner, e.Base+"/ui/coord?room=project%3Agithub.com%2Fnobody%2Fnothing")
	if status != http.StatusNotFound && status != http.StatusForbidden {
		t.Fatalf("room status %d", status)
	}
	if !strings.Contains(room, `href="/ui/coord"`) {
		t.Error("the room error does not lead back to the rooms")
	}
}

func TestAPostOfTheWrongKindOrAnApiPathIsNotTurnedIntoAPage(t *testing.T) {
	e := ovEnv(t)
	resp, err := e.Owner.Get(e.Base + "/static/nothere.css")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Errorf("a missing static file answers %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}
