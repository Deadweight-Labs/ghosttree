package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var (
	ErrCoordAgentOwned        = errors.New("coordination agent belongs to another principal")
	ErrCoordAgentScopeChanged = errors.New("coordination agent cannot switch public scope")
)

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
	PrincipalID      string `json:"principal_id,omitempty"`
	Cwd              string `json:"cwd,omitempty"`
	Branch           string `json:"branch,omitempty"`
	Worktree         string `json:"worktree,omitempty"`
	ParentExternalID string `json:"parent_external_id,omitempty"`
	Capabilities     string `json:"capabilities,omitempty"`
	RegisteredAt     string `json:"registered_at,omitempty"`
	LastSeenAt       string `json:"last_seen_at,omitempty"`
	// Owner ist der Kontoname des registrierenden Tokens, nur in Antworten.
	Owner string `json:"owner,omitempty"`
	// Role: bei der Anmeldung die angeforderte Rolle (lead, member, guest; leer
	// lässt eine bestehende unverändert), in Peer-Antworten die live berechnete
	// effektive Rolle im Projekt des Raums. In Räumen ohne Projekt leer.
	Role string `json:"role,omitempty"`
	// RequestedRole ist in Peer-Antworten die gespeicherte Anforderung.
	RequestedRole string `json:"requested_role,omitempty"`
	// CanReview ist in Peer-Antworten das Prüfer-Flag des Kontos.
	CanReview bool `json:"can_review,omitempty"`
	// SessionID ist die Transkript-Session des Harness, die der Agent bei der
	// Anmeldung meldet. Nur Eingabe: Peer-Antworten tragen sie nie.
	SessionID string `json:"session_id,omitempty"`
	// Presence ist nur in Peer-Antworten gesetzt, beim Lesen abgeleitet.
	Presence *Presence `json:"presence,omitempty"`
}

// maxExternalIDLen begrenzt Agenten-IDs.
const maxExternalIDLen = 160

// ReservedExternalID: der Präfix "system:" gehört dem Store (Systemmeldungen,
// etwa über Wartekreise). Kein Client darf sich so anmelden oder so senden;
// sonst könnte er sich als System ausgeben. Alt-Bestand, der so heißt, wird
// nicht gelöscht, kann aber nicht mehr senden.
func ReservedExternalID(id string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(id)), "system:")
}

// ValidExternalID sagt, ob eine Agenten-ID zulässig ist: Buchstaben, Ziffern und
// : . _ - und /, höchstens 160 Zeichen. Das deckt claude:<host>:<uuid>,
// codex:<...>, cli:<host> und Subagenten (<session>/<name>) ab. Die ID steht in Kopfzeilen von Texten, die
// Agenten lesen, und darf deshalb keine Zeilenumbrüche, Klammern oder Leerraum
// tragen.
func ValidExternalID(id string) bool {
	if id == "" || len(id) > maxExternalIDLen || ReservedExternalID(id) {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == ':', r == '.', r == '_', r == '-', r == '/':
		default:
			return false
		}
	}
	return true
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

// RegisterCoordAgent meldet eine Session in einem weiteren Raum an. Die
// Anmeldung ist idempotent über external_id und atomar an PrincipalID gebunden.
// room_key auf coord_agents bleibt als Kompatibilitätswert der Erstanmeldung
// unverändert; die normalisierte Mitgliedschaft ist die aktuelle Wahrheit.
func (s *Store) RegisterCoordAgent(a CoordAgent) (int64, error) {
	if s.writer != nil {
		return queueValue(s, []any{a}, func(d *Store, p []any) (int64, error) {
			return d.RegisterCoordAgent(p[0].(CoordAgent))
		})
	}
	if !ValidAgentRole(a.Role) {
		return 0, fmt.Errorf("%w: agent role must be lead, member or guest", ErrInvalidInput)
	}
	if ReservedExternalID(a.ExternalID) {
		return 0, fmt.Errorf("%w: the system: prefix is reserved", ErrInvalidInput)
	}
	if a.SessionID != "" && !ValidExternalID(a.SessionID) {
		return 0, fmt.Errorf("%w: session id has invalid characters", ErrInvalidInput)
	}
	at := a.RegisteredAt
	if at == "" {
		at = now()
	}
	kind := ""
	switch {
	case strings.HasPrefix(a.RoomKey, "project:"):
		kind = RoomProject
	case strings.HasPrefix(a.RoomKey, "machine:"):
		kind = RoomMachine
	default:
		return 0, fmt.Errorf("agent registration requires a project or machine room")
	}
	// registered_at bleibt beim Konflikt stehen: wann diese Session zuerst da
	// war, ist eine andere Auskunft als wann sie zuletzt gesehen wurde, und
	// die erste geht beim Überschreiben sonst verloren.
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var otherScope string
	err = tx.QueryRow(`SELECT m.room_key FROM coord_room_memberships m
		JOIN coord_rooms r ON r.room_key=m.room_key
		WHERE m.principal_id=? AND m.left_at='' AND r.kind=? AND m.room_key<>?
		LIMIT 1`, a.ExternalID, kind, a.RoomKey).Scan(&otherScope)
	switch {
	case err == nil:
		return 0, fmt.Errorf("%w: already joined %s", ErrCoordAgentScopeChanged, otherScope)
	case !errors.Is(err, sql.ErrNoRows):
		return 0, err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO coord_rooms(room_key,kind,label,created_at) VALUES(?,?,?,?)`, a.RoomKey, kind, "", at); err != nil {
		return 0, err
	}
	result, err := tx.Exec(`INSERT INTO coord_agents(
			external_id,provider,room_key,display_name,person,principal_id,cwd,branch,worktree,
			parent_external_id,capabilities,registered_at,last_seen_at,role,session_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,COALESCE(NULLIF(?,''),'member'),?)
		ON CONFLICT(external_id) DO UPDATE SET
			display_name=excluded.display_name, person=excluded.person,
			principal_id=CASE WHEN coord_agents.principal_id='' THEN excluded.principal_id ELSE coord_agents.principal_id END,
			cwd=excluded.cwd, branch=excluded.branch, worktree=excluded.worktree,
			capabilities=excluded.capabilities, last_seen_at=excluded.last_seen_at,
			role=COALESCE(NULLIF(?,''),coord_agents.role),
			session_id=COALESCE(NULLIF(excluded.session_id,''),coord_agents.session_id)
		WHERE (coord_agents.principal_id<>'' AND coord_agents.principal_id=excluded.principal_id)
		   OR (coord_agents.principal_id='' AND coord_agents.person<>'' AND coord_agents.person=excluded.person AND excluded.principal_id<>'')
		   OR (coord_agents.principal_id='' AND coord_agents.person='' AND excluded.principal_id='' AND excluded.person='')`,
		a.ExternalID, a.Provider, a.RoomKey, a.DisplayName, a.Person, a.PrincipalID, a.Cwd,
		a.Branch, a.Worktree, a.ParentExternalID, a.Capabilities, at, at, a.Role, a.SessionID, a.Role)
	if err != nil {
		return 0, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if changed == 0 {
		return 0, ErrCoordAgentOwned
	}
	if _, err := tx.Exec(`INSERT INTO coord_room_memberships(room_key,principal_id,joined_at,left_at,is_manager)
		VALUES(?,?,?,'',0) ON CONFLICT DO NOTHING`, a.RoomKey, a.ExternalID, membershipTime()); err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRow(`SELECT id FROM coord_agents WHERE external_id=?`, a.ExternalID).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func ensureCoordAgentPrincipalID(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(coord_agents)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == "principal_id" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(`ALTER TABLE coord_agents ADD COLUMN principal_id TEXT NOT NULL DEFAULT ''`)
	return err
}

// OwnAgents lists the agents a person registered themselves, in the project
// rooms they are still part of, with the same presence as CoordPeers. It reads
// only rows the person wrote, so it needs no role and reveals nothing about
// anyone else: a viewer whose project role is too low to list a room's agents
// still sees their own.
func (s *Store) OwnAgents(accountPrincipal string) ([]CoordAgent, error) {
	if s.reader != nil {
		return s.reader.OwnAgents(accountPrincipal)
	}
	if _, ok := accountNumericID(accountPrincipal); !ok {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT DISTINCT m.room_key, a.external_id
		FROM coord_agents a JOIN coord_room_memberships m ON m.principal_id=a.external_id
		WHERE a.principal_id=? AND m.left_at='' AND m.room_key LIKE 'project:%'`, accountPrincipal)
	if err != nil {
		return nil, err
	}
	mine := map[string]map[string]bool{}
	for rows.Next() {
		var room, id string
		if err := rows.Scan(&room, &id); err != nil {
			rows.Close()
			return nil, err
		}
		if mine[room] == nil {
			mine[room] = map[string]bool{}
		}
		mine[room][id] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	var out []CoordAgent
	for room, ids := range mine {
		peers, err := s.coordPeers(room, "", ids)
		if err != nil {
			return nil, err
		}
		for _, p := range peers {
			if ids[p.ExternalID] {
				out = append(out, p)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastSeenAt > out[j].LastSeenAt })
	return out, nil
}

// CoordPeers liefert die Teilnehmer eines Raums. since grenzt auf zuletzt
// gesehene Agenten ein und darf leer sein — wer alle will, bekommt alle,
// einschließlich der lange stillen.
//
// last_seen_at ist eine Beobachtung der Anmeldung, kein Lebenszeichen. Wer
// erreichbar ist und was er tut, steht in Presence (coordpresence.go): je Feld
// mit Herkunft und Alter, und ohne Beleg "unknown".
func (s *Store) CoordPeers(roomKey, since string) ([]CoordAgent, error) {
	if s.reader != nil {
		return s.reader.CoordPeers(roomKey, since)
	}
	return s.coordPeers(roomKey, since, nil)
}

// coordPeers is CoordPeers with a set of agents whose presence is derived from
// their own signals only (poll, tool activity, pause): no waits and no cycle,
// because those come from messages to other participants and would tell a
// viewer without the right to list the room whether an @-name is a member.
func (s *Store) coordPeers(roomKey, since string, ownOnly map[string]bool) ([]CoordAgent, error) {
	query := `SELECT a.id,a.external_id,a.provider,m.room_key,a.display_name,
			COALESCE(person,''),COALESCE(cwd,''),COALESCE(branch,''),
			COALESCE(worktree,''),COALESCE(parent_external_id,''),
			COALESCE(capabilities,''),registered_at,last_seen_at,COALESCE(a.principal_id,''),a.role,a.last_poll_at,a.session_id
		FROM coord_agents a JOIN coord_room_memberships m ON m.principal_id=a.external_id
		WHERE m.room_key=? AND m.left_at=''`
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
	var principals []string
	var polls, sessions []string
	for rows.Next() {
		var a CoordAgent
		var principal, poll, session string
		if err := rows.Scan(&a.ID, &a.ExternalID, &a.Provider, &a.RoomKey,
			&a.DisplayName, &a.Person, &a.Cwd, &a.Branch, &a.Worktree,
			&a.ParentExternalID, &a.Capabilities, &a.RegisteredAt, &a.LastSeenAt, &principal, &a.RequestedRole, &poll, &session); err != nil {
			return nil, err
		}
		out = append(out, a)
		principals = append(principals, principal)
		polls = append(polls, poll)
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(out) == 0 {
		return out, nil
	}
	names, err := s.accountNames()
	if err != nil {
		return nil, err
	}
	owner := instanceOwnerID(s.db)
	for i := range out {
		id := owner
		if n, ok := accountNumericID(principals[i]); ok {
			id = n
		}
		out[i].Owner = names[id]
	}
	agents := make([]presenceAgent, len(out))
	for i := range out {
		agents[i] = presenceAgent{ExternalID: out[i].ExternalID, PrincipalID: principals[i], SessionID: sessions[i], LastPoll: polls[i]}
	}
	derived := presenceBatch(s.db, time.Now().UTC(), roomKey, agents, ownOnly)
	// Wartekreise über alle Mitglieder des Raums, unabhängig vom since-Filter.
	var cycleOf map[string]*WaitCycle
	if cycles, err := roomWaitCycles(s.db, time.Now().UTC(), roomKey); err == nil && len(cycles) > 0 {
		cycleOf = map[string]*WaitCycle{}
		for i := range cycles {
			for _, m := range cycles[i].Members {
				cycleOf[m] = &cycles[i]
			}
		}
	}
	for i := range out {
		p := derived[out[i].ExternalID]
		if !ownOnly[out[i].ExternalID] {
			p.Cycle = cycleOf[out[i].ExternalID]
		}
		out[i].Presence = &p
	}
	// Rollen gibt es nur im Projektraum, und sie werden hier live berechnet.
	if remote, ok := strings.CutPrefix(roomKey, "project:"); ok {
		for i := range out {
			info := effectiveAgentRoleTx(s.db, remote, out[i].ExternalID)
			out[i].Role, out[i].CanReview = info.Role, info.CanReview
		}
	} else {
		for i := range out {
			out[i].RequestedRole = ""
		}
	}
	return out, nil
}
