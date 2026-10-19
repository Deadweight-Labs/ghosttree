package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/garden"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func pitfall(id int64, regression string) store.Knowledge {
	return store.Knowledge{ID: id, Type: "pitfall", Title: "t", Body: "siehe cmd/x.go", Status: "active", Confidence: "trusted",
		Scope: scope.Axes{Project: gardenProject}, ObservedAt: "2000-01-01T00:00:00Z", RegressionState: regression, UpdatedAt: "2026-10-01T00:00:00Z"}
}

// The regression question is put to a pitfall nobody has judged and that the
// gardener has not already made a proposal about; a decision, a judged pitfall
// and one with a proposal standing (in any state) are not asked.
func TestGardenAsksTheRegressionQuestionOnlyOfOpenPitfalls(t *testing.T) {
	judged := pitfall(2, "covered")
	decision := pitfall(3, "")
	decision.Type = "decision"
	asked := pitfall(4, "")
	rejected := pitfall(5, "")
	changed := pitfall(6, "")
	changed.UpdatedAt = "2026-10-10T00:00:00Z"
	note := func(id int64, status string) store.Knowledge {
		return store.Knowledge{ID: 100 + id, Type: "note", Harness: "gardener", SessionRef: garden.CoverRef(id, garden.CoverSketch), Status: status,
			Confidence: "staged", Scope: scope.Axes{Project: gardenProject}, CreatedAt: "2026-10-05T00:00:00Z"}
	}
	st := &gardenFakeStore{entries: []store.Knowledge{pitfall(1, ""), judged, decision, asked, rejected, changed,
		note(5, "deprecated"), note(6, "deprecated"), note(7, "active")}}
	var out bytes.Buffer
	opts := gardenOpts{Types: map[string]bool{"pitfall": true, "decision": true}}
	entries, _, code, done := gardenDue(st, gardenProject, opts, &out)
	if done || code != 0 {
		t.Fatalf("done=%v code=%d %s", done, code, out.String())
	}
	got := map[int64]bool{}
	for _, e := range entries {
		got[e.ID] = e.AskCover
	}
	// 5 has a rejected proposal for its current wording; 6 changed after the one it had.
	want := map[int64]bool{1: true, 2: false, 3: false, 4: true, 5: false, 6: true}
	for id, ask := range want {
		if got[id] != ask {
			t.Errorf("#%d: AskCover=%v, want %v", id, got[id], ask)
		}
	}
	if len(got) != len(want) {
		t.Errorf("entries = %v", got)
	}
}

// The thread is the way a contradiction is brought to a person. When it cannot be
// opened the finding is not lost: it is filed as a staged note, marked as the
// gardener's, like a duplicate.
func TestGardenFilesAContradictionAsANoteWhenItsThreadCannotBeOpened(t *testing.T) {
	repo := gardenRepo(t)
	st := &gardenFakeStore{threadErr: errors.New("refused"), entries: []store.Knowledge{
		{ID: 1, Type: "pitfall", Title: "eins", Body: "siehe cmd/x.go, Dienst läuft auf server-a", Status: "active", Confidence: "trusted", Scope: scope.Axes{Project: gardenProject}, ObservedAt: "2000-01-01T00:00:00Z"},
		{ID: 2, Type: "decision", Title: "zwei", Body: "siehe cmd/x.go, Dienst läuft auf server-b", Status: "active", Confidence: "trusted", Scope: scope.Axes{Project: gardenProject}, ObservedAt: "2000-01-02T00:00:00Z"},
	}}
	fr := &gardenFakeRunner{text: func(string) string {
		return `{"verdicts":[{"id":1,"verdict":"holds","reason":"r"},{"id":2,"verdict":"holds","reason":"r"}],` +
			`"relations":[{"kind":"contradiction","ids":[1,2],"reason":"Ort","quotes":[{"id":1,"text":"Dienst läuft auf server-a"},{"id":2,"text":"Dienst läuft auf server-b"}]}]}`
	}}
	opts := gardenOpts{Types: map[string]bool{"pitfall": true, "decision": true}, MaxUSD: 1, Propose: true, Sessions: 4, PerSession: 30, Turns: 30, Runner: fr}
	var out bytes.Buffer
	if code := runGardenAgent(st, repo, gardenProject, opts, gardenPrice, "", &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(st.threads) != 0 || len(st.written) != 1 {
		t.Fatalf("%d threads, %d notes written:\n%s", len(st.threads), len(st.written), out.String())
	}
	k := st.written[0]
	if k.Harness != "gardener" || k.Confidence != "staged" || !strings.Contains(k.Title, "Widerspruch?") || k.SessionRef != "gardener:relation:contradiction:1-2" {
		t.Errorf("fallback note = %+v", k)
	}
	if !strings.Contains(out.String(), "filing a staged note instead") {
		t.Errorf("the fallback is not said:\n%s", out.String())
	}
	// And the next run, with the thread working, does not open one on top of a note somebody may have seen.
	st.threadErr = nil
	st.entries = append(st.entries, st.written...)
	st.written = nil
	runGardenAgent(st, repo, gardenProject, opts, gardenPrice, "", &out)
	if len(st.threads) != 0 || len(st.written) != 0 {
		t.Errorf("after the note: %d threads, %d notes", len(st.threads), len(st.written))
	}
}

// Duplicates and answers to the regression question are staged notes; the
// regression answer is validated against the checkout first.
func TestGardenFilesRegressionAnswersAsStagedNotesAfterCheckingThem(t *testing.T) {
	repo := gardenRepo(t)
	st := &gardenFakeStore{entries: []store.Knowledge{pitfall(1, ""), pitfall(2, "")}}
	fr := &gardenFakeRunner{text: func(prompt string) string {
		if !strings.Contains(prompt, "[Regressionsfrage]") {
			t.Errorf("the open pitfalls were not asked:\n%s", prompt)
		}
		return `{"verdicts":[{"id":1,"verdict":"holds"},{"id":2,"verdict":"holds"}],"regression":[` +
			`{"id":1,"kind":"sketch","file":"cmd/x_test.go","test":"TestNewX","steps":"eins\nzwei\ndrei"},` +
			`{"id":2,"kind":"existing","file":"cmd/x_test.go","test":"TestInvented"}]}`
	}}
	opts := gardenOpts{Types: map[string]bool{"pitfall": true}, MaxUSD: 1, Propose: true, Sessions: 4, PerSession: 30, Turns: 30, Runner: fr}
	var out bytes.Buffer
	if code := runGardenAgent(st, repo, gardenProject, opts, gardenPrice, "", &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(st.written) != 1 || st.written[0].SessionRef != "gardener:regression:1:sketch" {
		t.Fatalf("written = %+v", st.written)
	}
	k := st.written[0]
	if k.Harness != "gardener" || k.Confidence != "staged" || k.RegressionState != "" {
		t.Errorf("note = %+v", k)
	}
	if !strings.Contains(out.String(), "1 about regression protection") {
		t.Errorf("summary:\n%s", out.String())
	}
}
