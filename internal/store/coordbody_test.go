package store

import (
	"errors"
	"strings"
	"testing"
)

// A body of exactly the limit is stored; one byte more is refused and nothing is
// written, through the same access every sender uses (the page, the API).
func TestCoordSendRefusesABodyOverTheLimit(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("github.com/x/y")
	registerAccessAgent(t, s, "person:1", "sess-a", room)
	access := s.CoordinationFor(Principal{ID: "person:1", Label: "owner"}, "sess-a")
	msg := func(client string, body string) CoordMessage {
		return CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, SenderExternalID: "sess-a", ClientID: client, Body: body}
	}

	if _, err := access.Send(msg("fits", strings.Repeat("a", CoordMaxBody))); err != nil {
		t.Fatalf("a body of the limit: %v", err)
	}
	_, err := access.Send(msg("over", strings.Repeat("a", CoordMaxBody+1)))
	if !errors.Is(err, ErrCoordBodyTooLong) {
		t.Fatalf("a body over the limit: want ErrCoordBodyTooLong, got %v", err)
	}
	// The limit counts bytes, not characters: 32769 two-byte letters are 65538 bytes.
	if _, err := access.Send(msg("bytes", strings.Repeat("ä", CoordMaxBody/2+1))); !errors.Is(err, ErrCoordBodyTooLong) {
		t.Fatalf("a body over the limit in bytes: got %v", err)
	}
	got, err := access.Messages(DestinationRoom, room, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ClientID != "fits" {
		t.Fatalf("only the message within the limit is stored: %+v", got)
	}
}
