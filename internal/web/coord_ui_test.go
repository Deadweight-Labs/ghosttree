package web

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestMessageBodyEscapesTextAndMarksMentions(t *testing.T) {
	m := coordMessageView{Body: `@claude schau <b>hier</b> (@Robin) a@b.de`}
	got := string(m.BodyHTML())
	for _, want := range []string{`<span class="mention">@claude</span>`, `<span class="mention">@Robin</span>`, "&lt;b&gt;hier&lt;/b&gt;", "a@b.de"} {
		if !strings.Contains(got, want) {
			t.Errorf("body lacks %q: %s", want, got)
		}
	}
	if strings.Contains(got, `<span class="mention">@b.de`) || strings.Contains(got, "<b>") {
		t.Errorf("an address or markup was taken for a mention: %s", got)
	}
}

func TestSidebarSplitsTeamsAndDirectsAndBadgePrefersWhatNeedsYou(t *testing.T) {
	s := coordSidebarView{Private: []coordRoomView{{Key: "a", Kind: store.RoomGroup}, {Key: "b", Kind: store.RoomDirect}, {Key: "c", Kind: store.RoomGroup}}}
	if g, d := s.Groups(), s.Directs(); len(g) != 2 || len(d) != 1 || d[0].Key != "b" {
		t.Errorf("groups %v directs %v", g, d)
	}
	for _, c := range []struct {
		room coordRoomView
		want int64
	}{{coordRoomView{NeedsYou: 2, Unread: 9}, 2}, {coordRoomView{Mentions: 3, Unread: 9}, 3}, {coordRoomView{Attention: 4, Unread: 9}, 4}, {coordRoomView{Unread: 9}, 9}, {coordRoomView{}, 0}} {
		if got := c.room.Badge(); got != c.want {
			t.Errorf("badge of %+v = %d, want %d", c.room, got, c.want)
		}
	}
}

func TestMessageAvatarShowsTheHarnessOfAnAgentWithItsOwner(t *testing.T) {
	l := langEN
	agent := string(msgAvatar(l, coordMessageView{AuthorKind: "agent", Name: "Claude · Robin"}, "xl"))
	if !strings.Contains(agent, "a-claude") || !strings.Contains(agent, `class="own"`) {
		t.Errorf("agent avatar: %s", agent)
	}
	person := string(msgAvatar(l, coordMessageView{AuthorKind: "human", Name: "Lena Berger"}, "xl"))
	if strings.Contains(person, "agent") || !strings.Contains(person, "LE") {
		t.Errorf("person avatar: %s", person)
	}
	if got := (coordMessageView{AuthorKind: "agent", Name: "Codex · Robin"}).ShortName(); got != "Codex" {
		t.Errorf("short name %q", got)
	}
}

func TestRoomInTheBandOfItsProjectIsCalledGeneralAndTheComposerOffersTheKindAndTheAddressee(t *testing.T) {
	e := ovEnv(t)
	room, _ := memberReadSeed(t, e)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/coord?room="+room)
	for _, want := range []string{`class="rband`, `<span class="coord-room-name" title="`, ">General<", `name="intent"`, `name="mentions"`, `data-room-filter`, `/static/ui-rooms.js`} {
		if !strings.Contains(page, want) {
			t.Errorf("room page lacks %q", want)
		}
	}
	// The last kind of the form wins: the button "as a request" sends a question.
	if !strings.Contains(page, `class="gbtn rm-ask" name="intent" value="question"`) {
		t.Error("the ask button is missing")
	}
}
