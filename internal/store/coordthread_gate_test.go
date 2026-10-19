package store

import (
	"bytes"
	"log/slog"
	"strconv"
	"testing"
)

// Ein eingeschränkter Alt-Thread nennt seine Leser in thread_visibility. Das
// ersetzt die Projektrolle nicht: wer sie verliert, liest den Thread nicht mehr.
func TestRestrictedThreadNeedsTheProjectRole(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	if _, err := st.RegisterCoordAgent(CoordAgent{ExternalID: "claude:mia", Provider: "claude", RoomKey: RoomKeyForProject(roleProject), PrincipalID: "person:3", Person: "mia"}); err != nil {
		t.Fatal(err)
	}
	tid, err := st.CreateThread(Thread{Project: roleProject, Title: "private question"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO thread_visibility(thread_id,member_external_id) VALUES(?,?)`, tid, "claude:mia"); err != nil {
		t.Fatal(err)
	}
	mia := st.CoordinationFor(Principal{ID: "person:3", Label: "mia"}, "claude:mia")
	dest := strconv.FormatInt(tid, 10)
	if _, _, err := mia.MessagePresentationWindow(DestinationDiscussion, dest, MessageWindow{Mode: messageWindowLatest}); err != nil {
		t.Fatalf("member with role reads the restricted thread: %v", err)
	}
	if err := st.RemoveProjectRole("person:1", roleProject, "person:3", RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mia.MessagePresentationWindow(DestinationDiscussion, dest, MessageWindow{Mode: messageWindowLatest}); err == nil {
		t.Fatal("role removed, but the restricted thread is still readable")
	}

	// Logmodus: altes Verhalten plus "would deny".
	var buf bytes.Buffer
	st.SetAccessMode(AccessMode{Logger: slog.New(slog.NewTextHandler(&buf, nil))})
	if _, _, err := mia.MessagePresentationWindow(DestinationDiscussion, dest, MessageWindow{Mode: messageWindowLatest}); err != nil {
		t.Fatalf("log mode must keep the old behaviour: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("access: would deny")) {
		t.Errorf("expected a would-deny line, got %q", buf.String())
	}
}
