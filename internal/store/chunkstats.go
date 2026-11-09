package store

import (
	"context"
	"database/sql"
	"math/rand/v2"
	"os"
	"sort"
	"time"

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
	Bytes  int64  `json:"bytes"`
	// SlimBytes ist, was die Regel aus internal/slim übrig ließe.
	SlimBytes int64 `json:"slim_bytes"`
}

// ChunkStatsReport ist die Übersicht über eine Stichprobe von session_chunks.
type ChunkStatsReport struct {
	// HighestID ist die größte Chunk-Nummer; Chunks werden nie gelöscht, sie
	// ist also eine gute Schätzung der Anzahl.
	HighestID   int64            `json:"highest_id"`
	Sampled     int              `json:"sampled"`
	Bytes       int64            `json:"bytes"`
	SlimBytes   int64            `json:"slim_bytes"`
	SlimVersion int              `json:"slim_version"`
	Kinds       []ChunkKindStats `json:"kinds"`
	Duration    time.Duration    `json:"duration_ns"`
}

// ChunkStats zieht eine Stichprobe aus session_chunks und zählt die Bytes von
// raw je Zeilentyp, dazu, was die aktuelle Regel davon übrig ließe. Jeder
// Chunk ist ein Sprung über die Nummer im B-Baum; durchlaufen wird nichts. Die
// Verbindung schreibt nichts (query_only, bei einer schreibgeschützten Datei
// mode=ro), ein Server darf daneben laufen.
func ChunkStats(ctx context.Context, path string, opts ChunkStatsOptions) (ChunkStatsReport, error) {
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
		var raw string
		err := stmt.QueryRowContext(ctx, 1+rng.Int64N(rep.HighestID)).Scan(&raw)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return rep, err
		}
		k := slim.Kind(raw)
		ks := kinds[k]
		if ks == nil {
			ks = &ChunkKindStats{Kind: k}
			kinds[k] = ks
		}
		after := 0
		if out, keep := slim.Line(raw); keep {
			after = len(out)
		}
		ks.Chunks++
		ks.Bytes += int64(len(raw))
		ks.SlimBytes += int64(after)
		rep.Sampled++
		rep.Bytes += int64(len(raw))
		rep.SlimBytes += int64(after)
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
