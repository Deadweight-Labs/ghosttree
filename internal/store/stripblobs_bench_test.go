package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// BenchmarkStripSessionBlobs misst, wie schnell der Bestandslauf Chunks mit
// Bildern bearbeitet: 10.000 Zeilen zu je rund 100 KB (1 GB Rohdaten), jede
// vierte ohne Bild. Die Datenbank wird je Durchlauf neu aufgebaut.
//
//	go test ./internal/store -run xxx -bench StripSessionBlobs -benchtime 1x
func BenchmarkStripSessionBlobs(b *testing.B) {
	const rows = 10_000
	images := make([]string, 8)
	for i := range images {
		images[i] = claudeImageLine(pixels(75_000, int64(i)))
	}
	text := plainLine
	for n := 0; n < b.N; n++ {
		b.StopTimer()
		path := filepath.Join(b.TempDir(), "ghosttree.db")
		s, err := Open(path)
		if err != nil {
			b.Fatal(err)
		}
		id, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "bench"})
		if err != nil {
			b.Fatal(err)
		}
		seed(b, s.db, id, rows, func(i int) string {
			if i%4 == 3 {
				return text
			}
			return images[i%len(images)]
		})
		if err := s.Close(); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		rep, err := StripSessionBlobs(context.Background(), path, StripBlobsOptions{})
		if err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		if rep.Changed != rows*3/4 {
			b.Fatalf("changed %d", rep.Changed)
		}
		secs := b.Elapsed().Seconds()
		b.ReportMetric(float64(rep.Changed)/secs, "chunks/s")
		b.ReportMetric(float64(rep.SavedBytes)/secs/(1<<20), "MiB/s")
		b.ReportMetric(float64(rep.SavedBytes)/(1<<20), "MiB-saved")
	}
}

func seed(b *testing.B, db *sql.DB, session int64, n int, line func(i int) string) {
	b.Helper()
	for from := 0; from < n; from += 500 {
		tx, err := db.Begin()
		if err != nil {
			b.Fatal(err)
		}
		for i := from; i < from+500 && i < n; i++ {
			if _, err := tx.Exec(`INSERT INTO session_chunks(session_id, seq, role, text, raw) VALUES(?,?,?,?,?)`, session, i, "", fmt.Sprint("chunk ", i), line(i)); err != nil {
				b.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			b.Fatal(err)
		}
	}
}
