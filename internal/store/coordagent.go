package store

import "strings"

// CoordAgent ist eine angemeldete Session, kein von ghosttree gestarteter
// Prozess. Niemand hier ist Parent eines anderen: ParentExternalID erklärt die
// Herkunft eines Subagenten und bestimmt nicht, wer ihn erreichen darf.
type CoordAgent struct {
	ID               int64  `json:"id,omitempty"`
	ExternalID       string `json:"external_id"`
	Provider         string `json:"provider"`
	RoomKey          string `json:"room_key"`
	DisplayName      string `json:"display_name"`
	Person           string `json:"person,omitempty"`
	Cwd              string `json:"cwd,omitempty"`
	Branch           string `json:"branch,omitempty"`
	Worktree         string `json:"worktree,omitempty"`
	ParentExternalID string `json:"parent_external_id,omitempty"`
	Capabilities     string `json:"capabilities,omitempty"`
	RegisteredAt     string `json:"registered_at,omitempty"`
	LastSeenAt       string `json:"last_seen_at,omitempty"`
}

// RoomKeyForProject bildet den Projektraum aus der normalisierten Remote.
// Absichtlich nicht aus dem cwd: zwei Agenten in packages/server und apps/web
// gehören in denselben Raum, und derselbe Pfad auf zwei Maschinen nicht.
// Die Normalisierung selbst gehört zu scope und wird hier vorausgesetzt.
func RoomKeyForProject(remote string) string {
	return "project:" + strings.TrimSpace(remote)
}

// RoomKeyForMachine ist der Raum für Agenten ohne Repository und für alles,
// was den ganzen Rechner betrifft. Er ist an den Host gebunden und nicht an
// "alle an diesem Server": sonst bedeutet "ich starte Postgres neu" für einen
// fremden Agenten irgendwann etwas anderes als für den eigenen.
func RoomKeyForMachine(host string) string {
	return "machine:" + strings.TrimSpace(host)
}

// RegisterCoordAgent meldet eine Session in ihrem Raum an. Die Anmeldung ist
// idempotent über die external_id: derselbe Aufruf nach einem Neustart ist
// dieselbe Session, nicht die zweite. Ohne das stünde nach jeder Sitzung ein
// Geist mehr in der Teilnehmerliste.
func (s *Store) RegisterCoordAgent(a CoordAgent) (int64, error) {
	if s.writer != nil {
		return queueValue(s, []any{a}, func(d *Store, p []any) (int64, error) {
			return d.RegisterCoordAgent(p[0].(CoordAgent))
		})
	}
	at := a.RegisteredAt
	if at == "" {
		at = now()
	}
	// registered_at bleibt beim Konflikt stehen: wann diese Session zuerst da
	// war, ist eine andere Auskunft als wann sie zuletzt gesehen wurde, und
	// die erste geht beim Überschreiben sonst verloren.
	_, err := s.db.Exec(`INSERT INTO coord_agents(
			external_id,provider,room_key,display_name,person,cwd,branch,worktree,
			parent_external_id,capabilities,registered_at,last_seen_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(external_id) DO UPDATE SET
			room_key=excluded.room_key, display_name=excluded.display_name,
			cwd=excluded.cwd, branch=excluded.branch, worktree=excluded.worktree,
			capabilities=excluded.capabilities, last_seen_at=excluded.last_seen_at`,
		a.ExternalID, a.Provider, a.RoomKey, a.DisplayName, a.Person, a.Cwd,
		a.Branch, a.Worktree, a.ParentExternalID, a.Capabilities, at, at)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRow(`SELECT id FROM coord_agents WHERE external_id=?`, a.ExternalID).Scan(&id)
	return id, err
}

// CoordPeers liefert die Teilnehmer eines Raums. since grenzt auf zuletzt
// gesehene Agenten ein und darf leer sein — wer alle will, bekommt alle,
// einschließlich der lange stillen.
//
// Was diese Liste bewusst NICHT sagt: ob jemand erreichbar ist. last_seen_at
// ist eine Beobachtung, kein Lebenszeichen. Ein ausbleibender Eintrag kann
// eine gekappte Verbindung sein, und ein lebender Prozess beweist nicht, dass
// ein Modell arbeitet. Erreichbarkeit bekommt erst dann eine Darstellung,
// wenn gemessen ist, was sie je Harness überhaupt heißen kann.
func (s *Store) CoordPeers(roomKey, since string) ([]CoordAgent, error) {
	if s.reader != nil {
		return s.reader.CoordPeers(roomKey, since)
	}
	query := `SELECT id,external_id,provider,room_key,display_name,
			COALESCE(person,''),COALESCE(cwd,''),COALESCE(branch,''),
			COALESCE(worktree,''),COALESCE(parent_external_id,''),
			COALESCE(capabilities,''),registered_at,last_seen_at
		FROM coord_agents WHERE room_key=?`
	args := []any{roomKey}
	if strings.TrimSpace(since) != "" {
		query += ` AND last_seen_at >= ?`
		args = append(args, since)
	}
	query += ` ORDER BY last_seen_at DESC, id`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CoordAgent
	for rows.Next() {
		var a CoordAgent
		if err := rows.Scan(&a.ID, &a.ExternalID, &a.Provider, &a.RoomKey,
			&a.DisplayName, &a.Person, &a.Cwd, &a.Branch, &a.Worktree,
			&a.ParentExternalID, &a.Capabilities, &a.RegisteredAt, &a.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
