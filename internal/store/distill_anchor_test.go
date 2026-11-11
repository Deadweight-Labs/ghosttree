package store

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// anchoredWrite lässt eine Session einen verankerten Eintrag schreiben, wie es
// der MCP-Server mit Header und Session-Kennung täte.
func anchoredWrite(t *testing.T, s *Store, session, title, commit string) {
	t.Helper()
	if _, err := s.InsertKnowledge(Knowledge{Type: "note", Title: title, Body: "b", Person: "robin", Scope: scope.Axes{Project: "p"},
		GitAnchor: anchorAt(commit, "feat/x"), OriginContext: &originctx.Context{SessionRef: session}}); err != nil {
		t.Fatal(err)
	}
}

func sourceSession(t *testing.T, s *Store, external string) int64 {
	t.Helper()
	id, err := s.UpsertSession(Session{Harness: "codex", ExternalID: external, Scope: scope.Axes{Project: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChunks(id, []Chunk{{Seq: 1, Role: "assistant", Text: "We chose SQLite because one writer is enough. Und exportieren als csv wär auch nice.", Raw: `{}`}}); err != nil {
		t.Fatal(err)
	}
	return id
}

func distilledOne(t *testing.T, s *Store) Knowledge {
	t.Helper()
	ks, err := s.KnowledgeForProject("p")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range ks {
		if k.Origin == "distilled" {
			return k
		}
	}
	t.Fatalf("no distilled entry in %+v", ks)
	return Knowledge{}
}

// Ein Destillat trägt den Stand der Quell-Session: den jüngsten Anker, den sie
// mit ihren eigenen Schreibzugriffen gemeldet hat. Die Anker anderer Sessions
// zählen nicht, und der Anker ist eine eigene Zeile, nicht die der Quelle.
func TestDistilledKnowledgeCarriesTheAnchorOfItsSourceSession(t *testing.T) {
	s := openTest(t)
	anchoredWrite(t, s, "src", "frueh", commitA)
	anchoredWrite(t, s, "src", "spaet", commitB)
	anchoredWrite(t, s, "other", "fremd", "ccccccc3333333333333333333333333333333cc")
	id := sourceSession(t, s, "src")

	items := []SessionDistilledItem{{Type: "decision", Title: "SQLite reicht", Body: "Ein Schreiber genügt.", Quote: "chose SQLite", ChunkSeq: 1}}
	if n, err := s.ApplySessionDistillation(id, "d", "v1", scope.Axes{Project: "p"}, items); err != nil || n != 1 {
		t.Fatalf("inserted %d err %v", n, err)
	}
	k := distilledOne(t, s)
	if k.GitAnchor == nil || k.GitAnchor.Commit != commitB || k.GitAnchor.Branch != "feat/x" {
		t.Fatalf("anchor of the distilled entry: %+v, want the newest one of its own session", k.GitAnchor)
	}
	var shared int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM knowledge WHERE anchor_id=(SELECT anchor_id FROM knowledge WHERE id=?)`, k.ID).Scan(&shared); err != nil || shared != 1 {
		t.Fatalf("an anchor row is never shared: %d (%v)", shared, err)
	}
}

// Hat die Session nie etwas mit Anker geschrieben, bleibt das Destillat ohne
// Anker; es wird keiner geraten und keine Zeile angelegt.
func TestDistilledKnowledgeWithoutAnAnchoredSourceStaysWithout(t *testing.T) {
	s := openTest(t)
	anchoredWrite(t, s, "other", "fremd", commitA)
	before := 0
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM git_anchors`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	id := sourceSession(t, s, "lonely")
	items := []SessionDistilledItem{{Type: "decision", Title: "SQLite reicht", Body: "x", Quote: "chose SQLite", ChunkSeq: 1}}
	if _, err := s.ApplySessionDistillation(id, "d", "v1", scope.Axes{Project: "p"}, items); err != nil {
		t.Fatal(err)
	}
	if k := distilledOne(t, s); k.GitAnchor != nil {
		t.Fatalf("anchor %+v was guessed", k.GitAnchor)
	}
	var after int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM git_anchors`).Scan(&after); err != nil || after != before {
		t.Fatalf("anchor rows %d -> %d", before, after)
	}
}

func TestDistilledWishCarriesTheAnchorOfItsSourceSession(t *testing.T) {
	s := openTest(t)
	anchoredWrite(t, s, "src", "frueh", commitA)
	id := sourceSession(t, s, "src")
	item := DistilledRequest{Type: "feature", Title: "CSV-Export", Body: "b", Quote: "exportieren als csv", ChunkSeq: 1}
	if _, err := s.ApplyRequestDistillation(id, "d", "req-v1", scope.Axes{Project: "p"}, []DistilledRequest{item}); err != nil {
		t.Fatal(err)
	}
	page, _ := s.SearchRequests(requestFilter("p"))
	if len(page.Results) != 1 {
		t.Fatalf("ledger = %+v", page.Results)
	}
	d, err := s.RequestByID(page.Results[0].Request.ID)
	if err != nil || len(d.Activity) != 1 || d.Activity[0].Kind != "request.created" || d.Activity[0].GitAnchor == nil || d.Activity[0].GitAnchor.Commit != commitA {
		t.Fatalf("activity %+v (%v)", d.Activity, err)
	}

	// Ohne Stand der Session bleibt es bei einem Eintrag ohne Ereignis, wie bisher.
	id2 := sourceSession(t, s, "lonely")
	item2 := DistilledRequest{Type: "feature", Title: "Anderer Wunsch", Body: "b", Quote: "exportieren als csv", ChunkSeq: 1}
	if _, err := s.ApplyRequestDistillation(id2, "d2", "req-v1", scope.Axes{Project: "p"}, []DistilledRequest{item2}); err != nil {
		t.Fatal(err)
	}
	page, _ = s.SearchRequests(requestFilter("p"))
	for _, r := range page.Results {
		if r.Request.Title != "Anderer Wunsch" {
			continue
		}
		if d, _ := s.RequestByID(r.Request.ID); len(d.Activity) != 0 {
			t.Fatalf("a distilled wish without a session anchor gets no event: %+v", d.Activity)
		}
	}
}

// Trägt eine Session eines anderen Kontos dieselbe Kennung (anderer Harness),
// kann dessen verankerter Schreibzugriff nicht der Quell-Session zugerechnet
// werden: Das Destillat bleibt ohne Anker, statt den fremden zu übernehmen.
func TestDistilledKnowledgeIgnoresAnchorsUnderAnIdOfAnotherAccount(t *testing.T) {
	s := openTest(t)
	for _, name := range []string{"owner", "mallory"} {
		if _, err := s.AddPerson(name); err != nil {
			t.Fatal(err)
		}
	}
	var owner, other int64
	if err := s.db.QueryRow(`SELECT id FROM persons WHERE name='owner'`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT id FROM persons WHERE name='mallory'`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	id := sourceSession(t, s, "shared-id")
	if _, err := s.db.Exec(`UPDATE sessions SET account_id=? WHERE id=?`, owner, id); err != nil {
		t.Fatal(err)
	}
	foreign, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "shared-id", Scope: scope.Axes{Project: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE sessions SET account_id=? WHERE id=?`, other, foreign); err != nil {
		t.Fatal(err)
	}
	anchoredWrite(t, s, "shared-id", "untergeschoben", commitB)

	items := []SessionDistilledItem{{Type: "decision", Title: "SQLite reicht", Body: "x", Quote: "chose SQLite", ChunkSeq: 1}}
	if _, err := s.ApplySessionDistillation(id, "d", "v1", scope.Axes{Project: "p"}, items); err != nil {
		t.Fatal(err)
	}
	if k := distilledOne(t, s); k.GitAnchor != nil {
		t.Fatalf("anchor %+v came from a session of another account", k.GitAnchor)
	}
}
