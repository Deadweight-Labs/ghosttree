package server

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Kriterium anlegen, Relation, Arbeit beginnen und Verwerfen tragen den Anker
// wie die übrigen Schreibpfade (REQ-597, Kriterium 9).
func TestLedgerWritesBeyondTheFirstPassCarryTheGitAnchor(t *testing.T) {
	srv, token := newTestServer(t)
	c := client.New(config.Config{ServerURL: srv.URL, Token: token})
	const commit = "cccccccc3333333333333333333333333333cccc"
	c.SetAnchorSource(func() gitanchor.Anchor { return gitanchor.Anchor{Commit: commit, Branch: "feat/ledger"} })
	ax := scope.Axes{Project: anchorProject}
	mk := func(title string) requestdomain.Detail {
		t.Helper()
		d, err := c.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: title, Scope: ax, Origin: "agent"}, Criteria: []string{"eins"}})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	a, b := mk("erster"), mk("zweiter")
	if _, err := c.AddRequestCriterion(a.Request.ID, "noch ein Kriterium"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddRequestRelation(a.Request.ID, requestdomain.Relation{Kind: "related", OtherRequestID: b.Request.ID}); err != nil {
		t.Fatal(err)
	}
	if err := c.DropRequest(b.Request.ID, "nicht mehr gewollt"); err != nil {
		t.Fatal(err)
	}
	anchored := func(d requestdomain.Detail) map[string]bool {
		out := map[string]bool{}
		for _, act := range d.Activity {
			out[act.Kind] = act.GitAnchor != nil && act.GitAnchor.Commit == commit && act.GitAnchor.Branch == "feat/ledger"
		}
		return out
	}
	da, err := c.GetRequest(a.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := anchored(da)
	for _, kind := range []string{"request.created", "criterion.added", "relation.added"} {
		if !got[kind] {
			t.Errorf("%s has no anchor: %v", kind, got)
		}
	}
	db, _ := c.GetRequest(b.Request.ID)
	if !anchored(db)["request.dropped"] {
		t.Errorf("drop has no anchor: %v", anchored(db))
	}
	// Ohne Quelle (Hooks, Verwaltungsbefehle) geht kein Anker mit, und es gelingt trotzdem.
	plain := client.New(config.Config{ServerURL: srv.URL, Token: token})
	if _, err := plain.AddRequestCriterion(a.Request.ID, "ohne Anker"); err != nil {
		t.Fatal(err)
	}
	da, _ = plain.GetRequest(a.Request.ID)
	last := da.Activity[len(da.Activity)-1]
	if !strings.HasPrefix(last.Kind, "criterion.added") || last.GitAnchor != nil {
		t.Fatalf("an unanchored write: %+v", last)
	}
}
