package store

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// The size of the archive the review measured against (REQ-641): 20,000
// sessions, 4,500 entries of which the broad ones carry the ties, 150,000
// evidence rows. The triggers of the project affinity sit on sessions, entries
// and evidence, so what a write costs there is what every upload of the
// collector costs.
const (
	affBenchSessions = 20000
	affBenchEntries  = 4500
	affBenchEvidence = 150000
	affBenchProjects = 40
)

func affinityBenchStore(b *testing.B) *Store {
	b.Helper()
	s, err := Open(filepath.Join(b.TempDir(), "affinity.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close() })
	tx, err := s.db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	ins, err := tx.Prepare(`INSERT INTO sessions(harness, external_id, project, machine, started_at, last_seen_at, public_id) VALUES('claude', ?, ?, 'm1', ?, ?, '')`)
	if err != nil {
		b.Fatal(err)
	}
	ts := now()
	for i := 0; i < affBenchSessions; i++ {
		if _, err := ins.Exec(fmt.Sprintf("bench-%d", i), fmt.Sprintf("github.com/dw/p%d", i%affBenchProjects), ts, ts); err != nil {
			b.Fatal(err)
		}
	}
	ins.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < affBenchEntries; i++ {
		if _, err := s.InsertKnowledge(Knowledge{Type: "pitfall", Title: fmt.Sprintf("entry %d", i), Body: "b", Scope: scope.Axes{Machine: "m1"}, Person: "p",
			OriginContext: &originctx.Context{SessionRef: fmt.Sprintf("bench-%d", (i*4)%affBenchSessions)}}); err != nil {
			b.Fatal(err)
		}
	}
	tx, err = s.db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	ev, err := tx.Prepare(`INSERT OR IGNORE INTO knowledge_evidence(knowledge_id, session_id, chunk_seq, quote) VALUES(?,?,?,'q')`)
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < affBenchEvidence; i++ {
		if _, err := ev.Exec(1+i%affBenchEntries, 1+(i*7)%affBenchSessions, i); err != nil {
			b.Fatal(err)
		}
	}
	ev.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	return s
}

// BenchmarkAffinityTriggers prices the writes the triggers hang on. Run it with
// -benchtime=2000x; "bare" is the same writes with the five triggers dropped.
func BenchmarkAffinityTriggers(b *testing.B) {
	for _, mode := range []string{"triggers", "bare"} {
		b.Run(mode, func(b *testing.B) {
			s := affinityBenchStore(b)
			if mode == "bare" {
				for _, name := range []string{"knowledge_affinity_evidence", "knowledge_affinity_insert", "knowledge_affinity_update",
					"knowledge_affinity_session_insert", "knowledge_affinity_session_update"} {
					if _, err := s.db.Exec(`DROP TRIGGER IF EXISTS ` + name); err != nil {
						b.Fatal(err)
					}
				}
			}
			upsert := func(ext, project string) {
				if _, err := s.UpsertSession(Session{Harness: "claude", ExternalID: ext, Scope: scope.Axes{Project: project, Machine: "m1"}}); err != nil {
					b.Fatal(err)
				}
			}
			b.Run("session-insert", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					upsert(fmt.Sprintf("new-%d", i), fmt.Sprintf("github.com/dw/p%d", i%affBenchProjects))
				}
			})
			b.Run("session-upsert-same-project", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					n := i % affBenchSessions
					upsert(fmt.Sprintf("bench-%d", n), fmt.Sprintf("github.com/dw/p%d", n%affBenchProjects))
				}
			})
			b.Run("session-project-change", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					upsert(fmt.Sprintf("bench-%d", i%affBenchSessions), fmt.Sprintf("github.com/dw/moved%d", i))
				}
			})
			b.Run("evidence-insert", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					if _, err := s.db.Exec(`INSERT OR IGNORE INTO knowledge_evidence(knowledge_id, session_id, chunk_seq, quote) VALUES(?,?,?,'q')`,
						1+i%affBenchEntries, 1+(i*13)%affBenchSessions, affBenchEvidence+i); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("knowledge-insert", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					if _, err := s.InsertKnowledge(Knowledge{Type: "pitfall", Title: fmt.Sprintf("late %d", i), Body: "b", Scope: scope.Axes{Machine: "m1"}, Person: "p",
						OriginContext: &originctx.Context{SessionRef: fmt.Sprintf("bench-%d", i%affBenchSessions)}}); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
