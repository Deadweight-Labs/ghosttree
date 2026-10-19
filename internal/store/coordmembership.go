package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidGroup        = errors.New("invalid coordination group")
	ErrLegacyGroupReadOnly = errors.New("legacy managerless group is read-only")
	ErrLastGroupManager    = errors.New("group must retain an active manager")
)

type RoomMembership struct {
	RoomKey     string `json:"room_key"`
	PrincipalID string `json:"principal_id"`
	JoinedAt    string `json:"joined_at"`
	LeftAt      string `json:"left_at,omitempty"`
	Manager     bool   `json:"manager"`
}

type GroupInput struct {
	Label   string   `json:"label"`
	Creator string   `json:"creator"`
	Members []string `json:"members"`
}

type GroupUpdate struct {
	RoomKey        string   `json:"room_key"`
	Actor          string   `json:"actor"`
	Label          *string  `json:"label,omitempty"`
	Add            []string `json:"add,omitempty"`
	Remove         []string `json:"remove,omitempty"`
	AddManagers    []string `json:"add_managers,omitempty"`
	RemoveManagers []string `json:"remove_managers,omitempty"`
}

type RoomMembershipEvent struct {
	ID          int64  `json:"id"`
	RoomKey     string `json:"room_key"`
	PrincipalID string `json:"principal_id"`
	Action      string `json:"action"`
	ActorID     string `json:"actor_id"`
	CreatedAt   string `json:"created_at"`
}

func membershipTime() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func migrateCoordRoomMemberships(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var migrated int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM coord_room_membership_migrations WHERE version=1`).Scan(&migrated); err != nil {
		return err
	}
	if migrated > 0 {
		return tx.Commit()
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO coord_rooms(room_key,kind,label,created_at)
		SELECT DISTINCT room_key,
		CASE WHEN room_key LIKE 'machine:%' THEN 'machine' ELSE 'project' END,'',registered_at
		FROM coord_agents WHERE room_key LIKE 'machine:%' OR room_key LIKE 'project:%'`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO coord_room_memberships(room_key,principal_id,joined_at,left_at,is_manager)
		SELECT room_key,external_id,registered_at,'',0 FROM coord_agents
		WHERE room_key LIKE 'machine:%' OR room_key LIKE 'project:%'`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO coord_room_memberships(room_key,principal_id,joined_at,left_at,is_manager)
		SELECT m.room_key,m.member_external_id,m.joined_at,'',0
		FROM coord_room_members m
		JOIN coord_agents a ON a.external_id=m.member_external_id
		WHERE COALESCE(a.person,'')<>''`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO coord_room_membership_migrations(version,migrated_at) VALUES(1,?)`, membershipTime()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) JoinCoordRoom(m RoomMembership) error {
	if strings.TrimSpace(m.RoomKey) == "" || strings.TrimSpace(m.PrincipalID) == "" {
		return fmt.Errorf("room key and principal id are required")
	}
	if s.writer != nil {
		return queueWrite(s, []any{m}, func(d *Store, p []any) error {
			return d.JoinCoordRoom(p[0].(RoomMembership))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	kind, err := coordRoomKindTx(tx, m.RoomKey)
	if err != nil {
		return err
	}
	if kind == RoomGroup {
		managers, err := activeManagerCount(tx, m.RoomKey)
		if err != nil {
			return err
		}
		if managers == 0 {
			return ErrLegacyGroupReadOnly
		}
		return fmt.Errorf("%w: group joins require a manager-authorized update", ErrInvalidGroup)
	}
	joined := m.JoinedAt
	if joined == "" {
		joined = membershipTime()
	}
	result, err := tx.Exec(`INSERT INTO coord_room_memberships(room_key,principal_id,joined_at,left_at,is_manager)
		VALUES(?,?,?,'',?) ON CONFLICT DO NOTHING`, m.RoomKey, m.PrincipalID, joined, m.Manager)
	if err != nil {
		return err
	}
	added, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if added > 0 {
		if kind == RoomGroup {
			if err := appendMembershipEvent(tx, m.RoomKey, m.PrincipalID, "join", "", joined); err != nil {
				return err
			}
			if m.Manager {
				if err := appendMembershipEvent(tx, m.RoomKey, m.PrincipalID, "manager_grant", "", joined); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}

func (s *Store) LeaveCoordRoom(roomKey, principalID, actorID string) error {
	if s.writer != nil {
		return queueWrite(s, []any{roomKey, principalID, actorID}, func(d *Store, p []any) error {
			return d.LeaveCoordRoom(p[0].(string), p[1].(string), p[2].(string))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	kind, err := coordRoomKindTx(tx, roomKey)
	if err != nil {
		return err
	}
	var targetManager bool
	if kind == RoomGroup {
		managers, err := activeManagerCount(tx, roomKey)
		if err != nil {
			return err
		}
		if managers == 0 {
			return ErrLegacyGroupReadOnly
		}
		targetManager, err = activeManager(tx, roomKey, principalID)
		if err != nil {
			return err
		}
		if targetManager && managers == 1 {
			return ErrLastGroupManager
		}
	}
	if actorID != principalID {
		ok, err := activeManager(tx, roomKey, actorID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: actor is not a room manager", ErrInvalidGroup)
		}
	}
	result, err := tx.Exec(`UPDATE coord_room_memberships SET left_at=?
		WHERE room_key=? AND principal_id=? AND left_at=''`, membershipTime(), roomKey, principalID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("membership not found")
	}
	if kind == RoomGroup {
		at := membershipTime()
		if err := appendMembershipEvent(tx, roomKey, principalID, "leave", actorID, at); err != nil {
			return err
		}
		if targetManager {
			if err := appendMembershipEvent(tx, roomKey, principalID, "manager_revoke", actorID, at); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) CoordRoomMemberships(roomKey string) ([]RoomMembership, error) {
	if s.reader != nil {
		return s.reader.CoordRoomMemberships(roomKey)
	}
	rows, err := s.db.Query(`SELECT room_key,principal_id,joined_at,left_at,is_manager
		FROM coord_room_memberships WHERE room_key=? ORDER BY joined_at,principal_id`, roomKey)
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
	return out, rows.Err()
}

func (s *Store) CreateCoordGroup(in GroupInput) (CoordRoom, error) {
	if s.writer != nil {
		return queueValue(s, []any{in}, func(d *Store, p []any) (CoordRoom, error) {
			return d.CreateCoordGroup(p[0].(GroupInput))
		})
	}
	members := normalizeMembers(in.Members)
	if len(members) < 2 || !containsMember(members, in.Creator) {
		return CoordRoom{}, ErrInvalidGroup
	}
	key, err := randomRoomKey("group:")
	if err != nil {
		return CoordRoom{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return CoordRoom{}, err
	}
	defer tx.Rollback()
	at := membershipTime()
	if _, err := tx.Exec(`INSERT INTO coord_rooms(room_key,kind,label,created_at) VALUES(?,?,?,?)`, key, RoomGroup, strings.TrimSpace(in.Label), at); err != nil {
		return CoordRoom{}, err
	}
	if err := appendMembershipEvent(tx, key, in.Creator, "group_create", in.Creator, at); err != nil {
		return CoordRoom{}, err
	}
	for _, member := range members {
		if _, err := tx.Exec(`INSERT INTO coord_room_memberships(room_key,principal_id,joined_at,left_at,is_manager)
			VALUES(?,?,?,'',?)`, key, member, at, member == in.Creator); err != nil {
			return CoordRoom{}, err
		}
		if err := appendMembershipEvent(tx, key, member, "join", in.Creator, at); err != nil {
			return CoordRoom{}, err
		}
		if member == in.Creator {
			if err := appendMembershipEvent(tx, key, member, "manager_grant", in.Creator, at); err != nil {
				return CoordRoom{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return CoordRoom{}, err
	}
	return CoordRoom{Key: key, Kind: RoomGroup, Label: strings.TrimSpace(in.Label), Members: members, CreatedAt: at}, nil
}

func (s *Store) UpdateCoordGroup(in GroupUpdate) error {
	if s.writer != nil {
		return queueWrite(s, []any{in}, func(d *Store, p []any) error { return d.UpdateCoordGroup(p[0].(GroupUpdate)) })
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	kind, err := coordRoomKindTx(tx, in.RoomKey)
	if err != nil {
		return err
	}
	if kind != RoomGroup {
		return fmt.Errorf("%w: room is not a group", ErrInvalidGroup)
	}
	managerCount, err := activeManagerCount(tx, in.RoomKey)
	if err != nil {
		return err
	}
	if managerCount == 0 {
		return ErrLegacyGroupReadOnly
	}
	manager, err := activeManager(tx, in.RoomKey, in.Actor)
	if err != nil {
		return err
	}
	if !manager {
		return fmt.Errorf("%w: actor is not a room manager", ErrInvalidGroup)
	}
	if in.Label != nil {
		if _, err := tx.Exec(`UPDATE coord_rooms SET label=? WHERE room_key=? AND kind='group'`, strings.TrimSpace(*in.Label), in.RoomKey); err != nil {
			return err
		}
	}
	at := membershipTime()
	for _, principal := range normalizeMembers(append(in.Add, in.AddManagers...)) {
		isManager := containsMember(normalizeMembers(in.AddManagers), principal)
		result, err := tx.Exec(`INSERT INTO coord_room_memberships(room_key,principal_id,joined_at,left_at,is_manager)
			VALUES(?,?,?,'',?) ON CONFLICT DO NOTHING`, in.RoomKey, principal, at, isManager)
		if err != nil {
			return err
		}
		added, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if added > 0 {
			if err := appendMembershipEvent(tx, in.RoomKey, principal, "join", in.Actor, at); err != nil {
				return err
			}
			if isManager {
				if err := appendMembershipEvent(tx, in.RoomKey, principal, "manager_grant", in.Actor, at); err != nil {
					return err
				}
			}
		}
		if isManager {
			result, err := tx.Exec(`UPDATE coord_room_memberships SET is_manager=1 WHERE room_key=? AND principal_id=? AND left_at='' AND is_manager=0`, in.RoomKey, principal)
			if err != nil {
				return err
			}
			changed, _ := result.RowsAffected()
			if changed > 0 {
				if err := appendMembershipEvent(tx, in.RoomKey, principal, "manager_grant", in.Actor, at); err != nil {
					return err
				}
			}
		}
	}
	for _, principal := range normalizeMembers(in.RemoveManagers) {
		result, err := tx.Exec(`UPDATE coord_room_memberships SET is_manager=0 WHERE room_key=? AND principal_id=? AND left_at='' AND is_manager=1`, in.RoomKey, principal)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed > 0 {
			if err := appendMembershipEvent(tx, in.RoomKey, principal, "manager_revoke", in.Actor, at); err != nil {
				return err
			}
		}
	}
	for _, principal := range normalizeMembers(in.Remove) {
		wasManager, err := activeManager(tx, in.RoomKey, principal)
		if err != nil {
			return err
		}
		result, err := tx.Exec(`UPDATE coord_room_memberships SET left_at=? WHERE room_key=? AND principal_id=? AND left_at=''`, at, in.RoomKey, principal)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed > 0 {
			if err := appendMembershipEvent(tx, in.RoomKey, principal, "leave", in.Actor, at); err != nil {
				return err
			}
			if wasManager {
				if err := appendMembershipEvent(tx, in.RoomKey, principal, "manager_revoke", in.Actor, at); err != nil {
					return err
				}
			}
		}
	}
	managers, err := activeManagerCount(tx, in.RoomKey)
	if err != nil {
		return err
	}
	if managers == 0 {
		return ErrLastGroupManager
	}
	return tx.Commit()
}

func (s *Store) CoordRoomMembershipEvents(roomKey string) ([]RoomMembershipEvent, error) {
	if s.reader != nil {
		return s.reader.CoordRoomMembershipEvents(roomKey)
	}
	rows, err := s.db.Query(`SELECT id,room_key,principal_id,action,actor_id,created_at FROM coord_room_membership_events WHERE room_key=? ORDER BY id`, roomKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RoomMembershipEvent
	for rows.Next() {
		var event RoomMembershipEvent
		if err := rows.Scan(&event.ID, &event.RoomKey, &event.PrincipalID, &event.Action, &event.ActorID, &event.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *Store) CoordRoomsForPrincipal(principalID string) ([]CoordRoom, error) {
	if s.reader != nil {
		return s.reader.CoordRoomsForPrincipal(principalID)
	}
	rows, err := s.db.Query(`SELECT r.room_key,r.kind,r.label,r.created_at
		FROM coord_rooms r JOIN coord_room_memberships m ON m.room_key=r.room_key
		WHERE m.principal_id=? AND m.left_at='' ORDER BY r.created_at DESC,r.room_key`, principalID)
	if err != nil {
		return nil, err
	}
	var out []CoordRoom
	for rows.Next() {
		var r CoordRoom
		if err := rows.Scan(&r.Key, &r.Kind, &r.Label, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		members, err := s.CoordRoomMembers(out[i].Key)
		if err != nil {
			return nil, err
		}
		out[i].Members = members
	}
	return out, nil
}

func randomRoomKey(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}

func containsMember(members []string, member string) bool {
	for _, candidate := range members {
		if candidate == member {
			return true
		}
	}
	return false
}

func activeManager(tx *sql.Tx, roomKey, principalID string) (bool, error) {
	var count int
	err := tx.QueryRow(`SELECT COUNT(*) FROM coord_room_memberships
		WHERE room_key=? AND principal_id=? AND left_at='' AND is_manager=1`, roomKey, principalID).Scan(&count)
	return count > 0, err
}

func activeManagerCount(tx *sql.Tx, roomKey string) (int, error) {
	var count int
	err := tx.QueryRow(`SELECT COUNT(*) FROM coord_room_memberships WHERE room_key=? AND left_at='' AND is_manager=1`, roomKey).Scan(&count)
	return count, err
}

func coordRoomKindTx(tx *sql.Tx, roomKey string) (string, error) {
	var kind string
	err := tx.QueryRow(`SELECT kind FROM coord_rooms WHERE room_key=?`, roomKey).Scan(&kind)
	return kind, err
}

func appendMembershipEvent(tx *sql.Tx, roomKey, principalID, action, actorID, at string) error {
	_, err := tx.Exec(`INSERT INTO coord_room_membership_events(room_key,principal_id,action,actor_id,created_at) VALUES(?,?,?,?,?)`, roomKey, principalID, action, actorID, at)
	return err
}
