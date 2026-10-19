package store

import (
	"errors"
	"sync"
	"testing"
)

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

func TestReregisterKeepsInitialCompatibilityRoom(t *testing.T) {
	s := openTest(t)
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "sess-a", Provider: "test", RoomKey: "machine:host", DisplayName: "A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "sess-a", Provider: "test", RoomKey: "project:repo", DisplayName: "A"}); err != nil {
		t.Fatal(err)
	}
	var room string
	if err := s.db.QueryRow(`SELECT room_key FROM coord_agents WHERE external_id='sess-a'`).Scan(&room); err != nil {
		t.Fatal(err)
	}
	if room != "machine:host" {
		t.Fatalf("compatibility room overwritten: %q", room)
	}
}

func TestAgentOwnershipClaimIsAtomicAndStable(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("repo")
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, agent := range []CoordAgent{
		{ExternalID: "shared", Provider: "test", RoomKey: room, DisplayName: "A", Person: "robin", PrincipalID: "person:1"},
		{ExternalID: "shared", Provider: "test", RoomKey: room, DisplayName: "B", Person: "philipp", PrincipalID: "person:2"},
	} {
		wg.Add(1)
		go func(agent CoordAgent) {
			defer wg.Done()
			<-start
			_, err := s.RegisterCoordAgent(agent)
			results <- err
		}(agent)
	}
	close(start)
	wg.Wait()
	close(results)
	var successes, rejected int
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrCoordAgentOwned) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || rejected != 1 {
		t.Fatalf("successes=%d rejected=%d", successes, rejected)
	}
}

func TestLegacyAgentClaimRequiresMatchingAuthenticatedLabel(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("repo")
	if _, err := s.db.Exec(`INSERT INTO coord_agents(external_id,provider,room_key,display_name,person,registered_at,last_seen_at)
		VALUES('legacy','test',?,'legacy','robin','2020-01-01T00:00:00Z','2020-01-01T00:00:00Z')`, room); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "legacy", Provider: "test", RoomKey: room, DisplayName: "legacy", Person: "philipp", PrincipalID: "person:2"}); !errors.Is(err, ErrCoordAgentOwned) {
		t.Fatalf("mismatched legacy claim err=%v", err)
	}
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "legacy", Provider: "test", RoomKey: room, DisplayName: "legacy", Person: "robin", PrincipalID: "person:1"}); err != nil {
		t.Fatalf("matching legacy claim: %v", err)
	}
	owner, registered, err := s.CoordAgentOwner("legacy")
	if err != nil || !registered || owner != "person:1" {
		t.Fatalf("owner=%q registered=%v err=%v", owner, registered, err)
	}
	if _, err := s.db.Exec(`INSERT INTO coord_agents(external_id,provider,room_key,display_name,person,registered_at,last_seen_at)
		VALUES('empty-owner','test',?,'empty','', '2020-01-01T00:00:00Z','2020-01-01T00:00:00Z')`, room); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterCoordAgent(CoordAgent{ExternalID: "empty-owner", Provider: "test", RoomKey: room, DisplayName: "empty", Person: "robin", PrincipalID: "person:1"}); !errors.Is(err, ErrCoordAgentOwned) {
		t.Fatalf("empty legacy owner was freely claimable: %v", err)
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

// AC-6 von REQ-350, zweite Hälfte: zwei Worktrees desselben Repos teilen den
// Projektraum, bleiben aber unterscheidbar. Derselbe Pfad im selben Checkout
// ist ein anderes Risiko als derselbe Pfad in zwei Worktrees — wer das
// verschmilzt, warnt entweder zu oft oder zu selten.
func TestTwoWorktreesShareTheRoomButStayDistinguishable(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("github.com/deadweight-labs/ghosttree")

	for _, a := range []CoordAgent{
		{ExternalID: "sess-main", Provider: "claude", RoomKey: room,
			DisplayName: "Claude-main", Branch: "main", Worktree: "/home/robin/ghosttree"},
		{ExternalID: "sess-feat", Provider: "codex", RoomKey: room,
			DisplayName: "Codex-feat", Branch: "feat/coordination",
			Worktree: "/home/robin/ghosttree/.claude/worktrees/feat"},
	} {
		if _, err := s.RegisterCoordAgent(a); err != nil {
			t.Fatalf("register %s: %v", a.ExternalID, err)
		}
	}

	peers, err := s.CoordPeers(room, "")
	if err != nil {
		t.Fatalf("peers: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("both worktrees belong in the same room, got %d peers", len(peers))
	}
	seen := map[string]string{}
	for _, p := range peers {
		seen[p.ExternalID] = p.Worktree
	}
	if seen["sess-main"] == seen["sess-feat"] {
		t.Fatalf("the two worktrees are indistinguishable: %v", seen)
	}
	if seen["sess-main"] == "" || seen["sess-feat"] == "" {
		t.Fatalf("the worktree must survive registration: %v", seen)
	}
}
