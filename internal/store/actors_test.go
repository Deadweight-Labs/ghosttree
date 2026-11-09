package store

import (
	"strconv"
	"testing"

	actordomain "github.com/Deadweight-Labs/ghosttree/internal/actor"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const actorProject = "github.com/dw/actors"

func actorExec(t *testing.T, st *Store, q string, args ...any) {
	t.Helper()
	if _, err := st.db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// actorSession legt eine Session mit festem Zeitraum an.
func actorSession(t *testing.T, st *Store, harness, ext string, account int64, machine, from, to string) int64 {
	t.Helper()
	id, err := st.UpsertSession(Session{Harness: harness, ExternalID: ext, AccountID: account, StartedAt: from,
		Scope: scope.Axes{Project: actorProject, Machine: machine}})
	if err != nil {
		t.Fatal(err)
	}
	actorExec(t, st, `UPDATE sessions SET last_seen_at=? WHERE id=?`, to, id)
	return id
}

func claudeLine(model, at string) string {
	return `{"type":"assistant","timestamp":"` + at + `","message":{"model":"` + model + `","content":[]}}`
}

// Kriterium 2: der Upload eines Transkripts hält das Modell je Session samt
// Wechseln; ein Eintrag bekommt das Modell, das bei seiner Entstehung aktiv war.
func TestUploadedTranscriptKeepsModelSwitchesAndEntriesGetTheActiveModel(t *testing.T) {
	st := originStore(t)
	sid := actorSession(t, st, "claude-code", "s-switch", 1, "laptop", "2026-11-09T08:00:00Z", "2026-11-09T12:00:00Z")
	first := []Chunk{
		{Seq: 0, Role: "user", Raw: `{"type":"user","timestamp":"2026-11-09T08:00:00Z","message":{"content":"los"}}`},
		{Seq: 1, Role: "assistant", Raw: claudeLine("claude-opus-5-5", "2026-11-09T08:01:00.500Z")},
		{Seq: 2, Role: "assistant", Raw: claudeLine("claude-opus-5-5", "2026-11-09T08:20:00Z")},
	}
	second := []Chunk{
		{Seq: 3, Role: "assistant", Raw: claudeLine("<synthetic>", "2026-11-09T08:30:00Z")},
		{Seq: 4, Role: "assistant", Raw: claudeLine("claude-sonnet-5-5", "2026-11-09T09:00:00Z")},
		{Seq: 5, Role: "assistant", Raw: claudeLine("claude-opus-5-5", "2026-11-09T10:00:00Z")},
	}
	for _, batch := range [][]Chunk{first, second, first} { // der dritte Upload wiederholt nur
		if err := st.AppendChunks(sid, batch); err != nil {
			t.Fatal(err)
		}
	}
	segs, err := st.SessionModels(sid)
	if err != nil {
		t.Fatal(err)
	}
	want := []actordomain.Segment{
		{Model: "claude-opus-5-5", FirstAt: "2026-11-09T08:01:00Z", LastAt: "2026-11-09T08:20:00Z"},
		{Model: "claude-sonnet-5-5", FirstAt: "2026-11-09T09:00:00Z", LastAt: "2026-11-09T09:00:00Z"},
		{Model: "claude-opus-5-5", FirstAt: "2026-11-09T10:00:00Z", LastAt: "2026-11-09T10:00:00Z"},
	}
	if len(segs) != len(want) {
		t.Fatalf("segments: %+v", segs)
	}
	for i := range want {
		if segs[i] != want[i] {
			t.Errorf("segment %d = %+v, want %+v", i, segs[i], want[i])
		}
	}
	agent := actordomain.Actor{Kind: actordomain.Agent, AccountID: 1, Harness: "claude-code", Machine: "laptop", SessionID: sid}
	for id, at := range map[int64]string{1: "2026-11-09T08:10:00Z", 2: "2026-11-09T09:30:00Z", 3: "2026-11-09T11:00:00Z"} {
		if err := recordActorTx(st.db, actordomain.Pain, id, agent, at, false); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ActorsOf(actordomain.Pain, []int64{1, 2, 3, 4})
	if err != nil {
		t.Fatal(err)
	}
	for id, model := range map[int64]string{1: "claude-opus-5-5", 2: "claude-sonnet-5-5", 3: "claude-opus-5-5"} {
		if ea := got[id]; ea.Model != model || ea.Person != "robin" || ea.Harness != "claude-code" {
			t.Errorf("pain %d: %+v, want model %s", id, ea, model)
		}
	}
	if _, ok := got[4]; ok {
		t.Error("an entry without stamp got an actor")
	}
}

func TestRecordActorKeepsTheFirstCreatorAndTheLastCloser(t *testing.T) {
	st := originStore(t)
	agent := actordomain.Actor{Kind: actordomain.Agent, AccountID: 1, Harness: "codex"}
	human := actordomain.Actor{Kind: actordomain.Human, AccountID: 1}
	for _, a := range []actordomain.Actor{agent, human} {
		if err := st.RecordActor(actordomain.Request, 7, a); err != nil {
			t.Fatal(err)
		}
		if err := st.RecordActor(actordomain.Criterion, 7, a); err != nil {
			t.Fatal(err)
		}
	}
	if ea, _, _ := st.ActorOf(actordomain.Request, 7); ea.Kind != actordomain.Agent {
		t.Errorf("creator was rewritten: %+v", ea)
	}
	if ea, _, _ := st.ActorOf(actordomain.Criterion, 7); ea.Kind != actordomain.Human {
		t.Errorf("closer was not replaced: %+v", ea)
	}
	// Ohne Art oder mit fremder Art wird nichts gespeichert.
	if err := st.RecordActor("session", 1, agent); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordActor(actordomain.Idea, 1, actordomain.Actor{AccountID: 1}); err != nil {
		t.Fatal(err)
	}
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM entry_actors`).Scan(&n)
	if n != 2 {
		t.Errorf("rows: %d", n)
	}
}

// Kriterium 4: der Nachtrag füllt nur, was nachweisbar ist, in Stapeln, mit
// Zahlenbericht; dry-run schreibt nichts, ein zweiter Lauf ändert nichts.
func TestBackfillActorsFillsOnlyWhatIsProven(t *testing.T) {
	st := originStore(t)
	if _, err := st.AddPerson("lena"); err != nil {
		t.Fatal(err)
	}
	sa := actorSession(t, st, "claude-code", "s-a", 1, "laptop", "2026-11-01T08:00:00Z", "2026-11-01T09:00:00Z")
	sb := actorSession(t, st, "codex", "s-b", 1, "buildbox", "2026-11-01T10:00:00Z", "2026-11-01T11:00:00Z")
	sc := actorSession(t, st, "claude-code", "s-c", 1, "laptop", "2026-11-01T10:30:00Z", "2026-11-01T10:45:00Z")
	actorSession(t, st, "claude-code", "s-l", 2, "lena-box", "2026-11-01T08:00:00Z", "2026-11-01T09:00:00Z")

	req := func(origin, ref string, anchor *gitanchor.Anchor) int64 {
		t.Helper()
		d, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "r " + origin + ref,
			Scope: scope.Axes{Project: actorProject}, Origin: origin, Person: "robin", SessionRef: ref}, AuthorID: 1, GitAnchor: anchor})
		if err != nil {
			t.Fatal(err)
		}
		actorExec(t, st, `UPDATE requests SET created_at=? WHERE id=?`, "2026-11-01T08:50:00Z", d.Request.ID)
		return d.Request.ID
	}
	rOwn := req("agent", "s-a", nil)
	rForeign := req("agent", "s-l", nil)
	rWeb := req("human", "", gitanchor.Web())
	rClaimed := req("human", "", nil)
	rDistilled := req("distilled", "s-a", nil)
	kid, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "k", Body: "b", Scope: scope.Axes{Project: actorProject}, Person: "robin", Origin: "agent", Harness: "codex", AuthorID: 1})
	if err != nil {
		t.Fatal(err)
	}
	actorExec(t, st, `UPDATE knowledge SET created_at='2026-11-01T08:00:00Z' WHERE id=?`, kid)
	painAt := func(title, at string, anchor *gitanchor.Anchor) int64 {
		t.Helper()
		id, err := st.commitPain(CreatePainInput{Project: actorProject, Title: title, Actor: pain.Actor{AccountID: 1, Person: "robin", GitAnchor: anchor}, AuthorID: 1}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if at != "" {
			actorExec(t, st, `UPDATE pains SET created_at=? WHERE id=?`, at, id)
		}
		return id
	}
	pOnly := painAt("eine Session ruft pain_record", "2026-11-01T08:30:00Z", nil)
	pNamed := painAt("zwei rufen, eine nennt die Nummer", "2026-11-01T10:35:00Z", nil)
	pNone := painAt("niemand ruft", "2026-11-01T10:40:00Z", nil)
	pWeb := painAt("aus dem Web", "2026-11-01T12:00:00Z", gitanchor.Web())
	painAt("gerade eben", "", nil)
	actorExec(t, st, `INSERT INTO ideas(project,title,person,session_ref,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		actorProject, "Idee ohne Nachweis", "robin", "", "2026-11-01T13:00:00Z", "2026-11-01T13:00:00Z")

	// Transkriptzeilen am Live-Weg vorbei, wie bei Sessions vor dieser Version.
	call := func(at string) string {
		return `{"type":"assistant","timestamp":"` + at + `","message":{"content":[{"type":"tool_use","name":"mcp__ghosttree__pain_record"}]}}`
	}
	result := func(at string, id int64) string {
		return `{"type":"user","timestamp":"` + at + `","message":{"content":[{"type":"tool_result","content":"PAIN-` + strconv.FormatInt(id, 10) + ` recorded"}]}}`
	}
	chunks := func(sid int64, lines ...string) {
		t.Helper()
		for i, l := range lines {
			actorExec(t, st, `INSERT INTO session_chunks(session_id,seq,role,text,raw) VALUES(?,?,'','',?)`, sid, i, l)
		}
	}
	// Zeilen ohne Zeit liegen dazwischen und zählen weder als Aufruf noch als Ende des Fensters.
	chunks(sa, claudeLine("claude-opus-5-5", "2026-11-01T08:05:00Z"), `{"type":"progress","data":"x"}`, call("2026-11-01T08:29:50Z"),
		`{"type":"progress","data":"y"}`, result("2026-11-01T08:29:55Z", pOnly), claudeLine("claude-sonnet-5-5", "2026-11-01T08:40:00Z"))
	actorExec(t, st, `UPDATE session_chunks SET role='assistant' WHERE session_id=? AND raw LIKE '%"model"%'`, sa)
	// In s-b nennt bei pNone nur ein Prompt das Werkzeug und ein Ergebnis die Nummer: kein Aufruf.
	chunks(sb, `{"timestamp":"2026-11-01T10:01:00Z","type":"turn_context","payload":{"model":"gpt-6-sol"}}`, call("2026-11-01T10:34:30Z"), result("2026-11-01T10:34:40Z", pNamed+100),
		`{"type":"user","timestamp":"2026-11-01T10:39:50Z","message":{"content":"bitte pain_record aufrufen, ctx pain add geht auch"}}`, result("2026-11-01T10:39:55Z", pNone))
	chunks(sc, call("2026-11-01T10:34:00Z"), result("2026-11-01T10:34:10Z", pNamed), `{"type":"user","timestamp":"2026-11-01T10:44:00Z"}`)

	dry, err := st.BackfillActors(ActorBackfillOptions{DryRun: true, Batch: 2, Models: true})
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	st.db.QueryRow(`SELECT COUNT(*) FROM entry_actors`).Scan(&rows)
	if rows != 0 {
		t.Fatalf("dry run wrote %d stamps", rows)
	}
	kinds := map[string]ActorBackfillKind{}
	for _, k := range dry.Kinds {
		kinds[k.Kind] = k
	}
	if r := kinds["request"]; r.Candidates != 5 || r.Agent != 2 || r.WithSession != 1 || r.Human != 1 || r.Unknown != 2 {
		t.Errorf("requests: %+v", r)
	}
	if p := kinds["pain"]; p.Candidates != 5 || p.Agent != 2 || p.WithSession != 2 || p.Human != 1 || p.Unknown != 1 || p.Recent != 1 {
		t.Errorf("pains: %+v", p)
	}
	if i := kinds["idea"]; i.Candidates != 1 || i.Unknown != 1 {
		t.Errorf("ideas: %+v", i)
	}
	if dry.Sessions != 1 || dry.Segments != 2 {
		t.Errorf("dry models: %d sessions, %d segments", dry.Sessions, dry.Segments)
	}

	rep, err := st.BackfillActors(ActorBackfillOptions{Batch: 2, Models: true})
	if err != nil {
		t.Fatal(err)
	}
	check := func(kind string, id int64, want actordomain.Actor, model string) {
		t.Helper()
		ea, ok, err := st.ActorOf(kind, id)
		if err != nil || !ok || ea.Kind != want.Kind || ea.Harness != want.Harness || ea.SessionID != want.SessionID || ea.Machine != want.Machine || ea.Model != model || !ea.Backfilled {
			t.Errorf("%s %d: %+v (ok %v), want %+v model %q", kind, id, ea, ok, want, model)
		}
	}
	agent := actordomain.Agent
	check("request", rOwn, actordomain.Actor{Kind: agent, Harness: "claude-code", Machine: "laptop", SessionID: sa}, "claude-sonnet-5-5")
	check("request", rForeign, actordomain.Actor{Kind: agent}, "")
	check("request", rWeb, actordomain.Actor{Kind: actordomain.Human}, "")
	check("knowledge", kid, actordomain.Actor{Kind: agent, Harness: "codex"}, "")
	check("pain", pOnly, actordomain.Actor{Kind: agent, Harness: "claude-code", Machine: "laptop", SessionID: sa}, "claude-opus-5-5")
	check("pain", pNamed, actordomain.Actor{Kind: agent, Harness: "claude-code", Machine: "laptop", SessionID: sc}, "")
	check("pain", pWeb, actordomain.Actor{Kind: actordomain.Human}, "")
	for kind, id := range map[string]int64{"request": rDistilled, "pain": pNone} {
		if _, ok, _ := st.ActorOf(kind, id); ok {
			t.Errorf("%s %d got an actor without proof", kind, id)
		}
	}
	if _, ok, _ := st.ActorOf("request", rClaimed); ok {
		t.Error("origin human without a web origin was taken as a person")
	}
	if rep.Sessions != 1 {
		t.Errorf("models: %+v", rep)
	}
	// Ein Live-Stempel ersetzt einen nachgetragenen, einen live geschriebenen nicht.
	if err := st.RecordActor("request", rForeign, actordomain.Actor{Kind: actordomain.Human, AccountID: 1}); err != nil {
		t.Fatal(err)
	}
	if ea, _, _ := st.ActorOf("request", rForeign); ea.Kind != actordomain.Human || ea.Backfilled {
		t.Errorf("live stamp did not replace the backfilled one: %+v", ea)
	}
	if err := st.RecordActor("request", rForeign, actordomain.Actor{Kind: agent, AccountID: 1}); err != nil {
		t.Fatal(err)
	}
	if ea, _, _ := st.ActorOf("request", rForeign); ea.Kind != actordomain.Human {
		t.Errorf("a second live stamp rewrote the first: %+v", ea)
	}
	again, err := st.BackfillActors(ActorBackfillOptions{Models: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range again.Kinds {
		if k.Human+k.Agent != 0 {
			t.Errorf("second run filled %+v", k)
		}
	}
	if again.Sessions != 0 {
		t.Errorf("second run read models again: %+v", again)
	}
	// Kriterium 5 im Store: nur das genannte Projekt zählt.
	stats, _, err := st.Access(Principal{ID: "person:1", Label: "robin", TokenKind: WebSessionKind}).ActorStats(actorProject)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range stats {
		if s.Actor == agent && s.Harness == "claude" && s.Model == "claude-sonnet-5-5" && s.ModelLabel == "Sonnet 5.5" && s.Entries == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("stats: %+v", stats)
	}
	if hits, _ := st.ActorStatHits([]string{"github.com/dw/other"}); len(hits) != 0 {
		t.Errorf("another project counts: %+v", hits)
	}
}

// Eine Session, die beim Ausrollen schon lief, hat nur den späteren Teil ihrer
// Modellgeschichte; der Nachtrag liest sie ganz neu.
func TestBackfillRereadsAPartialModelHistory(t *testing.T) {
	st := originStore(t)
	sid := actorSession(t, st, "claude-code", "s-run", 1, "laptop", "2026-11-01T08:00:00Z", "2026-11-01T12:00:00Z")
	actorExec(t, st, `INSERT INTO session_chunks(session_id,seq,role,text,raw) VALUES(?,0,'assistant','',?)`, sid, claudeLine("claude-opus-5-5", "2026-11-01T08:01:00Z"))
	if err := st.AppendChunks(sid, []Chunk{{Seq: 1, Role: "assistant", Raw: claudeLine("claude-sonnet-5-5", "2026-11-01T11:00:00Z")}}); err != nil {
		t.Fatal(err)
	}
	if err := recordActorTx(st.db, actordomain.Pain, 1, actordomain.Actor{Kind: actordomain.Agent, AccountID: 1, SessionID: sid}, "2026-11-01T09:00:00Z", false); err != nil {
		t.Fatal(err)
	}
	if ea, _, _ := st.ActorOf(actordomain.Pain, 1); ea.Model != "claude-sonnet-5-5" {
		t.Fatalf("before: %+v", ea)
	}
	rep, err := st.BackfillActors(ActorBackfillOptions{Models: true, Kinds: []string{"none"}})
	if err != nil || rep.Sessions != 1 {
		t.Fatalf("backfill: %+v %v", rep, err)
	}
	if ea, _, _ := st.ActorOf(actordomain.Pain, 1); ea.Model != "claude-opus-5-5" {
		t.Errorf("after: %+v", ea)
	}
}

// Ein Stempel, der vor dem ersten Upload seiner Session geschrieben wurde,
// bekommt die Session beim Upload, aber nur vom eigenen Konto.
func TestUploadBindsPendingStamps(t *testing.T) {
	st := originStore(t)
	if _, err := st.AddPerson("lena"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordActor(actordomain.Idea, 1, actordomain.Actor{Kind: actordomain.Agent, AccountID: 1, SessionRef: "s-late"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordActor(actordomain.Idea, 2, actordomain.Actor{Kind: actordomain.Agent, AccountID: 2, SessionRef: "s-late"}); err != nil {
		t.Fatal(err)
	}
	sid := actorSession(t, st, "codex", "s-late", 1, "laptop", "2026-11-01T08:00:00Z", "2026-11-01T09:00:00Z")
	var bound, ref string
	st.db.QueryRow(`SELECT COALESCE(session_id,0), session_ref FROM entry_actors WHERE kind='idea' AND entry_id=1`).Scan(&bound, &ref)
	if bound != strconv.FormatInt(sid, 10) || ref != "" {
		t.Errorf("own stamp: session %s ref %q", bound, ref)
	}
	if ea, _, _ := st.ActorOf(actordomain.Idea, 2); ea.SessionID != 0 {
		t.Errorf("foreign stamp got the session: %+v", ea)
	}
}

// Die Aufräumung der Raum-Nachrichten nimmt deren Stempel mit.
func TestCoordRetentionRemovesStampsOfDeletedMessages(t *testing.T) {
	st := originStore(t)
	if err := st.RecordActor(actordomain.Message, 999, actordomain.Actor{Kind: actordomain.Agent, AccountID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordActor(actordomain.Pain, 999, actordomain.Actor{Kind: actordomain.Agent, AccountID: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyCoordRetention("2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.ActorOf(actordomain.Message, 999); ok {
		t.Error("the stamp of a message that no longer exists stayed")
	}
	if _, ok, _ := st.ActorOf(actordomain.Pain, 999); !ok {
		t.Error("retention removed a stamp of another kind")
	}
}

// Die SQL-Funktion des Nachtrags rechnet dieselbe Id wie der Live-Stempel.
func TestVoteEntrySQLFunctionMatchesGo(t *testing.T) {
	st := originStore(t)
	var got int64
	if err := st.db.QueryRow(`SELECT ghosttree_vote_entry(5, 'person:1:agent')`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := actordomain.VoteEntryID(5, "person:1:agent"); got != want {
		t.Errorf("sql %d, go %d", got, want)
	}
}

// Die Kanonisierung löscht doppelte Requests samt ihren Stempeln.
func TestCanonicalizeRemovesStampsOfMergedRequests(t *testing.T) {
	s := openTest(t)
	var ids []int64
	for _, project := range []string{"github.com/Old-Owner/Project", "github.com/example/project"} {
		d, err := s.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "change", Title: "Doppelt",
			Description: "gleich", Scope: scope.Axes{Project: project}}, Criteria: []string{"eins"}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.Request.ID)
		agent := actordomain.Actor{Kind: actordomain.Agent, AccountID: 1}
		for kind, id := range map[string]int64{actordomain.Request: d.Request.ID, actordomain.RequestDone: d.Request.ID, actordomain.Criterion: d.Criteria[0].ID} {
			if err := s.RecordActor(kind, id, agent); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.CanonicalizeScopes(map[string]string{"github.com/old-owner/project": "github.com/example/project"}); err != nil {
		t.Fatal(err)
	}
	var left int
	s.db.QueryRow(`SELECT COUNT(*) FROM entry_actors e WHERE (e.kind IN ('request','request_done') AND NOT EXISTS (SELECT 1 FROM requests r WHERE r.id=e.entry_id))
		OR (e.kind='criterion' AND NOT EXISTS (SELECT 1 FROM request_criteria c WHERE c.id=e.entry_id))`).Scan(&left)
	if left != 0 {
		t.Errorf("%d stamps of deleted requests stayed", left)
	}
	var kept int
	s.db.QueryRow(`SELECT COUNT(*) FROM entry_actors`).Scan(&kept)
	if kept != 3 {
		t.Errorf("stamps of the surviving request: %d, want 3", kept)
	}
}

// Prüfbefund B3: Stimmen-Stempel der ersten Fassung (Schlüssel rowid) bekommen
// beim Start den stabilen Schlüssel; ein zweiter Start ändert nichts.
func TestVoteStampsKeyedByRowIDAreRekeyed(t *testing.T) {
	st := originStore(t)
	if _, err := st.db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO decision_votes(decision_id,voter_key,voter,by_agent,variant_id,reason,created_at,updated_at)
		VALUES(7,'person:1:agent','robin',1,1,'weil','2026-11-01T00:00:00Z','2026-11-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	var rowid int64
	st.db.QueryRow(`SELECT rowid FROM decision_votes WHERE decision_id=7`).Scan(&rowid)
	if err := recordActorTx(st.db, actordomain.Vote, rowid, actordomain.Actor{Kind: actordomain.Agent, AccountID: 1, Harness: "codex"}, "2026-11-01T00:00:00Z", false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := rekeyVoteActors(st.db); err != nil {
			t.Fatal(err)
		}
	}
	want := actordomain.VoteEntryID(7, "person:1:agent")
	if ea, ok, _ := st.ActorOf(actordomain.Vote, want); !ok || ea.Harness != "codex" {
		t.Errorf("rekeyed stamp: %+v %v", ea, ok)
	}
	if _, ok, _ := st.ActorOf(actordomain.Vote, rowid); ok {
		t.Error("the old key stayed")
	}
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM entry_actors WHERE kind='vote'`).Scan(&n)
	if n != 1 {
		t.Errorf("vote stamps: %d", n)
	}
}

// Prüfbefund B4: kommt zwischen Lesen und Ersetzen eine Zeile dazu, ersetzt der
// Nachtrag nichts; die Zeile (und ihr Modell vom Upload) geht nicht verloren.
func TestModelBackfillDoesNotReplaceAfterANewLine(t *testing.T) {
	st := originStore(t)
	sid := actorSession(t, st, "claude-code", "s-race", 1, "laptop", "2026-11-01T08:00:00Z", "2026-11-01T12:00:00Z")
	actorExec(t, st, `INSERT INTO session_chunks(session_id,seq,role,text,raw) VALUES(?,0,'assistant','',?)`, sid, claudeLine("claude-opus-5-5", "2026-11-01T08:01:00Z"))
	top, err := maxChunkSeq(st.db, sid)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := sessionObservations(st.db, sid, top)
	if err != nil {
		t.Fatal(err)
	}
	segs := actordomain.Fold(nil, obs)
	// Der Upload kommt dazwischen.
	if err := st.AppendChunks(sid, []Chunk{{Seq: 1, Role: "assistant", Raw: claudeLine("claude-sonnet-5-5", "2026-11-01T09:00:00Z")}}); err != nil {
		t.Fatal(err)
	}
	done, err := st.replaceSessionModels(sid, top, segs)
	if err != nil || done {
		t.Fatalf("replaced despite a new line: %v %v", done, err)
	}
	if got, _ := st.SessionModels(sid); len(got) != 1 || got[0].Model != "claude-sonnet-5-5" {
		t.Errorf("the upload's model was lost: %+v", got)
	}
	// Der ganze Lauf liest dann neu und hat beide.
	if n, err := st.backfillSessionModelsOf(sid, false); err != nil || n != 2 {
		t.Fatalf("backfill: %d %v", n, err)
	}
	if got, _ := st.SessionModels(sid); len(got) != 2 {
		t.Errorf("after backfill: %+v", got)
	}
}
