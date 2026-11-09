package store

import (
	"context"
	"strings"
	"testing"
)

// Der Startvergleich darf session_chunks nicht durchlaufen: auf einer großen
// Datenbank hielt das den Start minutenlang auf (PAIN-55).
func TestRaiseBackfillBoundUsesNoTableScan(t *testing.T) {
	st := orgStore(t, "robin")
	// Die Min/Max-Optimierung zeigt sich als ein einziges SEARCH ohne
	// Bereich; die frühere Abfrage las als SEARCH … (rowid>?) jeden Chunk
	// hinter der Grenze.
	for q, table := range map[string]string{maxChunkIDQuery: "session_chunks", maxIndexedChunkQuery: "chunk_index"} {
		rows, err := st.db.Query(`EXPLAIN QUERY PLAN ` + q)
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
		if got := strings.Join(plan, "; "); got != "SEARCH "+table {
			t.Errorf("%s: plan %q, want %q", q, got, "SEARCH "+table)
		}
	}
}

// Auf Prod steht die Grenze weit hinter dem Bestand: alles danach schrieb
// dasselbe Binary mit Index. Ein Start lässt den Lauf dann fertig.
func TestRaiseBackfillBoundKeepsFinishedRunWhenEverythingIsIndexed(t *testing.T) {
	st := orgStore(t, "robin")
	s := addSession(t, st, "old", 1, "", "m", "", nil)
	legacyChunks(t, st, s.ID, transcriptWith("narwhal"))
	resetBackfill(t, st)
	if err := st.RunIndexBackfill(context.Background(), BackfillOptions{Batch: 2}); err != nil {
		t.Fatal(err)
	}
	// Neue Chunks über den regulären Weg haben ihre Indexzeile.
	if err := st.AppendChunks(s.ID, []Chunk{{Seq: 50, Raw: userLine("2026-10-01T11:00:00Z", "indexed on write")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE index_state SET val=0 WHERE key='backfill_bound'`); err != nil {
		t.Fatal(err)
	}
	if err := st.startIndexBackfill(); err != nil {
		t.Fatal(err)
	}
	if !st.IndexProgress().Done {
		t.Fatal("a start with every chunk indexed restarted the backfill")
	}
	var bound int64
	_ = st.db.QueryRow(`SELECT val FROM index_state WHERE key='backfill_bound'`).Scan(&bound)
	if bound != 0 {
		t.Errorf("bound moved to %d without missing chunks", bound)
	}
}
