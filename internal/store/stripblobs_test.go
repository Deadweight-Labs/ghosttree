package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/blobstrip"
	"github.com/Deadweight-Labs/ghosttree/internal/transcript"
)

func pixels(n int, seed int64) string {
	raw := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(raw)
	return base64.StdEncoding.EncodeToString(raw)
}

// claudeImageLine ist ein tool_result mit Text und Bild, wie Claude Code es schreibt.
func claudeImageLine(b64 string) string {
	return `{"type":"user","timestamp":"2026-10-19T10:00:00Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"screenshot saved"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]}]}}`
}

func codexImageLine(b64 string) string {
	return `{"timestamp":"2026-10-19T10:00:01Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c1","output":[{"type":"input_text","text":"viewed"},{"type":"input_image","image_url":"data:image/png;base64,` + b64 + `","detail":"high"}]}}`
}

const plainLine = `{"type":"assistant","timestamp":"2026-10-19T10:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"base64 is a transfer encoding"}]}}`

func rawRows(t *testing.T, db *sql.DB) map[int64]string {
	t.Helper()
	rows, err := db.Query(`SELECT id, raw FROM session_chunks ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			t.Fatal(err)
		}
		out[id] = raw
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAppendChunksStoresPlaceholdersInsteadOfImages(t *testing.T) {
	for name, open := range map[string]func(t *testing.T) *Store{
		"direct": openTest,
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
			s := open(t)
			id, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "img-" + name})
			if err != nil {
				t.Fatal(err)
			}
			b64 := pixels(400_000, 1)
			chunks := []Chunk{
				{Seq: 0, Role: "tool", Text: "screenshot saved", Raw: claudeImageLine(b64)},
				{Seq: 1, Role: "", Text: "", Raw: plainLine},
			}
			original := append([]Chunk(nil), chunks...)
			if err := s.AppendChunks(id, chunks); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(chunks, original) {
				t.Fatal("AppendChunks changed the caller's chunks")
			}
			// Dieselben Chunks noch einmal (Neustart des Sammlers) ändern nichts.
			if err := s.AppendChunks(id, chunks); err != nil {
				t.Fatal(err)
			}
			got, err := s.ReadSession(id, 0, 10)
			if err != nil || len(got) != 2 {
				t.Fatalf("chunks %d, err %v", len(got), err)
			}
			if strings.Contains(got[0].Raw, b64[:200]) || len(got[0].Raw) > 2000 {
				t.Fatalf("image was stored: %d bytes", len(got[0].Raw))
			}
			if !json.Valid([]byte(got[0].Raw)) || !strings.Contains(got[0].Raw, "[ghosttree: Bild entfernt, image/png, 400000 Bytes, sha256:") {
				t.Fatalf("no placeholder in %.400s", got[0].Raw)
			}
			if got[0].Text != "screenshot saved" || got[1].Raw != plainLine {
				t.Fatalf("text or plain line changed: %+v", got)
			}
			if raws, err := s.SessionRaw(id); err != nil || len(raws) != 2 || raws[0] != got[0].Raw {
				t.Fatalf("SessionRaw = %v, %v", len(raws), err)
			}
			// Was gesucht und angezeigt wird, kommt aus dem Text und bleibt gleich.
			if before, after := transcript.Parse("claude-code", 0, original[0].Raw), transcript.Parse("claude-code", 0, got[0].Raw); !reflect.DeepEqual(before, after) {
				t.Fatalf("parse differs: %+v vs %+v", before, after)
			}
			var hits int
			if err := s.db.QueryRow(`SELECT count(*) FROM chunks_fts WHERE chunks_fts MATCH 'screenshot'`).Scan(&hits); err != nil || hits != 1 {
				t.Fatalf("fts hits %d, %v", hits, err)
			}
		})
	}
}

func TestAppendChunkBatchesStripsEveryBatchAndForm(t *testing.T) {
	s := openTest(t)
	a, _ := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "batch-a"})
	b, _ := s.UpsertSession(Session{Harness: "codex", ExternalID: "batch-b"})
	b64 := pixels(60_000, 2)
	if err := s.AppendChunkBatches([]ChunkBatch{
		{SessionID: a, Chunks: []Chunk{{Seq: 0, Raw: claudeImageLine(b64)}}},
		{SessionID: b, Chunks: []Chunk{{Seq: 0, Raw: codexImageLine(b64)}, {Seq: 1, Raw: plainLine}}},
	}); err != nil {
		t.Fatal(err)
	}
	for id, row := range rawRows(t, s.db) {
		if strings.Contains(row, b64[:100]) {
			t.Errorf("chunk %d still holds the image", id)
		}
	}
	got, _ := s.ReadSession(b, 0, 10)
	if len(got) != 2 || !strings.Contains(got[0].Raw, `"image_url":"[ghosttree: Bild entfernt, image/png, 60000 Bytes, sha256:`) || !strings.Contains(got[0].Raw, `"detail":"high"`) {
		t.Fatalf("codex chunk: %+v", got)
	}
}

// seedImages legt Zeilen an, wie sie vor dieser Änderung gespeichert wurden:
// unter Umgehung von AppendChunks.
func seedImages(t *testing.T, s *Store, n int) (sessionID int64, withImage int) {
	t.Helper()
	id, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "seed"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		raw, text := plainLine, "plain note"
		switch i % 4 {
		case 0:
			raw, text = claudeImageLine(pixels(5_000+i, int64(i))), "screenshot saved"
			withImage++
		case 1:
			raw = codexImageLine(pixels(3_000+i, int64(i)))
			withImage++
		case 3:
			raw = "not json at all, but it mentions base64 " + strings.Repeat("x", 3000)
		}
		if _, err := s.db.Exec(`INSERT INTO session_chunks(session_id, seq, role, text, raw) VALUES(?,?,?,?,?)`, id, i, "", text, raw); err != nil {
			t.Fatal(err)
		}
	}
	return id, withImage
}

func openSeeded(t *testing.T, n int) (string, *Store, int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	_, withImage := seedImages(t, s, n)
	return path, s, withImage
}

func TestStripSessionBlobsReplacesTheStock(t *testing.T) {
	// Schritte nach Zeilen, nach Bytes (jede Zeile allein) und die Voreinstellung
	// kommen zum selben Ergebnis.
	for name, opts := range map[string]StripBlobsOptions{
		"rows":    {Batch: 3},
		"bytes":   {Batch: 1000, BatchBytes: 1},
		"default": {},
	} {
		t.Run(name, func(t *testing.T) {
			path, s, withImage := openSeeded(t, 40)
			before := rawRows(t, s.db)
			var size int
			for _, r := range before {
				size += len(r)
			}

			dry := opts
			dry.DryRun = true
			dryRep, err := StripSessionBlobs(context.Background(), path, dry)
			if err != nil {
				t.Fatal(err)
			}
			if dryRep.Chunks != 40 || dryRep.Changed != int64(withImage) || dryRep.SavedBytes < 100_000 || dryRep.Candidates < dryRep.Changed {
				t.Fatalf("dry run: %+v, want %d changed", dryRep, withImage)
			}
			if !reflect.DeepEqual(rawRows(t, s.db), before) {
				t.Fatal("a dry run wrote")
			}

			steps := 0
			opts.Progress = func(StripBlobsReport) { steps++ }
			rep, err := StripSessionBlobs(context.Background(), path, opts)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Changed != dryRep.Changed || rep.SavedBytes != dryRep.SavedBytes || rep.Candidates != dryRep.Candidates {
				t.Fatalf("real run %+v differs from dry run %+v", rep, dryRep)
			}
			if name != "default" && steps < 3 {
				t.Fatalf("only %d steps: not walked in small batches", steps)
			}
			after := rawRows(t, s.db)
			now := 0
			for id, raw := range before {
				now += len(after[id])
				if want, _ := blobstrip.Strip(raw); after[id] != want {
					t.Fatalf("chunk %d is not what blobstrip.Strip makes of it", id)
				}
			}
			if int64(size-now) != rep.SavedBytes {
				t.Fatalf("saved %d, real %d", rep.SavedBytes, size-now)
			}

			// Zweiter Lauf: nichts mehr zu tun, nichts ändert sich.
			again, err := StripSessionBlobs(context.Background(), path, StripBlobsOptions{Batch: 5})
			if err != nil {
				t.Fatal(err)
			}
			if again.Changed != 0 || again.SavedBytes != 0 || !reflect.DeepEqual(rawRows(t, s.db), after) {
				t.Fatalf("second run: %+v", again)
			}

			// raw ist in keinem Volltextindex: das UPDATE lässt sie unberührt.
			var hits int
			if err := s.db.QueryRow(`SELECT count(*) FROM chunks_fts WHERE chunks_fts MATCH 'screenshot'`).Scan(&hits); err != nil || hits == 0 {
				t.Fatalf("fts lost rows: %d, %v", hits, err)
			}
			var ok string
			if err := s.db.QueryRow(`PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
				t.Fatalf("integrity_check: %q, %v", ok, err)
			}
			if _, err := s.db.Exec(`INSERT INTO chunks_fts(chunks_fts) VALUES('integrity-check')`); err != nil {
				t.Fatalf("fts integrity-check: %v", err)
			}
		})
	}
}

func TestStripSessionBlobsRunsBesideALiveWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, _ := seedImages(t, s, 120)

	// Der Server schreibt weiter, während der Lauf geht.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var appendErr error
	appended := 0
	running := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for seq := 1000; ; seq++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := s.AppendChunks(id, []Chunk{{Seq: seq, Raw: plainLine, Text: fmt.Sprint("live ", seq)}}); err != nil {
				appendErr = err
				if appended == 0 {
					close(running)
				}
				return
			}
			if appended++; appended == 1 {
				close(running)
			}
		}
	}()
	<-running
	rep, err := StripSessionBlobs(context.Background(), path, StripBlobsOptions{Batch: 10})
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if appendErr != nil {
		t.Fatalf("the live writer failed: %v", appendErr)
	}
	if appended == 0 || rep.Changed == 0 {
		t.Fatalf("appended %d, changed %d: no overlap", appended, rep.Changed)
	}
	for id, raw := range rawRows(t, s.db) {
		if _, saved := blobstrip.Strip(raw); saved != 0 {
			t.Fatalf("chunk %d still has a payload", id)
		}
	}
}

func TestStripSessionBlobsRefusesWhatIsNotAGhosttreeDatabase(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "typo.db")
	if _, err := StripSessionBlobs(context.Background(), missing, StripBlobsOptions{}); err == nil {
		t.Fatal("a missing file was accepted")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("the missing database was created")
	}
	if _, err := StripSessionBlobs(context.Background(), dir, StripBlobsOptions{}); err == nil {
		t.Fatal("a directory was accepted")
	}
	foreign := filepath.Join(dir, "foreign.db")
	db, err := sql.Open("sqlite", foreign)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE other(x)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := StripSessionBlobs(context.Background(), foreign, StripBlobsOptions{}); err == nil || !strings.Contains(err.Error(), "session_chunks") {
		t.Fatalf("foreign database: %v", err)
	}
}

func TestStripSessionBlobsStopsWhenCancelledAndResumes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedImages(t, s, 60)
	ctx, cancel := context.WithCancel(context.Background())
	rep, err := StripSessionBlobs(ctx, path, StripBlobsOptions{Batch: 4, Progress: func(r StripBlobsReport) {
		if r.Changed >= 5 {
			cancel()
		}
	}})
	if err != context.Canceled || rep.Changed < 5 || rep.Changed >= 30 {
		t.Fatalf("cancelled run: %+v, %v", rep, err)
	}
	for id, raw := range rawRows(t, s.db) {
		if id <= rep.LastID {
			if _, saved := blobstrip.Strip(raw); saved != 0 {
				t.Fatalf("chunk %d before the stop still has a payload", id)
			}
		}
	}
	rest, err := StripSessionBlobs(context.Background(), path, StripBlobsOptions{})
	if err != nil || rep.Changed+rest.Changed != 30 {
		t.Fatalf("resume: %+v, %v (first run changed %d)", rest, err, rep.Changed)
	}
}
