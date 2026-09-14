package store

import "testing"

// Zwei Sessions, die in verschiedenen Unterverzeichnissen desselben Repos
// gestartet wurden, müssen im selben Raum landen. Der cwd unterscheidet sich,
// die Repo-Identität nicht — und der Raum folgt der Repo-Identität. Hinge er
// am cwd, wäre ein Agent in packages/server von einem in apps/web getrennt,
// obwohl sie sich gerade dort in die Quere kommen.
func TestPeersFromDifferentSubdirectoriesShareOneRoom(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("github.com/deadweight-labs/ghosttree")

	a := CoordAgent{ExternalID: "sess-a", Provider: "claude", RoomKey: room,
		Cwd: "/repo/internal/server", Branch: "main", DisplayName: "Claude-A"}
	b := CoordAgent{ExternalID: "sess-b", Provider: "codex", RoomKey: room,
		Cwd: "/repo/cmd/ctx", Branch: "feat/x", DisplayName: "Codex-B"}

	if _, err := s.RegisterCoordAgent(a); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if _, err := s.RegisterCoordAgent(b); err != nil {
		t.Fatalf("register b: %v", err)
	}

	peers, err := s.CoordPeers(room, "")
	if err != nil {
		t.Fatalf("peers: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("want 2 peers in one room, got %d", len(peers))
	}
}

// Eine erneute Anmeldung derselben Session ist kein zweiter Teilnehmer.
// Sonst steht nach jedem Neustart ein Geist mehr in der Liste, und die Frage
// "wer arbeitet hier gerade" wird mit jeder Sitzung unbrauchbarer.
func TestRegisterIsIdempotentPerSession(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("github.com/deadweight-labs/ghosttree")
	a := CoordAgent{ExternalID: "sess-a", Provider: "claude", RoomKey: room, DisplayName: "Claude-A"}

	first, err := s.RegisterCoordAgent(a)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := s.RegisterCoordAgent(a)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first != second {
		t.Fatalf("same session got two agent ids: %d and %d", first, second)
	}
}

// Ein Raum trennt, was getrennt gehört. Der Maschinenraum ist an den Host
// gebunden und nicht an "alle an diesem Server": sonst bedeutet "ich starte
// Postgres neu" irgendwann für einen fremden Agenten etwas anderes als für
// den eigenen.
func TestProjectAndMachineRoomsDoNotMix(t *testing.T) {
	s := openTest(t)
	project := RoomKeyForProject("github.com/deadweight-labs/ghosttree")
	machine := RoomKeyForMachine("mainex")

	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "sess-a",
		Provider: "claude", RoomKey: project, DisplayName: "Claude-A"}); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "sess-m",
		Provider: "codex", RoomKey: machine, DisplayName: "Codex-M"}); err != nil {
		t.Fatalf("register m: %v", err)
	}

	peers, err := s.CoordPeers(project, "")
	if err != nil {
		t.Fatalf("peers: %v", err)
	}
	if len(peers) != 1 || peers[0].ExternalID != "sess-a" {
		t.Fatalf("machine agent leaked into the project room: %+v", peers)
	}
}
