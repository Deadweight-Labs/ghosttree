package store

import "testing"

func TestMessagePresentationWindowProjectsRelatedDataWithinDestination(t *testing.T) {
	s := newCoordAccessStore(t)
	room := RoomKeyForProject("presentation")
	registerAccessAgent(t, s, "person:1", "sess-owner", room)
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "sess-author", PrincipalID: "person:2", DisplayName: "Ada", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	parent, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, SenderExternalID: "sess-author", AuthorPrincipalID: "person:2", AuthorKind: AuthorAgent, ClientID: "parent", Body: "original"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, SenderExternalID: "sess-author", ClientID: "child", Body: "reply", ReplyTo: parent, Mentions: []string{"sess-owner"}, Refs: []CoordRef{{Kind: "request", ID: "REQ-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkCoordDelivery(child, "sess-owner", DeliveryAcked); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, SenderExternalID: "sess-author", ClientID: "child-2", Body: "second reply", ReplyTo: parent}); err != nil {
		t.Fatal(err)
	}
	_, presented, err := s.CoordinationFor(Principal{ID: "person:1"}, "").MessagePresentationWindow(DestinationRoom, room, LatestWindow(50))
	if err != nil {
		t.Fatal(err)
	}
	if len(presented) != 3 {
		t.Fatalf("presented=%+v", presented)
	}
	if presented[0].ReplyCount != 2 {
		t.Fatalf("parent reply count=%d, want 2", presented[0].ReplyCount)
	}
	got := presented[1]
	if got.AuthorLabel != "Ada" || got.Reply == nil || got.Reply.Author != "Ada" || got.Reply.Body != "original" || got.Reply.Missing {
		t.Fatalf("author/reply=%+v", got)
	}
	if len(got.Mentions) != 1 || len(got.Refs) != 1 || got.Delivery.Acked != 1 {
		t.Fatalf("related projection=%+v", got)
	}

	other := RoomKeyForProject("other")
	registerAccessAgent(t, s, "person:1", "sess-other", other)
	foreign, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: other, SenderExternalID: "sess-author", ClientID: "foreign", Body: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, SenderExternalID: "sess-author", ClientID: "cross", Body: "cross", ReplyTo: foreign}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: other, SenderExternalID: "sess-author", ClientID: "foreign-cross", Body: "must not count", ReplyTo: parent}); err != nil {
		t.Fatal(err)
	}
	_, presented, err = s.CoordinationFor(Principal{ID: "person:1"}, "").MessagePresentationWindow(DestinationRoom, room, LatestWindow(50))
	if err != nil {
		t.Fatal(err)
	}
	last := presented[len(presented)-1]
	if last.Reply == nil || !last.Reply.Missing || last.Reply.Body != "" {
		t.Fatalf("cross-target reply leaked: %+v", last.Reply)
	}
	if last.ReplyCount != 0 {
		t.Fatalf("missing parent gained a reply count: %d", last.ReplyCount)
	}
	if presented[0].ReplyCount != 2 {
		t.Fatalf("foreign-destination child changed reply count: %d", presented[0].ReplyCount)
	}
}
