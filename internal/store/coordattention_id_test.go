package store

import (
	"errors"
	"testing"
)

func attentionFixtureRoom(t *testing.T) (*Store, CoordAccess, string) {
	t.Helper()
	st := accessFixture(t)
	room := RoomKeyForProject(roleProject)
	for agent, principal := range map[string]string{"claude:mia": "person:3", "claude:lena": "person:2"} {
		if _, err := st.RegisterCoordAgent(CoordAgent{ExternalID: agent, Provider: "claude", RoomKey: room, PrincipalID: principal, Person: agent}); err != nil {
			t.Fatal(err)
		}
	}
	return st, st.CoordinationFor(Principal{ID: "person:3", Label: "mia"}, "claude:mia"), room
}

// Neue Einträge bekommen zufällige positive 63-Bit-Ids, keinen Zähler.
func TestNewAttentionIDsAreRandomAndPositive(t *testing.T) {
	st, mia, room := attentionFixtureRoom(t)
	var ids []int64
	for i := 0; i < 20; i++ {
		if _, err := mia.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, SenderExternalID: "claude:mia",
			ClientID: string(rune('a' + i)), Body: "q", Intent: IntentQuestion, Mentions: []string{"claude:lena"}}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := st.db.Query(`SELECT id FROM coord_attention ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 20 {
		t.Fatalf("got %d items", len(ids))
	}
	for i, id := range ids {
		if id < 1<<32 {
			t.Errorf("id %d looks like a counter", id)
		}
		if i > 0 && (id-ids[i-1] == 1 || id-ids[i-1] == 2) {
			t.Errorf("ids %d and %d follow a counting pattern", ids[i-1], id)
		}
	}
}

// Eine Kollision der Zufalls-Id wird mit einer neuen Zahl wiederholt; die
// Nachricht geht nicht verloren und der alte Eintrag bleibt unberührt.
func TestAttentionIDCollisionIsRetried(t *testing.T) {
	st, mia, room := attentionFixtureRoom(t)
	send := func(client string) {
		t.Helper()
		if _, err := mia.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, SenderExternalID: "claude:mia",
			ClientID: client, Body: "q", Intent: IntentQuestion, Mentions: []string{"claude:lena"}}); err != nil {
			t.Fatal(err)
		}
	}
	send("first")
	var existing int64
	if err := st.db.QueryRow(`SELECT id FROM coord_attention`).Scan(&existing); err != nil {
		t.Fatal(err)
	}
	old := attentionIDSource
	t.Cleanup(func() { attentionIDSource = old })
	calls := 0
	attentionIDSource = func() int64 {
		calls++
		if calls <= 3 {
			return existing // belegt
		}
		return old()
	}
	send("second")
	if calls < 4 {
		t.Fatalf("collision not retried, calls=%d", calls)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM coord_attention`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("items=%d err=%v", n, err)
	}
	// Dauerhaft belegt: ein Fehler, kein Überschreiben.
	attentionIDSource = func() int64 { return existing }
	if _, err := mia.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, SenderExternalID: "claude:mia",
		ClientID: "third", Body: "q", Intent: IntentQuestion, Mentions: []string{"claude:lena"}}); err == nil || errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("exhausted retries must fail, got %v", err)
	}
}

// Die Reihenfolge hängt nicht an der Id: alte (kleine) und neue (zufällige)
// Ids gemischt, neueste zuerst nach created_at und message_id.
func TestAttentionOrderDoesNotDependOnIDs(t *testing.T) {
	st, mia, room := attentionFixtureRoom(t)
	lena := st.CoordinationFor(Principal{ID: "person:2", Label: "lena"}, "claude:lena")
	var msgs []int64
	for _, c := range []string{"m1", "m2", "m3", "m4"} {
		id, err := mia.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, SenderExternalID: "claude:mia",
			ClientID: c, Body: c, Intent: IntentQuestion, Mentions: []string{"claude:lena"}, CreatedAt: "2026-10-01T10:00:00Z"})
		if err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, id)
	}
	// m1 und m3 behalten kleine Ids (Altbestand), m2 und m4 zufällige. Umgekehrte
	// Id-Reihenfolge zur Nachrichtenfolge: nach Id sortiert käme es falsch heraus.
	for i, small := range map[int]int64{0: 4, 2: 2} {
		if _, err := st.db.Exec(`UPDATE coord_attention SET id=? WHERE message_id=?`, small, msgs[i]); err != nil {
			t.Fatal(err)
		}
	}
	items, err := lena.Attention()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("items=%d", len(items))
	}
	for i, want := range []int64{msgs[3], msgs[2], msgs[1], msgs[0]} {
		if items[i].MessageID != want {
			t.Errorf("position %d: message %d, want %d", i, items[i].MessageID, want)
		}
	}
	// Auflösen funktioniert mit jeder Id.
	for _, it := range items {
		if err := lena.ResolveAttention(it.ID, AttentionActionDismiss); err != nil {
			t.Errorf("resolve %d: %v", it.ID, err)
		}
	}
}
