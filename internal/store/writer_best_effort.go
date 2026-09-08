package store

import (
	"math"
	"strings"
)

const (
	bestEffortDelivery = iota
	bestEffortSearch
	bestEffortMachine
	bestEffortKinds
	bestEffortMaxKeys       = 4096
	bestEffortMaxBytes      = 1 << 20
	bestEffortUsageKeyBytes = 128
)

type bestEffortBatch struct {
	usage    map[int64]int64
	machines map[string]struct{}
	bytes    int64
	active   bool
}

func (w *runtimeWriter) coalesceUsage(ks []Knowledge, search bool) {
	kind := bestEffortDelivery
	if search {
		kind = bestEffortSearch
	}
	if !w.mu.TryLock() {
		w.bestEffortDrops[kind].Add(uint64(len(ks)))
		return
	}
	defer w.mu.Unlock()
	if len(ks) > bestEffortMaxKeys {
		w.bestEffortDrops[kind].Add(uint64(len(ks) - bestEffortMaxKeys))
		ks = ks[:bestEffortMaxKeys]
	}
	batch := &w.bestEffort[kind]
	seen := make(map[int64]struct{}, min(len(ks), bestEffortMaxKeys))
	for _, k := range ks {
		if _, ok := seen[k.ID]; ok {
			continue
		}
		if w.closed || batch.active {
			w.bestEffortDrops[kind].Add(1)
			continue
		}
		count, exists := batch.usage[k.ID]
		if count == math.MaxInt64 || (!exists && (len(batch.usage) >= bestEffortMaxKeys || batch.bytes > bestEffortMaxBytes-bestEffortUsageKeyBytes)) {
			w.bestEffortDrops[kind].Add(1)
			continue
		}
		if batch.usage == nil {
			batch.usage = make(map[int64]int64)
		}
		if !exists {
			batch.bytes += bestEffortUsageKeyBytes
		}
		batch.usage[k.ID] = count + 1
		seen[k.ID] = struct{}{}
	}
	w.ready.Signal()
}

func (w *runtimeWriter) coalesceMachine(hostname string) {
	if !w.mu.TryLock() {
		w.bestEffortDrops[bestEffortMachine].Add(1)
		return
	}
	defer w.mu.Unlock()
	batch := &w.bestEffort[bestEffortMachine]
	if w.closed || batch.active {
		w.bestEffortDrops[bestEffortMachine].Add(1)
		return
	}
	if len(hostname) > bestEffortMaxBytes {
		w.bestEffortDrops[bestEffortMachine].Add(1)
		return
	}
	if _, exists := batch.machines[hostname]; exists {
		return
	}
	if len(batch.machines) >= bestEffortMaxKeys {
		w.bestEffortDrops[bestEffortMachine].Add(1)
		return
	}
	size := int64(len(hostname)) + 128
	if size > bestEffortMaxBytes-batch.bytes {
		w.bestEffortDrops[bestEffortMachine].Add(1)
		return
	}
	if batch.machines == nil {
		batch.machines = make(map[string]struct{})
	}
	batch.machines[strings.Clone(hostname)] = struct{}{}
	batch.bytes += size
	w.ready.Signal()
}

func (w *runtimeWriter) nextBestEffort() int {
	for kind := range bestEffortKinds {
		if w.bestEffort[kind].bytes > 0 {
			return kind
		}
	}
	return -1
}

func (w *runtimeWriter) runBestEffort(kind int, batch bestEffortBatch) {
	err := w.bestEffortWrite(kind, batch)
	w.mu.Lock()
	if err != nil {
		if kind == bestEffortMachine {
			w.bestEffortDrops[kind].Add(uint64(len(batch.machines)))
		} else {
			for _, count := range batch.usage {
				w.bestEffortDrops[kind].Add(uint64(count))
			}
		}
	}
	w.bestEffort[kind] = bestEffortBatch{}
	w.mu.Unlock()
}

func (s *Store) writeBestEffort(kind int, batch bestEffortBatch) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at := now()
	if kind == bestEffortMachine {
		stmt, err := tx.Prepare(`INSERT INTO machines(hostname,first_seen,last_seen) VALUES(?,?,?) ON CONFLICT(hostname) DO UPDATE SET last_seen=excluded.last_seen`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for hostname := range batch.machines {
			if _, err := stmt.Exec(hostname, at, at); err != nil {
				return err
			}
		}
	} else {
		stmt, err := tx.Prepare(`UPDATE knowledge SET last_used_at=?,hit_count=hit_count+?,search_hits=search_hits+? WHERE id=?`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for id, count := range batch.usage {
			var searches int64
			if kind == bestEffortSearch {
				searches = count
			}
			if _, err := stmt.Exec(at, count, searches, id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
