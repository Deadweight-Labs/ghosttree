package store

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// legacySignalSQL is the query SignalReport had before: the prompts of a session
// are counted in a correlated subquery that runs for every signal row.
const legacySignalSQL = `SELECT g.name, g.value, COUNT(*),
	SUM(CASE WHEN se.id IS NOT NULL AND se.last_seen_at < ? THEN 1 ELSE 0 END),
	SUM(CASE WHEN se.id IS NOT NULL AND se.last_seen_at < ? AND
		(SELECT COUNT(*) FROM (SELECT 1 FROM chunk_index ci WHERE ci.session_id=se.id AND ci.is_prompt=1 LIMIT ?)) <= ? THEN 1 ELSE 0 END)
	FROM session_signals g
	LEFT JOIN sessions se ON se.external_id=g.session_ref AND se.project=g.project AND (se.account_id=0 OR se.account_id=g.account_id)
	WHERE g.project IN (?) AND g.first_at >= ?
	GROUP BY g.name, g.value`

// signalFixture fills a store with sessions that are over: each has a few
// prompts and many other chunks (tool calls and answers), which is what makes a
// count of the prompts expensive when it has to read the chunks to find them.
func signalFixture(tb testing.TB, sessions, chunksPer int) *Store {
	tb.Helper()
	s, err := Open(":memory:")
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { s.Close() })
	over := time.Now().UTC().Add(-5 * time.Hour).Format(time.RFC3339)
	filler := strings.Repeat("tool output of some length ", 8)
	tx, _ := s.db.Begin()
	for i := range sessions {
		id := int64(i + 1)
		ref := fmt.Sprintf("sess-%d", i)
		if _, err := tx.Exec(`INSERT INTO sessions(id, harness, external_id, project, started_at, last_seen_at) VALUES(?,?,?,?,?,?)`,
			id, "claude", ref, logProject, over, over); err != nil {
			tb.Fatal(err)
		}
		for j := range chunksPer {
			prompt := 0
			if j < i%5 { // 0 to 4 prompts: most sessions are short
				prompt = 1
			}
			if _, err := tx.Exec(`INSERT INTO chunk_index(chunk_id, session_id, seq, role, is_prompt, text) VALUES(?,?,?,?,?,?)`,
				id*100000+int64(j), id, j, "assistant", prompt, filler); err != nil {
				tb.Fatal(err)
			}
		}
		for _, name := range SessionSignalNames {
			if _, err := tx.Exec(`INSERT INTO session_signals(project, session_ref, account_id, harness, name, value, first_at) VALUES(?,?,?,?,?,?,?)`,
				logProject, ref, 1, "claude", name, fmt.Sprintf("v%d", i%3), over); err != nil {
				tb.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
	return s
}

// The signal report counts the prompts of a session once, however many signal
// rows the session has, and reads the stored chunks through an index.
func TestSignalReportPlanCountsPromptsOncePerSession(t *testing.T) {
	s := signalFixture(t, 20, 30)
	in, _ := inList([]string{logProject})
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN `+signalReportSQL(in), logProject, "2020-01-01", "x", 4, 3, "x")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	text := strings.Join(plan, "\n")
	t.Log("\n" + text)
	if strings.Contains(text, "SCAN ci") || strings.Contains(text, "SCAN chunk_index") {
		t.Errorf("the chunks are scanned, not searched through an index:\n%s", text)
	}
	if !strings.Contains(text, "MATERIALIZE") {
		t.Errorf("the per-session counts are not materialized:\n%s", text)
	}
	if n := strings.Count(text, "chunk_index"); n != 1 {
		t.Errorf("chunk_index appears %d times in the plan, want once:\n%s", n, text)
	}
}

func benchSignal(b *testing.B, query func(in string) (string, []any)) {
	s := signalFixture(b, 400, 1500) // 600,000 chunks, 1,200 signal rows
	in, _ := inList([]string{logProject})
	q, args := query(in)
	b.ResetTimer()
	for range b.N {
		rows, err := s.db.Query(q, args...)
		if err != nil {
			b.Fatal(err)
		}
		n := 0
		for rows.Next() {
			n++
		}
		rows.Close()
		if n == 0 {
			b.Fatal("no lines")
		}
	}
}

func BenchmarkSignalReportLegacy(b *testing.B) {
	settled := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	benchSignal(b, func(in string) (string, []any) {
		return strings.Replace(legacySignalSQL, "IN (?)", "IN ("+in+")", 1), []any{settled, settled, 4, 3, logProject, "2020-01-01"}
	})
}

func BenchmarkSignalReport(b *testing.B) {
	settled := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	benchSignal(b, func(in string) (string, []any) {
		return signalReportSQL(in), []any{logProject, "2020-01-01", settled, 4, 3, settled}
	})
}
