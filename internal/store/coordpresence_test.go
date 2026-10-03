package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
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

// registerWithSession registers an agent that reports its transcript session,
// and stands in for the collector's upload of that session.
func registerWithSession(t *testing.T, st *Store, id, principal, session, project string) {
	t.Helper()
	account, _ := accountNumericID(principal)
	if _, err := st.RegisterCoordAgent(CoordAgent{ExternalID: id, Provider: "claude", RoomKey: RoomKeyForProject(roleProject),
		DisplayName: id, PrincipalID: principal, Role: "member", SessionID: session}); err != nil {
		t.Fatal(err)
	}
	uploadTestSession(t, st, session, project, account)
}

func uploadTestSession(t *testing.T, st *Store, session, project string, account int64) {
	t.Helper()
	if _, err := st.UpsertSession(Session{Harness: "claude", ExternalID: session, Scope: scope.Axes{Project: project}, AccountID: account}); err != nil {
		t.Fatal(err)
	}
}

// touch records activity the way the server does: stamped with the account of
// the session's uploader (the caller's account at the API).
func touch(t *testing.T, st *Store, session string) {
	t.Helper()
	var account int64
	_ = st.db.QueryRow(`SELECT account_id FROM sessions WHERE external_id=? LIMIT 1`, session).Scan(&account)
	touchAs(t, st, session, account)
}

func touchAs(t *testing.T, st *Store, session string, account int64) {
	t.Helper()
	if err := st.RecordPathActivity([]PathActivity{{SessionExternalID: session, Tool: "Edit", Path: "a.go",
		Quality: ActivityIntent, At: time.Now().UTC().Format(time.RFC3339), AccountID: account}}); err != nil {
		t.Fatal(err)
	}
}

func TestPeersCarryPresenceFromRealSignals(t *testing.T) {
	st := controlFixture(t)
	room := RoomKeyForProject(roleProject)

	// Silence: registered, nothing observed.
	p := presenceOf(t, st, room, controlAgent)
	if p.Reachability.Value != ReachUnknown || p.WorkState.Value != WorkUnknown {
		t.Fatalf("silent agent = %+v", p)
	}

	// Fresh poll: connected, observed, with a time; and nothing about work.
	if err := st.TouchCoordAgentPoll(controlAgent); err != nil {
		t.Fatal(err)
	}
	p = presenceOf(t, st, room, controlAgent)
	if p.Reachability.Value != ReachConnected || p.Reachability.Origin != OriginObserved || p.Reachability.At == "" {
		t.Fatalf("polling agent = %+v", p.Reachability)
	}
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

	// Activity with no session id on the agent proves nothing about it.
	touch(t, st, "pausable")
	if p = presenceOf(t, st, room, controlAgent); p.WorkState.Value != WorkUnknown {
		t.Fatalf("an agent without a registered session id must stay unknown: %+v", p.WorkState)
	}

	// The real mapping: the agent reports its session, the session is its own
	// account's and belongs to this project.
	registerWithSession(t, st, controlAgent, "person:4", "uuid-real", roleProject)
	touch(t, st, "uuid-real")
	p = presenceOf(t, st, room, controlAgent)
	if p.WorkState.Value != WorkWorking || p.WorkState.Origin != OriginObserved {
		t.Fatalf("active agent = %+v", p.WorkState)
	}
}

func TestActivityIsOnlyMappedByExactSessionIDOfTheSameAccountAndProject(t *testing.T) {
	st := controlFixture(t)
	room := RoomKeyForProject(roleProject)

	// A victim works in this project.
	registerWithSession(t, st, "claude:h:victim", "person:4", "uuid-victim", roleProject)
	touch(t, st, "uuid-victim")
	if p := presenceOf(t, st, room, "claude:h:victim"); p.WorkState.Value != WorkWorking {
		t.Fatalf("setup: %+v", p.WorkState)
	}

	// Suffix collision: an agent id that merely ENDS on the victim's session id.
	registerRoleAgent(t, st, "evil:x:uuid-victim", "person:3", room, "member")
	if p := presenceOf(t, st, room, "evil:x:uuid-victim"); p.WorkState.Value != WorkUnknown {
		t.Fatalf("suffix collision borrowed foreign activity: %+v", p.WorkState)
	}

	// Claiming the victim's session id from another account does not help: the
	// session is not that account's.
	if _, err := st.RegisterCoordAgent(CoordAgent{ExternalID: "evil:y:1", Provider: "claude", RoomKey: room,
		DisplayName: "evil", PrincipalID: "person:3", Role: "member", SessionID: "uuid-victim"}); err != nil {
		t.Fatal(err)
	}
	if p := presenceOf(t, st, room, "evil:y:1"); p.WorkState.Value != WorkUnknown {
		t.Fatalf("claimed foreign session: %+v", p.WorkState)
	}

	// Activity from another project must not show in this room, even for the
	// agent's own session: the viewer may not see that project.
	registerWithSession(t, st, "claude:h:elsewhere", "person:4", "uuid-else", "github.com/other/secret")
	touch(t, st, "uuid-else")
	if p := presenceOf(t, st, room, "claude:h:elsewhere"); p.WorkState.Value != WorkUnknown {
		t.Fatalf("activity from another project leaked into this room: %+v", p.WorkState)
	}
}

func TestWorkingIsNotShownInDirectOrGroupRooms(t *testing.T) {
	st := controlFixture(t)
	registerWithSession(t, st, "claude:h:a", "person:4", "uuid-a", roleProject)
	registerWithSession(t, st, "claude:h:b", "person:4", "uuid-b", roleProject)
	touch(t, st, "uuid-a")
	group, err := st.CreateCoordGroup(GroupInput{Label: "pair", Creator: "claude:h:a", Members: []string{"claude:h:a", "claude:h:b"}})
	if err != nil {
		t.Fatal(err)
	}
	if p := presenceOf(t, st, group.Key, "claude:h:a"); p.WorkState.Value != WorkUnknown {
		t.Fatalf("a group room has no project to scope activity: %+v", p.WorkState)
	}
}

func TestOnlyAnEffectivePauseShowsAsPaused(t *testing.T) {
	st := controlFixture(t)
	room := RoomKeyForProject(roleProject)
	c, err := st.RequestAgentControl(web("person:1", "robin"), controlAgent, ControlPause, "", RoleViaWeb)
	if err != nil {
		t.Fatal(err)
	}
	if p := presenceOf(t, st, room, controlAgent); p.WorkState.Value == WorkPaused {
		t.Fatal("a requested pause must not show as paused")
	}
	if _, err := st.RecordControlEvent("person:4", c.ID, ControlEvent{Kind: ControlEventAck, ToolUseID: "t1", SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	// The hook gave the pause; nothing shows that the model stopped.
	if p := presenceOf(t, st, room, controlAgent); p.WorkState.Value != WorkUnknown {
		t.Fatalf("an acknowledged pause is not yet a pause: %+v", p.WorkState)
	}
	if _, err := st.RecordControlEvent("person:4", c.ID, ControlEvent{Kind: ControlEventProof, ToolUseID: "t1", SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	p := presenceOf(t, st, room, controlAgent)
	if p.WorkState.Value != WorkPaused || p.WorkState.Origin != OriginObserved {
		t.Fatalf("effective pause = %+v", p.WorkState)
	}
}

type countingDB struct {
	db *sql.DB
	n  int
}

func (c *countingDB) Query(q string, a ...any) (*sql.Rows, error) { c.n++; return c.db.Query(q, a...) }
func (c *countingDB) QueryRow(q string, a ...any) *sql.Row        { c.n++; return c.db.QueryRow(q, a...) }

func TestPresenceQueryCountDoesNotGrowWithThePeers(t *testing.T) {
	st := controlFixture(t)
	room := RoomKeyForProject(roleProject)
	count := func(peers int) int {
		var agents []presenceAgent
		for i := 0; i < peers; i++ {
			id := fmt.Sprintf("claude:h:n%d-%d", peers, i)
			registerWithSession(t, st, id, "person:4", fmt.Sprintf("uuid-%d-%d", peers, i), roleProject)
			touch(t, st, fmt.Sprintf("uuid-%d-%d", peers, i))
			agents = append(agents, presenceAgent{ExternalID: id, PrincipalID: "person:4", SessionID: fmt.Sprintf("uuid-%d-%d", peers, i)})
		}
		c := &countingDB{db: st.db}
		presenceBatch(c, time.Now().UTC(), room, agents, nil)
		return c.n
	}
	small, large := count(2), count(40)
	if small != large {
		t.Fatalf("queries grow with the peers: %d for 2, %d for 40", small, large)
	}
	if large > 6 {
		t.Fatalf("too many queries for one peers call: %d", large)
	}
}

func TestPresenceWaitQueryUsesIndexes(t *testing.T) {
	st := controlFixture(t)
	args := []any{"r", AttentionQuestion, AttentionApproval, AttentionBlocker, "a", "b"}
	rows, err := st.db.Query(`EXPLAIN QUERY PLAN `+presenceWaitsSQL(2), args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		// The walk starts at the open attention rows (partial index) and looks
		// the message up by id; a scan of anything else is a full scan.
		if strings.HasPrefix(detail, "SCAN") {
			t.Fatalf("full scan in the wait query: %s", detail)
		}
		if strings.Contains(detail, "SEARCH a") && !strings.Contains(detail, "coord_attention_open_room (room_key=?") {
			t.Fatalf("open attention is not found by room: %s", detail)
		}
		if strings.Contains(detail, "SEARCH m") && !strings.Contains(detail, "PRIMARY KEY") && !strings.Contains(detail, "rowid") {
			t.Fatalf("messages are not looked up by id: %s", detail)
		}
	}
}

func TestOldDatabaseGainsThePresenceColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	registerRoleAgent(t, st, "claude:h:old", "", RoomKeyForProject(roleProject), "member")
	for _, col := range []string{"last_poll_at", "session_id"} {
		if _, err := st.db.Exec(`ALTER TABLE coord_agents DROP COLUMN ` + col); err != nil {
			t.Skipf("sqlite cannot drop %s: %v", col, err)
		}
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen old database: %v", err)
	}
	defer st.Close()
	if err := st.TouchCoordAgentPoll("claude:h:old"); err != nil {
		t.Fatal(err)
	}
	p := presenceOf(t, st, RoomKeyForProject(roleProject), "claude:h:old")
	if p.Reachability.Value != ReachConnected {
		t.Fatalf("migrated agent = %+v", p)
	}
}

func TestPresenceComparesTimesNotText(t *testing.T) {
	if !laterThan("2026-10-02T12:30:00Z", "2026-10-02T13:00:00+02:00") {
		t.Fatal("12:30Z is later than 11:00Z although it sorts lower as text")
	}
	if laterThan("garbage", "2026-10-02T12:00:00Z") {
		t.Fatal("an unreadable time is never later")
	}
	st := controlFixture(t)
	if err := st.RecordPathActivity([]PathActivity{{SessionExternalID: "s", Tool: "Edit", Path: "p", Quality: ActivityIntent, At: "2026-10-02T14:00:00+02:00"}}); err != nil {
		t.Fatal(err)
	}
	var at string
	if err := st.db.QueryRow(`SELECT at FROM path_activity WHERE session_external_id='s'`).Scan(&at); err != nil || at != "2026-10-02T12:00:00Z" {
		t.Fatalf("stored at = %q (%v), want UTC with Z", at, err)
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

func TestPollDueOnlyAfterTheInterval(t *testing.T) {
	st := controlFixture(t)
	if !st.CoordAgentPollDue(controlAgent) {
		t.Fatal("an agent that never polled is due")
	}
	if err := st.TouchCoordAgentPoll(controlAgent); err != nil {
		t.Fatal(err)
	}
	if st.CoordAgentPollDue(controlAgent) {
		t.Fatal("inside the interval nothing is due, so no write is queued")
	}
	if st.CoordAgentPollDue("claude:h:unknown") {
		t.Fatal("an unregistered agent is never due")
	}
}

// Sessions are unique per (harness, id). anna uploads robin's id under another
// harness: robin can still write, and anna's activity under that id is not
// robin's presence.
func TestForeignRowWithTheSameSessionIDNeitherBlocksNorInjects(t *testing.T) {
	st := controlFixture(t)
	room := RoomKeyForProject(roleProject)
	registerWithSession(t, st, "claude:h:robin", "person:4", "uuid-r", roleProject)
	// person:3 uploads the same id for another harness, in the same project.
	if _, err := st.UpsertSession(Session{Harness: "codex", ExternalID: "uuid-r", Scope: scope.Axes{Project: roleProject}, AccountID: 3}); err != nil {
		t.Fatal(err)
	}
	if !st.SessionOwnedBy("uuid-r", "person:4") || !st.SessionOwnedBy("uuid-r", "person:3") {
		t.Fatal("each account owns its own row; a foreign row must not lock the owner out")
	}
	touchAs(t, st, "uuid-r", 3)
	if p := presenceOf(t, st, room, "claude:h:robin"); p.WorkState.Value != WorkUnknown {
		t.Fatalf("anna's activity under robin's id injected presence: %+v", p.WorkState)
	}
	touchAs(t, st, "uuid-r", 4)
	if p := presenceOf(t, st, room, "claude:h:robin"); p.WorkState.Value != WorkWorking {
		t.Fatalf("robin's own activity must count: %+v", p.WorkState)
	}
	// The same unique key from another account does not swallow robin's row.
	a, _ := st.SessionPathActivity("uuid-r", 4, "2000-01-01T00:00:00Z", 10)
	b, _ := st.SessionPathActivity("uuid-r", 3, "2000-01-01T00:00:00Z", 10)
	if len(a) == 0 || len(b) != 1 {
		t.Fatalf("rows per account: robin %d, anna %d", len(a), len(b))
	}
	same := PathActivity{SessionExternalID: "uuid-r", Tool: "Edit", Path: "same.go", Quality: ActivityIntent, At: "2026-10-02T12:00:00Z"}
	first, second := same, same
	first.AccountID, second.AccountID = 3, 4
	if err := st.RecordPathActivity([]PathActivity{first}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordPathActivity([]PathActivity{second}); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM path_activity WHERE path='same.go'`).Scan(&n)
	if n != 2 {
		t.Fatalf("a pre-inserted identical row of another account swallowed robin's: %d rows", n)
	}
}

func TestOldPathActivityTableGainsTheAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DROP TABLE path_activity;
		CREATE TABLE path_activity(
		  id INTEGER PRIMARY KEY, project TEXT NOT NULL DEFAULT '', session_external_id TEXT NOT NULL,
		  checkout TEXT NOT NULL DEFAULT '', tool TEXT NOT NULL, path TEXT NOT NULL,
		  writes INTEGER NOT NULL DEFAULT 0,
		  quality TEXT NOT NULL CHECK(quality IN ('intent','reported_success','observed_change','unattributed')),
		  at TEXT NOT NULL, UNIQUE(session_external_id,tool,path,quality,at));
		INSERT INTO path_activity(project,session_external_id,tool,path,quality,at) VALUES('p','s','Edit','a.go','intent','2026-10-02T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen old database: %v", err)
	}
	defer st.Close()
	var account int64
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(account_id),-1) FROM path_activity`).Scan(&n, &account); err != nil || n != 1 || account != 0 {
		t.Fatalf("old rows must survive with account 0 (the instance owner): n=%d account=%d err=%v", n, account, err)
	}
	if err := st.RecordPathActivity([]PathActivity{{SessionExternalID: "s", Tool: "Edit", Path: "a.go", Quality: ActivityIntent, At: "2026-10-02T12:00:00Z", AccountID: 5}}); err != nil {
		t.Fatal(err)
	}
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM path_activity`).Scan(&n)
	if n != 2 {
		t.Fatalf("the rebuilt table must key on the account: %d rows", n)
	}
}
