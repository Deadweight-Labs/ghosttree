package store

import (
	"strings"
	"testing"
)

func roomWithMessages(t *testing.T, s *Store, room string, bodies ...string) []int64 {
	t.Helper()
	var ids []int64
	for i, b := range bodies {
		id, err := s.AppendCoordMessage(CoordMessage{
			DestinationKind: DestinationRoom, DestinationID: room,
			SenderExternalID: "sess-a", ClientID: room + string(rune('a'+i)), Body: b})
		if err != nil {
			t.Fatalf("append %q: %v", b, err)
		}
		ids = append(ids, id)
	}
	return ids
}

// BRIDGE-1 aus Spec §6: ausgewählte Beiträge werden dauerhaftes Thema, und
// die Quelle wird GESICHERT statt verlinkt. "Ein Link auf eine später
// gelöschte Nachricht reicht nicht."
func TestPromotedSourcesSurviveTheDeletionOfTheOriginal(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("p")
	ids := roomWithMessages(t, s, room,
		"ich ändere die Pagination", "bleibt items erhalten?", "Geplauder")

	res, err := s.PromoteMessagesToThread(room, ids[:2],
		Thread{Project: "p", Title: "Wie versionieren wir den API-Vertrag?"}, "robin")
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if res.Copied != 2 {
		t.Fatalf("want 2 copied, got %d (skipped %v)", res.Copied, res.Skipped)
	}

	// Die Chatnachrichten verschwinden — Aufbewahrungsfrist abgelaufen.
	if _, err := s.DB().Exec(`DELETE FROM coord_messages WHERE destination_id=?`, room); err != nil {
		t.Fatalf("simulate retention: %v", err)
	}

	sources, err := s.ThreadSources(res.ThreadID)
	if err != nil {
		t.Fatalf("sources: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("the sources must outlive the chat, got %d", len(sources))
	}
	if !strings.Contains(sources[0].Body, "Pagination") {
		t.Fatalf("the exact wording must survive: %+v", sources[0])
	}
	if sources[0].Author != "sess-a" {
		t.Errorf("the author must survive: %+v", sources[0])
	}
}

// Die Übernahme vergrößert die Sichtbarkeit NICHT. Sonst wäre "als Thema
// weiterführen" der bequemste Weg, einen DM öffentlich zu machen — und
// niemand würde es merken.
func TestPromotingFromAPrivateRoomKeepsItPrivate(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForDirect([]string{"sess-a", "sess-b"})
	if err := s.EnsureCoordRoom(CoordRoom{Key: room, Kind: RoomDirect,
		Members: []string{"sess-a", "sess-b"}}); err != nil {
		t.Fatal(err)
	}
	ids := roomWithMessages(t, s, room, "vertraulich besprochen")

	res, err := s.PromoteMessagesToThread(room, ids,
		Thread{Project: "p", Title: "Daraus wurde ein Thema"}, "robin")
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if !res.Restricted {
		t.Fatal("a thread from a private room must be marked restricted")
	}

	for _, member := range []string{"sess-a", "sess-b"} {
		ok, err := s.MayReadThread(res.ThreadID, member)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("%s was in the conversation and must be able to read", member)
		}
	}
	ok, err := s.MayReadThread(res.ThreadID, "sess-fremd")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("promoting a DM made it readable to an outsider")
	}
}

// Ein gewöhnlicher Projektthread ist projektweit sichtbar — das ist die
// richtige Voreinstellung für eine Untersuchung, die allen nützt.
func TestAThreadFromTheProjectRoomStaysProjectVisible(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("p")
	ids := roomWithMessages(t, s, room, "offen besprochen")
	res, err := s.PromoteMessagesToThread(room, ids, Thread{Project: "p", Title: "Offen"}, "robin")
	if err != nil {
		t.Fatal(err)
	}
	if res.Restricted {
		t.Fatal("a project thread must not be restricted")
	}
	ok, err := s.MayReadThread(res.ThreadID, "irgendwer")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("a project thread must be readable in the project")
	}
}

// Beiträge aus einem anderen Raum werden übersprungen und das AUSGEWIESEN.
// Eine Übernahme, die stillschweigend weniger mitnimmt als verlangt, ist die
// gefährlichere Variante.
func TestMessagesFromAnotherRoomAreSkippedVisibly(t *testing.T) {
	s := openTest(t)
	mine := RoomKeyForProject("p")
	other := RoomKeyForMachine("mainex")
	minIDs := roomWithMessages(t, s, mine, "gehört dazu")
	otherIDs := roomWithMessages(t, s, other, "gehört nicht dazu")

	res, err := s.PromoteMessagesToThread(mine, append(minIDs, otherIDs...),
		Thread{Project: "p", Title: "Gemischt"}, "robin")
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if res.Copied != 1 {
		t.Fatalf("only the message from the named room belongs, copied %d", res.Copied)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "different room") {
		t.Fatalf("a skipped message must be reported with a reason: %v", res.Skipped)
	}
	sources, err := s.ThreadSources(res.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || strings.Contains(sources[0].Body, "nicht") {
		t.Fatalf("the foreign message was copied anyway: %+v", sources)
	}
}

// Wenn gar nichts übernommen werden konnte, entsteht kein leerer Thread, der
// eine Herkunft behauptet.
func TestPromotingNothingCreatesNothing(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForProject("p")
	if _, err := s.PromoteMessagesToThread(room, []int64{999},
		Thread{Project: "p", Title: "Leer"}, "robin"); err == nil {
		t.Fatal("promoting only unknown messages must fail, not create an empty thread")
	}
	threads, err := s.SearchThreads("p", "", true, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 0 {
		t.Fatalf("a failed promotion left a thread behind: %+v", threads)
	}
}

// Die Sichtbarkeitsgrenze muss auch beim SUCHEN greifen. Ein Titel ist oft
// schon die Auskunft — ein aus einem DM übernommenes Thema in der Liste
// verrät, worüber zwei geredet haben.
func TestARestrictedThreadDoesNotAppearInAnOutsidersSearch(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForDirect([]string{"sess-a", "sess-b"})
	if err := s.EnsureCoordRoom(CoordRoom{Key: room, Kind: RoomDirect,
		Members: []string{"sess-a", "sess-b"}}); err != nil {
		t.Fatal(err)
	}
	ids := roomWithMessages(t, s, room, "vertraulich")
	res, err := s.PromoteMessagesToThread(room, ids,
		Thread{Project: "p", Title: "Kündigung von Anbieter X"}, "robin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateThread(Thread{Project: "p", Title: "Offenes Thema"}); err != nil {
		t.Fatal(err)
	}

	outsider, err := s.SearchThreadsFor("p", "", "sess-fremd", true, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range outsider {
		if th.ID == res.ThreadID {
			t.Fatalf("a restricted thread leaked into an outsider's search: %+v", th)
		}
	}
	if len(outsider) != 1 {
		t.Fatalf("the open thread must still be found, got %d", len(outsider))
	}

	member, err := s.SearchThreadsFor("p", "", "sess-a", true, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(member) != 2 {
		t.Fatalf("a member must see both, got %d", len(member))
	}
}

// Und auch nicht über den Umweg "welche Diskussionen hängen an diesem
// Pitfall". Spec §9 nennt Verknüpfungen ausdrücklich als Leckweg.
func TestARestrictedThreadDoesNotLeakThroughAnObjectLink(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForDirect([]string{"sess-a", "sess-b"})
	if err := s.EnsureCoordRoom(CoordRoom{Key: room, Kind: RoomDirect,
		Members: []string{"sess-a", "sess-b"}}); err != nil {
		t.Fatal(err)
	}
	ids := roomWithMessages(t, s, room, "vertraulich")
	res, err := s.PromoteMessagesToThread(room, ids, Thread{Project: "p", Title: "Privat"}, "robin")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkThread(ThreadLink{ThreadID: res.ThreadID, Kind: "knowledge", ID: "846"}); err != nil {
		t.Fatal(err)
	}

	outsider, err := s.ThreadsForObjectAs("knowledge", "846", "sess-fremd")
	if err != nil {
		t.Fatal(err)
	}
	if len(outsider) != 0 {
		t.Fatalf("a restricted thread leaked through its object link: %+v", outsider)
	}
	member, err := s.ThreadsForObjectAs("knowledge", "846", "sess-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(member) != 1 {
		t.Fatalf("a member must reach it from the object, got %d", len(member))
	}
}

func threadWithPosts(t *testing.T, s *Store, title string, bodies ...string) int64 {
	t.Helper()
	id, err := s.CreateThread(Thread{Project: "p", Title: title})
	if err != nil {
		t.Fatal(err)
	}
	dest := ThreadDestinationID(id)
	for i, b := range bodies {
		if _, err := s.AppendCoordMessage(CoordMessage{
			DestinationKind: DestinationDiscussion, DestinationID: dest,
			SenderExternalID: "sess-a", ClientID: dest + string(rune('a'+i)), Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// §B5: eine Abschweifung wird abgetrennt, und alte Links bleiben auflösbar.
// Die Beiträge werden KOPIERT, nicht verschoben — wer sie verschöbe, machte
// den alten Verlauf unlesbar: dort stünde eine Antwort ohne ihre Frage.
func TestSplittingCopiesAndLeavesTheOriginalIntact(t *testing.T) {
	s := openTest(t)
	src := threadWithPosts(t, s, "Pagination",
		"wie paginieren wir?", "cursor-basiert", "übrigens: Mandanten-Auth ist kaputt")

	res, err := s.SplitThread(src, []int64{3},
		Thread{Title: "Authentifizierung zwischen Mandanten"}, "robin")
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if res.Copied != 1 {
		t.Fatalf("want 1 copied, got %d (%v)", res.Copied, res.Skipped)
	}

	// Der Ursprung behält alle drei Beiträge.
	posts, err := s.CoordMessagesSince(DestinationDiscussion, ThreadDestinationID(src), 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 3 {
		t.Fatalf("splitting must not empty the original, got %d posts", len(posts))
	}

	// Und das neue Thema trägt die Quelle wörtlich.
	sources, err := s.ThreadSources(res.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || !strings.Contains(sources[0].Body, "Mandanten-Auth") {
		t.Fatalf("the split post must be carried over verbatim: %+v", sources)
	}
	if !strings.Contains(sources[0].ID, "THR-") {
		t.Errorf("the source must name where it came from: %q", sources[0].ID)
	}
}

// Beide Richtungen verlinkt: eine einseitige Referenz lässt den Ursprung so
// aussehen, als sei die Abschweifung im Sand verlaufen.
func TestSplittingLinksBothDirections(t *testing.T) {
	s := openTest(t)
	src := threadWithPosts(t, s, "Ursprung", "eins", "abschweifung")
	res, err := s.SplitThread(src, []int64{2}, Thread{Title: "Abzweig"}, "robin")
	if err != nil {
		t.Fatal(err)
	}
	forward, err := s.ThreadsForObject("thread", ThreadDestinationID(res.ThreadID))
	if err != nil {
		t.Fatal(err)
	}
	if len(forward) != 1 || forward[0].ID != src {
		t.Fatalf("the origin must point at the branch: %+v", forward)
	}
	back, err := s.ThreadsForObject("thread", ThreadDestinationID(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || back[0].ID != res.ThreadID {
		t.Fatalf("the branch must point back at its origin: %+v", back)
	}
}

// Ein Schnitt ist kein Weg, einen beschränkten Verlauf in ein offenes Thema
// zu heben.
func TestSplittingInheritsRestrictedVisibility(t *testing.T) {
	s := openTest(t)
	room := RoomKeyForDirect([]string{"sess-a", "sess-b"})
	if err := s.EnsureCoordRoom(CoordRoom{Key: room, Kind: RoomDirect,
		Members: []string{"sess-a", "sess-b"}}); err != nil {
		t.Fatal(err)
	}
	ids := roomWithMessages(t, s, room, "vertraulich")
	promoted, err := s.PromoteMessagesToThread(room, ids, Thread{Project: "p", Title: "Privat"}, "robin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendCoordMessage(CoordMessage{
		DestinationKind: DestinationDiscussion, DestinationID: ThreadDestinationID(promoted.ThreadID),
		SenderExternalID: "sess-a", ClientID: "p1", Body: "auch vertraulich"}); err != nil {
		t.Fatal(err)
	}

	split, err := s.SplitThread(promoted.ThreadID, []int64{1}, Thread{Title: "Abzweig"}, "robin")
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	ok, err := s.MayReadThread(split.ThreadID, "sess-fremd")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("splitting lifted a restricted history into an open thread")
	}
}

// Ein Beitrag, der nicht im Ursprung steht, wird übersprungen und benannt.
func TestSplittingSkipsPostsFromElsewhere(t *testing.T) {
	s := openTest(t)
	src := threadWithPosts(t, s, "Ursprung", "eins")
	if _, err := s.SplitThread(src, []int64{99}, Thread{Title: "Nichts"}, "robin"); err == nil {
		t.Fatal("splitting only unknown posts must fail rather than create an empty thread")
	}
	res, err := s.SplitThread(src, []int64{1, 99}, Thread{Title: "Teilweise"}, "robin")
	if err != nil {
		t.Fatal(err)
	}
	if res.Copied != 1 || len(res.Skipped) != 1 {
		t.Fatalf("a skipped post must be reported: copied %d skipped %v", res.Copied, res.Skipped)
	}
}
