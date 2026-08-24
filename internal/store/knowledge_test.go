package store

import (
	"fmt"
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
	rule := activation.Rule{Paths: []string{"packages/web/**"}, Tasks: []string{"code", "test"}}
	if err := s.SetActivation(id, rule); err != nil {
		t.Fatal(err)
	}

	k, err := s.KnowledgeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(k.Activation.Paths) != 1 || k.Activation.Paths[0] != "packages/web/**" || len(k.Activation.Tasks) != 2 {
		t.Fatalf("activation did not round-trip: %+v", k.Activation)
	}

	miss, err := s.KnowledgeForActivatedContext(scope.Axes{}, activation.Context{RepoPath: "packages/api", Task: "code"})
	if err != nil {
		t.Fatal(err)
	}
	if len(miss) != 0 {
		t.Fatalf("non-matching context returned %+v", miss)
	}
	hit, err := s.KnowledgeForActivatedContext(scope.Axes{}, activation.Context{RepoPath: "packages/web/src", Task: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hit) != 1 || hit[0].ID != id {
		t.Fatalf("matching context returned %+v", hit)
	}
}

func TestSetActivationRejectsNonInstructionAndReplacesAtomically(t *testing.T) {
	s := openTest(t)
	noteID, _ := s.InsertKnowledge(Knowledge{Type: "note", Title: "note", Body: "b"})
	if err := s.SetActivation(noteID, activation.Rule{Tasks: []string{"code"}}); err == nil {
		t.Fatal("activation on non-instruction must fail")
	}
	id, _ := s.InsertKnowledge(Knowledge{Type: "instruction", Title: "rule", Body: "b"})
	if err := s.SetActivation(id, activation.Rule{Paths: []string{"old/**"}, Tasks: []string{"code"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActivation(id, activation.Rule{Paths: []string{"new/**"}, Tasks: []string{"test"}}); err != nil {
		t.Fatal(err)
	}
	k, _ := s.KnowledgeByID(id)
	if len(k.Activation.Paths) != 1 || k.Activation.Paths[0] != "new/**" || len(k.Activation.Tasks) != 1 || k.Activation.Tasks[0] != "test" {
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
	if _, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "bad", Body: "b", Activation: activation.Rule{Tasks: []string{"code"}}}); err == nil {
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
	mk("other-machine", scope.Axes{Machine: "workstation-b"})
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
	s.InsertKnowledge(Knowledge{Type: "pitfall", Title: "ufw drops LAN", Body: "ssh only via private network", Scope: scope.Axes{Machine: "workstation-b"}})
	s.InsertKnowledge(Knowledge{Type: "note", Title: "unrelated", Body: "nothing"})
	got, err := s.SearchKnowledge("private network", scope.Axes{}, 10)
	if err != nil || len(got) != 1 || got[0].Scope.Machine != "workstation-b" {
		t.Fatalf("got %v err %v", got, err)
	}
	got, _ = s.SearchKnowledge("private network", scope.Axes{Machine: "workstation-a"}, 10)
	if len(got) != 0 {
		t.Errorf("machine filter must exclude workstation-b entry")
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
	_, _ = s.db.Exec(`UPDATE knowledge SET updated_at='2026-01-01T00:00:00Z' WHERE id IN (?,?)`, plan, decision)
	n, err := s.ApplyStaleness(time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC), 90*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("applied=%d err=%v", n, err)
	}
	stale, _ := s.KnowledgeByID(plan)
	durable, _ := s.KnowledgeByID(decision)
	if stale.Status != "stale" || durable.Status != "active" {
		t.Fatalf("plan=%s decision=%s", stale.Status, durable.Status)
	}
	pending, _ := s.PendingKnowledge(10)
	if len(pending) != 1 || pending[0].ID != plan {
		t.Fatalf("stale plan missing from review: %+v", pending)
	}
}
