package store

import (
	"testing"
	"time"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestPendingDevicesListOpenFlowsOnlyAndNeverTheUserCode(t *testing.T) {
	d, clock := newDeviceFixture(t)
	first, _ := d.Start("1.1.1.1", "alpha", "1.1.1.1")
	clock.advance(time.Minute)
	if _, err := d.Start("2.2.2.2", "beta", "2.2.2.2"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.StartJoin("3.3.3.3", "joiner", "3.3.3.3"); err != nil {
		t.Fatal(err)
	}
	got := d.Pending()
	if len(got) != 2 || got[0].Machine != "alpha" || got[1].Machine != "beta" {
		t.Fatalf("pending = %+v, want alpha then beta and no join flow", got)
	}
	if err := d.Decide(first.UserCode, "person:1", false); err != nil {
		t.Fatal(err)
	}
	if got := d.Pending(); len(got) != 1 || got[0].Machine != "beta" {
		t.Fatalf("after deny pending = %+v", got)
	}
	clock.advance(DeviceFlowTTL + time.Minute)
	if got := d.Pending(); len(got) != 0 {
		t.Fatalf("expired flows are listed: %+v", got)
	}
}

func TestCriteriaProgressCountsMetAndWaivedOfAll(t *testing.T) {
	s := openTest(t)
	detail, err := s.CreateRequest(requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: "r", Scope: scope.Axes{Project: "github.com/x/p"}},
		Criteria: []string{"a", "b", "c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "o", Scope: scope.Axes{Project: "github.com/x/hidden"}}, Criteria: []string{"x", "y"}})
	if err := s.SetCriterionState(detail.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "test", Ref: "go test", Person: "a"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.CriteriaProgress([]int64{detail.Request.ID})
	if err != nil {
		t.Fatal(err)
	}
	if p := got[detail.Request.ID]; p.Done != 1 || p.Total != 3 {
		t.Fatalf("progress = %+v, want 1/3", p)
	}
	if _, ok := got[other.Request.ID]; ok {
		t.Fatal("an ID that was not asked for is answered")
	}
}

func TestKnowledgeWindowRestrictsInTheQueryAndKeepsTheLimitForVisibleRows(t *testing.T) {
	s := openTest(t)
	visible, hidden := "github.com/x/visible", "github.com/x/hidden"
	mk := func(title, project, conf string) {
		if _, err := s.InsertKnowledge(Knowledge{Type: "note", Title: title, Body: "b", Scope: scope.Axes{Project: project}, Person: "p", Confidence: conf}); err != nil {
			t.Fatal(err)
		}
	}
	mk("old visible", visible, "trusted")
	for i := 0; i < 5; i++ { // newer hidden rows must not crowd out the visible one
		mk("hidden", hidden, "trusted")
	}
	mk("staged visible", visible, "staged")
	got, err := s.KnowledgeWindow(KnowledgeWindow{Restrict: true, Projects: []string{visible}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "old visible" {
		t.Fatalf("learned = %+v, want only the approved visible entry", got)
	}
	pending, _ := s.KnowledgeWindow(KnowledgeWindow{Pending: true, Restrict: true, Projects: []string{visible}})
	if len(pending) != 1 || pending[0].Title != "staged visible" {
		t.Fatalf("pending = %+v", pending)
	}
	all, _ := s.KnowledgeWindow(KnowledgeWindow{})
	if len(all) != 6 {
		t.Fatalf("unrestricted = %d, want 6 approved", len(all))
	}
	none, _ := s.KnowledgeWindow(KnowledgeWindow{Restrict: true})
	if len(none) != 0 {
		t.Fatalf("restricted to no project = %+v", none)
	}
	future, _ := s.KnowledgeWindow(KnowledgeWindow{Since: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if len(future) != 0 {
		t.Fatal("since is ignored")
	}
}
