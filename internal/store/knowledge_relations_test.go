package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

var relActor = RelationActor{Name: "robin", Role: "lead", Via: "cli"}

func relEntry(t *testing.T, s *Store, project, title string) int64 {
	t.Helper()
	id, err := s.InsertKnowledge(Knowledge{Type: "note", Title: title, Body: title + " body", Scope: scope.Axes{Project: project}, Person: "robin"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustRel(t *testing.T, s *Store, from, to int64, kind string) KnowledgeRelation {
	t.Helper()
	r, err := s.AddRelation(RelationInput{FromID: from, ToID: to, Kind: kind, Reason: "because"}, relActor)
	if err != nil {
		t.Fatalf("AddRelation(%d,%d,%s): %v", from, to, kind, err)
	}
	return r
}

func TestRelationKindsAndParentWithSiblings(t *testing.T) {
	s := openTest(t)
	a, b, c := relEntry(t, s, "p", "a"), relEntry(t, s, "p", "b"), relEntry(t, s, "p", "c")
	// Criterion 153: sibling and parenthood at once, a single parent with two children.
	mustRel(t, s, b, a, RelSupersedes)
	mustRel(t, s, c, a, RelSupersedes)
	sib := mustRel(t, s, b, c, RelSibling)
	if sib.State != RelActive || sib.Origin != RelOriginHuman || sib.GroupID == 0 {
		t.Fatalf("sibling = %+v", sib)
	}
	rels, err := s.RelationsOf(a)
	if err != nil || len(rels) != 2 {
		t.Fatalf("relations of a = %+v, %v", rels, err)
	}
	if _, err := s.AddRelation(RelationInput{FromID: a, ToID: b, Kind: "related", Reason: "x"}, relActor); !errors.Is(err, ErrRelationInvalid) {
		t.Errorf("unknown kind: %v", err)
	}
	if _, err := s.AddRelation(RelationInput{FromID: a, ToID: a, Kind: RelSibling}, relActor); !errors.Is(err, ErrRelationInvalid) {
		t.Errorf("self relation: %v", err)
	}
	if _, err := s.AddRelation(RelationInput{FromID: a, ToID: 999, Kind: RelSibling}, relActor); !errors.Is(err, ErrRelationInvalid) {
		t.Errorf("missing end: %v", err)
	}
	if _, err := s.AddRelation(RelationInput{FromID: b, ToID: a, Kind: RelSupersedes}, relActor); !errors.Is(err, ErrRelationInvalid) {
		t.Errorf("supersedes without reason: %v", err)
	}
	if _, err := s.AddRelation(RelationInput{FromID: b, ToID: a, Kind: RelSibling, Origin: RelOriginMigrated}, relActor); !errors.Is(err, ErrRelationInvalid) {
		t.Errorf("migrated origin must be reserved: %v", err)
	}
	if _, err := s.AddRelation(RelationInput{FromID: a, ToID: b, Kind: RelSibling}, RelationActor{}); !errors.Is(err, ErrRelationInvalid) {
		t.Errorf("actor required: %v", err)
	}
}

func TestRelationUniqueAndConflicts(t *testing.T) {
	s := openTest(t)
	a, b := relEntry(t, s, "p", "a"), relEntry(t, s, "p", "b")
	r := mustRel(t, s, a, b, RelSibling)
	// Siblings are undirected: the reversed pair is the same edge.
	if _, err := s.AddRelation(RelationInput{FromID: b, ToID: a, Kind: RelSibling}, relActor); !errors.Is(err, ErrRelationExists) {
		t.Errorf("reversed sibling: %v", err)
	}
	if _, err := s.AddRelation(RelationInput{FromID: a, ToID: b, Kind: RelSupersedes, Reason: "r"}, relActor); !errors.Is(err, ErrRelationConflict) {
		t.Errorf("supersedes over sibling: %v", err)
	}
	// After revoking, the pair is free again, and the old row stays.
	if _, err := s.RevokeRelation(r.ID, relActor, "wrong"); err != nil {
		t.Fatal(err)
	}
	mustRel(t, s, a, b, RelSupersedes)
	if _, err := s.AddRelation(RelationInput{FromID: a, ToID: b, Kind: RelSupersedes, Reason: "again"}, relActor); !errors.Is(err, ErrRelationExists) {
		t.Errorf("duplicate supersedes: %v", err)
	}
	if _, err := s.AddRelation(RelationInput{FromID: a, ToID: b, Kind: RelSibling}, relActor); !errors.Is(err, ErrRelationConflict) {
		t.Errorf("sibling over supersedes: %v", err)
	}
	// The database itself refuses a second live row, not only the Go check.
	if _, err := s.db.Exec(`INSERT INTO knowledge_relations(project,from_id,to_id,kind,state,origin,created_by,created_at) VALUES('p',?,?,'supersedes','active','human','x','t')`, a, b); err == nil {
		t.Error("unique index must reject a second live supersedes row")
	}
	if _, err := s.db.Exec(`INSERT INTO knowledge_relations(project,from_id,to_id,kind,state,origin,created_by,created_at) VALUES('p',?,?,'sibling','active','human','x','t')`, b, a); err == nil {
		t.Error("CHECK must require from_id < to_id for siblings")
	}
	all, _ := s.RelationsOf(a)
	if len(all) != 2 {
		t.Errorf("revoked row must be kept, got %d rows", len(all))
	}
}

func TestSupersedesRejectsCycles(t *testing.T) {
	s := openTest(t)
	a, b, c := relEntry(t, s, "p", "a"), relEntry(t, s, "p", "b"), relEntry(t, s, "p", "c")
	mustRel(t, s, a, b, RelSupersedes) // a supersedes b
	if _, err := s.AddRelation(RelationInput{FromID: b, ToID: a, Kind: RelSupersedes, Reason: "r"}, relActor); !errors.Is(err, ErrRelationCycle) {
		t.Errorf("A supersedes B supersedes A: %v", err)
	}
	mustRel(t, s, b, c, RelSupersedes)
	if _, err := s.AddRelation(RelationInput{FromID: c, ToID: a, Kind: RelSupersedes, Reason: "r"}, relActor); !errors.Is(err, ErrRelationCycle) {
		t.Errorf("three-cycle: %v", err)
	}
	// A diamond is fine: a also supersedes c directly.
	mustRel(t, s, a, c, RelSupersedes)
	// A proposed edge counts for cycle detection too.
	d := relEntry(t, s, "p", "d")
	if _, err := s.AddRelation(RelationInput{FromID: d, ToID: a, Kind: RelSupersedes, Reason: "r", State: RelProposed, Origin: RelOriginDistiller}, relActor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRelation(RelationInput{FromID: c, ToID: d, Kind: RelSupersedes, Reason: "r"}, relActor); !errors.Is(err, ErrRelationCycle) {
		t.Errorf("cycle through a proposed edge: %v", err)
	}
	// A revoked edge no longer blocks.
	rels, _ := s.RelationsOf(a)
	for _, r := range rels {
		if r.FromID == a && r.ToID == b {
			if _, err := s.RevokeRelation(r.ID, relActor, "undo"); err != nil {
				t.Fatal(err)
			}
		}
	}
	mustRel(t, s, b, a, RelSupersedes)
}

func TestRelationRulesAcrossProjectAndStatus(t *testing.T) {
	s := openTest(t)
	a, other := relEntry(t, s, "p", "a"), relEntry(t, s, "q", "other")
	if _, err := s.AddRelation(RelationInput{FromID: a, ToID: other, Kind: RelSibling}, relActor); !errors.Is(err, ErrRelationProject) {
		t.Errorf("cross project: %v", err)
	}
	dead, b := relEntry(t, s, "p", "dead"), relEntry(t, s, "p", "b")
	if err := s.UpdateKnowledge(dead, map[string]string{"status": "archived"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRelation(RelationInput{FromID: dead, ToID: b, Kind: RelSupersedes, Reason: "r"}, relActor); !errors.Is(err, ErrRelationInvalid) {
		t.Errorf("archived entry as parent: %v", err)
	}
	// A dead entry may still be superseded, or be a sibling.
	mustRel(t, s, b, dead, RelSupersedes)
}

func TestRelationEvents(t *testing.T) {
	s := openTest(t)
	a, b, c := relEntry(t, s, "p", "a"), relEntry(t, s, "p", "b"), relEntry(t, s, "p", "c")
	agent := RelationActor{Name: "agent-1", Role: "member", Via: "mcp"}
	r, err := s.AddRelation(RelationInput{FromID: b, ToID: a, Kind: RelSupersedes, Reason: "newer", Origin: RelOriginAgent}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevokeRelation(r.ID, relActor, "wrong parent"); err != nil {
		t.Fatal(err)
	}
	p, err := s.AddRelation(RelationInput{FromID: c, ToID: a, Kind: RelSupersedes, Reason: "r", State: RelProposed, Origin: RelOriginDistiller}, RelationActor{Name: "distiller", Via: "distiller"})
	if err != nil || p.State != RelProposed || p.DecidedBy != "" {
		t.Fatalf("proposed = %+v, %v", p, err)
	}
	if _, err := s.RevokeRelation(r.ID, relActor, "again"); !errors.Is(err, ErrRelationState) {
		t.Errorf("revoking a revoked edge: %v", err)
	}
	dec, err := s.DecideRelation(p.ID, true, relActor, "ok")
	if err != nil || dec.State != RelActive || dec.DecidedBy != "robin" {
		t.Fatalf("approved = %+v, %v", dec, err)
	}
	if _, err := s.DecideRelation(p.ID, false, relActor, ""); !errors.Is(err, ErrRelationState) {
		t.Errorf("deciding twice: %v", err)
	}
	evs, err := s.RelationEvents(r.ID, 0)
	if err != nil || len(evs) != 2 {
		t.Fatalf("events of r = %+v, %v", evs, err)
	}
	if evs[0].Action != "activated" || evs[0].Actor != "agent-1" || evs[0].ActorRole != "member" || evs[0].Via != "mcp" || evs[0].Detail != "newer" || evs[0].CreatedAt == "" {
		t.Errorf("first event = %+v", evs[0])
	}
	if evs[1].Action != "revoked" || evs[1].Actor != "robin" || evs[1].Detail != "wrong parent" {
		t.Errorf("second event = %+v", evs[1])
	}
	pe, _ := s.RelationEvents(p.ID, 0)
	if len(pe) != 2 || pe[0].Action != "proposed" || pe[1].Action != "activated" {
		t.Errorf("events of proposed = %+v", pe)
	}
	// Rejection ends the proposal and frees the slot.
	d := relEntry(t, s, "p", "d")
	q, _ := s.AddRelation(RelationInput{FromID: d, ToID: a, Kind: RelSupersedes, Reason: "r", State: RelProposed}, agent)
	rej, err := s.DecideRelation(q.ID, false, relActor, "no")
	if err != nil || rej.State != RelRejected {
		t.Fatalf("rejected = %+v, %v", rej, err)
	}
	mustRel(t, s, d, a, RelSupersedes)
	if _, err := s.RelationByID(9999); !errors.Is(err, ErrRelationNotFound) {
		t.Errorf("missing relation: %v", err)
	}
}

func TestGroupMembershipMergeAndSplit(t *testing.T) {
	s := openTest(t)
	ids := make([]int64, 6)
	for i := range ids {
		ids[i] = relEntry(t, s, "p", string(rune('a'+i)))
	}
	a, b, c, d, e, f := ids[0], ids[1], ids[2], ids[3], ids[4], ids[5]
	ab := mustRel(t, s, a, b, RelSibling)
	bc := mustRel(t, s, c, b, RelSibling) // reversed input is normalised
	if bc.FromID != b || bc.ToID != c {
		t.Fatalf("sibling not normalised: %+v", bc)
	}
	g, ok, err := s.GroupOf(c)
	if err != nil || !ok || !reflect.DeepEqual(g.Members, []int64{a, b, c}) || g.Volatility != "unrated" {
		t.Fatalf("group = %+v ok=%v err=%v", g, ok, err)
	}
	if _, ok, _ := s.GroupOf(d); ok {
		t.Error("ungrouped entry reports a group")
	}
	// Two groups with different volatility merge; the more volatile wins.
	mustRel(t, s, d, e, RelSibling)
	g1, _, _ := s.GroupOf(a)
	g2, _, _ := s.GroupOf(d)
	if _, err := s.SetGroupVolatility(g1.ID, "timeless", relActor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetGroupVolatility(g2.ID, "volatile", relActor); err != nil {
		t.Fatal(err)
	}
	cd := mustRel(t, s, c, d, RelSibling)
	m, _, _ := s.GroupOf(e)
	if !reflect.DeepEqual(m.Members, []int64{a, b, c, d, e}) || m.Volatility != "volatile" {
		t.Fatalf("merged = %+v", m)
	}
	if m.ID != g1.ID {
		t.Errorf("lower id must survive, got %d want %d", m.ID, g1.ID)
	}
	if gone, err := s.GroupByID(g2.ID); err != nil || gone.State != "merged" || gone.MergedInto != g1.ID || len(gone.Members) != 0 {
		t.Errorf("merged-away group must stay as a pointer: %+v %v", gone, err)
	}
	if _, err := s.SetGroupVolatility(g2.ID, "slow", relActor); !errors.Is(err, ErrRelationState) {
		t.Errorf("volatility on a merged group: %v", err)
	}
	// The survivor's history includes the merged group's events (its two
	// volatility_set events), and the project is on every event.
	hist, _ := s.RelationEvents(0, g1.ID)
	vols := 0
	for _, e := range hist {
		if e.Action == "volatility_set" {
			vols++
		}
		if e.Project != "p" {
			t.Errorf("event without project: %+v", e)
		}
	}
	if vols != 2 {
		t.Errorf("history of survivor has %d volatility events, want 2 (own and merged-in)", vols)
	}
	evs, _ := s.RelationEvents(0, m.ID)
	var merged *KnowledgeRelationEvent
	for i := range evs {
		if evs[i].Action == "group_merged" {
			merged = &evs[i]
		}
	}
	if merged == nil || merged.Detail == "" {
		t.Fatalf("no group_merged event in %+v", evs)
	}
	// Merging equal-rated sides: unrated yields to a rated value.
	mustRel(t, s, e, f, RelSibling)
	// Revoking the bridge splits the group again, both halves keep volatility.
	if _, err := s.RevokeRelation(cd.ID, relActor, "not related"); err != nil {
		t.Fatal(err)
	}
	left, _, _ := s.GroupOf(a)
	right, _, _ := s.GroupOf(f)
	if left.ID == right.ID {
		t.Fatal("group did not split")
	}
	if !reflect.DeepEqual(left.Members, []int64{a, b, c}) || !reflect.DeepEqual(right.Members, []int64{d, e, f}) {
		t.Errorf("split halves = %v / %v", left.Members, right.Members)
	}
	if left.Volatility != "volatile" || right.Volatility != "volatile" || left.ID != m.ID {
		t.Errorf("volatility not carried: %+v %+v", left, right)
	}
	// Revoking a non-bridge leaves the group in one piece; the last edge dissolves it.
	if _, err := s.RevokeRelation(ab.ID, relActor, "x"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GroupOf(a); ok {
		t.Error("a is left without a sibling, it must not be grouped")
	}
	if g, ok, _ := s.GroupOf(b); !ok || !reflect.DeepEqual(g.Members, []int64{b, c}) {
		t.Errorf("b/c group = %+v", g)
	}
	// An unactivated (proposed) sibling does not group anything.
	x, y := relEntry(t, s, "p", "x"), relEntry(t, s, "p", "y")
	pr, err := s.AddRelation(RelationInput{FromID: x, ToID: y, Kind: RelSibling, State: RelProposed, Origin: RelOriginDistiller}, relActor)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GroupOf(x); ok {
		t.Error("a proposed sibling must not form a group")
	}
	if _, err := s.DecideRelation(pr.ID, true, relActor, ""); err != nil {
		t.Fatal(err)
	}
	if g, ok, _ := s.GroupOf(y); !ok || len(g.Members) != 2 {
		t.Errorf("approved sibling group = %+v", g)
	}
}

func TestGroupVolatilityIsLogged(t *testing.T) {
	s := openTest(t)
	a, b := relEntry(t, s, "p", "a"), relEntry(t, s, "p", "b")
	mustRel(t, s, a, b, RelSibling)
	g, _, _ := s.GroupOf(a)
	member := RelationActor{Name: "anna", Role: "member", Via: "web"}
	got, err := s.SetGroupVolatility(g.ID, "slow", member)
	if err != nil || got.Volatility != "slow" || got.VolatilityBy != "anna" || got.VolatilityAt == "" {
		t.Fatalf("volatility = %+v, %v", got, err)
	}
	if _, err := s.SetGroupVolatility(g.ID, "volatile", relActor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetGroupVolatility(g.ID, "bogus", relActor); !errors.Is(err, ErrRelationInvalid) {
		t.Errorf("bad value: %v", err)
	}
	if _, err := s.SetGroupVolatility(404, "slow", relActor); !errors.Is(err, ErrRelationNotFound) {
		t.Errorf("missing group: %v", err)
	}
	evs, _ := s.RelationEvents(0, g.ID)
	var vol []KnowledgeRelationEvent
	for _, e := range evs {
		if e.Action == "volatility_set" {
			vol = append(vol, e)
		}
	}
	if len(vol) != 2 || vol[0].Actor != "anna" || vol[0].Detail != "unrated -> slow" || vol[1].Detail != "slow -> volatile" || vol[0].CreatedAt == "" {
		t.Errorf("volatility events = %+v", vol)
	}
}

func TestRelationsThroughWriterQueue(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.writer == nil {
		t.Skip("file store has no writer queue")
	}
	a, b := relEntry(t, s, "p", "a"), relEntry(t, s, "p", "b")
	r := mustRel(t, s, a, b, RelSibling)
	g, ok, err := s.GroupOf(a)
	if err != nil || !ok {
		t.Fatalf("group via reader: %v %v", ok, err)
	}
	if _, err := s.SetGroupVolatility(g.ID, "slow", relActor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevokeRelation(r.ID, relActor, "x"); err != nil {
		t.Fatal(err)
	}
	evs, err := s.RelationEvents(r.ID, 0)
	if err != nil || len(evs) != 2 {
		t.Errorf("events via queue = %+v, %v", evs, err)
	}
}

// legacyRelationsDB builds a database as it was before REQ-157: current
// schema minus the relation tables, with superseded_by chains as the old
// UpdateKnowledgeBy left them (every predecessor points at the chain end).
func legacyRelationsDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"v1", "v2", "v3", "solo", "other-project", "orphan"} {
		project := "p"
		if title == "other-project" {
			project = "q"
		}
		relEntry(t, s, project, title)
	}
	s.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`DROP TABLE knowledge_relation_events`, `DROP TABLE knowledge_relations`, `DROP TABLE knowledge_groups`,
		// 1 and 2 were superseded by 3 (chain moved to its end); 5 points at a
		// parent in another project; 6 points at an entry that no longer exists.
		`UPDATE knowledge SET status='superseded',superseded_by=3,last_modified_by='anna' WHERE id IN (1,2)`,
		`UPDATE knowledge SET status='superseded',superseded_by=1 WHERE id=5`,
		`PRAGMA foreign_keys=OFF`,
		`UPDATE knowledge SET status='superseded',superseded_by=77 WHERE id=6`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return path
}

func TestBackfillMigratesLegacySupersededBy(t *testing.T) {
	path := legacyRelationsDB(t)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rels, err := s.RelationsOf(3)
	if err != nil || len(rels) != 2 {
		t.Fatalf("relations of 3 = %+v, %v", rels, err)
	}
	for _, r := range rels {
		if r.FromID != 3 || r.Kind != RelSupersedes || r.State != RelActive || r.Origin != RelOriginMigrated || r.Project != "p" || r.CreatedBy != "anna" && r.CreatedBy != "robin" {
			t.Errorf("migrated edge = %+v", r)
		}
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM knowledge_relations`).Scan(&n)
	if n != 2 {
		t.Errorf("edges = %d, want 2 (cross-project and dangling parents are skipped)", n)
	}
	var events int
	s.db.QueryRow(`SELECT COUNT(*) FROM knowledge_relation_events WHERE action='activated' AND via='migration' AND actor='migration'`).Scan(&events)
	if events != 2 {
		t.Errorf("migration events = %d, want 2", events)
	}
	// Delivery still runs on the old fields: backfill changes neither.
	for _, id := range []int64{1, 2} {
		k, err := s.KnowledgeByID(id)
		if err != nil || k.Status != "superseded" || k.SupersededBy != 3 {
			t.Errorf("entry %d changed by backfill: %+v %v", id, k, err)
		}
	}
	for _, id := range []int64{3, 4} {
		if k, _ := s.KnowledgeByID(id); k.Status != "active" {
			t.Errorf("entry %d status = %s", id, k.Status)
		}
	}
	rep, err := BackfillKnowledgeRelations(s.db)
	if err != nil || rep.Created != 0 || rep.SkippedUnusable != 2 || rep.SkippedCycle != 0 {
		t.Errorf("rerun = %+v, %v (the two unusable rows are reported again, nothing else)", rep, err)
	}
}

func TestBackfillIsIdempotentAndKeepsRevokes(t *testing.T) {
	path := legacyRelationsDB(t)
	for i := 0; i < 3; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var n, ev int
		s.db.QueryRow(`SELECT COUNT(*) FROM knowledge_relations`).Scan(&n)
		s.db.QueryRow(`SELECT COUNT(*) FROM knowledge_relation_events`).Scan(&ev)
		if n != 2 || ev != 2 {
			t.Fatalf("open %d: edges=%d events=%d, want 2/2", i, n, ev)
		}
		if i == 0 {
			rels, _ := s.RelationsOf(1)
			if _, err := s.RevokeRelation(rels[0].ID, relActor, "legacy value was wrong"); err != nil {
				t.Fatal(err)
			}
			ev = 3 // the revoke event; nothing else may be added by later opens
		}
		if i == 1 {
			var state string
			s.db.QueryRow(`SELECT state FROM knowledge_relations WHERE to_id=1`).Scan(&state)
			if state != RelRevoked {
				t.Fatalf("a revoked migrated edge was resurrected: %s", state)
			}
		}
		s.Close()
		if i == 0 {
			// from here on the revoke event exists
			db, _ := sql.Open("sqlite", path)
			var total int
			db.QueryRow(`SELECT COUNT(*) FROM knowledge_relation_events`).Scan(&total)
			db.Close()
			if total != 3 {
				t.Fatalf("events after revoke = %d", total)
			}
			break
		}
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n, ev int
	s.db.QueryRow(`SELECT COUNT(*) FROM knowledge_relations`).Scan(&n)
	s.db.QueryRow(`SELECT COUNT(*) FROM knowledge_relation_events`).Scan(&ev)
	if n != 2 || ev != 3 {
		t.Errorf("after reopen: edges=%d events=%d, want 2/3", n, ev)
	}
}

func TestBackfillPicksUpNewLegacyWritesOnce(t *testing.T) {
	s := openTest(t)
	a, b := relEntry(t, s, "p", "a"), relEntry(t, s, "p", "b")
	// The old patch path is still live until WP2: it writes the column only.
	if err := s.UpdateKnowledge(a, map[string]string{"superseded_by": itoa(b)}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.RelationsOf(a); len(n) != 0 {
		t.Fatalf("the legacy patch must not write relations yet: %+v", n)
	}
	for i := 0; i < 2; i++ {
		if _, err := BackfillKnowledgeRelations(s.db); err != nil {
			t.Fatal(err)
		}
	}
	rels, _ := s.RelationsOf(a)
	if len(rels) != 1 || rels[0].FromID != b || rels[0].ToID != a {
		t.Errorf("relations = %+v", rels)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func groupCount(t *testing.T, s *Store, id int64) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT group_id) FROM knowledge_relations WHERE kind='sibling' AND state='active' AND (from_id=? OR to_id=?)`, id, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestApprovingSiblingKeepsEntryInOneGroup(t *testing.T) {
	s := openTest(t)
	a, b, c := relEntry(t, s, "p", "a"), relEntry(t, s, "p", "b"), relEntry(t, s, "p", "c")
	pr, err := s.AddRelation(RelationInput{FromID: a, ToID: b, Kind: RelSibling, State: RelProposed, Origin: RelOriginDistiller}, relActor)
	if err != nil {
		t.Fatal(err)
	}
	mustRel(t, s, a, c, RelSibling)
	if _, err := s.DecideRelation(pr.ID, true, relActor, ""); err != nil {
		t.Fatal(err)
	}
	if n := groupCount(t, s, a); n != 1 {
		t.Fatalf("a is in %d groups, want 1", n)
	}
	g, ok, _ := s.GroupOf(a)
	if !ok || !reflect.DeepEqual(g.Members, []int64{a, b, c}) {
		t.Errorf("group = %+v", g)
	}
}

func TestApprovalRechecksParentStatus(t *testing.T) {
	s := openTest(t)
	a, b := relEntry(t, s, "p", "a"), relEntry(t, s, "p", "b")
	pr, _ := s.AddRelation(RelationInput{FromID: a, ToID: b, Kind: RelSupersedes, Reason: "r", State: RelProposed}, relActor)
	if err := s.UpdateKnowledge(a, map[string]string{"status": "deprecated"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideRelation(pr.ID, true, relActor, ""); !errors.Is(err, ErrRelationInvalid) {
		t.Errorf("approving with a deprecated parent: %v", err)
	}
	if r, _ := s.RelationByID(pr.ID); r.State != RelProposed {
		t.Errorf("a failed approval must leave the proposal alone: %+v", r)
	}
}

func TestBackfillSkipsLegacyCycles(t *testing.T) {
	path := legacyRelationsDB(t)
	db, _ := sql.Open("sqlite", path)
	// 1<->2 is a 2-cycle; 3->4->5->3 is a 3-cycle. 6 is superseded without a parent.
	for _, stmt := range []string{
		`UPDATE knowledge SET status='active',superseded_by=0`,
		`UPDATE knowledge SET status='superseded',superseded_by=2 WHERE id=1`,
		`UPDATE knowledge SET status='superseded',superseded_by=1 WHERE id=2`,
		`UPDATE knowledge SET status='superseded',superseded_by=4 WHERE id=3`,
		`UPDATE knowledge SET status='superseded',superseded_by=5,project='p' WHERE id=4`,
		`UPDATE knowledge SET status='superseded',superseded_by=3,project='p' WHERE id=5`,
		`UPDATE knowledge SET status='superseded' WHERE id=6`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM knowledge_relations`).Scan(&n)
	if n != 3 {
		t.Fatalf("edges = %d, want 3 (one per cycle member pair minus the closing edge: 1 + 2)", n)
	}
	// No entry may be hidden by every one of its relations: some member of
	// each cycle has no incoming live edge.
	var heads int
	s.db.QueryRow(`SELECT COUNT(*) FROM knowledge k WHERE k.id IN (1,2,3,4,5) AND NOT EXISTS(SELECT 1 FROM knowledge_relations r WHERE r.to_id=k.id AND r.state='active')`).Scan(&heads)
	if heads != 2 {
		t.Errorf("unsuperseded heads = %d, want one per cycle", heads)
	}
	rep, err := BackfillKnowledgeRelations(s.db)
	if err != nil || rep.Created != 0 || rep.SkippedCycle != 2 || rep.SupersededWithoutParent != 1 {
		t.Errorf("rerun = %+v, %v", rep, err)
	}
}

func TestRelationEventsAreAppendOnly(t *testing.T) {
	s := openTest(t)
	a, b := relEntry(t, s, "p", "a"), relEntry(t, s, "p", "b")
	r := mustRel(t, s, a, b, RelSibling)
	if _, err := s.db.Exec(`UPDATE knowledge_relation_events SET detail='x' WHERE relation_id=?`, r.ID); err == nil {
		t.Error("update of an event must abort")
	}
	if _, err := s.db.Exec(`DELETE FROM knowledge_relation_events`); err == nil {
		t.Error("delete of an event must abort")
	}
}

func TestGroupHistorySurvivesMergeAndDissolve(t *testing.T) {
	s := openTest(t)
	a, b, c, d := relEntry(t, s, "p", "a"), relEntry(t, s, "p", "b"), relEntry(t, s, "p", "c"), relEntry(t, s, "p", "d")
	ab := mustRel(t, s, a, b, RelSibling)
	cd := mustRel(t, s, c, d, RelSibling)
	g1, _, _ := s.GroupOf(a)
	g2, _, _ := s.GroupOf(c)
	s.SetGroupVolatility(g2.ID, "slow", relActor)
	bc := mustRel(t, s, b, c, RelSibling) // merge g2 into g1
	s.RevokeRelation(cd.ID, relActor, "x")
	s.RevokeRelation(ab.ID, relActor, "x")
	s.RevokeRelation(bc.ID, relActor, "x") // last edge: the group dissolves
	// Groups are never reused or deleted: a new group gets a fresh id.
	e, f := relEntry(t, s, "p", "e"), relEntry(t, s, "p", "f")
	mustRel(t, s, e, f, RelSibling)
	ng, _, _ := s.GroupOf(e)
	var rows int
	s.db.QueryRow(`SELECT COUNT(*) FROM knowledge_groups`).Scan(&rows)
	if ng.ID == g1.ID || ng.ID == g2.ID || rows < 3 {
		t.Errorf("group ids reused or rows deleted: new=%d old=%d,%d rows=%d", ng.ID, g1.ID, g2.ID, rows)
	}
	h, _ := s.RelationEvents(0, g1.ID)
	seen := map[string]int{}
	for _, ev := range h {
		seen[ev.Action]++
	}
	if seen["volatility_set"] != 1 || seen["group_merged"] < 1 || seen["group_split"] < 1 {
		t.Errorf("history of g1 = %v", seen)
	}
	for _, ev := range h {
		if ng.ID != 0 && ev.GroupID == ng.ID {
			t.Errorf("a new group's events leaked into an old history: %+v", ev)
		}
	}
	// The group that lost all edges is dissolved and keeps its volatility.
	if d1, _ := s.GroupByID(g1.ID); d1.State != "dissolved" || d1.Volatility != "slow" {
		t.Errorf("dissolved group = %+v", d1)
	}
}

func TestRelationRecordsActorAccount(t *testing.T) {
	s := openTest(t)
	a, b := relEntry(t, s, "p", "a"), relEntry(t, s, "p", "b")
	who := RelationActor{Name: "anna", AccountID: 42, Role: "member", Via: "web"}
	r, err := s.AddRelation(RelationInput{FromID: b, ToID: a, Kind: RelSupersedes, Reason: "r"}, who)
	if err != nil || r.CreatedByAccount != 42 || r.DecidedByAccount != 42 {
		t.Fatalf("relation = %+v, %v", r, err)
	}
	rv, _ := s.RevokeRelation(r.ID, RelationActor{Name: "bob", AccountID: 7}, "no")
	if rv.DecidedByAccount != 7 || rv.CreatedByAccount != 42 {
		t.Errorf("revoked = %+v", rv)
	}
	evs, _ := s.RelationEvents(r.ID, 0)
	if len(evs) != 2 || evs[0].ActorAccountID != 42 || evs[1].ActorAccountID != 7 {
		t.Errorf("events = %+v", evs)
	}
}

func TestRelationQueriesUseIndexes(t *testing.T) {
	s := openTest(t)
	for _, q := range []string{
		`SELECT id FROM knowledge_relations WHERE from_id=1 UNION SELECT id FROM knowledge_relations WHERE to_id=1`,
		`SELECT group_id FROM knowledge_relations WHERE from_id=1 AND kind='sibling' AND state='active' AND id<>0 AND group_id IS NOT NULL UNION SELECT group_id FROM knowledge_relations WHERE to_id=1 AND kind='sibling' AND state='active' AND id<>0 AND group_id IS NOT NULL`,
		`SELECT id FROM knowledge_relations WHERE group_id=1`,
		`SELECT id FROM knowledge_relation_events WHERE relation_id=1`,
	} {
		rows, err := s.db.Query(`EXPLAIN QUERY PLAN ` + q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			rows.Scan(&id, &parent, &unused, &detail)
			if strings.HasPrefix(detail, "SCAN knowledge_relation") {
				t.Errorf("full scan in %q: %s", q, detail)
			}
		}
		rows.Close()
	}
}

func TestSplitGroupHasOwnSplitEvent(t *testing.T) {
	s := openTest(t)
	ids := make([]int64, 4)
	for i := range ids {
		ids[i] = relEntry(t, s, "p", string(rune('a'+i)))
	}
	mustRel(t, s, ids[0], ids[1], RelSibling)
	bridge := mustRel(t, s, ids[1], ids[2], RelSibling)
	mustRel(t, s, ids[2], ids[3], RelSibling)
	kept, _, _ := s.GroupOf(ids[0])
	if _, err := s.RevokeRelation(bridge.ID, relActor, "x"); err != nil {
		t.Fatal(err)
	}
	ng, ok, _ := s.GroupOf(ids[3])
	if !ok || ng.ID == kept.ID {
		t.Fatalf("no new group: %+v", ng)
	}
	evs, err := s.RelationEvents(0, ng.ID)
	if err != nil || len(evs) != 1 || evs[0].Action != "group_split" || evs[0].GroupID != ng.ID ||
		!strings.Contains(evs[0].Detail, "split_from=#"+strconv.FormatInt(kept.ID, 10)) {
		t.Errorf("events of the new group = %+v, %v", evs, err)
	}
}
