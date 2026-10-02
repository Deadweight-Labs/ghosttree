package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const reviewProject = "github.com/x/review"

type reviewEnv struct {
	srv                *httptest.Server
	st                 *store.Store
	room               string
	alice, anna, gus   *http.Client
	aliceAcc, gusAcc   store.CoordAccess
	annaAgent, annaAcc store.CoordAccess
}

// reviewFixture: alice (person:1) owns the project, anna (2) is a member with
// an agent, gus (3) is a guest.
func reviewFixture(t *testing.T) reviewEnv {
	t.Helper()
	srv, st, _ := testWeb(t)
	for _, name := range []string{"anna", "gus"} {
		if _, err := st.AddPerson(name); err != nil {
			t.Fatal(err)
		}
	}
	org, err := st.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"person:2", "person:3"} {
		code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
		if _, err := st.AcceptInvitation(who, code); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.ClaimProject("person:1", reviewProject, "alpha"); err != nil {
		t.Fatal(err)
	}
	for who, role := range map[string]string{"person:2": store.RoleMember, "person:3": store.RoleGuest} {
		if err := st.SetProjectRole("person:1", reviewProject, who, role, false, store.RoleViaWeb); err != nil {
			t.Fatal(err)
		}
	}
	room := store.RoomKeyForProject(reviewProject)
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:anna", PrincipalID: "person:2", Person: "anna", Provider: "claude", DisplayName: "ask@box", RoomKey: room, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	materializeWebRoomFor(t, st, room, "person:1", "alice")
	materializeWebRoomFor(t, st, room, "person:3", "gus")
	st.SetAccessMode(store.AccessMode{Enforce: true})
	web := func(id, label string) store.CoordAccess {
		return st.CoordinationFor(store.Principal{ID: id, Label: label, TokenKind: store.WebSessionKind}, "")
	}
	return reviewEnv{
		srv: srv, st: st, room: room,
		alice: loginInteractive(t, srv, st, "alice"), anna: loginInteractive(t, srv, st, "anna"), gus: loginInteractive(t, srv, st, "gus"),
		aliceAcc: web("person:1", "alice"), gusAcc: web("person:3", "gus"),
		annaAgent: st.CoordinationFor(store.Principal{ID: "person:2", Label: "anna"}, "claude:anna"),
	}
}

func (e reviewEnv) page(t *testing.T, c *http.Client) string {
	t.Helper()
	return coordPageBody(t, c, e.srv.URL+"/ui/coord?room="+url.QueryEscape(e.room))
}

func (e reviewEnv) post(t *testing.T, c *http.Client, path string, form url.Values) *http.Response {
	t.Helper()
	form.Set("csrf_token", renderedCSRFToken(t, c, e.srv.URL+"/ui/coord?room="+url.QueryEscape(e.room)))
	form.Set("room", e.room)
	return sameOriginPostForm(t, c, e.srv.URL+path, form)
}

func segment(t *testing.T, s, from, to string) string {
	t.Helper()
	i := strings.Index(s, from)
	if i < 0 {
		t.Fatalf("%q not found", from)
	}
	s = s[i:]
	j := strings.Index(s, to)
	if j < 0 {
		t.Fatalf("%q not found after %q", to, from)
	}
	return s[:j]
}

func TestCoordGuestCannotEndAnOwnersDirectiveAndIsNotOfferedEnd(t *testing.T) {
	e := reviewFixture(t)
	if _, err := e.aliceAcc.CreateStanding(store.StandingInput{RoomKey: e.room, ClientID: "s1", Body: "Never push to main."}); err != nil {
		t.Fatal(err)
	}
	list, _ := e.st.StandingInstructions(e.room)
	id := list[0].MessageID

	guestPage := e.page(t, e.gus)
	if !strings.Contains(guestPage, "Never push to main.") {
		t.Fatal("the guest reads the directive")
	}
	if strings.Contains(guestPage, `action="/ui/coord/standing/end"`) {
		t.Error("a guest is not offered End on an owner's directive")
	}
	if page := e.page(t, e.alice); strings.Count(page, `action="/ui/coord/standing/end"`) < 2 {
		t.Error("the owner is offered End on the plate and in the list")
	}
	if res := e.post(t, e.gus, "/ui/coord/standing/end", url.Values{"message_id": {id}}); res.StatusCode != http.StatusForbidden {
		t.Fatalf("guest ending an owner directive: %d", res.StatusCode)
	}
	if list, _ := e.st.StandingInstructions(e.room); len(list) != 1 {
		t.Fatal("the directive must still be in force")
	}
	if res := e.post(t, e.alice, "/ui/coord/standing/end", url.Values{"message_id": {id}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("owner ending: %d", res.StatusCode)
	}
	if list, _ := e.st.StandingInstructions(e.room); len(list) != 0 {
		t.Fatal("the owner ended it")
	}
}

func TestCoordDirectivePlateNamesTheRoleAndLowRankIsAStandingRequest(t *testing.T) {
	e := reviewFixture(t)
	if _, err := e.aliceAcc.CreateStanding(store.StandingInput{RoomKey: e.room, ClientID: "s1", Body: "Owner rule."}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.gusAcc.CreateStanding(store.StandingInput{RoomKey: e.room, ClientID: "s2", Body: "Guest wish."}); err != nil {
		t.Fatal(err)
	}
	page := e.page(t, e.alice)
	plate := func(body string) string {
		t.Helper()
		start := strings.LastIndex(page[:strings.Index(page, body)], `<article class="slab`)
		return page[start : start+strings.Index(page[start:], "</article>")]
	}
	owner, guest := plate("Owner rule."), plate("Guest wish.")
	if !strings.Contains(owner, `<span class="cm-role">owner</span>`) || !strings.Contains(owner, "<span>Directive</span>") {
		t.Errorf("owner plate: %s", owner)
	}
	if !strings.Contains(guest, `<span class="cm-role">guest</span>`) || !strings.Contains(guest, "Standing request") || strings.Contains(guest, "<span>Directive</span>") {
		t.Errorf("a guest's standing instruction is no directive: %s", guest)
	}
	if g := e.page(t, e.gus); strings.Contains(g, "Never") || !strings.Contains(g, "Owner rule.") || strings.Contains(plateOf(t, g, "Owner rule."), "Standing request") {
		t.Errorf("a guest, who is not told the author's role, must not see an owner's directive downgraded: %s", plateOf(t, g, "Owner rule."))
	}
	modes := func(page string) string { return segment(t, page, `class="pill clay"`, `</div>`) }
	if m := modes(page); !strings.Contains(m, "Directive") || strings.Contains(m, "Standing request") {
		t.Errorf("the owner can set directives: %s", m)
	}
	if m := modes(e.page(t, e.gus)); !strings.Contains(m, "Standing request") || strings.Contains(m, "Directive") {
		t.Errorf("a guest can only set standing requests: %s", m)
	}
}

func TestCoordRequestCardNamesTheSenderRoleAndKeepsItsStateAfterAnswering(t *testing.T) {
	e := reviewFixture(t)
	if _, err := e.annaAgent.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: e.room, SenderExternalID: "claude:anna", ClientID: "q1", Body: "Roles first?", Intent: store.IntentQuestion, Mentions: []string{"person:1"}}); err != nil {
		t.Fatal(err)
	}
	card := func() string {
		page := e.page(t, e.alice)
		start := strings.LastIndex(page[:strings.Index(page, "Roles first?")], `<article class="ask clay">`)
		return page[start : start+strings.Index(page[start:], "</article>")]
	}
	if c := card(); !strings.Contains(c, `<span class="cm-role">member</span>`) {
		t.Errorf("request card lacks the sender role: %s", c)
	}
	items, _ := e.aliceAcc.Attention()
	if res := e.post(t, e.alice, "/ui/coord/attention/action", url.Values{"attention_id": {strconv.FormatInt(items[0].ID, 10)}, "action": {"answer"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("answer: %d", res.StatusCode)
	}
	if c := card(); !strings.Contains(c, `class="ask-state"`) || !regexp.MustCompile(`Answered · \w+`).MatchString(c) {
		t.Errorf("an answered card shows its state: %s", c)
	}
}

func TestCoordComposerAsksForTheAddresseeInlineAndKeepsMoreNextToSend(t *testing.T) {
	e := reviewFixture(t)
	page := e.page(t, e.alice)
	composer := segment(t, page, `class="coord-composer"`, "</form>")
	to := segment(t, composer, `class="comp-to"`, "</div>")
	if !strings.Contains(to, `type="radio" name="mentions"`) || !strings.Contains(to, "ask@box") {
		t.Errorf("request mode needs inline addressee chips: %s", to)
	}
	if strings.Contains(composer, `class="coord-text-action">More`) {
		t.Error("More is no text link below the composer")
	}
	more := segment(t, composer, `<details class="comp-more`, "</summary>")
	if !strings.Contains(more, `aria-label="More"`) || !strings.Contains(more, "<svg") {
		t.Errorf("More is an icon button: %s", more)
	}
	if !strings.Contains(composer, `<div class="comp">`) || strings.Index(composer, `<details class="comp-more`) < strings.Index(composer, `<div class="comp">`) || strings.Index(composer, `<details class="comp-more`) > strings.Index(composer, "comp-send-message") {
		t.Errorf("More sits in the comp row before Send: %s", composer)
	}
}

func TestCoordNoJSRefreshLinkKeepsTheActiveRoom(t *testing.T) {
	e := reviewFixture(t)
	page := e.page(t, e.alice)
	note := segment(t, page, "<noscript><p class=\"coord-live-status coord-nojs-status\">", "</noscript>")
	want := `<a href="/ui/coord?room=` + url.QueryEscape(e.room) + `">Refresh</a>`
	if !strings.Contains(note, want) {
		t.Errorf("no-JS refresh must keep the room, want %s in %s", want, note)
	}
}

func TestCoordFocusAndTouchTargetsOfTheRoomMarkup(t *testing.T) {
	css := string(mustReadEmbedded(t, "static/app.css"))
	for _, sel := range []string{".coord-workspace :focus-visible", ".coord-chip input:focus-visible + span", ".pill input:focus-visible + span", ".comp-more > summary:focus-visible"} {
		if rule := coordCSSRule(t, css, sel); !strings.Contains(rule, "outline: 2px solid") {
			t.Errorf("%s needs a 2px focus ring: %s", sel, rule)
		}
	}
	coarse := css[strings.Index(css, "@media (hover: none), (pointer: coarse)"):]
	rule := coordCSSRule(t, coarse, ".coord-workspace button,")
	for _, want := range []string{"min-height: 44px", "min-width: 44px"} {
		if !strings.Contains(rule, want) {
			t.Errorf("coarse pointer targets lack %q: %s", want, rule)
		}
	}
	from := strings.Index(coarse, ".coord-workspace button,")
	head := coarse[from : from+strings.Index(coarse[from:], "{")]
	for _, sel := range []string{".comp-more > summary", ".pill span", ".coord-chip span", ".slab-end", ".coord-text-action", ".acts .btn"} {
		if !strings.Contains(head, sel) {
			t.Errorf("coarse selector list lacks %s", sel)
		}
	}
	if rule := coordCSSRule(t, css, ".comp-more > summary"); !strings.Contains(rule, "52px") {
		t.Errorf("More matches the send button: %s", rule)
	}
}

func TestCoordRightColumnFadesOutAtTheBottomWithinTheShadowPadding(t *testing.T) {
	css := string(mustReadEmbedded(t, "static/app.css"))
	rule := coordCSSRule(t, css, "\n.coord-context {")
	for _, want := range []string{"scrollbar-width: thin", "mask-image: linear-gradient", "var(--shadow-room-y)"} {
		if !strings.Contains(rule, want) {
			t.Errorf("right column lacks %q: %s", want, rule)
		}
	}
}

func TestCoordAppJSOnlyUsesAFormactionTheButtonDeclares(t *testing.T) {
	js := string(mustReadEmbedded(t, "static/app.js"))
	if strings.Contains(js, "submitter?.formAction ||") {
		t.Error("formAction falls back to the document URL without the attribute and defeats the interception")
	}
	if !strings.Contains(js, `submitter?.hasAttribute("formaction") ? submitter.formAction : form.action`) {
		t.Error("a submitter only overrides the form target when it declares formaction")
	}
}

func plateOf(t *testing.T, page, body string) string {
	t.Helper()
	start := strings.LastIndex(page[:strings.Index(page, body)], `<article class="slab`)
	return page[start : start+strings.Index(page[start:], "</article>")]
}
