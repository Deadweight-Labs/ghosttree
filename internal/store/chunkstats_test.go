package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestChunkStatsCountsBytesPerLineType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.UpsertSession(Session{Harness: "claude-code", ExternalID: "stats"})
	if err != nil {
		t.Fatal(err)
	}
	// So, wie ein älterer Server sie ablegte: mit Kopien.
	legacyChunks(t, st, id, []Chunk{
		{Seq: 0, Raw: `{"type":"user","message":{"role":"user","content":"hello"},"timestamp":"2026-11-01T10:00:00Z"}`},
		{Seq: 1, Raw: `{"type":"api-request-blob","message":{"role":"user","content":"hello"},"timestamp":"2026-11-01T10:00:01Z"}`},
		{Seq: 2, Raw: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hm","signature":"c2lnbmF0dXJlLXNpZ25hdHVyZS1zaWduYXR1cmU="}]},"timestamp":"2026-11-01T10:00:02Z"}`},
	})
	st.Close()

	rep, err := ChunkStats(context.Background(), path, ChunkStatsOptions{Sample: 300, Seed: 7, Pause: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if rep.HighestID != 3 || rep.Sampled != 300 || len(rep.Kinds) != 3 {
		t.Fatalf("report %+v", rep)
	}
	by := map[string]ChunkKindStats{}
	var sum int64
	for _, k := range rep.Kinds {
		by[k.Kind] = k
		sum += k.Bytes
	}
	if sum != rep.Bytes {
		t.Errorf("kinds add up to %d of %d bytes", sum, rep.Bytes)
	}
	if k := by["claude:api-request-blob"]; k.Chunks == 0 || k.SlimBytes != 0 {
		t.Errorf("blob %+v", k)
	}
	if k := by["claude:assistant"]; k.Chunks == 0 || k.SlimBytes >= k.Bytes {
		t.Errorf("assistant %+v", k)
	}
	if k := by["claude:user"]; k.SlimBytes != k.Bytes {
		t.Errorf("user line changed: %+v", k)
	}

	// Nichts geschrieben.
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var n int
	_ = st.db.QueryRow(`SELECT count(*) FROM session_chunks WHERE instr(raw,'api-request-blob')>0`).Scan(&n)
	if n != 1 {
		t.Errorf("stats changed the chunks: %d", n)
	}
}
