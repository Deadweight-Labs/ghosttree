package store

import "testing"

func TestRegisterCoordAgentLearnsTheProviderButNeverForgetsIt(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("github.com/dw/prov")
	reg := func(provider string) {
		t.Helper()
		if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "sess-prov", Provider: provider, RoomKey: room, DisplayName: "sess-prov"}); err != nil {
			t.Fatal(err)
		}
	}
	get := func() string {
		t.Helper()
		peers, err := s.CoordPeers(room, "")
		if err != nil || len(peers) != 1 {
			t.Fatalf("peers: %v %v", peers, err)
		}
		return peers[0].Provider
	}
	reg("unknown")
	reg("claude")
	if got := get(); got != "claude" {
		t.Fatalf("an unknown provider is replaced by the one a hook reports: %q", got)
	}
	reg("unknown")
	if got := get(); got != "claude" {
		t.Fatalf("a later coord call without a harness must not erase it: %q", got)
	}
}
