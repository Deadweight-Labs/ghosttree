package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// sendFollow posts a message as c and returns the redirect target with the page
// it leads to.
func sendFollow(t *testing.T, e shellEnv, c *http.Client, room, text string) (*url.URL, string) {
	t.Helper()
	roomURL := e.Base + "/ui/coord?room=" + url.QueryEscape(room)
	form := url.Values{"room": {room}, "body": {text}, "csrf_token": {renderedCSRFToken(t, c, roomURL)}}
	resp := sameOriginPostForm(t, c, e.Base+"/ui/coord/send", form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("send: %d", resp.StatusCode)
	}
	target, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, c, e.Base+target.RequestURI())
	return target, page
}

// A person who is in the project but has no agent in the room is reached by an
// @name: they read it as a mention and the sender reads who was mentioned.
func TestMentionOfAProjectMemberWithoutAgentIsMarkedForThem(t *testing.T) {
	e := ovEnv(t)
	room, _ := memberReadSeed(t, e)
	roomURL := e.Base + "/ui/coord?room=" + url.QueryEscape(room)
	target, senderPage := sendFollow(t, e, e.Member, room, "@lars could you look at this")
	if target.Query().Get("unreached") != "" {
		t.Errorf("a reached name is reported as unreached: %s", target)
	}
	if !strings.Contains(senderPage, "Mentioned: lars") || strings.Contains(senderPage, "reached no one") {
		t.Errorf("the sender does not read who was mentioned: %s", senderPage)
	}
	_, leadPage := fetchPage(t, e.Lead, roomURL)
	if !strings.Contains(leadPage, "could you look at this") || !strings.Contains(leadPage, `<span class="coord-mention-you">mentions you</span>`) {
		t.Errorf("the mentioned person does not see a mention: %s", leadPage)
	}
	// Somebody else in the room is not marked.
	_, ownerPage := fetchPage(t, e.Owner, roomURL)
	if strings.Contains(ownerPage, "coord-mention-you") {
		t.Errorf("the mention marks someone it does not address")
	}
}

// A name that reaches no one gets a quiet line under the room; a name that
// reaches someone, and a guest, get none.
func TestUnreachedMentionShowsAQuietNote(t *testing.T) {
	e := ovEnv(t)
	room, _ := memberReadSeed(t, e)
	roomURL := e.Base + "/ui/coord?room=" + url.QueryEscape(room)
	target, page := sendFollow(t, e, e.Member, room, "@nobody-here and @lars")
	if got := target.Query().Get("unreached"); got != "nobody-here" {
		t.Fatalf("unreached query %q", got)
	}
	if !strings.Contains(page, `<p class="coord-note" role="status">@nobody-here reached no one</p>`) {
		t.Errorf("no note: %s", page)
	}
	// The note is not part of the room: a reload without the query shows none.
	if _, again := fetchPage(t, e.Member, roomURL); strings.Contains(again, "reached no one") {
		t.Errorf("the note stays")
	}
	// Only well-formed names are echoed from the query.
	_, forged := fetchPage(t, e.Member, roomURL+"&unreached="+url.QueryEscape("<script>x</script>,ok"))
	if strings.Contains(forged, "<script>x") || !strings.Contains(forged, "@ok reached no one") {
		t.Errorf("forged hint: %s", forged)
	}
}
