package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// reactPost posts to /ui/reactions as client c and returns status, Location and body.
func reactPost(t *testing.T, e shellEnv, c *http.Client, form url.Values) (int, string, string) {
	t.Helper()
	form.Set("csrf_token", renderedCSRFToken(t, c, e.Base+"/ui/overview"))
	resp := sameOriginPostForm(t, c, e.Base+"/ui/reactions", form)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Location"), string(raw)
}

func TestReactionClickSwitchesAndTheFragmentIsTheRowAgain(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "React to me", Criteria: []string{"One"}, Desc: "A text."})
	form := func(key string) url.Values {
		return url.Values{"kind": {"request"}, "id": {itoa(d.Request.ID)}, "reaction": {key}}
	}

	// Without script: the form says next and goes back to the page of the issue.
	next := form("+1")
	next.Set("next", "1")
	status, loc, _ := reactPost(t, e, e.Member, next)
	if status != http.StatusSeeOther || loc != "/ui/x/shell/issues/"+itoa(d.Request.ID) {
		t.Fatalf("form without script -> %d to %q", status, loc)
	}
	// With script: no next, the answer is the row, with my reaction blue.
	status, _, row := reactPost(t, e, e.Owner, form("+1"))
	if status != http.StatusOK || !strings.Contains(row, `class="react on" aria-pressed="true"`) || !strings.Contains(row, `</span>2</button>`) {
		t.Fatalf("second person -> %d %s", status, row)
	}
	if !strings.Contains(row, `data-reacts`) || !strings.Contains(row, `class="rpick"`) {
		t.Errorf("the fragment is the whole row with its picker: %s", row)
	}
	// The tooltip names the people; "Du" comes first for the viewer.
	if !strings.Contains(row, `title="Reacted with 👍: You, `) {
		t.Errorf("tooltip lacks the names: %s", row)
	}
	// A second click takes my reaction back; the other one stays.
	status, _, row = reactPost(t, e, e.Owner, form("+1"))
	if status != http.StatusOK || strings.Contains(row, `class="react on"`) || !strings.Contains(row, `</span>1</button>`) {
		t.Fatalf("click again -> %d %s", status, row)
	}
	// The last one away: the row has no pill, only the add button.
	if _, _, row = reactPost(t, e, e.Member, form("+1")); strings.Contains(row, `class="react"`) || !strings.Contains(row, `class="react add"`) {
		t.Errorf("an empty row keeps the add button only: %s", row)
	}
	// All eight are in the picker, in the order of the list, with their names.
	for _, want := range []string{"Thumbs up", "Thumbs down", "Laugh", "Hooray", "Confused", "Heart", "Rocket", "Eyes"} {
		if !strings.Contains(row, `title="`+want+`"`) {
			t.Errorf("picker lacks %q", want)
		}
	}
	if n := strings.Count(row, `class="rc`); n != 8 {
		t.Errorf("picker has %d choices, want 8", n)
	}
}

func TestReactionsShowOnIssueIdeaAndCommentAndAGuestSeesNumbersOnly(t *testing.T) {
	e, pa := listEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Talked about", Criteria: []string{"One"}, Desc: "A text."})
	i, err := pa.CreateIdea(store.CreateIdeaInput{Project: shellProject, Title: "An idea", Body: "Wish."})
	if err != nil {
		t.Fatal(err)
	}
	c, err := pa.AddComment(store.CommentOnRequest, d.Request.ID, "A comment", store.CommentActor{Person: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		kind, key string
		id        int64
	}{{store.ReactOnRequest, "heart", d.Request.ID}, {store.ReactOnIdea, "rocket", i.ID}, {store.ReactOnComment, "eyes", c.ID}} {
		if _, err := pa.React(r.kind, r.id, r.key); err != nil {
			t.Fatalf("react %s: %v", r.kind, err)
		}
	}
	for _, p := range []struct{ path, emoji string }{{"/ui/x/shell/issues/" + itoa(d.Request.ID), "❤️"}, {"/ui/x/shell/ideas/" + itoa(i.ID), "🚀"}, {"/ui/x/shell/issues/" + itoa(d.Request.ID), "👀"}} {
		_, page := fetchPage(t, e.Member, e.Base+p.path)
		if !strings.Contains(page, `<span class="em">`+p.emoji+`</span>1`) || !strings.Contains(page, `class="react add"`) {
			t.Errorf("%s shows no %s with the add button for a member", p.path, p.emoji)
		}
		_, guest := fetchPage(t, e.Guest, e.Base+p.path)
		if !strings.Contains(guest, `<span class="em">`+p.emoji+`</span>1`) {
			t.Errorf("%s: a guest does not see the numbers", p.path)
		}
		if strings.Contains(guest, `name="reaction"`) || strings.Contains(guest, `class="react add"`) {
			t.Errorf("%s: a guest gets buttons", p.path)
		}
	}
	// An issue or comment nobody reacted to shows the add button to those who may react, nothing to a guest.
	bare := rqMake(t, e.St, rqSpec{Title: "Quiet", Criteria: []string{"One"}, Desc: "Text."})
	_, member := fetchPage(t, e.Member, e.Base+"/ui/x/shell/issues/"+itoa(bare.Request.ID))
	_, guest := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/issues/"+itoa(bare.Request.ID))
	if !strings.Contains(member, `class="react add"`) || strings.Contains(guest, `data-reacts`) {
		t.Error("an issue without reactions: add button for a member, no row for a guest")
	}
}

func TestReactionRightsAndInput(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Open one", Criteria: []string{"One"}})
	hidden := rqMake(t, e.St, rqSpec{Title: "Hidden", Criteria: []string{"One"}, Project: shellHiddenProject})
	ok := url.Values{"kind": {"request"}, "id": {itoa(d.Request.ID)}, "reaction": {"hooray"}}
	with := func(over map[string]string) url.Values {
		v := url.Values{}
		for k, vals := range ok {
			v[k] = append([]string{}, vals...)
		}
		for k, val := range over {
			v.Set(k, val)
		}
		return v
	}
	cases := []struct {
		name   string
		c      *http.Client
		form   url.Values
		status int
	}{
		{"a guest may read, not react", e.Guest, ok, http.StatusForbidden},
		{"a member reacts", e.Member, ok, http.StatusOK},
		{"an issue of a project the viewer cannot read is unknown", e.Member, with(map[string]string{"id": itoa(hidden.Request.ID)}), http.StatusNotFound},
		{"an unknown number", e.Member, with(map[string]string{"id": "999999"}), http.StatusNotFound},
		{"a free emoji is not on the list", e.Member, with(map[string]string{"reaction": "💩"}), http.StatusBadRequest},
		{"an unknown kind", e.Member, with(map[string]string{"kind": "pain"}), http.StatusBadRequest},
		{"no number", e.Member, with(map[string]string{"id": "x"}), http.StatusBadRequest},
	}
	for _, c := range cases {
		if status, _, body := reactPost(t, e, c.c, c.form); status != c.status {
			t.Errorf("%s -> %d, want %d (%s)", c.name, status, c.status, body)
		}
	}
	// A guest's refusal changes nothing; the member's reaction is there.
	got, err := tpOwner(e).Reactions(store.ReactOnRequest, d.Request.ID)
	if err != nil || len(got.Items) != 1 || got.Items[0].Count != 1 {
		t.Errorf("after the attempts: %+v %v", got, err)
	}
	// The unknown and the unreadable look alike.
	_, _, a := reactPost(t, e, e.Member, with(map[string]string{"id": itoa(hidden.Request.ID)}))
	_, _, b := reactPost(t, e, e.Member, with(map[string]string{"id": "999999"}))
	if a != b {
		t.Errorf("an unreadable issue answers differently from an unknown one: %q vs %q", a, b)
	}
}

func TestReactionNeedsTheTokenOfTheFormAndTheSameOrigin(t *testing.T) {
	e := ovEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Open one", Criteria: []string{"One"}})
	form := url.Values{"kind": {"request"}, "id": {itoa(d.Request.ID)}, "reaction": {"+1"}, "csrf_token": {"wrong"}}
	resp := sameOriginPostForm(t, e.Member, e.Base+"/ui/reactions", form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a wrong token -> %d, want 403", resp.StatusCode)
	}
	if got, _ := tpOwner(e).Reactions(store.ReactOnRequest, d.Request.ID); len(got.Items) != 0 {
		t.Errorf("a refused form reacted: %+v", got)
	}
	if resp, err := e.Member.Get(e.Base + "/ui/reactions"); err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed && resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET /ui/reactions -> %d", resp.StatusCode)
		}
	}
}

func TestReactionOnACommentLeadsBackToTheComment(t *testing.T) {
	e, pa := listEnv(t)
	d := rqMake(t, e.St, rqSpec{Title: "Talked about", Criteria: []string{"One"}})
	c, err := pa.AddComment(store.CommentOnRequest, d.Request.ID, "Hello", store.CommentActor{Person: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	status, loc, _ := reactPost(t, e, e.Member, url.Values{"kind": {"comment"}, "id": {itoa(c.ID)}, "reaction": {"laugh"}, "next": {"1"}})
	if want := "/ui/x/shell/issues/" + itoa(d.Request.ID) + "#comment-" + itoa(c.ID); status != http.StatusSeeOther || loc != want {
		t.Errorf("-> %d %q, want 303 %q", status, loc, want)
	}
	// A deleted comment takes no reaction.
	if _, err := pa.DeleteComment(c.ID, store.CommentActor{Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := reactPost(t, e, e.Member, url.Values{"kind": {"comment"}, "id": {itoa(c.ID)}, "reaction": {"laugh"}}); status != http.StatusNotFound {
		t.Errorf("a deleted comment -> %d, want 404", status)
	}
}

func TestReactionTipNamesWhoReactedAndWhatAClickDoes(t *testing.T) {
	l := langDE
	it := store.ReactionCount{Content: "+1", Emoji: "👍", Count: 4, Mine: true, People: []string{"Anna", "Robin", "Mara"}}
	got := reactionTip(l, it, "Robin", true)
	if want := "Mit 👍 reagiert: Du, Anna, Mara und 1 weitere\nKlick nimmt deine Reaktion zurück"; got != want {
		t.Errorf("tip of mine: %q, want %q", got, want)
	}
	it = store.ReactionCount{Content: "heart", Emoji: "❤️", Count: 1, People: []string{"Anna"}}
	if got, want := reactionTip(l, it, "Robin", true), "Mit ❤️ reagiert: Anna\nMit ❤️ reagieren"; got != want {
		t.Errorf("tip: %q, want %q", got, want)
	}
	if got, want := reactionTip(l, it, "", false), "Mit ❤️ reagiert: Anna"; got != want {
		t.Errorf("tip for a guest: %q, want %q", got, want)
	}
}
