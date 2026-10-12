package store

import "testing"

// Wer im Raum ist, darf die Namen der Raumteilnehmer sehen: auch der Absender
// liest bei seiner Erwähnung den Namen statt "Someone" (Abnahme 2026-10-11,
// Befund 4). Ein Gast bekommt weiter keinen.
func TestMentionLabelsNameTheMentionedPeopleForRoomMembers(t *testing.T) {
	e := authorityFixture(t)
	sender := e.human("person:1")
	id, err := sendText(t, sender, e.room, "@anna can you look", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, reader := range []string{"person:1", "person:2", "person:3"} {
		_, presented, err := e.human(reader).MessagePresentationWindow(DestinationRoom, e.room, MessageWindow{Mode: messageWindowLatest, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, p := range presented {
			if p.Message.ID != id {
				continue
			}
			found = true
			if got := p.MentionLabels["person:2"]; got != "anna" {
				t.Errorf("%s reads the mentioned person as %q, want anna (%+v)", reader, got, p.MentionLabels)
			}
		}
		if !found {
			t.Fatalf("%s does not see the message", reader)
		}
	}
}

func TestMentionLabelsStayHiddenFromGuests(t *testing.T) {
	e := guestEnv(t)
	id, err := sendText(t, e.human("person:1"), e.room, "@anna hello", "")
	if err != nil {
		t.Fatal(err)
	}
	_, presented, err := e.human("person:5").MessagePresentationWindow(DestinationRoom, e.room, MessageWindow{Mode: messageWindowLatest, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range presented {
		if p.Message.ID == id && len(p.MentionLabels) != 0 {
			t.Errorf("a guest gets names: %+v", p.MentionLabels)
		}
	}
}
