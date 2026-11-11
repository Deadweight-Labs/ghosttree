package store

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/deliverylog"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// BenchmarkJudgeDeliveries measures what one judging step costs on a database of
// generated sessions of realistic size (REQ-645): per session, how long reading
// and matching take (outside the write lock) and how long the single write at the
// end holds the lock. Run it once, it builds the database first:
//
//	go test ./internal/store -run '^$' -bench JudgeDeliveries -benchtime 1x
//
// The sessions have 600 lines each: 40 % lines of the assistant (prose of a few
// hundred characters, or a tool call), the rest tool results of 1 to 20 KB and
// prompts, all stored packed in a file database with the runtime store.
func BenchmarkJudgeDeliveries(b *testing.B) {
	const (
		sessions     = 120
		linesPerSess = 600
		entries      = 400
		perSession   = 10
	)
	rnd := rand.New(rand.NewSource(7))
	words := strings.Fields("hook server budget zustellung protokoll transkript sitzung lesepool schreiber queue index sqlite trigger affinität projekt knowledge ledger runbook worker profil briefing pfad datei befehl fehler lösung upgrade schema migration backup rollout")
	phrase := func(n int) string {
		w := make([]string, n)
		for i := range w {
			w[i] = words[rnd.Intn(len(words))]
		}
		return strings.Join(w, " ")
	}
	s, err := OpenRuntime(filepath.Join(b.TempDir(), "judge.db"), DefaultWriterConfig())
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	var ids []int64
	for i := range entries {
		body := phrase(80) + fmt.Sprintf(" Siehe `internal/pkg%d/file%d.go` und `ctx befehl%d --db <pfad>`.", i%20, i, i%9)
		id, err := s.InsertKnowledge(Knowledge{Type: "pitfall", Title: phrase(7), Body: body, Person: "robin"})
		if err != nil {
			b.Fatal(err)
		}
		ids = append(ids, id)
	}
	base := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Second)
	lineBytes := 0
	for n := range sessions {
		key := fmt.Sprintf("bench-%d", n)
		sid, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: key, Scope: scope.Axes{Project: logProject}, AccountID: 7})
		if err != nil {
			b.Fatal(err)
		}
		var chunks []Chunk
		for i := range linesPerSess {
			at := base.Add(time.Duration(i*5) * time.Second)
			var raw, role string
			switch {
			case i%5 == 0:
				raw, role = useLine(at, "user", useText(phrase(12))), "user"
			case i%5 == 1 || i%5 == 3:
				raw, role = useLine(at, "assistant", useText(phrase(40+rnd.Intn(30)))), "assistant"
			case i%5 == 2:
				raw, role = useLine(at, "assistant", useTool("Read", map[string]any{"file_path": fmt.Sprintf("/home/x/repo/internal/pkg%d/file%d.go", rnd.Intn(20), rnd.Intn(400))})), "assistant"
			default:
				raw, role = useLine(at, "user", []map[string]any{{"type": "tool_result", "tool_use_id": "x", "content": phrase(200 + rnd.Intn(3000))}}), "tool"
			}
			lineBytes += len(raw)
			chunks = append(chunks, Chunk{Seq: i, Role: role, Raw: raw})
		}
		if err := s.AppendChunks(sid, chunks); err != nil {
			b.Fatal(err)
		}
		var events []DeliveryEvent
		for d := range perSession {
			id := ids[rnd.Intn(len(ids))]
			serve(b, s, 7, key, id)
			events = append(events, DeliveryEvent{Call: fmt.Sprintf("c%d", d), At: base.Add(time.Duration(d*60) * time.Second).Format(time.RFC3339),
				Parts: []deliverylog.Part{{Via: deliverylog.ViaRelevance, Kind: deliverylog.KindKnowledge, ID: id, Chars: 300}}})
		}
		if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: key, Harness: "claude", Events: events}, 7); err != nil {
			b.Fatal(err)
		}
	}
	b.Logf("database: %d sessions, %d lines, %.1f MB of transcript, %d deliveries", sessions, sessions*linesPerSess, float64(lineBytes)/1e6, sessions*perSession)
	now := time.Now().Add(2 * time.Hour)
	var readTotal, writeTotal time.Duration
	var judged int
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		if _, err := s.db.Exec(`UPDATE context_delivery_log SET used=-1, judged_at=''`); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		for {
			opt := JudgeOptions{Now: now, MaxSessions: DefaultJudgeSessions}
			t0 := time.Now()
			plans, more, err := s.reader.planJudgement(context.Background(), now.UTC(), opt)
			if err != nil {
				b.Fatal(err)
			}
			var vs []judgeVerdict
			for _, p := range plans {
				v, err := s.reader.judgeSession(context.Background(), p)
				if err != nil {
					b.Fatal(err)
				}
				vs = append(vs, v...)
			}
			readTotal += time.Since(t0)
			t1 := time.Now()
			if len(vs) > 0 {
				if _, err := s.applyJudgement(vs, now.UTC().Format(time.RFC3339)); err != nil {
					b.Fatal(err)
				}
			}
			writeTotal += time.Since(t1)
			judged += len(plans)
			if !more {
				break
			}
		}
	}
	b.StopTimer()
	if judged > 0 {
		b.ReportMetric(float64(readTotal.Microseconds())/float64(judged)/1000, "ms-read/session")
		b.ReportMetric(float64(writeTotal.Microseconds())/float64(judged)/1000, "ms-write/session")
		b.ReportMetric(float64(writeTotal.Microseconds())/float64(judged/DefaultJudgeSessions+1)/1000, "ms-lock/step")
	}
}

// BenchmarkPlanJudgementBacklog is the worst case of the search for sessions: a
// large backlog of open rows whose sessions were never uploaded (they wait for
// the give-up age), older than the one session that can be judged. The search
// must not read all of them to find it.
//
//	go test ./internal/store -run '^$' -bench PlanJudgementBacklog -benchtime 20x
func BenchmarkPlanJudgementBacklog(b *testing.B) {
	const lost = 200000
	s, err := Open(filepath.Join(b.TempDir(), "plan.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	old := time.Now().UTC().Add(-5 * 24 * time.Hour).Format(time.RFC3339)
	tx, _ := s.db.Begin()
	ins, _ := tx.Prepare(`INSERT INTO context_delivery_log(account_id, session_key, call, ord, project, harness, via, kind, object_id, chars, delivered_at)
		VALUES(7, ?, '', 0, ?, 'claude', 'bootstrap', 'knowledge', 1, 100, ?)`)
	for i := range lost {
		if _, err := ins.Exec(fmt.Sprintf("lost-%d", i), logProject, old); err != nil {
			b.Fatal(err)
		}
	}
	tx.Commit()
	sid, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "present", Scope: scope.Axes{Project: logProject}, AccountID: 7})
	if err != nil {
		b.Fatal(err)
	}
	_ = sid
	serve(b, s, 7, "present", 1)
	if _, err := s.db.Exec(`INSERT INTO context_delivery_log(account_id, session_key, call, ord, project, harness, via, kind, object_id, chars, delivered_at)
		VALUES(7, 'present', '', 0, ?, 'claude', 'bootstrap', 'knowledge', 1, 100, ?)`, logProject, time.Now().UTC().Add(-4*time.Hour).Format(time.RFC3339)); err != nil {
		b.Fatal(err)
	}
	now := time.Now().Add(2 * time.Hour)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		plans, _, err := s.planJudgement(context.Background(), now.UTC(), JudgeOptions{Settle: time.Hour, MaxSessions: 20})
		if err != nil || len(plans) != 1 {
			b.Fatalf("plans %d %v", len(plans), err)
		}
		if _, err := s.expiredRows(context.Background(), now.UTC(), JudgeOptions{GiveUp: DefaultJudgeGiveUp}); err != nil {
			b.Fatal(err)
		}
	}
}
