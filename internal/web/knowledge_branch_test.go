package web

import (
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Branch-Namen sind für Gäste verborgen wie auf den Code-, PR- und Commit-Seiten
// (seesAllBranches); der Standard-Branch darf bleiben.
func TestKnowledgePagesHideBranchNamesFromGuests(t *testing.T) {
	e := seedSessions(t)
	feat := knInsert(t, e.St, store.Knowledge{Title: "Feature lesson", Confidence: "trusted",
		GitAnchor: &gitanchor.Anchor{Commit: "abcdef1234567890", Branch: "feat/x"}})
	main := knInsert(t, e.St, store.Knowledge{Title: "Main lesson", Confidence: "trusted",
		GitAnchor: &gitanchor.Anchor{Commit: "1234567abcdef000", Branch: "main"}})
	// Eine überschriebene Fassung trägt ihren Anker in die Historie.
	hist := knInsert(t, e.St, store.Knowledge{Title: "Edited lesson", Confidence: "trusted",
		GitAnchor: &gitanchor.Anchor{Commit: "fedcba9876543210", Branch: "feat/old"}})
	if err := e.St.UpdateKnowledgeBy(hist, map[string]string{"body": "second text"}, "alice"); err != nil {
		t.Fatal(err)
	}
	fid, mid := strconv.FormatInt(feat, 10), strconv.FormatInt(main, 10)

	for _, path := range []string{"/ui/knowledge", "/ui/knowledge/" + fid} {
		if _, page := e.get(t, e.Guest, path); strings.Contains(page, "feat/x") {
			t.Errorf("guest %s leaks the branch name", path)
		}
		if _, page := e.get(t, e.Member, path); !strings.Contains(page, "feat/x") {
			t.Errorf("member %s lacks the branch name", path)
		}
	}
	hpath := "/ui/knowledge/" + strconv.FormatInt(hist, 10)
	if _, page := e.get(t, e.Guest, hpath); strings.Contains(page, "feat/old") {
		t.Error("guest sees the branch of an earlier revision")
	}
	if _, page := e.get(t, e.Guest, "/ui/knowledge/"+mid); !strings.Contains(page, "main") {
		t.Error("the default branch stays visible for a guest")
	}
}

func TestKnowledgeViewDropsBranchForGuests(t *testing.T) {
	e := seedSessions(t)
	id := knInsert(t, e.St, store.Knowledge{Title: "L", Confidence: "trusted",
		GitAnchor: &gitanchor.Anchor{Commit: "abcdef1234567890", Branch: "feat/x"}})
	raw, err := e.St.KnowledgeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	guest := e.St.Access(store.Principal{ID: "person:3", Label: "gina"})
	if got := guest.KnowledgeView(raw); got.GitAnchor == nil || got.GitAnchor.Branch != "" || got.GitAnchor.Commit == "" {
		t.Errorf("guest view anchor = %+v", got.GitAnchor)
	}
	if raw.GitAnchor.Branch != "feat/x" {
		t.Error("the view changed the stored entry")
	}
}
