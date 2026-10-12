package store

import "testing"

func TestRegisterCoordAgentLearnsTheProviderButNeverForgetsIt(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("github.com/dw/prov")
	reg := func(id, provider string) {
		t.Helper()
		if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: id, Provider: provider, RoomKey: room, DisplayName: id}); err != nil {
			t.Fatal(err)
		}
	}
	get := func(id string) string {
		t.Helper()
		peers, err := s.CoordPeers(room, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range peers {
			if p.ExternalID == id {
				return p.Provider
			}
		}
		t.Fatalf("%s not found", id)
		return ""
	}
	reg("a", "unknown")
	reg("a", "claude")
	if got := get("a"); got != "claude" {
		t.Fatalf("an unknown provider is replaced by the one a hook reports: %q", got)
	}
	reg("a", "unknown")
	if got := get("a"); got != "claude" {
		t.Fatalf("a later coord call without a harness must not erase it: %q", got)
	}
	reg("b", "unidentified-harness")
	reg("b", "codex")
	if got := get("b"); got != "codex" {
		t.Fatalf("unidentified-harness is replaced too: %q", got)
	}
	reg("b", "claude")
	if got := get("b"); got != "codex" {
		t.Fatalf("one real provider does not overwrite another: %q", got)
	}
}

func TestRegisterCoordAgentRefreshesLastSeenAndKeepsRegisteredAt(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("github.com/dw/seen")
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "s", Provider: "claude", RoomKey: room, RegisteredAt: "2020-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "s", Provider: "claude", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	peers, err := s.CoordPeers(room, "")
	if err != nil || len(peers) != 1 {
		t.Fatalf("%v %v", peers, err)
	}
	if peers[0].LastSeenAt == "2020-01-01T00:00:00Z" || peers[0].LastSeenAt < "2025" {
		t.Fatalf("last_seen must move to now: %q", peers[0].LastSeenAt)
	}
	if peers[0].RegisteredAt != "2020-01-01T00:00:00Z" {
		t.Fatalf("registered_at is the first sighting: %q", peers[0].RegisteredAt)
	}
}
