package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// actorCall is f.call with the session header an agent's ctx sends.
func actorCall(t *testing.T, f *accessAPIFixture, who, session string, want int, method, path string, body any) string {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq, _ := http.NewRequest(method, f.srv.URL+path, r)
	rq.Header.Set("Authorization", "Bearer "+f.tok[who])
	if session != "" {
		rq.Header.Set(originctx.SessionHeader, session)
	}
	resp, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s %s = %d (%s), want %d", who, method, path, resp.StatusCode, out, want)
	}
	return string(out)
}

// codexSession gives mia a Codex session on her machine whose transcript
// switched the model an hour ago.
func codexSession(t *testing.T, f *accessAPIFixture) int64 {
	t.Helper()
	id, err := f.st.UpsertSession(store.Session{Harness: "codex", ExternalID: "codex-mia", AccountID: 3,
		Scope: scope.Axes{Project: accProject, Machine: "mia-box"}})
	if err != nil {
		t.Fatal(err)
	}
	at := func(d time.Duration) string { return time.Now().UTC().Add(-d).Format(time.RFC3339Nano) }
	lines := []string{
		`{"timestamp":"` + at(2*time.Hour) + `","type":"turn_context","payload":{"model":"gpt-6-sol"}}`,
		`{"timestamp":"` + at(90*time.Minute) + `","type":"turn_context","payload":{"model":"codex-auto-review"}}`,
		`{"timestamp":"` + at(time.Hour) + `","type":"turn_context","payload":{"model":"gpt-6-luna"}}`,
	}
	var chunks []store.Chunk
	for i, l := range lines {
		chunks = append(chunks, store.Chunk{Seq: i, Raw: l})
	}
	if err := f.st.AppendChunks(id, chunks); err != nil {
		t.Fatal(err)
	}
	return id
}

func sessionRefOf(t *testing.T, st *store.Store, table string, id int64) string {
	t.Helper()
	var ref string
	if err := st.DB().QueryRow(`SELECT session_ref FROM `+table+` WHERE id=?`, id).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

func stampOf(t *testing.T, st *store.Store, kind string, id int64) store.EntryActor {
	t.Helper()
	ea, ok, err := st.ActorOf(kind, id)
	if err != nil || !ok {
		t.Fatalf("%s %d has no actor (%v)", kind, id, err)
	}
	return ea
}

// Kriterium 1: für jede Art setzt der Server den Akteur selbst, aus dem Token und
// der Session des Clients; Harness, Maschine und Modell kommen aus der Session.
func TestServerStampsTheActorOfEveryKind(t *testing.T) {
	f, room := guestRoomFixture(t, true)
	sid := codexSession(t, f)
	const s = "codex-mia"
	ids := map[string]int64{}

	d := decode[requestdomain.Detail](t, actorCall(t, f, "mia", s, 201, "POST", "/api/requests",
		map[string]any{"type": "feature", "title": "Akteur", "project": accProject, "criteria": []string{"eins"}}))
	ids[actor.Request] = d.Request.ID
	k := decode[store.Knowledge](t, actorCall(t, f, "mia", s, 200, "POST", "/api/knowledge",
		map[string]any{"type": "note", "title": "Notiz", "body": "b", "scope": map[string]string{"project": accProject}}))
	ids[actor.Knowledge] = k.ID
	p := decode[struct {
		Pain struct{ ID int64 } `json:"pain"`
	}](t, actorCall(t, f, "mia", s, 201, "POST", "/api/pains", map[string]any{"project": accProject, "title": "Schmerz"}))
	ids[actor.Pain] = p.Pain.ID
	i := decode[idea.Idea](t, actorCall(t, f, "mia", s, 201, "POST", "/api/ideas", map[string]any{"project": accProject, "title": "Wunsch"}))
	ids[actor.Idea] = i.ID
	task := decode[struct{ ID int64 }](t, actorCall(t, f, "mia", s, 201, "POST", "/api/tasks",
		map[string]any{"project": accProject, "kind": "input", "title": "Antwort", "request_id": d.Request.ID}))
	ids[actor.Task] = task.ID
	c := decode[store.Comment](t, actorCall(t, f, "mia", s, 201, "POST", "/api/comments", map[string]any{"kind": "request", "id": d.Request.ID, "body": "hallo"}))
	ids[actor.Comment] = c.ID
	actorCall(t, f, "mia", s, 200, "PUT", "/api/reactions", map[string]any{"kind": "request", "id": d.Request.ID, "content": "+1"})
	ids[actor.Reaction] = f.st.ReactionRowID("request", d.Request.ID, "+1", 3)
	m := decode[map[string]int64](t, actorCall(t, f, "mia", s, 200, "POST", "/api/coord/messages", store.CoordMessage{
		DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: "claude:mia", ClientID: "a1", Body: "hi"}))
	ids[actor.Message] = m["id"]
	feat := decode[struct{ ID int64 }](t, actorCall(t, f, "mia", s, 201, "POST", "/api/features",
		map[string]any{"project": accProject, "name": "Urheber", "summary": "Wer schrieb was."}))
	ids[actor.Feature] = feat.ID
	dec := decode[idea.Decision](t, actorCall(t, f, "mia", s, 201, "POST", "/api/decisions", map[string]any{
		"subject": "idea", "id": i.ID, "question": "Welche Anzeige",
		"variants": []map[string]any{{"title": "Name"}, {"title": "Name und Harness"}}}))
	actorCall(t, f, "mia", s, 200, "POST", idPath("/api/decisions/%d/votes", dec.ID), map[string]any{"variant": "B", "reason": "sagt mehr"})
	ids[actor.Vote] = f.st.VoteRowID(dec.ID, "person:3:agent")
	crit := d.Criteria[0].ID
	actorCall(t, f, "mia", s, 200, "PATCH", idPath("/api/criteria/%d", crit), map[string]any{"state": "met", "evidence_kind": "test", "evidence_ref": "go test ./..."})
	ids[actor.Criterion] = crit
	actorCall(t, f, "mia", s, 200, "POST", idPath("/api/requests/%d/complete", d.Request.ID), map[string]any{"evidence_kind": "test", "evidence_ref": "ok"})
	ids[actor.RequestDone] = d.Request.ID

	for kind, id := range ids {
		if id <= 0 {
			t.Errorf("%s: no id", kind)
			continue
		}
		ea := stampOf(t, f.st, kind, id)
		if ea.Kind != actor.Agent || ea.Harness != "codex" || ea.Machine != "mia-box" || ea.SessionID != sid || ea.AccountID != 3 || ea.Model != "gpt-6-luna" {
			t.Errorf("%s: %+v", kind, ea)
		}
	}
	// Die Lücke von Pains und Ideen: die Session steht jetzt auch dort.
	if got := sessionRefOf(t, f.st, "pains", p.Pain.ID); got != s {
		t.Errorf("pain session_ref = %q", got)
	}
	if got := sessionRefOf(t, f.st, "ideas", i.ID); got != s {
		t.Errorf("idea session_ref = %q", got)
	}
}

// Kein Parameter macht einen Agenten zum Menschen oder leiht sich eine fremde
// Session: Herkunft, Harness und Session-Id im Body zählen nicht, eine fremde
// Session im Header auch nicht.
func TestActorCannotBeSpoofed(t *testing.T) {
	f := accessAPI(t, true)
	codexSession(t, f)
	robinSession := f.id["s-robin"]

	d := decode[requestdomain.Detail](t, actorCall(t, f, "mia", "codex-mia", 201, "POST", "/api/requests",
		map[string]any{"type": "bug", "title": "Spoof", "project": accProject, "origin": "human", "session_ref": "s-robin"}))
	if d.Request.Origin != "agent" {
		t.Errorf("a token wrote origin %q", d.Request.Origin)
	}
	if ea := stampOf(t, f.st, actor.Request, d.Request.ID); ea.Kind != actor.Agent || ea.Harness != "codex" {
		t.Errorf("request: %+v", ea)
	}
	k := decode[store.Knowledge](t, actorCall(t, f, "mia", "codex-mia", 200, "POST", "/api/knowledge",
		map[string]any{"type": "note", "title": "Spoof", "body": "b", "origin": "human", "harness": "claude-code", "scope": map[string]string{"project": accProject}}))
	if ea := stampOf(t, f.st, actor.Knowledge, k.ID); ea.Kind != actor.Agent || ea.Harness != "codex" {
		t.Errorf("knowledge: %+v", ea)
	}
	// Robins Session im Header und als Body-Id: mia bekommt keine Session.
	p := decode[struct {
		Pain struct{ ID int64 } `json:"pain"`
	}](t, actorCall(t, f, "mia", "s-robin", 201, "POST", "/api/pains", map[string]any{"project": accProject, "title": "Fremd", "session_id": robinSession}))
	if ea := stampOf(t, f.st, actor.Pain, p.Pain.ID); ea.Kind != actor.Agent || ea.SessionID != 0 || ea.Harness != "" || ea.SessionRef != "" {
		t.Errorf("foreign session was taken: %+v", ea)
	}
	if got := sessionRefOf(t, f.st, "pains", p.Pain.ID); got != "" {
		t.Errorf("foreign session_ref on the pain: %q", got)
	}
	// Ohne Session ist ein Token-Aufruf ein Agent ohne Harness, nie ein Mensch.
	i := decode[idea.Idea](t, actorCall(t, f, "mia", "", 201, "POST", "/api/ideas", map[string]any{"project": accProject, "title": "Ohne"}))
	if ea := stampOf(t, f.st, actor.Idea, i.ID); ea.Kind != actor.Agent || ea.Harness != "" {
		t.Errorf("no session: %+v", ea)
	}
}

// Eine Session, die beim Schreiben noch nicht hochgeladen ist, wird später
// aufgelöst, aber nur zu einer Session desselben Kontos.
func TestActorResolvesALateSession(t *testing.T) {
	f := accessAPI(t, true)
	i := decode[idea.Idea](t, actorCall(t, f, "mia", "late-mia", 201, "POST", "/api/ideas", map[string]any{"project": accProject, "title": "Früh"}))
	if ea := stampOf(t, f.st, actor.Idea, i.ID); ea.SessionID != 0 || ea.SessionRef != "late-mia" {
		t.Fatalf("before upload: %+v", ea)
	}
	sid, err := f.st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "late-mia", AccountID: 3, Scope: scope.Axes{Project: accProject, Machine: "mia-box"}})
	if err != nil {
		t.Fatal(err)
	}
	if ea := stampOf(t, f.st, actor.Idea, i.ID); ea.SessionID != sid || ea.Harness != "claude-code" || ea.Machine != "mia-box" {
		t.Errorf("after upload: %+v", ea)
	}
}

// Kriterium 5: die Statistik zählt je Harness und Modell, nur in sichtbaren
// Projekten, und ein Gast bekommt das Projekt nicht.
func TestActorStatsAPI(t *testing.T) {
	f := accessAPI(t, true)
	codexSession(t, f)
	d := decode[requestdomain.Detail](t, actorCall(t, f, "mia", "codex-mia", 201, "POST", "/api/requests",
		map[string]any{"type": "feature", "title": "Zählen", "project": accProject, "criteria": []string{"eins"}}))
	actorCall(t, f, "mia", "codex-mia", 200, "PATCH", idPath("/api/criteria/%d", d.Criteria[0].ID), map[string]any{"state": "met", "evidence_kind": "test", "evidence_ref": "t"})
	actorCall(t, f, "mia", "codex-mia", 200, "POST", idPath("/api/requests/%d/complete", d.Request.ID), map[string]any{"evidence_kind": "test", "evidence_ref": "t"})

	type reply struct {
		Projects []string          `json:"projects"`
		Stats    []store.ActorStat `json:"stats"`
	}
	got := decode[reply](t, f.expect(t, "mia", 200, "GET", "/api/stats/actors", nil))
	var line *store.ActorStat
	for i := range got.Stats {
		if got.Stats[i].Model == "gpt-6-luna" {
			line = &got.Stats[i]
		}
	}
	if line == nil || line.Harness != "codex" || line.Family != "Codex" || line.Entries != 1 || line.CriteriaMet != 1 || line.RequestsDone != 1 || line.FeaturesDone != 1 {
		t.Fatalf("stats: %+v", got)
	}
	// Ausgemustertes Wissen zählt nicht, wie in den Listen.
	k := decode[store.Knowledge](t, actorCall(t, f, "mia", "codex-mia", 200, "POST", "/api/knowledge",
		map[string]any{"type": "note", "title": "Alt", "body": "b", "scope": map[string]string{"project": accProject}}))
	if _, err := f.st.DB().Exec(`UPDATE knowledge SET status='deprecated' WHERE id=?`, k.ID); err != nil {
		t.Fatal(err)
	}
	if again := decode[reply](t, f.expect(t, "mia", 200, "GET", "/api/stats/actors", nil)); len(again.Stats) == 0 || again.Stats[0].Entries != 1 {
		t.Errorf("deprecated knowledge counts: %+v", again.Stats)
	}
	// Für ein anderes Mitglied ist mias Session privat: dieselben Zahlen, Modell unbekannt.
	rex := decode[reply](t, f.expect(t, "rex", 200, "GET", "/api/stats/actors", nil))
	for _, st := range rex.Stats {
		if st.Model != "" {
			t.Errorf("a member counts the model of a private session: %+v", rex.Stats)
		}
	}
	if len(rex.Stats) == 0 || rex.Stats[0].Harness != "codex" || rex.Stats[0].RequestsDone != 1 {
		t.Errorf("member's stats: %+v", rex.Stats)
	}
	guest := decode[reply](t, f.expect(t, "gus", 200, "GET", "/api/stats/actors", nil))
	for _, p := range guest.Projects {
		if p == accProject {
			t.Errorf("a guest counts the project: %+v", guest)
		}
	}
	f.expect(t, "nora", 404, "GET", "/api/stats/actors?project="+accProject, nil)
	one := decode[reply](t, f.expect(t, "mia", 200, "GET", "/api/stats/actors?project="+accProject, nil))
	if len(one.Projects) != 1 || one.Projects[0] != accProject {
		t.Errorf("one project: %+v", one.Projects)
	}
}

// Kriterium 6 über die API: die Suche filtert nach by:, harness: und model:,
// und ein Gast filtert nicht nach dem Modell (es gehört zur Session).
func TestSearchFiltersByActor(t *testing.T) {
	f := accessAPI(t, true)
	codexSession(t, f)
	pain := func(session, title string) int64 {
		t.Helper()
		return decode[struct {
			Pain struct{ ID int64 } `json:"pain"`
		}](t, actorCall(t, f, "mia", session, 201, "POST", "/api/pains", map[string]any{"project": accProject, "title": title})).Pain.ID
	}
	byCodex := pain("codex-mia", "zebra codex")
	bare := pain("", "zebra bare")
	byHand := pain("", "zebra hand")
	// Wie aus der Weboberfläche: der Stempel des Token-Aufrufs weicht dem Menschen.
	if _, err := f.st.DB().Exec(`DELETE FROM entry_actors WHERE kind='pain' AND entry_id=?`, byHand); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RecordActor(actor.Pain, byHand, actor.Actor{Kind: actor.Human, AccountID: 3}); err != nil {
		t.Fatal(err)
	}
	find := func(who, line string) map[int64]bool {
		t.Helper()
		q := url.Values{"q": {"zebra " + line}, "project": {accProject}, "limit": {"50"}}
		res := decode[struct {
			Pains []struct{ ID int64 } `json:"pains"`
		}](t, f.expect(t, who, 200, "GET", "/api/search?"+q.Encode(), nil))
		out := map[int64]bool{}
		for _, p := range res.Pains {
			out[p.ID] = true
		}
		return out
	}
	for line, want := range map[string][]int64{
		"is:pain by:agent":                {byCodex, bare},
		"is:pain by:human":                {byHand},
		"is:pain harness:codex":           {byCodex},
		"is:pain model:gpt-6-luna":        {byCodex},
		"is:pain by:agent -harness:codex": {bare},
	} {
		got := find("mia", line)
		if len(got) != len(want) {
			t.Errorf("%s: %v, want %v", line, got, want)
			continue
		}
		for _, id := range want {
			if !got[id] {
				t.Errorf("%s: %v, want %v", line, got, want)
			}
		}
	}
	// Ein anderes Mitglied sieht mias private Session nicht: model: trifft nicht,
	// harness: schon (der Name steht ohnehin an jedem Eintrag).
	if got := find("rex", "is:pain model:gpt-6-luna"); len(got) != 0 {
		t.Errorf("a member filters by the model of a private session: %v", got)
	}
	if got := find("rex", "is:pain harness:codex"); !got[byCodex] {
		t.Errorf("a member does not find by harness: %v", got)
	}
	if got := find("gus", "is:pain harness:codex"); !got[byCodex] {
		t.Errorf("a guest sees the pain and its harness: %v", got)
	}
	if got := find("gus", "is:pain model:gpt"); len(got) != 0 {
		t.Errorf("a guest filters by model: %v", got)
	}
}
