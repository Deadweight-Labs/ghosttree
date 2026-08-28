package store

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestInstructionActivationPersistsAndFiltersContext(t *testing.T) {
	s := openTest(t)
	id, err := s.InsertKnowledge(Knowledge{Type: "instruction", Title: "frontend", Body: "use pnpm"})
	if err != nil {
		t.Fatal(err)
	}
	rule := activation.Rule{Paths: []string{"packages/web/**"}}
	if err := s.SetActivation(id, rule); err != nil {
		t.Fatal(err)
	}

	k, err := s.KnowledgeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(k.Activation.Paths) != 1 || k.Activation.Paths[0] != "packages/web/**" {
		t.Fatalf("activation did not round-trip: %+v", k.Activation)
	}

	miss, err := s.KnowledgeForActivatedContext(scope.Axes{}, activation.Context{RepoPath: "packages/api"})
	if err != nil {
		t.Fatal(err)
	}
	if len(miss) != 0 {
		t.Fatalf("non-matching context returned %+v", miss)
	}
	hit, err := s.KnowledgeForActivatedContext(scope.Axes{}, activation.Context{RepoPath: "packages/web/src"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hit) != 1 || hit[0].ID != id {
		t.Fatalf("matching context returned %+v", hit)
	}
}

func TestKnowledgeKeepsAndReadsThePersonWhoVerifiedIt(t *testing.T) {
	s := openTest(t)
	id, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "two people", Body: "b", Person: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateKnowledge(id, map[string]string{"confidence": "verified", "confirmed_by": "bob"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.KnowledgeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Person != "alice" || got.ConfirmedBy != "bob" {
		t.Fatalf("provenance = person %q, confirmed_by %q, want alice/bob", got.Person, got.ConfirmedBy)
	}
}

func TestKnowledgeUpdateKeepsOriginalAuthorAndRecordsLastEditor(t *testing.T) {
	s := openTest(t)
	id, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "original", Body: "b", Person: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateKnowledgeBy(id, map[string]string{"title": "corrected"}, "bob"); err != nil {
		t.Fatal(err)
	}

	current, err := s.KnowledgeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if current.Person != "alice" || current.LastModifiedBy != "bob" || current.Title != "corrected" {
		t.Fatalf("current provenance = author %q, last editor %q, title %q", current.Person, current.LastModifiedBy, current.Title)
	}

	history, err := s.KnowledgeHistory(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Title != "original" || history[0].Person != "alice" || history[0].ChangedBy != "bob" {
		t.Fatalf("history = %+v, want original authored by alice and replaced by bob", history)
	}
}

func TestOpeningExistingKnowledgeDatabaseAddsHistoryProvenance(t *testing.T) {
	db := t.TempDir() + "/knowledge.db"
	first, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	id, err := first.InsertKnowledge(Knowledge{Type: "note", Title: "before restart", Body: "b", Person: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.DB().Exec(`ALTER TABLE knowledge DROP COLUMN last_modified_by`); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })
	if err := second.UpdateKnowledgeBy(id, map[string]string{"body": "after restart"}, "bob"); err != nil {
		t.Fatal(err)
	}
	history, err := second.KnowledgeHistory(id)
	if err != nil || len(history) != 1 {
		t.Fatalf("history after reopening = %+v, err=%v", history, err)
	}
}

func TestSetActivationRejectsNonInstructionAndReplacesAtomically(t *testing.T) {
	s := openTest(t)
	noteID, _ := s.InsertKnowledge(Knowledge{Type: "note", Title: "note", Body: "b"})
	if err := s.SetActivation(noteID, activation.Rule{Paths: []string{"any/**"}}); err == nil {
		t.Fatal("activation on non-instruction must fail")
	}
	id, _ := s.InsertKnowledge(Knowledge{Type: "instruction", Title: "rule", Body: "b"})
	if err := s.SetActivation(id, activation.Rule{Paths: []string{"old/**"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActivation(id, activation.Rule{Paths: []string{"new/**"}}); err != nil {
		t.Fatal(err)
	}
	k, _ := s.KnowledgeByID(id)
	if len(k.Activation.Paths) != 1 || k.Activation.Paths[0] != "new/**" {
		t.Fatalf("replacement left stale gates: %+v", k.Activation)
	}
}

func TestGeneralWritesMaintainActivationInvariant(t *testing.T) {
	s := openTest(t)
	id, err := s.InsertKnowledge(Knowledge{Type: "instruction", Title: "gated", Body: "b", Activation: activation.Rule{Paths: []string{"core/**"}}})
	if err != nil {
		t.Fatal(err)
	}
	k, _ := s.KnowledgeByID(id)
	if len(k.Activation.Paths) != 1 {
		t.Fatalf("insert lost activation: %+v", k)
	}
	if _, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "bad", Body: "b", Activation: activation.Rule{Paths: []string{"core/**"}}}); err == nil {
		t.Fatal("non-instruction activation accepted")
	}
	if err := s.UpdateKnowledge(id, map[string]string{"type": "note"}); err == nil {
		t.Fatal("gated instruction changed to non-instruction")
	}
	k, _ = s.KnowledgeByID(id)
	if k.Type != "instruction" || len(k.Activation.Paths) != 1 {
		t.Fatalf("failed type update mutated entry: %+v", k)
	}
}

func TestKnowledgeContextUnion(t *testing.T) {
	s := openTest(t)
	mk := func(title string, ax scope.Axes) {
		_, err := s.InsertKnowledge(Knowledge{Type: "note", Title: title, Body: "b", Scope: ax})
		if err != nil {
			t.Fatal(err)
		}
	}
	mk("global", scope.Axes{})
	mk("machine-only", scope.Axes{Machine: "workstation-a"})
	mk("project-only", scope.Axes{Project: "github.com/x/y"})
	mk("proj-branch", scope.Axes{Project: "github.com/x/y", Branch: "feat"})
	mk("proj-machine", scope.Axes{Project: "github.com/x/y", Machine: "workstation-a"})
	mk("other-machine", scope.Axes{Machine: "server-a"})
	mk("other-branch", scope.Axes{Project: "github.com/x/y", Branch: "main"})

	got, err := s.KnowledgeForContext(scope.Axes{Project: "github.com/x/y", Branch: "feat", Machine: "workstation-a"})
	if err != nil {
		t.Fatal(err)
	}
	titles := map[string]bool{}
	for _, k := range got {
		titles[k.Title] = true
	}
	for _, want := range []string{"global", "machine-only", "project-only", "proj-branch", "proj-machine"} {
		if !titles[want] {
			t.Errorf("missing %q in context result", want)
		}
	}
	if titles["other-machine"] || titles["other-branch"] {
		t.Errorf("got out-of-scope entries: %v", titles)
	}
}

func TestKnowledgeOrderingAndStatus(t *testing.T) {
	s := openTest(t)
	id1, _ := s.InsertKnowledge(Knowledge{Type: "note", Title: "obs", Body: "b"})
	s.InsertKnowledge(Knowledge{Type: "note", Title: "ver", Body: "b", Confidence: "verified"})
	s.InsertKnowledge(Knowledge{Type: "note", Title: "gone", Body: "b"})
	if err := s.UpdateKnowledge(3, map[string]string{"status": "deprecated"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.KnowledgeForContext(scope.Axes{})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (deprecated filtered)", len(got))
	}
	if got[0].Title != "ver" {
		t.Errorf("verified must sort first, got %q", got[0].Title)
	}
	_ = id1
}

func TestQuarantinedIsInvisibleUntilApproved(t *testing.T) {
	s := openTest(t)
	s.InsertKnowledge(Knowledge{Type: "note", Title: "quarantined finding about private network", Body: "b",
		Origin: "distilled", Confidence: "quarantined"})
	s.InsertKnowledge(Knowledge{Type: "note", Title: "staged finding about private network", Body: "b",
		Origin: "distilled", Confidence: "staged"})

	ctx, err := s.KnowledgeForContext(scope.Axes{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx) != 0 {
		t.Errorf("binding context must hide staged and quarantined, got %+v", ctx)
	}
	preview, err := s.KnowledgeForActivatedPreview(scope.Axes{}, activation.Context{})
	if err != nil || len(preview) != 1 || preview[0].Confidence != "staged" {
		t.Errorf("explicit preview=%+v err=%v", preview, err)
	}
	hits, _ := s.SearchKnowledge("private network", scope.Axes{}, 10)
	if len(hits) != 1 {
		t.Errorf("search returned %d hits, want 1 (quarantined excluded)", len(hits))
	}
}

func TestContextOrdersByTrust(t *testing.T) {
	s := openTest(t)
	s.InsertKnowledge(Knowledge{Type: "note", Title: "c-staged", Body: "b", Origin: "distilled", Confidence: "staged"})
	s.InsertKnowledge(Knowledge{Type: "note", Title: "b-trusted", Body: "b", Confidence: "trusted"})
	s.InsertKnowledge(Knowledge{Type: "note", Title: "a-verified", Body: "b", Confidence: "verified"})
	got, _ := s.KnowledgeForContext(scope.Axes{})
	var order []string
	for _, k := range got {
		order = append(order, k.Title)
	}
	want := []string{"a-verified", "b-trusted"}
	for i := range want {
		if i >= len(order) || order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestInsertDefaultsByOrigin(t *testing.T) {
	s := openTest(t)
	id, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "from an agent", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	k, _ := s.KnowledgeByID(id)
	if k.Origin != "agent" || k.Confidence != "trusted" {
		t.Errorf("agent default = %q/%q, want agent/trusted", k.Origin, k.Confidence)
	}

	id, err = s.InsertKnowledge(Knowledge{Type: "note", Title: "from a distiller", Body: "b", Origin: "distilled"})
	if err != nil {
		t.Fatal(err)
	}
	k, _ = s.KnowledgeByID(id)
	if k.Confidence != "quarantined" {
		t.Errorf("distilled default = %q, want quarantined", k.Confidence)
	}
	if k.SupersededBy != 0 {
		t.Errorf("SupersededBy = %d, want 0", k.SupersededBy)
	}
}

func TestInsertRejectsUnknownConfidence(t *testing.T) {
	s := openTest(t)
	if _, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "t", Body: "b", Confidence: "observation"}); err == nil {
		t.Error("the old 'observation' value must be rejected by the CHECK")
	}
}

func TestSearchKnowledge(t *testing.T) {
	s := openTest(t)
	s.InsertKnowledge(Knowledge{Type: "pitfall", Title: "ufw drops LAN", Body: "ssh only via private network", Scope: scope.Axes{Machine: "server-a"}})
	s.InsertKnowledge(Knowledge{Type: "note", Title: "unrelated", Body: "nothing"})
	got, err := s.SearchKnowledge("private network", scope.Axes{}, 10)
	if err != nil || len(got) != 1 || got[0].Scope.Machine != "server-a" {
		t.Fatalf("got %v err %v", got, err)
	}
	got, _ = s.SearchKnowledge("private network", scope.Axes{Machine: "workstation-a"}, 10)
	if len(got) != 0 {
		t.Errorf("machine filter must exclude server-a entry")
	}
}

func TestKnowledgeWritesMaintainSharedProjection(t *testing.T) {
	s := openTest(t)
	id, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "first title", Body: "first body", Scope: scope.Axes{Project: "github.com/x/y"}})
	if err != nil {
		t.Fatal(err)
	}
	var title, body string
	if err := s.DB().QueryRow(`SELECT title,body FROM search_documents WHERE kind='knowledge' AND domain_id=?`, id).Scan(&title, &body); err != nil {
		t.Fatal(err)
	}
	if title != "first title" || body != "first body" {
		t.Fatalf("projection = %q %q", title, body)
	}
	if err := s.UpdateKnowledge(id, map[string]string{"title": "updated title", "body": "updated body"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT title,body FROM search_documents WHERE kind='knowledge' AND domain_id=?`, id).Scan(&title, &body); err != nil {
		t.Fatal(err)
	}
	if title != "updated title" || body != "updated body" {
		t.Fatalf("updated projection = %q %q", title, body)
	}
}

func TestNewTypesAndArchivedStatus(t *testing.T) {
	s := openTest(t)
	if _, err := s.InsertKnowledge(Knowledge{Type: "instruction", Title: "t-instruction", Body: "b"}); err != nil {
		t.Errorf("instruction must be allowed: %v", err)
	}
	if _, err := s.InsertKnowledge(Knowledge{Type: "request", Title: "wrong domain", Body: "b"}); err == nil {
		t.Error("request must be rejected by the knowledge write path")
	}
	id, err := s.InsertKnowledge(Knowledge{Type: "plan", Title: "old spec", Body: "b", Status: "archived"})
	if err != nil {
		t.Fatalf("status archived must be allowed: %v", err)
	}
	for _, k := range mustContext(t, s) {
		if k.ID == id {
			t.Error("archived entry must not appear in KnowledgeForContext")
		}
	}
	if hits, _ := s.SearchKnowledge("spec", scope.Axes{}, 10); len(hits) != 0 {
		t.Errorf("agent search must hide archived entries, got %d hits", len(hits))
	}
	if hits, _ := s.SearchAllKnowledge("spec", scope.Axes{}, 10); len(hits) != 1 {
		t.Errorf("operator search must retain archived entries, got %d hits", len(hits))
	}
}

func mustContext(t *testing.T, s *Store) []Knowledge {
	t.Helper()
	ks, err := s.KnowledgeForContext(scope.Axes{})
	if err != nil {
		t.Fatal(err)
	}
	return ks
}

func TestSupersessionIsAtomicAndConsolidatesCorrectionChains(t *testing.T) {
	s := openTest(t)
	a, _ := s.InsertKnowledge(Knowledge{Type: "note", Title: "v1", Body: "old"})
	b, _ := s.InsertKnowledge(Knowledge{Type: "note", Title: "v2", Body: "newer"})
	c, _ := s.InsertKnowledge(Knowledge{Type: "note", Title: "v3", Body: "current"})
	if err := s.UpdateKnowledge(a, map[string]string{"superseded_by": fmt.Sprint(b)}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateKnowledge(b, map[string]string{"superseded_by": fmt.Sprint(c)}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{a, b} {
		got, _ := s.KnowledgeByID(id)
		if got.Status != "superseded" || got.SupersededBy != c {
			t.Errorf("#%d=%+v", id, got)
		}
	}
	if err := s.UpdateKnowledge(c, map[string]string{"superseded_by": fmt.Sprint(a)}); err == nil {
		t.Fatal("supersession cycle accepted")
	}
}

func TestApplyStalenessMarksOldPlansButNotDurableKnowledge(t *testing.T) {
	s := openTest(t)
	plan, _ := s.InsertKnowledge(Knowledge{Type: "plan", Title: "old rollout", Body: "steps"})
	decision, _ := s.InsertKnowledge(Knowledge{Type: "decision", Title: "old decision", Body: "why"})
	_, _ = s.db.Exec(`UPDATE knowledge SET observed_at='2026-01-01T00:00:00Z' WHERE id IN (?,?)`, plan, decision)
	n, err := s.ApplyStaleness(time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC), 90*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("applied=%d err=%v", n, err)
	}
	stale, _ := s.KnowledgeByID(plan)
	durable, _ := s.KnowledgeByID(decision)
	if stale.Status != "stale" || durable.Status != "active" {
		t.Fatalf("plan=%s decision=%s", stale.Status, durable.Status)
	}
	pending, _ := s.PendingKnowledge("", 10)
	if len(pending) != 1 || pending[0].ID != plan {
		t.Fatalf("stale plan missing from review: %+v", pending)
	}
}

// A bootstrap that ranks by recency delivers whatever the last distiller run
// produced. Corroboration is the signal that separates a systemic defect from
// a one-off observation, and it must outrank being new.
func TestContextRanksByCorroborationNotRecency(t *testing.T) {
	s := openTest(t)
	sessions := testSessions(t, s, 4)
	old, _ := s.InsertKnowledge(Knowledge{Type: "note", Title: "corroborated", Body: "b",
		Origin: "distilled", Confidence: "trusted"})
	if err := s.AddEvidence(old, []Evidence{
		{SessionID: sessions[0], ChunkSeq: 1}, {SessionID: sessions[1], ChunkSeq: 1}, {SessionID: sessions[2], ChunkSeq: 1},
	}); err != nil {
		t.Fatal(err)
	}
	fresh, _ := s.InsertKnowledge(Knowledge{Type: "note", Title: "newest", Body: "b",
		Origin: "distilled", Confidence: "trusted"})
	if err := s.AddEvidence(fresh, []Evidence{{SessionID: sessions[3], ChunkSeq: 1}}); err != nil {
		t.Fatal(err)
	}

	got, err := s.KnowledgeForContext(scope.Axes{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Title != "corroborated" {
		t.Fatalf("order = %v, want corroborated first", titles(got))
	}
}

// A deliberately written entry has no transcript evidence and would rank below
// every distilled item if corroboration were counted raw. Writing something
// down on purpose is worth one observation, not zero.
func TestHandWrittenKnowledgeCountsAsOneObservation(t *testing.T) {
	s := openTest(t)
	written, _ := s.InsertKnowledge(Knowledge{Type: "note", Title: "written", Body: "b",
		Origin: "agent", Confidence: "trusted"})
	distilled, _ := s.InsertKnowledge(Knowledge{Type: "note", Title: "distilled", Body: "b",
		Origin: "distilled", Confidence: "trusted"})
	if err := s.AddEvidence(distilled, []Evidence{{SessionID: testSessions(t, s, 1)[0], ChunkSeq: 1}}); err != nil {
		t.Fatal(err)
	}
	s.recordKnowledgeSearchHit([]Knowledge{{ID: written}})

	got, _ := s.KnowledgeForContext(scope.Axes{})
	if len(got) != 2 || got[0].Title != "written" {
		t.Fatalf("order = %v, want written first: equal corroboration, more search hits", titles(got))
	}
}

// Ranking by delivery would be circular: an entry that fits the budget is
// delivered, which raises its rank, which keeps it in the budget. Only a search
// hit says an agent went looking and this answered.
func TestDeliveryDoesNotRaiseRank(t *testing.T) {
	s := openTest(t)
	delivered, _ := s.InsertKnowledge(Knowledge{Type: "note", Title: "delivered", Body: "b", Confidence: "trusted"})
	searched, _ := s.InsertKnowledge(Knowledge{Type: "note", Title: "searched", Body: "b", Confidence: "trusted"})
	for i := 0; i < 5; i++ {
		s.recordKnowledgeUse([]Knowledge{{ID: delivered}})
	}
	s.recordKnowledgeSearchHit([]Knowledge{{ID: searched}})

	got, _ := s.KnowledgeForContext(scope.Axes{})
	if len(got) != 2 || got[0].Title != "searched" {
		t.Fatalf("order = %v, want searched first despite five deliveries of the other", titles(got))
	}
}

// testSessions creates n distinct sessions, because evidence references them
// and recurrence counts them.
func testSessions(t *testing.T, s *Store, n int) []int64 {
	t.Helper()
	out := make([]int64, n)
	for i := range out {
		id, err := s.UpsertSession(Session{
			Harness:    "claude-code",
			ExternalID: fmt.Sprintf("sess-%d-%d", len(out), i),
			StartedAt:  "2026-08-23T00:00:00Z",
		})
		if err != nil {
			t.Fatal(err)
		}
		out[i] = id
	}
	return out
}

func titles(ks []Knowledge) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = k.Title
	}
	return out
}

// Die Historie allein reicht nicht: der Eintrag, den eine Sitzung liest, muss
// selbst sagen, dass jemand anderes ihn umgeschrieben hat. Sonst behält er den
// fremden Namen und liest sich als dessen Aussage — genau der Befund, aus dem
// REQ-181 entstand, nur im aktuellen Stand statt im Verlauf.
func TestProvenanceNamesTheEditorNotOnlyTheAuthor(t *testing.T) {
	fremd := KnowledgeProvenance(Knowledge{Person: "alice", LastModifiedBy: "bob"})
	if !strings.Contains(fremd, "by alice") || !strings.Contains(fremd, "last edited by bob") {
		t.Errorf("provenance = %q, want both names", fremd)
	}
	// Wer seinen eigenen Eintrag nachbessert, erzeugt keine zweite Zeile.
	eigen := KnowledgeProvenance(Knowledge{Person: "alice", LastModifiedBy: "alice"})
	if strings.Contains(eigen, "last edited") {
		t.Errorf("provenance = %q, want no editor line when author and editor are the same", eigen)
	}
}
