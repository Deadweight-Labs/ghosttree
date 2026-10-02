package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestAgentCanOccupyMachineAndProjectRooms(t *testing.T) {
	s := openTest(t)
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "sess-a", Provider: "test", RoomKey: "machine:host", DisplayName: "A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "sess-a", Provider: "test", RoomKey: "project:repo", DisplayName: "A"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.CoordRoomsForPrincipal("sess-a")
	if err != nil {
		t.Fatal(err)
	}
	assertCoordRoomKeys(t, got, "machine:host", "project:repo")
}

func TestAgentCannotSwitchToAnotherRoomOfTheSamePublicKind(t *testing.T) {
	s := openTest(t)
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "sess-a", PrincipalID: "person:1", RoomKey: "project:a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "sess-a", PrincipalID: "person:1", RoomKey: "project:b"}); !errors.Is(err, ErrCoordAgentScopeChanged) {
		t.Fatalf("project switch: want scope error, got %v", err)
	}
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "sess-a", PrincipalID: "person:1", RoomKey: "machine:a"}); err != nil {
		t.Fatalf("project plus machine must remain allowed: %v", err)
	}
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "sess-a", PrincipalID: "person:1", RoomKey: "machine:b"}); !errors.Is(err, ErrCoordAgentScopeChanged) {
		t.Fatalf("machine switch: want scope error, got %v", err)
	}
}

func TestTwoGroupsWithSameMembersStayDistinct(t *testing.T) {
	s := openTest(t)
	a, err := s.CreateCoordGroup(GroupInput{Label: "release", Creator: "person:1", Members: []string{"person:1", "sess-a"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateCoordGroup(GroupInput{Label: "incident", Creator: "person:1", Members: []string{"person:1", "sess-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if a.Key == b.Key {
		t.Fatal("groups collapsed by member set")
	}
}

func TestRemovingMemberRevokesWholeGroupHistory(t *testing.T) {
	s := openTest(t)
	r, err := s.CreateCoordGroup(GroupInput{Label: "release", Creator: "person:1", Members: []string{"person:1", "sess-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LeaveCoordRoom(r.Key, "sess-a", "person:1"); err != nil {
		t.Fatal(err)
	}
	ok, err := s.MayReadCoordRoom(r.Key, "sess-a")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("removed member retained history access")
	}
}

func TestLegacyCoordMembershipsMigrateWhenStoreReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := OpenWithOptions(path, OpenOptions{MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO coord_rooms(room_key,kind,label,created_at) VALUES('group:legacy','group','legacy','2020-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO coord_room_members(room_key,member_external_id,joined_at) VALUES('group:legacy','sess-old','2020-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO coord_agents(external_id,provider,room_key,display_name,person,registered_at,last_seen_at)
		VALUES('sess-old','legacy','project:repo','old','robin','2020-01-01T00:00:00Z','2020-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM coord_room_memberships`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM coord_room_membership_migrations`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenWithOptions(path, OpenOptions{MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ok, err := s.MayReadCoordRoom("group:legacy", "sess-old")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("legacy member was not migrated")
	}
}

func TestLegacyPrivateMigrationQuarantinesMissingAndOwnerlessMembers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quarantine.db")
	s, err := OpenWithOptions(path, OpenOptions{MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"missing", "ownerless"} {
		key := "group:" + member
		if _, err := s.db.Exec(`INSERT INTO coord_rooms(room_key,kind,label,created_at) VALUES(?,'group','','2020-01-01T00:00:00Z')`, key); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT INTO coord_room_members(room_key,member_external_id,joined_at) VALUES(?,?,'2020-01-01T00:00:00Z')`, key, member); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO coord_agents(external_id,provider,room_key,display_name,person,registered_at,last_seen_at)
		VALUES('ownerless','legacy','project:repo','ownerless','','2020-01-01T00:00:00Z','2020-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM coord_room_membership_migrations`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenWithOptions(path, OpenOptions{MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, member := range []string{"missing", "ownerless"} {
		ok, err := s.MayReadCoordRoom("group:"+member, member)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Fatalf("quarantined legacy member %q became active", member)
		}
	}
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "missing", Provider: "test", RoomKey: "project:repo", DisplayName: "missing", Person: "philipp", PrincipalID: "person:2"}); err != nil {
		t.Fatal(err)
	}
	ok, err := s.MayReadCoordRoom("group:missing", "missing")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("later registration inherited quarantined private history")
	}
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "ownerless", Provider: "test", RoomKey: "project:repo", DisplayName: "ownerless", Person: "philipp", PrincipalID: "person:2"}); !errors.Is(err, ErrCoordAgentOwned) {
		t.Fatalf("ownerless legacy row was claimable: %v", err)
	}
}

func TestReopeningStoreDoesNotResurrectLeftLegacyAgentRoom(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leave.db")
	s, err := OpenWithOptions(path, OpenOptions{MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "sess-a", Provider: "test", RoomKey: "machine:host", DisplayName: "A"}); err != nil {
		t.Fatal(err)
	}
	if err := s.LeaveCoordRoom("machine:host", "sess-a", "sess-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenWithOptions(path, OpenOptions{MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	rooms, err := s.CoordRoomsForPrincipal("sess-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 0 {
		t.Fatalf("left room resurrected after reopen: %+v", rooms)
	}
}

func TestOnlyManagerCanRemoveAnotherGroupMember(t *testing.T) {
	s := openTest(t)
	r, err := s.CreateCoordGroup(GroupInput{Creator: "owner", Members: []string{"owner", "member", "other"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LeaveCoordRoom(r.Key, "other", "member"); err == nil {
		t.Fatal("ordinary member removed another member")
	}
	if err := s.LeaveCoordRoom(r.Key, "other", "owner"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.CoordRoomMemberships(r.Key)
	if err != nil {
		t.Fatal(err)
	}
	var left string
	for _, row := range rows {
		if row.PrincipalID == "other" {
			left = row.LeftAt
		}
	}
	if left == "" {
		t.Fatal("leave history was not retained")
	}
}

func TestRoomDiscoveryIncludesOnlyActiveMembers(t *testing.T) {
	s := openTest(t)
	r, err := s.CreateCoordGroup(GroupInput{Creator: "owner", Members: []string{"owner", "active", "leaver"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LeaveCoordRoom(r.Key, "leaver", "owner"); err != nil {
		t.Fatal(err)
	}
	rooms, err := s.CoordRoomsForPrincipal("owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 1 {
		t.Fatalf("rooms=%+v", rooms)
	}
	if len(rooms[0].Members) != 2 || rooms[0].Members[0] != "active" || rooms[0].Members[1] != "owner" {
		t.Fatalf("active members=%v", rooms[0].Members)
	}
}

func TestRejoiningRoomKeepsPriorMembershipPeriod(t *testing.T) {
	s := openTest(t)
	r, err := s.CreateCoordGroup(GroupInput{Creator: "owner", Members: []string{"owner", "member"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LeaveCoordRoom(r.Key, "member", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := s.JoinCoordRoom(RoomMembership{RoomKey: r.Key, PrincipalID: "member"}); err == nil {
		t.Fatal("group join bypassed manager")
	}
	if err := s.UpdateCoordGroup(GroupUpdate{RoomKey: r.Key, Actor: "owner", Add: []string{"member"}}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.CoordRoomMemberships(r.Key)
	if err != nil {
		t.Fatal(err)
	}
	var periods, active int
	for _, row := range rows {
		if row.PrincipalID != "member" {
			continue
		}
		periods++
		if row.LeftAt == "" {
			active++
		}
	}
	if periods != 2 || active != 1 {
		t.Fatalf("membership periods=%d active=%d rows=%+v", periods, active, rows)
	}
}

func TestAgentRegistrationCannotJoinPrivateRoom(t *testing.T) {
	s := openTest(t)
	r, err := s.CreateCoordGroup(GroupInput{Creator: "owner", Members: []string{"owner", "member"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "outsider", Provider: "test", RoomKey: r.Key, DisplayName: "outsider"}); err == nil {
		t.Fatal("private room accepted as an automatic agent room")
	}
	ok, err := s.MayReadCoordRoom(r.Key, "outsider")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("registration granted private-room access")
	}
}

func TestLegacyAgentRoomMigrationCannotGrantPrivateAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-legacy.db")
	s, err := OpenWithOptions(path, OpenOptions{MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO coord_rooms(room_key,kind,label,created_at) VALUES('group:private','group','','2020-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO coord_agents(external_id,provider,room_key,display_name,registered_at,last_seen_at)
		VALUES('outsider','legacy','group:private','outsider','2020-01-01T00:00:00Z','2020-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM coord_room_membership_migrations`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenWithOptions(path, OpenOptions{MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ok, err := s.MayReadCoordRoom("group:private", "outsider")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("legacy agent room granted private access")
	}
}

func TestExistingPrivateRoomCannotBeRepostedWithNewMembersOrKind(t *testing.T) {
	s := openTest(t)
	r, err := s.CreateCoordGroup(GroupInput{Creator: "owner", Members: []string{"owner", "member"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureCoordRoom(CoordRoom{Key: r.Key, Kind: RoomDirect, Members: []string{"owner", "attacker"}}); err == nil {
		t.Fatal("existing group accepted as a direct room")
	}
	ok, err := s.MayReadCoordRoom(r.Key, "attacker")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("reposting private key added attacker")
	}
}

func TestDirectRoomRequiresCanonicalKey(t *testing.T) {
	s := openTest(t)
	if err := s.EnsureCoordRoom(CoordRoom{Key: "direct:chosen", Kind: RoomDirect, Members: []string{"a", "b"}}); err == nil {
		t.Fatal("noncanonical direct key accepted")
	}
}

func TestGroupCannotLoseItsLastManagerOrLabelByOmission(t *testing.T) {
	s := openTest(t)
	r, err := s.CreateCoordGroup(GroupInput{Label: "kept", Creator: "owner", Members: []string{"owner", "member"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateCoordGroup(GroupUpdate{RoomKey: r.Key, Actor: "owner", Add: []string{"new"}}); err != nil {
		t.Fatal(err)
	}
	rooms, err := s.CoordRoomsForPrincipal("owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 1 || rooms[0].Label != "kept" {
		t.Fatalf("omitted label cleared group label: %+v", rooms)
	}
	if err := s.UpdateCoordGroup(GroupUpdate{RoomKey: r.Key, Actor: "owner", RemoveManagers: []string{"owner"}}); err == nil {
		t.Fatal("last manager demoted")
	}
	if err := s.LeaveCoordRoom(r.Key, "owner", "owner"); err == nil {
		t.Fatal("last manager left without succession")
	}
	if err := s.UpdateCoordGroup(GroupUpdate{RoomKey: r.Key, Actor: "owner", Remove: []string{"owner"}}); err == nil {
		t.Fatal("last manager removed without succession")
	}
	if err := s.UpdateCoordGroup(GroupUpdate{RoomKey: r.Key, Actor: "owner", Remove: []string{"owner", "member", "new"}}); err == nil {
		t.Fatal("removing every member also removed every manager")
	}
}

func TestMigratedManagerlessGroupIsExplicitlyReadOnly(t *testing.T) {
	s := openTest(t)
	key := RoomKeyForGroup([]string{"a", "b"})
	if err := s.EnsureCoordRoom(CoordRoom{Key: key, Kind: RoomGroup, Members: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.LeaveCoordRoom(key, "a", "a"); !errors.Is(err, ErrLegacyGroupReadOnly) {
		t.Fatalf("leave error=%v, want legacy read-only", err)
	}
	if err := s.UpdateCoordGroup(GroupUpdate{RoomKey: key, Actor: "a", Add: []string{"c"}}); !errors.Is(err, ErrLegacyGroupReadOnly) {
		t.Fatalf("update error=%v, want legacy read-only", err)
	}
	if err := s.JoinCoordRoom(RoomMembership{RoomKey: key, PrincipalID: "c"}); !errors.Is(err, ErrLegacyGroupReadOnly) {
		t.Fatalf("join error=%v, want legacy read-only", err)
	}
	if err := s.EnsureCoordRoom(CoordRoom{Key: key, Kind: RoomGroup, Label: "changed", Members: []string{"a", "b"}}); !errors.Is(err, ErrLegacyGroupReadOnly) {
		t.Fatalf("ensure update error=%v, want legacy read-only", err)
	}
}

func TestGroupMembershipMutationsAreAudited(t *testing.T) {
	s := openTest(t)
	r, err := s.CreateCoordGroup(GroupInput{Creator: "owner", Members: []string{"owner", "member"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateCoordGroup(GroupUpdate{RoomKey: r.Key, Actor: "owner", AddManagers: []string{"member"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.LeaveCoordRoom(r.Key, "owner", "owner"); err != nil {
		t.Fatal(err)
	}
	events, err := s.CoordRoomMembershipEvents(r.Key)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"group_create": false, "join": false, "manager_grant": false, "leave": false, "manager_revoke": false}
	for _, event := range events {
		if _, ok := want[event.Action]; ok {
			want[event.Action] = true
		}
	}
	for action, seen := range want {
		if !seen {
			t.Fatalf("missing %s event in %+v", action, events)
		}
	}
	if _, err := s.db.Exec(`UPDATE coord_room_membership_events SET actor_id='tampered' WHERE room_key=?`, r.Key); err == nil {
		t.Fatal("membership audit accepted update")
	}
	if _, err := s.db.Exec(`DELETE FROM coord_room_membership_events WHERE room_key=?`, r.Key); err == nil {
		t.Fatal("membership audit accepted delete")
	}
}

func TestAddingNewManagerRecordsGrantEvent(t *testing.T) {
	s := openTest(t)
	r, err := s.CreateCoordGroup(GroupInput{Creator: "owner", Members: []string{"owner", "member"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateCoordGroup(GroupUpdate{RoomKey: r.Key, Actor: "owner", AddManagers: []string{"new-manager"}}); err != nil {
		t.Fatal(err)
	}
	events, err := s.CoordRoomMembershipEvents(r.Key)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Action == "manager_grant" && event.PrincipalID == "new-manager" && event.ActorID == "owner" {
			return
		}
	}
	t.Fatalf("new manager grant not audited: %+v", events)
}

func assertCoordRoomKeys(t *testing.T, rooms []CoordRoom, want ...string) {
	t.Helper()
	got := make(map[string]bool, len(rooms))
	for _, room := range rooms {
		got[room.Key] = true
	}
	for _, key := range want {
		if !got[key] {
			t.Fatalf("room %q missing from %+v", key, rooms)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("got room keys %v, want %v", got, want)
	}
}
