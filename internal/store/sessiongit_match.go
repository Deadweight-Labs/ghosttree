package store

import (
	"database/sql"
	"errors"
	"log"
	"sort"
	"strings"
	"time"
)

// Abgleich von Zeitfenstern und gemeldeten SHAs gegen die Commits der PRs.
//
// Alles, was ein Client über eine Session meldet (Zeitfenster, Branch, SHAs), ist
// eine Angabe, die niemand prüfen kann, der das Repo nicht hat. Der Server hat von
// den Commits nur, was GitHub über die PRs liefert: SHA, Zeit und den GitHub-Login
// des Autors. Daraus entsteht das Modell, das KN-4493 begründet:
//
//   - Der Autor eines Commits wird gegen das GitHub-Konto des meldenden Kontos
//     gelegt (account_github). Ein bewiesenes Konto (github_id > 0), dessen Login
//     der Autor ist, macht den Kandidaten STARK. Ein anderer bekannter Login scheidet
//     den Kandidaten aus: der Commit stammt nicht von dieser Person. Lässt sich
//     nichts vergleichen (der Commit hat keinen Login, das Konto keine Verknüpfung,
//     die Verknüpfung ist nicht bewiesen), ist der Kandidat SCHWACH.
//   - Gibt es starke Kandidaten, entscheiden nur sie. Schwache nehmen nicht teil, und
//     eine schwache Zuordnung, die schon besteht, hält nie gegen einen starken
//     Kandidaten: das spätere echte Ereignis des Autors ersetzt sie.
//   - Eine schwache Zuordnung ist gekennzeichnet (weak) und zeigt sich so. Schwache
//     Zuordnungen verschiedener Konten verdrängen sich nicht (Regel 4 von
//     resolveCommit): wer zuerst kommt, behält die seine; sie gilt aber nie als Beleg.

// fitLevel sagt, wie gut der Autor eines Commits zum Konto einer Session passt.
type fitLevel int

const (
	fitNone   fitLevel = iota // der Autor ist bekannt und eine andere Person
	fitWeak                   // nicht zu prüfen
	fitStrong                 // das bewiesene GitHub-Konto des Kontos ist der Autor
)

// ghLogin ist die GitHub-Verknüpfung eines Kontos.
type ghLogin struct {
	login    string
	verified bool
	linked   bool
}

// dbTx ist, was *sql.DB und *sql.Tx gemeinsam können.
type dbTx interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
}

// dbQuerier ist die lesende Hälfte davon.
type dbQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// matcher legt Fenster und gemeldete SHAs über die Commits der PRs. Er hält, was
// sich innerhalb eines Laufs nicht ändert (Konten, GitHub-Logins), und die
// Obergrenzen der Arbeit.
type matcher struct {
	q        dbTx
	owner    int64
	logins   map[int64]ghLogin
	byLogin  map[string]int64
	deadline time.Time
	budget   int
	stopped  bool
}

func newMatcher(q dbTx, owner int64) *matcher {
	return &matcher{
		q: q, owner: owner,
		logins:   map[int64]ghLogin{},
		byLogin:  map[string]int64{},
		deadline: time.Now().Add(maxMatchTime),
		budget:   maxResolvePerRun,
	}
}

// accountLogin liest die GitHub-Verknüpfung eines Kontos (0 = Altbestand, gehört dem
// Instanz-Owner).
func (m *matcher) accountLogin(acct int64) (ghLogin, error) {
	if acct == 0 {
		acct = m.owner
	}
	if l, ok := m.logins[acct]; ok {
		return l, nil
	}
	var l ghLogin
	var id int64
	err := m.q.QueryRow(`SELECT login, github_id FROM account_github WHERE account_id=?`, acct).Scan(&l.login, &id)
	switch {
	case err == nil:
		l.linked, l.verified = true, id > 0
	case !errors.Is(err, sql.ErrNoRows):
		return ghLogin{}, err
	}
	m.logins[acct] = l
	return l, nil
}

// verifiedAccount nennt das Konto, dessen bewiesenes GitHub-Konto dieser Login ist
// (0, wenn es keines gibt).
func (m *matcher) verifiedAccount(login string) (int64, error) {
	key := strings.ToLower(login)
	if id, ok := m.byLogin[key]; ok {
		return id, nil
	}
	var id int64
	err := m.q.QueryRow(`SELECT account_id FROM account_github WHERE github_id>0 AND lower(login)=?`, key).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	m.byLogin[key] = id
	return id, nil
}

// fit legt den Autor eines Commits (GitHub-Login, leer = unbekannt) gegen das Konto.
// Ein Eintrag ohne Konto (0, Altbestand ohne Spalte) ist nicht zu prüfen.
func (m *matcher) fit(acct int64, author string) (fitLevel, error) {
	if author == "" || acct == 0 {
		return fitWeak, nil
	}
	l, err := m.accountLogin(acct)
	if err != nil {
		return fitNone, err
	}
	switch {
	case !l.linked:
		return fitWeak, nil
	case !strings.EqualFold(l.login, author):
		return fitNone, nil
	case l.verified:
		return fitStrong, nil
	}
	return fitWeak, nil
}

// ---------------------------------------------------- gemeldete SHAs

// settleReported prüft die gemeldeten SHAs gegen Commits, die der Server aus den
// PRs kennt, und verlängert kurze zu vollen.
//
// Eine gemeldete SHA ist eine Behauptung des Collectors. Kennt der Server die
// Commits, die zu ihr passen, muss mindestens einer zum Zeitfenster des Vorgangs
// passen, der sie gemeldet hat UND von einem Autor stammen, der zum Konto der
// Session passt: ein Commit, der Stunden vor oder nach dem Aufruf entstand oder
// von einer anderen Person geschrieben wurde, kann nicht aus diesem Aufruf
// stammen. Widersprechen ALLE, wird die Zeile verworfen (ab zwölf Zeichen; eine
// kürzere bleibt, ist aber für die Leser unsichtbar, solange sie widerspricht), und
// eine erfundene SHA nimmt keinem anderen einen Commit. Passt genau einer, wird
// eine kurze SHA durch die volle ersetzt (Quelle bleibt reported); passen mehrere
// (ein Präfix von sieben Zeichen ist nicht eindeutig), bleibt sie, wie sie ist. Ein
// Commit, der nur denselben Präfix trägt, verwirft nie die Zeile einer Session,
// deren Commit ein anderer ist.
//
// commits sind die Commits, die gerade bekannt geworden sind; geprüft werden die
// Zeilen, die zu einem von ihnen passen, gegen ALLE Commits mit ihrem Präfix.
func (m *matcher) settleReported(project string, commits []pullCommitAt) error {
	type reported struct {
		session int64
		sha     string
		seq     int
	}
	seen := map[reported]bool{}
	var todo []reported
	for _, c := range commits {
		rows, err := m.q.Query(`SELECT session_id, sha, chunk_seq FROM session_commits
			WHERE project=? AND source IN ('reported','observed') AND sha IN (SELECT value FROM json_each(?))`, project, jsonStrings(shaPrefixes(c.SHA)))
		if err != nil {
			return err
		}
		for rows.Next() {
			var r reported
			if err := rows.Scan(&r.session, &r.sha, &r.seq); err != nil {
				rows.Close()
				return err
			}
			if !seen[r] {
				seen[r] = true
				todo = append(todo, r)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	for _, r := range todo {
		candidates, err := pullCommitsWithPrefix(m.q, project, r.sha)
		if err != nil {
			return err
		}
		var fitting []pullCommitAt
		for _, c := range candidates {
			ok, err := m.reportedFits(r.session, r.seq, c)
			if err != nil {
				return err
			}
			if ok {
				fitting = append(fitting, c)
			}
		}
		switch {
		case len(candidates) == 0:
		case len(fitting) == 0:
			// Ein kurzer Präfix kann zu einem Commit gehören, den der Server noch
			// nicht kennt; die Zeile bleibt, die Leser blenden sie aus
			// (reportedContradicted), bis ein passender Commit sie bestätigt.
			if len(r.sha) >= minDeleteSHA {
				_, err = m.q.Exec(`DELETE FROM session_commits WHERE session_id=? AND sha=? AND source IN ('reported','observed')`, r.session, r.sha)
			}
		case len(fitting) == 1 && r.sha != fitting[0].SHA:
			var have int
			if err = m.q.QueryRow(`SELECT COUNT(*) FROM session_commits WHERE session_id=? AND sha=?`, r.session, fitting[0].SHA).Scan(&have); err != nil {
				return err
			}
			if have > 0 {
				_, err = m.q.Exec(`DELETE FROM session_commits WHERE session_id=? AND sha=?`, r.session, r.sha)
			} else {
				_, err = m.q.Exec(`UPDATE session_commits SET sha=? WHERE session_id=? AND sha=?`, fitting[0].SHA, r.session, r.sha)
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// settleClaims prüft die SHAs, die ein Stapel gemeldet oder beobachtet hat, gegen die
// Commits der PRs, die der Server schon kennt.
func (m *matcher) settleClaims(b *gitBatch) error {
	for project, list := range b.claimed {
		var known []pullCommitAt
		for _, sha := range uniqueStrings(list) {
			found, err := pullCommitsWithPrefix(m.q, project, sha)
			if err != nil {
				return err
			}
			known = append(known, found...)
		}
		if err := m.settleReported(project, known); err != nil {
			return err
		}
	}
	return nil
}

// matchWindows legt die Zeitfenster der Vorgänge ohne SHA über die Commits der PRs
// und gibt zurück, wie viele Commits dabei einer Session zugeordnet wurden.
func (m *matcher) matchWindows(b *gitBatch) (int, error) {
	total := 0
	for project, ws := range b.windows {
		commits, err := m.commitsInWindows(project, ws)
		if err != nil {
			return total, err
		}
		n, err := m.resolveAll(project, commits)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// minDeleteSHA: erst ab dieser Länge ist ein Präfix eindeutig genug, dass ein
// widersprechender Commit die gemeldete Zeile beweisbar entkräftet. Eine kürzere
// kann zu einem Commit gehören, den der Server noch nicht kennt.
const minDeleteSHA = 12

// reportedContradicted sagt, ob eine gemeldete Zeile dem widerspricht, was der
// Server über die Commits mit ihrem Präfix weiß: es gibt welche, und keiner passt
// ins Fenster des Vorgangs oder zum Autor. Eine Zeile ohne bekannten Commit
// widerspricht nicht.
func reportedContradicted(q dbTx, project string, session int64, seq int, sha string) (bool, error) {
	candidates, err := pullCommitsWithPrefix(q, project, sha)
	if err != nil || len(candidates) == 0 {
		return false, err
	}
	m := newMatcher(q, instanceOwnerID(q))
	for _, c := range candidates {
		ok, err := m.reportedFits(session, seq, c)
		if err != nil || ok {
			return false, err
		}
	}
	return true, nil
}

// pullCommit ist ein Commit eines PRs mit seiner Zeit und seinem Autor.
type pullCommitAt struct {
	SHA         string
	CommittedAt time.Time
	// Author ist der GitHub-Login des Autors; leer, wenn GitHub keinen kennt.
	Author string
}

// pullCommitsWithPrefix nennt die Commits der PRs des Projekts, deren SHA mit
// dem Präfix beginnt, mit ihrer Zeit (ohne Zeit nicht: sie lassen sich nicht
// prüfen).
func pullCommitsWithPrefix(q dbQuerier, project, prefix string) ([]pullCommitAt, error) {
	rows, err := q.Query(sqlPullCommitsWithPrefix, project, prefix, prefix+"g")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pullCommitAt
	for rows.Next() {
		var sha, at, author string
		if err := rows.Scan(&sha, &at, &author); err != nil {
			return nil, err
		}
		if t, ok := parseGitTime(at); ok {
			out = append(out, pullCommitAt{SHA: sha, CommittedAt: t, Author: author})
		}
	}
	return out, rows.Err()
}

const sqlPullCommitsWithPrefix = `SELECT DISTINCT sha, committed_at, author FROM git_pr_commits
	WHERE project=? AND sha >= ? AND sha < ? AND committed_at != ''`

// reportedFits sagt, ob ein Commit der Vorgang gewesen sein kann, der seine SHA
// gemeldet hat: seine Zeit liegt im Fenster, und sein Autor ist nicht ein anderer
// Mensch als der Besitzer der Session. Ein Vorgang, den es nicht gibt, widerspricht
// nicht.
func (m *matcher) reportedFits(session int64, seq int, c pullCommitAt) (bool, error) {
	var started, ended string
	var acct int64
	err := m.q.QueryRow(`SELECT started_at, ended_at, account_id FROM session_git_events WHERE session_id=? AND seq=? AND kind='commit'`, session, seq).Scan(&started, &ended, &acct)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	from, ok1 := parseGitTime(started)
	to, ok2 := parseGitTime(ended)
	if ok1 && ok2 && (c.CommittedAt.Before(from.Add(-gitMatchSlack)) || c.CommittedAt.After(to.Add(gitMatchSlack))) {
		return false, nil
	}
	level, err := m.fit(acct, c.Author)
	if err != nil {
		return false, err
	}
	return level != fitNone, nil
}

// --------------------------------------------------- Zeitfenster

// gitWindow ist das Fenster eines Vorgangs ohne gemeldete SHA (RFC 3339, UTC).
type gitWindow struct{ from, to string }

// commitsInWindows nennt die Commits der PRs des Projekts, die in eines der Fenster
// fallen, einmal je SHA und nach Zeit geordnet. Je Fenster ein Bereichsabruf auf
// (project, committed_at); höchstens maxWindowCommits je Fenster und so viele
// insgesamt, wie der Lauf noch bearbeiten darf.
func (m *matcher) commitsInWindows(project string, ws []gitWindow) ([]pullCommitAt, error) {
	seenWindow := map[gitWindow]bool{}
	seen := map[string]bool{}
	var out []pullCommitAt
	for _, w := range ws {
		if seenWindow[w] {
			continue
		}
		seenWindow[w] = true
		from, ok1 := parseGitTime(w.from)
		to, ok2 := parseGitTime(w.to)
		if !ok1 || !ok2 {
			continue
		}
		rows, err := m.q.Query(sqlCommitsInWindow, project, from.Add(-gitMatchSlack).Format(time.RFC3339), to.Add(gitMatchSlack).Format(time.RFC3339), maxWindowCommits)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var sha, at, author string
			if err := rows.Scan(&sha, &at, &author); err != nil {
				rows.Close()
				return nil, err
			}
			if seen[sha] {
				continue
			}
			if t, ok := parseGitTime(at); ok {
				seen[sha] = true
				out = append(out, pullCommitAt{SHA: sha, CommittedAt: t, Author: author})
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		if len(out) >= m.budget {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CommittedAt.Before(out[j].CommittedAt) })
	return out, nil
}

const sqlCommitsInWindow = `SELECT sha, committed_at, author FROM git_pr_commits
	WHERE project=? AND committed_at >= ? AND committed_at <= ? AND committed_at != '' LIMIT ?`

// resolveAll entscheidet je Commit, welche Session ihn erzeugt hat. Je Lauf ist die
// Arbeit gedeckelt (Anzahl und Zeit): der Writer wartet nicht auf einen Stapel, der
// Hunderte Commits gegen Tausende Fenster legt. Was liegen bleibt, holt der nächste
// Sync des PRs oder der nächste Vorgang nach, denn beide gleichen gegen ALLE
// Vorgänge ab.
func (m *matcher) resolveAll(project string, commits []pullCommitAt) (int, error) {
	assigned := 0
	if len(commits) > m.budget {
		// Der Deckel soll nicht immer dieselben ersten Commits treffen: was noch
		// keiner Session gehört, kommt zuerst.
		var err error
		if commits, err = m.unassignedFirst(project, commits); err != nil {
			return 0, err
		}
	}
	for _, c := range commits {
		if m.budget <= 0 || time.Now().After(m.deadline) {
			if !m.stopped {
				log.Printf("session git match for %s stopped at its work limit; the rest follows with the next sync", project)
			}
			m.stopped = true
			break
		}
		m.budget--
		n, err := m.resolveCommit(project, c)
		if err != nil {
			return assigned, err
		}
		assigned += n
	}
	return assigned, nil
}

// unassignedFirst ordnet die Commits so, dass die ohne zugeordnete Zeile vor den
// schon zugeordneten stehen, je Gruppe nach der bisherigen Reihenfolge.
func (m *matcher) unassignedFirst(project string, commits []pullCommitAt) ([]pullCommitAt, error) {
	shas := make([]string, len(commits))
	for i, c := range commits {
		shas[i] = c.SHA
	}
	rows, err := m.q.Query(`SELECT DISTINCT sha FROM session_commits WHERE project=? AND source='matched' AND sha IN (SELECT value FROM json_each(?))`,
		project, jsonStrings(shas))
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			rows.Close()
			return nil, err
		}
		have[sha] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	out := make([]pullCommitAt, 0, len(commits))
	for _, c := range commits {
		if !have[c.SHA] {
			out = append(out, c)
		}
	}
	for _, c := range commits {
		if have[c.SHA] {
			out = append(out, c)
		}
	}
	return out, nil
}

type windowEvent struct {
	id       int64
	session  int64
	account  int64
	seq      int
	op       string
	branch   string
	started  string
	reported bool
	// weak (nur für gehaltene Zeilen): die Zuordnung ist als schwach gespeichert.
	weak bool
}

// Die Abfragen des Abgleichs; ein Test prüft ihre Pläne.
const (
	// Vorgänge, deren Fenster eine Zeit enthält. %s: leer oder "AND e.account_id=?".
	sqlWindowEvents = `SELECT e.id, e.session_id, CASE WHEN e.account_id!=0 THEN e.account_id WHEN s.account_id=0 THEN ? ELSE s.account_id END,
			e.seq, e.op, COALESCE(NULLIF(e.branch,''), s.branch), e.started_at,
			EXISTS(SELECT 1 FROM session_commits r WHERE r.session_id=e.session_id AND r.chunk_seq=e.seq AND r.source IN ('reported','observed'))
		FROM session_git_events e JOIN sessions s ON s.id=e.session_id
		WHERE e.project=? AND e.kind='commit' AND e.ok=1 %s AND e.started_at >= ? AND e.started_at <= ? AND e.ended_at >= ?
		LIMIT ?`
	sqlClaimants = `SELECT r.session_id, CASE WHEN e.account_id!=0 THEN e.account_id WHEN s.account_id=0 THEN ? ELSE s.account_id END
		FROM session_commits r JOIN sessions s ON s.id=r.session_id
		JOIN session_git_events e ON e.session_id=r.session_id AND e.seq=r.chunk_seq AND e.kind='commit'
		WHERE r.project=? AND r.source IN ('reported','observed') AND r.sha IN (SELECT value FROM json_each(?))
		  AND e.started_at <= ? AND e.ended_at >= ?
		LIMIT ?`
	sqlHeldMatches = `SELECT r.session_id, CASE WHEN s.account_id=0 THEN ? ELSE s.account_id END, r.chunk_seq, r.weak
		FROM session_commits r JOIN sessions s ON s.id=r.session_id
		WHERE r.project=? AND r.sha=? AND r.source='matched'`
	// Von den Commits aus (Index project, sha), nicht über alle PRs des Projekts.
	sqlPullHeads = `SELECT DISTINCT p.head_ref FROM git_pr_commits k CROSS JOIN git_pull_requests p ON p.project=k.project AND p.number=k.pr
		WHERE k.project=? AND k.sha=?`
)

func windowEventsSQL(byAccount bool) string {
	cond := ""
	if byAccount {
		cond = "AND e.account_id=?"
	}
	return strings.Replace(sqlWindowEvents, "%s", cond, 1)
}

// windowEvents liest die Vorgänge ohne gemeldete SHA, deren Fenster die Zeit
// enthält. Mit acct != 0 nur die dieses Kontos. crowded: es sind mehr als
// maxWindowCandidates, die Zeit sagt dann nichts mehr aus.
func (m *matcher) windowEvents(project string, at time.Time, acct int64) (events []windowEvent, crowded bool, err error) {
	loStr := at.Add(-gitMatchSlack).Format(time.RFC3339)
	hiStr := at.Add(gitMatchSlack).Format(time.RFC3339)
	lo := at.Add(-gitMatchSlack).Add(-maxGitCallWindow).Format(time.RFC3339)
	args := []any{m.owner, project}
	if acct != 0 {
		args = append(args, acct)
	}
	args = append(args, lo, hiStr, loStr, maxWindowCandidates+1)
	rows, err := m.q.Query(windowEventsSQL(acct != 0), args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var e windowEvent
		if err := rows.Scan(&e.id, &e.session, &e.account, &e.seq, &e.op, &e.branch, &e.started, &e.reported); err != nil {
			return nil, false, err
		}
		n++
		// Ein Vorgang mit gemeldeter SHA hat seine Antwort; er nimmt weder teil noch
		// blockiert er.
		if !e.reported {
			events = append(events, e)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if n > maxWindowCandidates {
		return nil, true, nil
	}
	// Die Reihenfolge entscheidet, welcher von mehreren Vorgängen derselben Session
	// die Zeile bekommt: der früheste.
	sort.Slice(events, func(i, j int) bool {
		if events[i].started != events[j].started {
			return events[i].started < events[j].started
		}
		return events[i].id < events[j].id
	})
	return events, false, nil
}

// claimants liest die Sessions, die diese SHA (auch als kurze Fassung) selbst
// gemeldet oder beobachtet haben, deren Vorgang die Zeit einschließt.
func (m *matcher) claimants(project string, c pullCommitAt) (list []windowEvent, crowded bool, err error) {
	loStr := c.CommittedAt.Add(-gitMatchSlack).Format(time.RFC3339)
	hiStr := c.CommittedAt.Add(gitMatchSlack).Format(time.RFC3339)
	rows, err := m.q.Query(sqlClaimants, m.owner, project, jsonStrings(shaPrefixes(c.SHA)), hiStr, loStr, maxWindowCandidates+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var e windowEvent
		if err := rows.Scan(&e.session, &e.account); err != nil {
			return nil, false, err
		}
		list = append(list, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return list, len(list) > maxWindowCandidates, nil
}

// resolveCommit ordnet einen Commit eines PRs der Session zu, deren Vorgang ihn
// erzeugt haben kann, oder keiner. Der Zeitpunkt des Commits stammt von GitHub,
// die Fenster aus den Transkripten der Sessions.
//
// Regeln:
//  0. Der Autor des Commits entscheidet, wer teilnehmen darf (siehe oben): ein
//     anderer bekannter Autor schließt aus; stark vor schwach. Gibt es starke
//     Kandidaten, nehmen schwache nicht teil.
//  1. Eine SHA, die eine Session selbst gemeldet oder beobachtet hat (reported,
//     observed) und deren Fenster die Commit-Zeit enthält, ist belegt; das
//     Zeitfenster der anderen entscheidet dann nichts mehr.
//  2. Vorgänge anderer Sessions, deren Fenster die Commit-Zeit enthält, treten in
//     Wettbewerb. Ein Vorgang, dessen Branch dem Branch eines PRs widerspricht,
//     der den Commit trägt, scheidet aus. Treffen mehr als maxWindowCandidates
//     zu, bleibt der Commit offen.
//  3. Bleibt genau eine Session, bekommt sie den Commit. Bleiben mehrere, bekommt
//     ihn die einzige, deren Branch zum PR passt; sonst niemand.
//  4. Was einmal einem Konto zugeordnet ist, nimmt kein anderes Konto zurück:
//     ein Vorgang eines fremden Kontos im selben Fenster ändert die Zuordnung
//     nicht, und ein fremder Anspruch ersetzt sie nicht. Das gilt nicht für eine
//     schwache Zuordnung gegenüber einem starken Kandidaten (das echte Ereignis des
//     Autors ersetzt sie) und nicht für eine Zuordnung, deren Konto ein anderer
//     Autor ausschließt. Zwischen Sessions desselben Kontos (ein Orchestrator und
//     sein Worker auf einem Branch) bleibt das Ergebnis unabhängig von der
//     Reihenfolge, in der die Vorgänge eintrafen, und eine Zuordnung, die ein
//     späterer zweiter Vorgang mehrdeutig macht, wird wieder zurückgenommen. Wer ein
//     Fenster oder einen Branch erfindet, kann so fremde Arbeit nicht löschen; er
//     kann nur seine eigene, schwache Zeile anlegen. Eine gemeldete SHA eines anderen
//     Kontos legt ihre Zeile NEBEN die Zuordnung des Zeitfensters, sie ersetzt sie
//     nicht: beide Quellen stehen mit ihrem Namen da (reported ist stärker als
//     matched, das zeigt die Anzeige), und niemand kann mit einer Behauptung fremde
//     Arbeit entfernen.
func (m *matcher) resolveCommit(project string, c pullCommitAt) (int, error) {
	// Das Konto des Autors, wenn es seine GitHub-Verknüpfung bewiesen hat.
	var authorAcct int64
	if c.Author != "" {
		id, err := m.verifiedAccount(c.Author)
		if err != nil {
			return 0, err
		}
		authorAcct = id
	}
	// Regel 1: belegte Ansprüche.
	claimed, crowded, err := m.claimants(project, c)
	if err != nil || crowded {
		return 0, err
	}
	var strongClaims, weakClaims []windowEvent
	for _, e := range claimed {
		lvl, err := m.fit(e.account, c.Author)
		if err != nil {
			return 0, err
		}
		switch lvl {
		case fitStrong:
			strongClaims = append(strongClaims, e)
		case fitWeak:
			weakClaims = append(weakClaims, e)
		}
	}
	// Regel 0 und 2: wer nimmt teil. Stark vor schwach.
	var claims, events []windowEvent
	useStrong := false
	if len(strongClaims) > 0 {
		useStrong, claims = true, strongClaims
	} else {
		if authorAcct != 0 {
			ev, crowded, err := m.windowEvents(project, c.CommittedAt, authorAcct)
			if err != nil || crowded {
				return 0, err
			}
			if len(ev) > 0 {
				useStrong, events = true, ev
			}
		}
		if !useStrong {
			claims = weakClaims
			if len(claims) == 0 {
				ev, crowded, err := m.windowEvents(project, c.CommittedAt, 0)
				if err != nil || crowded {
					return 0, err
				}
				for _, e := range ev {
					lvl, err := m.fit(e.account, c.Author)
					if err != nil {
						return 0, err
					}
					if lvl != fitNone {
						events = append(events, e)
					}
				}
			}
		}
	}
	if len(claims) > 0 {
		events = nil
	}
	winner, err := m.windowOwner(project, c.SHA, events)
	if err != nil {
		return 0, err
	}
	// Regel 4: was schon zugeordnet ist.
	rows, err := m.q.Query(sqlHeldMatches, m.owner, project, c.SHA)
	if err != nil {
		return 0, err
	}
	var held []windowEvent
	for rows.Next() {
		var h windowEvent
		if err := rows.Scan(&h.session, &h.account, &h.seq, &h.weak); err != nil {
			rows.Close()
			return 0, err
		}
		held = append(held, h)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	involved := map[int64]bool{}
	for _, e := range events {
		involved[e.account] = true
	}
	for _, e := range claims {
		involved[e.account] = true
	}
	kept := false
	for _, h := range held {
		if winner != nil && h.session == winner.session {
			if h.weak && useStrong {
				if _, err := m.q.Exec(`UPDATE session_commits SET weak=0 WHERE session_id=? AND sha=?`, h.session, c.SHA); err != nil {
					return 0, err
				}
			}
			continue
		}
		hf, err := m.fit(h.account, c.Author)
		if err != nil {
			return 0, err
		}
		switch {
		case hf == fitNone:
			// Der Autor ist bekannt und nicht diese Person: die Zuordnung war nie
			// beweisbar (der Autor kam erst mit dem PR-Sync).
		case h.weak && useStrong:
			// Ein starker Kandidat ersetzt eine schwache Zuordnung, auch die eines
			// anderen Kontos.
		default:
			// Nimmt ein anderes Konto an der Entscheidung teil, bleibt die Zuordnung
			// stehen.
			foreign := winner != nil && winner.account != h.account
			for acct := range involved {
				foreign = foreign || acct != h.account
			}
			if foreign {
				kept = true
				continue
			}
		}
		if _, err := m.q.Exec(`DELETE FROM session_commits WHERE session_id=? AND sha=? AND source='matched'`, h.session, c.SHA); err != nil {
			return 0, err
		}
		if _, err := m.q.Exec(`UPDATE session_git_events SET resolved = EXISTS(
				SELECT 1 FROM session_commits sc WHERE sc.session_id=session_git_events.session_id AND sc.chunk_seq=session_git_events.seq)
			WHERE session_id=? AND seq=? AND kind='commit'`, h.session, h.seq); err != nil {
			return 0, err
		}
	}
	if winner == nil || kept {
		return 0, nil
	}
	op := winner.op
	if op == "" {
		op = "commit"
	}
	added, err := putSessionCommit(m.q, sessionCommit{
		SessionID: winner.session, Project: project, SHA: c.SHA, Source: CommitSourceMatched,
		Op: op, ChunkSeq: winner.seq, CommittedAt: c.CommittedAt.Format(time.RFC3339), Weak: !useStrong,
	}, now())
	if err != nil {
		return 0, err
	}
	if _, err := m.q.Exec(`UPDATE session_git_events SET resolved=1 WHERE id=?`, winner.id); err != nil {
		return 0, err
	}
	if added {
		return 1, nil
	}
	return 0, nil
}

// windowOwner wendet die Regeln 2 und 3 an.
func (m *matcher) windowOwner(project, sha string, events []windowEvent) (*windowEvent, error) {
	if len(events) == 0 {
		return nil, nil
	}
	// Die Branches der PRs, die den Commit tragen.
	rows, err := m.q.Query(sqlPullHeads, project, sha)
	if err != nil {
		return nil, err
	}
	heads := map[string]bool{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return nil, err
		}
		heads[h] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	var fitting, unknown []windowEvent
	for _, e := range events {
		switch {
		case e.branch == "" || len(heads) == 0:
			unknown = append(unknown, e)
		case heads[e.branch]:
			fitting = append(fitting, e)
		}
	}
	// Mehrere Vorgänge derselben Session im selben Fenster sind kein Wettbewerb.
	switch {
	case len(sessionsOf(fitting)) == 1:
		return &fitting[0], nil
	case len(fitting) == 0 && len(sessionsOf(unknown)) == 1:
		return &unknown[0], nil
	}
	return nil, nil
}

func sessionsOf(events []windowEvent) map[int64]bool {
	out := map[int64]bool{}
	for _, e := range events {
		out[e.session] = true
	}
	return out
}

// matchPullCommits gleicht nach einem PR-Sync ab: kurze SHAs werden voll, und die
// Commits des PRs werden gegen offene und frühere Vorgänge gelegt. Ein Fehler
// hier darf den Sync nicht scheitern lassen (er läuft nach dessen Commit); er wird
// protokolliert und beim nächsten Sync nachgeholt.
func (s *Store) matchPullCommits(project string, commits []pullCommitAt) {
	if len(commits) == 0 {
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("session git match for %s: %v", project, err)
		return
	}
	defer tx.Rollback()
	m := newMatcher(tx, instanceOwnerID(tx))
	if err := m.settleReported(project, commits); err != nil {
		log.Printf("session git match for %s: %v", project, err)
		return
	}
	if _, err := m.resolveAll(project, commits); err != nil {
		log.Printf("session git match for %s: %v", project, err)
		return
	}
	if err := tx.Commit(); err != nil {
		log.Printf("session git match for %s: %v", project, err)
	}
}
