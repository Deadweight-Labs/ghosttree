package store

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

var (
	// ErrNotCurrent: der Eintrag ist nicht mehr gültig (abgelöst, zurückgezogen,
	// archiviert) und lässt sich deshalb weder ablösen noch zurückziehen.
	ErrNotCurrent = errors.New("the entry is no longer current")
	// ErrReasonRequired: Ablösen und Zurückziehen verlangen eine Begründung.
	ErrReasonRequired = errors.New("a reason is required")
)

// KnowledgeRetirement ist eine Zeile im Protokoll eines Eintrags: wer ihn
// warum zurückgezogen hat. Die Fassungen halten nur Text, eine Statusänderung
// trägt keine Begründung.
type KnowledgeRetirement struct {
	ID          int64  `json:"id"`
	KnowledgeID int64  `json:"knowledge_id"`
	Reason      string `json:"reason"`
	RetiredBy   string `json:"retired_by"`
	RetiredAt   string `json:"retired_at"`
}

func currentStatus(status string) bool { return status == "active" || status == "stale" }

// SupersedeKnowledge löst ältere Einträge durch einen neueren ab, in einer
// Transaktion: entweder alle oder keiner. Die Kante ist eine supersedes-
// Relation mit Begründung (REQ-157); ausgeliefert wird weiter über status und
// superseded_by, die hier mitgesetzt werden. Die abgelösten Fassungen bleiben
// über Historie und Detail erreichbar. Die Rechtsprüfung liegt beim Aufrufer.
func (s *Store) SupersedeKnowledge(newID int64, oldIDs []int64, reason string, actor RelationActor) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: say why the older entries are superseded", ErrReasonRequired)
	}
	if len(oldIDs) == 0 {
		return fmt.Errorf("%w: name the entries to supersede", ErrRelationInvalid)
	}
	if s.writer != nil {
		return queueWrite(s, []any{newID, oldIDs, reason, actor}, func(d *Store, p []any) error {
			return d.SupersedeKnowledge(p[0].(int64), p[1].([]int64), p[2].(string), p[3].(RelationActor))
		})
	}
	if err := actor.valid(); err != nil {
		return err
	}
	olds := slices.Clone(oldIDs)
	slices.Sort(olds)
	olds = slices.Compact(olds)
	origin := RelOriginHuman
	if actor.Via == "mcp" {
		origin = RelOriginAgent
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRow(`SELECT status FROM knowledge WHERE id=?`, newID).Scan(&status); err != nil {
		return fmt.Errorf("%w: no entry #%d", ErrRelationInvalid, newID)
	}
	if !currentStatus(status) {
		return fmt.Errorf("%w: #%d is %s and cannot supersede another entry", ErrNotCurrent, newID, status)
	}
	ts := now()
	for _, id := range olds {
		if err := tx.QueryRow(`SELECT status FROM knowledge WHERE id=?`, id).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: no entry #%d", ErrRelationInvalid, id)
			}
			return err
		}
		if !currentStatus(status) {
			return fmt.Errorf("%w: #%d is %s", ErrNotCurrent, id, status)
		}
		if _, err := addRelationTx(tx, RelationInput{FromID: newID, ToID: id, Kind: RelSupersedes, State: RelActive, Origin: origin, Reason: reason}, actor); err != nil {
			return err
		}
		if err := archiveKnowledgeTx(tx, id, actor.Name, ts); err != nil {
			return err
		}
		if err := markSupersededTx(tx, id, newID, ts); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE knowledge SET last_modified_by=? WHERE id=?`, actor.Name, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RetireKnowledge zieht einen Eintrag zurück, dessen beschriebener Zustand
// nicht mehr existiert: derselbe Status (deprecated) wie Zurückziehen im Web,
// dazu die Begründung im Protokoll. Die Rechtsprüfung liegt beim Aufrufer.
func (s *Store) RetireKnowledge(id int64, reason, by string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("%w: say why the entry is retired", ErrReasonRequired)
	}
	if s.writer != nil {
		return queueWrite(s, []any{id, reason, by}, func(d *Store, p []any) error {
			return d.RetireKnowledge(p[0].(int64), p[1].(string), p[2].(string))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRow(`SELECT status FROM knowledge WHERE id=?`, id).Scan(&status); err != nil {
		return err
	}
	if !currentStatus(status) {
		return fmt.Errorf("%w: #%d is %s", ErrNotCurrent, id, status)
	}
	ts := now()
	if err := archiveKnowledgeTx(tx, id, by, ts); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE knowledge SET status='deprecated', updated_at=?, last_modified_by=? WHERE id=?`, ts, by, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO knowledge_retirements(knowledge_id, reason, retired_by, retired_at) VALUES(?,?,?,?)`, id, reason, by, ts); err != nil {
		return err
	}
	return tx.Commit()
}

// KnowledgeRetirements liefert das Rückzugs-Protokoll eines Eintrags, neu zuerst.
func (s *Store) KnowledgeRetirements(id int64) ([]KnowledgeRetirement, error) {
	if s.reader != nil {
		return s.reader.KnowledgeRetirements(id)
	}
	rows, err := s.db.Query(`SELECT id, knowledge_id, reason, retired_by, retired_at
		FROM knowledge_retirements WHERE knowledge_id=? ORDER BY id DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KnowledgeRetirement
	for rows.Next() {
		var r KnowledgeRetirement
		if err := rows.Scan(&r.ID, &r.KnowledgeID, &r.Reason, &r.RetiredBy, &r.RetiredAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
