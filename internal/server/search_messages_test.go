package server

import (
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Raum-Nachrichten und Sessions als Trefferarten der Suchsprache (REQ-600,
// Kriterium 8). Die Welt: ein Projektraum mit zwei Agenten (Mia, Rex), ein
// privater Direktraum von Mias und Lenas Agent, dazu die Sessions aus dem
// Fixture (jede private Session gehört ihrem Konto).
type msgWorld struct {
	*qWorld
	project, direct string
	open, dm, old   int64
}

func newMsgWorld(t *testing.T) *msgWorld {
	t.Helper()
	w := &msgWorld{qWorld: newQWorld(t), project: store.RoomKeyForProject(accProject)}
	st := w.f.st
	for _, a := range []store.CoordAgent{
		{ExternalID: "a-mia", PrincipalID: "person:3", Person: "mia", Provider: "test", RoomKey: w.project},
		{ExternalID: "a-rex", PrincipalID: "person:4", Person: "rex", Provider: "test", RoomKey: w.project},
		{ExternalID: "a-lena", PrincipalID: "person:2", Person: "lena", Provider: "test", RoomKey: w.project},
	} {
		if _, err := st.RegisterCoordAgent(a); err != nil {
			t.Fatal(err)
		}
	}
	w.direct = store.RoomKeyForDirect([]string{"a-mia", "a-lena"})
	if err := st.EnsureCoordRoom(store.CoordRoom{Key: w.direct, Kind: store.RoomDirect, Members: []string{"a-mia", "a-lena"}}); err != nil {
		t.Fatal(err)
	}
	send := func(room, client, body string) int64 {
		id, err := st.CoordinationFor(store.Principal{ID: "person:3"}, "a-mia").Send(store.CoordMessage{
			DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: client, Body: body,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	w.open = send(w.project, "m-open", "marker offen im Projektraum")
	w.dm = send(w.direct, "m-dm", "marker nur im Direktraum")
	w.old = send(w.project, "m-old", "marker alt im Projektraum")
	w.exec(t, `UPDATE coord_messages SET created_at='2026-10-01T10:00:00Z' WHERE id=?`, w.old)
	// Anker und Bereich der offenen Nachricht, wie ein Client sie beim Schreiben mitgeschickt hätte.
	w.exec(t, `UPDATE coord_messages SET anchor_id=?, origin_id=? WHERE id=?`, w.anchor(t, "abcdef1234567", "feat/x", 12), w.origin(t, "internal/web"), w.open)
	return w
}

func (w *msgWorld) messages(t *testing.T, who, agent, line string, extra ...string) []int64 {
	t.Helper()
	q := url.Values{"q": {line}, "project": {accProject}, "limit": {"50"}}
	if agent != "" {
		q.Set("agent_external_id", agent)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	res := decode[struct {
		Messages []store.MessageHit `json:"messages"`
	}](t, w.f.expect(t, who, 200, "GET", "/api/search?"+q.Encode(), nil))
	var ids []int64
	for _, m := range res.Messages {
		ids = append(ids, m.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func sameIDs(got []int64, want ...int64) bool {
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestMessagesAreAKindOfTheSearchLanguage(t *testing.T) {
	w := newMsgWorld(t)
	check := func(who, agent, line string, want ...int64) {
		t.Helper()
		if got := w.messages(t, who, agent, line); !sameIDs(got, want...) {
			t.Errorf("%s %q = %v, want %v", who, line, got, want)
		}
	}
	// Text und Art.
	check("mia", "a-mia", "marker kind:message", w.open, w.dm, w.old)
	check("mia", "a-mia", "marker is:message", w.open, w.dm, w.old)
	check("mia", "a-mia", "marker kind:messages -kind:file", w.open, w.dm, w.old)
	check("mia", "a-mia", "nirgendwo kind:message")
	// Ohne Art stehen Nachrichten neben den anderen Arten.
	check("mia", "a-mia", "marker pr:12", w.open)
	// repo: trifft den Projektraum, nie den Direktraum (er gehört zu keinem Projekt).
	check("mia", "a-mia", "marker kind:message repo:dw/p", w.open, w.old)
	check("mia", "a-mia", "marker kind:message -repo:dw/p", w.dm)
	// Anker.
	check("mia", "a-mia", "kind:message pr:12", w.open)
	check("mia", "a-mia", "kind:message pr:13..")
	check("mia", "a-mia", "kind:message branch:feat/x", w.open)
	check("mia", "a-mia", "kind:message -branch:feat/x", w.dm, w.old)
	check("mia", "a-mia", "kind:message commit:abcdef1", w.open)
	// Bereich des Entstehungskontexts und Zeitraum.
	check("mia", "a-mia", "kind:message area:internal/web", w.open)
	check("mia", "a-mia", "kind:message area:internal/store")
	check("mia", "a-mia", "kind:message created:<2026-10-15", w.old)
	check("mia", "a-mia", "kind:message created:>2026-10-15", w.open, w.dm)
	// Absender.
	check("mia", "a-mia", "kind:message author:a-mia", w.open, w.dm, w.old)
	check("mia", "a-mia", "kind:message author:a-rex")
	// Nachrichten haben keinen Zustand und kein Label.
	check("mia", "a-mia", "kind:message is:open")
	check("mia", "a-mia", "kind:message label:bug")
	// Die Obergrenze gilt auch für die Nachrichten.
	if got := w.messages(t, "mia", "a-mia", "marker kind:message", "limit", "1"); len(got) != 1 {
		t.Errorf("limit=1 gave %d messages", len(got))
	}
	if got := w.messages(t, "mia", "a-mia", "marker kind:message", "limit", "999999999"); len(got) != 3 {
		t.Errorf("a huge limit is clamped and still answers: %d", len(got))
	}
}

// Rechte: eine Raum-Nachricht sieht nur, wer den Raum auch sonst lesen darf.
func TestMessageSearchNeverShowsAPrivateRoom(t *testing.T) {
	w := newMsgWorld(t)
	for _, c := range []struct {
		who, agent string
		want       []int64
	}{
		{"mia", "a-mia", []int64{w.open, w.dm, w.old}},
		{"lena", "a-lena", []int64{w.open, w.dm, w.old}},
		// Rex liest den Projektraum, nicht den Direktraum von Mia und Lena.
		{"rex", "a-rex", []int64{w.open, w.old}},
		// Ein fremder Agent: Rex kann nicht als Mias Agent fragen.
		{"rex", "a-mia", nil},
		// Eine nicht registrierte Kennung und ein Konto ohne Rolle im Projekt.
		{"rex", "a-unknown", nil},
		{"nora", "", nil},
	} {
		got := w.messages(t, c.who, c.agent, "marker kind:message")
		if !sameIDs(got, c.want...) {
			t.Errorf("%s as %q sees %v, want %v", c.who, c.agent, got, c.want)
		}
		// Auch ohne Art in der Suchzeile darf der Direktraum nicht auftauchen.
		for _, id := range w.messages(t, c.who, c.agent, "marker pr:12 -pr:99") {
			if id == w.dm && c.who != "mia" && c.who != "lena" {
				t.Errorf("%s sees the private room through a qualifier line", c.who)
			}
		}
	}
}

func TestSessionsAreAKindOfTheSearchLanguage(t *testing.T) {
	w := newMsgWorld(t)
	sessions := func(who, line string) []int64 {
		t.Helper()
		q := url.Values{"q": {line}, "project": {accProject}, "limit": {"50"}}
		res := decode[struct {
			Sessions []store.SessionHit `json:"sessions"`
		}](t, w.f.expect(t, who, 200, "GET", "/api/search?"+q.Encode(), nil))
		var ids []int64
		for _, h := range res.Sessions {
			ids = append(ids, h.Session.ID)
		}
		return ids
	}
	mia := w.f.id["s-mia"]
	if got := sessions("mia", "secretterm kind:session"); !sameIDs(got, mia) {
		t.Errorf("mia finds her session: %v", got)
	}
	if got := sessions("mia", "secretterm is:session repo:dw/p"); !sameIDs(got, mia) {
		t.Errorf("repo: reaches sessions: %v", got)
	}
	if got := sessions("mia", "secretterm kind:session -repo:dw/p"); len(got) != 0 {
		t.Errorf("-repo: leaves the session out: %v", got)
	}
	if got := sessions("mia", "secretterm kind:session created:>2000-01-01"); !sameIDs(got, mia) {
		t.Errorf("created: reaches sessions: %v", got)
	}
	if got := sessions("mia", "secretterm kind:session created:<2000-01-01"); len(got) != 0 {
		t.Errorf("created: filters sessions: %v", got)
	}
	if got := sessions("mia", "secretterm kind:session is:open"); len(got) != 0 {
		t.Errorf("a session has no state: %v", got)
	}
	if got := sessions("mia", "secretterm kind:session pr:12"); len(got) != 0 {
		t.Errorf("a session carries no anchor: %v", got)
	}
	// Ohne Suchwort gibt es keine Session-Treffer: ein Transkript wird gefunden, nicht aufgelistet.
	if got := sessions("mia", "kind:session repo:dw/p"); len(got) != 0 {
		t.Errorf("sessions are not listed without a word: %v", got)
	}
}

// Negativtest: private Sessions bleiben privat, auch in der Suchsprache.
func TestPrivateSessionsStayPrivateInTheSearchLanguage(t *testing.T) {
	w := newMsgWorld(t)
	for _, who := range []string{"lena", "rex", "gus", "nora"} {
		for _, line := range []string{"secretterm kind:session", "secretterm repo:dw/p", "secretterm created:>2000-01-01", "secretterm -is:open"} {
			q := url.Values{"q": {line}, "project": {accProject}, "limit": {"50"}}
			out := w.f.expect(t, who, 200, "GET", "/api/search?"+q.Encode(), nil)
			for _, foreign := range []string{"secretterm mia", "secretterm robin"} {
				if strings.Contains(out, foreign) {
					t.Errorf("%s sees %q through %q", who, foreign, line)
				}
			}
		}
	}
	// Rex sieht seine eigene, nicht die der anderen.
	q := url.Values{"q": {"secretterm kind:session"}, "project": {accProject}}
	out := w.f.expect(t, "rex", 200, "GET", "/api/search?"+q.Encode(), nil)
	if !strings.Contains(out, "secretterm rex") || strings.Contains(out, "secretterm mia") {
		t.Errorf("rex sees only his own: %s", out)
	}
}
