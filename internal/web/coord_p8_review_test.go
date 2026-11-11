package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func threadStateOf(t *testing.T, st *store.Store, access store.CoordAccess, id int64) string {
	t.Helper()
	thread, err := access.Thread(id)
	if err != nil {
		t.Fatal(err)
	}
	return thread.State
}

// A reply that is refused must not leave the thread already changed: the state
// is set only after the post went through.
func TestCoordThreadStateChangesOnlyWhenTheReplyWentThrough(t *testing.T) {
	client, base, st, room := p8Fixture(t)
	access := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, "")
	threadID, err := access.CreateTaskThreadInRoom(room, "Thread", "", "")
	if err != nil {
		t.Fatal(err)
	}
	before := threadStateOf(t, st, access, threadID)
	for name, form := range map[string]url.Values{
		"invalid intent":   {"body": {"Done"}, "intent": {"nonsense"}},
		"invalid reply_to": {"body": {"Done"}, "reply_to": {"-4"}},
		"unknown reply_to": {"body": {"Done"}, "reply_to": {"999999"}},
	} {
		form.Set("thread_id", strconv.FormatInt(threadID, 10))
		form.Set("state", "resolved")
		form.Set("state_was", before)
		res := authenticatedPostForm(t, client, base+"/ui/coord/thread/post", form)
		res.Body.Close()
		if res.StatusCode < 400 {
			t.Errorf("%s: want a refusal, got %d", name, res.StatusCode)
		}
		if got := threadStateOf(t, st, access, threadID); got != before {
			t.Errorf("%s: the refused reply left the thread %q instead of %q", name, got, before)
		}
	}
	// With a valid reply the state follows.
	res := authenticatedPostForm(t, client, base+"/ui/coord/thread/post", url.Values{
		"thread_id": {strconv.FormatInt(threadID, 10)}, "body": {"Done"}, "state": {"resolved"}, "state_was": {before},
	})
	res.Body.Close()
	if res.StatusCode >= 400 || threadStateOf(t, st, access, threadID) != store.ThreadResolved {
		t.Errorf("a valid reply with a state change: %d, state %q", res.StatusCode, threadStateOf(t, st, access, threadID))
	}
}

// The thread composer has no required text: without script a bare status change
// must post. The room composer keeps its required field.
func TestCoordOnlyTheRoomComposerRequiresText(t *testing.T) {
	client, base, st, room := p8Fixture(t)
	anchor := p8Human(t, st, room, "anchor", "Anchor")
	access := st.CoordinationFor(store.Principal{ID: "person:1", Label: "robin"}, "")
	threadID, err := access.PromoteRoomMessageToTaskThread(anchor, "Thread", "", "")
	if err != nil {
		t.Fatal(err)
	}
	textarea := func(page, id string) string {
		return segment(t, page, `<textarea id="`+id+`"`, ">")
	}
	if tag := textarea(coordPageBody(t, client, base+coordThreadURL(room, threadID)), "coord-thread-body"); strings.Contains(tag, "required") {
		t.Errorf("the thread composer must not require text: %s", tag)
	}
	if tag := textarea(coordPageBody(t, client, base+"/ui/coord?room="+url.QueryEscape(room)), "coord-message-body"); !strings.Contains(tag, "required") {
		t.Errorf("the room composer requires text: %s", tag)
	}
}

// The anchor is shown above the thread, not in the stream of the room: the
// divider "New" of the stream must not follow it there. An anchor the viewer
// cannot read is no anchor at all.
func TestCoordThreadAnchorDropsTheDividerAndHidesWhatIsUnreadable(t *testing.T) {
	e := reviewFixture(t)
	id, err := e.annaAgent.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: e.room, SenderExternalID: "claude:anna", ClientID: "anc", Body: "The anchor"})
	if err != nil {
		t.Fatal(err)
	}
	a := &app{store: e.st}
	l := langEN
	r := httptest.NewRequest(http.MethodGet, "/ui/coord", nil)
	_, pres, err := e.aliceAcc.MessagePresentationWindow(store.DestinationRoom, e.room, store.LatestWindow(200))
	if err != nil {
		t.Fatal(err)
	}
	var seq int64
	for _, p := range pres {
		if p.Message.ID == id {
			seq = p.Message.Sequence
		}
	}
	if seq == 0 {
		t.Fatal("the anchor is not in the room")
	}
	thread := &coordThreadView{AnchorMessageID: id, AnchorSequence: seq}

	inWindow := &coordRoomDetailView{Messages: []coordMessageView{{ID: id, Sequence: seq, NewFrom: true}}}
	anchor := a.coordThreadAnchor(r, l, e.aliceAcc, e.room, thread, inWindow, nil, nil, nil, nil, false)
	if anchor == nil || anchor.NewFrom {
		t.Fatalf("the anchor must not carry the divider of the stream: %+v", anchor)
	}
	if !inWindow.Messages[0].NewFrom {
		t.Error("the message in the stream keeps its divider")
	}

	// Outside the window it is read again; whoever may not read the room gets nil.
	empty := &coordRoomDetailView{}
	stranger := e.st.CoordinationFor(store.Principal{ID: "person:99", Label: "stranger", TokenKind: store.WebSessionKind}, "")
	public := e.st.CoordinationPublicFor(store.Principal{ID: "person:99", Label: "stranger", TokenKind: store.WebSessionKind})
	for name, access := range map[string]store.CoordAccess{"stranger": stranger, "public reader": public} {
		if got := a.coordThreadAnchor(r, l, access, e.room, thread, empty, nil, nil, nil, nil, false); got != nil {
			t.Errorf("%s: an anchor that cannot be read must be nil, got %+v", name, got)
		}
	}
	guestKey := store.RoomKeyForProject("github.com/x/unknown")
	if got := a.coordThreadAnchor(r, l, e.gusAcc, guestKey, thread, empty, nil, nil, nil, nil, false); got != nil {
		t.Errorf("a guest asking for the anchor in another room: nil, got %+v", got)
	}
}

// One card carries the items of all its addressees. A foreign id in the list
// stops everything and answers like an id that does not exist; the list is
// capped, deduplicated, and an item someone else answered first is skipped.
func TestCoordAttentionActionIsAllOrNothingAndCapped(t *testing.T) {
	e := reviewFixture(t)
	// alice asks anna twice; anna's agent asks alice once.
	ask := func(acc store.CoordAccess, sender, client, to string) {
		t.Helper()
		if _, err := acc.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: e.room, SenderExternalID: sender, ClientID: client, Body: "Question " + client, Intent: store.IntentQuestion, Mentions: []string{to}}); err != nil {
			t.Fatal(err)
		}
	}
	ask(e.aliceAcc, "", "a1", "person:2")
	ask(e.aliceAcc, "", "a2", "person:2")
	ask(e.annaAgent, "claude:anna", "b1", "person:1")
	annaAcc := e.st.CoordinationFor(store.Principal{ID: "person:2", Label: "anna", TokenKind: store.WebSessionKind}, "")
	mine, err := annaAcc.Attention()
	if err != nil {
		t.Fatal(err)
	}
	var own []int64
	for _, item := range mine {
		if item.IsRecipient && item.State == store.AttentionOpen {
			own = append(own, item.ID)
		}
	}
	theirs, _ := e.aliceAcc.Attention()
	var foreign int64
	for _, item := range theirs {
		if item.IsRecipient && item.State == store.AttentionOpen {
			foreign = item.ID
		}
	}
	if len(own) < 2 || foreign == 0 {
		t.Fatalf("fixture: own %v foreign %d", own, foreign)
	}
	stateOf := func(acc store.CoordAccess, id int64) string {
		items, _ := acc.Attention()
		for _, item := range items {
			if item.ID == id {
				return item.State
			}
		}
		return "?"
	}
	ids := func(list ...int64) []string {
		out := make([]string, len(list))
		for i, id := range list {
			out[i] = strconv.FormatInt(id, 10)
		}
		return out
	}
	act := func(list []string) *http.Response {
		return e.post(t, e.anna, "/ui/coord/attention/action", url.Values{"attention_id": list, "action": {"dismiss"}})
	}

	// A foreign id among her own: nothing is resolved, and the answer is the one for an id that does not exist.
	res := act(ids(own[0], foreign, own[1]))
	text := body(t, res)
	missing := act(ids(own[0], 987654, own[1]))
	missingText := body(t, missing)
	if res.StatusCode != http.StatusNotFound || res.StatusCode != missing.StatusCode || text != missingText {
		t.Errorf("a foreign id must look like a missing one: %d %q vs %d %q", res.StatusCode, text, missing.StatusCode, missingText)
	}
	for _, id := range own[:2] {
		if got := stateOf(annaAcc, id); got != store.AttentionOpen {
			t.Errorf("item %d resolved although the list held a foreign id: %s", id, got)
		}
	}
	if got := stateOf(e.aliceAcc, foreign); got != store.AttentionOpen {
		t.Errorf("a foreign item was resolved: %s", got)
	}

	// Over the cap, nothing happens.
	many := make([]string, 0, 12)
	for i := 0; i < 11; i++ {
		many = append(many, strconv.Itoa(1000+i))
	}
	if res := act(append(many, ids(own[0])...)); res.StatusCode != http.StatusBadRequest {
		t.Errorf("more than %d ids: %d", maxAttentionActionIDs, res.StatusCode)
	}
	if got := stateOf(annaAcc, own[0]); got != store.AttentionOpen {
		t.Errorf("a refused list resolved an item: %s", got)
	}

	// Duplicates count once; one item already closed does not fail the other.
	if res := act(ids(own[1])); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("a single item: %d", res.StatusCode)
	}
	if res := act(ids(own[1])); res.StatusCode != http.StatusConflict {
		t.Errorf("a single closed item is still told as closed: %d", res.StatusCode)
	}
	if res := act(ids(own[0], own[0], own[1])); res.StatusCode != http.StatusSeeOther {
		t.Errorf("a closed item in a list is already done: %d", res.StatusCode)
	}
	if got := stateOf(annaAcc, own[0]); got != store.AttentionDismissed {
		t.Errorf("the open item of the list was not resolved: %s", got)
	}
}

// What a reply quotes is text of someone else: name and words are escaped.
func TestCoordReplyQuoteEscapesNameAndText(t *testing.T) {
	e := reviewFixture(t)
	if _, err := e.st.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:evil", PrincipalID: "person:2", Person: "anna", Provider: "claude", DisplayName: `<b id="pwn">evil</b>@box`, RoomKey: e.room, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	evil := e.st.CoordinationFor(store.Principal{ID: "person:2", Label: "anna"}, "claude:evil")
	sent, err := evil.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: e.room, SenderExternalID: "claude:evil", ClientID: "ev", Body: `<script>alert(1)</script> and <img src=x onerror=alert(2)>`})
	if err != nil {
		t.Fatal(err)
	}
	if res := e.post(t, e.alice, "/ui/coord/send", url.Values{"body": {"Noted"}, "reply_to": {strconv.FormatInt(sent, 10)}, "form_id": {"quote"}}); res.StatusCode >= 400 {
		t.Fatalf("reply: %d", res.StatusCode)
	}
	page := e.page(t, e.alice)
	quote := segment(t, page, `class="cm-quote"`, "</a>")
	for _, raw := range []string{"<script>", "<img src=x", `<b id="pwn">`} {
		if strings.Contains(quote, raw) {
			t.Errorf("the quote carries unescaped %q: %s", raw, quote)
		}
	}
	if !strings.Contains(quote, "&lt;script&gt;") {
		t.Errorf("the quoted words are shown escaped: %s", quote)
	}
	// The name comes from the label of the author: rendered on its own with markup in it.
	var sb strings.Builder
	view := coordMessageView{Reply: &coordReplyView{Sequence: 3, Author: `<b id="pwn">evil</b>`, Body: `<i>words</i>`, URL: "/x", Lang: langEN}}
	if err := langEN.page().ExecuteTemplate(&sb, "coord-quote", view); err != nil {
		t.Fatal(err)
	}
	if out := sb.String(); strings.Contains(out, `<b id="pwn">`) || strings.Contains(out, "<i>words") || !strings.Contains(out, "&lt;b id=") || !strings.Contains(out, "&lt;i&gt;words") {
		t.Errorf("name and words of a quote are escaped: %s", out)
	}
}
