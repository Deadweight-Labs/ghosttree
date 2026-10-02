package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	waitA = "claude:h:a"
	waitB = "claude:h:b"
	waitC = "claude:h:c"
)

func waitFixture(t *testing.T) (*Store, string) {
	t.Helper()
	st := controlFixture(t)
	room := RoomKeyForProject(roleProject)
	for _, id := range []string{waitA, waitB, waitC} {
		registerRoleAgent(t, st, id, "person:4", room, "member")
	}
	return st, room
}

var waitSeq int

// ask: from asks to (an open question in the project room); returns the
// attention id the recipient can resolve.
func ask(t *testing.T, st *Store, room, from, to, intent string) {
	t.Helper()
	waitSeq++
	if _, err := st.CoordinationFor(Principal{ID: "person:4"}, from).Send(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: room, ClientID: fmt.Sprintf("w%d", waitSeq),
		Body: "ping", Intent: intent, Mentions: []string{to},
	}); err != nil {
		t.Fatal(err)
	}
}

func answerOne(t *testing.T, st *Store, who string) {
	t.Helper()
	acc := st.CoordinationFor(Principal{ID: "person:4"}, who)
	items, err := acc.Attention()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.IsRecipient && it.State == AttentionOpen {
			action := AttentionActionAnswer
			switch it.Reason {
			case AttentionBlocker:
				action = AttentionActionResolve
			case AttentionApproval:
				action = AttentionActionApprove
			}
			if err := acc.ResolveAttention(it.ID, action); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("%s has nothing open", who)
}

func cycleOf(t *testing.T, st *Store, room, agent string) *WaitCycle {
	t.Helper()
	return presenceOf(t, st, room, agent).Cycle
}

func cycleNotes(t *testing.T, st *Store) []int64 {
	t.Helper()
	rows, err := st.db.Query(`SELECT id FROM coord_messages WHERE sender_external_id=? ORDER BY id`, WaitCycleSender)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids
}

func TestMutualWaitBetweenTwoIsDetected(t *testing.T) {
	st, room := waitFixture(t)
	ask(t, st, room, waitA, waitB, IntentQuestion)
	if c := cycleOf(t, st, room, waitA); c != nil {
		t.Fatalf("one-sided waiting is no cycle: %+v", c)
	}
	ask(t, st, room, waitB, waitA, IntentApproval)
	for _, who := range []string{waitA, waitB} {
		c := cycleOf(t, st, room, who)
		if c == nil || len(c.Members) != 2 || c.Since == "" {
			t.Fatalf("%s: cycle = %+v", who, c)
		}
		// The hint complements waiting_peer, it does not replace it.
		if p := presenceOf(t, st, room, who); p.WorkState.Value != WorkWaitingPeer {
			t.Fatalf("%s work = %+v", who, p.WorkState)
		}
	}
	if c := cycleOf(t, st, room, waitC); c != nil {
		t.Fatalf("a bystander is no part of the cycle: %+v", c)
	}
	line := cycleOf(t, st, room, waitA).Describe(nil)
	if !strings.Contains(line, "gegenseitiges Warten: "+waitA+" ↔ "+waitB+" (seit ") {
		t.Fatalf("line = %q", line)
	}
}

func TestMutualWaitInAThreeCycle(t *testing.T) {
	st, room := waitFixture(t)
	ask(t, st, room, waitA, waitB, IntentQuestion)
	ask(t, st, room, waitB, waitC, IntentBlocker)
	if cycleOf(t, st, room, waitA) != nil {
		t.Fatal("a chain is no cycle")
	}
	ask(t, st, room, waitC, waitA, IntentQuestion)
	for _, who := range []string{waitA, waitB, waitC} {
		c := cycleOf(t, st, room, who)
		if c == nil || len(c.Members) != 3 || !c.Ordered {
			t.Fatalf("%s: cycle = %+v", who, c)
		}
	}
	if line := cycleOf(t, st, room, waitB).Describe(nil); !strings.Contains(line, waitA+" → "+waitB+" → "+waitC+" → "+waitA) {
		t.Fatalf("line = %q", line)
	}
}

func TestNoCycleOnceOneSideIsAnswered(t *testing.T) {
	st, room := waitFixture(t)
	ask(t, st, room, waitA, waitB, IntentQuestion)
	ask(t, st, room, waitB, waitA, IntentQuestion)
	if cycleOf(t, st, room, waitA) == nil {
		t.Fatal("setup: no cycle")
	}
	answerOne(t, st, waitA) // A answers B's question: B no longer waits
	for _, who := range []string{waitA, waitB} {
		if c := cycleOf(t, st, room, who); c != nil {
			t.Fatalf("%s still in a cycle: %+v", who, c)
		}
	}
	// A handoff is not a wait.
	ask(t, st, room, waitB, waitA, IntentHandoff)
	if cycleOf(t, st, room, waitA) != nil {
		t.Fatal("a handoff closed a cycle")
	}
}

func TestExpiredWaitNoLongerClosesACycle(t *testing.T) {
	st, room := waitFixture(t)
	ask(t, st, room, waitA, waitB, IntentQuestion)
	ask(t, st, room, waitB, waitA, IntentQuestion)
	if _, err := st.db.Exec(`UPDATE coord_messages SET expires_at=? WHERE sender_external_id=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), waitB); err != nil {
		t.Fatal(err)
	}
	if c := cycleOf(t, st, room, waitA); c != nil {
		t.Fatalf("an expired wait closes a cycle: %+v", c)
	}
}

func TestWaitEdgeReviewDateAndOverdue(t *testing.T) {
	ref := presenceRef
	young, _ := waitRow{Sender: "a", Recipient: "b", Reason: AttentionQuestion, At: ago(10 * time.Minute), Kind: "peer"}.edge(ref)
	if !young.ReviewDerived || young.Overdue || young.ReviewAt != ago(-20*time.Minute) {
		t.Fatalf("young edge = %+v", young)
	}
	old, _ := waitRow{Sender: "b", Recipient: "a", Reason: AttentionQuestion, At: ago(45 * time.Minute), Kind: "peer"}.edge(ref)
	if !old.Overdue || old.ReviewAt != ago(15*time.Minute) {
		t.Fatalf("old edge = %+v", old)
	}
	own, _ := waitRow{Sender: "a", Recipient: "b", Reason: AttentionQuestion, At: ago(45 * time.Minute), Expires: ago(-time.Hour), Kind: "peer"}.edge(ref)
	if own.ReviewDerived || own.Overdue || own.ReviewAt != ago(-time.Hour) {
		t.Fatalf("an own expiry is the review date: %+v", own)
	}
	if _, ok := (waitRow{Sender: "a", Recipient: "person:1", Kind: "user"}).edge(ref); ok {
		t.Fatal("a wait on a person is no agent edge")
	}
	cs := DetectWaitCycles([]WaitEdge{young, old})
	if len(cs) != 1 || !cs[0].Overdue || cs[0].ReviewAt != old.ReviewAt || !strings.Contains(cs[0].Describe(nil), "überfällig") {
		t.Fatalf("cycle = %+v", cs)
	}
	// Overdue is marked, not closed: the cycle is still reported.
	if cs[0].Since != young.Since {
		t.Fatalf("since is the youngest edge: %+v", cs[0])
	}
}

func TestOverdueWaitIsMarkedInThePresence(t *testing.T) {
	st, room := waitFixture(t)
	ask(t, st, room, waitA, waitB, IntentQuestion)
	ask(t, st, room, waitB, waitA, IntentQuestion)
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if _, err := st.db.Exec(`UPDATE coord_attention SET created_at=?`, old); err != nil {
		t.Fatal(err)
	}
	c := cycleOf(t, st, room, waitA)
	if c == nil || !c.Overdue || !c.ReviewDerived {
		t.Fatalf("cycle = %+v", c)
	}
}

func TestWaitCycleVisibility(t *testing.T) {
	st, room := waitFixture(t)
	ask(t, st, room, waitA, waitB, IntentQuestion)
	ask(t, st, room, waitB, waitA, IntentQuestion)
	if err := st.SetProjectRole("person:1", roleProject, "person:2", RoleGuest, false, RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	st.SetAccessMode(AccessMode{Enforce: true})

	// A member sees it; a guest gets no peer list, so no cycle.
	if peers, err := st.CoordinationFor(Principal{ID: "person:4"}, waitC).Peers(room, ""); err != nil || len(peers) == 0 {
		t.Fatalf("member peers: %v", err)
	} else {
		found := false
		for _, p := range peers {
			found = found || (p.Presence != nil && p.Presence.Cycle != nil)
		}
		if !found {
			t.Fatal("the member does not see the cycle")
		}
	}
	registerRoleAgent(t, st, "claude:guest", "person:2", room, "guest")
	guest := st.CoordinationFor(Principal{ID: "person:2"}, "claude:guest")
	if _, err := guest.Peers(room, ""); !errors.Is(err, ErrCoordNotFound) {
		t.Fatalf("guest peers: %v", err)
	}
	// The room message does not name who waits, not even through mentions.
	_, presented, err := guest.MessagePresentationWindow(DestinationRoom, room, MessageWindow{Mode: messageWindowLatest, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, p := range presented {
		if p.Message.SenderExternalID != WaitCycleSender {
			continue
		}
		seen++
		blob := p.Message.Body + strings.Join(p.Mentions, " ")
		if strings.Contains(blob, waitA) || strings.Contains(blob, waitB) {
			t.Fatalf("guest sees who waits: %q", blob)
		}
	}
	if seen != 1 {
		t.Fatalf("expected the one cycle note in the room, got %d", seen)
	}
}

func TestEdgesInARestrictedThreadAreNotPartOfTheRoomPicture(t *testing.T) {
	st, room := waitFixture(t)
	thID, err := st.CreateThread(Thread{Project: roleProject, Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO thread_homes(thread_id,room_key,created_at) VALUES(?,?,?)`, thID, room, now()); err != nil {
		t.Fatal(err)
	}
	for i, pair := range [][2]string{{waitA, waitB}, {waitB, waitA}} {
		if _, err := st.CoordinationFor(Principal{ID: "person:4"}, pair[0]).ThreadPost(thID, CoordMessage{
			ClientID: fmt.Sprintf("t%d", i), Body: "private", Intent: IntentQuestion, Mentions: []string{pair[1]},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if cycleOf(t, st, room, waitA) == nil {
		t.Fatal("control: a thread homed in the room closes a cycle")
	}
	if _, err := st.db.Exec(`INSERT INTO thread_visibility(thread_id,member_external_id) VALUES(?,?)`, thID, waitA); err != nil {
		t.Fatal(err)
	}
	if c := cycleOf(t, st, room, waitA); c != nil {
		t.Fatalf("a restricted thread leaks into the room picture: %+v", c)
	}
}

func TestCycleNotificationIsSentOncePerCycleThroughTheWakeRule(t *testing.T) {
	st, room := waitFixture(t)
	ask(t, st, room, waitA, waitB, IntentQuestion)
	if n := len(cycleNotes(t, st)); n != 0 {
		t.Fatalf("no notification before the cycle forms: %d", n)
	}
	ask(t, st, room, waitB, waitA, IntentQuestion)
	notes := cycleNotes(t, st)
	if len(notes) != 1 {
		t.Fatalf("cycle formed: %d notifications", len(notes))
	}
	// More traffic inside the standing cycle does not notify again.
	ask(t, st, room, waitA, waitB, IntentApproval)
	ask(t, st, room, waitB, waitA, IntentBlocker)
	if n := len(cycleNotes(t, st)); n != 1 {
		t.Fatalf("a standing cycle notified again: %d", n)
	}

	// The note goes through the one wake rule: it wakes exactly the members.
	m, mentions := noteMessage(t, st, notes[0])
	now := time.Now().UTC()
	for _, who := range []string{waitA, waitB} {
		if !ShouldWake(who, RoomProject, m, mentions, WakeParentNotOwn, now) {
			t.Fatalf("%s is not woken", who)
		}
	}
	if ShouldWake(waitC, RoomProject, m, mentions, WakeParentNotOwn, now) {
		t.Fatal("a bystander is woken")
	}
	if m.Intent != IntentAttention || m.AuthorKind != AuthorSystem {
		t.Fatalf("note = %+v", m)
	}
	// It is no wait itself, so it cannot start a loop of its own.
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM coord_attention WHERE message_id=?`, notes[0]).Scan(&n)
	if n != 0 {
		t.Fatalf("the note created %d attention items", n)
	}
	if _, ok := attentionReasonForIntent(IntentAttention); ok {
		t.Fatal("the note intent must not be a wait")
	}

	// The cycle dissolves ...
	answerOne(t, st, waitA)
	answerOne(t, st, waitA)
	answerOne(t, st, waitB)
	answerOne(t, st, waitB)
	if n := len(cycleNotes(t, st)); n != 1 {
		t.Fatalf("dissolving must not notify: %d", n)
	}
	// ... and forms again: a new cycle, a new notification.
	ask(t, st, room, waitA, waitB, IntentQuestion)
	ask(t, st, room, waitB, waitA, IntentQuestion)
	if n := len(cycleNotes(t, st)); n != 2 {
		t.Fatalf("a re-formed cycle: %d notifications, want 2", n)
	}
}

func TestExpiredCycleCountsAsDissolvedWhenItFormsAgain(t *testing.T) {
	st, room := waitFixture(t)
	ask(t, st, room, waitA, waitB, IntentQuestion)
	ask(t, st, room, waitB, waitA, IntentQuestion)
	if _, err := st.db.Exec(`UPDATE coord_messages SET expires_at=? WHERE sender_external_id IN (?,?)`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), waitA, waitB); err != nil {
		t.Fatal(err)
	}
	ask(t, st, room, waitA, waitB, IntentQuestion)
	ask(t, st, room, waitB, waitA, IntentQuestion)
	if n := len(cycleNotes(t, st)); n != 2 {
		t.Fatalf("the lapsed cycle and the new one: %d notifications, want 2", n)
	}
}

func noteMessage(t *testing.T, st *Store, id int64) (CoordMessage, []string) {
	t.Helper()
	msgs, err := st.CoordinationFor(Principal{ID: "person:4"}, waitA).Messages(DestinationRoom, RoomKeyForProject(roleProject), 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.ID == id {
			mentions, err := st.CoordMessageMentions(id)
			if err != nil {
				t.Fatal(err)
			}
			return m, mentions
		}
	}
	t.Fatalf("note %d not readable", id)
	return CoordMessage{}, nil
}

func TestWaitQueriesStayConstantAndUseIndexes(t *testing.T) {
	st, room := waitFixture(t)
	count := func(n int) int {
		var agents []presenceAgent
		for i := 0; i < n; i++ {
			agents = append(agents, presenceAgent{ExternalID: fmt.Sprintf("claude:x:%d-%d", n, i), PrincipalID: "person:4"})
		}
		c := &countingDB{db: st.db}
		presenceBatch(c, time.Now().UTC(), room, agents)
		return c.n
	}
	if a, b := count(2), count(40); a != b || b > 6 {
		t.Fatalf("queries: %d for 2, %d for 40", a, b)
	}
	// The cycle bookkeeping runs on the write path, with a fixed set of queries.
	rows, err := st.db.Query(`EXPLAIN QUERY PLAN SELECT id,cycle_key FROM coord_wait_cycles WHERE room_key=? AND dissolved_at=''`, room)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		rows.Scan(&id, &parent, &unused, &detail)
		if strings.HasPrefix(detail, "SCAN") {
			t.Fatalf("full scan in the cycle ledger: %s", detail)
		}
	}
}
