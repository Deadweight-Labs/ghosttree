package store

import (
	"database/sql"
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
	// The rate limit has its own test; the others look at single cycles.
	previous := waitNoteInterval
	waitNoteInterval = 0
	t.Cleanup(func() { waitNoteInterval = previous })
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
	principal := "person:4"
	_ = st.db.QueryRow(`SELECT principal_id FROM coord_agents WHERE external_id=?`, from).Scan(&principal)
	if _, err := st.CoordinationFor(Principal{ID: principal}, from).Send(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: room, ClientID: fmt.Sprintf("w%d", waitSeq),
		Body: "ping", Intent: intent, Mentions: []string{to},
	}); err != nil {
		t.Fatal(err)
	}
}

func answerOne(t *testing.T, st *Store, who string) {
	t.Helper()
	principal := "person:4"
	_ = st.db.QueryRow(`SELECT principal_id FROM coord_agents WHERE external_id=?`, who).Scan(&principal)
	acc := st.CoordinationFor(Principal{ID: principal}, who)
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
	if !strings.Contains(line, "mutual wait: "+waitA+" ↔ "+waitB+" (since ") {
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
	if len(cs) != 1 || !cs[0].Overdue || cs[0].ReviewAt != old.ReviewAt || !strings.Contains(cs[0].Describe(nil), "overdue") {
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
		presenceBatch(c, time.Now().UTC(), room, agents, nil)
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

func TestSystemPrefixIsReservedForRegistrationAndSending(t *testing.T) {
	st, room := waitFixture(t)
	for _, id := range []string{"system:wait-cycle", "System:x", "system:anything"} {
		if ValidExternalID(id) {
			t.Fatalf("%q is a valid client id", id)
		}
		_, err := st.RegisterCoordAgent(CoordAgent{ExternalID: id, Provider: "claude", RoomKey: room, DisplayName: id, PrincipalID: "person:4", Role: "member"})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("registering %q: %v", id, err)
		}
	}
	// An agent that already carries the prefix in an old database is kept, but
	// cannot send as the system.
	if _, err := st.db.Exec(`INSERT INTO coord_agents(external_id,provider,room_key,display_name,principal_id,registered_at,last_seen_at)
		VALUES('system:old','claude',?, 'old','person:4',?,?)`, room, now(), now()); err != nil {
		t.Skipf("legacy fixture: %v", err)
	}
	if _, err := st.db.Exec(`INSERT INTO coord_room_memberships(room_key,principal_id,joined_at,left_at) VALUES(?, 'system:old', ?, '')`, room, now()); err != nil {
		t.Skipf("legacy membership fixture: %v", err)
	}
	_, err := st.CoordinationFor(Principal{ID: "person:4"}, "system:old").Send(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: room, ClientID: "x", Body: "forged", Intent: IntentAttention,
	})
	if !errors.Is(err, ErrCoordForbidden) {
		t.Fatalf("sending as system:old: %v", err)
	}
}

func TestPreSentClientIDsDoNotSuppressTheNote(t *testing.T) {
	st, room := waitFixture(t)
	// What an old database could hold: messages from the system sender with the
	// ids the note used to derive from the ledger row.
	for i := 1; i <= 5; i++ {
		if _, err := st.AppendCoordMessage(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room,
			SenderExternalID: WaitCycleSender, ClientID: fmt.Sprintf("wait-cycle:%d", i), Body: "squatter"}); err != nil {
			t.Fatal(err)
		}
	}
	ask(t, st, room, waitA, waitB, IntentQuestion)
	ask(t, st, room, waitB, waitA, IntentQuestion)
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM coord_messages WHERE sender_external_id=? AND author_kind=?`, WaitCycleSender, AuthorSystem).Scan(&n)
	if n != 1 {
		t.Fatalf("real notes = %d, want 1", n)
	}
}

func TestAGuestInTheCycleIsStillWoken(t *testing.T) {
	st, room := waitFixture(t)
	registerRoleAgent(t, st, "claude:guest", "person:2", room, "member")
	ask(t, st, room, "claude:guest", waitA, IntentQuestion)
	ask(t, st, room, waitA, "claude:guest", IntentQuestion)
	notes := cycleNotes(t, st)
	if len(notes) != 1 {
		t.Fatalf("notes = %d", len(notes))
	}
	registerRoleAgent(t, st, "claude:bystander", "person:2", room, "member")
	if err := st.SetProjectRole("person:1", roleProject, "person:2", RoleGuest, false, RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	st.SetAccessMode(AccessMode{Enforce: true})
	m, _ := noteMessage(t, st, notes[0])
	now := time.Now().UTC()
	// The guest agent is a recipient: it sees itself, and nobody else.
	mine, err := st.CoordinationFor(Principal{ID: "person:2"}, "claude:guest").MessageMentions(notes[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(mine, " "), waitA) || !ShouldWake("claude:guest", RoomProject, m, mine, WakeParentNotOwn, now) {
		t.Fatalf("recipient view = %v", mine)
	}
	// A guest who is not in the cycle learns nothing and is not woken.
	other, err := st.CoordinationFor(Principal{ID: "person:2"}, "claude:bystander").MessageMentions(notes[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(other, " "), "claude:guest") || strings.Contains(strings.Join(other, " "), waitA) ||
		ShouldWake("claude:bystander", RoomProject, m, other, WakeParentNotOwn, now) {
		t.Fatalf("bystander view = %v", other)
	}
}

func TestShrinkingCycleDoesNotNotifyAgain(t *testing.T) {
	st, room := waitFixture(t)
	ask(t, st, room, waitA, waitB, IntentQuestion)
	ask(t, st, room, waitB, waitA, IntentQuestion)
	ask(t, st, room, waitB, waitC, IntentQuestion)
	ask(t, st, room, waitC, waitB, IntentQuestion)
	if c := cycleOf(t, st, room, waitC); c == nil || len(c.Members) != 3 {
		t.Fatalf("setup: %+v", c)
	}
	before := len(cycleNotes(t, st))
	answerOne(t, st, waitC) // C answers B: B no longer waits on C, C leaves the cycle
	if c := cycleOf(t, st, room, waitA); c == nil || len(c.Members) != 2 {
		t.Fatalf("shrunk cycle = %+v", c)
	}
	if n := len(cycleNotes(t, st)); n != before {
		t.Fatalf("a shrunk cycle notified again: %d -> %d", before, n)
	}
	// C comes back: it left, so it is newly added and the cycle is announced
	// again (the rate limit bounds how often that can happen).
	ask(t, st, room, waitB, waitC, IntentQuestion)
	if n := len(cycleNotes(t, st)); n != before+1 {
		t.Fatalf("a member added to the cycle must notify: %d -> %d", before, n)
	}
}

func TestNewMemberInAStandingCycleNotifiesOnce(t *testing.T) {
	st, room := waitFixture(t)
	ask(t, st, room, waitA, waitB, IntentQuestion)
	ask(t, st, room, waitB, waitA, IntentQuestion)
	if n := len(cycleNotes(t, st)); n != 1 {
		t.Fatalf("notes = %d", n)
	}
	// C is pulled in: A -> C -> B closes a bigger ring around the old one.
	ask(t, st, room, waitB, waitC, IntentQuestion)
	ask(t, st, room, waitC, waitB, IntentQuestion)
	if n := len(cycleNotes(t, st)); n != 2 {
		t.Fatalf("a new member must notify: %d notes", n)
	}
	ask(t, st, room, waitC, waitA, IntentQuestion)
	if n := len(cycleNotes(t, st)); n != 2 {
		t.Fatalf("no new member, no new note: %d", n)
	}
}

func heldCount(st *Store) int {
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM coord_wait_cycles WHERE held=1 AND dissolved_at=''`).Scan(&n)
	return n
}

func TestGuestCycleDoesNotTakeTheSlotOfAMemberCycle(t *testing.T) {
	st, room := waitFixture(t)
	waitNoteInterval = 10 * time.Minute
	registerRoleAgent(t, st, "claude:g:1", "person:4", room, "guest")
	registerRoleAgent(t, st, "claude:g:2", "person:4", room, "guest")
	ask(t, st, room, "claude:g:1", "claude:g:2", IntentQuestion)
	ask(t, st, room, "claude:g:2", "claude:g:1", IntentQuestion)
	if n := len(cycleNotes(t, st)); n != 1 {
		t.Fatalf("guest cycle notes = %d", n)
	}
	ask(t, st, room, waitA, waitB, IntentQuestion)
	ask(t, st, room, waitB, waitA, IntentQuestion)
	if n := len(cycleNotes(t, st)); n != 2 {
		t.Fatalf("the member cycle was not announced: %d notes", n)
	}
}

func TestTwoDifferentCyclesWithinTheWindowAreBothAnnounced(t *testing.T) {
	st, room := waitFixture(t)
	waitNoteInterval = 10 * time.Minute
	registerRoleAgent(t, st, "claude:h:d", "person:4", room, "member")
	registerRoleAgent(t, st, "claude:h:e", "person:4", room, "member")
	ask(t, st, room, waitA, waitB, IntentQuestion)
	ask(t, st, room, waitB, waitA, IntentQuestion)
	ask(t, st, room, "claude:h:d", "claude:h:e", IntentQuestion)
	ask(t, st, room, "claude:h:e", "claude:h:d", IntentQuestion)
	if n := len(cycleNotes(t, st)); n != 2 {
		t.Fatalf("notes = %d, want 2", n)
	}
	if heldCount(st) != 0 {
		t.Fatal("nothing should be held back")
	}
}

func TestTheSameSetWithinTheWindowIsHeldBackAndSentLaterIfStillActive(t *testing.T) {
	st, room := waitFixture(t)
	waitNoteInterval = 10 * time.Minute
	ask(t, st, room, waitA, waitB, IntentQuestion)
	ask(t, st, room, waitB, waitA, IntentQuestion)
	answerOne(t, st, waitA)
	answerOne(t, st, waitB) // dissolved
	ask(t, st, room, waitA, waitB, IntentQuestion)
	ask(t, st, room, waitB, waitA, IntentQuestion) // the same set again, inside the window
	if n := len(cycleNotes(t, st)); n != 1 {
		t.Fatalf("notes = %d, want 1 inside the window", n)
	}
	if heldCount(st) != 1 {
		t.Fatalf("held = %d, want 1", heldCount(st))
	}
	// Still inside the window: another reconcile does not send it.
	ask(t, st, room, waitC, waitA, IntentQuestion)
	if n := len(cycleNotes(t, st)); n != 1 {
		t.Fatalf("sent early: %d", n)
	}
	// The window passes; the cycle is still active, the next reconcile sends.
	if _, err := st.db.Exec(`UPDATE coord_wait_cycles SET notified_at=? WHERE notified_message_id<>0`, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	ask(t, st, room, waitC, waitB, IntentQuestion)
	if n := len(cycleNotes(t, st)); n != 2 || heldCount(st) != 0 {
		t.Fatalf("notes = %d held = %d, want 2 and 0", n, heldCount(st))
	}
}

func TestAHeldNoteIsDroppedWhenTheCycleDissolvesFirst(t *testing.T) {
	st, room := waitFixture(t)
	waitNoteInterval = 10 * time.Minute
	ask(t, st, room, waitA, waitB, IntentQuestion)
	ask(t, st, room, waitB, waitA, IntentQuestion)
	answerOne(t, st, waitA)
	answerOne(t, st, waitB)
	ask(t, st, room, waitA, waitB, IntentQuestion)
	ask(t, st, room, waitB, waitA, IntentQuestion)
	if heldCount(st) != 1 {
		t.Fatalf("held = %d", heldCount(st))
	}
	answerOne(t, st, waitA)
	answerOne(t, st, waitB)
	if heldCount(st) != 0 || len(cycleNotes(t, st)) != 1 {
		t.Fatalf("held=%d notes=%d", heldCount(st), len(cycleNotes(t, st)))
	}
}

func TestAFailedRollbackAbortsTheSend(t *testing.T) {
	st, room := waitFixture(t)
	if _, err := st.db.Exec(`DROP TABLE coord_wait_cycles`); err != nil {
		t.Fatal(err)
	}
	previous := execWaitSavepoint
	execWaitSavepoint = func(tx *sql.Tx, q string) error {
		if strings.HasPrefix(q, "ROLLBACK TO") {
			return errors.New("simulated rollback failure")
		}
		return previous(tx, q)
	}
	t.Cleanup(func() { execWaitSavepoint = previous })
	_, err := st.CoordinationFor(Principal{ID: "person:4"}, waitA).Send(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: room, ClientID: "r1", Body: "x", Intent: IntentQuestion, Mentions: []string{waitB},
	})
	if err == nil || !strings.Contains(err.Error(), "simulated rollback failure") {
		t.Fatalf("send = %v", err)
	}
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM coord_messages WHERE client_id='r1'`).Scan(&n)
	if n != 0 {
		t.Fatal("the message was committed although the bookkeeping state is unknown")
	}
}

func TestOpenAttentionOfOtherRoomsIsNotScanned(t *testing.T) {
	st, room := waitFixture(t)
	const other = "github.com/dw/other"
	if _, err := st.EnsureProject("person:1", other); err != nil {
		t.Fatal(err)
	}
	otherRoom := RoomKeyForProject(other)
	registerRoleAgent(t, st, "claude:o:1", "person:1", otherRoom, "member")
	registerRoleAgent(t, st, "claude:o:2", "person:1", otherRoom, "member")
	for i := 0; i < 30; i++ {
		ask(t, st, otherRoom, "claude:o:1", "claude:o:2", IntentQuestion)
	}
	ask(t, st, room, waitA, waitB, IntentQuestion)

	rows, err := loadWaitRows(st.db, time.Now().UTC(), room, []any{waitA, "claude:o:1"})
	if err != nil || len(rows) != 1 || rows[0].Sender != waitA {
		t.Fatalf("rows = %+v err = %v", rows, err)
	}
	// The plan walks the index of this room only.
	args := []any{room, AttentionQuestion, AttentionApproval, AttentionBlocker, waitA}
	plan, err := st.db.Query(`EXPLAIN QUERY PLAN `+presenceWaitsSQL(1), args...)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	found := false
	for plan.Next() {
		var id, parent, unused int
		var detail string
		plan.Scan(&id, &parent, &unused, &detail)
		found = found || strings.Contains(detail, "coord_attention_open_room (room_key=?")
		if strings.HasPrefix(detail, "SCAN") {
			t.Fatalf("scan: %s", detail)
		}
	}
	if !found {
		t.Fatal("the wait query does not use the room index")
	}
}

func TestOldOpenAttentionRowsGetTheirRoomKey(t *testing.T) {
	st, room := waitFixture(t)
	ask(t, st, room, waitA, waitB, IntentQuestion)
	if _, err := st.db.Exec(`UPDATE coord_attention SET room_key=''`); err != nil {
		t.Fatal(err)
	}
	if err := ensureCoordWaitCycles(st.db); err != nil {
		t.Fatal(err)
	}
	var got string
	st.db.QueryRow(`SELECT room_key FROM coord_attention LIMIT 1`).Scan(&got)
	if got != room {
		t.Fatalf("room_key = %q, want %q", got, room)
	}
}

func TestAFailingCycleBookkeepingDoesNotBlockTheSend(t *testing.T) {
	st, room := waitFixture(t)
	if _, err := st.db.Exec(`DROP TABLE coord_wait_cycles`); err != nil {
		t.Fatal(err)
	}
	ask(t, st, room, waitA, waitB, IntentQuestion) // ask fails the test on an error
	ask(t, st, room, waitB, waitA, IntentQuestion)
	if n := len(cycleNotes(t, st)); n != 0 {
		t.Fatalf("notes = %d", n)
	}
	var open int
	st.db.QueryRow(`SELECT COUNT(*) FROM coord_attention WHERE state='open'`).Scan(&open)
	if open != 2 {
		t.Fatalf("the questions were not stored: %d", open)
	}
}

func TestWaitQueryChunksLargeSenderLists(t *testing.T) {
	st, room := waitFixture(t)
	ask(t, st, room, waitA, waitB, IntentQuestion)
	senders := []any{waitA}
	for i := 0; i < 1300; i++ {
		senders = append(senders, fmt.Sprintf("claude:h:bulk%d", i))
	}
	rows, err := loadWaitRows(st.db, time.Now().UTC(), room, senders)
	if err != nil || len(rows) != 1 || rows[0].Kind != "peer" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

func TestCycleShowsForEveryMemberEvenWhenPeersAreFilteredBySince(t *testing.T) {
	st, room := waitFixture(t)
	ask(t, st, room, waitA, waitB, IntentQuestion)
	ask(t, st, room, waitB, waitA, IntentQuestion)
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if _, err := st.db.Exec(`UPDATE coord_agents SET last_seen_at=? WHERE external_id=?`, old, waitB); err != nil {
		t.Fatal(err)
	}
	since := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	peers, err := st.CoordPeers(room, since)
	if err != nil {
		t.Fatal(err)
	}
	var sawA, sawB bool
	for _, p := range peers {
		sawA = sawA || p.ExternalID == waitA
		sawB = sawB || p.ExternalID == waitB
		if p.ExternalID == waitA && (p.Presence.Cycle == nil || len(p.Presence.Cycle.Members) != 2) {
			t.Fatalf("filtered listing lost the cycle: %+v", p.Presence.Cycle)
		}
	}
	if !sawA || sawB {
		t.Fatalf("fixture: sawA=%v sawB=%v", sawA, sawB)
	}
}
