package store

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	ErrCoordNotFound         = errors.New("coordination target not found")
	ErrCoordForbidden        = errors.New("coordination target forbidden")
	ErrCoordUnknownRecipient = errors.New("coordination recipient is not visible")
)

type CoordRecipient struct {
	PrincipalID string
	Label       string
	Kind        string
}

type StandingInput struct {
	RoomKey, ClientID, Body, ExpiresAt string
	Mentions                           []string
}

// CoordAccess is the authenticated boundary around coordination data. HTTP
// and browser handlers use it instead of combining raw store reads with
// their own authorization checks.
type CoordAccess struct {
	Store           *Store
	Principal       Principal
	AgentExternalID string
	publicOnly      bool
}

func (s *Store) CoordinationFor(p Principal, agentID string) CoordAccess {
	return CoordAccess{Store: s, Principal: p, AgentExternalID: strings.TrimSpace(agentID)}
}

// CoordinationPublicFor is the read-only API view used by projections such
// as the repository mirror. It can see unrestricted project threads but is
// never treated as the signed-in browser principal.
func (s *Store) CoordinationPublicFor(p Principal) CoordAccess {
	return CoordAccess{Store: s, Principal: p, publicOnly: true}
}

func (a CoordAccess) direct(s *Store) CoordAccess {
	a.Store = s
	return a
}

func queuedCoordAccess(d *Store, p []any) CoordAccess {
	return CoordAccess{
		Principal:       p[0].(Principal),
		AgentExternalID: p[1].(string),
		publicOnly:      p[2].(bool),
	}.direct(d)
}

// authorKind setzt die Herkunft eines Beitrags. human gilt nur für einen
// Menschen in einer interaktiven Browser-Sitzung (OIDC, Login-Link oder Code,
// TokenKind web). Ein Bearer-Token und eine Sitzung aus eingefügtem Token liegen
// in der Konfiguration von Rechnern, auf denen Agenten laufen; sie posten als
// agent, mit dem Konto als Absender.
func (a CoordAccess) authorKind() string {
	if a.AgentExternalID == "" && a.Principal.TokenKind == WebSessionKind {
		return AuthorHuman
	}
	return AuthorAgent
}

func (a CoordAccess) actor() (string, error) {
	if a.Store == nil || strings.TrimSpace(a.Principal.ID) == "" {
		return "", ErrCoordForbidden
	}
	if a.AgentExternalID == "" {
		if a.publicOnly {
			return "", nil
		}
		return a.Principal.ID, nil
	}
	owner, registered, err := a.Store.CoordAgentOwner(a.AgentExternalID)
	if err != nil {
		return "", err
	}
	if !registered || owner == "" || owner != a.Principal.ID {
		return "", ErrCoordForbidden
	}
	return a.AgentExternalID, nil
}

func (a CoordAccess) mutationActor() (string, error) {
	if a.publicOnly {
		return "", ErrCoordForbidden
	}
	actor, err := a.actor()
	if err != nil {
		return "", err
	}
	if actor == "" {
		return "", ErrCoordForbidden
	}
	return actor, nil
}

// requireRoomAccess is the write gate: active membership (an own agent in the
// room), never a role (spec 7.5: roles never grant write access).
func (a CoordAccess) requireRoomAccess(roomKey string) (CoordRoom, error) {
	return a.roomAccess(roomKey, false)
}

// requireRoomRead is the read gate: membership, or for a project room the
// project role member and up in a web session (matrix 8.1).
func (a CoordAccess) requireRoomRead(roomKey string) (CoordRoom, error) {
	return a.roomAccess(roomKey, true)
}

func (a CoordAccess) roomAccess(roomKey string, byRole bool) (CoordRoom, error) {
	actor, err := a.actor()
	if err != nil {
		return CoordRoom{}, err
	}
	room, err := a.rawRoom(roomKey)
	if err != nil {
		return CoordRoom{}, err
	}
	switch room.Kind {
	case RoomDirect, RoomGroup:
		ok, err := a.activeRoomMember(room.Key, actor)
		if err != nil {
			return CoordRoom{}, err
		}
		if !ok {
			return CoordRoom{}, ErrCoordNotFound
		}
	case RoomProject, RoomMachine:
		if err := a.projectRoomGate(room.Kind, room.Key, ResRoom, nil); err != nil {
			return CoordRoom{}, err
		}
		if a.publicOnly {
			return room, nil
		}
		if a.AgentExternalID != "" {
			ok, err := a.activeRoomMember(room.Key, actor)
			if err != nil {
				return CoordRoom{}, err
			}
			if !ok {
				return CoordRoom{}, noMembershipError(room.Kind)
			}
		} else {
			ok, err := a.principalOwnsPublicMembership(room.Key)
			if err != nil {
				return CoordRoom{}, err
			}
			if !ok && !(byRole && room.Kind == RoomProject && a.memberReadsProjectRoom(room.Key, nil)) {
				return CoordRoom{}, noMembershipError(room.Kind)
			}
		}
	default:
		return CoordRoom{}, ErrCoordNotFound
	}
	if room.Kind == RoomDirect || room.Kind == RoomGroup {
		room.Members, err = a.Store.CoordRoomMembers(room.Key)
		if err != nil {
			return CoordRoom{}, err
		}
	}
	return room, nil
}

// noMembershipError: a project room the viewer may see but is not a member of
// is forbidden; a machine room is not found, so that hostnames cannot be
// enumerated (#2447). Both answer the same bytes as a missing room.
func noMembershipError(kind string) error {
	if kind == RoomMachine {
		return ErrCoordNotFound
	}
	return ErrCoordForbidden
}

// memberReadsProjectRoom lets a signed-in person with the project role member
// or higher (owner, lead, member, reviewer) READ the project room without an
// agent of their own in it (matrix 8.1). It never grants write access (spec
// 7.5): posting, directives, threads and mutations keep the membership rule,
// and agent tokens, pasted sessions and guests are not covered.
func (a CoordAccess) memberReadsProjectRoom(roomKey string, tx rowQuerier) bool {
	if a.Store == nil || a.publicOnly || a.AgentExternalID != "" || a.Principal.TokenKind != WebSessionKind || !strings.HasPrefix(roomKey, "project:") {
		return false
	}
	acct, ok := accountNumericID(a.Principal.ID)
	if !ok {
		return false
	}
	project := strings.TrimPrefix(roomKey, "project:")
	var role RoleInfo
	if tx != nil {
		role = projectRoleTx(tx, project, acct)
	} else {
		role = a.Store.ProjectRole(project, a.Principal.ID)
	}
	return matrixAllows(role, ResAgents, ActRead, Object{})
}

func (a CoordAccess) principalOwnsPublicMembership(roomKey string) (bool, error) {
	// This projects observed ownership for browser navigation. Registration is
	// self-reported convenience, not cryptographic tenant isolation; private
	// room membership remains the confidentiality boundary.
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	var count int
	err := reader.db.QueryRow(`SELECT COUNT(*) FROM coord_room_memberships m
		LEFT JOIN coord_agents a ON a.external_id=m.principal_id
		WHERE m.room_key=? AND m.left_at=''
		  AND (m.principal_id=? OR a.principal_id=?)`, roomKey, a.Principal.ID, a.Principal.ID).Scan(&count)
	return count > 0, err
}

func (a CoordAccess) rawRoom(roomKey string) (CoordRoom, error) {
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	var room CoordRoom
	err := reader.db.QueryRow(`SELECT room_key,kind,label,created_at FROM coord_rooms WHERE room_key=?`, roomKey).
		Scan(&room.Key, &room.Kind, &room.Label, &room.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CoordRoom{}, ErrCoordNotFound
	}
	if err != nil {
		return CoordRoom{}, err
	}
	return room, nil
}

func (a CoordAccess) activeRoomMember(roomKey, actor string) (bool, error) {
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	var count int
	err := reader.db.QueryRow(`SELECT COUNT(*) FROM coord_room_memberships
		WHERE room_key=? AND principal_id=? AND left_at=''`, roomKey, actor).Scan(&count)
	return count > 0, err
}

func (a CoordAccess) Room(roomKey string) (CoordRoom, error) {
	return a.requireRoomRead(roomKey)
}

// CanPost says whether the viewer may write into the room: the same check
// that Send, CreateStanding and thread creation apply, so the page offers a
// composer only where a post would be accepted.
func (a CoordAccess) CanPost(roomKey string) bool {
	if a.publicOnly {
		return false
	}
	_, err := a.requireRoomAccess(roomKey)
	return err == nil
}

func (a CoordAccess) RoomMemberships(roomKey string) ([]RoomMembership, error) {
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	tx, err := reader.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return nil, err
	}
	if err := a.canReadTx(tx, actor, DestinationRoom, roomKey); err != nil {
		return nil, err
	}
	var kind string
	if err := tx.QueryRow(`SELECT kind FROM coord_rooms WHERE room_key=?`, roomKey).Scan(&kind); err != nil {
		return nil, err
	}
	if kind != RoomDirect && kind != RoomGroup {
		return nil, nil
	}
	rows, err := tx.Query(`SELECT room_key,principal_id,joined_at,left_at,is_manager FROM coord_room_memberships WHERE room_key=? ORDER BY joined_at,principal_id`, roomKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RoomMembership
	for rows.Next() {
		var m RoomMembership
		if err := rows.Scan(&m.RoomKey, &m.PrincipalID, &m.JoinedAt, &m.LeftAt, &m.Manager); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (a CoordAccess) Rooms() ([]CoordRoom, error) {
	rooms, _, err := a.roomsWithRole()
	return rooms, err
}

// roomsWithRole is Rooms plus the keys that are listed only through the
// viewer's project role (computed once, for the summaries).
func (a CoordAccess) roomsWithRole() ([]CoordRoom, map[string]bool, error) {
	actor, err := a.actor()
	if err != nil {
		return nil, nil, err
	}
	rooms, err := a.Store.CoordRoomsForPrincipal(actor)
	if err != nil {
		return nil, nil, err
	}
	if a.AgentExternalID == "" && !a.publicOnly {
		reader := a.Store
		if reader.reader != nil {
			reader = reader.reader
		}
		rows, err := reader.db.Query(`SELECT DISTINCT r.room_key,r.kind,r.label,r.created_at
			FROM coord_rooms r
			JOIN coord_room_memberships m ON m.room_key=r.room_key AND m.left_at=''
			JOIN coord_agents a ON a.external_id=m.principal_id
			WHERE r.kind IN ('project','machine') AND a.principal_id=?
			ORDER BY r.created_at DESC,r.room_key`, a.Principal.ID)
		if err != nil {
			return nil, nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var room CoordRoom
			if err := rows.Scan(&room.Key, &room.Kind, &room.Label, &room.CreatedAt); err != nil {
				return nil, nil, err
			}
			found := false
			for _, existing := range rooms {
				if existing.Key == room.Key {
					found = true
					break
				}
			}
			if !found {
				rooms = append(rooms, room)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
	}
	byRole, err := a.roleReadableProjectRooms(rooms)
	if err != nil {
		return nil, nil, err
	}
	keys := map[string]bool{}
	for _, room := range byRole {
		rooms = append(rooms, room)
		keys[room.Key] = true
	}
	return a.gateProjectRooms(rooms), keys, nil
}

// roleReadableProjectRooms lists the project rooms the viewer may read through
// their project role alone (member and up, web session) and that are not in
// have yet, so the room tabs show every room the page lets them open. One
// query for all roles of the account, filtered to rank 2 and up.
func (a CoordAccess) roleReadableProjectRooms(have []CoordRoom) ([]CoordRoom, error) {
	if a.Store == nil || a.publicOnly || a.AgentExternalID != "" || a.Principal.TokenKind != WebSessionKind {
		return nil, nil
	}
	acct, ok := accountNumericID(a.Principal.ID)
	if !ok {
		return nil, nil
	}
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	rows, err := reader.db.Query(`SELECT r.room_key,r.kind,r.label,r.created_at,
			om.role, COALESCE(pm.role,''), COALESCE(pm.can_review,0)
		FROM coord_rooms r
		JOIN projects p ON r.room_key='project:'||p.remote
		JOIN org_members om ON om.org_id=p.org_id AND om.account_id=?
		LEFT JOIN project_members pm ON pm.project_id=p.id AND pm.account_id=?
		WHERE r.kind='project' ORDER BY r.created_at DESC,r.room_key`, acct, acct)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for _, room := range have {
		seen[room.Key] = true
	}
	var out []CoordRoom
	for rows.Next() {
		var room CoordRoom
		var orgRole, role string
		var review int
		if err := rows.Scan(&room.Key, &room.Kind, &room.Label, &room.CreatedAt, &orgRole, &role, &review); err != nil {
			return nil, err
		}
		if seen[room.Key] || !matrixAllows(deriveRole(orgRole, role, review == 1), ResAgents, ActRead, Object{}) {
			continue
		}
		out = append(out, room)
	}
	return out, rows.Err()
}

// gateProjectRooms legt die Projektrolle über die Raumliste: eine Mitgliedschaft
// im Projektraum bleibt in der Tabelle stehen, wenn das Konto seine Rolle oder
// die Org verliert, und wirkt dann nicht mehr. Ohne Rolle fehlt der Raum, der
// Gast sieht ihn ohne Mitgliederliste (Agenten zeigt der Raum ab member), wie
// bei Peers. Gelesen statt beim Entzug aufgeräumt, weil jeder Weg, eine Rolle zu
// verlieren (Rolle entziehen, Org verlassen, Projekt verschieben, Zeile von
// Hand), so von allein abgedeckt ist und die Mitgliedschaft mit der Rolle
// zurückkommt. Im Log-Modus bleibt die Liste, und "would deny" wird protokolliert.
func (a CoordAccess) gateProjectRooms(rooms []CoordRoom) []CoordRoom {
	out := make([]CoordRoom, 0, len(rooms))
	for _, room := range rooms {
		if room.Kind == RoomProject {
			if a.projectRoomGate(room.Kind, room.Key, ResRoom, nil) != nil {
				continue
			}
			if a.projectRoomGate(room.Kind, room.Key, ResAgents, nil) != nil {
				room.Members = []string{}
			}
		}
		out = append(out, room)
	}
	return out
}

func (a CoordAccess) requireThreadAccess(threadID int64) (Thread, error) {
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	tx, err := reader.db.Begin()
	if err != nil {
		return Thread{}, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return Thread{}, err
	}
	if err := a.canReadThreadTx(tx, actor, threadID); err != nil {
		return Thread{}, err
	}
	row := tx.QueryRow(`SELECT id,project,title,question,state,archived,person,author_principal_id,
		created_at,updated_at,COALESCE(resolved_at,'') FROM threads WHERE id=?`, threadID)
	t, err := scanThread(row)
	if err != nil {
		return Thread{}, ErrCoordNotFound
	}
	a.maskThreadPersonTx(tx, &t)
	if err := tx.Commit(); err != nil {
		return Thread{}, err
	}
	return t, nil
}

func (a CoordAccess) canRead(kind, id string) error {
	switch kind {
	case DestinationRoom:
		_, err := a.requireRoomRead(id)
		return err
	case DestinationDiscussion:
		threadID, err := strconv.ParseInt(id, 10, 64)
		if err != nil || threadID <= 0 {
			return ErrCoordNotFound
		}
		_, err = a.requireThreadAccess(threadID)
		return err
	default:
		return ErrCoordNotFound
	}
}

func (a CoordAccess) Messages(kind, id string, afterID int64, limit int) ([]CoordMessage, error) {
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	tx, err := reader.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return nil, err
	}
	if err := a.canReadTx(tx, actor, kind, id); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := tx.Query(`SELECT id,destination_kind,destination_id,sequence,
		sender_external_id,author_principal_id,author_kind,COALESCE(parent_external_id,''),
		client_id,kind,intent,priority,body,COALESCE(reply_to,0),COALESCE(origin_event_id,''),
		COALESCE(causation_id,''),COALESCE(expires_at,''),COALESCE(observed_at_client,''),created_at
		FROM coord_messages WHERE destination_kind=? AND destination_id=? AND id>?
		ORDER BY id LIMIT ?`, kind, id, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nowTS := now()
	var out []CoordMessage
	for rows.Next() {
		var message CoordMessage
		if err := rows.Scan(&message.ID, &message.DestinationKind, &message.DestinationID, &message.Sequence,
			&message.SenderExternalID, &message.AuthorPrincipalID, &message.AuthorKind, &message.ParentExternalID,
			&message.ClientID, &message.Kind, &message.Intent, &message.Priority, &message.Body, &message.ReplyTo,
			&message.OriginEventID, &message.CausationID, &message.ExpiresAt, &message.ObservedAtClient, &message.CreatedAt); err != nil {
			return nil, err
		}
		message.Expired = expiredAt(message.ExpiresAt, nowTS)
		out = append(out, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := fillSenderDisplayNamesTx(tx, a, kind, id, out); err != nil {
		return nil, err
	}
	if a.AgentExternalID != "" {
		// Der Leser ist ein Agent: Rollen und Autorität live aus dem Zustand
		// dieser Transaktion, nach dem Schließen des Cursors.
		ctx := newAgentAuthorityCtx(tx, a.AgentExternalID)
		for i := range out {
			au := ctx.evaluate(out[i])
			out[i].SenderRole, out[i].RecipientRole, out[i].Authority = au.SenderRole, au.RecipientRole, au.Authority
		}
	}
	// Erst nach der Autoritätsberechnung: sie liest das Konto des Absenders.
	if len(out) > 0 && a.guestViewForMessageTx(tx, kind, id) {
		a.maskMessageOwners(out)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// maskMessageOwners leert in der Gastsicht AuthorPrincipalID: bei einem
// Agenten ist es das Konto seines Besitzers, also ein Mitgliedsorakel. Nur die
// eigenen Nachrichten des Lesers behalten es. Menschliche Absender erkennt der
// Gast an der Absender-ID (person:N), die ohnehin sichtbar ist.
func (a CoordAccess) maskMessageOwners(msgs []CoordMessage) {
	for i := range msgs {
		if msgs[i].AuthorPrincipalID != a.Principal.ID {
			msgs[i].AuthorPrincipalID = ""
		}
	}
}

// maskThreadPersonTx entfernt für Gäste den Urheber des Threads (Person und
// AuthorPrincipalID). Der eigene Thread des Leser bleibt, wie er ist.
func (a CoordAccess) maskThreadPersonTx(tx *sql.Tx, t *Thread) {
	a.maskThreadPersonCachedTx(tx, t, nil)
}

// maskThreadPersonCachedTx fragt die Gastsicht einmal je Projekt (der
// Projektraum entscheidet), nicht je Thread. cache darf nil sein.
func (a CoordAccess) maskThreadPersonCachedTx(tx *sql.Tx, t *Thread, cache map[string]bool) {
	if t.AuthorPrincipalID != "" && t.AuthorPrincipalID == a.Principal.ID {
		return
	}
	guest, ok := cache[t.Project]
	if !ok {
		guest = a.guestViewForMessageTx(tx, DestinationRoom, RoomKeyForProject(t.Project))
		if cache != nil {
			cache[t.Project] = guest
		}
	}
	if guest {
		maskThreadPerson(a, t)
	}
}

// maskThreadPerson leert Person und AuthorPrincipalID. Der Thread speichert
// nur das Konto, nicht den Agenten: bei einem Agenten-Thread wäre person:N
// (wie der Name) der Besitzer des Agenten und damit ein Mitgliedsorakel. Eine
// Herleitung des Agenten aus dem ersten Beitrag wäre unsicher, deshalb sieht
// der Gast keinen Urheber.
func maskThreadPerson(a CoordAccess, t *Thread) {
	if t.AuthorPrincipalID == a.Principal.ID {
		return
	}
	t.Person, t.AuthorPrincipalID = "", ""
}

// fillSenderDisplayNamesTx setzt den Kontonamen menschlicher Absender. Die
// Gastfrage stellt guestViewForMessageTx einmal für den ganzen Raum (nicht pro
// Nachricht): wer die Mitglieder nicht sieht, bekommt keinen Namen, auch nicht
// den eines Absenders, dessen Nachricht er liest. Der Name hängt nur an der
// sichtbaren Nachricht und an keiner verborgenen Zeile.
func fillSenderDisplayNamesTx(tx *sql.Tx, a CoordAccess, kind, id string, msgs []CoordMessage) error {
	names := map[string]string{}
	guest, guestKnown := false, false
	for i := range msgs {
		m := &msgs[i]
		if m.AuthorKind != AuthorHuman || !strings.HasPrefix(m.AuthorPrincipalID, "person:") {
			continue
		}
		if !guestKnown {
			guest, guestKnown = a.guestViewForMessageTx(tx, kind, id), true
		}
		if guest {
			return nil
		}
		name, ok := names[m.AuthorPrincipalID]
		if !ok {
			personID, err := strconv.ParseInt(strings.TrimPrefix(m.AuthorPrincipalID, "person:"), 10, 64)
			if err == nil {
				if err := tx.QueryRow(`SELECT name FROM persons WHERE id=?`, personID).Scan(&name); err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
			}
			names[m.AuthorPrincipalID] = name
		}
		m.SenderDisplayName = NormalizeAccountName(name)
	}
	return nil
}

func (a CoordAccess) MessageWindow(kind, id string, window MessageWindow) (MessagePage, error) {
	if a.Store == nil || strings.TrimSpace(a.Principal.ID) == "" {
		return MessagePage{}, ErrCoordForbidden
	}
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	tx, err := reader.db.Begin()
	if err != nil {
		return MessagePage{}, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return MessagePage{}, err
	}
	if err := a.canReadTx(tx, actor, kind, id); err != nil {
		return MessagePage{}, err
	}
	page, err := coordMessageWindowTx(tx, kind, id, window)
	if err != nil {
		return MessagePage{}, err
	}
	if len(page.Messages) > 0 && a.guestViewForMessageTx(tx, kind, id) {
		a.maskMessageOwners(page.Messages)
	}
	if err := tx.Commit(); err != nil {
		return MessagePage{}, err
	}
	return page, nil
}

func (a CoordAccess) actorTx(tx *sql.Tx) (string, error) {
	if a.AgentExternalID == "" {
		if a.publicOnly {
			return "", nil
		}
		return a.Principal.ID, nil
	}
	var owner string
	err := tx.QueryRow(`SELECT principal_id FROM coord_agents WHERE external_id=?`, a.AgentExternalID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrCoordForbidden
	}
	if err != nil {
		return "", err
	}
	if owner == "" || owner != a.Principal.ID {
		return "", ErrCoordForbidden
	}
	return a.AgentExternalID, nil
}

func (a CoordAccess) canReadTx(tx *sql.Tx, actor, kind, id string) error {
	return a.canAccessTx(tx, actor, kind, id, true)
}

// canWriteTx is canReadTx without the role fallback: the member rule.
func (a CoordAccess) canWriteTx(tx *sql.Tx, actor, kind, id string) error {
	return a.canAccessTx(tx, actor, kind, id, false)
}

func (a CoordAccess) canAccessTx(tx *sql.Tx, actor, kind, id string, byRole bool) error {
	switch kind {
	case DestinationRoom:
		return a.roomAccessTx(tx, actor, id, byRole)
	case DestinationDiscussion:
		threadID, err := strconv.ParseInt(id, 10, 64)
		if err != nil || threadID <= 0 {
			return ErrCoordNotFound
		}
		return a.threadAccessTx(tx, actor, threadID, byRole)
	default:
		return ErrCoordNotFound
	}
}

func (a CoordAccess) canReadThreadTx(tx *sql.Tx, actor string, threadID int64) error {
	return a.threadAccessTx(tx, actor, threadID, true)
}

func (a CoordAccess) threadAccessTx(tx *sql.Tx, actor string, threadID int64, byRole bool) error {
	var project string
	if err := tx.QueryRow(`SELECT project FROM threads WHERE id=?`, threadID).Scan(&project); errors.Is(err, sql.ErrNoRows) {
		return ErrCoordNotFound
	} else if err != nil {
		return err
	}
	if home, found, err := threadHomeTx(tx, threadID); err != nil {
		return err
	} else if found {
		return a.roomAccessTx(tx, actor, home.RoomKey, byRole)
	}
	{
		var restricted int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM thread_visibility WHERE thread_id=?`, threadID).Scan(&restricted); err != nil {
			return err
		}
		if restricted > 0 {
			if a.publicOnly {
				return ErrCoordNotFound
			}
			var member int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM thread_visibility
				WHERE thread_id=? AND member_external_id=?`, threadID, actor).Scan(&member); err != nil {
				return err
			}
			if member == 0 {
				return ErrCoordNotFound
			}
			// Die Thread-Liste allein genügt nicht: wer die Projektrolle verloren
			// hat, liest auch einen eingeschränkten Thread nicht mehr.
			return a.projectRoomGate(RoomProject, RoomKeyForProject(project), ResRoom, tx)
		}
		if a.publicOnly {
			// public_only ist eine Auswahl (nur nicht eingeschränkte Threads), nie
			// eine Lockerung: die Projektrolle gilt auch hier.
			return a.projectRoomGate(RoomProject, RoomKeyForProject(project), ResRoom, tx)
		}
		return a.roomAccessTx(tx, actor, RoomKeyForProject(project), byRole)
	}
}

// requireRoomAccessTx is the write gate (membership only).
func (a CoordAccess) requireRoomAccessTx(tx *sql.Tx, actor, roomKey string) error {
	return a.roomAccessTx(tx, actor, roomKey, false)
}

func (a CoordAccess) roomAccessTx(tx *sql.Tx, actor, roomKey string, byRole bool) error {
	var kind string
	if err := tx.QueryRow(`SELECT kind FROM coord_rooms WHERE room_key=?`, roomKey).Scan(&kind); errors.Is(err, sql.ErrNoRows) {
		return ErrCoordNotFound
	} else if err != nil {
		return err
	}
	switch kind {
	case RoomDirect, RoomGroup:
		var member int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM coord_room_memberships
			WHERE room_key=? AND principal_id=? AND left_at=''`, roomKey, actor).Scan(&member); err != nil {
			return err
		}
		if member == 0 {
			return ErrCoordNotFound
		}
		return nil
	case RoomProject, RoomMachine:
		if err := a.projectRoomGate(kind, roomKey, ResRoom, tx); err != nil {
			return err
		}
		if a.publicOnly {
			return nil
		}
		var member int
		if a.AgentExternalID != "" {
			if err := tx.QueryRow(`SELECT COUNT(*) FROM coord_room_memberships
				WHERE room_key=? AND principal_id=? AND left_at=''`, roomKey, actor).Scan(&member); err != nil {
				return err
			}
		} else {
			if err := tx.QueryRow(`SELECT COUNT(*) FROM coord_room_memberships m
				LEFT JOIN coord_agents agent ON agent.external_id=m.principal_id
				WHERE m.room_key=? AND m.left_at=''
				  AND (m.principal_id=? OR agent.principal_id=?)`, roomKey, a.Principal.ID, a.Principal.ID).Scan(&member); err != nil {
				return err
			}
		}
		if member == 0 && !(byRole && kind == RoomProject && a.memberReadsProjectRoom(roomKey, tx)) {
			return noMembershipError(kind)
		}
		return nil
	default:
		return ErrCoordNotFound
	}
}

func (a CoordAccess) MarkRead(kind, id string, through int64) error {
	if a.Store != nil && a.Store.writer != nil {
		return queueWrite(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, kind, id, through}, func(d *Store, p []any) error {
			return queuedCoordAccess(d, p).MarkRead(p[3].(string), p[4].(string), p[5].(int64))
		})
	}
	actor, err := a.mutationActor()
	if err != nil {
		return err
	}
	if err := a.canRead(kind, id); err != nil {
		return err
	}
	return a.Store.MarkCoordRead(actor, kind, id, through)
}

func (a CoordAccess) MarkUnread(kind, id string, from int64) error {
	if a.Store != nil && a.Store.writer != nil {
		return queueWrite(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, kind, id, from}, func(d *Store, p []any) error {
			return queuedCoordAccess(d, p).MarkUnread(p[3].(string), p[4].(string), p[5].(int64))
		})
	}
	actor, err := a.mutationActor()
	if err != nil {
		return err
	}
	if err := a.canRead(kind, id); err != nil {
		return err
	}
	return a.Store.MarkCoordUnread(actor, kind, id, from)
}

func (a CoordAccess) RoomSummaries() ([]CoordRoomSummary, error) {
	actor, err := a.actor()
	if err != nil {
		return nil, err
	}
	if actor == "" {
		return nil, ErrCoordForbidden
	}
	rooms, byRole, err := a.roomsWithRole()
	if err != nil {
		return nil, err
	}
	return a.Store.projectRoomSummaries(actor, a.Principal.ID, a.AgentExternalID, rooms, byRole)
}

func (a CoordAccess) Send(message CoordMessage) (int64, error) {
	if a.Store != nil && a.Store.writer != nil {
		return queueValue(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, message}, func(d *Store, p []any) (int64, error) {
			return queuedCoordAccess(d, p).Send(p[3].(CoordMessage))
		})
	}
	if a.Store == nil || a.publicOnly {
		return 0, ErrCoordForbidden
	}
	tx, err := a.Store.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil || actor == "" {
		if err != nil {
			return 0, err
		}
		return 0, ErrCoordForbidden
	}
	if ReservedExternalID(actor) {
		return 0, ErrCoordForbidden // der Präfix system: gehört dem Store
	}
	if err := a.canWriteTx(tx, actor, message.DestinationKind, message.DestinationID); err != nil {
		return 0, err
	}
	if _, actionable := attentionReasonForIntent(message.Intent); actionable && len(normalizeMembers(message.Mentions)) == 0 {
		if message.DestinationKind == DestinationRoom {
			var roomKind string
			if err := tx.QueryRow(`SELECT kind FROM coord_rooms WHERE room_key=?`, message.DestinationID).Scan(&roomKind); err != nil {
				return 0, ErrCoordNotFound
			}
			if roomKind == RoomDirect {
				rows, err := tx.Query(`SELECT principal_id FROM coord_room_memberships
					WHERE room_key=? AND left_at='' AND principal_id<>? ORDER BY principal_id`, message.DestinationID, actor)
				if err != nil {
					return 0, err
				}
				for rows.Next() {
					var recipient string
					if err := rows.Scan(&recipient); err != nil {
						rows.Close()
						return 0, err
					}
					message.Mentions = append(message.Mentions, recipient)
				}
				if err := rows.Err(); err != nil {
					rows.Close()
					return 0, err
				}
				if err := rows.Close(); err != nil {
					return 0, err
				}
			}
		}
		if len(normalizeMembers(message.Mentions)) == 0 {
			return 0, ErrAttentionRecipientRequired
		}
	}
	var rawMentions []string
	if len(message.Mentions) > 0 {
		var mentions, raw []string
		var mentionErr error
		if message.DestinationKind == DestinationDiscussion {
			threadID, parseErr := strconv.ParseInt(message.DestinationID, 10, 64)
			if parseErr != nil || threadID <= 0 {
				return 0, ErrCoordNotFound
			}
			if home, found, homeErr := threadHomeTx(tx, threadID); homeErr != nil {
				return 0, homeErr
			} else if found {
				mentions, raw, mentionErr = a.resolveMentionsTx(tx, actor, home.RoomKey, message.Mentions)
			} else {
				mentions, raw, mentionErr = a.resolveLegacyThreadMentionsTx(tx, actor, threadID, message.Mentions)
			}
		} else {
			mentions, raw, mentionErr = a.resolveMentionsTx(tx, actor, message.DestinationID, message.Mentions)
		}
		if mentionErr != nil {
			return 0, mentionErr
		}
		message.Mentions, rawMentions = mentions, raw
	}
	if message.ReplyTo != 0 {
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM coord_messages
			WHERE id=? AND destination_kind=? AND destination_id=?`, message.ReplyTo,
			message.DestinationKind, message.DestinationID).Scan(&count); err != nil {
			return 0, err
		}
		if count == 0 {
			return 0, ErrCoordNotFound
		}
	}
	message.SenderExternalID = actor
	message.AuthorPrincipalID = a.Principal.ID
	message.AuthorKind = a.authorKind()
	message.SenderRole, message.RecipientRole, message.Authority, message.SenderDisplayName = "", "", "", ""
	// Ein Wartekreis wird vor dem Senden abgeglichen, damit einer, der
	// zwischenzeitlich abgelaufen ist, als aufgelöst gilt, bevor diese Nachricht
	// ihn neu schließt; und danach, weil sie ihn schließen kann.
	waitRoom := ""
	if reason, ok := attentionReasonForIntent(message.Intent); ok && reason != AttentionHandoff {
		waitRoom = messageRoomKeyTx(tx, message.DestinationKind, message.DestinationID)
		if err := reconcileWaitCyclesSafeTx(tx, waitRoom, time.Now().UTC()); err != nil {
			return 0, err
		}
	}
	id, err := appendCoordMessageTx(tx, message)
	if err != nil {
		return 0, err
	}
	if waitRoom != "" {
		if err := reconcileWaitCyclesSafeTx(tx, waitRoom, time.Now().UTC()); err != nil {
			return 0, err
		}
	}
	if err := insertRawMentionsTx(tx, id, rawMentions); err != nil {
		return 0, err
	}
	if message.DestinationKind == DestinationDiscussion {
		threadID, _ := strconv.ParseInt(message.DestinationID, 10, 64)
		if _, err := tx.Exec(`UPDATE threads SET updated_at=? WHERE id=?`, now(), threadID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (a CoordAccess) resolveLegacyThreadMentionsTx(tx *sql.Tx, actor string, threadID int64, principals []string) (kept, raw []string, err error) {
	var restricted int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM thread_visibility WHERE thread_id=?`, threadID).Scan(&restricted); err != nil {
		return nil, nil, err
	}
	if restricted == 0 {
		var project string
		if err := tx.QueryRow(`SELECT project FROM threads WHERE id=?`, threadID).Scan(&project); err != nil {
			return nil, nil, ErrCoordNotFound
		}
		return a.resolveMentionsTx(tx, actor, RoomKeyForProject(project), principals)
	}
	return principals, nil, a.validateRestrictedThreadParticipantsTx(tx, actor, threadID, principals)
}

func (a CoordAccess) validateRestrictedThreadParticipantsTx(tx *sql.Tx, actor string, threadID int64, principals []string) error {
	allowed := map[string]bool{actor: true, a.Principal.ID: true}
	rows, err := tx.Query(`SELECT member_external_id FROM thread_visibility WHERE thread_id=?`, threadID)
	if err != nil {
		return err
	}
	var members []string
	for rows.Next() {
		var member string
		if err := rows.Scan(&member); err != nil {
			rows.Close()
			return err
		}
		allowed[member] = true
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, member := range members {
		var owner string
		err := tx.QueryRow(`SELECT COALESCE(principal_id,'') FROM coord_agents WHERE external_id=?`, member).Scan(&owner)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if owner != "" {
			allowed[owner] = true
		}
	}
	for _, principal := range normalizeMembers(principals) {
		if !allowed[principal] {
			return fmt.Errorf("%w: %s", ErrCoordUnknownRecipient, principal)
		}
	}
	return nil
}

func (a CoordAccess) Thread(threadID int64) (Thread, error) {
	return a.requireThreadAccess(threadID)
}

func (a CoordAccess) ThreadPost(threadID int64, message CoordMessage) (int64, error) {
	message.DestinationKind = DestinationDiscussion
	message.DestinationID = ThreadDestinationID(threadID)
	return a.Send(message)
}

func (a CoordAccess) Cursor(kind, id string) (int64, error) {
	actor, err := a.actor()
	if err != nil {
		return 0, err
	}
	if err := a.canRead(kind, id); err != nil {
		return 0, err
	}
	return a.Store.CoordCursor(actor, kind, id)
}

func (a CoordAccess) SetCursor(kind, id string, lastMessageID int64) error {
	if a.Store != nil && a.Store.writer != nil {
		return queueWrite(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, kind, id, lastMessageID}, func(d *Store, p []any) error {
			return queuedCoordAccess(d, p).SetCursor(p[3].(string), p[4].(string), p[5].(int64))
		})
	}
	actor, err := a.mutationActor()
	if err != nil {
		return err
	}
	if err := a.canRead(kind, id); err != nil {
		return err
	}
	return a.Store.SetCoordCursor(actor, kind, id, lastMessageID)
}

func (a CoordAccess) Peers(roomKey, since string) ([]CoordAgent, error) {
	room, err := a.requireRoomRead(roomKey)
	if err != nil {
		return nil, err
	}
	// Agenten und Peers sieht ab member; der Gast liest den Raum, mehr nicht.
	if err := a.projectRoomGate(room.Kind, room.Key, ResAgents, nil); err != nil {
		return nil, err
	}
	return a.Store.CoordPeers(roomKey, since)
}

// ProjectAgents lists the agents of a project room for a browser viewer whose
// own project role may read agents (member and up), even without an agent of
// their own in the room. Read only: the room, its posts and every write keep
// their membership rules. What comes back depends on the viewer's role alone;
// anyone below member gets exactly what Peers answers.
func (a CoordAccess) ProjectAgents(project string) ([]CoordAgent, error) {
	roomKey := RoomKeyForProject(project)
	if a.Store == nil || a.AgentExternalID != "" || a.publicOnly {
		return a.Peers(roomKey, "")
	}
	if _, ok := accountNumericID(a.Principal.ID); !ok || !matrixAllows(a.Store.ProjectRole(project, a.Principal.ID), ResAgents, ActRead, Object{}) {
		return a.Peers(roomKey, "")
	}
	if _, err := a.rawRoom(roomKey); err != nil {
		return nil, err
	}
	return a.Store.CoordPeers(roomKey, "")
}

func (a CoordAccess) messageTarget(messageID int64) (string, string, error) {
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	var kind, id string
	err := reader.db.QueryRow(`SELECT destination_kind,destination_id FROM coord_messages WHERE id=?`, messageID).Scan(&kind, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrCoordNotFound
	}
	if err != nil {
		return "", "", err
	}
	if err := a.canRead(kind, id); err != nil {
		return "", "", err
	}
	return kind, id, nil
}

func (a CoordAccess) MessageMentions(messageID int64) ([]string, error) {
	kind, id, err := a.messageTarget(messageID)
	if err != nil {
		return nil, err
	}
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	tx, err := reader.db.Begin()
	if err != nil {
		return nil, err
	}
	var raw []string
	if a.guestViewForMessageTx(tx, kind, id) {
		raw, err = rawMentionsTx(tx, messageID)
	}
	tx.Rollback()
	if err != nil {
		return nil, err
	}
	if raw != nil {
		// Der Gast sieht von den Erwähnungen nur, was getippt wurde, und sich
		// selbst, wenn er gemeint ist: die eigene Empfängerrolle ist ihm ohnehin
		// bekannt, und ohne sie würde ein maskierter Empfänger (etwa bei einer
		// Meldung über einen Wartekreis) nie geweckt. Andere Empfänger bleiben
		// verborgen.
		if actor, aerr := a.actor(); aerr == nil && actor != "" && !slices.Contains(raw, actor) {
			if real, merr := a.Store.CoordMessageMentions(messageID); merr == nil && slices.Contains(real, actor) {
				raw = append(raw, actor)
			}
		}
		return raw, nil
	}
	return a.Store.CoordMessageMentions(messageID)
}

func (a CoordAccess) MarkDelivery(messageID int64, state string) error {
	if a.Store != nil && a.Store.writer != nil {
		return queueWrite(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, messageID, state}, func(d *Store, p []any) error {
			return queuedCoordAccess(d, p).MarkDelivery(p[3].(int64), p[4].(string))
		})
	}
	actor, err := a.mutationActor()
	if err != nil {
		return err
	}
	if _, _, err := a.messageTarget(messageID); err != nil {
		return err
	}
	return a.Store.MarkCoordDelivery(messageID, actor, state)
}

// Heartbeat stempelt den Abruf des eigenen Agenten. Nur der Agent des Tokens
// darf das; die Antwort verrät nicht, ob geschrieben oder gedrosselt wurde.
func (a CoordAccess) Heartbeat() error {
	if a.AgentExternalID == "" {
		return ErrCoordForbidden
	}
	actor, err := a.mutationActor()
	if err != nil {
		return err
	}
	if !a.Store.CoordAgentPollDue(actor) {
		return nil
	}
	return a.Store.TouchCoordAgentPoll(actor)
}

func (a CoordAccess) ClaimDelivery(messageID int64) (bool, error) {
	if a.Store != nil && a.Store.writer != nil {
		return queueValue(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, messageID}, func(d *Store, p []any) (bool, error) {
			return queuedCoordAccess(d, p).ClaimDelivery(p[3].(int64))
		})
	}
	actor, err := a.mutationActor()
	if err != nil {
		return false, err
	}
	if _, _, err := a.messageTarget(messageID); err != nil {
		return false, err
	}
	return a.Store.ClaimCoordDelivery(messageID, actor)
}

// InjectedMessages nennt nur Zustellungen des eigenen Akteurs; fremde
// Empfänger sind nicht abfragbar.
func (a CoordAccess) InjectedMessages(messageIDs []int64) ([]int64, error) {
	actor, err := a.actor()
	if err != nil {
		return nil, err
	}
	if actor == "" {
		return nil, ErrCoordForbidden
	}
	return a.Store.CoordInjectedMessages(actor, messageIDs)
}

func (a CoordAccess) SearchThreads(project, query string, includeArchived bool, limit int) ([]Thread, error) {
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	tx, err := reader.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return nil, err
	}
	if !a.publicOnly {
		if err := a.roomAccessTx(tx, actor, RoomKeyForProject(project), true); err != nil {
			return nil, err
		}
	} else if err := a.projectRoomGate(RoomProject, RoomKeyForProject(project), ResRoom, tx); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	statement := `SELECT id,project,title,question,state,archived,person,author_principal_id,
		created_at,updated_at,COALESCE(resolved_at,'') FROM threads WHERE project=?`
	args := []any{project}
	if !includeArchived {
		statement += ` AND archived=0`
	}
	if q := strings.TrimSpace(query); q != "" {
		statement += ` AND (title LIKE ? OR question LIKE ?)`
		args = append(args, "%"+q+"%", "%"+q+"%")
	}
	statement += ` ORDER BY updated_at DESC,id DESC`
	rows, err := tx.Query(statement, args...)
	if err != nil {
		return nil, err
	}
	var candidates []Thread
	for rows.Next() {
		thread, scanErr := scanThread(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		candidates = append(candidates, thread)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]Thread, 0, len(candidates))
	guestCache := map[string]bool{}
	for _, thread := range candidates {
		if err := a.canReadThreadTx(tx, actor, thread.ID); err == nil {
			a.maskThreadPersonCachedTx(tx, &thread, guestCache)
			out = append(out, thread)
			if len(out) == limit {
				break
			}
		} else if !errors.Is(err, ErrCoordNotFound) && !errors.Is(err, ErrCoordForbidden) {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (a CoordAccess) ThreadsForObject(kind, id string) ([]Thread, error) {
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	tx, err := reader.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(`SELECT t.id,t.project,t.title,t.question,t.state,t.archived,t.person,t.author_principal_id,
		t.created_at,t.updated_at,COALESCE(t.resolved_at,'') FROM threads t
		JOIN thread_links l ON l.thread_id=t.id WHERE l.object_kind=? AND l.object_id=?
		ORDER BY t.updated_at DESC,t.id DESC`, kind, id)
	if err != nil {
		return nil, err
	}
	var candidates []Thread
	for rows.Next() {
		thread, scanErr := scanThread(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		candidates = append(candidates, thread)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	visible := make([]Thread, 0, len(candidates))
	guestCache := map[string]bool{}
	for _, thread := range candidates {
		if err := a.canReadThreadTx(tx, actor, thread.ID); err == nil {
			a.maskThreadPersonCachedTx(tx, &thread, guestCache)
			visible = append(visible, thread)
		} else if !errors.Is(err, ErrCoordNotFound) && !errors.Is(err, ErrCoordForbidden) {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return visible, nil
}

func (a CoordAccess) CreateThread(thread Thread) (int64, error) {
	if a.Store != nil && a.Store.writer != nil {
		return queueValue(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, thread}, func(d *Store, p []any) (int64, error) {
			return queuedCoordAccess(d, p).CreateThread(p[3].(Thread))
		})
	}
	if strings.TrimSpace(thread.Title) == "" || thread.Project == "" {
		return 0, fmt.Errorf("a thread needs a title and project")
	}
	tx, err := a.Store.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil || actor == "" || a.publicOnly {
		if err != nil {
			return 0, err
		}
		return 0, ErrCoordForbidden
	}
	if err := a.requireRoomAccessTx(tx, actor, RoomKeyForProject(thread.Project)); err != nil {
		return 0, err
	}
	state := thread.State
	if state == "" {
		state = ThreadOpen
	}
	ts := thread.CreatedAt
	if ts == "" {
		ts = now()
	}
	res, err := tx.Exec(`INSERT INTO threads(project,title,question,state,archived,person,author_principal_id,created_at,updated_at)
		VALUES(?,?,?,?,0,?,?,?,?)`, thread.Project, thread.Title, thread.Question, state, a.Principal.Label, a.Principal.ID, ts, ts)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	// A thread opened through the API (the MCP thread_open tool) lives in its
	// project's room like one opened in the browser; without this row the room
	// never listed it.
	if _, err := tx.Exec(`INSERT INTO thread_homes(thread_id,room_key,anchor_message_id,created_at) VALUES(?,?,NULL,?)`,
		id, RoomKeyForProject(thread.Project), ts); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (a CoordAccess) PromoteMessagesToThread(roomKey string, messageIDs []int64, thread Thread) (PromoteResult, error) {
	if a.Store != nil && a.Store.writer != nil {
		return queueValue(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, roomKey, messageIDs, thread}, func(d *Store, p []any) (PromoteResult, error) {
			return queuedCoordAccess(d, p).PromoteMessagesToThread(p[3].(string), p[4].([]int64), p[5].(Thread))
		})
	}
	if len(messageIDs) == 0 || strings.TrimSpace(thread.Title) == "" {
		return PromoteResult{}, fmt.Errorf("promoting needs messages and a title")
	}
	tx, err := a.Store.db.Begin()
	if err != nil {
		return PromoteResult{}, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil || actor == "" || a.publicOnly {
		if err != nil {
			return PromoteResult{}, err
		}
		return PromoteResult{}, ErrCoordForbidden
	}
	if err := a.requireRoomAccessTx(tx, actor, roomKey); err != nil {
		return PromoteResult{}, err
	}
	var kind string
	if err := tx.QueryRow(`SELECT kind FROM coord_rooms WHERE room_key=?`, roomKey).Scan(&kind); err != nil {
		return PromoteResult{}, err
	}
	if kind != RoomProject || roomKey != RoomKeyForProject(thread.Project) {
		return PromoteResult{}, ErrCoordForbidden
	}
	ts := now()
	res, err := tx.Exec(`INSERT INTO threads(project,title,question,state,archived,person,author_principal_id,created_at,updated_at)
		VALUES(?,?,?,'open',0,?,?,?,?)`, thread.Project, thread.Title, thread.Question, a.Principal.Label, a.Principal.ID, ts, ts)
	if err != nil {
		return PromoteResult{}, err
	}
	threadID, err := res.LastInsertId()
	if err != nil {
		return PromoteResult{}, err
	}
	out := PromoteResult{ThreadID: threadID}
	for _, messageID := range messageIDs {
		var body, sender, authorKind, createdAt, fromRoom string
		err := tx.QueryRow(`SELECT body,sender_external_id,author_kind,created_at,destination_id FROM coord_messages WHERE id=? AND destination_kind='room'`, messageID).
			Scan(&body, &sender, &authorKind, &createdAt, &fromRoom)
		if err != nil {
			out.Skipped = append(out.Skipped, strconv.FormatInt(messageID, 10)+" (not found)")
			continue
		}
		if fromRoom != roomKey {
			out.Skipped = append(out.Skipped, strconv.FormatInt(messageID, 10)+" (different room)")
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO thread_sources(thread_id,source_kind,source_id,room_key,author,author_kind,body,original_at,copied_at)
			VALUES(?,'coord_message',?,?,?,?,?,?,?)`, threadID, strconv.FormatInt(messageID, 10), roomKey, sender, authorKind, body, createdAt, ts); err != nil {
			return PromoteResult{}, err
		}
		out.Copied++
	}
	if out.Copied == 0 {
		return PromoteResult{}, fmt.Errorf("nothing was promoted: none of the messages belong to %s", roomKey)
	}
	if err := tx.Commit(); err != nil {
		return PromoteResult{}, err
	}
	return out, nil
}

func (a CoordAccess) PromoteRoomMessageToTaskThread(anchorMessageID int64, title, question, requestID string) (int64, error) {
	if a.Store != nil && a.Store.writer != nil {
		return queueValue(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, anchorMessageID, title, question, requestID}, func(d *Store, p []any) (int64, error) {
			return queuedCoordAccess(d, p).PromoteRoomMessageToTaskThread(p[3].(int64), p[4].(string), p[5].(string), p[6].(string))
		})
	}
	return a.createTaskThread("", anchorMessageID, title, question, requestID)
}

func (a CoordAccess) CreateTaskThreadInRoom(roomKey, title, question, requestID string) (int64, error) {
	if a.Store != nil && a.Store.writer != nil {
		return queueValue(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, roomKey, title, question, requestID}, func(d *Store, p []any) (int64, error) {
			return queuedCoordAccess(d, p).CreateTaskThreadInRoom(p[3].(string), p[4].(string), p[5].(string), p[6].(string))
		})
	}
	return a.createTaskThread(strings.TrimSpace(roomKey), 0, title, question, requestID)
}

func (a CoordAccess) createTaskThread(roomKey string, anchorMessageID int64, title, question, requestID string) (int64, error) {
	title = strings.TrimSpace(title)
	question = strings.TrimSpace(question)
	requestID = strings.TrimSpace(requestID)
	if title == "" || (anchorMessageID <= 0 && roomKey == "") {
		return 0, fmt.Errorf("home room and title are required")
	}
	tx, err := a.Store.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil || actor == "" || a.publicOnly {
		if err != nil {
			return 0, err
		}
		return 0, ErrCoordForbidden
	}
	var body, sender, authorKind, createdAt string
	if anchorMessageID > 0 {
		var anchorRoom string
		if err := tx.QueryRow(`SELECT destination_id,body,sender_external_id,author_kind,created_at
			FROM coord_messages WHERE id=? AND destination_kind='room'`, anchorMessageID).
			Scan(&anchorRoom, &body, &sender, &authorKind, &createdAt); errors.Is(err, sql.ErrNoRows) {
			return 0, ErrCoordNotFound
		} else if err != nil {
			return 0, err
		}
		if roomKey != "" && roomKey != anchorRoom {
			return 0, ErrCoordNotFound
		}
		roomKey = anchorRoom
	}
	if err := a.requireRoomAccessTx(tx, actor, roomKey); err != nil {
		return 0, err
	}
	var kind string
	if err := tx.QueryRow(`SELECT kind FROM coord_rooms WHERE room_key=?`, roomKey).Scan(&kind); err != nil {
		return 0, err
	}
	project := roomKey
	if kind == RoomProject {
		project = strings.TrimPrefix(roomKey, "project:")
	}
	if requestID != "" {
		requestNumber, parseErr := strconv.ParseInt(strings.TrimPrefix(requestID, "REQ-"), 10, 64)
		if parseErr != nil || requestNumber <= 0 || requestID != "REQ-"+strconv.FormatInt(requestNumber, 10) {
			return 0, fmt.Errorf("request link must be a canonical REQ-id")
		}
		var requestProject string
		if err := tx.QueryRow(`SELECT project FROM requests WHERE id=?`, requestNumber).Scan(&requestProject); errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("linked request not found")
		} else if err != nil {
			return 0, err
		}
		if kind == RoomProject && requestProject != "" && requestProject != project {
			return 0, ErrCoordForbidden
		}
		if project == roomKey && requestProject != "" {
			project = requestProject
		}
	}
	ts := now()
	res, err := tx.Exec(`INSERT INTO threads(project,title,question,state,archived,person,author_principal_id,created_at,updated_at)
		VALUES(?,?,?,'open',0,?,?,?,?)`, project, title, question, a.Principal.Label, a.Principal.ID, ts, ts)
	if err != nil {
		return 0, err
	}
	threadID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO thread_homes(thread_id,room_key,anchor_message_id,created_at) VALUES(?,?,?,?)`,
		threadID, roomKey, nullableCoordID(anchorMessageID), ts); err != nil {
		if anchorMessageID > 0 && strings.Contains(strings.ToLower(err.Error()), "unique") {
			var existingID int64
			var existingTitle, existingQuestion, existingRequest string
			lookupErr := tx.QueryRow(`SELECT t.id,t.title,t.question,COALESCE((
				SELECT l.object_id FROM thread_links l WHERE l.thread_id=t.id AND l.object_kind='request' ORDER BY l.rowid LIMIT 1),'')
				FROM thread_homes h JOIN threads t ON t.id=h.thread_id WHERE h.anchor_message_id=?`, anchorMessageID).
				Scan(&existingID, &existingTitle, &existingQuestion, &existingRequest)
			if lookupErr == nil && existingTitle == title && existingQuestion == question && existingRequest == requestID {
				return existingID, nil
			}
			return 0, ErrAnchorAlreadyThreaded
		}
		return 0, err
	}
	if anchorMessageID > 0 {
		if _, err := tx.Exec(`INSERT INTO thread_sources(thread_id,source_kind,source_id,room_key,author,author_kind,body,original_at,copied_at)
			VALUES(?,'coord_message',?,?,?,?,?,?,?)`, threadID, strconv.FormatInt(anchorMessageID, 10), roomKey, sender, authorKind, body, createdAt, ts); err != nil {
			return 0, err
		}
	}
	if requestID != "" {
		if _, err := tx.Exec(`INSERT INTO thread_links(thread_id,object_kind,object_id,object_revision,created_at)
			VALUES(?,'request',?,'',?)`, threadID, requestID, ts); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return threadID, nil
}

func (a CoordAccess) ThreadHome(threadID int64) (ThreadHome, error) {
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	tx, err := reader.db.Begin()
	if err != nil {
		return ThreadHome{}, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return ThreadHome{}, err
	}
	if err := a.canReadThreadTx(tx, actor, threadID); err != nil {
		return ThreadHome{}, err
	}
	home, found, err := threadHomeTx(tx, threadID)
	if err != nil {
		return ThreadHome{}, err
	}
	if !found {
		return ThreadHome{}, ErrCoordNotFound
	}
	if err := tx.Commit(); err != nil {
		return ThreadHome{}, err
	}
	return home, nil
}

func (a CoordAccess) RoomThreads(roomKey string) ([]RoomThread, error) {
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	tx, err := reader.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return nil, err
	}
	if err := a.roomAccessTx(tx, actor, roomKey, true); err != nil {
		return nil, err
	}
	threads, err := roomThreadsTx(tx, roomKey)
	if err != nil {
		return nil, err
	}
	if a.guestViewForMessageTx(tx, DestinationRoom, roomKey) {
		for i := range threads {
			maskThreadPerson(a, &threads[i].Thread)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return threads, nil
}

func (a CoordAccess) requireThreadMutation(threadID int64) (Thread, error) {
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	tx, err := reader.db.Begin()
	if err != nil {
		return Thread{}, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return Thread{}, err
	}
	thread, err := a.requireThreadMutationTx(tx, actor, threadID)
	if err != nil {
		return Thread{}, err
	}
	if err := tx.Commit(); err != nil {
		return Thread{}, err
	}
	return thread, nil
}

func (a CoordAccess) requireThreadMutationTx(tx *sql.Tx, actor string, threadID int64) (Thread, error) {
	if a.publicOnly {
		return Thread{}, ErrCoordForbidden
	}
	if err := a.threadAccessTx(tx, actor, threadID, false); err != nil {
		return Thread{}, err
	}
	row := tx.QueryRow(`SELECT id,project,title,question,state,archived,person,author_principal_id,
		created_at,updated_at,COALESCE(resolved_at,'') FROM threads WHERE id=?`, threadID)
	thread, err := scanThread(row)
	if err != nil {
		return Thread{}, ErrCoordNotFound
	}
	if thread.AuthorPrincipalID != "" && thread.AuthorPrincipalID == a.Principal.ID {
		return thread, nil
	}
	home, found, err := threadHomeTx(tx, threadID)
	if err != nil {
		return Thread{}, err
	}
	if found {
		var manager int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM coord_room_memberships
			WHERE room_key=? AND principal_id=? AND left_at='' AND is_manager=1`, home.RoomKey, actor).Scan(&manager); err != nil {
			return Thread{}, err
		}
		if manager > 0 {
			return thread, nil
		}
	}
	return Thread{}, ErrCoordForbidden
}

func (a CoordAccess) ThreadLinks(threadID int64) ([]ThreadLink, error) {
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	tx, err := reader.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return nil, err
	}
	if err := a.canReadThreadTx(tx, actor, threadID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(`SELECT thread_id,object_kind,object_id,object_revision,created_at FROM thread_links WHERE thread_id=? ORDER BY object_kind,object_id`, threadID)
	if err != nil {
		return nil, err
	}
	var out []ThreadLink
	for rows.Next() {
		var link ThreadLink
		if err := rows.Scan(&link.ThreadID, &link.Kind, &link.ID, &link.Revision, &link.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, link)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (a CoordAccess) LinkThread(link ThreadLink) error {
	if a.Store != nil && a.Store.writer != nil {
		return queueWrite(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, link}, func(d *Store, p []any) error {
			return queuedCoordAccess(d, p).LinkThread(p[3].(ThreadLink))
		})
	}
	if strings.TrimSpace(link.Kind) == "" || strings.TrimSpace(link.ID) == "" {
		return fmt.Errorf("a thread link needs an object kind and id")
	}
	tx, err := a.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return err
	}
	if _, err := a.requireThreadMutationTx(tx, actor, link.ThreadID); err != nil {
		return err
	}
	if link.Kind == "request" {
		if home, found, err := threadHomeTx(tx, link.ThreadID); err != nil {
			return err
		} else if found {
			requestNumber, parseErr := strconv.ParseInt(strings.TrimPrefix(link.ID, "REQ-"), 10, 64)
			if parseErr != nil || requestNumber <= 0 || link.ID != "REQ-"+strconv.FormatInt(requestNumber, 10) {
				return fmt.Errorf("request link must be a canonical REQ-id")
			}
			var requestProject string
			if err := tx.QueryRow(`SELECT project FROM requests WHERE id=?`, requestNumber).Scan(&requestProject); errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("linked request not found")
			} else if err != nil {
				return err
			}
			var roomKind string
			if err := tx.QueryRow(`SELECT kind FROM coord_rooms WHERE room_key=?`, home.RoomKey).Scan(&roomKind); err != nil {
				return err
			}
			if roomKind == RoomProject && requestProject != "" && requestProject != strings.TrimPrefix(home.RoomKey, "project:") {
				return ErrCoordForbidden
			}
			var existingID string
			err := tx.QueryRow(`SELECT object_id FROM thread_links WHERE thread_id=? AND object_kind='request' ORDER BY rowid LIMIT 1`, link.ThreadID).Scan(&existingID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil && existingID == link.ID {
				return tx.Commit()
			}
			if err == nil {
				return fmt.Errorf("a task thread can link only one request")
			}
		}
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO thread_links(thread_id,object_kind,object_id,object_revision,created_at)
		VALUES(?,?,?,?,?)`, link.ThreadID, link.Kind, link.ID, link.Revision, now()); err != nil {
		return err
	}
	return tx.Commit()
}

func (a CoordAccess) SetThreadState(threadID int64, state string) error {
	if a.Store != nil && a.Store.writer != nil {
		return queueWrite(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, threadID, state}, func(d *Store, p []any) error {
			return queuedCoordAccess(d, p).SetThreadState(p[3].(int64), p[4].(string))
		})
	}
	if state != ThreadOpen && state != ThreadResolved && state != ThreadDeferred {
		return fmt.Errorf("unknown thread state %q", state)
	}
	tx, err := a.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return err
	}
	if _, err := a.requireThreadMutationTx(tx, actor, threadID); err != nil {
		return err
	}
	ts := now()
	var resolved any
	if state == ThreadResolved {
		resolved = ts
	}
	if _, err := tx.Exec(`UPDATE threads SET state=?,resolved_at=?,updated_at=? WHERE id=? AND state<>?`, state, resolved, ts, threadID, state); err != nil {
		return err
	}
	return tx.Commit()
}

func (a CoordAccess) SetThreadArchived(threadID int64, archived bool) error {
	if a.Store != nil && a.Store.writer != nil {
		return queueWrite(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, threadID, archived}, func(d *Store, p []any) error {
			return queuedCoordAccess(d, p).SetThreadArchived(p[3].(int64), p[4].(bool))
		})
	}
	tx, err := a.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return err
	}
	if _, err := a.requireThreadMutationTx(tx, actor, threadID); err != nil {
		return err
	}
	flag := 0
	if archived {
		flag = 1
	}
	if _, err := tx.Exec(`UPDATE threads SET archived=?,updated_at=? WHERE id=? AND archived<>?`, flag, now(), threadID, flag); err != nil {
		return err
	}
	return tx.Commit()
}

func (a CoordAccess) TouchThread(threadID int64) error {
	if a.Store != nil && a.Store.writer != nil {
		return queueWrite(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, threadID}, func(d *Store, p []any) error {
			return queuedCoordAccess(d, p).TouchThread(p[3].(int64))
		})
	}
	tx, err := a.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return err
	}
	if _, err := a.requireThreadMutationTx(tx, actor, threadID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE threads SET updated_at=? WHERE id=?`, now(), threadID); err != nil {
		return err
	}
	return tx.Commit()
}

func (a CoordAccess) ThreadSummary(threadID int64) (ThreadSummary, bool, error) {
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	tx, err := reader.db.Begin()
	if err != nil {
		return ThreadSummary{}, false, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return ThreadSummary{}, false, err
	}
	if err := a.canReadThreadTx(tx, actor, threadID); err != nil {
		return ThreadSummary{}, false, err
	}
	var summary ThreadSummary
	err = tx.QueryRow(`SELECT thread_id,revision,body,open_questions,covers_through_sequence,person,created_at
		FROM thread_summaries WHERE thread_id=? ORDER BY revision DESC LIMIT 1`, threadID).
		Scan(&summary.ThreadID, &summary.Revision, &summary.Body, &summary.OpenQuestions, &summary.CoversThrough, &summary.Person, &summary.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return ThreadSummary{}, false, err
		}
		return ThreadSummary{}, false, nil
	}
	if err != nil {
		return ThreadSummary{}, false, err
	}
	if a.guestViewForMessageTx(tx, DestinationDiscussion, strconv.FormatInt(threadID, 10)) {
		// Die Zusammenfassung trägt nur den Kontonamen; ein Gast bekommt keinen.
		summary.Person = ""
	}
	if err := tx.Commit(); err != nil {
		return ThreadSummary{}, false, err
	}
	return summary, true, nil
}

func (a CoordAccess) PutThreadSummary(summary ThreadSummary) (int, error) {
	if a.Store != nil && a.Store.writer != nil {
		return queueValue(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, summary}, func(d *Store, p []any) (int, error) {
			return queuedCoordAccess(d, p).PutThreadSummary(p[3].(ThreadSummary))
		})
	}
	tx, err := a.Store.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return 0, err
	}
	if _, err := a.requireThreadMutationTx(tx, actor, summary.ThreadID); err != nil {
		return 0, err
	}
	var next int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(revision),0)+1 FROM thread_summaries WHERE thread_id=?`, summary.ThreadID).Scan(&next); err != nil {
		return 0, err
	}
	at := summary.CreatedAt
	if at == "" {
		at = now()
	}
	if _, err := tx.Exec(`INSERT INTO thread_summaries(thread_id,revision,body,open_questions,covers_through_sequence,person,created_at)
		VALUES(?,?,?,?,?,?,?)`, summary.ThreadID, next, summary.Body, summary.OpenQuestions, summary.CoversThrough, a.Principal.Label, at); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return next, nil
}

func (a CoordAccess) ThreadOutcomes(threadID int64) ([]ThreadOutcome, error) {
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	tx, err := reader.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return nil, err
	}
	if err := a.canReadThreadTx(tx, actor, threadID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(`SELECT thread_id,kind,ref_id,state,note,created_at,COALESCE(decided_at,'') FROM thread_outcomes WHERE thread_id=? ORDER BY kind,ref_id`, threadID)
	if err != nil {
		return nil, err
	}
	var out []ThreadOutcome
	for rows.Next() {
		var outcome ThreadOutcome
		if err := rows.Scan(&outcome.ThreadID, &outcome.Kind, &outcome.RefID, &outcome.State, &outcome.Note, &outcome.CreatedAt, &outcome.DecidedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, outcome)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (a CoordAccess) PutThreadOutcome(outcome ThreadOutcome) error {
	if a.Store != nil && a.Store.writer != nil {
		return queueWrite(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, outcome}, func(d *Store, p []any) error {
			return queuedCoordAccess(d, p).PutThreadOutcome(p[3].(ThreadOutcome))
		})
	}
	if outcome.State != "" && outcome.State != OutcomeProposed && outcome.State != OutcomeAccepted && outcome.State != OutcomeRejected {
		return fmt.Errorf("unknown outcome state %q", outcome.State)
	}
	tx, err := a.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return err
	}
	if _, err := a.requireThreadMutationTx(tx, actor, outcome.ThreadID); err != nil {
		return err
	}
	state := outcome.State
	if state == "" {
		state = OutcomeProposed
	}
	ts := now()
	var decided any
	if state != OutcomeProposed {
		decided = ts
	}
	if _, err := tx.Exec(`INSERT INTO thread_outcomes(thread_id,kind,ref_id,state,note,created_at,decided_at)
		VALUES(?,?,?,?,?,?,?) ON CONFLICT(thread_id,kind,ref_id) DO UPDATE SET
		state=excluded.state,note=excluded.note,decided_at=excluded.decided_at`,
		outcome.ThreadID, outcome.Kind, outcome.RefID, state, outcome.Note, ts, decided); err != nil {
		return err
	}
	return tx.Commit()
}

func (a CoordAccess) CreateGroup(in GroupInput) (CoordRoom, error) {
	if a.Store != nil && a.Store.writer != nil {
		return queueValue(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, in}, func(d *Store, p []any) (CoordRoom, error) {
			return queuedCoordAccess(d, p).CreateGroup(p[3].(GroupInput))
		})
	}
	actor, err := a.mutationActor()
	if err != nil {
		return CoordRoom{}, err
	}
	in.Creator = actor
	if err := a.validatePrivateRecipients(actor, in.Members); err != nil {
		return CoordRoom{}, err
	}
	return a.Store.CreateCoordGroup(in)
}

func (a CoordAccess) EnsureDirect(room CoordRoom) error {
	if a.Store != nil && a.Store.writer != nil {
		return queueWrite(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, room}, func(d *Store, p []any) error {
			return queuedCoordAccess(d, p).EnsureDirect(p[3].(CoordRoom))
		})
	}
	actor, err := a.mutationActor()
	if err != nil {
		return err
	}
	if room.Kind != RoomDirect || !containsMember(normalizeMembers(room.Members), actor) {
		return ErrCoordNotFound
	}
	if err := a.validatePrivateRecipients(actor, room.Members); err != nil {
		return err
	}
	room.Actor = actor
	return a.Store.EnsureCoordRoom(room)
}

func (a CoordAccess) UpdateGroup(in GroupUpdate) error {
	if a.Store != nil && a.Store.writer != nil {
		return queueWrite(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, in}, func(d *Store, p []any) error {
			return queuedCoordAccess(d, p).UpdateGroup(p[3].(GroupUpdate))
		})
	}
	actor, err := a.mutationActor()
	if err != nil {
		return err
	}
	if actor != in.Actor {
		return ErrCoordForbidden
	}
	if _, err := a.requireRoomAccess(in.RoomKey); err != nil {
		return err
	}
	if err := a.validatePrivateRecipients(actor, append(append([]string{}, in.Add...), in.AddManagers...)); err != nil {
		return err
	}
	return a.Store.UpdateCoordGroup(in)
}

func (a CoordAccess) LeaveRoom(roomKey, principalID string) error {
	if a.Store != nil && a.Store.writer != nil {
		return queueWrite(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, roomKey, principalID}, func(d *Store, p []any) error {
			return queuedCoordAccess(d, p).LeaveRoom(p[3].(string), p[4].(string))
		})
	}
	actor, err := a.mutationActor()
	if err != nil {
		return err
	}
	room, err := a.requireRoomAccess(roomKey)
	if err != nil {
		return err
	}
	if room.Kind != RoomGroup {
		return fmt.Errorf("%w: only groups can be left", ErrInvalidGroup)
	}
	if actor != principalID {
		return fmt.Errorf("%w: use manager-authorized group update", ErrCoordForbidden)
	}
	return a.Store.LeaveCoordRoom(roomKey, principalID, actor)
}

// Recipients returns principals the current actor has actually encountered in
// a room they can read. It is the shared boundary used before creating or
// expanding private conversations; accepting arbitrary caller-supplied IDs
// would turn the UI and API into a principal-enumeration surface.
func (a CoordAccess) Recipients() ([]CoordRecipient, error) {
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	tx, err := reader.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return nil, err
	}
	if actor == "" {
		return nil, ErrCoordForbidden
	}
	roomRows, err := tx.Query(`SELECT room_key FROM coord_rooms ORDER BY room_key`)
	if err != nil {
		return nil, err
	}
	var rooms []string
	for roomRows.Next() {
		var key string
		if err := roomRows.Scan(&key); err != nil {
			roomRows.Close()
			return nil, err
		}
		rooms = append(rooms, key)
	}
	if err := roomRows.Err(); err != nil {
		roomRows.Close()
		return nil, err
	}
	if err := roomRows.Close(); err != nil {
		return nil, err
	}
	ids := make(map[string]bool)
	for _, room := range rooms {
		if err := a.canWriteTx(tx, actor, DestinationRoom, room); err != nil {
			if errors.Is(err, ErrCoordNotFound) || errors.Is(err, ErrCoordForbidden) {
				continue
			}
			return nil, err
		}
		// Wer Mitglieder eines Projektraums sehen darf, regelt dieselbe Stufe wie
		// bei Peers: ab member. Ein Gast liest den Raum, bekommt aber keine Liste.
		if a.projectRoomGate(roomKindOf(room), room, ResAgents, tx) != nil {
			continue
		}
		memberRows, err := tx.Query(`SELECT principal_id FROM coord_room_memberships WHERE room_key=? AND left_at=''`, room)
		if err != nil {
			return nil, err
		}
		var members []string
		for memberRows.Next() {
			var member string
			if err := memberRows.Scan(&member); err != nil {
				memberRows.Close()
				return nil, err
			}
			if member != actor {
				ids[member] = true
			}
			members = append(members, member)
		}
		if err := memberRows.Err(); err != nil {
			memberRows.Close()
			return nil, err
		}
		if err := memberRows.Close(); err != nil {
			return nil, err
		}
		for _, member := range members {
			var owner string
			queryErr := tx.QueryRow(`SELECT COALESCE(principal_id,'') FROM coord_agents WHERE external_id=?`, member).Scan(&owner)
			if queryErr != nil && !errors.Is(queryErr, sql.ErrNoRows) {
				return nil, queryErr
			}
			if owner != "" && owner != a.Principal.ID {
				ids[owner] = true
			}
		}
	}
	out := make([]CoordRecipient, 0, len(ids))
	for id := range ids {
		recipient := CoordRecipient{PrincipalID: id, Label: id, Kind: "session"}
		if strings.HasPrefix(id, "person:") {
			recipient.Kind = "person"
			if personID, parseErr := parsePersonPrincipalID(id); parseErr == nil {
				var label string
				if queryErr := tx.QueryRow(`SELECT name FROM persons WHERE id=?`, personID).Scan(&label); queryErr == nil && label != "" {
					recipient.Label = label
				}
			}
		} else {
			externalID := id
			if strings.HasPrefix(id, "agent:") {
				recipient.Kind = "agent"
				externalID = strings.TrimPrefix(id, "agent:")
			}
			var label string
			if queryErr := tx.QueryRow(`SELECT display_name FROM coord_agents WHERE external_id=?`, externalID).Scan(&label); queryErr == nil && strings.TrimSpace(label) != "" {
				recipient.Label = label
			}
		}
		out = append(out, recipient)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Label != out[j].Label {
			return out[i].Label < out[j].Label
		}
		return out[i].PrincipalID < out[j].PrincipalID
	})
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (a CoordAccess) ValidateRoomParticipants(roomKey string, principals []string) error {
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	tx, err := reader.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return err
	}
	if err := a.canWriteTx(tx, actor, DestinationRoom, roomKey); err != nil {
		return err
	}
	if err := a.validateRoomParticipantsTx(tx, actor, roomKey, principals); err != nil {
		return err
	}
	return tx.Commit()
}

func (a CoordAccess) validateRoomParticipantsTx(tx *sql.Tx, actor, roomKey string, principals []string) error {
	allowed, err := a.mentionTargetsTx(tx, actor, roomKey)
	if err != nil {
		return err
	}
	for _, id := range normalizeMembers(principals) {
		if !allowed[id] {
			return fmt.Errorf("%w: %s", ErrCoordUnknownRecipient, id)
		}
	}
	return nil
}

// mentionTargetsTx sind die Prinzipale, die in einem Raum erwähnt werden
// dürfen: seine aktiven Mitglieder, deren Besitzer und der Absender selbst.
func (a CoordAccess) mentionTargetsTx(tx *sql.Tx, actor, roomKey string) (map[string]bool, error) {
	allowed := map[string]bool{actor: true, a.Principal.ID: true}
	rows, err := tx.Query(`SELECT principal_id FROM coord_room_memberships WHERE room_key=? AND left_at=''`, roomKey)
	if err != nil {
		return nil, err
	}
	var participantIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		allowed[id] = true
		participantIDs = append(participantIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, id := range participantIDs {
		var owner string
		qerr := tx.QueryRow(`SELECT COALESCE(principal_id,'') FROM coord_agents WHERE external_id=?`, id).Scan(&owner)
		if qerr != nil && !errors.Is(qerr, sql.ErrNoRows) {
			return nil, qerr
		}
		if owner != "" {
			allowed[owner] = true
		}
	}
	return allowed, nil
}

// resolveMentionsTx liefert die Erwähnungen, die wirklich gespeichert werden.
//
// Ein Mitglied bekommt bei einem unbekannten Ziel einen Fehler: es sieht die
// Mitgliederliste ohnehin. Ein Gast sieht sie nicht; für ihn würde Fehler
// gegen Erfolg verraten, wer im Raum ist. Er bekommt deshalb immer Erfolg, und
// Ziele außerhalb des Raums fallen stumm weg: keine Zustellung, kein
// Attention-Eintrag. Es wird nie an jemanden außerhalb des Raums zugestellt.
//
// Das Rohe bleibt zur Anzeige für den Gast erhalten (zweiter Rückgabewert):
// was er von seinen Beiträgen zurückliest, ist seine eigene Eingabe und nie das
// gefilterte Ergebnis. Sonst wäre das Zurücklesen ein Orakel für die Mitglieder.
func (a CoordAccess) resolveMentionsTx(tx *sql.Tx, actor, roomKey string, mentions []string) (kept, raw []string, err error) {
	if a.projectRoomGate(roomKindOf(roomKey), roomKey, ResAgents, tx) == nil {
		return mentions, nil, a.validateRoomParticipantsTx(tx, actor, roomKey, mentions)
	}
	allowed, err := a.mentionTargetsTx(tx, actor, roomKey)
	if err != nil {
		return nil, nil, err
	}
	raw = normalizeMembers(mentions)
	for _, id := range raw {
		if allowed[id] {
			kept = append(kept, id)
		}
	}
	return kept, raw, nil
}

// guestViewForMessageTx ist die EINE Frage hinter jeder Anzeige von Zustand
// pro Empfänger an eigenen Beiträgen: sieht dieser Leser die Mitglieder des
// Projektraums nicht, in dem die Nachricht liegt (Gast)? Wer sie bejaht,
// zeigt dem Gast nur, was er selbst eingegeben hat, nie, wer erreicht wurde.
//
// Den Raum leitet messageRoomKeyTx ab, auch für Threads ohne thread_homes-Zeile.
// Verbraucher: Attention, Attention-Ereignisse und Zustellereignisse im Log,
// Zustellübersicht, Erwähnungen, Standing, ResolveAttention (Zurückziehen).
// Im Logmodus gilt wie überall das alte Verhalten.
func (a CoordAccess) guestViewForMessageTx(tx *sql.Tx, kind, id string) bool {
	roomKey := messageRoomKeyTx(tx, kind, id)
	return roomKey != "" && a.projectRoomGate(roomKindOf(roomKey), roomKey, ResAgents, tx) != nil
}

// messageRoomKeyTx ist der Raum, in dem eine Nachricht liegt (bei Threads der
// Heimatraum oder der Projektraum des Alt-Threads), sonst "".
func messageRoomKeyTx(tx *sql.Tx, kind, id string) string {
	if kind == DestinationRoom {
		return id
	}
	threadID, err := strconv.ParseInt(id, 10, 64)
	if err != nil || threadID <= 0 {
		return ""
	}
	if home, found, err := threadHomeTx(tx, threadID); err == nil && found {
		return home.RoomKey
	}
	var project string
	if err := tx.QueryRow(`SELECT project FROM threads WHERE id=?`, threadID).Scan(&project); err != nil {
		return ""
	}
	return RoomKeyForProject(project)
}

func insertRawMentionsTx(tx *sql.Tx, messageID int64, raw []string) error {
	for _, mention := range raw {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO coord_message_raw_mentions(message_id,mentioned_external_id)
			VALUES(?,?)`, messageID, mention); err != nil {
			return err
		}
	}
	return nil
}

// rawMentionsTx liefert die eingegebenen Erwähnungen einer Gast-Nachricht oder
// nil, wenn es keine gibt.
func rawMentionsTx(tx *sql.Tx, messageID int64) ([]string, error) {
	rows, err := tx.Query(`SELECT mentioned_external_id FROM coord_message_raw_mentions WHERE message_id=? ORDER BY mentioned_external_id`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (a CoordAccess) Standing(roomKey string) ([]StandingInstruction, error) {
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	tx, err := reader.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return nil, err
	}
	if err := a.canReadTx(tx, actor, DestinationRoom, roomKey); err != nil {
		return nil, err
	}
	rows, err := tx.Query(`SELECT room_key,message_id,person,body,targets,created_at FROM coord_standing WHERE room_key=? AND ended_at IS NULL ORDER BY created_at`, roomKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StandingInstruction
	for rows.Next() {
		var in StandingInstruction
		var targets string
		if err := rows.Scan(&in.RoomKey, &in.MessageID, &in.Person, &in.Body, &targets, &in.CreatedAt); err != nil {
			return nil, err
		}
		in.Targets = strings.Fields(targets)
		out = append(out, in)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if !a.guestViewForMessageTx(tx, DestinationRoom, roomKey) {
		for i := range out {
			out[i].Person = NormalizeAccountName(out[i].Person)
		}
	} else {
		for i := range out {
			// Person ist der Kontoname des Urhebers; der Gast sieht die ID.
			var sender string
			if err := tx.QueryRow(`SELECT sender_external_id FROM coord_messages WHERE id=?`, out[i].MessageID).Scan(&sender); err == nil {
				out[i].Person = sender
			} else {
				out[i].Person = ""
			}
			messageID, perr := strconv.ParseInt(out[i].MessageID, 10, 64)
			if perr != nil {
				continue
			}
			raw, rerr := rawMentionsTx(tx, messageID)
			if rerr != nil {
				return nil, rerr
			}
			if raw != nil {
				out[i].Targets = raw
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (a CoordAccess) CreateStanding(in StandingInput) (int64, error) {
	if a.Store != nil && a.Store.writer != nil {
		return queueValue(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, in}, func(d *Store, p []any) (int64, error) {
			return queuedCoordAccess(d, p).CreateStanding(p[3].(StandingInput))
		})
	}
	actor, err := a.mutationActor()
	if err != nil {
		return 0, err
	}
	if ReservedExternalID(actor) {
		return 0, ErrCoordForbidden
	}
	in.RoomKey = strings.TrimSpace(in.RoomKey)
	in.ClientID = strings.TrimSpace(in.ClientID)
	in.Body = strings.TrimSpace(in.Body)
	in.Mentions = normalizeMembers(in.Mentions)
	if in.RoomKey == "" || in.ClientID == "" || in.Body == "" {
		return 0, fmt.Errorf("standing instruction needs room, client id and body")
	}
	tx, err := a.Store.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := a.requireRoomAccessTx(tx, actor, in.RoomKey); err != nil {
		return 0, err
	}
	mentions, raw, err := a.resolveMentionsTx(tx, actor, in.RoomKey, in.Mentions)
	if err != nil {
		return 0, err
	}
	in.Mentions = normalizeMembers(mentions)
	message := CoordMessage{DestinationKind: DestinationRoom, DestinationID: in.RoomKey, SenderExternalID: actor, AuthorPrincipalID: a.Principal.ID, ClientID: in.ClientID, Body: in.Body, Intent: IntentStanding, ExpiresAt: strings.TrimSpace(in.ExpiresAt), Mentions: in.Mentions}
	message.AuthorKind = a.authorKind()
	id, err := appendCoordMessageTx(tx, message)
	if err != nil {
		return 0, err
	}
	if err := insertRawMentionsTx(tx, id, raw); err != nil {
		return 0, err
	}
	var destination, body, intent, authorPrincipal, expiresAt string
	if err := tx.QueryRow(`SELECT destination_id,body,intent,author_principal_id,COALESCE(expires_at,'') FROM coord_messages WHERE id=? AND destination_kind='room'`, id).Scan(&destination, &body, &intent, &authorPrincipal, &expiresAt); err != nil {
		return 0, err
	}
	mentionRows, err := tx.Query(`SELECT mentioned_external_id FROM coord_message_mentions WHERE message_id=? ORDER BY mentioned_external_id`, id)
	if err != nil {
		return 0, err
	}
	var storedMentions []string
	for mentionRows.Next() {
		var mention string
		if err := mentionRows.Scan(&mention); err != nil {
			mentionRows.Close()
			return 0, err
		}
		storedMentions = append(storedMentions, mention)
	}
	if err := mentionRows.Err(); err != nil {
		mentionRows.Close()
		return 0, err
	}
	if err := mentionRows.Close(); err != nil {
		return 0, err
	}
	if destination != in.RoomKey || body != in.Body || intent != IntentStanding || authorPrincipal != a.Principal.ID || expiresAt != strings.TrimSpace(in.ExpiresAt) || strings.Join(storedMentions, "\x00") != strings.Join(in.Mentions, "\x00") {
		return 0, fmt.Errorf("standing client id already belongs to another message")
	}
	person := a.Principal.Label
	if person == "" {
		person = a.Principal.ID
	}
	if _, err := tx.Exec(`INSERT INTO coord_standing(room_key,message_id,person,body,targets,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(room_key,message_id) DO UPDATE SET body=excluded.body,targets=excluded.targets`, in.RoomKey, FormatMessageID(id), person, in.Body, strings.Join(in.Mentions, " "), now()); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (a CoordAccess) EndStanding(roomKey, messageID string) error {
	if a.Store != nil && a.Store.writer != nil {
		return queueWrite(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, roomKey, messageID}, func(d *Store, p []any) error {
			return queuedCoordAccess(d, p).EndStanding(p[3].(string), p[4].(string))
		})
	}
	actor, err := a.mutationActor()
	if err != nil {
		return err
	}
	tx, err := a.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := a.requireRoomAccessTx(tx, actor, roomKey); err != nil {
		return err
	}
	if !canEndStandingTx(tx, a.Principal.ID, a.AgentExternalID, roomKey, messageID, a.projectRoomGate(roomKindOf(roomKey), roomKey, ResAgents, tx) != nil) {
		return ErrCoordForbidden
	}
	endedBy := a.Principal.Label
	if endedBy == "" {
		endedBy = a.Principal.ID
	}
	res, err := tx.Exec(`UPDATE coord_standing SET ended_at=?,ended_by=? WHERE room_key=? AND message_id=? AND ended_at IS NULL`, now(), endedBy, roomKey, messageID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrCoordNotFound
	}
	return tx.Commit()
}

func (a CoordAccess) validatePrivateRecipients(actor string, principals []string) error {
	known, err := a.Recipients()
	if err != nil {
		return err
	}
	allowed := map[string]bool{actor: true}
	for _, recipient := range known {
		allowed[recipient.PrincipalID] = true
	}
	for _, principal := range normalizeMembers(principals) {
		if !allowed[principal] {
			return fmt.Errorf("%w: %s", ErrCoordUnknownRecipient, principal)
		}
	}
	return nil
}

// projectRoomGate legt die Projektrolle über die Mitgliedschaft im Projektraum
// (Spec 8.1, Koordination): ohne Rolle im Projekt gibt es den Raum nicht, der
// Gast liest und schreibt (seine Beiträge gelten als Bitten), Agenten und Peers
// zeigt der Raum erst ab member. DMs, Gruppen und Maschinenräume bleiben bei der
// reinen Mitgliedschaft; ein Projekt-Owner liest keine fremden DMs. Mit tx wird
// die Rolle in derselben Transaktion gelesen, sonst über den Store.
//
// Eine Remote ohne Projektzeile ist unbeansprucht; ihr Raum bleibt wie bisher
// offen für Mitglieder des Raums, bis ein Owner sie beansprucht.
func (a CoordAccess) projectRoomGate(kind, roomKey string, res Resource, tx rowQuerier) error {
	if kind != RoomProject || a.Store == nil {
		return nil
	}
	project := strings.TrimPrefix(roomKey, "project:")
	acct, ok := accountNumericID(a.Principal.ID)
	var role RoleInfo
	if ok {
		if tx != nil {
			role = projectRoleTx(tx, project, acct)
		} else {
			role = a.Store.ProjectRole(project, a.Principal.ID)
		}
	}
	d := Decision{Allowed: matrixAllows(role, res, ActRead, Object{})}
	if !d.Allowed {
		d.Hidden, d.Reason = true, "no access to the project room"
		if RoleRank(role.Role) == 0 && !a.projectClaimed(tx, project) {
			d = Decision{Allowed: true, Reason: "unclaimed project"}
		}
	}
	if a.Store.accessCfg().apply(a.Principal.ID, project, res, ActRead, d) {
		return nil
	}
	return ErrCoordNotFound
}

func (a CoordAccess) projectClaimed(tx rowQuerier, project string) bool {
	if tx != nil {
		_, claimed := projectTx(tx, project)
		return claimed
	}
	_, claimed := a.Store.ProjectByRemote(project)
	return claimed
}

// roomKindOf liest die Art eines Raums aus dem Schlüssel. Nur Projekträume
// tragen das Präfix; für alle anderen Arten gilt projectRoomGate nicht.
func roomKindOf(roomKey string) string {
	if strings.HasPrefix(roomKey, "project:") {
		return RoomProject
	}
	return ""
}
