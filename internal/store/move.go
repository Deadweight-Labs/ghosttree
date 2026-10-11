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
// eigene Notiz nicht mehr lesen. Der Autor darf seine Notiz immer lesen und
// pflegen, auch wenn ein anderes Konto die Maschine später beansprucht (sie
// wird dann nicht mehr ausgeliefert, siehe machineAuthorDelivers); anlegen darf
// er nur, solange niemand die Maschine beansprucht hat. Verify folgt daraus nie.
func (a *ProjectAccess) ownNoteOnUnclaimedMachine(obj Object, act Action) bool {
	if !obj.Own {
		return false
	}
	switch act {
	case ActRead, ActEdit:
		return true
	case ActCreate:
		a.loadMachines()
		_, claimed := a.machines[canonicalMachine(obj.Machine)]
		return !claimed
	}
	return false
}

// machineDeliveryIndex: Maschinenname -> Konto-Id des Besitzers, die Konto-Id des
// Instanz-Owners (für Namen ohne Zeile und Altbestand) und Personenname ->
// Konto-Id. Ein Name zählt in Kleinschreibung, mit dem heutigen Namen eines
// Kontos und seinen früheren aus person_name_history; der heutige Name gewinnt,
// wenn ein alter Name inzwischen ein anderes Konto trägt.
func (s *Store) machineDeliveryIndex() (map[string]int64, int64, map[string]int64) {
	if s.reader != nil {
		return s.reader.machineDeliveryIndex()
	}
	owner := instanceOwnerID(s.db)
	owners := map[string]int64{}
	if rows, err := s.db.Query(`SELECT hostname, account_id FROM machines`); err == nil {
		for rows.Next() {
			var host string
			var id int64
			if rows.Scan(&host, &id) == nil {
				owners[canonicalMachine(host)] = effectiveOwner(id, owner)
			}
		}
		rows.Close()
	}
	names := map[string]int64{}
	if rows, err := s.db.Query(`SELECT person_id, old_name FROM person_name_history`); err == nil {
		for rows.Next() {
			var id int64
			var name string
			if rows.Scan(&id, &name) == nil {
				names[strings.ToLower(strings.TrimSpace(name))] = id
			}
		}
		rows.Close()
	}
	if rows, err := s.db.Query(`SELECT id, name FROM persons`); err == nil {
		for rows.Next() {
			var id int64
			var name string
			if rows.Scan(&id, &name) == nil {
				names[strings.ToLower(strings.TrimSpace(name))] = id
			}
		}
		rows.Close()
	}
	return owners, owner, names
}

// machineAuthorDelivers: Wissen für eine Maschine wird nur ausgeliefert, wenn
// sein Autor zum Zeitpunkt der Auslieferung Besitzer der Maschine ist. Sonst
// könnte ein Konto eine Anweisung auf einen noch freien Maschinennamen legen,
// und wer den Namen später beansprucht, bekäme sie als bindende Anweisung.
//
// Der Autor wird ohne Groß-/Kleinschreibung und über frühere Kontonamen
// gefunden. Ein Autor, der auf kein Konto zeigt (Altbestand mit dem Label
// "claude"), gehört dem Instanz-Owner wie der Rest des Altbestands. Ein Eintrag
// ganz ohne Autor (Destillat) wird wie bisher ausgeliefert. Der Eintrag bleibt
// für seinen Autor sichtbar, wird nur nicht zugestellt.
func (a *ProjectAccess) machineAuthorDelivers(k Knowledge) bool {
	if strings.TrimSpace(k.Person) == "" {
		// Distilled entries carry no author and belong to the machine they were
		// distilled on; they keep reaching it.
		return true
	}
	a.ownersOnce.Do(func() { a.ownerIDs, a.ownerDefault, a.authorIDs = a.st.machineDeliveryIndex() })
	author, known := a.authorIDs[strings.ToLower(strings.TrimSpace(k.Person))]
	if !known {
		author = a.ownerDefault
	}
	owner, claimed := a.ownerIDs[canonicalMachine(k.Scope.Machine)]
	if !claimed {
		owner = a.ownerDefault
	}
	return owner == author
}

// MayMakeGlobal: ein Eintrag aus einem Projekt wird nur von Lead aufwärts (oder
// dem Instanz-Admin) nach global gelegt. Global erreicht jede Session jedes
// Kontos, also mehr Leser als das Projekt, aus dem er kommt; ein Mitglied darf
// den eigenen Eintrag verschieben, aber nicht so weit ausspielen.
func (a *ProjectAccess) MayMakeGlobal(k Knowledge) bool {
	if k.Scope.Project == "" || a.IsAdmin() {
		return true
	}
	return RoleRank(a.Role(k.Scope.Project).Role) >= 3
}
