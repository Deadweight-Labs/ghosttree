package store

import "testing"

func askQuestion(t *testing.T, author CoordAccess, room, client string, to ...string) int64 {
	t.Helper()
	id, err := author.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: client,
		Body: "Ship it?", Intent: IntentQuestion, Mentions: to})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func attentionStateOf(t *testing.T, a CoordAccess, messageID int64) string {
	t.Helper()
	items, err := a.Attention()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.MessageID == messageID && it.IsRecipient {
			return it.State
		}
	}
	t.Fatalf("no attention item for message %d in %+v", messageID, items)
	return ""
}

// A reply of the person asked closes the question (REQ-679 AC-1); a plain
// message in the room does not, and a reply of somebody else does not either.
func TestReplyOfTheAskedClosesTheQuestion(t *testing.T) {
	_, author, recipient, other, room := attentionGroupFixture(t)
	q := askQuestion(t, author, room, "q1", "person:2")

	if _, err := recipient.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "plain", Body: "thinking"}); err != nil {
		t.Fatal(err)
	}
	if got := attentionStateOf(t, recipient, q); got != AttentionOpen {
		t.Fatalf("a plain message closed the question: %s", got)
	}
	if _, err := other.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "o1", Body: "yes", ReplyTo: q}); err != nil {
		t.Fatal(err)
	}
	if got := attentionStateOf(t, recipient, q); got != AttentionOpen {
		t.Fatalf("the reply of somebody else closed the question: %s", got)
	}
	answer, err := recipient.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "a1", Body: "yes, ship", ReplyTo: q})
	if err != nil {
		t.Fatal(err)
	}
	if got := attentionStateOf(t, recipient, q); got != AttentionResolved {
		t.Fatalf("question after the reply: %s", got)
	}
	// The asker still gets the answer: it is an ordinary message of the room.
	msgs, err := author.Store.CoordMessagesSince(DestinationRoom, room, q, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range msgs {
		found = found || (m.ID == answer && m.ReplyTo == q)
	}
	if !found {
		t.Fatalf("the answer is not delivered after the question: %+v", msgs)
	}
}

// An approval is answered by its verdict, not by a reply.
func TestReplyDoesNotCloseAnApproval(t *testing.T) {
	_, author, recipient, _, room := attentionGroupFixture(t)
	id, err := author.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "ap", Body: "Merge?", Intent: IntentApproval, Mentions: []string{"person:2"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recipient.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "r", Body: "let me look", ReplyTo: id}); err != nil {
		t.Fatal(err)
	}
	if got := attentionStateOf(t, recipient, id); got != AttentionOpen {
		t.Fatalf("approval after a reply: %s", got)
	}
}

// A post of the person asked in the thread that hangs on the question closes it.
func TestThreadAnswerOfTheAskedClosesTheQuestion(t *testing.T) {
	_, author, recipient, _, room := attentionGroupFixture(t)
	q := askQuestion(t, author, room, "q2", "person:2")
	threadID, err := author.PromoteRoomMessageToTaskThread(q, "Ship", "Ship it?", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := attentionStateOf(t, recipient, q); got != AttentionOpen {
		t.Fatalf("question before the answer: %s", got)
	}
	if _, err := recipient.ThreadPost(threadID, CoordMessage{ClientID: "t1", Body: "yes"}); err != nil {
		t.Fatal(err)
	}
	if got := attentionStateOf(t, recipient, q); got != AttentionResolved {
		t.Fatalf("question after the thread answer: %s", got)
	}
}

// A counter-question is not an answer: the question stays open.
func TestCounterQuestionDoesNotCloseTheQuestion(t *testing.T) {
	_, author, recipient, _, room := attentionGroupFixture(t)
	q := askQuestion(t, author, room, "q3", "person:2")
	if _, err := recipient.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "c1", Body: "Which one?", ReplyTo: q,
		Intent: IntentQuestion, Mentions: []string{"person:1"}}); err != nil {
		t.Fatal(err)
	}
	if got := attentionStateOf(t, recipient, q); got != AttentionOpen {
		t.Fatalf("a counter-question closed the question: %s", got)
	}
}
