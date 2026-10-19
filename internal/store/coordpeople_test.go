package store

import (
	"errors"
	"testing"
)

func peopleFixture(t *testing.T) *Store {
	t.Helper()
	st := roleFixture(t) // robin(owner) anna ben cleo dev; anna, ben, cleo are org members
	for who, role := range map[string]string{"person:2": RoleMember, "person:3": RoleGuest, "person:4": RoleLead} {
		if err := setRole(st, "person:1", who, role, false); err != nil {
			t.Fatal(err)
		}
	}
	st.SetAccessMode(AccessMode{Enforce: true})
	if err := st.EnsureCoordRoom(CoordRoom{Key: RoomKeyForProject(roleProject), Kind: RoomProject}); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestRoomPeopleListsTheProjectsPeopleForMembersAndUp(t *testing.T) {
	st := peopleFixture(t)
	room := RoomKeyForProject(roleProject)
	for _, viewer := range []string{"person:1", "person:2", "person:4"} {
		people, err := st.CoordinationFor(Principal{ID: viewer, TokenKind: WebSessionKind}, "").RoomPeople(room)
		if err != nil {
			t.Fatalf("%s: %v", viewer, err)
		}
		got := map[string]string{}
		for _, p := range people {
			got[p.AccountID] = p.Role
		}
		want := map[string]string{"person:1": RoleOwner, "person:2": RoleMember, "person:3": RoleGuest, "person:4": RoleLead}
		if len(got) != len(want) {
			t.Fatalf("%s sees %v, want %v", viewer, got, want)
		}
		for id, role := range want {
			if got[id] != role {
				t.Errorf("%s sees %s as %q, want %q", viewer, id, got[id], role)
			}
		}
	}
}

func TestRoomPeopleIsRefusedToGuestsAndStrangersLikePeers(t *testing.T) {
	st := peopleFixture(t)
	room := RoomKeyForProject(roleProject)
	for _, viewer := range []string{"person:3", "person:5"} {
		acc := st.CoordinationFor(Principal{ID: viewer, TokenKind: WebSessionKind}, "")
		people, err := acc.RoomPeople(room)
		_, peersErr := acc.Peers(room, "")
		if len(people) != 0 || err == nil {
			t.Fatalf("%s got %v, %v", viewer, people, err)
		}
		// The refusal is the same one Peers gives, so the two cannot tell
		// a viewer apart from each other.
		if !errors.Is(err, ErrCoordNotFound) && !errors.Is(err, ErrCoordForbidden) || (errors.Is(err, ErrCoordNotFound) != errors.Is(peersErr, ErrCoordNotFound)) {
			t.Errorf("%s: RoomPeople %v vs Peers %v", viewer, err, peersErr)
		}
	}
}

func TestRoomPeopleIsEmptyOutsideProjectRooms(t *testing.T) {
	st := peopleFixture(t)
	acc := st.CoordinationFor(Principal{ID: "person:1", TokenKind: WebSessionKind}, "")
	if err := st.EnsureCoordRoom(CoordRoom{Key: "machine:box", Kind: RoomMachine}); err != nil {
		t.Fatal(err)
	}
	if people, _ := acc.RoomPeople("machine:box"); len(people) != 0 {
		t.Fatalf("machine room: %v", people)
	}
}
