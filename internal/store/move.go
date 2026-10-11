package store

import (
	"errors"
	"fmt"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// ErrNothingToMove: der Eintrag liegt schon dort.
var ErrNothingToMove = errors.New("the entry is already there")

// KnowledgeMove ist eine Zeile im Protokoll eines Wissenseintrags: wer ihn
// wohin gelegt hat. Wissen hat kein Aktivitätsprotokoll wie ein Request; die
// Fassungen halten nur Text, und eine Verschiebung ändert keinen Text.
type KnowledgeMove struct {
	ID          int64  `json:"id"`
	KnowledgeID int64  `json:"knowledge_id"`
	From        string `json:"from"`
	To          string `json:"to"`
	MovedBy     string `json:"moved_by"`
	MovedAt     string `json:"moved_at"`
}

// MoveKnowledge legt einen Eintrag in ein anderes Projekt, auf eine andere
// Maschine oder global. Der Zweig bleibt nicht stehen: er gehört zum alten
// Projekt. Die Rechtsprüfung (Quelle und Ziel) liegt beim Aufrufer.
func (s *Store) MoveKnowledge(id int64, to scope.Axes, person string) error {
	to = scope.CanonicalAxes(scope.Axes{Project: to.Project, Machine: to.Machine})
	if s.writer != nil {
		return queueWrite(s, []any{id, to, person}, func(d *Store, p []any) error {
			return d.MoveKnowledge(p[0].(int64), p[1].(scope.Axes), p[2].(string))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var from scope.Axes
	if err := tx.QueryRow(`SELECT project, branch, machine FROM knowledge WHERE id=?`, id).Scan(&from.Project, &from.Branch, &from.Machine); err != nil {
		return err
	}
	if from.Same(to) {
		return ErrNothingToMove
	}
	ts := now()
	if _, err := tx.Exec(`UPDATE knowledge SET project=?, branch='', machine=?, updated_at=?, last_modified_by=? WHERE id=?`,
		to.Project, to.Machine, ts, person, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE search_documents SET project=?, branch='', machine=? WHERE kind='knowledge' AND domain_id=?`,
		to.Project, to.Machine, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO knowledge_moves(knowledge_id, from_scope, to_scope, moved_by, moved_at) VALUES(?,?,?,?,?)`,
		id, from.Label(), to.Label(), person, ts); err != nil {
		return err
	}
	return tx.Commit()
}

// KnowledgeMoves liefert das Verschiebe-Protokoll eines Eintrags, neu zuerst.
func (s *Store) KnowledgeMoves(id int64) ([]KnowledgeMove, error) {
	if s.reader != nil {
		return s.reader.KnowledgeMoves(id)
	}
	rows, err := s.db.Query(`SELECT id, knowledge_id, from_scope, to_scope, moved_by, moved_at
		FROM knowledge_moves WHERE knowledge_id=? ORDER BY id DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KnowledgeMove
	for rows.Next() {
		var m KnowledgeMove
		if err := rows.Scan(&m.ID, &m.KnowledgeID, &m.From, &m.To, &m.MovedBy, &m.MovedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MoveRequest legt einen Auftrag in ein anderes Projekt und schreibt das in sein
// Aktivitätsprotokoll. Ein Auftrag gehört immer zu einem Projekt: das Ziel ist
// Pflicht. Zweig und Maschine des Auftrags gehören zum alten Projekt und
// entfallen. Die Rechtsprüfung (Quelle und Ziel) liegt beim Aufrufer.
func (s *Store) MoveRequest(id int64, project, person, reason string) error {
	project = scope.NormalizeRemote(project)
	if project == "" {
		return requestdomain.NewRuleError("project_required", "a request belongs to a project", "name the project to move it to", nil)
	}
	if s.writer != nil {
		return queueWrite(s, []any{id, project, person, reason}, func(d *Store, p []any) error {
			return d.MoveRequest(p[0].(int64), p[1].(string), p[2].(string), p[3].(string))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var from scope.Axes
	if err := tx.QueryRow(`SELECT project, branch, machine FROM requests WHERE id=?`, id).Scan(&from.Project, &from.Branch, &from.Machine); err != nil {
		return err
	}
	if from.Same(scope.Axes{Project: project}) {
		return ErrNothingToMove
	}
	ts := now()
	if _, err := tx.Exec(`UPDATE requests SET project=?, branch='', machine='', updated_at=? WHERE id=?`, project, ts, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE search_documents SET project=?, branch='', machine='' WHERE kind='request' AND domain_id=?`, project, id); err != nil {
		return err
	}
	data := fmt.Sprintf("%s → %s", from.Label(), project)
	if strings.TrimSpace(reason) != "" {
		data += " — " + strings.TrimSpace(reason)
	}
	if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at) VALUES(?,'request.moved',?,?,?)`, id, person, data, ts); err != nil {
		return err
	}
	return tx.Commit()
}

// WritableProjects nennt die Projekte, in die das Konto Wissen UND Aufträge
// schreiben darf: dieselbe Matrix wie beim Schreiben im eigenen Projekt, nicht
// eine zweite Regel. Grundlage der Zielangabe "project" beim Schreiben und
// Verschieben. Nur Projekte mit Zeile; eine unbekannte Remote gehört niemandem
// und ist kein Ziel.
func (a *ProjectAccess) WritableProjects() ([]string, error) {
	projects, err := a.st.ListProjects(a.principal.ID, 0)
	if err != nil {
		return nil, err
	}
	a.Filtered()
	out := []string{}
	for _, p := range projects {
		own := Object{Own: true}
		if a.Decide(p.Remote, ResKnowledge, ActCreate, own).Allowed && a.Decide(p.Remote, ResRequest, ActCreate, own).Allowed {
			out = append(out, p.Remote)
		}
	}
	return out, nil
}

// ownNoteOnUnclaimedMachine: ein Hinweis auf eine Maschine, auf der nie ein
// Agent lief (etwa ein ssh-Ziel), steht unter einem Namen ohne Besitzer. Dort
// gilt der Instanz-Owner als Besitzer, und jedes andere Konto könnte seine
// eigene Notiz nicht mehr lesen. Der Autor darf sie lesen und pflegen, bis ein
// Konto die Maschine beansprucht; wer sie beansprucht, ist ihr Besitzer. Das
// Recht zu bestätigen (verify) folgt daraus nicht.
func (a *ProjectAccess) ownNoteOnUnclaimedMachine(obj Object, act Action) bool {
	if !obj.Own || act == ActVerify {
		return false
	}
	a.loadMachines()
	_, claimed := a.machines[canonicalMachine(obj.Machine)]
	return !claimed
}
