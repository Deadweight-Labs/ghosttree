package store

import (
	"database/sql"
	"time"
)

// Der Abgleich des Bestandslaufs: dasselbe, was der Server nach einem PR-Sync fährt
// (matchPullCommits), aber in Scheiben. Eine Transaktion wägt höchstens MatchBatch
// Commits oder gemeldete SHAs ab und hält die Schreibsperre so kurz wie die eines
// Stapels der Route.

// withBackfillTx führt fn in einer Transaktion aus, wiederholt sie bei einer Sperre und
// gibt zurück, wie lange sie gedauert hat.
func (r *backfillRun) withBackfillTx(fn func(tx *sql.Tx) error) error {
	for attempt := 0; ; attempt++ {
		tx, err := r.db.BeginTx(r.ctx, nil)
		if err != nil {
			return err
		}
		began := time.Now()
		err = fn(tx)
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
		if err == nil {
			held := time.Since(began)
			r.noteLock(held)
			return r.pause(held)
		}
		if !isBusy(err) || attempt >= len(slimBusyBackoff) {
			return err
		}
		if err := r.sleep(slimBusyBackoff[attempt]); err != nil {
			return err
		}
	}
}

// settleClaims prüft jede gemeldete oder beobachtete SHA gegen die Commits der PRs:
// eine kurze wird voll, eine, der alle bekannten Commits widersprechen, fällt weg. Der
// Server tut das, wenn eine SHA eintrifft und wenn ein PR synchronisiert wird; der
// Bestand hat beides verpasst.
func (r *backfillRun) settleClaims() error {
	var lastSession int64
	var lastSHA string
	for {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		rows, err := r.db.QueryContext(r.ctx, `SELECT session_id, project, sha FROM session_commits
			WHERE source IN ('reported','observed') AND project != '' AND (session_id, sha) > (?, ?)
			ORDER BY session_id, sha LIMIT ?`, lastSession, lastSHA, r.opts.MatchBatch)
		if err != nil {
			return err
		}
		batch := newGitBatch()
		n := 0
		for rows.Next() {
			var id int64
			var project, sha string
			if err := rows.Scan(&id, &project, &sha); err != nil {
				rows.Close()
				return err
			}
			batch.claimed[project] = append(batch.claimed[project], sha)
			// Eine kurze SHA, die der Abgleich zur vollen verlängert, sortiert danach hinter
			// ihrem alten Schlüssel: mit "g" (hinter jedem Hexzeichen) beginnt die nächste
			// Seite hinter allen ihren Verlängerungen, und sie wird nicht noch einmal gezählt.
			lastSession, lastSHA = id, sha+"g"
			n++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		err = r.withBackfillTx(func(tx *sql.Tx) error {
			return newMatcher(tx, r.owner).settleClaims(batch)
		})
		if err != nil {
			return err
		}
		r.rep.ClaimsChecked += int64(n)
	}
}

// matchEvents legt die Zeitfenster der Commit-Vorgänge ohne SHA über die Commits der PRs.
// Je Stapel von Vorgängen werden die Commits ihrer Fenster ohne Sperre gelesen und dann in
// Scheiben zu MatchBatch aufgelöst, jede in einer eigenen Transaktion. Ein Commit, den ein
// früherer Stapel schon abgewogen hat, kommt nicht noch einmal: alle Vorgänge sind
// geschrieben, bevor der Abgleich beginnt, ein zweites Abwägen änderte nichts.
func (r *backfillRun) matchEvents() error {
	var last int64
	seen := map[string]bool{}
	for {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		rows, err := r.db.QueryContext(r.ctx, `SELECT id, project, started_at, ended_at FROM session_git_events
			WHERE kind='commit' AND ok=1 AND resolved=0 AND project != '' AND id > ? ORDER BY id LIMIT ?`, last, r.opts.MatchBatch)
		if err != nil {
			return err
		}
		windows := map[string][]gitWindow{}
		n := 0
		for rows.Next() {
			var id int64
			var project, from, to string
			if err := rows.Scan(&id, &project, &from, &to); err != nil {
				rows.Close()
				return err
			}
			windows[project] = append(windows[project], gitWindow{from: from, to: to})
			last = id
			n++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		r.rep.EventsMatched += int64(n)
		for project, ws := range windows {
			reader := newMatcher(r.db, r.owner)
			reader.budget = 1 << 30
			commits, err := reader.commitsInWindows(project, ws)
			if err != nil {
				return err
			}
			var todo []pullCommitAt
			for _, c := range commits {
				if k := project + "\x00" + c.SHA; !seen[k] {
					seen[k] = true
					todo = append(todo, c)
				}
			}
			for len(todo) > 0 {
				k := min(r.opts.MatchBatch, len(todo))
				chunk := todo[:k]
				todo = todo[k:]
				err := r.withBackfillTx(func(tx *sql.Tx) error {
					m := newMatcher(tx, r.owner)
					got, err := m.resolveAll(project, chunk)
					if err == nil {
						r.rep.CommitsMatched += int64(got)
					}
					return err
				})
				if err != nil {
					return err
				}
			}
		}
	}
}

// ---------------------------------------------------------------- Abdeckung

// Rang einer Quelle: kleiner ist stärker.
const sqlSourceRank = `CASE sc.source WHEN 'reported' THEN 0 WHEN 'observed' THEN 1 ELSE 2 END`

// coverage zählt, was die Datenbank nach dem Lauf weiß. Nur lesen.
func (r *backfillRun) coverage() error {
	c := &r.rep.Coverage
	if err := r.db.QueryRowContext(r.ctx, `SELECT COUNT(*) FROM sessions WHERE harness IN ('claude-code','codex')`).Scan(&c.Sessions); err != nil {
		return err
	}
	if err := r.db.QueryRowContext(r.ctx, `SELECT COUNT(DISTINCT session_id) FROM session_git_events`).Scan(&c.SessionsWithCalls); err != nil {
		return err
	}
	// Commit-Aufrufe: je erfolgreichem Vorgang die stärkste Quelle der SHA, die an seiner
	// Zeile hängt.
	if err := r.rank(`SELECT (SELECT MIN(`+sqlSourceRank+`) FROM session_commits sc WHERE sc.session_id=e.session_id AND sc.chunk_seq=e.seq),
			COALESCE((SELECT MIN(sc.weak) FROM session_commits sc WHERE sc.session_id=e.session_id AND sc.chunk_seq=e.seq
				AND `+sqlSourceRank+`=2), 0)
		FROM session_git_events e WHERE e.kind='commit' AND e.ok=1`, &c.CommitCalls); err != nil {
		return err
	}
	// Commits in PRs, über die SHA selbst.
	if err := r.rank(`SELECT (SELECT MIN(`+sqlSourceRank+`) FROM session_commits sc WHERE sc.project=k.project AND sc.sha=k.sha),
			COALESCE((SELECT MIN(sc.weak) FROM session_commits sc WHERE sc.project=k.project AND sc.sha=k.sha AND `+sqlSourceRank+`=2), 0)
		FROM (SELECT DISTINCT project, sha FROM git_pr_commits WHERE in_pr=1) k`, &c.CommitsBy); err != nil {
		return err
	}
	c.Commits = c.CommitsBy.With() + c.CommitsBy.None
	if err := r.db.QueryRowContext(r.ctx, `SELECT COUNT(*) FROM session_commits WHERE length(sha) < 40`).Scan(&c.ShortReported); err != nil {
		return err
	}
	return r.pullCoverage()
}

// rank führt eine Abfrage aus, die je Zeile (Rang, schwach) liefert, und zählt sie ein.
func (r *backfillRun) rank(query string, into *BySource) error {
	rows, err := r.db.QueryContext(r.ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var rank sql.NullInt64
		var weak int
		if err := rows.Scan(&rank, &weak); err != nil {
			return err
		}
		if !rank.Valid {
			into.add(-1, false)
			continue
		}
		into.add(int(rank.Int64), weak == 1)
	}
	return rows.Err()
}

// pullCoverage zählt die PRs mit Sessions, so wie die PR-Seite sie zeigt: je PR die
// stärkste Quelle unter den Sessions, die den PR bedient haben (session_prs) oder
// deren Commits in ihm liegen. Ein "created" oder "merged" kommt aus dem Transkript
// und zählt wie reported.
func (r *backfillRun) pullCoverage() error {
	rows, err := r.db.QueryContext(r.ctx, `SELECT project, number, state FROM git_pull_requests`)
	if err != nil {
		return err
	}
	type pr struct {
		project string
		number  int64
		merged  bool
	}
	var prs []pr
	for rows.Next() {
		var p pr
		var state string
		if err := rows.Scan(&p.project, &p.number, &state); err != nil {
			rows.Close()
			return err
		}
		p.merged = state == "merged"
		prs = append(prs, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	c := &r.rep.Coverage
	st := &Store{db: r.db}
	for _, p := range prs {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		set := linkSet{}
		if err := st.sessionsForPull(p.project, p.number, "", set); err != nil {
			return err
		}
		rank, weak := 3, false
		for _, l := range set.list() {
			lr := 2
			for _, src := range l.Sources {
				switch src {
				case CommitSourceReported, PRSourceCreated, PRSourceMerged:
					lr = min(lr, 0)
				case CommitSourceObserved:
					lr = min(lr, 1)
				}
			}
			if lr < rank || (lr == rank && weak && !l.Weak) {
				rank, weak = lr, l.Weak
			}
		}
		c.PRs++
		c.PRsBy.add(rank, weak)
		if p.merged {
			c.MergedPRs++
			c.MergedBy.add(rank, weak)
		}
	}
	return nil
}
