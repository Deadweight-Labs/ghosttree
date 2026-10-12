package store

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

var (
	// ErrNotCurrent: der Eintrag ist nicht mehr gültig (abgelöst, zurückgezogen,
	// archiviert) und lässt sich deshalb weder ablösen noch zurückziehen.
	ErrNotCurrent = errors.New("the entry is no longer current")
	// ErrReasonRequired: Ablösen und Zurückziehen verlangen eine Begründung.
	ErrReasonRequired = errors.New("a reason is required")
	// ErrNotDeliverable: a replacement that is held back (staged, quarantined)
	// would hide the old entry without anything taking its place.
	ErrNotDeliverable = errors.New("a held-back entry cannot supersede another")
	// ErrScopeMismatch: a replacement must live exactly where the old entry
	// lives, or it hides project-wide knowledge from some readers only.
	ErrScopeMismatch = errors.New("the replacement must have exactly the scope of the entry it supersedes")
	// ErrHasPredecessors: retiring the head of a supersede chain would leave the
	// entries it replaced hidden for good.
	ErrHasPredecessors = errors.New("other entries were superseded by this one")
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
	if err := checkSupersedeInput(oldIDs, reason); err != nil {
		return err
	}
	if s.writer != nil {
		return queueWrite(s, []any{newID, oldIDs, reason, actor}, func(d *Store, p []any) error {
			return d.SupersedeKnowledge(p[0].(int64), p[1].([]int64), p[2].(string), p[3].(RelationActor))
		})
	}
	if err := actor.valid(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := supersedeTx(tx, newID, oldIDs, reason, actor); err != nil {
		return err
	}
	return tx.Commit()
}

// InsertKnowledgeSuperseding writes an entry and supersedes older ones in ONE
// transaction: if anything fails, nothing is written, so a refused supersede
// never leaves a follow-up entry behind. Status and superseded_by of the new
// entry are forced (a replacement is live), and the effective confidence, after
// the defaults, must be deliverable.
func (s *Store) InsertKnowledgeSuperseding(k Knowledge, oldIDs []int64, reason string, actor RelationActor) (int64, error) {
	if err := checkSupersedeInput(oldIDs, reason); err != nil {
		return 0, err
	}
	if err := activation.ValidateRule(k.Activation); err != nil {
		return 0, err
	}
	if k.Type != "instruction" && len(k.Activation.Paths) > 0 {
		return 0, fmt.Errorf("activation requires instruction, got %s", k.Type)
	}
	if s.writer != nil {
		return queueValue(s, []any{k, oldIDs, reason, actor}, func(d *Store, p []any) (int64, error) {
			return d.InsertKnowledgeSuperseding(p[0].(Knowledge), p[1].([]int64), p[2].(string), p[3].(RelationActor))
		})
	}
	if err := actor.valid(); err != nil {
		return 0, err
	}
	k.Status, k.SupersededBy = "", 0
	k = withKnowledgeDefaults(k)
	if k.Confidence != "trusted" && k.Confidence != "verified" {
		return 0, fmt.Errorf("%w: it would be %s", ErrNotDeliverable, k.Confidence)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := insertKnowledgeTx(tx, k)
	if err != nil {
		return 0, err
	}
	if err := supersedeTx(tx, id, oldIDs, reason, actor); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func checkSupersedeInput(oldIDs []int64, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: say why the older entries are superseded", ErrReasonRequired)
	}
	if len(oldIDs) == 0 {
		return fmt.Errorf("%w: name the entries to supersede", ErrRelationInvalid)
	}
	return nil
}

func supersedeTx(tx *sql.Tx, newID int64, oldIDs []int64, reason string, actor RelationActor) error {
	olds := slices.Clone(oldIDs)
	slices.Sort(olds)
	olds = slices.Compact(olds)
	origin := RelOriginHuman
	if actor.Via == "mcp" {
		origin = RelOriginAgent
	}
	var status string
	var at scope.Axes
	if err := tx.QueryRow(`SELECT status, project, branch, machine FROM knowledge WHERE id=?`, newID).Scan(&status, &at.Project, &at.Branch, &at.Machine); err != nil {
		return fmt.Errorf("%w: no entry #%d", ErrRelationInvalid, newID)
	}
	if !currentStatus(status) {
		return fmt.Errorf("%w: #%d is %s and cannot supersede another entry", ErrNotCurrent, newID, status)
	}
	ts := now()
	for _, id := range olds {
		var old scope.Axes
		if err := tx.QueryRow(`SELECT status, project, branch, machine FROM knowledge WHERE id=?`, id).Scan(&status, &old.Project, &old.Branch, &old.Machine); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: no entry #%d", ErrRelationInvalid, id)
			}
			return err
		}
		if !currentStatus(status) {
			return fmt.Errorf("%w: #%d is %s", ErrNotCurrent, id, status)
		}
		if !old.Same(at) {
			return fmt.Errorf("%w: #%d is %s, the new entry is %s; move it first, then supersede", ErrScopeMismatch, id, old.Label(), at.Label())
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
	return nil
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
	var heads int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM knowledge WHERE superseded_by=?`, id).Scan(&heads); err != nil {
		return err
	}
	if heads > 0 {
		return fmt.Errorf("%w: retire those first, or supersede #%d again instead", ErrHasPredecessors, id)
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
