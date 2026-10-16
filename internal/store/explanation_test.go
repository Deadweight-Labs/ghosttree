package store

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
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
	if err := st.AddLLMUsage("explain", "2026-10-16", llmusage.Usage{Calls: 1, Failures: 0, InputTokens: 100, OutputTokens: 40}); err != nil {
		t.Fatal(err)
	}
	e, ok, _ := st.Explanation(explain.KindKnowledge, id)
	u, _ := st.LLMUsageOn("explain", "2026-10-16")
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
	_ = st.AddLLMUsage("explain", "2026-10-15", llmusage.Usage{Calls: 2, Failures: 1, InputTokens: 1000, OutputTokens: 200})
	_ = st.AddLLMUsage("explain", "2026-10-16", llmusage.Usage{Calls: 1, Failures: 0, InputTokens: 300, OutputTokens: 100})
	_ = st.AddLLMUsage("explain", "2026-10-16", llmusage.Usage{Calls: 1, Failures: 1, InputTokens: 200, OutputTokens: 0})
	u, _ := st.LLMUsageOn("explain", "2026-10-16")
	if u.Calls != 2 || u.Failures != 1 || u.InputTokens != 500 || u.OutputTokens != 100 {
		t.Errorf("%+v", u)
	}
	if none, _ := st.LLMUsageOn("explain", "2026-01-01"); none.Calls != 0 || none.Day != "2026-01-01" {
		t.Errorf("%+v", none)
	}
	days, _ := st.LLMUsageRecent("explain", 7)
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

// What the viewer-bound access model holds back must not reach the model
// provider and must not end up in an explanation every reader sees.
func TestHeldAndMachinePrivateObjectsAreNotExplainable(t *testing.T) {
	f := newVisFixture(t) // f.b: private session of another account
	st := f.st
	ref := "session:" + strconv.FormatInt(f.b.ID, 10) + "#1"
	ts := "'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'"
	insK := func(title, machine, confidence, origin, sref string) int64 {
		res, err := st.db.Exec(`INSERT INTO knowledge(type,title,body,project,machine,confidence,status,origin,session_ref,created_at,updated_at)
			VALUES('note',?,'body',?,?,?,'active',?,?,`+ts+`)`, title, roleProject, machine, confidence, origin, sref)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	insR := func(title, origin, person, sref string) int64 {
		res, err := st.db.Exec(`INSERT INTO requests(type,title,description,state,project,origin,person,session_ref,created_at,updated_at)
			VALUES('feature',?,'see REQ-1','open',?,?,?,?,`+ts+`)`, title, roleProject, origin, person, sref)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	heldK := insK("held finding", "", "quarantined", "distilled", ref)
	machK := insK("machine note", "box", "staged", "agent", "")
	openK := insK("open note", "", "staged", "agent", ref)
	heldR := insR("HELD REQUEST TITLE", "distilled", "", "session:"+strconv.FormatInt(f.b.ID, 10))
	openR := insR("visible request", "agent", "", "")
	// REQ-1 is whichever request got the first id here; make the reference explicit.
	_ = heldR

	for name, c := range map[string]struct {
		kind string
		id   int64
	}{"held knowledge": {explain.KindKnowledge, heldK}, "machine knowledge": {explain.KindKnowledge, machK}, "held request": {explain.KindRequest, heldR}} {
		src, err := st.ExplainSource(c.kind, c.id)
		if err != nil || src.Project != "" || len(src.Context) != 0 {
			t.Errorf("%s: %+v %v", name, src, err)
		}
	}
	for _, c := range []struct {
		kind string
		id   int64
	}{{explain.KindKnowledge, openK}, {explain.KindRequest, openR}} {
		if src, _ := st.ExplainSource(c.kind, c.id); src.Project != roleProject {
			t.Errorf("%s %d should stay explainable: %+v", c.kind, c.id, src)
		}
	}
	cands, err := st.ExplainCandidates(100)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if (c.Kind == explain.KindKnowledge && (c.ID == heldK || c.ID == machK)) || (c.Kind == explain.KindRequest && c.ID == heldR) {
			t.Errorf("candidate %+v is held back", c)
		}
	}
	if len(cands) != 2 {
		t.Errorf("want the two open objects, got %+v", cands)
	}

	// A public entry that names a held request must not carry its title.
	pub := insK("public", "", "trusted", "agent", "")
	if _, err := st.db.Exec(`UPDATE knowledge SET body = ? WHERE id = ?`, "see REQ-"+strconv.FormatInt(heldR, 10)+" and REQ-"+strconv.FormatInt(openR, 10), pub); err != nil {
		t.Fatal(err)
	}
	src, _ := st.ExplainSource(explain.KindKnowledge, pub)
	joined := strings.Join(src.Context, "\n")
	if strings.Contains(joined, "HELD REQUEST TITLE") || !strings.Contains(joined, "visible request") {
		t.Errorf("context %q", joined)
	}
}

func TestReplayedCreationDoesNotReplaceAnExplanation(t *testing.T) {
	st, _ := Open(":memory:")
	defer st.Close()
	id, _ := st.InsertKnowledge(Knowledge{Type: "note", Title: "T", Body: "B", Scope: scope.Axes{Project: roleProject}})
	if err := st.SaveAgentExplanation(explain.KindKnowledge, id, note); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAgentExplanation(explain.KindKnowledge, id, "Empfehlung: unbedingt ablehnen, das ist ganz falsch."); err != nil {
		t.Fatal(err)
	}
	if e, _, _ := st.Explanation(explain.KindKnowledge, id); e.Text != note {
		t.Errorf("replaced: %q", e.Text)
	}
}

func TestHashFollowsProjectAndState(t *testing.T) {
	st, _ := Open(":memory:")
	defer st.Close()
	id, _ := st.InsertKnowledge(Knowledge{Type: "note", Title: "T", Body: "B", Scope: scope.Axes{Project: roleProject}})
	before, _ := st.ExplainSource(explain.KindKnowledge, id)
	if _, err := st.db.Exec(`UPDATE knowledge SET project = 'github.com/dw/other' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	after, _ := st.ExplainSource(explain.KindKnowledge, id)
	if before.Hash == after.Hash {
		t.Error("moving to another project keeps the hash")
	}
}

// A request bound to a machine is private to that machine's owner: its title
// must reach neither a request reference, a task's context nor a pain's
// "motivates" line, since those explanations are shown to every reader.
func TestMachineBoundRequestTitleIsNotContextLine(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ts := "'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'"
	res, err := st.db.Exec(`INSERT INTO requests(type,title,description,state,project,machine,origin,person,created_at,updated_at)
		VALUES('feature','MACHINE SECRET TITLE','','open',?,'box','agent','mia',`+ts+`)`, roleProject)
	if err != nil {
		t.Fatal(err)
	}
	reqID, _ := res.LastInsertId()
	if title, ok := st.readableRequestTitle(reqID, roleProject); ok || title != "" {
		t.Errorf("machine-bound request resolved: %q", title)
	}
	kid, _ := st.InsertKnowledge(Knowledge{Type: "note", Title: "T", Body: "see REQ-" + strconv.FormatInt(reqID, 10), Scope: scope.Axes{Project: roleProject}})
	src, err := st.ExplainSource(explain.KindKnowledge, kid)
	if err != nil || strings.Contains(strings.Join(src.Context, "\n"), "MACHINE SECRET TITLE") {
		t.Errorf("knowledge context leaks: %+v %v", src.Context, err)
	}
	pres, err := st.db.Exec(`INSERT INTO pains(project,title,body,created_at,updated_at) VALUES(?,'P','b',`+ts+`)`, roleProject)
	if err != nil {
		t.Fatal(err)
	}
	painID, _ := pres.LastInsertId()
	if _, err := st.db.Exec(`INSERT INTO entity_links(project,src_kind,src_id,kind,dst_kind,dst_id,created_at) VALUES(?,'pain',?,'motivates','request',?,'2026-01-01T00:00:00Z')`, roleProject, painID, reqID); err != nil {
		t.Fatal(err)
	}
	psrc, err := st.ExplainSource(explain.KindPain, painID)
	if err != nil || strings.Contains(strings.Join(psrc.Context, "\n"), "MACHINE SECRET TITLE") {
		t.Errorf("pain context leaks: %+v %v", psrc.Context, err)
	}
}

func TestLLMUsageIsStoredPerJobAndDayAndSumsPerMonth(t *testing.T) {
	st := openLLMMem(t)
	add := func(job, day string, calls int, in, cr, cw int, micro int64) {
		t.Helper()
		if err := st.AddLLMUsage(job, day, llmusage.Usage{Calls: calls, InputTokens: in, OutputTokens: 1, CacheReadTokens: cr, CacheWriteTokens: cw, CostMicroUSD: micro}); err != nil {
			t.Fatal(err)
		}
	}
	add("garden", "2026-10-01", 2, 100, 1000, 10, 5_000)
	add("garden", "2026-10-01", 1, 50, 0, 0, 1_000)
	add("garden", "2026-10-16", 3, 10, 5, 5, 2_000)
	add("garden", "2026-09-30", 9, 9, 9, 9, 9_000_000)
	add("explain", "2026-10-16", 4, 4, 0, 0, 40)
	d, _ := st.LLMUsageOn("garden", "2026-10-01")
	if d.Calls != 3 || d.InputTokens != 150 || d.CacheReadTokens != 1000 || d.CostMicroUSD != 6_000 {
		t.Fatalf("day %+v", d)
	}
	m, _ := st.LLMUsageMonth("garden", "2026-10")
	if m.Calls != 6 || m.CacheWriteTokens != 15 || m.CostMicroUSD != 8_000 {
		t.Fatalf("month %+v (september must not count)", m)
	}
	if x, _ := st.LLMUsageMonth("explain", "2026-10"); x.Calls != 4 {
		t.Fatalf("jobs must be kept apart: %+v", x)
	}
	if rows, _ := st.LLMUsageRecent("garden", 2); len(rows) != 2 || rows[0].Day != "2026-10-16" {
		t.Fatalf("recent %+v", rows)
	}
	// The old explain API reads and writes the same rows.
	if err := st.AddLLMUsage("explain", "2026-10-16", llmusage.Usage{Calls: 1, Failures: 0, InputTokens: 5, OutputTokens: 5}); err != nil {
		t.Fatal(err)
	}
	if u, _ := st.LLMUsageOn("explain", "2026-10-16"); u.Calls != 5 {
		t.Fatalf("explain usage %+v", u)
	}
}

func TestOldExplainUsageIsCarriedIntoTheNewTableOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gt.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO explain_usage(day, calls, failures, input_tokens, output_tokens) VALUES('2026-10-10', 7, 1, 700, 70)`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	u, _ := st.LLMUsageOn("explain", "2026-10-10")
	if u.Calls != 7 || u.InputTokens != 700 || u.Failures != 1 {
		t.Fatalf("legacy row lost: %+v", u)
	}
}

func TestGardenChecksAreRememberedPerProjectAndEntry(t *testing.T) {
	st := openLLMMem(t)
	if err := st.SaveGardenCheck("p", GardenCheck{KnowledgeID: 5, CheckedAt: "2026-10-01T00:00:00Z", Commit: "abc", EntryHash: "h1", Verdict: "holds"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveGardenCheck("p", GardenCheck{KnowledgeID: 5, CheckedAt: "2026-10-16T00:00:00Z", Commit: "def", EntryHash: "h2", Verdict: "stale"}); err != nil {
		t.Fatal(err)
	}
	_ = st.SaveGardenCheck("other", GardenCheck{KnowledgeID: 5, Commit: "zzz"})
	got, err := st.GardenChecks("p")
	if err != nil || len(got) != 1 || got[5].Commit != "def" || got[5].Verdict != "stale" {
		t.Fatalf("%+v %v", got, err)
	}
	if n, oldest, _ := st.GardenCheckSummary(); n != 2 || oldest == "" {
		t.Fatalf("summary %d %q", n, oldest)
	}
	if err := st.SaveGardenCheck("", GardenCheck{KnowledgeID: 1}); err == nil {
		t.Error("a check without a project was accepted")
	}
}

func openLLMMem(t *testing.T) *Store {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}
