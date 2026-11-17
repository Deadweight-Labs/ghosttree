package web

import (
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-677 AC-1 (P10): a display name stays as it was typed. The profile saves it,
// and the header, the profile and the member list show it as text.
func TestDisplayNameWithBracketsStaysAsTypedEverywhere(t *testing.T) {
	e := ovEnv(t)
	const name = "Mara Keller (Tech-Lead) *Ü_x*"
	resp := postForm(t, e, e.Member, "/ui/profile", url.Values{"name": {name}})
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "notice=saved") {
		t.Fatalf("saving the name -> %d %q (the name was cleaned, it must not be)", resp.StatusCode, resp.Header.Get("Location"))
	}
	acct, err := e.St.AccountByName(name)
	if err != nil || acct.Name != name {
		t.Fatalf("stored name = %q, %v", acct.Name, err)
	}
	for _, where := range []string{"/ui/profile", "/ui/x/shell/issues"} {
		page := fetchBody(t, e.Member, e.Base+where)
		if !strings.Contains(page, html.EscapeString(name)) && !strings.Contains(page, strings.ReplaceAll(html.EscapeString(name), "&#39;", "'")) {
			t.Errorf("%s does not show %q as written", where, name)
		}
		if strings.Contains(page, "_Tech-Lead_") {
			t.Errorf("%s still shows the underscore form", where)
		}
	}
}

// REQ-677 AC-3: a mention in a comment reaches the person in the inbox under what
// was said to them; a name that reaches nobody is told to the sender.
func TestMentionInACommentArrivesInTheInboxAndTheSenderLearnsWhoWasMissed(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Spool begrenzen"})
	id := d.Request.ID
	resp := postForm(t, e, e.Member, "/ui/requests/"+itoa(id)+"/close", url.Values{"mode": {"comment"},
		"reason": {"Hey @lars, ist das für dich? Und @nobody noch."}})
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(loc, "unreached=nobody") || strings.Contains(loc, "lars") {
		t.Fatalf("comment -> %d %q: only the name that reached nobody is told", resp.StatusCode, loc)
	}
	_, after := fetchPage(t, e.Member, e.Base+loc)
	if !strings.Contains(after, "@nobody reached no one") {
		t.Errorf("the sender is not told: %.400s", after)
	}
	inbox := fetchBody(t, e.Lead, e.Base+"/ui/inbox")
	if !strings.Contains(inbox, "Hey @lars, ist das für dich?") || !strings.Contains(inbox, "anna") {
		t.Fatalf("lars finds nothing in the inbox:\n%.1500s", inbox)
	}
	if !strings.Contains(inbox, `id="mentioned"`) || !strings.Contains(inbox, `/ui/inbox/mention-read`) {
		t.Error("the mention is not in the section of what was said, or cannot be marked read")
	}
	if strings.Contains(fetchBody(t, e.Reviewer, e.Base+"/ui/inbox"), "ist das für dich") {
		t.Error("somebody else's mention is in another inbox")
	}
	// Reading it takes it out.
	list, _ := e.St.Access(store.Principal{ID: "person:4", Label: "lars"}).TextMentions(shellProject)
	if len(list) != 1 {
		t.Fatalf("mentions of lars: %+v", list)
	}
	if r := postForm(t, e, e.Lead, "/ui/inbox/mention-read", url.Values{"id": {itoa(list[0].ID)}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("mark read -> %d", r.StatusCode)
	}
	if strings.Contains(fetchBody(t, e.Lead, e.Base+"/ui/inbox"), "ist das für dich") {
		t.Error("a mention that was read is still in the inbox")
	}
}

// A mention in the text of a new issue reaches the person, and a name of somebody
// with no access to the project reaches nobody: the sender is told.
func TestMentionInANewIssueReachesThePersonWithAccessOnly(t *testing.T) {
	e := ovEnv(t)
	if _, err := e.St.AddPerson("olga"); err != nil {
		t.Fatal(err)
	}
	resp := postForm(t, e, e.Owner, "/ui/issues/create", url.Values{"project": {shellProject}, "kind": {"issue"}, "title": {"Neues Thema"},
		"body": {"@lars schau bitte drauf, auch @olga"}})
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || !strings.Contains(loc, "unreached=olga") {
		t.Fatalf("create issue -> %d %q: olga has no access and the sender must hear it", resp.StatusCode, loc)
	}
	if !strings.Contains(fetchBody(t, e.Lead, e.Base+"/ui/inbox"), "@lars schau bitte drauf") {
		t.Error("lars is not told about the issue that names him")
	}
}

type mentionAnswer struct {
	Options []store.MentionOption        `json:"options"`
	Names   map[string]store.MentionName `json:"names"`
}

func mentionsOf(t *testing.T, e shellEnv, c *http.Client, query string) mentionAnswer {
	t.Helper()
	resp, err := c.Get(e.Base + "/ui/mentions?project=" + url.QueryEscape(shellProject) + query)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out mentionAnswer
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// REQ-677 AC-2: the editor is offered the people of the project, a handle names
// the person with the full name for the pill, and a viewer who may not see the
// members gets nothing.
func TestMentionsEndpointOffersTheProjectAndNamesHandles(t *testing.T) {
	e := ovEnv(t)
	if _, err := e.St.SetOwnName("person:4", "Lars Berg (Tech-Lead)"); err != nil {
		t.Fatal(err)
	}
	got := mentionsOf(t, e, e.Member, "&options=1&h=lars&h=nobody")
	handles := map[string]string{}
	for _, o := range got.Options {
		handles[o.Handle] = o.Label
	}
	if handles["lars"] != "Lars Berg (Tech-Lead)" {
		t.Fatalf("options = %+v", got.Options)
	}
	if n := got.Names["lars"]; n.Name != "Lars Berg (Tech-Lead)" {
		t.Fatalf("names = %+v", got.Names)
	}
	if _, ok := got.Names["nobody"]; ok {
		t.Error("a handle that fits nobody is named")
	}
	guest := mentionsOf(t, e, e.Guest, "&options=1&h=lars")
	if len(guest.Options) != 0 || len(guest.Names) != 0 {
		t.Errorf("a guest must not read the member list through it: %+v", guest)
	}
	if other := mentionsOf(t, e, e.Member, "&options=1"); len(other.Options) == 0 {
		t.Error("no options")
	}
}

// The pages carry what the script needs: the project the names are read in, on a
// repository page and on the room of a project, and the script itself.
func TestPagesTellTheScriptWhichProjectTheirMentionsBelongTo(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Spool begrenzen"})
	page := fetchBody(t, e.Member, e.Base+"/ui/x/shell/issues/"+itoa(d.Request.ID))
	if !strings.Contains(page, `data-mention-repo="`+shellProject+`"`) {
		t.Error("an issue page does not name its project for the mentions script")
	}
	if !strings.Contains(page, `src="/static/ui-mentions.js`) && !strings.Contains(page, "bundle-base.js") {
		t.Error("the mentions script is not on the page")
	}
	room := fetchBody(t, e.Member, e.Base+"/ui/coord?room="+url.QueryEscape(store.RoomKeyForProject(shellProject)))
	if !strings.Contains(room, `data-mention-repo="`+shellProject+`"`) {
		t.Errorf("the room of a project does not name it: %.200s", room)
	}
}
