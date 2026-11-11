package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Die Lückensuche liest nur die beiden Indizes über (session_id, seq).
func TestRepairIndexGapsReadsOnlyCoveringIndexes(t *testing.T) {
	st := orgStore(t, "robin")
	for q, want := range map[string]string{
		chunkKeysQuery:   "USING COVERING INDEX sqlite_autoindex_session_chunks_1",
		indexedKeysQuery: "USING COVERING INDEX chunk_index_session",
	} {
		rows, err := st.db.Query(`EXPLAIN QUERY PLAN `+q, 1, 1, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		got := strings.Join(plan, "; ")
		if !strings.Contains(got, want) || strings.Contains(got, "TEMP B-TREE") {
			t.Errorf("plan %q, want %q without sorting", got, want)
		}
	}
}

// Ein Binary ohne Index schrieb neben einem mit Index: seine Chunks liegen
// unter der größten indizierten Nummer, der Startvergleich sieht sie nicht,
// die Lückensuche schon.
func TestRepairIndexGapsFindsChunksBelowTheHighestIndexed(t *testing.T) {
	for name, open := range map[string]func(t *testing.T) *Store{
		"direct": func(t *testing.T) *Store { return orgStore(t, "robin") },
		"writer": func(t *testing.T) *Store {
			s, err := Open(filepath.Join(t.TempDir(), "ghosttree.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			return s
		},
	} {
		t.Run(name, func(t *testing.T) {
			st := open(t)
			a, err := st.UpsertSession(Session{Harness: "claude-code", ExternalID: "a-" + name})
			if err != nil {
				t.Fatal(err)
			}
			b, err := st.UpsertSession(Session{Harness: "claude-code", ExternalID: "b-" + name})
			if err != nil {
				t.Fatal(err)
			}
			if err := st.AppendChunks(a, []Chunk{{Seq: 0, Raw: userLine("2026-10-01T10:00:00Z", "first")}}); err != nil {
				t.Fatal(err)
			}
			legacyChunks(t, st, b, transcriptWith("narwhal"))
			if err := st.AppendChunks(a, []Chunk{{Seq: 1, Raw: userLine("2026-10-01T10:01:00Z", "after the old binary")}}); err != nil {
				t.Fatal(err)
			}
			if err := st.startIndexBackfill(); err != nil {
				t.Fatal(err)
			}
			rep, err := st.RepairIndexGaps(context.Background(), IndexGapOptions{Batch: 2, Pause: time.Nanosecond})
			if err != nil {
				t.Fatal(err)
			}
			legacy := len(transcriptWith("narwhal"))
			if rep.Checked != legacy+2 || rep.Missing != legacy || rep.Sessions != 1 || rep.Repaired != legacy {
				t.Errorf("report %+v, want %d missing in 1 session", rep, legacy)
			}
			var total, indexed int
			_ = st.db.QueryRow(`SELECT count(*) FROM session_chunks`).Scan(&total)
			_ = st.db.QueryRow(`SELECT count(*) FROM chunk_index`).Scan(&indexed)
			if total != indexed {
				t.Errorf("indexed %d of %d", indexed, total)
			}
			again, err := st.RepairIndexGaps(context.Background(), IndexGapOptions{Batch: 2, Pause: time.Nanosecond})
			if err != nil || again.Missing != 0 || again.Checked != total {
				t.Errorf("second run %+v err %v", again, err)
			}
		})
	}
}
