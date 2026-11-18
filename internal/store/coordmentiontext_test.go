package store

import (
	"errors"
	"slices"
	"strconv"
	"testing"
)

func sendText(t *testing.T, a CoordAccess, room, body, intent string) (int64, error) {
	t.Helper()
	sendTextSeq++
	return a.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "t" + strconv.Itoa(sendTextSeq),
		Body: body, Intent: intent})
}

var sendTextSeq int

func storedMentions(t *testing.T, e authorityEnv, id int64) []string {
	t.Helper()
	got, err := e.st.CoordMessageMentions(id)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	return got
}

func TestTextMentionResolvesNamesAndHandles(t *testing.T) {
	e := authorityFixture(t)
	sender := e.human("person:1")
	cases := []struct {
		body string
		want []string
	}{
		{"@anna can you look", []string{"person:2"}},
		{"hi @ANNA, thanks", []string{"person:2"}},
		{"@cle please", []string{"person:4"}},
		{"@claude-anna please", []string{"a-anna"}},
		{"@a-ben fix it", []string{"a-ben"}},
		{"@anna and @a-cleo", []string{"a-cleo", "person:2"}},
		{"mail ben@example.com", nil},
		{"@nobody-here", nil},
		{"no mention at all", nil},
		{"(@anna)", []string{"person:2"}},
	}
	for _, c := range cases {
		id, err := sendText(t, sender, e.room, c.body, "")
		if err != nil {
			t.Fatalf("%q: %v", c.body, err)
		}
		if got := storedMentions(t, e, id); !slices.Equal(got, c.want) {
			t.Errorf("%q: mentions %v, want %v", c.body, got, c.want)
		}
	}
}

func TestTextMentionAmbiguityIsAHintNotAGuess(t *testing.T) {
	e := authorityFixture(t)
	var amb *AmbiguousMentionError
	_, err := sendText(t, e.human("person:1"), e.room, "@claude ping", "")
	if !errors.As(err, &amb) || !errors.Is(err, ErrCoordAmbiguousMention) {
		t.Fatalf("want ambiguous mention error, got %v", err)
	}
	if amb.Token != "claude" || len(amb.Candidates) < 2 {
		t.Fatalf("hint should name the token and candidates: %+v", amb)
	}
	// Nothing was stored.
	msgs, _ := e.human("person:1").Messages(DestinationRoom, e.room, 0, 50)
	for _, m := range msgs {
		if m.Body == "@claude ping" {
			t.Fatal("an ambiguous message must not be stored")
		}
	}
}

func TestTextMentionAloneSatisfiesAnAttentionIntent(t *testing.T) {
	e := authorityFixture(t)
	id, err := sendText(t, e.human("person:1"), e.room, "@a-ben could you review", IntentQuestion)
	if err != nil {
		t.Fatal(err)
	}
	if got := storedMentions(t, e, id); !slices.Equal(got, []string{"a-ben"}) {
		t.Fatalf("mentions %v", got)
	}
}

func TestTextMentionDoesNotMentionTheSender(t *testing.T) {
	e := authorityFixture(t)
	id, err := sendText(t, e.human("person:1"), e.room, "note to @robin", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := storedMentions(t, e, id); len(got) != 0 {
		t.Fatalf("self mention stored: %v", got)
	}
}

// Ein Gast schreibt im Projektraum nichts (room.post ist member und höher), und
// die Antwort ist für gefundene, unbekannte und mehrdeutige Namen dieselbe: der
// Raum verrät einem Gast nicht, wer in ihm ist (#2447).
func TestTextMentionGivesGuestsNoOracle(t *testing.T) {
	e := guestEnv(t)
	guest := e.human("person:5")
	for _, body := range []string{"@ben hello", "@nobody hello", "@claude hello"} {
		if _, err := sendText(t, guest, e.room, body, ""); !errors.Is(err, ErrCoordForbidden) {
			t.Fatalf("%q: guest answer = %v, want forbidden for every name", body, err)
		}
	}
	for _, body := range []string{"@ben q", "@nobody q"} {
		if _, err := sendText(t, guest, e.room, body, IntentQuestion); !errors.Is(err, ErrCoordForbidden) {
			t.Fatalf("%q with intent: %v, want forbidden", body, err)
		}
	}
}

// Ein Agent, der als Agent eines Gastes in den Projektraum kam (vor der Regel,
// dass Gäste dort keine Agenten mehr anmelden), liest den Raum und schreibt nicht:
// die Mitgliedschaft bleibt, jede Nachricht und jede Vorgabe prüft room.post.
func TestAGuestsAgentReadsTheProjectRoomButDoesNotWriteInIt(t *testing.T) {
	for _, enforce := range []bool{true, false} {
		e := guestEnv(t)
		e.st.SetAccessMode(AccessMode{Enforce: enforce})
		registerRoleAgent(t, e.st, "a-guest", "person:5", e.room, "guest")
		agent := e.agent("a-guest")
		if _, err := sendText(t, agent, e.room, "hallo", ""); !errors.Is(err, ErrCoordForbidden) {
			t.Fatalf("enforce=%v: a guest's agent wrote in the project room: %v", enforce, err)
		}
		if _, err := agent.CreateStanding(StandingInput{RoomKey: e.room, ClientID: "g1", Body: "Vorgabe"}); !errors.Is(err, ErrCoordForbidden) {
			t.Fatalf("enforce=%v: a guest's agent created a directive: %v", enforce, err)
		}
		if _, err := agent.Messages(DestinationRoom, e.room, 0, 10); err != nil {
			t.Fatalf("enforce=%v: a guest's agent may read the room: %v", enforce, err)
		}
		// Ein Mitglied schreibt weiter.
		if _, err := sendText(t, e.human("person:1"), e.room, "ok", ""); err != nil {
			t.Fatalf("a member writes: %v", err)
		}
	}
}
