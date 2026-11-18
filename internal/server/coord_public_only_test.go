package server

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// public_only ist eine Auswahl für Projekträume mit Projektrolle, nie ein Weg
// um die Mitgliedschaft: Threads in Maschinen-, Direkt- und Gruppenräumen
// liest nur, wer im Raum Mitglied ist. Das gilt in beiden Modi.
func TestPublicOnlyNeverOpensMachineDirectOrGroupRooms(t *testing.T) {
	for _, enforce := range []bool{true, false} {
		f := roomGateFixture(t, enforce)
		machineRoom := "machine:mia-box"
		if _, err := f.st.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:mia", Provider: "claude", RoomKey: machineRoom, PrincipalID: "person:3", Person: "mia"}); err != nil {
			t.Fatal(err)
		}
		mia := f.st.CoordinationFor(store.Principal{ID: "person:3", Label: "mia"}, "claude:mia")
		machineThread, err := mia.CreateTaskThreadInRoom(machineRoom, "Ops notes", "the disk fills up at night", "")
		if err != nil {
			t.Fatalf("enforce=%v: %v", enforce, err)
		}
		for agent, principal := range map[string]string{"claude:mia-p": "person:3", "claude:rex": "person:4"} {
			if _, err := f.st.RegisterCoordAgent(store.CoordAgent{ExternalID: agent, Provider: "claude", RoomKey: store.RoomKeyForProject(accProject), PrincipalID: principal, Person: "x"}); err != nil {
				t.Fatal(err)
			}
		}
		miaP := f.st.CoordinationFor(store.Principal{ID: "person:3", Label: "mia"}, "claude:mia-p")
		group, err := miaP.CreateGroup(store.GroupInput{Label: "plans", Creator: "claude:mia-p", Members: []string{"claude:mia-p", "claude:rex"}})
		if err != nil {
			t.Fatal(err)
		}
		groupThread, err := miaP.CreateTaskThreadInRoom(group.Key, "Group plan", "the secret plan", "")
		if err != nil {
			t.Fatal(err)
		}
		for who, id := range map[string]int64{"nora": machineThread, "lena": machineThread, "robin": groupThread} {
			room := machineRoom
			if id == groupThread {
				room = group.Key
			}
			for _, path := range []string{
				fmt.Sprintf("/api/threads/home?room_key=%s&public_only=1", room),
				fmt.Sprintf("/api/threads/%d?public_only=1", id),
				fmt.Sprintf("/api/threads/%d/summary?public_only=1", id),
				fmt.Sprintf("/api/threads/%d/links?public_only=1", id),
				fmt.Sprintf("/api/threads/%d/outcomes?public_only=1", id),
				fmt.Sprintf("/api/coord/messages?destination_kind=discussion&destination_id=%d&public_only=1", id),
			} {
				code, out := f.call(t, who, "GET", path, nil)
				if code == 200 && (strings.Contains(out, "Ops notes") || strings.Contains(out, "disk fills") || strings.Contains(out, "Group plan") || strings.Contains(out, "secret plan")) {
					t.Errorf("enforce=%v: %s GET %s reads a room it is not in: %s", enforce, who, path, out)
				}
			}
		}
	}
}

// Ein Gast meldet keinen Agenten im Projektraum an, in keinem Modus: der Raum
// beginnt wie in der Weboberfläche bei member.
func TestGuestDoesNotJoinTheProjectRoom(t *testing.T) {
	for _, enforce := range []bool{true, false} {
		f := roomGateFixture(t, enforce)
		room := store.RoomKeyForProject(accProject)
		if got := registerAgent(t, f, "gus", room); got != 403 {
			t.Errorf("enforce=%v: guest registers in the project room: %d, want 403", enforce, got)
		}
		if got := registerAgent(t, f, "mia", room); got != 200 {
			t.Errorf("enforce=%v: member registers in the project room: %d, want 200", enforce, got)
		}
	}
}

// Ein Thread in einem Maschinen-, Direkt- oder Gruppenraum verknüpft nur
// Requests, die der Aufrufer lesen darf, und antwortet für fremde und für
// unbekannte Nummern gleich.
func TestTaskThreadLinksOnlyReadableRequests(t *testing.T) {
	// nora ist hier Mitglied der Org ohne Rolle im Projekt: im Log-Modus liest sie
	// dessen Requests innerhalb der Org, unter Durchsetzung nicht.
	for _, enforce := range []bool{true} {
		f := roomGateFixture(t, enforce)
		room := "machine:nora-box"
		if _, err := f.st.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:nora", Provider: "claude", RoomKey: room, PrincipalID: "person:6", Person: "nora"}); err != nil {
			t.Fatal(err)
		}
		nora := f.st.CoordinationFor(store.Principal{ID: "person:6", Label: "nora"}, "claude:nora")
		foreign := fmt.Sprintf("REQ-%d", f.id["r-mia"])
		_, errForeign := nora.CreateTaskThreadInRoom(room, "probe", "", foreign)
		_, errMissing := nora.CreateTaskThreadInRoom(room, "probe", "", "REQ-9999")
		if errForeign == nil || errMissing == nil || errForeign.Error() != errMissing.Error() {
			t.Errorf("enforce=%v: foreign request answers %v, unknown one %v; they must be the same refusal", enforce, errForeign, errMissing)
		}
	}
	// Mit Durchsetzung zählt die Rolle: ein Mitglied der Org ohne Rolle im Projekt
	// bindet dessen Request nicht an einen Raum, in dem es sitzt.
	f := roomGateFixture(t, true)
	if _, err := f.st.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:nora", Provider: "claude", RoomKey: "machine:nora-box", PrincipalID: "person:6", Person: "nora"}); err != nil {
		t.Fatal(err)
	}
	nora := f.st.CoordinationFor(store.Principal{ID: "person:6", Label: "nora"}, "claude:nora")
	if _, err := nora.CreateTaskThreadInRoom("machine:nora-box", "probe", "", fmt.Sprintf("REQ-%d", f.id["r-mia"])); err == nil {
		t.Errorf("org member without a role links the request of the project")
	}
}
