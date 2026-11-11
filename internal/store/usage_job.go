package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/chunktext"
	"github.com/Deadweight-Labs/ghosttree/internal/rawcodec"
)

// Der Auswertungsschritt des Nutzungsnachweises (REQ-645, P3 des Konzepts
// "Schlankes Profil und Kontext, der hilft"). Er füllt context_delivery_log.used
// und judged_at, schreibt die Quelle "used" des Projektbezugs und führt
// knowledge_usage fort. Was er entscheidet, steht in usage_judge.go; hier steht,
// wie er an die Daten kommt, ohne die Schreibsperre zu halten.
//
// Ablauf eines Schritts:
//
//  1. Lesen (Lesepool, keine Sperre): welche Sitzungen haben offene Zeilen, die
//     das Konto besitzt und die zu Ende sind (planJudgement).
//  2. Lesen (Lesepool): je Sitzung die Zeilen des Transkripts ab der ersten
//     Zustellung, abgeleitet über chunktext, durch den Matcher (judgeSession).
//     Nur Zahlen bleiben übrig.
//  3. Schreiben (eine kurze Transaktion über den Schreiber): die Zahlen eintragen
//     (applyJudgement). Jede Zeile wird nur geändert, solange sie offen ist, ein
//     zweiter Lauf über dieselben Zeilen ändert nichts.
//
// Besitz: Eine Zeile wird nur gegen eine Sitzung ausgewertet, die dem Konto der
// Zeile gehört (sessions.account_id; 0 ist Altbestand des Instanz-Owners, wie
// überall). Wessen Sitzung unter demselben Namen hochgeladen wurde, bleibt
// ungelesen. Was den Store verlässt, sind Summen, nie Text.
//
// Das Schema ist additiv. knowledge_usage hält je Eintrag, in wie vielen
// Sitzungen er zugestellt und in wie vielen aufgegriffen wurde; es hat keine
// Sitzungsnummern und überlebt die 180 Tage der Rohzeilen. Der Teilindex deckt
// genau die Zeilen, die der Schritt sucht (offen, beurteilbar), damit die Suche
// nicht mit den vielen Pfadbeschreibungen des Protokolls wächst; er ist
// absichtlich dieselbe Bedingung wie in der Abfrage, sonst nutzt SQLite ihn
// nicht.
//
// Ein Löschpfad für Sitzungen oder Konten existiert nicht (KN-4494); ein späterer
// muss knowledge_usage nicht nennen, denn die Tabelle kennt keine Sitzung.
const usageSchema = `
CREATE TABLE IF NOT EXISTS knowledge_usage(
  knowledge_id INTEGER PRIMARY KEY,
  delivered_sessions INTEGER NOT NULL DEFAULT 0,
  used_sessions INTEGER NOT NULL DEFAULT 0,
  followed_sessions INTEGER NOT NULL DEFAULT 0,
  last_used_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS context_delivery_log_open ON context_delivery_log(delivered_at)
  WHERE used = -1 AND judged_at = '' AND kind IN ('knowledge','runbook');
`

func ensureUsageJudge(db *sql.DB) error {
	_, err := db.Exec(usageSchema)
	return err
}

const (
	// DefaultJudgeSessions ist die Zahl Sitzungen, die ein Schritt höchstens liest.
	DefaultJudgeSessions = 20
	// judgeRowCap ist die Zahl offener Zeilen, nach der ein Schritt Sitzungen
	// sucht. Es ist größer als die Sitzungen mal die Zeilen je Sitzung, die der
	// Schritt je schafft, und klein genug, dass die Suche nicht von der Menge des
	// Rückstands abhängt.
	judgeRowCap = 800
	// judgeLineCap begrenzt die Zeilen, die eine Sitzung dem Matcher gibt.
	judgeLineCap = 20000
	// judgeGiveUpRows begrenzt, wie viele Zeilen ein Schritt als nicht
	// auswertbar abschließt.
	judgeGiveUpRows = 500
	// DefaultJudgeGiveUp ist das Alter, ab dem eine Zeile ohne Transkript nicht
	// mehr auf eines wartet. Eine Sitzung, die nach zwei Wochen noch nicht
	// hochgeladen ist, wird es kaum noch.
	DefaultJudgeGiveUp = 14 * 24 * time.Hour
)

// JudgeOptions steuert einen Schritt. Nullwerte heißen: Standard.
type JudgeOptions struct {
	// MaxSessions ist die Zahl Sitzungen, die ein Schritt höchstens liest.
	MaxSessions int
	// Settle ist die Ruhe, nach der eine Sitzung als beendet gilt.
	Settle time.Duration
	// GiveUp ist das Alter, ab dem eine Zeile ohne Transkript abgeschlossen wird.
	GiveUp time.Duration
	// Now ersetzt die Uhr (Tests).
	Now time.Time
}

// JudgeResult sagt, was ein Schritt getan hat: Zahlen, kein Inhalt.
type JudgeResult struct {
	Sessions int // gelesene Sitzungen
	Rows     int // beurteilte Zeilen
	PickedUp int // davon aufgegriffen (used = 1)
	Followed int // davon gefolgt (used = 2)
	GaveUp   int // ohne Urteil abgeschlossen (kein Transkript, Eintrag fehlt)
	// More sagt, dass nach diesem Schritt noch offene Zeilen auswertbar sind.
	More bool
}

// judgeVerdict ist das Ergebnis für eine Zeile des Protokolls. Used -1 heißt:
// nicht auswertbar, die Zeile wird abgeschlossen, ohne ein Urteil zu tragen.
type judgeVerdict struct {
	RowID       int64
	Used        int
	Kind        string
	ObjectID    int64
	Project     string
	Account     int64
	Session     string
	DeliveredAt string
}

// sessionOfLog verbindet eine Zeile des Protokolls (Alias d) mit der Sitzung, die
// ihrem Konto gehört (Alias se). Der Hook meldet "claude", der Sammler schreibt
// "claude-code". Beide Spalten des eindeutigen Index stehen als Gleichheit da,
// damit er benutzt wird. Der eine Parameter ist der Owner der Instanz.
const sessionOfLog = `se.harness = CASE d.harness WHEN 'claude' THEN 'claude-code' ELSE d.harness END
	AND se.external_id = d.session_key
	AND (CASE WHEN se.account_id = 0 THEN ? ELSE se.account_id END) = d.account_id`

// openJudgeable ist die Bedingung der offenen, beurteilbaren Zeilen. Sie steht
// wörtlich im Teilindex context_delivery_log_open.
const openJudgeable = `d.used = -1 AND d.judged_at = '' AND d.kind IN ('knowledge','runbook')`

// servedByServer ist die Bedingung, dass der Server die Sache dieser Sitzung
// selbst zugestellt hat (context_deliveries, vom Server beim Ausliefern
// geschrieben, nach dem Zugriffsrecht des Kontos). Das Protokoll schreibt der
// Hook, und wer in einem Projekt schreiben darf, kann darin jede Nummer melden.
// Ohne diese Bedingung wäre das Urteil ein Orakel: Wer eine fremde, für ihn
// unlesbare Eintragsnummer meldet und im eigenen Transkript Wörter ausprobiert,
// läse am Wert von used ab, ob sie im Titel oder Text des Eintrags stehen. Eine
// Zeile, die der Server nicht bestätigt, wird nie ausgewertet und nach GiveUp
// ohne Urteil geschlossen; ihr Ausgang verrät nichts. Der Server vergisst
// Zustellungen nach contextDeliveryRetention (7 Tagen); eine Zeile, die der
// Job bis dahin nicht erreicht hat, wird deshalb nicht mehr beurteilt.
const servedByServer = `EXISTS (SELECT 1 FROM context_deliveries cd
	WHERE cd.account_id = 'person:' || d.account_id AND cd.session_key = d.session_key
	  AND ((d.kind = 'knowledge' AND cd.knowledge_id = d.object_id)
	    OR (d.kind = 'runbook' AND cd.knowledge_id = 0 AND cd.term = 'runbook:' || d.object_id)))`

type judgeSessionPlan struct {
	account int64
	key     string
	id      int64
	harness string
	rows    []judgeVerdict
}

// JudgeDeliveries führt einen Schritt aus. Gelesen wird über den Lesepool, die
// Schreibsperre hält nur der Eintrag der Zahlen am Ende.
func (s *Store) JudgeDeliveries(ctx context.Context, opt JudgeOptions) (JudgeResult, error) {
	if opt.MaxSessions <= 0 {
		opt.MaxSessions = DefaultJudgeSessions
	}
	if opt.Settle <= 0 {
		opt.Settle = signalSettleTime
	}
	if opt.GiveUp <= 0 {
		opt.GiveUp = DefaultJudgeGiveUp
	}
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	now := opt.Now.UTC()
	rd := s
	if s.reader != nil {
		rd = s.reader
	}
	var res JudgeResult
	plans, more, err := rd.planJudgement(ctx, now, opt)
	if err != nil {
		return res, err
	}
	res.More = more
	var verdicts []judgeVerdict
	for _, p := range plans {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		vs, err := rd.judgeSession(ctx, p)
		if err != nil {
			return res, fmt.Errorf("judge session %d: %w", p.id, err)
		}
		res.Sessions++
		verdicts = append(verdicts, vs...)
	}
	stale, err := rd.expiredRows(ctx, now, opt)
	if err != nil {
		return res, err
	}
	for _, id := range stale {
		verdicts = append(verdicts, judgeVerdict{RowID: id, Used: UsedPending})
	}
	if len(verdicts) == 0 {
		return res, nil
	}
	applied, err := s.applyJudgement(verdicts, now.Format(time.RFC3339))
	if err != nil {
		return res, err
	}
	for _, v := range applied {
		switch v.Used {
		case UsedPending:
			res.GaveUp++
		case UsedPickedUp:
			res.PickedUp++
			res.Rows++
		case UsedFollowed:
			res.Followed++
			res.Rows++
		default:
			res.Rows++
		}
	}
	return res, nil
}

// planJudgement sucht die Sitzungen, die ausgewertet werden können: offene Zeilen
// einer Sitzung, die dem Konto gehört und seit settle ruht. Die ältesten
// Zustellungen kommen zuerst. Zeilen, deren Sitzung es nicht gibt, kommen nicht
// vor; sie werden nach GiveUp abgeschlossen (expiredRows).
func (s *Store) planJudgement(ctx context.Context, now time.Time, opt JudgeOptions) ([]judgeSessionPlan, bool, error) {
	settled := now.Add(-opt.Settle).Format(time.RFC3339)
	owner := instanceOwnerID(s.db)
	rows, err := s.db.QueryContext(ctx, `SELECT d.account_id, d.session_key, se.id, se.harness
		FROM context_delivery_log d JOIN sessions se ON `+sessionOfLog+`
		WHERE `+openJudgeable+` AND d.subagent = 0 AND d.held_out = 0 AND d.delivered_at < ? AND se.last_seen_at < ?
		  AND `+servedByServer+`
		ORDER BY d.delivered_at LIMIT ?`, owner, settled, settled, judgeRowCap)
	if err != nil {
		return nil, false, err
	}
	type key struct {
		account int64
		session string
	}
	var plans []judgeSessionPlan
	seen := map[key]bool{}
	n := 0
	for rows.Next() {
		var p judgeSessionPlan
		if err := rows.Scan(&p.account, &p.key, &p.id, &p.harness); err != nil {
			rows.Close()
			return nil, false, err
		}
		n++
		k := key{p.account, p.key}
		if seen[k] || len(plans) == opt.MaxSessions {
			continue
		}
		seen[k] = true
		plans = append(plans, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := n == judgeRowCap || len(seen) == opt.MaxSessions && n > 0
	for i := range plans {
		p := &plans[i]
		r, err := s.db.QueryContext(ctx, `SELECT d.id, d.kind, d.object_id, d.project, d.delivered_at FROM context_delivery_log d
			WHERE d.account_id = ? AND d.session_key = ? AND `+openJudgeable+` AND d.subagent = 0 AND d.held_out = 0 AND d.delivered_at < ?
			AND `+servedByServer+`
			ORDER BY d.delivered_at, d.id`, p.account, p.key, settled)
		if err != nil {
			return nil, false, err
		}
		for r.Next() {
			v := judgeVerdict{Account: p.account, Session: p.key}
			if err := r.Scan(&v.RowID, &v.Kind, &v.ObjectID, &v.Project, &v.DeliveredAt); err != nil {
				r.Close()
				return nil, false, err
			}
			p.rows = append(p.rows, v)
		}
		r.Close()
		if err := r.Err(); err != nil {
			return nil, false, err
		}
	}
	return plans, more, nil
}

// expiredRows sind offene Zeilen, die älter als GiveUp sind und zu keiner
// Sitzung mit Transkript gehören, die dem Konto gehört, oder die der Server
// nicht als zugestellt kennt (servedByServer). Sie werden
// abgeschlossen, damit die Suche nach Auswertbarem nicht an ihnen hängt.
func (s *Store) expiredRows(ctx context.Context, now time.Time, opt JudgeOptions) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.id FROM context_delivery_log d
		WHERE `+openJudgeable+` AND d.delivered_at < ?
		  AND (NOT `+servedByServer+` OR NOT EXISTS (SELECT 1 FROM sessions se WHERE `+sessionOfLog+`
		       AND EXISTS (SELECT 1 FROM session_chunks c WHERE c.session_id = se.id)))
		ORDER BY d.delivered_at LIMIT ?`, now.Add(-opt.GiveUp).Format(time.RFC3339), instanceOwnerID(s.db), judgeGiveUpRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// judgeSession liest das Transkript einer Sitzung einmal und beurteilt alle
// offenen Zeilen. Aus dem Transkript wird nur gelesen, was der Agent getan hat
// (Rolle assistant, bei Codex auch die Werkzeugaufrufe), und nur von der ersten
// Zustellung an; was der Matcher sieht, verlässt diese Funktion nicht.
func (s *Store) judgeSession(ctx context.Context, p judgeSessionPlan) ([]judgeVerdict, error) {
	items, missing, err := s.judgeItems(ctx, p.rows)
	if err != nil {
		return nil, err
	}
	out := make([]judgeVerdict, 0, len(p.rows))
	for _, v := range p.rows {
		if missing[v.RowID] {
			v.Used = UsedPending
			out = append(out, v)
		}
	}
	if len(items) == 0 {
		return out, nil
	}
	first := items[0].Since
	for _, it := range items {
		if it.Since < first {
			first = it.Since
		}
	}
	j := newJudger(items)
	roles := `c.role = 'assistant'`
	if p.harness == "codex" {
		roles = `c.role IN ('assistant','tool')`
	}
	// Zeilen vor der ersten Zustellung werden nicht entpackt: die Bedingung auf
	// die Zeit steht in der Abfrage, raw wird nur gelesen, wenn kein Text
	// gespeichert ist.
	rows, err := s.db.QueryContext(ctx, `SELECT c.role, c.text, CASE WHEN c.text = '' THEN c.raw END, COALESCE(i.ts, '')
		FROM session_chunks c LEFT JOIN chunk_index i ON i.session_id = c.session_id AND i.seq = c.seq
		WHERE c.session_id = ? AND `+roles+` AND (i.ts IS NULL OR i.ts = '' OR substr(i.ts, 1, 19) >= ?)
		ORDER BY c.seq LIMIT ?`, p.id, first[:min(19, len(first))], judgeLineCap)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var role, text, at string
		var raw rawcodec.Value
		if err := rows.Scan(&role, &text, &raw, &at); err != nil {
			return nil, err
		}
		if text == "" {
			line, err := s.rawLine(raw)
			if err != nil {
				return nil, err
			}
			role, text = chunktext.Derive(p.harness, line)
		}
		j.feed(judgeLine{Role: role, Text: text, At: at})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	verdicts := j.verdicts()
	for _, v := range p.rows {
		if missing[v.RowID] {
			continue
		}
		v.Used = verdicts[v.RowID]
		out = append(out, v)
	}
	return out, nil
}

// judgeItems macht aus den Zeilen die Dinge für den Matcher: Titel und Text des
// Eintrags, Name des Ablaufs. Eine Zeile, deren Eintrag es nicht mehr gibt,
// kommt in missing.
func (s *Store) judgeItems(ctx context.Context, rows []judgeVerdict) ([]judgeItem, map[int64]bool, error) {
	type ref struct {
		kind string
		id   int64
	}
	titles := map[ref]judgeItem{}
	ids := map[string][]int64{}
	for _, v := range rows {
		ids[v.Kind] = append(ids[v.Kind], v.ObjectID)
	}
	load := func(kind, query string, two bool) error {
		list := ids[kind]
		sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
		for start := 0; start < len(list); start += 200 {
			end := min(start+200, len(list))
			in, args := inList64(list[start:end])
			r, err := s.db.QueryContext(ctx, query+` WHERE id IN (`+in+`)`, args...)
			if err != nil {
				return err
			}
			for r.Next() {
				var id int64
				var title, body string
				if two {
					err = r.Scan(&id, &title, &body)
				} else {
					err = r.Scan(&id, &title)
				}
				if err != nil {
					r.Close()
					return err
				}
				titles[ref{kind, id}] = judgeItem{Title: title, Body: body}
			}
			r.Close()
			if err := r.Err(); err != nil {
				return err
			}
		}
		return nil
	}
	if err := load("knowledge", `SELECT id, title, body FROM knowledge`, true); err != nil {
		return nil, nil, err
	}
	if err := load("runbook", `SELECT id, name FROM runbooks`, false); err != nil {
		return nil, nil, err
	}
	var items []judgeItem
	missing := map[int64]bool{}
	for _, v := range rows {
		t, ok := titles[ref{v.Kind, v.ObjectID}]
		if !ok || v.ObjectID == 0 {
			missing[v.RowID] = true
			continue
		}
		t.Key, t.Kind, t.ID, t.Since = v.RowID, v.Kind, v.ObjectID, v.DeliveredAt
		items = append(items, t)
	}
	return items, missing, nil
}

func inList64(ids []int64) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(ids)), ","), args
}

// applyJudgement trägt die Urteile ein und gibt zurück, welche Zeilen sich
// geändert haben. Eine Zeile wird nur geschrieben, solange sie offen ist; ein
// Urteil, das ein anderer Lauf schon eingetragen hat, bleibt und zählt nicht
// zweimal. Dasselbe gilt für knowledge_usage: gezählt wird je Sitzung und
// Eintrag einmal, und nur für die Zeilen, die dieser Aufruf geändert hat.
func (s *Store) applyJudgement(vs []judgeVerdict, at string) ([]judgeVerdict, error) {
	if s.writer != nil {
		return queueValue(s, []any{vs, at}, func(d *Store, p []any) ([]judgeVerdict, error) {
			return d.applyJudgement(p[0].([]judgeVerdict), p[1].(string))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	up, err := tx.Prepare(`UPDATE context_delivery_log SET used = ?, judged_at = ? WHERE id = ? AND used = -1 AND judged_at = ''`)
	if err != nil {
		return nil, err
	}
	defer up.Close()
	var applied []judgeVerdict
	for _, v := range vs {
		res, err := up.Exec(v.Used, at, v.RowID)
		if err != nil {
			return nil, err
		}
		if n, err := res.RowsAffected(); err == nil && n == 1 {
			applied = append(applied, v)
		}
	}
	type entry struct {
		account int64
		session string
		id      int64
	}
	type sum struct {
		used    int
		project string
		at      string
	}
	per := map[entry]*sum{}
	for _, v := range applied {
		if v.Kind != "knowledge" || v.Used < UsedNone || v.ObjectID == 0 {
			continue
		}
		k := entry{v.Account, v.Session, v.ObjectID}
		e := per[k]
		if e == nil {
			e = &sum{project: v.Project}
			per[k] = e
		}
		e.used = max(e.used, v.Used)
		e.at = max(e.at, v.DeliveredAt)
	}
	for k, e := range per {
		picked, followed, last := 0, 0, ""
		if e.used >= UsedPickedUp {
			picked, last = 1, e.at
		}
		if e.used == UsedFollowed {
			followed = 1
		}
		if _, err := tx.Exec(`INSERT INTO knowledge_usage(knowledge_id, delivered_sessions, used_sessions, followed_sessions, last_used_at, updated_at)
			VALUES(?,1,?,?,?,?)
			ON CONFLICT(knowledge_id) DO UPDATE SET delivered_sessions = delivered_sessions + 1,
			  used_sessions = used_sessions + excluded.used_sessions,
			  followed_sessions = followed_sessions + excluded.followed_sessions,
			  last_used_at = MAX(last_used_at, excluded.last_used_at), updated_at = excluded.updated_at`,
			k.id, picked, followed, last, at); err != nil {
			return nil, err
		}
		// Ein breiter Eintrag, der in diesem Projekt zugestellt und aufgegriffen
		// wurde, hat Bezug zu ihm (KN-4500). Projektwissen braucht keinen.
		if picked == 1 && e.project != "" {
			if _, err := tx.Exec(`INSERT INTO knowledge_project_affinity(knowledge_id, project, source)
				SELECT id, ?, 'used' FROM knowledge WHERE id = ? AND project = ''
				ON CONFLICT DO NOTHING`, e.project, k.id); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return applied, nil
}
