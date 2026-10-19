package mirror

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func findDoc(t *testing.T, docs []Doc, path string) Doc {
	t.Helper()
	for _, d := range docs {
		if d.Path == path {
			return d
		}
	}
	t.Fatalf("no doc at %s (have %d)", path, len(docs))
	return Doc{}
}

// Spec §11: der Spiegel zeigt Threads, und was er zeigt, darf nicht mehr
// behaupten als es weiß. Ruhend ist nicht gelöst, und eine Karte ohne
// Quellenstand liest sich wie der heutige Stand.
func TestThreadMirrorDoesNotOverstate(t *testing.T) {
	docs := Build(Input{Project: "p", Threads: []ThreadView{
		{Thread: store.Thread{ID: 1, Project: "p", Title: "Gilt Pitfall 846 noch?",
			Question: "Nach dem Refactoring", State: store.ThreadOpen, Dormant: true,
			UpdatedAt: "2026-08-01T00:00:00Z"}},
		{Thread: store.Thread{ID: 2, Project: "p", Title: "API-Vertrag",
			State: store.ThreadOpen, UpdatedAt: "2026-09-14T00:00:00Z"},
			HasSummary: true, NewPosts: 3,
			Summary: store.ThreadSummary{Revision: 2, CoversThrough: 40,
				Body: "Stand: Variante B", OpenQuestions: "Migrationspfad"},
			Links: []store.ThreadLink{
				{Kind: "knowledge", ID: "846"},
				{Kind: "document", ID: "360", Revision: "1"},
			},
			Outcomes: []store.ThreadOutcome{
				{Kind: "decision", RefID: "neu-1", State: store.OutcomeProposed, Note: "TTL serverseitig"},
			}},
		{Thread: store.Thread{ID: 3, Project: "p", Title: "Beantwortet",
			State: store.ThreadResolved, UpdatedAt: "2026-09-10T00:00:00Z"}},
	}})

	idx := findDoc(t, docs, "threads/INDEX.md").Body
	if !strings.Contains(idx, "Dormant is not resolved") {
		t.Errorf("the index must say dormant is not resolved: %s", idx)
	}
	if !strings.Contains(idx, "not the same as work being done") {
		t.Errorf("a settled discussion must not read like finished work: %s", idx)
	}

	body := findDoc(t, docs, "threads/THR-2-api-vertrag.md").Body
	if !strings.Contains(body, "covers through post 40") {
		t.Errorf("the summary must name its reach: %s", body)
	}
	if !strings.Contains(body, "3 posts since") {
		t.Errorf("posts past the summary must be counted: %s", body)
	}
	if !strings.Contains(body, "current head — not a record of what it said then") {
		t.Errorf("a link to a mutable head must say so: %s", body)
	}
	if !strings.Contains(body, "proposal is not an accepted rule") {
		t.Errorf("a proposal must not read like a rule: %s", body)
	}

	// Ein Thema ohne Karte sagt das, statt einen leeren Stand zu zeigen.
	dormant := findDoc(t, docs, "threads/THR-1-gilt-pitfall-846-noch.md").Body
	if !strings.Contains(dormant, "No summary yet") {
		t.Errorf("a thread without a summary must say so: %s", dormant)
	}
	if !strings.Contains(dormant, "dormant — still open, just quiet") {
		t.Errorf("dormant must not read as closed: %s", dormant)
	}

	if !strings.Contains(findDoc(t, docs, "INDEX.md").Body, "`threads/`") {
		t.Error("the index must point at the discussions")
	}
}

// Keine Themen, keine Dateien: ein leeres Verzeichnis mit einem leeren Index
// sieht aus wie ein kaputter Spiegel.
func TestNoThreadsWritesNoThreadFiles(t *testing.T) {
	for _, d := range Build(Input{Project: "p"}) {
		if strings.HasPrefix(d.Path, "threads/") {
			t.Fatalf("an empty thread list wrote %s", d.Path)
		}
	}
}
