package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type reactionFixture struct {
	commentFixture
	commentID int64
}

func newReactionFixture(t *testing.T) reactionFixture {
	t.Helper()
	f := reactionFixture{commentFixture: newCommentFixture(t)}
	code, out := f.add(t, "mia", "request", "Ein Kommentar")
	if code != 201 {
		t.Fatalf("comment: %d %s", code, out)
	}
	f.commentID = decodeComment(t, out).ID
	return f
}

func (f reactionFixture) target(kind string) int64 {
	if kind == "comment" {
		return f.commentID
	}
	return f.targets[kind]
}

func (f reactionFixture) put(t *testing.T, who, kind, content string) (int, string) {
	t.Helper()
	return f.call(t, who, "PUT", "/api/reactions", map[string]any{"kind": kind, "id": f.target(kind), "content": content})
}

func (f reactionFixture) del(t *testing.T, who, kind, content string) (int, string) {
	t.Helper()
	return f.call(t, who, "DELETE", fmt.Sprintf("/api/reactions?kind=%s&id=%d&content=%s", kind, f.target(kind), url.QueryEscape(content)), nil)
}

func (f reactionFixture) get(t *testing.T, who, kind string) (int, string) {
	t.Helper()
	return f.call(t, who, "GET", fmt.Sprintf("/api/reactions?kind=%s&id=%d", kind, f.target(kind)), nil)
}

type reactionAnswer struct {
	Kind     string
	ID       int64
	CanReact bool `json:"can_react"`
	Items    []struct {
		Content string
		Emoji   string
		Count   int
		Mine    bool
		People  []string
	}
	Choices []struct{ Key, Emoji, Label string }
}

func decodeReactions(t *testing.T, body string) reactionAnswer {
	t.Helper()
	return decode[reactionAnswer](t, body)
}

func TestReactionsAreSetAndRemovedPerPersonAndIdempotently(t *testing.T) {
	f := newReactionFixture(t)
	for _, kind := range []string{"comment", "idea", "request"} {
		code, out := f.put(t, "mia", kind, "+1")
		if code != 200 {
			t.Fatalf("%s: %d %s", kind, code, out)
		}
		// Ein zweites Mal ändert nichts.
		if code, out = f.put(t, "mia", kind, "+1"); code != 200 {
			t.Fatalf("%s again: %d %s", kind, code, out)
		}
		r := decodeReactions(t, out)
		if r.Kind != kind || r.ID != f.target(kind) || len(r.Items) != 1 || r.Items[0].Content != "+1" || r.Items[0].Emoji != "👍" || r.Items[0].Count != 1 ||
			!r.Items[0].Mine || len(r.Items[0].People) != 1 || r.Items[0].People[0] != "mia" || !r.CanReact {
			t.Fatalf("%s: %s", kind, out)
		}
		// Eine zweite Person: die Zahl wächst, "mine" gehört dem Betrachter.
		code, out = f.put(t, "lena", kind, "👍")
		r = decodeReactions(t, out)
		if code != 200 || r.Items[0].Count != 2 || !r.Items[0].Mine || strings.Join(r.Items[0].People, ",") != "mia,lena" {
			t.Fatalf("%s second person: %d %s", kind, code, out)
		}
		if _, out = f.get(t, "rex", kind); decodeReactions(t, out).Items[0].Mine {
			t.Errorf("%s: a third person has no reaction of its own: %s", kind, out)
		}
		// Entfernen trifft nur die eigene Reaktion und ist idempotent.
		if code, out = f.del(t, "lena", kind, "+1"); code != 200 {
			t.Fatalf("%s delete: %d %s", kind, code, out)
		}
		r = decodeReactions(t, out)
		if r.Items[0].Count != 1 || r.Items[0].Mine || r.Items[0].People[0] != "mia" {
			t.Fatalf("%s after lena removed hers: %s", kind, out)
		}
		if code, _ = f.del(t, "lena", kind, "+1"); code != 200 {
			t.Fatalf("%s delete again: %d", kind, code)
		}
		if code, out = f.del(t, "mia", kind, "+1"); code != 200 || len(decodeReactions(t, out).Items) != 0 {
			t.Fatalf("%s delete last: %d %s", kind, code, out)
		}
	}
}

func TestReactionsAreAFixedListWithoutFreeEmojis(t *testing.T) {
	f := newReactionFixture(t)
	for _, in := range []string{"+1", "-1", "laugh", "hooray", "confused", "heart", "rocket", "eyes", "👎", "😄", "🎉", "😕", "❤️", "❤", "🚀", "👀", "HEART", " rocket "} {
		if code, out := f.put(t, "mia", "idea", in); code != 200 {
			t.Errorf("%q: %d %s", in, code, out)
		}
	}
	_, out := f.get(t, "mia", "idea")
	r := decodeReactions(t, out)
	// Eine Zeile je Reaktion der Liste, in der Reihenfolge der Liste.
	order := []string{}
	for _, it := range r.Items {
		order = append(order, it.Content)
		if it.Count != 1 {
			t.Errorf("%s counted %d times (a glyph and its key are one reaction)", it.Content, it.Count)
		}
	}
	if strings.Join(order, ",") != "+1,-1,laugh,hooray,confused,heart,rocket,eyes" {
		t.Errorf("order %v", order)
	}
	if len(r.Choices) != len(store.ReactionSet) || r.Choices[0].Key != "+1" || r.Choices[0].Emoji != "👍" {
		t.Errorf("choices: %+v", r.Choices)
	}
	for _, bad := range []string{"🦄", "💩", "thumbsup", "", " ", "+2", "👍👍", "<script>", "+1\n-1"} {
		code, body := f.put(t, "mia", "request", bad)
		if code != 400 || !strings.Contains(body, "invalid_reaction") {
			t.Errorf("%q: %d %s", bad, code, body)
		}
		if code, _ := f.del(t, "mia", "request", bad); code != 400 {
			t.Errorf("delete %q: %d", bad, code)
		}
	}
	if _, out := f.get(t, "mia", "request"); len(decodeReactions(t, out).Items) != 0 {
		t.Errorf("a refused reaction was stored: %s", out)
	}
	f.expect(t, "mia", 400, "PUT", "/api/reactions", map[string]any{"kind": "pain", "id": f.targets["pain"], "content": "+1"})
	f.expect(t, "mia", 400, "PUT", "/api/reactions", map[string]any{"kind": "nonsense", "id": 1, "content": "+1"})
	f.expect(t, "mia", 400, "GET", "/api/reactions?kind=idea&id=x", nil)
}

// Lesen wie das Objekt, reagieren ab member; Fremde, Unbekanntes und Verbotenes
// sehen gleich aus.
func TestReactionRightsGuestReadsStrangerAndFormerMemberSeeNothing(t *testing.T) {
	f := newReactionFixture(t)
	for _, kind := range []string{"comment", "idea", "request"} {
		f.put(t, "mia", kind, "heart")
		code, out := f.get(t, "gus", kind)
		if code != 200 || len(decodeReactions(t, out).Items) != 1 || decodeReactions(t, out).CanReact {
			t.Errorf("%s: guest reads: %d %s", kind, code, out)
		}
		if code, _ := f.put(t, "gus", kind, "+1"); code != 403 {
			t.Errorf("%s: guest sets: %d", kind, code)
		}
		if code, _ := f.del(t, "gus", kind, "heart"); code != 403 {
			t.Errorf("%s: guest removes: %d", kind, code)
		}
		if _, out := f.get(t, "gus", kind); len(decodeReactions(t, out).Items) != 1 || decodeReactions(t, out).Items[0].Content != "heart" {
			t.Errorf("%s: a guest changed something: %s", kind, out)
		}
		// Fremder: dasselbe wie für ein Objekt, das es nicht gibt.
		strangerCode, strangerBody := f.get(t, "nora", kind)
		missCode, missBody := f.call(t, "nora", "GET", fmt.Sprintf("/api/reactions?kind=%s&id=%d", kind, 9999), nil)
		if strangerCode != 404 || strangerCode != missCode || strings.ReplaceAll(strangerBody, fmt.Sprint(f.target(kind)), "N") != strings.ReplaceAll(missBody, "9999", "N") {
			t.Errorf("%s: stranger %d %s / missing %d %s", kind, strangerCode, strangerBody, missCode, missBody)
		}
		if code, _ := f.put(t, "nora", kind, "+1"); code != 404 {
			t.Errorf("%s: stranger sets: %d", kind, code)
		}
		if code, _ := f.del(t, "nora", kind, "+1"); code != 404 {
			t.Errorf("%s: stranger removes: %d", kind, code)
		}
		if code, _ := f.call(t, "", "GET", fmt.Sprintf("/api/reactions?kind=%s&id=%d", kind, f.target(kind)), nil); code != 401 {
			t.Errorf("%s: no token: %d", kind, code)
		}
	}
	// Ehemaliges Mitglied.
	p, _ := f.st.ProjectByRemote(accProject)
	if err := f.st.RemoveProjectRole("person:1", accProject, "person:3", store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RemoveOrgMember("person:1", p.OrgID, "person:3"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"comment", "idea", "request"} {
		if code, out := f.get(t, "mia", kind); code != 404 {
			t.Errorf("%s: former member reads: %d %s", kind, code, out)
		}
		if code, _ := f.put(t, "mia", kind, "+1"); code != 404 {
			t.Errorf("%s: former member sets: %d", kind, code)
		}
		if code, _ := f.del(t, "mia", kind, "heart"); code != 404 {
			t.Errorf("%s: former member removes: %d", kind, code)
		}
	}
	// Die Reaktion des ehemaligen Mitglieds bleibt stehen, die anderen sehen sie.
	if _, out := f.get(t, "lena", "idea"); len(decodeReactions(t, out).Items) != 1 {
		t.Errorf("the former member's reaction is gone: %s", out)
	}
}

func TestReactionOnADeletedCommentIsRefusedAndTheOtherOnesKeepTheirs(t *testing.T) {
	f := newReactionFixture(t)
	code, out := f.add(t, "mia", "request", "Zweiter")
	if code != 201 {
		t.Fatal(out)
	}
	second := decodeComment(t, out).ID
	f.put(t, "lena", "comment", "rocket")
	f.expect(t, "mia", 200, "PUT", "/api/reactions", map[string]any{"kind": "comment", "id": second, "content": "eyes"})
	f.expect(t, "mia", 200, "DELETE", fmt.Sprintf("/api/comments/%d", f.commentID), nil)
	if code, body := f.put(t, "lena", "comment", "+1"); code != 404 {
		t.Errorf("reacting to a deleted comment: %d %s", code, body)
	}
	if code, body := f.get(t, "lena", "comment"); code != 404 {
		t.Errorf("reading the reactions of a deleted comment: %d %s", code, body)
	}
	// Alle Kommentare eines Objekts auf einmal: das gelöschte trägt nichts, das andere seine Reaktion.
	out = f.expect(t, "gus", 200, "GET", fmt.Sprintf("/api/comments/reactions?kind=request&id=%d", f.targets["request"]), nil)
	m := decode[struct {
		Reactions map[string]reactionAnswer `json:"reactions"`
		Choices   []struct{ Key string }
	}](t, out)
	if _, ok := m.Reactions[fmt.Sprint(f.commentID)]; ok || len(m.Reactions[fmt.Sprint(second)].Items) != 1 || m.Reactions[fmt.Sprint(second)].Items[0].Content != "eyes" || len(m.Choices) != 8 {
		t.Fatalf("comment reactions: %s", out)
	}
	f.expect(t, "nora", 404, "GET", fmt.Sprintf("/api/comments/reactions?kind=request&id=%d", f.targets["request"]), nil)
}

// Die Liste für Ideen und Issues in einem Aufruf lässt weg, was der Betrachter nicht lesen darf.
func TestReactionsForAListSkipWhatTheViewerMayNotRead(t *testing.T) {
	f := newReactionFixture(t)
	f.put(t, "mia", "request", "hooray")
	ids := []int64{f.targets["request"], f.id["r-robin"], 99999}
	as := func(id string) *store.ProjectAccess { return f.st.Access(store.Principal{ID: id}) }
	got, err := as("person:6").ReactionsFor("request", ids) // nora: nicht in der Org
	if err != nil || len(got) != 0 {
		t.Fatalf("a stranger: %v %v", got, err)
	}
	got, err = as("person:5").ReactionsFor("request", ids) // gus
	if err != nil || len(got) != 2 || got[f.targets["request"]].Items[0].Content != "hooray" || got[f.targets["request"]].CanReact || got[f.id["r-robin"]].CanReact {
		t.Fatalf("a guest: %+v %v", got, err)
	}
	got, err = as("person:3").ReactionsFor("request", ids) // mia
	if err != nil || len(got) != 2 || !got[f.targets["request"]].CanReact || !got[f.targets["request"]].Items[0].Mine {
		t.Fatalf("a member: %+v %v", got, err)
	}
	if _, err := as("person:3").ReactionsFor("pain", ids); err == nil {
		t.Error("pain has no reactions")
	}
	if _, err := as("person:3").ReactionsFor("request", make([]int64, 501)); err == nil {
		t.Error("501 objects at once must be refused")
	}
}
