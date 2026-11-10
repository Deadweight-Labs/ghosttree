package store

import (
	"context"
	"database/sql"
	"math/rand/v2"
	"os"
	"sort"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/rawcodec"
	"github.com/Deadweight-Labs/ghosttree/internal/slim"
)

// ChunkStatsOptions steuert ChunkStats.
type ChunkStatsOptions struct {
	// Sample: so viele zufällig gewählte Chunks (Standard 2000).
	Sample int
	// Batch Chunks je Schritt, danach Pause (Standard 100 und 20 ms), damit
	// ein laufender Server nicht merkt, dass jemand zählt.
	Batch int
	Pause time.Duration
	// Seed macht die Stichprobe wiederholbar (0: zufällig).
	Seed uint64
}

// ChunkKindStats ist eine Zeile der Übersicht: ein Zeilentyp (slim.Kind).
type ChunkKindStats struct {
	Kind   string `json:"kind"`
	Chunks int    `json:"chunks"`
	// Bytes ist die Länge der Zeilen im Klartext (entpackt).
	Bytes int64 `json:"bytes"`
	// StoredBytes ist, was die Zeilen in raw belegen, gepackt oder nicht.
	StoredBytes int64 `json:"stored_bytes"`
	// SlimBytes ist, was die Regel aus internal/slim übrig ließe.
	SlimBytes int64 `json:"slim_bytes"`
	// PackedBytes ist, was davon gepackt mit dem neuesten Wörterbuch bliebe:
	// der Stand nach dem Bestandslauf.
	PackedBytes int64 `json:"packed_bytes"`
	// Packed ist die Zahl der Zeilen, die schon gepackt gespeichert sind.
	Packed int `json:"packed"`
}

// ChunkStatsReport ist die Übersicht über eine Stichprobe von session_chunks.
type ChunkStatsReport struct {
	// HighestID ist die größte Chunk-Nummer, eine Schätzung der Anzahl nach
	// oben (der Bestandslauf löscht Zeilen, die ganz wegfallen).
	HighestID   int64 `json:"highest_id"`
	Sampled     int   `json:"sampled"`
	Bytes       int64 `json:"bytes"`
	StoredBytes int64 `json:"stored_bytes"`
	SlimBytes   int64 `json:"slim_bytes"`
	PackedBytes int64 `json:"packed_bytes"`
	Packed      int   `json:"packed"`
	SlimVersion int   `json:"slim_version"`
	// Dict ist die Version des neuesten Wörterbuchs (0: keines).
	Dict     int64            `json:"dict"`
	Kinds    []ChunkKindStats `json:"kinds"`
	Duration time.Duration    `json:"duration_ns"`
}

// ChunkStats zieht eine Stichprobe aus session_chunks und zählt die Bytes von
// raw je Zeilentyp: im Klartext, wie gespeichert (gepackt oder nicht), was die
// aktuelle Regel davon übrig ließe und was davon gepackt bliebe. Jeder
// Chunk ist ein Sprung über die Nummer im B-Baum; durchlaufen wird nichts. Die
// Verbindung schreibt nichts (query_only, bei einer schreibgeschützten Datei
// mode=ro), ein Server darf daneben laufen.
func ChunkStats(ctx context.Context, path string, opts ChunkStatsOptions) (ChunkStatsReport, error) {
	var rep ChunkStatsReport
	abs, err := databaseFilePath(path)
	if err != nil {
		return rep, err
	}
	query := "_pragma=busy_timeout(5000)&_pragma=query_only(1)"
	if f, err := os.OpenFile(abs, os.O_RDWR, 0); err == nil {
		f.Close()
	} else {
		query = "mode=ro&_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", fileURI(abs, query))
	if err != nil {
		return rep, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	return chunkStatsOn(ctx, db, rawStateFor(db), opts)
}

// TranscriptStats ist ChunkStats über die Verbindung des Servers, für die
// Admin-Ansicht.
func (s *Store) TranscriptStats(ctx context.Context, opts ChunkStatsOptions) (ChunkStatsReport, error) {
	if s.reader != nil {
		return s.reader.TranscriptStats(ctx, opts)
	}
	return chunkStatsOn(ctx, s.db, s.rawState(), opts)
}

func chunkStatsOn(ctx context.Context, db *sql.DB, state *rawState, opts ChunkStatsOptions) (ChunkStatsReport, error) {
	rep := ChunkStatsReport{SlimVersion: slim.Version}
	if opts.Sample <= 0 {
		opts.Sample = 2000
	}
	if opts.Batch <= 0 {
		opts.Batch = 100
	}
	if opts.Pause == 0 {
		opts.Pause = 20 * time.Millisecond
	}
	start := time.Now()
	var highest sql.NullInt64
	if err := db.QueryRowContext(ctx, maxChunkIDQuery).Scan(&highest); err != nil {
		return rep, err
	}
	rep.HighestID = highest.Int64
	if rep.HighestID == 0 {
		return rep, nil
	}
	seed := opts.Seed
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	rng := rand.New(rand.NewPCG(seed, seed>>1|1))
	enc := state.encoder(ctx, db)
	rep.Dict = enc.Version()
	stmt, err := db.PrepareContext(ctx, `SELECT raw FROM session_chunks WHERE id >= ? ORDER BY id LIMIT 1`)
	if err != nil {
		return rep, err
	}
	defer stmt.Close()
	kinds := map[string]*ChunkKindStats{}
	for i := 0; i < opts.Sample; i++ {
		if i > 0 && i%opts.Batch == 0 {
			select {
			case <-ctx.Done():
				return rep, ctx.Err()
			case <-time.After(opts.Pause):
			}
		}
		var v rawcodec.Value
		err := stmt.QueryRowContext(ctx, 1+rng.Int64N(rep.HighestID)).Scan(&v)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return rep, err
		}
		raw, err := state.line(ctx, db, v)
		if err != nil {
			return rep, err
		}
		k := slim.Kind(raw)
		ks := kinds[k]
		if ks == nil {
			ks = &ChunkKindStats{Kind: k}
			kinds[k] = ks
		}
		after, packed := 0, 0
		if out, keep := slim.Line(raw); keep {
			after = len(out)
			packed = storedLen(enc.Encode(out))
		}
		stored := v.StoredLen()
		ks.Chunks++
		ks.Bytes += int64(len(raw))
		ks.StoredBytes += int64(stored)
		ks.SlimBytes += int64(after)
		ks.PackedBytes += int64(packed)
		rep.Sampled++
		rep.Bytes += int64(len(raw))
		rep.StoredBytes += int64(stored)
		rep.SlimBytes += int64(after)
		rep.PackedBytes += int64(packed)
		if v.Packed {
			ks.Packed++
			rep.Packed++
		}
	}
	for _, ks := range kinds {
		rep.Kinds = append(rep.Kinds, *ks)
	}
	sort.Slice(rep.Kinds, func(i, j int) bool {
		if rep.Kinds[i].Bytes != rep.Kinds[j].Bytes {
			return rep.Kinds[i].Bytes > rep.Kinds[j].Bytes
		}
		return rep.Kinds[i].Kind < rep.Kinds[j].Kind
	})
	rep.Duration = time.Since(start)
	return rep, nil
}

// storedLen ist die Länge eines Werts von rawcodec.Encoder.Encode.
func storedLen(v any) int {
	switch x := v.(type) {
	case string:
		return len(x)
	case []byte:
		return len(x)
	}
	return 0
}
