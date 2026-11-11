package store

import (
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// Projektbezug breiten Wissens (REQ-641, P2 des Konzepts "Schlankes Profil und
// Kontext, der hilft", Teil 3, PAIN-74).
//
// Ein Eintrag mit Maschinen- oder globalem Scope gehört zu keinem Projekt, und
// der Startkontext konnte bisher nicht sagen, ob er zu dem Repo passt, in dem
// die Sitzung läuft. knowledge_project_affinity hält das fest, berechnet und
// nicht gepflegt: ein breiter Eintrag hat Bezug zu P, wenn
//
//	origin   seine schreibende Session in P lief (die Kennung am Eintrag oder am
//	         Entstehungskontext, origin_contexts.session_ref),
//	evidence eine seiner Beleg-Sessions in P lief (knowledge_evidence), oder
//	used     er in P zugestellt und aufgegriffen wurde (P3 des Konzepts; die
//	         Quelle steht im Schema, geschrieben wird sie noch nicht).
//
// Dazu gibt es knowledge.everywhere für "gilt überall": die Handvoll Fakten, die
// in jedem Projekt stimmen (Nightwatch, die Rechercheregeln). Das Kennzeichen
// setzt ein Mensch; es wird nicht berechnet.
//
// Die Zeilen entstehen in SQL-Triggern, nicht im Go-Schreibpfad. Wissen kommt auf
// vier Wegen in die Datenbank (Agent, Destillat, Migration, Upgrade), und die
// Belege der Sessions wachsen später dazu; ein Trigger hängt an der Tabelle und
// sieht alle. Eine Session, die erst nach dem Eintrag hochgeladen wird (der
// Entstehungskontext sagt dann nur ihre Kennung), holt der Trigger auf sessions
// nach.
//
// Zeilen werden nie gelöscht: wer einmal in P gearbeitet hat, hat Bezug zu P
// behalten, auch wenn die Session später verschwindet. Ein Eintrag, der in ein
// Projekt verschoben wird, behält seine Zeilen; sie lesen nur breite Einträge.
const affinitySchema = `
CREATE TABLE IF NOT EXISTS knowledge_project_affinity(
  knowledge_id INTEGER NOT NULL,
  project TEXT NOT NULL,
  source TEXT NOT NULL CHECK(source IN ('origin','evidence','used')),
  PRIMARY KEY(knowledge_id, project, source)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS knowledge_project_affinity_project ON knowledge_project_affinity(project, knowledge_id);
CREATE INDEX IF NOT EXISTS origin_contexts_session ON origin_contexts(session_ref) WHERE session_ref != '';
CREATE INDEX IF NOT EXISTS knowledge_broad_session ON knowledge(session_ref) WHERE project = '' AND session_ref != '';

CREATE TRIGGER IF NOT EXISTS knowledge_affinity_evidence AFTER INSERT ON knowledge_evidence
BEGIN
  INSERT OR IGNORE INTO knowledge_project_affinity(knowledge_id, project, source)
  SELECT NEW.knowledge_id, s.project, 'evidence' FROM sessions s
   WHERE s.id = NEW.session_id AND s.project != ''
     AND EXISTS(SELECT 1 FROM knowledge k WHERE k.id = NEW.knowledge_id AND k.project = '');
END;

CREATE TRIGGER IF NOT EXISTS knowledge_affinity_insert AFTER INSERT ON knowledge WHEN NEW.project = ''
BEGIN
  INSERT OR IGNORE INTO knowledge_project_affinity(knowledge_id, project, source)
  SELECT NEW.id, s.project, 'origin' FROM sessions s
   WHERE s.project != '' AND s.external_id != ''
     AND (s.external_id = NEW.session_ref
          OR s.external_id = (SELECT oc.session_ref FROM origin_contexts oc WHERE oc.id = NEW.origin_id));
END;

CREATE TRIGGER IF NOT EXISTS knowledge_affinity_update AFTER UPDATE OF origin_id, session_ref, project ON knowledge WHEN NEW.project = ''
BEGIN
  INSERT OR IGNORE INTO knowledge_project_affinity(knowledge_id, project, source)
  SELECT NEW.id, s.project, 'origin' FROM sessions s
   WHERE s.project != '' AND s.external_id != ''
     AND (s.external_id = NEW.session_ref
          OR s.external_id = (SELECT oc.session_ref FROM origin_contexts oc WHERE oc.id = NEW.origin_id));
END;

CREATE TRIGGER IF NOT EXISTS knowledge_affinity_session_insert AFTER INSERT ON sessions WHEN NEW.project != ''
BEGIN
  INSERT OR IGNORE INTO knowledge_project_affinity(knowledge_id, project, source)
  SELECT k.id, NEW.project, 'origin' FROM knowledge k WHERE k.project = '' AND k.session_ref = NEW.external_id
  UNION
  SELECT k.id, NEW.project, 'origin' FROM origin_contexts oc JOIN knowledge k ON k.origin_id = oc.id
   WHERE oc.session_ref = NEW.external_id AND k.project = ''
  UNION
  SELECT e.knowledge_id, NEW.project, 'evidence' FROM knowledge_evidence e JOIN knowledge k ON k.id = e.knowledge_id
   WHERE e.session_id = NEW.id AND k.project = '';
END;

CREATE TRIGGER IF NOT EXISTS knowledge_affinity_session_update AFTER UPDATE OF project ON sessions WHEN NEW.project != ''
BEGIN
  INSERT OR IGNORE INTO knowledge_project_affinity(knowledge_id, project, source)
  SELECT k.id, NEW.project, 'origin' FROM knowledge k WHERE k.project = '' AND k.session_ref = NEW.external_id
  UNION
  SELECT k.id, NEW.project, 'origin' FROM origin_contexts oc JOIN knowledge k ON k.origin_id = oc.id
   WHERE oc.session_ref = NEW.external_id AND k.project = ''
  UNION
  SELECT e.knowledge_id, NEW.project, 'evidence' FROM knowledge_evidence e JOIN knowledge k ON k.id = e.knowledge_id
   WHERE e.session_id = NEW.id AND k.project = '';
END;
`

// affinityBuild füllt die Tabelle aus dem Bestand: dieselben drei Beziehungen wie
// die Trigger, über alle breiten Einträge auf einmal. INSERT OR IGNORE macht den
// Lauf wiederholbar.
const affinityBuild = `
INSERT OR IGNORE INTO knowledge_project_affinity(knowledge_id, project, source)
SELECT k.id, s.project, 'origin' FROM knowledge k JOIN sessions s ON s.external_id = k.session_ref
 WHERE k.project = '' AND k.session_ref != '' AND s.project != ''
UNION
SELECT k.id, s.project, 'origin' FROM knowledge k JOIN origin_contexts oc ON oc.id = k.origin_id
  JOIN sessions s ON s.external_id = oc.session_ref
 WHERE k.project = '' AND oc.session_ref != '' AND s.project != ''
UNION
SELECT e.knowledge_id, s.project, 'evidence' FROM knowledge_evidence e
  JOIN knowledge k ON k.id = e.knowledge_id JOIN sessions s ON s.id = e.session_id
 WHERE k.project = '' AND s.project != ''`

// ensureProjectAffinity legt Tabelle, Spalte und Trigger additiv an, beliebig oft
// aufrufbar. Beim ersten Mal baut es die Tabelle aus dem Bestand. Der Aufbau
// läuft nur dann, wenn die Tabelle neu ist: später halten die Trigger sie
// aktuell, und ein erneuter Aufbau (RebuildProjectAffinity) ist ein Befehl.
func ensureProjectAffinity(db *sql.DB) error {
	var existed int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='knowledge_project_affinity'`).Scan(&existed); err != nil {
		return err
	}
	if err := ensureColumn(db, "knowledge", "everywhere", `INTEGER NOT NULL DEFAULT 0 CHECK(everywhere IN (0,1))`); err != nil {
		return fmt.Errorf("knowledge.everywhere: %w", err)
	}
	if _, err := db.Exec(affinitySchema); err != nil {
		return err
	}
	if existed == 0 {
		if _, err := db.Exec(affinityBuild); err != nil {
			return fmt.Errorf("build knowledge_project_affinity: %w", err)
		}
	}
	return nil
}

// RebuildProjectAffinity berechnet den Bezug aus dem Bestand neu und liefert,
// wie viele Zeilen neu entstanden sind. Gedacht für den Fall, dass Einträge vor
// den Triggern oder an ihnen vorbei entstanden sind (ein Import, eine
// Wiederherstellung).
func (s *Store) RebuildProjectAffinity() (int64, error) {
	if s.writer != nil {
		return queueValue(s, nil, func(d *Store, _ []any) (int64, error) { return d.RebuildProjectAffinity() })
	}
	res, err := s.db.Exec(affinityBuild)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetKnowledgeEverywhere setzt oder nimmt das Kennzeichen "gilt überall". Es gilt
// nur für breite Einträge: ein Projekteintrag wird ohnehin in seinem Projekt
// zugestellt und gehört nicht in jedes andere.
//
// Bewusst nicht über UpdateKnowledge: das Kennzeichen ist eine Aussage über die
// Reichweite, nicht über den Text, und eine neue Fassung dafür füllte die
// Historie mit Rauschen (wie bei SetRegressionCover).
func (s *Store) SetKnowledgeEverywhere(id int64, on bool) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, on}, func(d *Store, p []any) error { return d.SetKnowledgeEverywhere(p[0].(int64), p[1].(bool)) })
	}
	var project string
	if err := s.db.QueryRow(`SELECT project FROM knowledge WHERE id=?`, id).Scan(&project); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("knowledge %d not found", id)
		}
		return err
	}
	if project != "" {
		return fmt.Errorf("knowledge %d belongs to a project; only machine and global entries can apply everywhere", id)
	}
	v := 0
	if on {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE knowledge SET everywhere=?, updated_at=? WHERE id=?`, v, now(), id)
	return err
}

// TiedBroadKnowledge nennt die breiten Einträge, die der Startkontext des
// Projekts zustellen darf: die mit Kennzeichen "gilt überall" und die mit Bezug
// zum Projekt. Die Auswahl der Einträge selbst bleibt beim Aufrufer; das ist nur
// die Menge, nach der er sie sortiert.
func (s *Store) TiedBroadKnowledge(project string) (map[int64]bool, error) {
	if s.reader != nil {
		return s.reader.TiedBroadKnowledge(project)
	}
	rows, err := s.db.Query(`SELECT id FROM knowledge WHERE project = '' AND (everywhere = 1
		OR id IN (SELECT knowledge_id FROM knowledge_project_affinity WHERE project = ?))`, project)
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

// TriageKind sagt, warum die Durchsicht einen Eintrag nennt.
type TriageKind string

const (
	// TriageMove: ein Maschineneintrag, der in genau einem Projekt entstand. Er
	// handelt vermutlich von diesem Projekt auf dieser Maschine und gehört als
	// "Projekt plus Maschine" geschrieben (die Regel dazu steht im Regelblock).
	TriageMove TriageKind = "move"
	// TriageUnused: ein breiter Eintrag, den seit TriageUnusedDays Tagen niemand
	// über ein Wort im Prompt oder eine Suche aufgegriffen hat.
	TriageUnused TriageKind = "unused"
)

// TriageUnusedDays ist die Frist des Konzepts.
const TriageUnusedDays = 90

// TriageItem ist ein Vorschlag der Durchsicht. Der Eintrag wird nicht verändert;
// ein Mensch entscheidet: behalten, zurückziehen, verschieben, "gilt überall".
type TriageItem struct {
	Knowledge Knowledge  `json:"knowledge"`
	Kind      TriageKind `json:"kind"`
	// Project ist bei TriageMove das einzige Entstehungsprojekt.
	Project string `json:"project,omitempty"`
	// Deliveries zählt, wie oft der Eintrag laut Zustellprotokoll beim Start
	// zugestellt wurde; bei TriageUnused die Zahl, die das "ungenutzt" einordnet.
	Deliveries int `json:"deliveries,omitempty"`
}

// BroadTriage legt breite Einträge zur Durchsicht vor, ohne etwas zu ändern.
//
// TriageMove: Maschineneinträge, deren Entstehung genau ein Projekt nennt. Mit
// project nur die, deren Projekt dieses ist.
//
// TriageUnused: breite Einträge, die älter als TriageUnusedDays sind, in dieser
// Zeit über kein Wort im Prompt und keinen Hinweis zugestellt wurden
// (context_delivery_log, Weg relevance und hint) und nie als Suchtreffer
// gezählt wurden (knowledge.search_hits; einen Zeitpunkt der Suche gibt es
// nicht, ein Eintrag, der je gesucht wurde, bleibt daher draußen). "Aufgegriffen"
// im Sinn des Konzepts, also das Urteil über die Folgehandlung, kommt mit P3;
// bis dahin ist die Zustellung auf Zuruf der Ersatz. Wer das Kennzeichen
// "gilt überall" trägt, steht nie in dieser Liste. Das Protokoll hält 180 Tage,
// die Frist von 90 liegt darin.
//
// Die Sichtbarkeit prüft der Aufrufer (keep); die Zahl der Treffer wird erst
// danach begrenzt.
func (s *Store) BroadTriage(project string, now time.Time, limit int, keep func(Knowledge) bool) ([]TriageItem, error) {
	if s.reader != nil {
		return s.reader.BroadTriage(project, now, limit, keep)
	}
	if limit <= 0 {
		limit = 50
	}
	var out []TriageItem
	push := func(item TriageItem) bool {
		if keep != nil && !keep(item.Knowledge) {
			return false
		}
		out = append(out, item)
		return true
	}

	// Verschieben: genau ein Entstehungsprojekt.
	rows, err := s.db.Query(`SELECT a.knowledge_id, MIN(a.project) FROM knowledge_project_affinity a
		JOIN knowledge k ON k.id = a.knowledge_id
		WHERE a.source = 'origin' AND k.project = '' AND k.machine != '' AND k.status = 'active'
		GROUP BY a.knowledge_id HAVING COUNT(DISTINCT a.project) = 1`)
	if err != nil {
		return nil, err
	}
	moveProject := map[int64]string{}
	var moveIDs []int64
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			rows.Close()
			return nil, err
		}
		if project != "" && p != project {
			continue
		}
		moveProject[id] = p
		moveIDs = append(moveIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	sort.Slice(moveIDs, func(i, j int) bool { return moveIDs[i] < moveIDs[j] })
	ks, err := s.knowledgeByIDs(moveIDs)
	if err != nil {
		return nil, err
	}
	for _, k := range ks {
		if len(out) >= limit {
			break
		}
		push(TriageItem{Knowledge: k, Kind: TriageMove, Project: moveProject[k.ID]})
	}

	// Ungenutzt.
	cutoff := now.UTC().Add(-TriageUnusedDays * 24 * time.Hour).Format(time.RFC3339)
	rows, err = s.db.Query(`SELECT k.id FROM knowledge k
		WHERE k.project = '' AND k.status = 'active' AND k.everywhere = 0
		  AND k.search_hits = 0 AND k.created_at < ?
		  AND NOT EXISTS(SELECT 1 FROM context_delivery_log l
		        WHERE l.kind = 'knowledge' AND l.object_id = k.id AND l.via IN ('relevance','hint') AND l.delivered_at >= ?)
		ORDER BY k.id`, cutoff, cutoff)
	if err != nil {
		return nil, err
	}
	var unusedIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		if _, moved := moveProject[id]; moved {
			continue
		}
		unusedIDs = append(unusedIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	ks, err = s.knowledgeByIDs(unusedIDs)
	if err != nil {
		return nil, err
	}
	for _, k := range ks {
		if len(out) >= limit {
			break
		}
		n, err := s.bootstrapDeliveries(k.ID, cutoff)
		if err != nil {
			return nil, err
		}
		push(TriageItem{Knowledge: k, Kind: TriageUnused, Deliveries: n})
	}
	return out, nil
}

// bootstrapDeliveries zählt die Zustellungen eines Eintrags beim Start seit dem
// Zeitpunkt.
func (s *Store) bootstrapDeliveries(id int64, since string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM context_delivery_log WHERE kind='knowledge' AND object_id=? AND via='bootstrap' AND delivered_at >= ?`, id, since).Scan(&n)
	return n, err
}

// knowledgeByIDs liest Einträge in der Reihenfolge der Nummern, die fehlenden
// fehlen.
func (s *Store) knowledgeByIDs(ids []int64) ([]Knowledge, error) {
	var out []Knowledge
	for len(ids) > 0 {
		part := ids
		if len(part) > 500 {
			part = part[:500]
		}
		ids = ids[len(part):]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		rows, err := s.db.Query(`SELECT `+knowledgeCols+` FROM knowledge WHERE id IN (`+placeholders(len(part))+`) ORDER BY id`, args...)
		if err != nil {
			return nil, err
		}
		ks, err := s.scanKnowledge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ks...)
	}
	return out, nil
}
