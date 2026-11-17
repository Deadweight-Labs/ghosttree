package store

import (
	"fmt"
	"testing"
)

// threadWhoFixture: robin (person:1) creates a thread in the project room; the
// agents waitA, waitB of cleo (person:4) and waitC (also cleo) are registered in
// the room.
func threadWhoFixture(t *testing.T) (st *Store, room string, threadID int64, robin CoordAccess) {
	t.Helper()
	st, room = waitFixture(t)
	robin = st.CoordinationFor(web("person:1", "robin"), "")
	id, err := robin.CreateTaskThreadInRoom(room, "Thread", "What do we draw?", "")
	if err != nil {
		t.Fatal(err)
	}
	return st, room, id, robin
}

var threadSeq int

func threadSay(t *testing.T, acc CoordAccess, thread int64, msg CoordMessage) {
	t.Helper()
	threadSeq++
	msg.ClientID = fmt.Sprintf("tw%d", threadSeq)
	if msg.Body == "" {
		msg.Body = "text"
	}
	if _, err := acc.ThreadPost(thread, msg); err != nil {
		t.Fatal(err)
	}
}

func agentAcc(st *Store, id string) CoordAccess {
	return st.CoordinationFor(Principal{ID: "person:4"}, id)
}

// AC-701.2: the participants of a thread are the ones who wrote in it. A member
// of the room or an agent that never wrote is not among them.
func TestThreadAuthorsAreOnlyThoseWhoWrote(t *testing.T) {
	st, _, id, robin := threadWhoFixture(t)
	threadSay(t, agentAcc(st, waitA), id, CoordMessage{})
	threadSay(t, agentAcc(st, waitA), id, CoordMessage{})
	threadSay(t, st.CoordinationFor(web("person:2", "anna"), ""), id, CoordMessage{})
	threadSay(t, robin, id, CoordMessage{})

	got, err := robin.ThreadAuthors(id)
	if err != nil {
		t.Fatal(err)
	}
	// robin created the thread (his guiding question), then anna, waitA; waitB and
	// waitC are in the room and never wrote.
	byKey := map[string]ThreadAuthor{}
	for _, a := range got {
		byKey[a.Kind+"|"+a.PrincipalID+"|"+a.SenderID] = a
	}
	if len(got) != 3 {
		t.Fatalf("authors = %+v, want robin, anna and %s", got, waitA)
	}
	if a := byKey["agent|person:4|"+waitA]; a.Messages != 2 || a.LastAt == "" {
		t.Fatalf("agent %s = %+v, want two messages", waitA, a)
	}
	if _, ok := byKey["human|person:2|"]; !ok {
		t.Fatalf("anna missing: %+v", got)
	}
	if a, ok := byKey["human|person:1|"]; !ok || a.Messages != 1 {
		t.Fatalf("robin = %+v (ok=%v), want one message", a, ok)
	}
	for key := range byKey {
		if key == "agent|person:4|"+waitB || key == "agent|person:4|"+waitC {
			t.Fatalf("%s never wrote but is listed: %+v", key, got)
		}
	}
}

// The person who created the thread with a guiding question is a participant
// even before anybody wrote in it; the order is the order of the first message.
func TestThreadAuthorsIncludeTheCreatorAndKeepOrder(t *testing.T) {
	st, _, id, robin := threadWhoFixture(t)
	got, err := robin.ThreadAuthors(id)
	if err != nil || len(got) != 1 || got[0].Kind != AuthorHuman || got[0].PrincipalID != "person:1" || got[0].Messages != 0 {
		t.Fatalf("empty thread authors = %+v %v", got, err)
	}
	threadSay(t, agentAcc(st, waitB), id, CoordMessage{})
	threadSay(t, agentAcc(st, waitA), id, CoordMessage{})
	got, _ = robin.ThreadAuthors(id)
	if len(got) != 3 || got[1].SenderID != waitB || got[2].SenderID != waitA {
		t.Fatalf("order = %+v, want robin, %s, %s", got, waitB, waitA)
	}
}

// The thread started from a message of the room: the author of that message is
// a participant, and so are the readers of nothing else.
func TestThreadAuthorsCountTheAnchorMessage(t *testing.T) {
	st, room := waitFixture(t)
	robin := st.CoordinationFor(web("person:1", "robin"), "")
	anchor, err := agentAcc(st, waitC).Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, ClientID: "anc", Body: "question from the room"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := robin.PromoteRoomMessageToTaskThread(anchor, "Promoted", "", "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := robin.ThreadAuthors(id)
	if err != nil {
		t.Fatal(err)
	}
	var sawC bool
	for _, a := range got {
		sawC = sawC || a.SenderID == waitC
	}
	if !sawC {
		t.Fatalf("the author of the anchor message is missing: %+v", got)
	}
}

// A thread that does not exist (or that the viewer may not read) tells nothing
// about who wrote in it.
func TestThreadAuthorsRefuseAnUnknownThread(t *testing.T) {
	st, _, _, _ := threadWhoFixture(t)
	acc := st.CoordinationFor(web("person:3", "ben"), "")
	if _, err := acc.ThreadAuthors(999999); err == nil {
		t.Fatal("an unknown thread must be refused")
	}
	if _, err := acc.ThreadWaitCycles(999999); err == nil {
		t.Fatal("an unknown thread must be refused for the waits too")
	}
}

// AC-701.3: only the open items of this thread count. A wait in the room, or in
// another thread, is not a wait of this thread.
func TestThreadWaitCyclesSeeOnlyThisThread(t *testing.T) {
	st, room, id, robin := threadWhoFixture(t)
	other, err := robin.CreateTaskThreadInRoom(room, "Other", "", "")
	if err != nil {
		t.Fatal(err)
	}
	ask := func(from, to string, thread int64) {
		threadSay(t, agentAcc(st, from), thread, CoordMessage{Body: "ping", Intent: IntentQuestion, Mentions: []string{to}})
	}
	// A asks B in this thread; B asks A in the other thread: no cycle in either.
	ask(waitA, waitB, id)
	ask(waitB, waitA, other)
	for _, thread := range []int64{id, other} {
		if got, err := robin.ThreadWaitCycles(thread); err != nil || len(got) != 0 {
			t.Fatalf("thread %d: cycles = %+v %v, want none", thread, got, err)
		}
	}
	// B asks A in this thread too: now there is a cycle here, and only here.
	ask(waitB, waitA, id)
	got, err := robin.ThreadWaitCycles(id)
	if err != nil || len(got) != 1 || len(got[0].Members) != 2 {
		t.Fatalf("cycles = %+v %v, want one of two", got, err)
	}
	if got[0].Since == "" {
		t.Fatalf("a cycle names since when: %+v", got[0])
	}
	if got, _ := robin.ThreadWaitCycles(other); len(got) != 0 {
		t.Fatalf("the other thread has no cycle: %+v", got)
	}
}

// A question to a person is no wait between agents, and waiting on one side is no cycle.
func TestThreadWaitCyclesIgnoreQuestionsToPeople(t *testing.T) {
	st, _, id, robin := threadWhoFixture(t)
	threadSay(t, agentAcc(st, waitA), id, CoordMessage{Body: "ping", Intent: IntentQuestion, Mentions: []string{"person:1"}})
	threadSay(t, agentAcc(st, waitB), id, CoordMessage{Body: "ping", Intent: IntentQuestion, Mentions: []string{waitA}})
	if got, _ := robin.ThreadWaitCycles(id); len(got) != 0 {
		t.Fatalf("question to a person and one-sided waiting are no cycle: %+v", got)
	}
}
