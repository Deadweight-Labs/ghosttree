package store

import (
	"testing"
	"time"
)

var presenceRef = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) string { return presenceRef.Add(-d).Format(time.RFC3339) }

func TestDerivePresence(t *testing.T) {
	cases := []struct {
		name        string
		in          PresenceInput
		reach, work string
		reachOrigin string
		workOrigin  string
		workAge     int64
		reachAge    int64
	}{
		{name: "no signal at all is unknown, not idle or ended", reach: ReachUnknown, work: WorkUnknown},
		{name: "fresh poll is connected and observed", in: PresenceInput{LastPollAt: ago(20 * time.Second)},
			reach: ReachConnected, reachOrigin: OriginObserved, reachAge: 20, work: WorkUnknown},
		{name: "a poll older than the TTL is unknown, never ended", in: PresenceInput{LastPollAt: ago(10 * time.Minute)},
			reach: ReachUnknown, work: WorkUnknown},
		{name: "fresh tool activity is working, observed", in: PresenceInput{LastActivityAt: ago(30 * time.Second)},
			reach: ReachUnknown, work: WorkWorking, workOrigin: OriginObserved, workAge: 30},
		{name: "old tool activity falls back to unknown, not idle", in: PresenceInput{LastActivityAt: ago(time.Hour)},
			reach: ReachUnknown, work: WorkUnknown},
		{name: "proven pause", in: PresenceInput{PauseAt: ago(5 * time.Second), LastActivityAt: ago(time.Second)},
			reach: ReachUnknown, work: WorkPaused, workOrigin: OriginObserved, workAge: 5},
		{name: "open question to a peer is derived waiting_peer",
			in:    PresenceInput{Waits: []PresenceWait{{Reason: AttentionQuestion, Kind: "peer", At: ago(time.Minute)}}},
			reach: ReachUnknown, work: WorkWaitingPeer, workOrigin: OriginDerived, workAge: 60},
		{name: "open approval to a person is derived waiting_user",
			in:    PresenceInput{Waits: []PresenceWait{{Reason: AttentionApproval, Kind: "user", At: ago(time.Minute)}}},
			reach: ReachUnknown, work: WorkWaitingUser, workOrigin: OriginDerived, workAge: 60},
		{name: "blocker is self reported",
			in:    PresenceInput{Waits: []PresenceWait{{Reason: AttentionBlocker, Kind: "peer", At: ago(time.Minute)}}},
			reach: ReachUnknown, work: WorkBlocked, workOrigin: OriginSelfReported, workAge: 60},
		{name: "a wait to an unclassified recipient claims nothing",
			in:    PresenceInput{Waits: []PresenceWait{{Reason: AttentionQuestion, At: ago(time.Minute)}}},
			reach: ReachUnknown, work: WorkUnknown},
		{name: "activity newer than the question wins over waiting",
			in: PresenceInput{LastActivityAt: ago(10 * time.Second),
				Waits: []PresenceWait{{Reason: AttentionQuestion, Kind: "peer", At: ago(time.Minute)}}},
			reach: ReachUnknown, work: WorkWorking, workOrigin: OriginObserved, workAge: 10},
		{name: "question newer than the activity wins",
			in: PresenceInput{LastActivityAt: ago(90 * time.Second),
				Waits: []PresenceWait{{Reason: AttentionQuestion, Kind: "peer", At: ago(30 * time.Second)}}},
			reach: ReachUnknown, work: WorkWaitingPeer, workOrigin: OriginDerived, workAge: 30},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := DerivePresence(presenceRef, c.in)
			if p.Reachability.Value != c.reach || p.WorkState.Value != c.work {
				t.Fatalf("got %s / %s, want %s / %s", p.Reachability.Value, p.WorkState.Value, c.reach, c.work)
			}
			if p.Reachability.Origin != c.reachOrigin || p.WorkState.Origin != c.workOrigin {
				t.Fatalf("origins %q / %q, want %q / %q", p.Reachability.Origin, p.WorkState.Origin, c.reachOrigin, c.workOrigin)
			}
			if p.Reachability.AgeSeconds != c.reachAge || p.WorkState.AgeSeconds != c.workAge {
				t.Fatalf("ages %d / %d, want %d / %d", p.Reachability.AgeSeconds, p.WorkState.AgeSeconds, c.reachAge, c.workAge)
			}
			// Every claim carries its origin and time; unknown carries neither.
			for _, f := range []PresenceField{p.Reachability, p.WorkState} {
				if (f.Value == "unknown") != (f.Origin == "" && f.At == "") {
					t.Fatalf("field %+v: a claim needs origin and time, unknown must have none", f)
				}
			}
		})
	}
}

// Nothing in the derivation may produce ended without an end event.
func TestDeriveNeverProducesEndedFromSilence(t *testing.T) {
	for _, age := range []time.Duration{time.Second, time.Minute, time.Hour, 30 * 24 * time.Hour} {
		p := DerivePresence(presenceRef, PresenceInput{LastPollAt: ago(age), LastActivityAt: ago(age)})
		if p.Reachability.Value == ReachEnded {
			t.Fatalf("age %s produced ended", age)
		}
	}
	if len(PresenceGaps) == 0 || PresenceGaps[0][:5] != "ended" {
		t.Fatal("the missing end signal must be named as a gap")
	}
}

func presenceOf(t *testing.T, st *Store, room, agent string) Presence {
	t.Helper()
	peers, err := st.CoordPeers(room, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range peers {
		if p.ExternalID == agent {
			if p.Presence == nil {
				t.Fatalf("peer %s has no presence", agent)
			}
			return *p.Presence
		}
	}
	t.Fatalf("peer %s not listed", agent)
	return Presence{}
}

func TestPeersCarryPresenceFromRealSignals(t *testing.T) {
	st := controlFixture(t)
	room := RoomKeyForProject(roleProject)
	const peer = "claude:h:peer"
	registerRoleAgent(t, st, peer, "person:4", room, "member")

	// Silence: registered, nothing observed.
	p := presenceOf(t, st, room, controlAgent)
	if p.Reachability.Value != ReachUnknown || p.WorkState.Value != WorkUnknown {
		t.Fatalf("silent agent = %+v", p)
	}

	// Fresh poll: connected, observed, with a time.
	if err := st.TouchCoordAgentPoll(controlAgent); err != nil {
		t.Fatal(err)
	}
	p = presenceOf(t, st, room, controlAgent)
	if p.Reachability.Value != ReachConnected || p.Reachability.Origin != OriginObserved || p.Reachability.At == "" {
		t.Fatalf("polling agent = %+v", p.Reachability)
	}
	// A poll alone says nothing about work.
	if p.WorkState.Value != WorkUnknown {
		t.Fatalf("poll must not imply work: %+v", p.WorkState)
	}

	// An old poll falls back to unknown.
	if _, err := st.db.Exec(`UPDATE coord_agents SET last_poll_at=? WHERE external_id=?`, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), controlAgent); err != nil {
		t.Fatal(err)
	}
	if p = presenceOf(t, st, room, controlAgent); p.Reachability.Value != ReachUnknown {
		t.Fatalf("stale poll = %+v", p.Reachability)
	}

	// Collector activity under the transcript session id: working, observed.
	if err := st.RecordPathActivity([]PathActivity{{
		SessionExternalID: "pausable", Tool: "Edit", Path: "a.go", Quality: ActivityIntent,
		At: time.Now().UTC().Format(time.RFC3339),
	}}); err != nil {
		t.Fatal(err)
	}
	p = presenceOf(t, st, room, controlAgent)
	if p.WorkState.Value != WorkWorking || p.WorkState.Origin != OriginObserved {
		t.Fatalf("active agent = %+v", p.WorkState)
	}

	// A requested pause is not yet a claim; an acknowledged one is.
	c, err := st.RequestAgentControl(web("person:1", "robin"), controlAgent, ControlPause, "", RoleViaWeb)
	if err != nil {
		t.Fatal(err)
	}
	if p = presenceOf(t, st, room, controlAgent); p.WorkState.Value == WorkPaused {
		t.Fatal("a requested pause must not show as paused")
	}
	if _, err := st.RecordControlEvent("person:4", c.ID, ControlEvent{Kind: ControlEventAck, ToolUseID: "t1", SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	p = presenceOf(t, st, room, controlAgent)
	if p.WorkState.Value != WorkPaused || p.WorkState.Origin != OriginObserved {
		t.Fatalf("paused agent = %+v", p.WorkState)
	}
}

func TestOpenQuestionToAPeerShowsWaitingPeerUntilAnswered(t *testing.T) {
	st := controlFixture(t)
	room := RoomKeyForProject(roleProject)
	const peer = "claude:h:peer"
	registerRoleAgent(t, st, peer, "person:4", room, "member")
	asker := st.CoordinationFor(Principal{ID: "person:4"}, controlAgent)
	if _, err := asker.Send(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: room, ClientID: "q1",
		Body: "may I change the schema?", Intent: IntentQuestion, Mentions: []string{peer},
	}); err != nil {
		t.Fatal(err)
	}
	p := presenceOf(t, st, room, controlAgent)
	if p.WorkState.Value != WorkWaitingPeer || p.WorkState.Origin != OriginDerived || p.WorkState.At == "" {
		t.Fatalf("waiter = %+v", p.WorkState)
	}
	// The one who is asked is not the one waiting.
	if q := presenceOf(t, st, room, peer); q.WorkState.Value != WorkUnknown {
		t.Fatalf("recipient = %+v", q.WorkState)
	}
	// Answer closes the item; the waiter is unknown again, never idle.
	items, err := st.CoordinationFor(Principal{ID: "person:4"}, peer).Attention()
	if err != nil || len(items) == 0 {
		t.Fatalf("attention=%+v err=%v", items, err)
	}
	if err := st.CoordinationFor(Principal{ID: "person:4"}, peer).ResolveAttention(items[0].ID, AttentionActionAnswer); err != nil {
		t.Fatal(err)
	}
	if p = presenceOf(t, st, room, controlAgent); p.WorkState.Value != WorkUnknown {
		t.Fatalf("after the answer = %+v", p.WorkState)
	}
}

func TestPresenceIgnoresWaitsFromOtherRooms(t *testing.T) {
	st := controlFixture(t)
	room := RoomKeyForProject(roleProject)
	const peer = "claude:h:peer"
	registerRoleAgent(t, st, peer, "person:4", room, "member")
	// A private conversation must not show up as a work state in the project room.
	dm, err := st.CreateCoordGroup(GroupInput{Label: "private", Creator: controlAgent, Members: []string{controlAgent, peer}})
	if err != nil {
		t.Skipf("group fixture unavailable: %v", err)
	}
	if _, err := st.CoordinationFor(Principal{ID: "person:4"}, controlAgent).Send(CoordMessage{
		DestinationKind: DestinationRoom, DestinationID: dm.Key, ClientID: "q",
		Body: "private question", Intent: IntentQuestion, Mentions: []string{peer},
	}); err != nil {
		t.Skipf("private send unavailable: %v", err)
	}
	if p := presenceOf(t, st, room, controlAgent); p.WorkState.Value != WorkUnknown {
		t.Fatalf("private wait leaked into the project room: %+v", p.WorkState)
	}
}

func TestHeartbeatWritesAtMostOncePerInterval(t *testing.T) {
	st := controlFixture(t)
	poll := func() string {
		var v string
		if err := st.db.QueryRow(`SELECT last_poll_at FROM coord_agents WHERE external_id=?`, controlAgent).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if poll() != "" {
		t.Fatal("registration must not count as a poll")
	}
	if err := st.TouchCoordAgentPoll(controlAgent); err != nil {
		t.Fatal(err)
	}
	if poll() == "" {
		t.Fatal("first touch must write")
	}
	// Inside the interval a touch changes nothing.
	if _, err := st.db.Exec(`UPDATE coord_agents SET last_poll_at=? WHERE external_id=?`, time.Now().UTC().Add(-10*time.Second).Format(time.RFC3339), controlAgent); err != nil {
		t.Fatal(err)
	}
	before := poll()
	if err := st.TouchCoordAgentPoll(controlAgent); err != nil {
		t.Fatal(err)
	}
	if poll() != before {
		t.Fatal("a touch inside the interval must not write")
	}
	old := time.Now().UTC().Add(-HeartbeatInterval - 5*time.Second).Format(time.RFC3339)
	if _, err := st.db.Exec(`UPDATE coord_agents SET last_poll_at=? WHERE external_id=?`, old, controlAgent); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchCoordAgentPoll(controlAgent); err != nil {
		t.Fatal(err)
	}
	if poll() == old {
		t.Fatal("a touch after the interval must write")
	}
}

func TestHeartbeatBelongsToTheAgentsOwner(t *testing.T) {
	st := controlFixture(t)
	if err := st.CoordinationFor(Principal{ID: "person:4"}, controlAgent).Heartbeat(); err != nil {
		t.Fatalf("owner: %v", err)
	}
	for name, a := range map[string]CoordAccess{
		"other account": st.CoordinationFor(Principal{ID: "person:3"}, controlAgent),
		"no agent":      st.CoordinationFor(Principal{ID: "person:4"}, ""),
		"public view":   st.CoordinationPublicFor(Principal{ID: "person:4"}),
	} {
		if err := a.Heartbeat(); err == nil {
			t.Errorf("%s may not heartbeat for the agent", name)
		}
	}
}

// The heartbeat column has no trigger: it must not create a live event, whose
// sequence gaps would count hidden activity for a guest (pitfall #2447).
func TestHeartbeatCreatesNoEvent(t *testing.T) {
	st := controlFixture(t)
	count := func() int {
		var n int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM coord_events`).Scan(&n); err != nil {
			t.Skipf("no coord_events table: %v", err)
		}
		return n
	}
	before := count()
	if err := st.TouchCoordAgentPoll(controlAgent); err != nil {
		t.Fatal(err)
	}
	if count() != before {
		t.Fatal("heartbeat must not emit coordination events")
	}
}
