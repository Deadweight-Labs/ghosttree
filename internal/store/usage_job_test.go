package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/chunktext"
	"github.com/Deadweight-Labs/ghosttree/internal/deliverylog"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// usageFixture is one session of an account with three delivered entries and a
// transcript in which the agent names the first, ignores the second and opens
// the file the third names (the acceptance test of P3).
type usageFixture struct {
	s             *Store
	base          time.Time
	account       int64
	session       string
	sid           int64
	picked, none  int64
	followed      int64
	pickedRow     int64
	rows          map[int64]int64 // entry -> row of the log
	deliveredPath string
}

func useLine(at time.Time, role string, content any) string {
	b, _ := json.Marshal(map[string]any{"type": role, "timestamp": at.UTC().Format("2006-01-02T15:04:05.000Z"),
		"message": map[string]any{"role": role, "content": content}})
	return string(b)
}

func useText(s string) []map[string]any { return []map[string]any{{"type": "text", "text": s}} }

func useTool(name string, input map[string]any) []map[string]any {
	return []map[string]any{{"type": "tool_use", "id": "toolu_" + name, "name": name, "input": input}}
}

// appendLines stores lines of a session; withText stores the derived text with
// them (a server that writes it) or leaves it empty (a server that derives it).
func useAppend(t *testing.T, s *Store, sid int64, withText bool, lines []string, roles []string) {
	t.Helper()
	var chunks []Chunk
	for i, raw := range lines {
		c := Chunk{Seq: i, Role: roles[i], Raw: raw}
		if withText {
			_, c.Text = chunktext.Derive("claude-code", raw)
		}
		chunks = append(chunks, c)
	}
	if err := s.AppendChunks(sid, chunks); err != nil {
		t.Fatal(err)
	}
}

func newUsageFixture(t *testing.T, s *Store, withText bool) *usageFixture {
	t.Helper()
	f := &usageFixture{s: s, base: time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second), account: 7, session: "sess-use-1",
		rows: map[int64]int64{}, deliveredPath: "internal/deliverylog/deliverylog.go"}
	var err error
	if f.picked, err = s.InsertKnowledge(Knowledge{Type: "pitfall", Title: "Zustellprotokoll schreibt der Hook, nicht der Server",
		Body: "Nur im Hook sind Text und Budget bekannt.", Person: "robin"}); err != nil {
		t.Fatal(err)
	}
	if f.none, err = s.InsertKnowledge(Knowledge{Type: "pitfall", Title: "Wörterbuch lernt sich ein, erst nach neunzig Sekunden",
		Body: "Pakete davor sind ohne Wörterbuch.", Person: "robin"}); err != nil {
		t.Fatal(err)
	}
	if f.followed, err = s.InsertKnowledge(Knowledge{Type: "pitfall", Title: "Teile einer Zustellung ergeben die Zeichenzahl",
		Body: "Siehe `" + f.deliveredPath + "`, dort steht Fit.", Person: "robin"}); err != nil {
		t.Fatal(err)
	}
	f.sid, err = s.UpsertSession(Session{Harness: "claude-code", ExternalID: f.session, Scope: scope.Axes{Project: logProject}, AccountID: f.account})
	if err != nil {
		t.Fatal(err)
	}
	at := func(sec int) time.Time { return f.base.Add(time.Duration(sec) * time.Second) }
	lines := []string{
		useLine(at(-60), "user", "Bitte schau dir das Protokoll an"),
		useLine(at(5), "assistant", useText("Das steht in KN-"+fmt.Sprint(f.picked)+", der Hook schreibt es.")),
		useLine(at(6), "user", []map[string]any{{"type": "tool_result", "tool_use_id": "x", "content": "Wörterbuch lernt sich ein, erst nach neunzig Sekunden"}}),
		useLine(at(10), "assistant", useTool("Read", map[string]any{"file_path": "/home/x/repo/" + f.deliveredPath})),
	}
	useAppend(t, s, f.sid, withText, lines, []string{"user", "assistant", "tool", "assistant"})
	for i, id := range []int64{f.picked, f.none, f.followed} {
		serve(t, s, f.account, f.session, id)
		if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: f.session, Harness: "claude", Events: []DeliveryEvent{{
			Call: fmt.Sprintf("call%d", i), At: f.base.Format(time.RFC3339),
			Parts: []deliverylog.Part{{Via: deliverylog.ViaBootstrap, Kind: deliverylog.KindKnowledge, ID: id, Chars: 100 + 10*i}}}}}, f.account); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int64{f.picked, f.none, f.followed} {
		if err := s.db.QueryRow(`SELECT id FROM context_delivery_log WHERE object_id=? AND session_key=?`, id, f.session).Scan(new(int64)); err != nil {
			t.Fatal(err)
		}
		var row int64
		_ = s.db.QueryRow(`SELECT id FROM context_delivery_log WHERE object_id=? AND session_key=?`, id, f.session).Scan(&row)
		f.rows[id] = row
	}
	return f
}

// serve is what the server does when it hands an entry to a session: it
// remembers it in the delivery memory, which the job relies on.
func serve(t testing.TB, s *Store, account int64, session string, id int64) {
	t.Helper()
	if _, err := s.ClaimContextDeliveries(fmt.Sprintf("person:%d", account), session, DeliveredByBootstrap, []ContextDelivery{{KnowledgeID: id}}); err != nil {
		t.Fatal(err)
	}
}

func (f *usageFixture) used(t *testing.T, id int64) (int, string) {
	t.Helper()
	var u int
	var at string
	if err := f.s.db.QueryRow(`SELECT used, judged_at FROM context_delivery_log WHERE id=?`, f.rows[id]).Scan(&u, &at); err != nil {
		t.Fatal(err)
	}
	return u, at
}

func (f *usageFixture) judge(t *testing.T) JudgeResult {
	t.Helper()
	res, err := f.s.JudgeDeliveries(context.Background(), JudgeOptions{Now: time.Now().Add(2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// REQ-645 AC 1: a transcript in which the agent names one delivered entry,
// ignores another and opens the file the third names gives 1, 0 and 2, and the
// judgement is dated. The same for a server that stores the text of a chunk and
// one that derives it, on a plain store and on the runtime store with its reader
// and writer queue.
func TestJudgeDeliveriesReadsTheTranscriptOfAFinishedSession(t *testing.T) {
	for name, open := range map[string]func(*testing.T) *Store{"direct": openTest, "runtime": openRuntimeTest} {
		for _, withText := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stored-text=%v", name, withText), func(t *testing.T) {
				s := open(t)
				f := newUsageFixture(t, s, withText)
				res := f.judge(t)
				for id, want := range map[int64]int{f.picked: UsedPickedUp, f.none: UsedNone, f.followed: UsedFollowed} {
					got, at := f.used(t, id)
					if got != want || at == "" {
						t.Errorf("entry %d: used = %d judged_at = %q, want %d and a date", id, got, at, want)
					}
				}
				if res.Rows != 3 || res.PickedUp != 1 || res.Followed != 1 || res.Sessions != 1 || res.More {
					t.Errorf("result = %+v", res)
				}
				// A second run changes nothing and counts nothing twice.
				if again := f.judge(t); again.Rows != 0 || again.Sessions != 0 {
					t.Errorf("second run = %+v", again)
				}
				var delivered, used, followed int
				if err := s.db.QueryRow(`SELECT delivered_sessions, used_sessions, followed_sessions FROM knowledge_usage WHERE knowledge_id=?`, f.followed).Scan(&delivered, &used, &followed); err != nil ||
					delivered != 1 || used != 1 || followed != 1 {
					t.Errorf("knowledge_usage of the followed entry: %d %d %d %v", delivered, used, followed, err)
				}
				if err := s.db.QueryRow(`SELECT delivered_sessions, used_sessions FROM knowledge_usage WHERE knowledge_id=?`, f.none).Scan(&delivered, &used); err != nil || delivered != 1 || used != 0 {
					t.Errorf("knowledge_usage of the ignored entry: %d %d %v", delivered, used, err)
				}
			})
		}
	}
}

// REQ-645 AC 3: a broad entry that was delivered in a project and picked up there
// is tied to that project (source used); one that was not is not.
func TestJudgeDeliveriesTiesPickedUpBroadEntriesToTheProject(t *testing.T) {
	s := openTest(t)
	f := newUsageFixture(t, s, false)
	f.judge(t)
	for id, want := range map[int64]int{f.picked: 1, f.none: 0, f.followed: 1} {
		if n := logCount(t, s, `SELECT COUNT(*) FROM knowledge_project_affinity WHERE knowledge_id=? AND project=? AND source='used'`, id, logProject); n != want {
			t.Errorf("entry %d: %d affinity rows from use, want %d", id, n, want)
		}
	}
	// An entry that belongs to a project already needs no tie.
	own, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "Eigenes Projektwissen hier", Body: "x", Scope: scope.Axes{Project: logProject}, Person: "robin"})
	if err != nil {
		t.Fatal(err)
	}
	serve(t, s, f.account, f.session, own)
	if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: f.session, Harness: "claude", Events: []DeliveryEvent{{
		Call: "own", At: f.base.Format(time.RFC3339), Parts: []deliverylog.Part{{Via: deliverylog.ViaRelevance, Kind: deliverylog.KindKnowledge, ID: own, Chars: 50}}}}}, f.account); err != nil {
		t.Fatal(err)
	}
	f.judge(t)
	if n := logCount(t, s, `SELECT COUNT(*) FROM knowledge_project_affinity WHERE knowledge_id=?`, own); n != 0 {
		t.Errorf("project knowledge got %d affinity rows", n)
	}
}

// REQ-645 AC 2: a session the account does not own is not read, and neither is
// one that is not over, nor a subagent's briefing nor a row that was held out.
func TestJudgeDeliveriesReadsOnlySessionsTheAccountOwns(t *testing.T) {
	s := openTest(t)
	f := newUsageFixture(t, s, false)
	// The same session key delivered to another account: its rows must not be
	// judged against this account's transcript, and this account's against its own.
	if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: f.session, Harness: "claude", Events: []DeliveryEvent{{
		Call: "other", At: f.base.Format(time.RFC3339), Parts: []deliverylog.Part{{Via: deliverylog.ViaBootstrap, Kind: deliverylog.KindKnowledge, ID: f.picked, Chars: 5}}}}}, 8); err != nil {
		t.Fatal(err)
	}
	// A subagent briefing of the account's own session, and a part not yet settled.
	if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: f.session, Harness: "claude", Events: []DeliveryEvent{
		{Call: "sub", Subagent: true, At: f.base.Format(time.RFC3339), Parts: []deliverylog.Part{{Via: deliverylog.ViaSubagent, Kind: deliverylog.KindKnowledge, ID: f.picked, Chars: 5}}},
	}}, f.account); err != nil {
		t.Fatal(err)
	}
	f.judge(t)
	var other, sub int
	_ = s.db.QueryRow(`SELECT used FROM context_delivery_log WHERE call='other'`).Scan(&other)
	_ = s.db.QueryRow(`SELECT used FROM context_delivery_log WHERE call='sub'`).Scan(&sub)
	if other != UsedPending || sub != UsedPending {
		t.Errorf("a row of another account or of a subagent was judged: other=%d subagent=%d", other, sub)
	}
	if n := logCount(t, s, `SELECT COUNT(*) FROM context_delivery_log WHERE call IN ('other','sub') AND judged_at != ''`); n != 0 {
		t.Errorf("%d rows were dated", n)
	}
	// A session that is still running is left alone.
	youngSession(t, s)
	if res, err := s.JudgeDeliveries(context.Background(), JudgeOptions{Now: time.Now()}); err != nil || res.Sessions != 0 {
		t.Errorf("a session that is not over was read: %+v %v", res, err)
	}
}

// youngSession is a session of another account whose last upload is just now,
// with a delivery from an hour and a half ago.
func youngSession(t *testing.T, s *Store) {
	t.Helper()
	sid, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "sess-young", Scope: scope.Axes{Project: logProject}, AccountID: 9})
	if err != nil {
		t.Fatal(err)
	}
	useAppend(t, s, sid, false, []string{useLine(time.Now(), "assistant", useText("KN-1"))}, []string{"assistant"})
	if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: "sess-young", Harness: "claude", Events: []DeliveryEvent{{
		Call: "young", At: time.Now().Add(-90 * time.Minute).UTC().Format(time.RFC3339), Parts: []deliverylog.Part{{Via: deliverylog.ViaBootstrap, Kind: deliverylog.KindKnowledge, ID: 1, Chars: 5}}}}}, 9); err != nil {
		t.Fatal(err)
	}
}

// A row whose session never arrived is closed after the give-up age with no
// verdict, so the job does not look at it again; an entry that is gone likewise.
func TestJudgeDeliveriesClosesRowsThatCannotBeJudged(t *testing.T) {
	s := openTest(t)
	base := time.Now().UTC().Add(-3 * time.Hour)
	if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: "never-uploaded", Harness: "claude", Events: []DeliveryEvent{{
		Call: "lost", At: base.Format(time.RFC3339), Parts: []deliverylog.Part{{Via: deliverylog.ViaBootstrap, Kind: deliverylog.KindKnowledge, ID: 1, Chars: 5}}}}}, 7); err != nil {
		t.Fatal(err)
	}
	if res, err := s.JudgeDeliveries(context.Background(), JudgeOptions{Now: time.Now().Add(2 * time.Hour)}); err != nil || res.GaveUp != 0 {
		t.Fatalf("closed too early: %+v %v", res, err)
	}
	res, err := s.JudgeDeliveries(context.Background(), JudgeOptions{Now: time.Now().Add(15 * 24 * time.Hour)})
	if err != nil || res.GaveUp != 1 || res.Rows != 0 {
		t.Fatalf("result = %+v %v", res, err)
	}
	var used int
	var at string
	if err := s.db.QueryRow(`SELECT used, judged_at FROM context_delivery_log WHERE call='lost'`).Scan(&used, &at); err != nil || used != UsedPending || at == "" {
		t.Errorf("row: used=%d judged_at=%q %v", used, at, err)
	}
	// The entry of a part is gone: closed without a verdict, and without a count.
	f := newUsageFixture(t, s, false)
	if _, err := s.db.Exec(`DELETE FROM knowledge WHERE id=?`, f.none); err != nil {
		t.Fatal(err)
	}
	res = f.judge(t)
	if got, at := f.used(t, f.none); got != UsedPending || at == "" || res.GaveUp != 1 || res.Rows != 2 {
		t.Errorf("vanished entry: used=%d judged_at=%q result=%+v", got, at, res)
	}
}

// The step takes at most MaxSessions sessions and says that more is due.
func TestJudgeDeliveriesIsCappedPerStep(t *testing.T) {
	s := openTest(t)
	base := time.Now().UTC().Add(-3 * time.Hour)
	for i := range 5 {
		key := fmt.Sprintf("capped-%d", i)
		sid, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: key, Scope: scope.Axes{Project: logProject}, AccountID: 7})
		if err != nil {
			t.Fatal(err)
		}
		useAppend(t, s, sid, false, []string{useLine(base.Add(5*time.Second), "assistant", useText("nichts"))}, []string{"assistant"})
		serve(t, s, 7, key, 1)
		if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: key, Harness: "claude", Events: []DeliveryEvent{{
			Call: "c" + key[len(key)-1:], At: base.Format(time.RFC3339), Parts: []deliverylog.Part{{Via: deliverylog.ViaBootstrap, Kind: deliverylog.KindKnowledge, ID: 1, Chars: 5}}}}}, 7); err != nil {
			t.Fatal(err)
		}
	}
	// Entry 1 does not exist here; the step must still cap and close.
	res, err := s.JudgeDeliveries(context.Background(), JudgeOptions{MaxSessions: 2, Now: time.Now().Add(2 * time.Hour)})
	if err != nil || res.Sessions != 2 || !res.More {
		t.Fatalf("first step = %+v %v", res, err)
	}
	for range 2 {
		if _, err := s.JudgeDeliveries(context.Background(), JudgeOptions{MaxSessions: 2, Now: time.Now().Add(2 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if n := logCount(t, s, `SELECT COUNT(*) FROM context_delivery_log WHERE judged_at = ''`); n != 0 {
		t.Errorf("%d rows still open after three steps", n)
	}
}

// The index the search relies on is the one the query uses.
func TestJudgeDeliveriesUsesItsPartialIndex(t *testing.T) {
	s := openTest(t)
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN SELECT d.account_id, d.session_key, se.id, se.harness
		FROM context_delivery_log d JOIN sessions se ON `+sessionOfLog+`
		WHERE `+openJudgeable+` AND d.subagent = 0 AND d.held_out = 0 AND d.delivered_at < ? AND se.last_seen_at < ?
		  AND `+servedByServer+`
		ORDER BY d.delivered_at LIMIT ?`, 1, "x", "x", 10)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "context_delivery_log_open") || strings.Contains(plan, "SCAN se") {
		t.Errorf("plan:\n%s", plan)
	}
}

// Steps that overlap (two servers by mistake, a step that outlived its timeout)
// judge a row once and count it once; the writer queue and the reader pool serve
// them while hooks keep reporting.
func TestJudgeDeliveriesOverlappingStepsCountOnce(t *testing.T) {
	s := openRuntimeTest(t)
	f := newUsageFixture(t, s, false)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := s.JudgeDeliveries(context.Background(), JudgeOptions{Now: time.Now().Add(2 * time.Hour)}); err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: "noise", Harness: "claude", Events: []DeliveryEvent{{
				Call: fmt.Sprintf("noise%d", i), Parts: []deliverylog.Part{{Via: deliverylog.ViaBootstrap, Kind: deliverylog.KindKnowledge, ID: f.none, Chars: 3}}}}}, f.account); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var delivered int
	if err := s.db.QueryRow(`SELECT delivered_sessions FROM knowledge_usage WHERE knowledge_id=?`, f.picked).Scan(&delivered); err != nil || delivered != 1 {
		t.Errorf("delivered_sessions = %d, want 1 (%v)", delivered, err)
	}
}

// ContextUseReport counts judged, picked-up, followed and waiting parts, and
// leaves path descriptions and other kinds unjudged.
func TestContextUseReportCountsByRouteKindAndProfile(t *testing.T) {
	s := openTest(t)
	f := newUsageFixture(t, s, false)
	if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: f.session, Harness: "claude", Events: []DeliveryEvent{{
		Call: "ghost", Profile: "lean", At: f.base.Format(time.RFC3339), Parts: []deliverylog.Part{{Via: deliverylog.ViaPath, Kind: deliverylog.KindGhost, Path: "a/b.go", Chars: 70}}}}}, f.account); err != nil {
		t.Fatal(err)
	}
	before, err := s.ContextUseReport([]string{logProject}, "2020-01-01")
	if err != nil {
		t.Fatal(err)
	}
	var know ContextUseLine
	for _, l := range before.Lines {
		if l.Kind == "knowledge" {
			know = l
		}
	}
	if know.Parts != 3 || know.Pending != 3 || know.Judged != 0 {
		t.Errorf("before judging: %+v", know)
	}
	f.judge(t)
	rep, err := s.ContextUseReport([]string{logProject}, "2020-01-01")
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[string]ContextUseLine{}
	for _, l := range rep.Lines {
		byKind[l.Kind] = l
	}
	k := byKind["knowledge"]
	if k.Parts != 3 || k.Judged != 3 || k.PickedUp != 1 || k.Followed != 1 || k.Pending != 0 || k.Chars != 330 || k.JudgedChars != 330 || k.UsedChars != 100+120 {
		t.Errorf("knowledge: %+v", k)
	}
	if g := byKind["ghost"]; g.Parts != 1 || g.Judged != 0 || g.Pending != 0 || g.Chars != 70 {
		t.Errorf("ghost: %+v", g)
	}
	if len(rep.Profiles) != 1 || rep.Profiles[0].Profile != "full" || rep.Profiles[0].Kind != "knowledge" {
		t.Errorf("profiles: %+v", rep.Profiles)
	}
	if none, _ := s.ContextUseReport(nil, "2020-01-01"); len(none.Lines) != 0 {
		t.Errorf("no projects: %+v", none)
	}
	if later, _ := s.ContextUseReport([]string{logProject}, "2099-01-01"); len(later.Lines) != 0 {
		t.Errorf("a later window: %+v", later)
	}
}

// The job must not be an oracle: a row that names an entry the server never
// delivered to the session (one the reporting account cannot read, a number picked
// freely) is never judged, however much of its title the transcript says, and it
// is closed without a verdict, so nothing about the entry can be read from it.
func TestJudgeDeliveriesDoesNotJudgeWhatTheServerDidNotDeliver(t *testing.T) {
	s := openTest(t)
	f := newUsageFixture(t, s, false)
	secret, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "Wörterbuch lernt sich ein, erst nach neunzig Sekunden", Body: "geheim",
		Scope: scope.Axes{Project: "github.com/other/private"}, Person: "someone"})
	if err != nil {
		t.Fatal(err)
	}
	// The transcript says the secret's title words and its number; the log claims
	// it was delivered, the server's memory has nothing.
	if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: f.session, Harness: "claude", Events: []DeliveryEvent{{
		Call: "guess", At: f.base.Format(time.RFC3339), Parts: []deliverylog.Part{{Via: deliverylog.ViaBootstrap, Kind: deliverylog.KindKnowledge, ID: secret, Chars: 9},
			{Via: deliverylog.ViaRunbook, Kind: deliverylog.KindRunbook, ID: 99, Chars: 9}}}}}, f.account); err != nil {
		t.Fatal(err)
	}
	f.judge(t)
	var used int
	var at string
	if err := s.db.QueryRow(`SELECT COALESCE(MIN(used),-1), COALESCE(MAX(judged_at),'') FROM context_delivery_log WHERE call='guess'`).Scan(&used, &at); err != nil || used != UsedPending || at != "" {
		t.Fatalf("an unserved part was judged: used=%d judged_at=%q %v", used, at, err)
	}
	if n := logCount(t, s, `SELECT COUNT(*) FROM knowledge_usage WHERE knowledge_id=?`, secret); n != 0 {
		t.Error("an unserved entry has usage counts")
	}
	if n := logCount(t, s, `SELECT COUNT(*) FROM knowledge_project_affinity WHERE knowledge_id=?`, secret); n != 0 {
		t.Error("an unserved entry got a project tie")
	}
	// Later the row is closed without a verdict, the same as any that cannot be judged.
	res, err := s.JudgeDeliveries(context.Background(), JudgeOptions{Now: time.Now().Add(15 * 24 * time.Hour)})
	if err != nil || res.GaveUp != 2 {
		t.Fatalf("result = %+v %v", res, err)
	}
	if err := s.db.QueryRow(`SELECT MIN(used), MIN(judged_at) FROM context_delivery_log WHERE call='guess'`).Scan(&used, &at); err != nil || used != UsedPending || at == "" {
		t.Errorf("closed rows: used=%d judged_at=%q %v", used, at, err)
	}
}
