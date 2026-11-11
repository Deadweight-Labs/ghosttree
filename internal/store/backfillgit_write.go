package store

import "time"

// sessionCounts sind die Zähler, an denen die Grenzen je Session hängen.
type sessionCounts struct{ events, shas int }

// flush schreibt die gesammelten Vorgänge in einer Transaktion. Die Zähler der Grenzen
// je Session werden davor gelesen: so beginnt die Transaktion mit einem Schreibzugriff,
// und eine Sperre, die ein anderer Schreiber zwischendurch hielt, macht aus ihr keinen
// Lesezustand, der sich nicht mehr hochstufen lässt.
func (r *backfillRun) flush(batch []backfillSession, upTo int64) error {
	if len(batch) == 0 {
		return r.commitCursor(upTo)
	}
	have := make([]sessionCounts, len(batch))
	for i, s := range batch {
		if err := r.db.QueryRowContext(r.ctx, `SELECT COUNT(*) FROM session_git_events WHERE session_id=?`, s.id).Scan(&have[i].events); err != nil {
			return err
		}
		if err := r.db.QueryRowContext(r.ctx, `SELECT COUNT(*) FROM session_commits WHERE session_id=?`, s.id).Scan(&have[i].shas); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		held, res, added, err := r.flushOnce(batch, have, upTo)
		if err == nil {
			total := 0
			for _, s := range batch {
				total += len(s.events)
			}
			r.rep.Recorded += int64(res.Recorded)
			r.rep.Enriched += int64(res.Enriched)
			r.rep.Capped += int64(res.Capped)
			r.rep.ReportedAdded += int64(added)
			r.rep.Known += int64(total - res.Recorded - res.Capped)
			r.rep.LastID = upTo
			r.noteLock(held)
			return r.pause(held)
		}
		if !isBusy(err) || attempt >= len(slimBusyBackoff) {
			return err
		}
		r.rep.BusyRetries++
		if err := r.sleep(slimBusyBackoff[attempt]); err != nil {
			return err
		}
	}
}

// flushOnce schreibt den Stapel. Die Vorgänge sind schon sauber (cleanSessionGitEvent).
// Nur das Schreiben der Vorgänge, gemeldeten SHAs und PRs geschieht hier; der Abgleich
// gegen die PRs folgt in eigenen Transaktionen (settleClaims, matchEvents), damit die
// Sperre kurz bleibt.
func (r *backfillRun) flushOnce(batch []backfillSession, have []sessionCounts, upTo int64) (held time.Duration, res SessionGitResult, added int, err error) {
	tx, err := r.db.BeginTx(r.ctx, nil)
	if err != nil {
		return 0, res, 0, err
	}
	defer tx.Rollback()
	began := time.Now()
	ts := now()
	// Der Abgleich kommt später; was writeGitEvent dafür sammelt, wird verworfen.
	sink := newGitBatch()
	for i, s := range batch {
		sess := &gitSession{id: s.id, project: s.project, branch: s.branch, events: have[i].events, shas: have[i].shas}
		for _, e := range s.events {
			if sess.events >= maxSessionGitEventsPerSession {
				res.Capped++
				continue
			}
			before := sess.shas
			if err := writeGitEvent(tx, sess, s.account, e, ts, sink, &res); err != nil {
				return 0, SessionGitResult{}, 0, err
			}
			added += sess.shas - before
		}
	}
	if err := writeCursor(tx, upTo, r.opts.DryRun); err != nil {
		return 0, SessionGitResult{}, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, SessionGitResult{}, 0, err
	}
	return time.Since(began), res, added, nil
}

// writeCursor hält den Fortschritt fest (nicht im Probelauf). Eine Datei ohne index_state
// braucht ihn nicht: der Lauf setzt dann nur mit AfterID fort.
func writeCursor(q dbTx, upTo int64, dry bool) error {
	if dry {
		return nil
	}
	if _, err := q.Exec(`INSERT OR REPLACE INTO index_state(key, val) VALUES(?, ?)`, backfillGitCursor, upTo); err != nil && !isNoSuchTable(err) {
		return err
	}
	return nil
}

// commitCursor schreibt den Fortschritt allein, wenn ein Abschnitt ohne Vorgänge endet.
func (r *backfillRun) commitCursor(upTo int64) error {
	r.rep.LastID = upTo
	if r.opts.DryRun || upTo == 0 {
		return nil
	}
	for attempt := 0; ; attempt++ {
		_, err := r.db.ExecContext(r.ctx, `INSERT OR REPLACE INTO index_state(key, val) VALUES(?, ?)`, backfillGitCursor, upTo)
		if err == nil || isNoSuchTable(err) {
			return nil
		}
		if !isBusy(err) || attempt >= len(slimBusyBackoff) {
			return err
		}
		r.rep.BusyRetries++
		if err := r.sleep(slimBusyBackoff[attempt]); err != nil {
			return err
		}
	}
}

// noteLock hält eine Schreibtransaktion fest und meldet den Stand.
func (r *backfillRun) noteLock(held time.Duration) {
	r.rep.Batches++
	r.rep.Write += held
	r.locks = append(r.locks, held)
	if r.opts.Progress != nil {
		r.rep.Duration = time.Since(r.start)
		r.opts.Progress(*r.rep)
	}
}

// pause wartet mindestens so lange, wie die Transaktion die Sperre hielt: der Server
// kommt in der Zwischenzeit mindestens so lange zum Schreiben, wie der Lauf es tat.
func (r *backfillRun) pause(held time.Duration) error { return r.sleep(max(r.opts.Pause, held)) }

func (r *backfillRun) sleep(d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-r.ctx.Done():
		return r.ctx.Err()
	case <-t.C:
		return nil
	}
}
