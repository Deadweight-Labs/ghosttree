package store

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/deliverylog"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const (
	affHere  = "github.com/dw/here"
	affOther = "github.com/dw/other"
)

func affinityStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func affSession(t *testing.T, s *Store, ext, project string) int64 {
	t.Helper()
	id, err := s.UpsertSession(Session{Harness: "claude", ExternalID: ext, Scope: scope.Axes{Project: project, Machine: "m1"}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// affEntry writes a machine entry that arose in the session ext (named only by
// its origin context, as a write over the API does).
func affEntry(t *testing.T, s *Store, title, ext string) int64 {
	t.Helper()
	k := Knowledge{Type: "pitfall", Title: title, Body: "b", Scope: scope.Axes{Machine: "m1"}, Person: "p"}
	if ext != "" {
		k.OriginContext = &originctx.Context{SessionRef: ext}
	}
	id, err := s.InsertKnowledge(k)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func tied(t *testing.T, s *Store, project string) map[int64]bool {
	t.Helper()
	got, err := s.TiedBroadKnowledge(project)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// The three entries of REQ-641: one arose in this project, one in another, one
// carries "applies everywhere". Only the first and the third are tied to here.
func TestTiedBroadKnowledgeFollowsOriginAndEverywhere(t *testing.T) {
	s := affinityStore(t)
	affSession(t, s, "s-here", affHere)
	affSession(t, s, "s-other", affOther)
	inHere := affEntry(t, s, "arose here", "s-here")
	inOther := affEntry(t, s, "arose elsewhere", "s-other")
	everywhere := affEntry(t, s, "holds everywhere", "s-other")
	if err := s.SetKnowledgeEverywhere(everywhere, true); err != nil {
		t.Fatal(err)
	}

	got := tied(t, s, affHere)
	if !got[inHere] || !got[everywhere] || got[inOther] {
		t.Fatalf("tied to here = %v, want %d and %d but not %d", got, inHere, everywhere, inOther)
	}
	// The same entry is tied to the other project through its origin, and the
	// flagged one to every project.
	if got := tied(t, s, affOther); !got[inOther] || !got[everywhere] || got[inHere] {
		t.Errorf("tied to other = %v", got)
	}
	if got := tied(t, s, "github.com/dw/third"); len(got) != 1 || !got[everywhere] {
		t.Errorf("tied to a third project = %v, want only the flagged entry", got)
	}
	if err := s.SetKnowledgeEverywhere(everywhere, false); err != nil {
		t.Fatal(err)
	}
	if got := tied(t, s, "github.com/dw/third"); len(got) != 0 {
		t.Errorf("flag taken back, still tied: %v", got)
	}
}

// A project entry is delivered in its project anyway and has no business being
// everywhere.
func TestEverywhereOnlyForBroadEntries(t *testing.T) {
	s := affinityStore(t)
	id, err := s.InsertKnowledge(Knowledge{Type: "pitfall", Title: "t", Body: "b", Scope: scope.Axes{Project: affHere}, Person: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetKnowledgeEverywhere(id, true); err == nil {
		t.Error("a project entry was flagged as applying everywhere")
	}
	if err := s.SetKnowledgeEverywhere(9999, true); err == nil {
		t.Error("an unknown entry was flagged")
	}
}

// Evidence ties an entry to the project of the session that backs it, also when
// the evidence is added after the entry; and a session that is uploaded after
// the entry catches up.
func TestAffinityFromEvidenceAndLateSessions(t *testing.T) {
	s := affinityStore(t)
	sess := affSession(t, s, "s-ev", affHere)
	id := affEntry(t, s, "distilled, no origin", "")
	if tied(t, s, affHere)[id] {
		t.Fatal("tied before any evidence")
	}
	if err := s.AddEvidence(id, []Evidence{{SessionID: sess, ChunkSeq: 1, Quote: "q"}}); err != nil {
		t.Fatal(err)
	}
	if !tied(t, s, affHere)[id] {
		t.Error("evidence from a session of the project did not tie the entry")
	}

	late := affEntry(t, s, "written before its session arrived", "s-late")
	if tied(t, s, affOther)[late] {
		t.Fatal("tied to a session nobody knows yet")
	}
	affSession(t, s, "s-late", affOther)
	if !tied(t, s, affOther)[late] {
		t.Error("the session arrived later and did not catch the entry up")
	}
}

// The table is built from the stock when it is created, so an archive that
// already holds entries has its ties on the first start of the new binary, and
// RebuildProjectAffinity restores them after a wipe.
func TestAffinityRebuildRestoresTiesFromStock(t *testing.T) {
	s := affinityStore(t)
	affSession(t, s, "s-here", affHere)
	id := affEntry(t, s, "arose here", "s-here")
	if _, err := s.db.Exec(`DELETE FROM knowledge_project_affinity`); err != nil {
		t.Fatal(err)
	}
	if tied(t, s, affHere)[id] {
		t.Fatal("test setup: table is not empty")
	}
	n, err := s.RebuildProjectAffinity()
	if err != nil || n == 0 {
		t.Fatalf("rebuild = %d, %v", n, err)
	}
	if !tied(t, s, affHere)[id] {
		t.Error("rebuild did not restore the tie")
	}
	if again, err := s.RebuildProjectAffinity(); err != nil || again != 0 {
		t.Errorf("a second rebuild added %d rows (%v), want none", again, err)
	}
}

// The durable archive: a project entry never gets rows, so the table stays the
// size of the broad knowledge.
func TestAffinityOnlyForBroadEntries(t *testing.T) {
	s := affinityStore(t)
	affSession(t, s, "s-here", affHere)
	if _, err := s.InsertKnowledge(Knowledge{Type: "pitfall", Title: "t", Body: "b", Scope: scope.Axes{Project: affHere}, Person: "p",
		SessionRef: "s-here", OriginContext: &originctx.Context{SessionRef: "s-here"}}); err != nil {
		t.Fatal(err)
	}
	if n := logCount(t, s, `SELECT COUNT(*) FROM knowledge_project_affinity`); n != 0 {
		t.Errorf("a project entry has %d affinity rows", n)
	}
}

// Review list: machine entries with exactly one project of origin, and broad
// entries that have not been picked up for 90 days. An entry that was searched,
// delivered on a word or flagged is left out, and so is one younger than the
// period.
func TestBroadTriage(t *testing.T) {
	s := affinityStore(t)
	affSession(t, s, "s-here", affHere)
	affSession(t, s, "s-other", affOther)
	one := affEntry(t, s, "one project of origin", "s-here")
	two := affEntry(t, s, "two projects of origin", "s-here")
	if _, err := s.db.Exec(`INSERT INTO knowledge_project_affinity VALUES(?,?, 'origin')`, two, affOther); err != nil {
		t.Fatal(err)
	}
	stale := affEntry(t, s, "never picked up", "")
	searched := affEntry(t, s, "searched once", "")
	picked := affEntry(t, s, "picked up by a word", "")
	flagged := affEntry(t, s, "flagged", "")
	young := affEntry(t, s, "too young", "")
	old := time.Now().UTC().AddDate(0, 0, -120).Format(time.RFC3339)
	for _, id := range []int64{one, two, stale, searched, picked, flagged} {
		if _, err := s.db.Exec(`UPDATE knowledge SET created_at=? WHERE id=?`, old, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`UPDATE knowledge SET search_hits=3 WHERE id=?`, searched); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKnowledgeEverywhere(flagged, true); err != nil {
		t.Fatal(err)
	}
	recent := time.Now().UTC().AddDate(0, 0, -5).Format(time.RFC3339)
	if _, err := s.db.Exec(`INSERT INTO context_delivery_log(session_key,project,via,kind,object_id,delivered_at) VALUES('s',?,?,?,?,?)`,
		affHere, deliverylog.ViaRelevance, deliverylog.KindKnowledge, picked, recent); err != nil {
		t.Fatal(err)
	}
	// The log reaches back further than the period, so the list can be judged.
	logSince(t, s, 100)

	items, err := s.BroadTriage("", time.Now(), 50, nil)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[int64]TriageKind{}
	for _, it := range items {
		kinds[it.Knowledge.ID] = it.Kind
		if it.Kind == TriageMove && it.Knowledge.ID == one && it.Project != affHere {
			t.Errorf("move proposal names %q, want %q", it.Project, affHere)
		}
	}
	want := map[int64]TriageKind{one: TriageMove, two: TriageUnused, stale: TriageUnused}
	for id, k := range want {
		if kinds[id] != k {
			t.Errorf("entry %d: %q, want %q (all: %v)", id, kinds[id], k, kinds)
		}
	}
	for _, id := range []int64{searched, picked, flagged, young} {
		if _, in := kinds[id]; in {
			t.Errorf("entry %d is listed but was used, flagged or too young", id)
		}
	}

	// Per project: only what arose there as its single project.
	items, err = s.BroadTriage(affOther, time.Now(), 50, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Kind == TriageMove {
			t.Errorf("entry %d proposed for a move to %s, but arose in one other project", it.Knowledge.ID, affOther)
		}
	}
}

// REQ-641 review, blocker: the collector calls UpsertSession on every upload. The
// triggers on sessions write with INSERT OR IGNORE, but the DO UPDATE branch of
// the upsert runs with ABORT and overrules it, so the second upload of a session
// that already had a row failed with a UNIQUE violation, for good.
func TestUpsertSessionTwiceAfterABroadEntryTiedIt(t *testing.T) {
	s := affinityStore(t)
	affSession(t, s, "s-here", affHere)
	id := affEntry(t, s, "arose here", "s-here")
	if !tied(t, s, affHere)[id] {
		t.Fatal("test setup: the entry has no tie yet")
	}
	// The same upload again, as the collector does it, several times.
	for i := 0; i < 3; i++ {
		if _, err := s.UpsertSession(Session{Harness: "claude", ExternalID: "s-here", Scope: scope.Axes{Project: affHere, Machine: "m1"}}); err != nil {
			t.Fatalf("upload %d of a session with a tie: %v", i+2, err)
		}
	}
	// And a session that moves to another project and back lands on the row it
	// already has.
	for _, p := range []string{affOther, affHere} {
		if _, err := s.UpsertSession(Session{Harness: "claude", ExternalID: "s-here", Scope: scope.Axes{Project: p, Machine: "m1"}}); err != nil {
			t.Fatalf("move to %s: %v", p, err)
		}
	}
	if got := tied(t, s, affHere); !got[id] {
		t.Errorf("the tie to here is gone: %v", got)
	}
	if got := tied(t, s, affOther); !got[id] {
		t.Errorf("the session was in other for a while and tied the entry there: %v", got)
	}
}

// The same cause on evidence. A plain INSERT lets the OR IGNORE of the trigger
// stand (measured), but a statement with its own conflict clause (OR ABORT, OR
// FAIL) overrules it and fails on the second chunk of a session that already
// backs the entry. Plain inserts, the idempotent form of AddEvidence and the
// explicit forms must all work.
func TestEvidenceInsertedRepeatedlyForOneSession(t *testing.T) {
	forms := map[string]string{
		"plain":      `INSERT INTO knowledge_evidence(knowledge_id,session_id,chunk_seq,quote) VALUES(?,?,?,'q')`,
		"or ignore":  `INSERT OR IGNORE INTO knowledge_evidence(knowledge_id,session_id,chunk_seq,quote) VALUES(?,?,?,'q')`,
		"do nothing": `INSERT INTO knowledge_evidence(knowledge_id,session_id,chunk_seq,quote) VALUES(?,?,?,'q') ON CONFLICT DO NOTHING`,
		"or abort":   `INSERT OR ABORT INTO knowledge_evidence(knowledge_id,session_id,chunk_seq,quote) VALUES(?,?,?,'q')`,
		"or fail":    `INSERT OR FAIL INTO knowledge_evidence(knowledge_id,session_id,chunk_seq,quote) VALUES(?,?,?,'q')`,
	}
	for name, q := range forms {
		t.Run(name, func(t *testing.T) {
			s := affinityStore(t)
			sess := affSession(t, s, "s-ev", affHere)
			id := affEntry(t, s, "distilled", "")
			for seq := 1; seq <= 3; seq++ {
				if _, err := s.db.Exec(q, id, sess, seq); err != nil {
					t.Fatalf("chunk %d: %v", seq, err)
				}
			}
			if !tied(t, s, affHere)[id] {
				t.Error("evidence did not tie the entry")
			}
			if n := logCount(t, s, `SELECT COUNT(*) FROM knowledge_project_affinity WHERE knowledge_id=`+strconv.FormatInt(id, 10)); n != 1 {
				t.Errorf("%d affinity rows for the entry, want 1", n)
			}
		})
	}
	// The public path.
	s := affinityStore(t)
	sess := affSession(t, s, "s-ev", affHere)
	id := affEntry(t, s, "distilled", "")
	for i := 0; i < 2; i++ {
		if err := s.AddEvidence(id, []Evidence{{SessionID: sess, ChunkSeq: 1, Quote: "q"}, {SessionID: sess, ChunkSeq: 2, Quote: "q"}}); err != nil {
			t.Fatalf("AddEvidence %d: %v", i, err)
		}
	}
}

// logSince writes a delivery of an unrelated object that many days ago, so the
// log reaches back that far.
func logSince(t *testing.T, s *Store, days int) {
	t.Helper()
	at := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
	if _, err := s.db.Exec(`INSERT INTO context_delivery_log(session_key,project,via,kind,object_id,delivered_at) VALUES('old',?,?,?,?,?)`,
		affHere, deliverylog.ViaBootstrap, deliverylog.KindKnowledge, 999999, at); err != nil {
		t.Fatal(err)
	}
}

func unusedIDs(t *testing.T, s *Store) map[int64]bool {
	t.Helper()
	items, err := s.BroadTriage("", time.Now(), 50, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]bool{}
	for _, it := range items {
		if it.Kind == TriageUnused {
			out[it.Knowledge.ID] = true
		}
	}
	return out
}

// REQ-641 review, major 2: the delivery log exists only for a short time, so an
// entry it has never been able to show is not "unused for 90 days". While the log
// is younger than the period the list is left out; binding instructions are never
// on it.
func TestTriageUnusedWaitsForTheDeliveryLog(t *testing.T) {
	s := affinityStore(t)
	stale := affEntry(t, s, "never picked up", "")
	instr, err := s.InsertKnowledge(Knowledge{Type: "instruction", Title: "binding rule", Body: "b", Scope: scope.Axes{Machine: "m1"}, Person: "p"})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(0, 0, -200).Format(time.RFC3339)
	for _, id := range []int64{stale, instr} {
		if _, err := s.db.Exec(`UPDATE knowledge SET created_at=? WHERE id=?`, old, id); err != nil {
			t.Fatal(err)
		}
	}

	// No log at all, then a log of three days: nothing can be judged.
	if got := unusedIDs(t, s); len(got) != 0 {
		t.Fatalf("empty log: unused list %v, want none", got)
	}
	if d, err := s.TriageLogDays(time.Now()); err != nil || d != 0 {
		t.Fatalf("empty log reaches back %d days (%v), want 0", d, err)
	}
	logSince(t, s, 3)
	if got := unusedIDs(t, s); len(got) != 0 {
		t.Fatalf("log of 3 days: unused list %v, want none", got)
	}
	if d, _ := s.TriageLogDays(time.Now()); d != 3 {
		t.Errorf("log reaches back %d days, want 3", d)
	}

	// A log that covers the period: the entry is listed, the instruction is not.
	logSince(t, s, TriageUnusedDays+1)
	got := unusedIDs(t, s)
	if !got[stale] {
		t.Errorf("entry %d not listed although the log covers %d days: %v", stale, TriageUnusedDays, got)
	}
	if got[instr] {
		t.Errorf("binding instruction %d is on the unused list", instr)
	}
	// The move proposals do not depend on the log.
	if _, err := s.db.Exec(`DELETE FROM context_delivery_log`); err != nil {
		t.Fatal(err)
	}
	affSession(t, s, "s-here", affHere)
	one := affEntry(t, s, "one project of origin", "s-here")
	items, err := s.BroadTriage("", time.Now(), 50, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range items {
		found = found || (it.Kind == TriageMove && it.Knowledge.ID == one)
	}
	if !found {
		t.Errorf("the move proposal for %d vanished with the log", one)
	}
}

// REQ-641 review, minor 3: schema and first build are one transaction. A build
// that breaks leaves no table behind, so the next start builds again instead of
// trusting an empty table.
func TestAffinityFirstBuildIsAtomic(t *testing.T) {
	s := affinityStore(t)
	affSession(t, s, "s-here", affHere)
	id := affEntry(t, s, "arose here", "s-here")
	for _, q := range []string{`DROP TABLE knowledge_project_affinity`, `DROP TABLE knowledge_evidence`} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureProjectAffinity(s.db); err == nil {
		t.Fatal("the build ran although a table it reads is gone")
	}
	if n := logCount(t, s, `SELECT COUNT(*) FROM sqlite_master WHERE name='knowledge_project_affinity'`); n != 0 {
		t.Fatalf("a failed first build left the table behind (%d), the next start would take it for built", n)
	}
	if _, err := s.db.Exec(`CREATE TABLE knowledge_evidence(
		id INTEGER PRIMARY KEY,
		knowledge_id INTEGER NOT NULL REFERENCES knowledge(id),
		session_id INTEGER NOT NULL REFERENCES sessions(id),
		chunk_seq INTEGER NOT NULL,
		quote TEXT NOT NULL DEFAULT '',
		UNIQUE(knowledge_id, session_id, chunk_seq))`); err != nil {
		t.Fatal(err)
	}
	if err := ensureProjectAffinity(s.db); err != nil {
		t.Fatal(err)
	}
	if !tied(t, s, affHere)[id] {
		t.Error("the second start did not build the ties from the stock")
	}
}

// A database that already ran the first version of the triggers (INSERT OR
// IGNORE) gets the fixed ones on the next start.
func TestAffinityTriggersAreReplacedOnStart(t *testing.T) {
	s := affinityStore(t)
	if _, err := s.db.Exec(`DROP TRIGGER knowledge_affinity_session_update;
		CREATE TRIGGER knowledge_affinity_session_update AFTER UPDATE OF project ON sessions WHEN NEW.project != ''
		BEGIN INSERT OR IGNORE INTO knowledge_project_affinity(knowledge_id, project, source) SELECT 1, 'x', 'origin'; END`); err != nil {
		t.Fatal(err)
	}
	if err := ensureProjectAffinity(s.db); err != nil {
		t.Fatal(err)
	}
	var sqlText string
	if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='knowledge_affinity_session_update'`).Scan(&sqlText); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sqlText, "OR IGNORE") || !strings.Contains(sqlText, "ON CONFLICT DO NOTHING") {
		t.Errorf("the old trigger stayed: %s", sqlText)
	}
}
