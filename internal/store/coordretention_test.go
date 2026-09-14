package store

import (
	"testing"
	"time"
)

func oldMessage(t *testing.T, s *Store, room, clientID, body string, daysAgo int) int64 {
	t.Helper()
	at := time.Now().UTC().Add(-time.Duration(daysAgo) * 24 * time.Hour).Format(time.RFC3339)
	id, err := s.AppendCoordMessage(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: room,
		SenderExternalID: "sess-a", ClientID: clientID, Body: body, CreatedAt: at})
	if err != nil {
		t.Fatalf("append %s: %v", clientID, err)
	}
	return id
}

// Spec §7: gewöhnlicher Chatverkehr hat eine Frist. Die AUSNAHME ist der
// eigentliche Inhalt — eine Nachricht, die Grundlage eines dauerhaften
// Ergebnisses ist, darf nicht verschwinden (§6).
func TestRetentionKeepsWhatAThreadStandsOn(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("p")
	geplauder := oldMessage(t, s, room, "c1", "reines Geplauder", 60)
	grundlage := oldMessage(t, s, room, "c2", "daraus wurde ein Thema", 60)
	_ = geplauder

	if _, err := s.PromoteMessagesToThread(room, []int64{grundlage},
		Thread{Project: "p", Title: "Das Thema"}, "robin"); err != nil {
		t.Fatalf("promote: %v", err)
	}

	res, err := s.ApplyCoordRetention(RetentionCutoff(ChatRetention), RetentionCutoff(ActivityRetention))
	if err != nil {
		t.Fatalf("retention: %v", err)
	}
	if res.MessagesDeleted != 1 {
		t.Fatalf("want 1 deleted, got %d", res.MessagesDeleted)
	}
	if res.MessagesHeld != 1 {
		t.Fatalf("want 1 held back, got %d", res.MessagesHeld)
	}

	left, err := s.CoordMessagesSince(DestinationRoom, room, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].ID != grundlage {
		t.Fatalf("retention removed the message a thread stands on: %+v", left)
	}
}

// §11: "Chat-Retention entfernt keine noch aktive menschliche
// Einschränkung." Eine geltende Vorgabe überlebt ihre Frist, eine beendete
// nicht.
func TestRetentionKeepsAStandingInstructionButNotAnEndedOne(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("p")
	gilt := oldMessage(t, s, room, "c1", "keine Breaking Changes", 60)
	beendet := oldMessage(t, s, room, "c2", "galt mal", 60)

	for id, body := range map[int64]string{gilt: "keine Breaking Changes", beendet: "galt mal"} {
		if err := s.PutStandingInstruction(StandingInstruction{
			RoomKey: room, MessageID: FormatMessageID(id), Person: "robin", Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EndStandingInstruction(room, FormatMessageID(beendet), "robin"); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ApplyCoordRetention(RetentionCutoff(ChatRetention),
		RetentionCutoff(ActivityRetention)); err != nil {
		t.Fatal(err)
	}
	left, err := s.CoordMessagesSince(DestinationRoom, room, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].ID != gilt {
		t.Fatalf("an instruction that still holds must survive its retention window: %+v", left)
	}
}

// Wer auf ein Ghosttree-Objekt zeigt, ist Provenienz und nicht Geplauder.
func TestRetentionKeepsMessagesThatReferenceObjects(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("p")
	at := time.Now().UTC().Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := s.AppendCoordMessage(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: room,
		SenderExternalID: "sess-a", ClientID: "c1", CreatedAt: at,
		Body: "Vertrag geändert", Refs: []CoordRef{{Kind: "request", ID: "350"}}}); err != nil {
		t.Fatal(err)
	}
	res, err := s.ApplyCoordRetention(RetentionCutoff(ChatRetention), RetentionCutoff(ActivityRetention))
	if err != nil {
		t.Fatal(err)
	}
	if res.MessagesDeleted != 0 || res.MessagesHeld != 1 {
		t.Fatalf("a message with object references is provenance: deleted %d held %d",
			res.MessagesDeleted, res.MessagesHeld)
	}
}

// Frische Nachrichten bleiben, und Aktivität hat eine eigene, kürzere Frist.
func TestRetentionLeavesFreshDataAndUsesSeparateWindows(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("p")
	oldMessage(t, s, room, "frisch", "von heute", 0)

	if err := s.RecordPathActivity([]PathActivity{
		{Project: "p", SessionExternalID: "sess-a", Tool: "Edit", Path: "alt.go",
			Quality: ActivityIntent, At: time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339)},
		{Project: "p", SessionExternalID: "sess-a", Tool: "Edit", Path: "neu.go",
			Quality: ActivityIntent, At: time.Now().UTC().Format(time.RFC3339)},
	}); err != nil {
		t.Fatal(err)
	}

	res, err := s.ApplyCoordRetention(RetentionCutoff(ChatRetention), RetentionCutoff(ActivityRetention))
	if err != nil {
		t.Fatal(err)
	}
	if res.MessagesDeleted != 0 {
		t.Errorf("a message from today must not be swept: %d deleted", res.MessagesDeleted)
	}
	// Aktivität hat sieben Tage, Chat dreißig — die getrennten Fenster sind
	// der Punkt, nicht ein Detail.
	if res.ActivityDeleted != 1 {
		t.Errorf("activity has its own shorter window: %d deleted", res.ActivityDeleted)
	}
}
