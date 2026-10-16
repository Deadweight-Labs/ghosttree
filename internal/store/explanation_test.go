package store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const note = "Das ist die Einordnung in Alltagssprache. Sie sagt, worum es geht und was zu tun ist. Empfehlung: freigeben."

func TestExplanationTablesAreAdditiveAndOpeningTwiceKeepsThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "e.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := st.InsertKnowledge(Knowledge{Type: "note", Title: "T", Body: "B", Scope: scope.Axes{Project: roleProject}})
	if err := st.SaveAgentExplanation(explain.KindKnowledge, id, note); err != nil {
		t.Fatal(err)
	}
	st.Close()
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	e, ok, err := again.Explanation(explain.KindKnowledge, id)
	if err != nil || !ok || e.Text != note || e.Source != explain.SourceAgent {
		t.Fatalf("%+v %v %v", e, ok, err)
	}
}

func TestSaveExplanationReplacesAndRefusesBadInput(t *testing.T) {
	st, _ := Open(":memory:")
	defer st.Close()
	e := explain.Explanation{Kind: explain.KindRequest, ObjectID: 5, Text: "Erste Fassung der Einordnung.", Source: explain.SourceAgent, ContentHash: "h1"}
	if err := st.SaveExplanation(e); err != nil {
		t.Fatal(err)
	}
	e.Text, e.Source, e.Model, e.Language, e.ContentHash = "Zweite Fassung der Einordnung.", explain.SourceGenerated, "m", "de", "h2"
	if err := st.SaveExplanation(e); err != nil {
		t.Fatal(err)
	}
	got, _, _ := st.Explanation(explain.KindRequest, 5)
	if got.Text != e.Text || got.Source != explain.SourceGenerated || got.Model != "m" || got.Language != "de" || got.ContentHash != "h2" || got.GeneratedAt == "" {
		t.Errorf("%+v", got)
	}
	for name, bad := range map[string]explain.Explanation{
		"kind":   {Kind: "nope", ObjectID: 1, Text: "x", Source: "agent", ContentHash: "h"},
		"source": {Kind: explain.KindPain, ObjectID: 1, Text: "x", Source: "robot", ContentHash: "h"},
		"text":   {Kind: explain.KindPain, ObjectID: 1, Text: "  ", Source: "agent", ContentHash: "h"},
		"hash":   {Kind: explain.KindPain, ObjectID: 1, Text: "x", Source: "agent"},
	} {
		if err := st.SaveExplanation(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestExplanationWorksThroughTheRuntimeWriter(t *testing.T) {
	st, err := OpenRuntime(filepath.Join(t.TempDir(), "rt.db"), DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "T", Body: "B", Scope: scope.Axes{Project: roleProject}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAgentExplanation(explain.KindKnowledge, id, note); err != nil {
		t.Fatal(err)
	}
	if err := st.AddExplainUsage("2026-10-16", 1, 0, 100, 40); err != nil {
		t.Fatal(err)
	}
	e, ok, _ := st.Explanation(explain.KindKnowledge, id)
	u, _ := st.ExplainUsageOn("2026-10-16")
	if !ok || e.Text != note || u.Calls != 1 || u.Tokens() != 140 {
		t.Errorf("%+v %v %+v", e, ok, u)
	}
}

func TestEditingAnObjectChangesItsHashAndOnlyTheTextDoes(t *testing.T) {
	st, _ := Open(":memory:")
	defer st.Close()
	id, _ := st.InsertKnowledge(Knowledge{Type: "pitfall", Title: "Titel", Body: "Text", Scope: scope.Axes{Project: roleProject}})
	before, _ := st.ExplainSource(explain.KindKnowledge, id)
	if err := st.SetRegressionCover(id, "not_applicable", ""); err != nil {
		t.Fatal(err)
	}
	same, _ := st.ExplainSource(explain.KindKnowledge, id)
	if same.Hash != before.Hash {
		t.Error("a regression judgement is not part of the text but changed the hash")
	}
	if err := st.UpdateKnowledge(id, map[string]string{"body": "Anderer Text"}); err != nil {
		t.Fatal(err)
	}
	after, _ := st.ExplainSource(explain.KindKnowledge, id)
	if after.Hash == before.Hash || after.Body != "Anderer Text" {
		t.Errorf("edit not noticed: %+v", after)
	}
}

func TestKnowledgeSourceUsesOnlyTheProjectsOwnRequestsAndNamesCodePaths(t *testing.T) {
	st, _ := Open(":memory:")
	defer st.Close()
	own, _ := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Eigener Auftrag", Scope: scope.Axes{Project: roleProject}}})
	foreign, _ := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "GEHEIMER FREMDER TITEL", Scope: scope.Axes{Project: "github.com/other/x"}}})
	body := "Siehe " + own.Request.HumanID() + " und " + foreign.Request.HumanID() + ", Code in internal/web/knowledge.go (PR #12)."
	id, _ := st.InsertKnowledge(Knowledge{Type: "pitfall", Title: "T", Body: body, Scope: scope.Axes{Project: roleProject}})
	src, err := st.ExplainSource(explain.KindKnowledge, id)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(src.Context, "\n")
	if !strings.Contains(joined, "Eigener Auftrag") || !strings.Contains(joined, "Code: internal/web/knowledge.go") || !strings.Contains(joined, "Pull request #12") {
		t.Errorf("context: %q", joined)
	}
	if strings.Contains(joined, "GEHEIMER") || src.Project != roleProject || src.Label != "pitfall" {
		t.Errorf("foreign request title leaked into the context or wrong source: %+v", src)
	}
}

func TestKnowledgeWithoutAProjectIsNotProjectData(t *testing.T) {
	st, _ := Open(":memory:")
	defer st.Close()
	id, _ := st.InsertKnowledge(Knowledge{Type: "note", Title: "Maschine", Body: "nur dieser Rechner", Scope: scope.Axes{Machine: "box"}})
	src, err := st.ExplainSource(explain.KindKnowledge, id)
	if err != nil || src.Project != "" || src.Hash == "" {
		t.Errorf("%+v %v", src, err)
	}
	if _, err := st.ExplainSource(explain.KindKnowledge, 9999); err != explain.ErrNotExplainable {
		t.Errorf("missing object: %v", err)
	}
	if _, err := st.ExplainSource("bogus", 1); err != explain.ErrNotExplainable {
		t.Errorf("bogus kind: %v", err)
	}
}

func TestSourcesOfRequestPainTaskAndQuestion(t *testing.T) {
	w := newPainWorld(t)
	st := w.st
	rid := w.request(t, "Zahlung einbauen")
	rsrc, err := st.ExplainSource(explain.KindRequest, rid)
	if err != nil || rsrc.Project != roleProject || rsrc.Title != "Zahlung einbauen" || !strings.Contains(strings.Join(rsrc.Context, "|"), "it works") {
		t.Fatalf("request: %+v %v", rsrc, err)
	}
	p := w.newPain(t, "mia", pain.Target{RequestID: rid})
	psrc, err := st.ExplainSource(explain.KindPain, p.Pain.ID)
	if err != nil || psrc.Project != roleProject || !strings.Contains(strings.Join(psrc.Context, "|"), "Zahlung einbauen") {
		t.Fatalf("pain: %+v %v", psrc, err)
	}
	task := w.task(t, "mia", humantask.CreateInput{RequestID: rid, Kind: "secret", Title: "Key anlegen", Location: "Agent Vault: stripe/prod"})
	tsrc, err := st.ExplainSource(explain.KindTask, task.ID)
	if err != nil || tsrc.Project != roleProject || !strings.Contains(tsrc.Body, "Agent Vault: stripe/prod") || !strings.Contains(strings.Join(tsrc.Context, "|"), "Zahlung einbauen") {
		t.Fatalf("task: %+v %v", tsrc, err)
	}

	room := RoomKeyForProject(roleProject)
	id := sendQuestion(t, st, room, "Soll der Wächter scharf geschaltet werden?")
	qsrc, err := st.ExplainSource(explain.KindQuestion, id)
	if err != nil || qsrc.Project != roleProject || !strings.Contains(qsrc.Body, "scharf") {
		t.Fatalf("question: %+v %v", qsrc, err)
	}
	// A question in a machine room belongs to no project: nothing is generated for it.
	mid := sendQuestion(t, st, RoomKeyForMachine("box"), "Neustart okay?")
	if msrc, err := st.ExplainSource(explain.KindQuestion, mid); err != nil || msrc.Project != "" {
		t.Errorf("machine room question: %+v %v", msrc, err)
	}
}

func sendQuestion(t *testing.T, st *Store, room, body string) int64 {
	t.Helper()
	kind := RoomProject
	if strings.HasPrefix(room, "machine:") {
		kind = RoomMachine
	}
	for _, agent := range []string{"claude:mia", "claude:lena"} {
		if _, err := st.RegisterCoordAgent(CoordAgent{ExternalID: agent, Provider: "claude", RoomKey: room, PrincipalID: map[string]string{"claude:mia": "person:3", "claude:lena": "person:2"}[agent], Person: strings.TrimPrefix(agent, "claude:")}); err != nil {
			t.Fatalf("register %s in %s (%s): %v", agent, room, kind, err)
		}
	}
	access := st.CoordinationFor(Principal{ID: "person:3", Label: "mia"}, "claude:mia")
	id, err := access.Send(CoordMessage{DestinationKind: DestinationRoom, DestinationID: room, SenderExternalID: "claude:mia", ClientID: "c-" + body[:3],
		Body: body, Intent: IntentQuestion, Mentions: []string{"claude:lena"}})
	if err != nil {
		t.Fatalf("send in %s: %v", room, err)
	}
	return id
}

func TestCandidatesStartWithTheReviewQueue(t *testing.T) {
	w := newPainWorld(t)
	st := w.st
	trusted, _ := st.InsertKnowledge(Knowledge{Type: "note", Title: "alt", Body: "b", Confidence: "trusted", Scope: scope.Axes{Project: roleProject}})
	staged, _ := st.InsertKnowledge(Knowledge{Type: "pitfall", Title: "wartet", Body: "b", Confidence: "staged", Scope: scope.Axes{Project: roleProject}})
	noProject, _ := st.InsertKnowledge(Knowledge{Type: "note", Title: "ohne", Body: "b", Confidence: "staged", Scope: scope.Axes{Machine: "box"}})
	rid := w.request(t, "offen")
	cands, err := st.ExplainCandidates(50)
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, c := range cands {
		pos[c.Kind+":"+itoa(c.ID)] = i + 1
	}
	if pos["knowledge:"+itoa(staged)] == 0 || pos["knowledge:"+itoa(staged)] > pos["request:"+itoa(rid)] || pos["request:"+itoa(rid)] > pos["knowledge:"+itoa(trusted)] {
		t.Errorf("order wrong: %v", cands)
	}
	if pos["knowledge:"+itoa(noProject)] != 0 {
		t.Error("a machine-bound entry is a candidate")
	}
	if got, _ := st.ExplainCandidates(1); len(got) != 1 {
		t.Errorf("scan limit ignored: %v", got)
	}
}

func TestUsageAddsUpPerDayAndCountsBySource(t *testing.T) {
	st, _ := Open(":memory:")
	defer st.Close()
	_ = st.AddExplainUsage("2026-10-15", 2, 1, 1000, 200)
	_ = st.AddExplainUsage("2026-10-16", 1, 0, 300, 100)
	_ = st.AddExplainUsage("2026-10-16", 1, 1, 200, 0)
	u, _ := st.ExplainUsageOn("2026-10-16")
	if u.Calls != 2 || u.Failures != 1 || u.InputTokens != 500 || u.OutputTokens != 100 {
		t.Errorf("%+v", u)
	}
	if none, _ := st.ExplainUsageOn("2026-01-01"); none.Calls != 0 || none.Day != "2026-01-01" {
		t.Errorf("%+v", none)
	}
	days, _ := st.ExplainUsageRecent(7)
	if len(days) != 2 || days[0].Day != "2026-10-16" {
		t.Errorf("%+v", days)
	}
	id, _ := st.InsertKnowledge(Knowledge{Type: "note", Title: "T", Body: "B", Scope: scope.Axes{Project: roleProject}})
	_ = st.SaveAgentExplanation(explain.KindKnowledge, id, note)
	_ = st.SaveExplanation(explain.Explanation{Kind: explain.KindPain, ObjectID: 1, Text: "x", Source: explain.SourceGenerated, ContentHash: "h"})
	if a, g, _ := st.ExplanationCounts(); a != 1 || g != 1 {
		t.Errorf("counts %d %d", a, g)
	}
}
