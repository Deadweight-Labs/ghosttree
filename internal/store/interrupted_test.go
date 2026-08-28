package store

import (
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const testProject = "github.com/x/y"

func silentSince(t *testing.T, s *Store, sessionID int64, ts string) {
	t.Helper()
	if _, err := s.DB().Exec(`UPDATE sessions SET last_seen_at=? WHERE id=?`, ts, sessionID); err != nil {
		t.Fatal(err)
	}
}

func openThread(t *testing.T, s *Store, title, externalID string) (int64, int64) {
	t.Helper()
	sessionID, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: externalID,
		Scope: scope.Axes{Project: testProject}})
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
		Type: "feature", Title: title, Scope: scope.Axes{Project: testProject}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartRequestWork(created.Request.ID, sessionID, "primary", "alice"); err != nil {
		t.Fatal(err)
	}
	return created.Request.ID, sessionID
}

// Eine Arbeit, die niemand pausiert hat, deren Sitzung aber still geworden ist,
// ist faktisch unterbrochen. Der Zustand wird aus der Sitzungsaktivität
// abgeleitet, weil niemand daran denkt, ihn zu setzen.
func TestActiveWorkOfASilentSessionIsInterrupted(t *testing.T) {
	s := openTest(t)
	requestID, sessionID := openThread(t, s, "Ghost-Dateien", "gone")
	silentSince(t, s, sessionID, "2026-08-24T20:00:00Z")

	threads, err := s.InterruptedWork(scope.Axes{Project: testProject}, "2026-08-24T20:20:00Z", "", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 {
		t.Fatalf("threads = %+v, want the one silent thread", threads)
	}
	got := threads[0]
	if got.RequestID != requestID || got.Title != "Ghost-Dateien" {
		t.Fatalf("thread = %+v, want request %d by name", got, requestID)
	}
	if got.Since != "2026-08-24T20:00:00Z" {
		t.Fatalf("since = %q, want the moment the session went silent", got.Since)
	}
	if got.Handoff != "" || !got.Derived {
		t.Fatalf("thread = %+v, want a derived thread without handoff", got)
	}
}

// Eine Sitzung, die noch spricht, arbeitet — sie meldet sich nicht selbst als
// unterbrochen.
func TestASessionStillSpeakingIsNotInterrupted(t *testing.T) {
	s := openTest(t)
	_, sessionID := openThread(t, s, "läuft gerade", "alive")
	silentSince(t, s, sessionID, "2026-08-24T20:19:00Z")

	threads, err := s.InterruptedWork(scope.Axes{Project: testProject}, "2026-08-24T20:00:00Z", "", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 0 {
		t.Fatalf("threads = %+v, want none while the session is still active", threads)
	}
}

// Die eigene Sitzung ist ausgenommen, auch wenn sie alt genug wäre: eine
// wiederaufgenommene Sitzung feuert session-start ein zweites Mal, und ihr
// eigener Faden ist nicht der, den sie sucht.
func TestTheAskingSessionIsNeverItsOwnInterruptedThread(t *testing.T) {
	s := openTest(t)
	_, sessionID := openThread(t, s, "meine eigene Arbeit", "self")
	silentSince(t, s, sessionID, "2026-08-24T18:00:00Z")

	threads, err := s.InterruptedWork(scope.Axes{Project: testProject}, "2026-08-24T20:20:00Z", "self", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 0 {
		t.Fatalf("threads = %+v, want none: the asking session is its own thread", threads)
	}
}

// Der Fall, für den das Ganze gebaut wurde: die Übergabe existiert, wurde
// ordentlich geschrieben — und wird bis heute nur gefunden, wenn jemand im
// Prompt danach fragt.
func TestPausedWorkIsInterruptedAndCarriesItsHandoff(t *testing.T) {
	s := openTest(t)
	requestID, sessionID := openThread(t, s, "Ghost-Dateien", "handed-over")
	detail, err := s.RequestByID(requestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRequestWork(detail.Work[0].ID, "paused", "Schema steht, Rollout fehlt", "alice"); err != nil {
		t.Fatal(err)
	}
	silentSince(t, s, sessionID, "2026-08-24T20:00:00Z")

	threads, err := s.InterruptedWork(scope.Axes{Project: testProject}, "2026-08-24T20:20:00Z", "", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 {
		t.Fatalf("threads = %+v, want the paused thread", threads)
	}
	if threads[0].Handoff != "Schema steht, Rollout fehlt" {
		t.Fatalf("handoff = %q, want the summary that was written", threads[0].Handoff)
	}
	if threads[0].Derived {
		t.Fatalf("thread = %+v: a thread someone paused on purpose is not derived", threads[0])
	}
}

// Fertige Arbeit ist kein offener Faden, und ein erledigter Request auch nicht.
func TestFinishedWorkAndClosedRequestsAreNotThreads(t *testing.T) {
	s := openTest(t)
	requestID, sessionID := openThread(t, s, "erledigt", "done")
	detail, _ := s.RequestByID(requestID)
	if _, err := s.FinishRequestWork(detail.Work[0].ID, "completed", "fertig", "alice"); err != nil {
		t.Fatal(err)
	}
	silentSince(t, s, sessionID, "2026-08-24T20:00:00Z")

	threads, err := s.InterruptedWork(scope.Axes{Project: testProject}, "2026-08-24T20:20:00Z", "", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 0 {
		t.Fatalf("threads = %+v, want none for completed work", threads)
	}
}

// Ein fremdes Projekt geht diese Sitzung nichts an.
func TestAThreadOfAnotherProjectStaysThere(t *testing.T) {
	s := openTest(t)
	_, sessionID := openThread(t, s, "woanders", "elsewhere")
	silentSince(t, s, sessionID, "2026-08-24T20:00:00Z")

	threads, err := s.InterruptedWork(scope.Axes{Project: "github.com/other/repo"}, "2026-08-24T20:20:00Z", "", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 0 {
		t.Fatalf("threads = %+v, want none outside the project", threads)
	}
}
