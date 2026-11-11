package store

import (
	"database/sql"
	"errors"
	"log"
	"sort"
	"strings"
	"time"
)

// Nachricht und Push-Bereich als Signale für Sessions an Commits (IDEA-31, P6).
//
// Zwei Angaben im Transkript sagen, welche Session einen Commit erzeugt hat, auch wenn
// die SHA fehlt (git commit -q):
//
//   - message: der Aufruf git commit -m "…" trägt die Nachricht. Der Collector meldet nur ihren
//     Hash (gitmsg, Titelzeile und Titel mit Rumpf); der Server legt ihn gegen die Schlüssel der
//     Commits der PRs (git_pr_commits.title_key, msg_key). Dazu passt das Zeitfenster: die
//     Commit-Zeit liegt im Fenster des Aufrufs (genau), oder, bei Amend und Rebase, später (dann nur,
//     wenn dieselbe Session zu dieser Zeit selbst einen umschreibenden Aufruf hat, oder die
//     Patch-ID den Commit an sie bindet).
//   - pushed: die Ausgabe von git push nennt den Bereich old..new. Die Commits des PRs auf
//     diesem Branch zwischen old (ohne) und new (mit) hat die Session gepusht. Die Gegenprobe:
//     die Zahl ihrer erfolgreichen Commit-Aufrufe seit dem letzten Push muss zur Zahl der
//     Commits im Bereich passen, dann gehören sie in der Reihenfolge zusammen. Aufrufe, die
//     schon eine Nachricht einem Commit zugeordnet hat, scheiden vorher aus (die Nachricht hat
//     Vorrang). Ein Force-Push gilt nur für den Kopf des Bereichs.
//
// Stärke. Beide Signale kommen aus dem Client, und wer einen PR lesen darf, kennt Nachricht und
// Bereich seiner Commits: sie lassen sich erfinden. Darum bleibt "weak" dem Autor-Abgleich
// (KN-4499, fit) vorbehalten, für ein Signal allein wie für beide: stark ist nur, wessen
// bewiesenes GitHub-Konto der Autor ist; nicht prüfbar heißt schwach; ein anderer bekannter
// Autor schließt aus. Das Zusammentreffen beider Signale ist die stärkere QUELLE
// (message+pushed vor message vor pushed vor matched): sie entscheidet, wer einen Commit
// bekommt, wenn mehrere Sessions in Frage kommen, und sie zählt der Bericht für sich. Wie die
// gemeldeten SHAs legt sie ihre Zeile NEBEN die anderer Sessions und löscht nie eine fremde
// (Kaperschutz, #242); nur eine pushed-Zeile einer Session desselben Kontos weicht einer
// message-Zeile, damit das Ergebnis nicht von der Reihenfolge abhängt, in der die Vorgänge
// eintrafen.

// Signale und die Quellen, in denen sie stehen.
const (
	CommitSourceMessage       = "message"
	CommitSourcePushed        = "pushed"
	CommitSourceMessagePushed = "message+pushed"
)

// SourceNames zerlegt den Wert einer Quelle in die Namen, unter denen die Anzeige sie
// führt ("message+pushed" sind zwei).
func SourceNames(source string) []string {
	if strings.Contains(source, "+") {
		return strings.Split(source, "+")
	}
	return []string{source}
}

// sqlEvidenceSources: die Quellen, mit denen ein Vorgang seine Antwort hat.
const sqlEvidenceSources = `'reported','observed','message','pushed','message+pushed'`

const (
	// maxEvidencePushes begrenzt, wie viele Pushes eines Branches ein PR-Sync prüft.
	maxEvidencePushes = 60
	// maxLaterMessageAge: so lange nach dem Aufruf kann die Patch-ID einen umgeschriebenen
	// Commit (Rebase, Amend) noch an den ursprünglichen Aufruf binden.
	maxLaterMessageAge = 30 * 24 * time.Hour
)

// evidenceEvent ist ein Vorgang, dessen Nachricht oder Push-Bereich der Abgleich prüft.
type evidenceEvent struct {
	session int64
	seq     int
	kind    string
	title   string // commit: Schlüssel der Titelzeile
	branch  string // push: Branch auf der Gegenseite
	// op, from und to: bei einem Commit-Vorgang der Befehl und sein Zeitfenster. Ein umschreibender
	// Befehl (amend, rebase …) lässt die Commits in seinem Fenster neu abwägen.
	op       string
	from, to string
}

// rewritingOp: der Befehl schreibt Commits um, deren Nachricht ein früherer Aufruf trug.
func rewritingOp(op string) bool {
	return op == "amend" || op == "rebase" || op == "cherry-pick" || op == "am"
}

// evidenceCommit ist ein Commit eines PRs samt den Schlüsseln seiner Nachricht.
type evidenceCommit struct {
	pullCommitAt
	title, full string
}

// spend nimmt eine Einheit Arbeit vom Deckel des Laufs.
func (m *matcher) spend(project string) bool {
	if m.budget <= 0 || time.Now().After(m.deadline) {
		if !m.stopped {
			log.Printf("session git evidence for %s stopped at its work limit; the rest follows with the next sync", project)
		}
		m.stopped = true
		return false
	}
	m.budget--
	return true
}

// ------------------------------------------------------------ Einstiege

// matchEvidence gleicht die Vorgänge eines Stapels mit Nachricht oder Push-Bereich ab und gibt
// zurück, wie viele Commits dabei einer Session zugeordnet wurden.
func (m *matcher) matchEvidence(b *gitBatch) (int, error) {
	total := 0
	for project, events := range b.evidence {
		seenCommit := map[string]bool{}
		seenPush := map[[2]int64]bool{}
		for _, e := range events {
			switch e.kind {
			case GitEventCommit:
				if e.title != "" {
					commits, err := m.commitsByTitle(project, e.title)
					if err != nil {
						return total, err
					}
					for _, c := range commits {
						if seenCommit[c.SHA] {
							continue
						}
						seenCommit[c.SHA] = true
						n, err := m.resolveEvidence(project, c)
						if err != nil {
							return total, err
						}
						total += n
					}
				}
				// Ein Befehl, der Commits umschreibt: die Commits in seinem Fenster tragen die Nachricht
				// eines früheren Aufrufs.
				if rewritingOp(e.op) {
					commits, err := m.commitsKeyedInWindow(project, e.from, e.to)
					if err != nil {
						return total, err
					}
					for _, c := range commits {
						if seenCommit[c.SHA] {
							continue
						}
						seenCommit[c.SHA] = true
						n, err := m.resolveEvidence(project, c)
						if err != nil {
							return total, err
						}
						total += n
					}
				}
				// Der Push, der auf diesen Aufruf folgt, zählt ihn jetzt mit.
				var next sql.NullInt64
				if err := m.q.QueryRow(`SELECT MIN(seq) FROM session_git_pushes WHERE session_id=? AND seq > ?`, e.session, e.seq).Scan(&next); err != nil {
					return total, err
				}
				if next.Valid {
					key := [2]int64{e.session, next.Int64}
					if !seenPush[key] {
						seenPush[key] = true
						n, err := m.resolvePush(project, e.session, int(next.Int64))
						if err != nil {
							return total, err
						}
						total += n
					}
				}
			case GitEventPush:
				key := [2]int64{e.session, int64(e.seq)}
				if seenPush[key] {
					continue
				}
				seenPush[key] = true
				n, err := m.resolvePush(project, e.session, e.seq)
				if err != nil {
					return total, err
				}
				total += n
			}
		}
	}
	return total, nil
}

// matchEvidenceCommits gleicht nach einem PR-Sync ab: Commits, die jetzt bekannt sind,
// gegen die Nachrichten der Vorgänge, und die Pushes auf den Branches ihrer PRs gegen deren
// Commits.
func (m *matcher) matchEvidenceCommits(project string, commits []pullCommitAt) (int, error) {
	total := 0
	heads := map[string]bool{}
	for _, c := range commits {
		ec, ok, err := m.evidenceCommitOf(project, c)
		if err != nil {
			return total, err
		}
		if !ok {
			continue
		}
		n, err := m.resolveEvidence(project, ec)
		if err != nil {
			return total, err
		}
		total += n
		hs, err := m.pullHeads(project, c.SHA)
		if err != nil {
			return total, err
		}
		for h := range hs {
			heads[h] = true
		}
	}
	branches := make([]string, 0, len(heads))
	for h := range heads {
		branches = append(branches, h)
	}
	sort.Strings(branches)
	for _, branch := range branches {
		pushes, err := m.pushesOnBranch(project, branch)
		if err != nil {
			return total, err
		}
		for _, p := range pushes {
			n, err := m.resolvePush(project, p[0], int(p[1]))
			if err != nil {
				return total, err
			}
			total += n
		}
	}
	return total, nil
}

// evidenceCommitOf liest die Schlüssel eines Commits, den die PR-Sync-Liste nennt.
func (m *matcher) evidenceCommitOf(project string, c pullCommitAt) (evidenceCommit, bool, error) {
	ec := evidenceCommit{pullCommitAt: c}
	err := m.q.QueryRow(`SELECT title_key, msg_key FROM git_pr_commits WHERE project=? AND sha=? AND title_key != '' LIMIT 1`, project, c.SHA).Scan(&ec.title, &ec.full)
	if errors.Is(err, sql.ErrNoRows) {
		return ec, false, nil
	}
	return ec, err == nil, err
}

// commitsKeyedInWindow nennt die Commits der PRs mit Schlüsseln, deren Zeit im Fenster liegt.
func (m *matcher) commitsKeyedInWindow(project, from, to string) ([]evidenceCommit, error) {
	f, ok1 := parseGitTime(from)
	t, ok2 := parseGitTime(to)
	if !ok1 || !ok2 {
		return nil, nil
	}
	rows, err := m.q.Query(`SELECT sha, committed_at, author, title_key, msg_key FROM git_pr_commits
		WHERE project=? AND committed_at >= ? AND committed_at <= ? AND title_key != '' GROUP BY sha LIMIT ?`,
		project, f.Add(-gitMatchSlack).Format(time.RFC3339), t.Add(gitMatchSlack).Format(time.RFC3339), maxWindowCommits)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []evidenceCommit
	for rows.Next() {
		var c evidenceCommit
		var at string
		if err := rows.Scan(&c.SHA, &at, &c.Author, &c.title, &c.full); err != nil {
			return nil, err
		}
		if tm, ok := parseGitTime(at); ok {
			c.CommittedAt = tm
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

// commitsByTitle nennt die Commits der PRs, deren Titel diesen Schlüssel hat.
func (m *matcher) commitsByTitle(project, title string) ([]evidenceCommit, error) {
	rows, err := m.q.Query(`SELECT sha, committed_at, author, title_key, msg_key FROM git_pr_commits
		WHERE project=? AND title_key=? AND committed_at != '' GROUP BY sha LIMIT ?`, project, title, maxWindowCommits)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []evidenceCommit
	for rows.Next() {
		var c evidenceCommit
		var at string
		if err := rows.Scan(&c.SHA, &at, &c.Author, &c.title, &c.full); err != nil {
			return nil, err
		}
		t, ok := parseGitTime(at)
		if !ok {
			continue
		}
		c.CommittedAt = t
		out = append(out, c)
	}
	return out, rows.Err()
}

// numbers liest eine Spalte von Zahlen.
func (m *matcher) numbers(query string, args ...any) ([]int64, error) {
	rows, err := m.q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (m *matcher) pullHeads(project, sha string) (map[string]bool, error) {
	rows, err := m.q.Query(sqlPullHeads, project, sha)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	heads := map[string]bool{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		heads[h] = true
	}
	return heads, rows.Err()
}

// pushesOnBranch nennt die Pushes (Session, Zeilennummer), die den Branch verändert haben,
// die jüngsten zuerst und begrenzt.
func (m *matcher) pushesOnBranch(project, branch string) ([][2]int64, error) {
	rows, err := m.q.Query(`SELECT DISTINCT session_id, seq FROM session_git_pushes WHERE project=? AND branch=? ORDER BY session_id DESC, seq DESC LIMIT ?`,
		project, branch, maxEvidencePushes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]int64
	for rows.Next() {
		var s, q int64
		if err := rows.Scan(&s, &q); err != nil {
			return nil, err
		}
		out = append(out, [2]int64{s, q})
	}
	return out, rows.Err()
}

// ------------------------------------------------------------ Nachricht

// messageEvent ist ein Commit-Vorgang, dessen Nachricht zu einem Commit passt.
type messageEvent struct {
	id       int64
	session  int64
	account  int64
	seq      int
	op       string
	branch   string
	started  time.Time
	ended    time.Time
	full     string
	lastSeen time.Time
	exact    bool
}

const sqlMessageEvents = `SELECT e.id, e.session_id, CASE WHEN e.account_id!=0 THEN e.account_id WHEN s.account_id=0 THEN ? ELSE s.account_id END,
		e.seq, e.op, COALESCE(NULLIF(e.branch,''), s.branch), e.started_at, e.ended_at, e.msg_full, s.last_seen_at
	FROM session_git_events e JOIN sessions s ON s.id=e.session_id
	WHERE e.project=? AND e.msg_title=? AND e.started_at >= ? AND e.started_at <= ? AND e.kind='commit' AND e.ok=1
	LIMIT ?`

// resolveEvidence legt die Nachricht eines Commits der PRs über die Vorgänge, die sie
// getragen haben, und schreibt die Zuordnung (Quelle message, oder message+pushed, wenn der
// Push-Bereich derselben Session sie schon stützt). Gibt zurück, wie viele Zeilen neu sind.
func (m *matcher) resolveEvidence(project string, c evidenceCommit) (int, error) {
	if c.title == "" || !m.spend(project) {
		return 0, nil
	}
	// Ein Aufruf, der älter ist als maxLaterMessageAge, kann den Commit auch umgeschrieben nicht mehr
	// meinen; so bleibt die Menge klein, auch wenn ein Titel oft vorkommt ("ci: retrigger").
	lo := c.CommittedAt.Add(-maxLaterMessageAge).Format(time.RFC3339)
	hi := c.CommittedAt.Add(gitMatchSlack).Format(time.RFC3339)
	rows, err := m.q.Query(sqlMessageEvents, m.owner, project, c.title, lo, hi, maxWindowCandidates+1)
	if err != nil {
		return 0, err
	}
	var cands []messageEvent
	for rows.Next() {
		var e messageEvent
		var started, ended, seen string
		if err := rows.Scan(&e.id, &e.session, &e.account, &e.seq, &e.op, &e.branch, &started, &ended, &e.full, &seen); err != nil {
			rows.Close()
			return 0, err
		}
		var ok1, ok2 bool
		e.started, ok1 = parseGitTime(started)
		e.ended, ok2 = parseGitTime(ended)
		e.lastSeen, _ = parseGitTime(seen)
		if ok1 && ok2 {
			cands = append(cands, e)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if len(cands) > maxWindowCandidates {
		return 0, nil // zu viele Vorgänge mit derselben Nachricht: der Titel sagt nichts
	}
	// Zeit und Rumpf.
	var timed []messageEvent
	for _, e := range cands {
		// Dieselbe Titelzeile, aber ein anderer Rumpf: ein anderer Commit.
		if c.full != "" && e.full != "" && c.full != e.full {
			continue
		}
		switch {
		case !c.CommittedAt.Before(e.started.Add(-gitMatchSlack)) && !c.CommittedAt.After(e.ended.Add(gitMatchSlack)):
			e.exact = true
		case c.CommittedAt.After(e.ended.Add(gitMatchSlack)) && (e.op == "" || e.op == OpCommitName || e.op == "amend"):
			// Später entstanden: ein Amend, Rebase oder Cherry-Pick desselben Commits. Gilt, wenn
			// dieselbe Session selbst im Fenster dieser Zeit einen solchen Aufruf hat, oder wenn eine
			// Patch-ID den Commit an die Session bindet (ein anderer hat ihn rebaset). Ohne das wäre
			// jeder spätere Commit mit demselben Titel ("ci: retrigger") ein Treffer.
			if c.CommittedAt.Sub(e.started) > maxLaterMessageAge {
				continue
			}
			rewrote, err := m.rewroteAt(e.session, c.CommittedAt)
			if err != nil {
				return 0, err
			}
			if !rewrote {
				linked, err := m.patchLinked(project, e.session, c.SHA)
				if err != nil {
					return 0, err
				}
				if !linked {
					continue
				}
			}
		default:
			continue
		}
		timed = append(timed, e)
	}
	if len(timed) == 0 {
		return 0, nil
	}
	// Wer zählt: Autor-Abgleich (stark vor schwach), eine Session mit ihrem besten Vorgang.
	type cand struct {
		ev  messageEvent
		lvl fitLevel
	}
	best := map[int64]cand{}
	strong := false
	for _, e := range timed {
		lvl, err := m.fit(e.account, c.Author)
		if err != nil {
			return 0, err
		}
		if lvl == fitNone {
			continue
		}
		if cur, ok := best[e.session]; ok && !betterMessageEvent(e, cur.ev) {
			continue
		}
		best[e.session] = cand{e, lvl}
	}
	for _, b := range best {
		strong = strong || b.lvl == fitStrong
	}
	var field []cand
	for _, b := range best {
		if strong && b.lvl != fitStrong {
			continue
		}
		field = append(field, b)
	}
	if len(field) == 0 {
		return 0, nil
	}
	sort.Slice(field, func(i, j int) bool { return field[i].ev.session < field[j].ev.session })
	winner := field[0]
	if len(field) > 1 {
		// Mehrere Sessions mit derselben Nachricht (ein Orchestrator und sein Worker, zwei
		// Sessions auf einem Branch): erst die, deren Fenster die Zeit trifft, dann die, deren
		// Branch der des PRs ist; bleibt mehr als eine, entscheidet die Nachricht nichts.
		var exact []cand
		for _, f := range field {
			if f.ev.exact {
				exact = append(exact, f)
			}
		}
		if len(exact) > 0 {
			field = exact
		}
		if len(field) > 1 {
			heads, err := m.pullHeads(project, c.SHA)
			if err != nil {
				return 0, err
			}
			var onBranch []cand
			for _, f := range field {
				if f.ev.branch != "" && heads[f.ev.branch] {
					onBranch = append(onBranch, f)
				}
			}
			if len(onBranch) > 0 {
				field = onBranch
			}
		}
		if len(field) != 1 {
			return 0, nil
		}
		winner = field[0]
	}
	// Hat die Session eine SHA für diesen Aufruf gemeldet oder beobachtet, ist die
	// Antwort schon da; putEvidence lässt eine solche Zeile stehen.
	added, err := m.putEvidence(project, winner.ev.session, c, CommitSourceMessage, winner.ev.seq, winner.ev.op, winner.lvl != fitStrong)
	if err != nil {
		return 0, err
	}
	if _, err := m.q.Exec(`UPDATE session_git_events SET resolved=1 WHERE id=?`, winner.ev.id); err != nil {
		return 0, err
	}
	// Eine pushed-Zeile einer anderen Session desselben Kontos weicht der Nachricht.
	if err := m.dropOutrankedPushed(project, c.SHA, winner.ev.session, winner.ev.account); err != nil {
		return 0, err
	}
	return added, nil
}

// OpCommitName ist der Wert von op für einen gewöhnlichen Commit.
const OpCommitName = "commit"

// betterMessageEvent: der genaue Treffer vor dem späteren, dann der frühere Vorgang.
func betterMessageEvent(a, b messageEvent) bool {
	if a.exact != b.exact {
		return a.exact
	}
	if !a.started.Equal(b.started) {
		return a.started.Before(b.started)
	}
	return a.id < b.id
}

// rewroteAt sagt, ob die Session zu dieser Zeit einen Aufruf hatte, der Commits umschreibt.
func (m *matcher) rewroteAt(session int64, at time.Time) (bool, error) {
	var one int
	err := m.q.QueryRow(`SELECT 1 FROM session_git_events WHERE session_id=? AND kind='commit' AND ok=1
		AND op IN ('amend','rebase','cherry-pick','am') AND started_at <= ? AND ended_at >= ? LIMIT 1`,
		session, at.Add(gitMatchSlack).Format(time.RFC3339), at.Add(-gitMatchSlack).Format(time.RFC3339)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// patchLinked sagt, ob die Session eine Zeile mit einer Patch-ID hat, die auch der Commit
// trägt (Reflog, P2): dieselbe Änderung, nach Rebase oder Amend unter anderer SHA.
func (m *matcher) patchLinked(project string, session int64, sha string) (bool, error) {
	var one int
	err := m.q.QueryRow(`SELECT 1 FROM session_commits a JOIN session_commits b ON b.project=a.project AND b.patch_id=a.patch_id
		WHERE a.session_id=? AND a.patch_id != '' AND b.project=? AND b.sha=? LIMIT 1`, session, project, sha).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ------------------------------------------------------------ Push-Bereich

// pushRef ist eine Zeile von session_git_pushes.
type pushRef struct {
	n         int
	src       string
	old, new  string
	newBranch bool
	forced    bool
}

// commitCall ist ein Commit-Vorgang, der für die Gegenprobe zählt.
type commitCall struct {
	id      int64
	seq     int
	op      string
	branch  string
	started time.Time
	ended   time.Time
	title   string
	full    string
}

// resolvePush ordnet die Commits eines Push-Bereichs der Session zu, die ihn gepusht hat.
func (m *matcher) resolvePush(project string, session int64, seq int) (int, error) {
	if !m.spend(project) {
		return 0, nil
	}
	var started, ended string
	var acct int64
	var sessBranch string
	err := m.q.QueryRow(`SELECT e.started_at, e.ended_at, CASE WHEN e.account_id!=0 THEN e.account_id WHEN s.account_id=0 THEN ? ELSE s.account_id END, s.branch
		FROM session_git_events e JOIN sessions s ON s.id=e.session_id
		WHERE e.session_id=? AND e.seq=? AND e.kind='push'`, m.owner, session, seq).Scan(&started, &ended, &acct, &sessBranch)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	pushEnd, ok := parseGitTime(ended)
	if !ok {
		return 0, nil
	}
	rows, err := m.q.Query(`SELECT n, branch, src, old, new, new_branch, forced FROM session_git_pushes WHERE session_id=? AND seq=? ORDER BY n`, session, seq)
	if err != nil {
		return 0, err
	}
	type branchRef struct {
		branch string
		pushRef
	}
	var refs []branchRef
	for rows.Next() {
		var r branchRef
		var nb, fo int
		if err := rows.Scan(&r.n, &r.branch, &r.src, &r.old, &r.new, &nb, &fo); err != nil {
			rows.Close()
			return 0, err
		}
		r.newBranch, r.forced = nb == 1, fo == 1
		refs = append(refs, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	total := 0
	for _, r := range refs {
		numbers, err := m.numbers(`SELECT number FROM git_pull_requests WHERE project=? AND head_ref=? ORDER BY number`, project, r.branch)
		if err != nil {
			return total, err
		}
		for _, pr := range numbers {
			n, err := m.resolvePushRange(project, session, acct, seq, pushEnd, r.pushRef, r.branch, pr)
			if err != nil {
				return total, err
			}
			total += n
		}
	}
	return total, nil
}

const sqlPRCommitsOrdered = `SELECT sha, committed_at, author, title_key, msg_key FROM git_pr_commits
	WHERE project=? AND pr=? AND in_pr=1 AND committed_at != '' ORDER BY position`

func (m *matcher) prCommits(project string, pr int64) ([]evidenceCommit, error) {
	rows, err := m.q.Query(sqlPRCommitsOrdered, project, pr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []evidenceCommit
	for rows.Next() {
		var c evidenceCommit
		var at string
		if err := rows.Scan(&c.SHA, &at, &c.Author, &c.title, &c.full); err != nil {
			return nil, err
		}
		t, ok := parseGitTime(at)
		if !ok {
			continue
		}
		c.CommittedAt = t
		out = append(out, c)
	}
	return out, rows.Err()
}

// prefixIndex nennt den Index des Commits, dessen SHA mit dem Präfix beginnt; -1, wenn keiner
// oder mehr als einer passt.
func prefixIndex(commits []evidenceCommit, prefix string) int {
	idx := -1
	for i, c := range commits {
		if strings.HasPrefix(c.SHA, prefix) {
			if idx >= 0 {
				return -1
			}
			idx = i
		}
	}
	return idx
}

// resolvePushRange wendet einen Ref eines Push auf die Commits eines PRs an.
func (m *matcher) resolvePushRange(project string, session, acct int64, pushSeq int, pushEnd time.Time, r pushRef, branch string, pr int64) (int, error) {
	commits, err := m.prCommits(project, pr)
	if err != nil || len(commits) == 0 {
		return 0, err
	}
	lo, hi := 0, -1
	switch {
	case r.new != "":
		hi = prefixIndex(commits, r.new)
		if hi < 0 {
			return 0, nil // der Kopf ist nicht (mehr) im PR
		}
		if r.forced {
			lo = hi // ein Force-Push gilt nur für den Kopf
			break
		}
		oldIdx := prefixIndex(commits, r.old)
		if r.old == "" || oldIdx < 0 || oldIdx >= hi {
			return 0, nil
		}
		lo = oldIdx + 1
	case r.newBranch:
		for i, c := range commits {
			if !c.CommittedAt.After(pushEnd.Add(gitMatchSlack)) {
				hi = i
			}
		}
		if hi < 0 {
			return 0, nil
		}
	default:
		return 0, nil
	}
	rangeCommits := commits[lo : hi+1]
	calls, valid, err := m.callsBeforePush(session, pushSeq, r.src, branch)
	if err != nil {
		return 0, err
	}
	// Die Commit-Aufrufe, wie sie als Commits ankamen: ein Amend ersetzt den Commit davor.
	effective := effectiveCalls(calls)
	used := map[int]bool{}  // Index in effective
	paired := map[int]int{} // Index im Bereich -> Index in effective
	// 1. Nachricht vor Push-Bereich.
	for ri, c := range rangeCommits {
		if c.title == "" {
			continue
		}
		for ei, call := range effective {
			if used[ei] || call.title != c.title {
				continue
			}
			if call.full != "" && c.full != "" && call.full != c.full {
				continue
			}
			if !fitsCallWindow(c, call) {
				continue
			}
			used[ei] = true
			paired[ri] = ei
			break
		}
	}
	// 2. Der Rest der Reihe nach, wenn die Zahlen passen (Gegenprobe).
	if !r.forced && valid {
		var restCommits, restCalls []int
		for ri := range rangeCommits {
			if _, ok := paired[ri]; !ok {
				restCommits = append(restCommits, ri)
			}
		}
		for ei := range effective {
			if !used[ei] {
				restCalls = append(restCalls, ei)
			}
		}
		if len(restCommits) > 0 && len(restCommits) == len(restCalls) {
			ok := true
			for i, ri := range restCommits {
				call := effective[restCalls[i]]
				c := rangeCommits[ri]
				if !fitsCallWindow(c, call) || (call.title != "" && c.title != "" && call.title != c.title) {
					ok = false
					break
				}
			}
			if ok {
				for i, ri := range restCommits {
					paired[ri] = restCalls[i]
				}
			}
		}
	}
	if r.forced {
		// Der Kopf nach einem Force-Push: der letzte Aufruf (auch Rebase, Amend), dessen
		// Fenster die Commit-Zeit enthält.
		c := rangeCommits[0]
		for ei := len(calls) - 1; ei >= 0; ei-- {
			if fitsCallWindow(c, calls[ei]) {
				effective = calls
				paired[0] = ei
				break
			}
		}
	}
	total := 0
	for ri, ei := range paired {
		c := rangeCommits[ri]
		call := effective[ei]
		lvl, err := m.fit(acct, c.Author)
		if err != nil {
			return total, err
		}
		if lvl == fitNone {
			continue
		}
		// Die Nachricht hat Vorrang: hat eine andere Session diesen Commit über seine
		// Nachricht, und diese Session nicht, bleibt er der anderen.
		hasOwn, err := m.hasMessageRow(session, project, c.SHA)
		if err != nil {
			return total, err
		}
		if !hasOwn && call.title != "" && call.title == c.title {
			hasOwn = true // die Nachricht dieser Session stimmt; resolveEvidence schreibt sie
		}
		if !hasOwn {
			other, err := m.otherSessionHasMessage(project, c.SHA, session)
			if err != nil {
				return total, err
			}
			if other {
				continue
			}
		}
		added, err := m.putEvidence(project, session, c, CommitSourcePushed, call.seq, call.op, lvl != fitStrong)
		if err != nil {
			return total, err
		}
		total += added
		if _, err := m.q.Exec(`UPDATE session_git_events SET resolved=1 WHERE id=?`, call.id); err != nil {
			return total, err
		}
	}
	return total, nil
}

// fitsCallWindow: der Commit entstand im Fenster des Aufrufs.
func fitsCallWindow(c evidenceCommit, call commitCall) bool {
	return !c.CommittedAt.Before(call.started.Add(-gitMatchSlack)) && !c.CommittedAt.After(call.ended.Add(gitMatchSlack))
}

const sqlCallsBeforePush = `SELECT id, seq, op, COALESCE(NULLIF(branch,''), ?), started_at, ended_at, msg_title, msg_full
	FROM session_git_events WHERE session_id=? AND kind='commit' AND ok=1 AND seq > ? AND seq < ? ORDER BY seq`

// callsBeforePush nennt die erfolgreichen Commit-Aufrufe der Session seit ihrem letzten Push
// und vor diesem, auf dem Branch des Push (src oder Branch auf der Gegenseite; ein Aufruf
// ohne Branch zählt mit). valid ist false, wenn ein Befehl dabei ist, dessen Zahl an Commits
// sich nicht aus dem Aufruf ergibt (Rebase, Cherry-Pick, Merge, am, revert).
func (m *matcher) callsBeforePush(session int64, pushSeq int, src, branch string) ([]commitCall, bool, error) {
	var sessBranch string
	if err := m.q.QueryRow(`SELECT branch FROM sessions WHERE id=?`, session).Scan(&sessBranch); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	// Der letzte Push davor: einer, der etwas verändert hat, oder ohne Ausgabe (-q) erfolgreich war.
	prev := -1
	err := m.q.QueryRow(`SELECT COALESCE(MAX(seq), -1) FROM session_git_events e WHERE session_id=? AND kind='push' AND seq < ?
		AND (ok=1 OR EXISTS (SELECT 1 FROM session_git_pushes p WHERE p.session_id=e.session_id AND p.seq=e.seq))`, session, pushSeq).Scan(&prev)
	if err != nil {
		return nil, false, err
	}
	rows, err := m.q.Query(sqlCallsBeforePush, sessBranch, session, prev, pushSeq)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []commitCall
	valid := true
	for rows.Next() {
		var c commitCall
		var s, e string
		if err := rows.Scan(&c.id, &c.seq, &c.op, &c.branch, &s, &e, &c.title, &c.full); err != nil {
			return nil, false, err
		}
		var ok1, ok2 bool
		c.started, ok1 = parseGitTime(s)
		c.ended, ok2 = parseGitTime(e)
		if !ok1 || !ok2 {
			continue
		}
		if c.branch != "" && c.branch != src && c.branch != branch {
			continue
		}
		if c.op != "" && c.op != "commit" && c.op != "amend" {
			valid = false
		}
		out = append(out, c)
	}
	return out, valid, rows.Err()
}

// effectiveCalls macht aus den Aufrufen die Commits, die sie hinterlassen: ein commit legt
// einen an, ein amend ersetzt den letzten (sein Fenster und seine Nachricht gelten dann). Ein
// amend ohne Commit davor ändert einen Commit, der schon gepusht war; er trägt nichts bei.
func effectiveCalls(calls []commitCall) []commitCall {
	var out []commitCall
	for _, c := range calls {
		switch c.op {
		case "amend":
			if len(out) == 0 {
				continue
			}
			if c.title == "" {
				c.title, c.full = out[len(out)-1].title, out[len(out)-1].full
			}
			out[len(out)-1] = c
		case "", "commit":
			out = append(out, c)
		}
	}
	return out
}

// ------------------------------------------------------------ Schreiben

// hasMessageRow: die Session hat für den Commit schon eine Zeile aus der Nachricht.
func (m *matcher) hasMessageRow(session int64, project, sha string) (bool, error) {
	var one int
	err := m.q.QueryRow(`SELECT 1 FROM session_commits WHERE session_id=? AND sha=? AND source IN ('message','message+pushed')`, session, sha).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// otherSessionHasMessage: eine andere Session hat den Commit über seine Nachricht.
func (m *matcher) otherSessionHasMessage(project, sha string, session int64) (bool, error) {
	var one int
	err := m.q.QueryRow(`SELECT 1 FROM session_commits WHERE project=? AND sha=? AND session_id != ? AND source IN ('message','message+pushed') LIMIT 1`,
		project, sha, session).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// putEvidence schreibt, dass ein Signal die Session an den Commit bindet. Eine Zeile derselben
// Session bleibt, wie sie ist, wenn sie stärker ist (reported, observed); matched wird durch das
// Signal ersetzt; message und pushed zusammen ergeben message+pushed. Eine kürzere SHA derselben
// Session wird dabei durch die volle ersetzt (putSessionCommit).
func (m *matcher) putEvidence(project string, session int64, c evidenceCommit, signal string, seq int, op string, weak bool) (int, error) {
	added, err := m.putEvidenceRow(project, session, c, signal, seq, op, weak)
	if err != nil || weak {
		return added, err
	}
	// Ein starker Kandidat ersetzt eine schwache Zuordnung, auch die eines anderen Kontos
	// (KN-4499): schwach hält nie gegen stark.
	return added, m.dropWeakOthers(project, c.SHA, session)
}

func (m *matcher) putEvidenceRow(project string, session int64, c evidenceCommit, signal string, seq int, op string, weak bool) (int, error) {
	var src string
	var curWeak int
	err := m.q.QueryRow(`SELECT source, weak FROM session_commits WHERE session_id=? AND sha=?`, session, c.SHA).Scan(&src, &curWeak)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if op == "" {
			op = "commit"
		}
		added, err := putSessionCommit(m.q, sessionCommit{
			SessionID: session, Project: project, SHA: c.SHA, Source: signal, Op: op, ChunkSeq: seq,
			CommittedAt: c.CommittedAt.Format(time.RFC3339), Weak: weak,
		}, now())
		if err != nil {
			return 0, err
		}
		if added {
			m.evAdded++
			return 1, nil
		}
		return 0, nil
	case err != nil:
		return 0, err
	}
	next := src
	switch src {
	case CommitSourceMatched:
		next = signal
	case CommitSourceMessage:
		if signal == CommitSourcePushed {
			next = CommitSourceMessagePushed
		}
	case CommitSourcePushed:
		if signal == CommitSourceMessage {
			next = CommitSourceMessagePushed
		}
	}
	if next == src {
		return 0, nil
	}
	newWeak := min(curWeak, boolInt(weak))
	if src == CommitSourceMatched {
		newWeak = boolInt(weak)
	}
	_, err = m.q.Exec(`UPDATE session_commits SET source=?, weak=?, chunk_seq=CASE WHEN ?='matched' THEN ? ELSE chunk_seq END WHERE session_id=? AND sha=?`,
		next, newWeak, src, seq, session, c.SHA)
	if err == nil {
		m.evUpgraded++
	}
	return 0, err
}

// dropWeakOthers entfernt die schwachen Zuordnungen anderer Sessions zu diesem Commit, die
// auf einem Signal oder einem Zeitfenster beruhen: der Autor selbst hat die Session gefunden.
func (m *matcher) dropWeakOthers(project, sha string, keep int64) error {
	return m.dropRows(`SELECT session_id FROM session_commits WHERE project=? AND sha=? AND session_id != ? AND weak=1
		AND source IN ('message','pushed','message+pushed','matched')`, project, sha, keep)
}

// dropRows löscht die Zeilen von session_commits (Session, sha) der Sessions, die die Abfrage
// nennt (Argumente: project, sha, ...), und stellt den Stand "gelöst" ihrer Vorgänge wieder her.
func (m *matcher) dropRows(query string, args ...any) error {
	rows, err := m.q.Query(query, args...)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	sha := args[1]
	for _, id := range ids {
		if _, err := m.q.Exec(`DELETE FROM session_commits WHERE session_id=? AND sha=?`, id, sha); err != nil {
			return err
		}
		if _, err := m.q.Exec(`UPDATE session_git_events SET resolved = EXISTS(
				SELECT 1 FROM session_commits sc WHERE sc.session_id=session_git_events.session_id AND sc.chunk_seq=session_git_events.seq)
			WHERE session_id=? AND kind='commit'`, id); err != nil {
			return err
		}
	}
	return nil
}

// dropOutrankedPushed entfernt die pushed-Zeilen anderer Sessions desselben Kontos für
// diesen Commit: die Nachricht hat Vorrang vor dem Push-Bereich, und so hängt das Ergebnis
// nicht davon ab, welches Signal zuerst ankam. Fremde Konten behalten ihre Zeile.
func (m *matcher) dropOutrankedPushed(project, sha string, winner, acct int64) error {
	return m.dropRows(`SELECT sc.session_id FROM session_commits sc JOIN sessions s ON s.id=sc.session_id
		WHERE sc.project=? AND sc.sha=? AND sc.source='pushed' AND sc.session_id != ?
		AND (CASE WHEN s.account_id=0 THEN ? ELSE s.account_id END)=?`, project, sha, winner, m.owner, acct)
}
