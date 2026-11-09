package store

import (
	"context"
	"time"
)

// IndexGapOptions steuert RepairIndexGaps.
type IndexGapOptions struct {
	// Batch: Einträge je Schritt (Standard 5000), danach Pause (Standard
	// 50 ms), damit Leser und Schreiber Luft haben.
	Batch int
	Pause time.Duration
}

// IndexGapReport sagt, was RepairIndexGaps gefunden und nachgeholt hat.
type IndexGapReport struct {
	Checked  int
	Missing  int
	Sessions int
	Repaired int
}

type chunkKey struct {
	session int64
	seq     int
	id      int64
}

// RepairIndexGaps sucht Chunks ohne Indexzeile im ganzen Bestand und holt sie
// nach. Beim Start vergleicht raiseBackfillBound nur die größten Nummern; eine
// Lücke darunter entstünde nur, wenn ein Binary ohne Index und eines mit Index
// gleichzeitig in dieselbe Datenbank schrieben. Diese Prüfung fängt auch das,
// läuft aber nach dem Start im Hintergrund.
//
// Sie liest nur die beiden Indizes über (session_id, seq), nie raw: den
// Schlüssel von session_chunks und chunk_index_session, die beide die
// Chunk-Nummer tragen. Beide sind nach (session_id, seq) geordnet; jeder
// Schritt nimmt einen Abschnitt des einen und die Indexzeilen desselben
// Abschnitts aus dem anderen. Nachgeholt wird je Session über
// indexSessionBatch, also über die Schreiberwarteschlange.
func (s *Store) RepairIndexGaps(ctx context.Context, opts IndexGapOptions) (IndexGapReport, error) {
	var rep IndexGapReport
	if opts.Batch <= 0 {
		opts.Batch = 5000
	}
	if opts.Pause == 0 {
		opts.Pause = 50 * time.Millisecond
	}
	q := s
	if s.reader != nil {
		q = s.reader
	}
	var afterSession int64 = -1
	afterSeq := -1
	gaps := map[int64]int{}
	var order []int64
	for {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		batch, err := q.chunkKeys(ctx, chunkKeysQuery, afterSession, afterSeq, opts.Batch)
		if err != nil {
			return rep, err
		}
		if len(batch) == 0 {
			break
		}
		first, last := batch[0], batch[len(batch)-1]
		indexed, err := q.indexedIDs(ctx, first, last)
		if err != nil {
			return rep, err
		}
		for _, k := range batch {
			if !indexed[k.id] {
				if gaps[k.session] == 0 {
					order = append(order, k.session)
				}
				gaps[k.session]++
				rep.Missing++
			}
		}
		rep.Checked += len(batch)
		afterSession, afterSeq = last.session, last.seq
		if len(batch) < opts.Batch {
			break
		}
		select {
		case <-ctx.Done():
			return rep, ctx.Err()
		case <-time.After(opts.Pause):
		}
	}
	rep.Sessions = len(order)
	for _, id := range order {
		for {
			if err := ctx.Err(); err != nil {
				return rep, err
			}
			n, more, err := s.indexSessionBatch(id)
			if err != nil {
				return rep, err
			}
			rep.Repaired += n
			if !more {
				break
			}
		}
	}
	return rep, nil
}

// Die Abfragen der Lückensuche. Beide laufen über einen deckenden Index
// (Test im Abfrageplan); raw wird nie gelesen.
const (
	chunkKeysQuery = `SELECT session_id, seq, id FROM session_chunks
		WHERE (session_id, seq) > (?, ?) ORDER BY session_id, seq LIMIT ?`
	indexedKeysQuery = `SELECT chunk_id FROM chunk_index
		WHERE (session_id, seq) >= (?, ?) AND (session_id, seq) <= (?, ?)`
)

func (s *Store) chunkKeys(ctx context.Context, query string, afterSession int64, afterSeq, limit int) ([]chunkKey, error) {
	rows, err := s.db.QueryContext(ctx, query, afterSession, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []chunkKey
	for rows.Next() {
		var k chunkKey
		if err := rows.Scan(&k.session, &k.seq, &k.id); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) indexedIDs(ctx context.Context, first, last chunkKey) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx, indexedKeysQuery, first.session, first.seq, last.session, last.seq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
