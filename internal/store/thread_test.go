package store

import (
	"strings"
	"testing"
	"time"
)

func newThread(t *testing.T, s *Store, title string) int64 {
	t.Helper()
	id, err := s.CreateThread(Thread{Project: "p", Title: title,
		Question: "gilt das noch?", Person: "robin"})
	if err != nil {
		t.Fatalf("create %q: %v", title, err)
	}
	return id
}

// Ein Thread braucht einen Titel. Er ist das, wonach jemand in sechs Monaten
// sucht — ein namenloser Thread ist ein verlorener.
func TestThreadNeedsATitle(t *testing.T) {
	s := openTest(t)
	if _, err := s.CreateThread(Thread{Project: "p", Title: "   "}); err == nil {
		t.Fatal("a thread without a title must be rejected")
	}
}

// AC-1: ein Thread wird angelegt, beantwortet und aus einer SPÄTEREN Session
// fortgeführt. Beiträge sind gewöhnliche CoordMessages mit
// destination_kind=discussion — dieselbe Primitive wie im Raum.
func TestThreadContinuesAcrossSessions(t *testing.T) {
	s := openTest(t)
	id := newThread(t, s, "Warum kehrt der Fehler nach Kontextwechseln zurück?")
	dest := ThreadDestinationID(id)

	for i, post := range []struct{ sess, body string }{
		{"sess-heute", "ich vermute den Parser"},
		{"sess-morgen", "der Parser war es nicht, der Cache ist es"},
	} {
		if _, err := s.AppendCoordMessage(CoordMessage{
			DestinationKind: DestinationDiscussion, DestinationID: dest,
			SenderExternalID: post.sess, ClientID: string(rune('a' + i)),
			Body: post.body}); err != nil {
			t.Fatalf("post %d: %v", i, err)
		}
	}

	posts, err := s.CoordMessagesSince(DestinationDiscussion, dest, 0, 50)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(posts) != 2 {
		t.Fatalf("want 2 posts from two sessions, got %d", len(posts))
	}
	if posts[0].Sequence != 1 || posts[1].Sequence != 2 {
		t.Fatalf("thread posts must carry their own sequence: %+v", posts)
	}
}

// AC-2: derselbe Thread hängt an mehreren Objekten und ist von jedem aus
// erreichbar — als EIN Thread, nicht als drei Kopien mit auseinander
// laufendem Verlauf.
func TestOneThreadIsReachableFromEveryObjectItHangsOn(t *testing.T) {
	s := openTest(t)
	id := newThread(t, s, "Gilt Pitfall #846 nach dem Refactoring noch?")

	for _, l := range []ThreadLink{
		{ThreadID: id, Kind: "knowledge", ID: "846"},
		{ThreadID: id, Kind: "request", ID: "349"},
		{ThreadID: id, Kind: "document", ID: "360", Revision: "1"},
	} {
		if err := s.LinkThread(l); err != nil {
			t.Fatalf("link %s: %v", l.Kind, err)
		}
	}

	for _, probe := range []struct{ kind, id string }{
		{"knowledge", "846"}, {"request", "349"}, {"document", "360"},
	} {
		got, err := s.ThreadsForObject(probe.kind, probe.id)
		if err != nil {
			t.Fatalf("from %s: %v", probe.kind, err)
		}
		if len(got) != 1 || got[0].ID != id {
			t.Fatalf("from %s %s: want the one thread %d, got %+v", probe.kind, probe.id, id, got)
		}
	}

	links, err := s.ThreadLinks(id)
	if err != nil {
		t.Fatalf("links: %v", err)
	}
	if len(links) != 3 {
		t.Fatalf("want 3 links on one thread, got %d", len(links))
	}
}

// AC-3 und AC-4: die Zusammenfassung nennt ihren Quellenstand, und der
// Originalverlauf bleibt nach ihr vollständig lesbar. Eine Karte, die nicht
// sagt, wie weit sie reicht, veraltet unbemerkt.
func TestSummaryCarriesItsSourceStateAndDoesNotReplaceTheHistory(t *testing.T) {
	s := openTest(t)
	id := newThread(t, s, "Wie versionieren wir den API-Vertrag?")
	dest := ThreadDestinationID(id)

	for i := 0; i < 45; i++ {
		if _, err := s.AppendCoordMessage(CoordMessage{
			DestinationKind: DestinationDiscussion, DestinationID: dest,
			SenderExternalID: "sess-a", ClientID: "post-" + string(rune('A'+i%26)) + string(rune('0'+i/26)),
			Body: "Beitrag"}); err != nil {
			t.Fatalf("post %d: %v", i, err)
		}
	}

	if _, err := s.PutThreadSummary(ThreadSummary{ThreadID: id,
		Body:          "Stand: additive Änderungen, nextCursor optional",
		OpenQuestions: "Was passiert mit Clients, die items strikt validieren?",
		CoversThrough: 40}); err != nil {
		t.Fatalf("summary: %v", err)
	}

	sum, ok, err := s.LatestThreadSummary(id)
	if err != nil || !ok {
		t.Fatalf("latest summary: %v ok=%v", err, ok)
	}
	if sum.CoversThrough != 40 {
		t.Fatalf("the summary must name its source state, got %d", sum.CoversThrough)
	}

	// Der Verlauf ist vollständig geblieben, und die fünf Beiträge nach dem
	// Quellenstand sind auffindbar — daran erkennt ein Leser, dass ihm
	// gegenüber der Karte etwas fehlt.
	all, err := s.CoordMessagesSince(DestinationDiscussion, dest, 0, 200)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(all) != 45 {
		t.Fatalf("the original history must survive the summary, got %d of 45", len(all))
	}
	var newer int
	for _, m := range all {
		if m.Sequence > sum.CoversThrough {
			newer++
		}
	}
	if newer != 5 {
		t.Fatalf("want 5 posts past the summary's reach, got %d", newer)
	}
}

// Spec §B4: eine frühere Fassung bleibt lesbar. Ohne das kann niemand
// prüfen, ob eine Zusammenfassung einen Einwand verloren hat.
func TestSummaryRevisionsAccumulateInsteadOfOverwriting(t *testing.T) {
	s := openTest(t)
	id := newThread(t, s, "Thema")

	first, err := s.PutThreadSummary(ThreadSummary{ThreadID: id, Body: "erste Fassung", CoversThrough: 10})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := s.PutThreadSummary(ThreadSummary{ThreadID: id, Body: "zweite Fassung", CoversThrough: 20})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first != 1 || second != 2 {
		t.Fatalf("revisions must count up: %d then %d", first, second)
	}
	sum, _, err := s.LatestThreadSummary(id)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Revision != 2 || sum.Body != "zweite Fassung" {
		t.Fatalf("latest summary is wrong: %+v", sum)
	}
}

// Ein Thread ohne Zusammenfassung ist lesbar, nur roh. Spec §B4: fällt der
// Zusammenfassungsdienst aus, bleibt der Thread benutzbar.
func TestAThreadWithoutASummaryIsStillUsable(t *testing.T) {
	s := openTest(t)
	id := newThread(t, s, "Thema")
	_, ok, err := s.LatestThreadSummary(id)
	if err != nil {
		t.Fatalf("a missing summary must not be an error: %v", err)
	}
	if ok {
		t.Fatal("there is no summary yet")
	}
}

// AC-5: ein Thread SCHLÄGT VOR. Der Vorschlag steht auf proposed, bis
// jemand anderes entscheidet — und ein abgelehnter Vorschlag bleibt als
// Ergebnis stehen, damit dieselbe Idee nicht in drei Monaten erneut
// durchdiskutiert wird.
func TestOutcomesStartAsProposalsAndRejectionIsAResult(t *testing.T) {
	s := openTest(t)
	id := newThread(t, s, "Thema")

	if err := s.PutThreadOutcome(ThreadOutcome{ThreadID: id, Kind: "decision", RefID: "neu-1",
		Note: "Presence-Ablauf serverseitig per TTL"}); err != nil {
		t.Fatalf("propose: %v", err)
	}
	got, err := s.ThreadOutcomes(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].State != OutcomeProposed {
		t.Fatalf("an outcome must start as a proposal: %+v", got)
	}
	if got[0].DecidedAt != "" {
		t.Fatal("a proposal has not been decided yet")
	}

	if err := s.PutThreadOutcome(ThreadOutcome{ThreadID: id, Kind: "decision", RefID: "neu-1",
		State: OutcomeRejected, Note: "verworfen: Harness-Lifecycle ist uneinheitlich"}); err != nil {
		t.Fatalf("reject: %v", err)
	}
	got, err = s.ThreadOutcomes(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].State != OutcomeRejected {
		t.Fatalf("the rejection must replace the proposal, not add a second row: %+v", got)
	}
	if got[0].DecidedAt == "" {
		t.Fatal("a decided outcome must carry when it was decided")
	}
	if !strings.Contains(got[0].Note, "verworfen") {
		t.Fatalf("the reason must survive: %q", got[0].Note)
	}
}

// AC-7: ruhend ist NICHT gelöst. Ein ruhender Thread trägt weiterhin
// state=open, und ein neuer Beitrag macht ihn wieder aktiv.
func TestDormantIsNotResolvedAndActivityRevivesIt(t *testing.T) {
	s := openTest(t)
	id := newThread(t, s, "lange still")

	old := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := s.DB().Exec(`UPDATE threads SET updated_at=? WHERE id=?`, old, id); err != nil {
		t.Fatalf("age it: %v", err)
	}

	got, err := s.ThreadByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Dormant {
		t.Fatal("a thread untouched for 30 days must read as dormant")
	}
	if got.State != ThreadOpen {
		t.Fatalf("dormant is not resolved: state is %q", got.State)
	}

	if err := s.TouchThread(id); err != nil {
		t.Fatalf("touch: %v", err)
	}
	got, err = s.ThreadByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dormant {
		t.Fatal("activity must revive a dormant thread")
	}
}

// Ein beantworteter Thread ruht nicht — er ist fertig, kein vergessener.
func TestResolvedThreadsDoNotGoDormant(t *testing.T) {
	s := openTest(t)
	id := newThread(t, s, "beantwortet")
	if err := s.SetThreadState(id, ThreadResolved); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	old := time.Now().UTC().Add(-90 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := s.DB().Exec(`UPDATE threads SET updated_at=? WHERE id=?`, old, id); err != nil {
		t.Fatalf("age it: %v", err)
	}
	got, err := s.ThreadByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dormant {
		t.Fatal("a resolved thread is finished, not forgotten")
	}
}

// Wiedereröffnen ist derselbe Aufruf mit open und setzt resolved_at zurück.
func TestReopeningClearsTheResolution(t *testing.T) {
	s := openTest(t)
	id := newThread(t, s, "doch nicht fertig")
	if err := s.SetThreadState(id, ThreadResolved); err != nil {
		t.Fatal(err)
	}
	if err := s.SetThreadState(id, ThreadOpen); err != nil {
		t.Fatal(err)
	}
	got, err := s.ThreadByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != ThreadOpen || got.ResolvedAt != "" {
		t.Fatalf("reopening must clear the resolution: %+v", got)
	}
}

// Archiviert ist eine Sichtbarkeitsentscheidung, keine Löschung: der Thread
// verschwindet aus der Standardansicht und bleibt auffindbar.
func TestArchivedThreadsLeaveTheDefaultViewButStaySearchable(t *testing.T) {
	s := openTest(t)
	keep := newThread(t, s, "sichtbar")
	hide := newThread(t, s, "archiviert")
	if err := s.SetThreadArchived(hide, true); err != nil {
		t.Fatalf("archive: %v", err)
	}

	visible, err := s.SearchThreads("p", "", false, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 1 || visible[0].ID != keep {
		t.Fatalf("the default view must hide archived threads: %+v", visible)
	}

	all, err := s.SearchThreads("p", "", true, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("an archived thread must stay findable, got %d", len(all))
	}
}

// Ein erfundener Zustand wird abgewiesen, statt still in der Tabelle zu
// landen.
func TestUnknownThreadStateIsRejected(t *testing.T) {
	s := openTest(t)
	id := newThread(t, s, "Thema")
	if err := s.SetThreadState(id, "erledigt"); err == nil {
		t.Fatal("an invented thread state must be rejected")
	}
}
