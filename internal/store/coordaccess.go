package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
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

func (a CoordAccess) requireRoomAccess(roomKey string) (CoordRoom, error) {
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
		if a.publicOnly {
			return room, nil
		}
		if a.AgentExternalID != "" {
			ok, err := a.activeRoomMember(room.Key, actor)
			if err != nil {
				return CoordRoom{}, err
			}
			if !ok {
				return CoordRoom{}, ErrCoordForbidden
			}
		} else {
			ok, err := a.principalOwnsPublicMembership(room.Key)
			if err != nil {
				return CoordRoom{}, err
			}
			if !ok {
				return CoordRoom{}, ErrCoordForbidden
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
	return a.requireRoomAccess(roomKey)
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
	actor, err := a.actor()
	if err != nil {
		return nil, err
	}
	rooms, err := a.Store.CoordRoomsForPrincipal(actor)
	if err != nil {
		return nil, err
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
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var room CoordRoom
			if err := rows.Scan(&room.Key, &room.Kind, &room.Label, &room.CreatedAt); err != nil {
				return nil, err
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
			return nil, err
		}
	}
	return rooms, nil
}

func (a CoordAccess) requireThreadAccess(threadID int64) (Thread, error) {
	actor, err := a.actor()
	if err != nil {
		return Thread{}, err
	}
	t, err := a.Store.ThreadByID(threadID)
	if err != nil {
		return Thread{}, ErrCoordNotFound
	}
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	var restricted int
	if err := reader.db.QueryRow(`SELECT COUNT(*) FROM thread_visibility WHERE thread_id=?`, threadID).Scan(&restricted); err != nil {
		return Thread{}, err
	}
	if restricted > 0 {
		if a.publicOnly {
			return Thread{}, ErrCoordNotFound
		}
		var member int
		if err := reader.db.QueryRow(`SELECT COUNT(*) FROM thread_visibility
			WHERE thread_id=? AND member_external_id=?`, threadID, actor).Scan(&member); err != nil {
			return Thread{}, err
		}
		if member == 0 {
			return Thread{}, ErrCoordNotFound
		}
		return t, nil
	}
	if !a.publicOnly {
		if _, err := a.requireRoomAccess(RoomKeyForProject(t.Project)); err != nil {
			return Thread{}, err
		}
	}
	return t, nil
}

func (a CoordAccess) canRead(kind, id string) error {
	switch kind {
	case DestinationRoom:
		_, err := a.requireRoomAccess(id)
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
	if err := a.canRead(kind, id); err != nil {
		return nil, err
	}
	return a.Store.CoordMessagesSince(kind, id, afterID, limit)
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
	switch kind {
	case DestinationRoom:
		return a.requireRoomAccessTx(tx, actor, id)
	case DestinationDiscussion:
		threadID, err := strconv.ParseInt(id, 10, 64)
		if err != nil || threadID <= 0 {
			return ErrCoordNotFound
		}
		var project string
		if err := tx.QueryRow(`SELECT project FROM threads WHERE id=?`, threadID).Scan(&project); errors.Is(err, sql.ErrNoRows) {
			return ErrCoordNotFound
		} else if err != nil {
			return err
		}
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
			return nil
		}
		if a.publicOnly {
			return nil
		}
		return a.requireRoomAccessTx(tx, actor, RoomKeyForProject(project))
	default:
		return ErrCoordNotFound
	}
}

func (a CoordAccess) requireRoomAccessTx(tx *sql.Tx, actor, roomKey string) error {
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
		if member == 0 {
			return ErrCoordForbidden
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
	rooms, err := a.Rooms()
	if err != nil {
		return nil, err
	}
	return a.Store.projectRoomSummaries(actor, a.Principal.ID, a.AgentExternalID, rooms)
}

func (a CoordAccess) Send(message CoordMessage) (int64, error) {
	if a.Store != nil && a.Store.writer != nil {
		return queueValue(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, message}, func(d *Store, p []any) (int64, error) {
			return queuedCoordAccess(d, p).Send(p[3].(CoordMessage))
		})
	}
	actor, err := a.mutationActor()
	if err != nil {
		return 0, err
	}
	if err := a.canRead(message.DestinationKind, message.DestinationID); err != nil {
		return 0, err
	}
	if message.DestinationKind == DestinationRoom && len(message.Mentions) > 0 {
		if err := a.ValidateRoomParticipants(message.DestinationID, message.Mentions); err != nil {
			return 0, err
		}
	}
	if message.ReplyTo != 0 {
		reader := a.Store
		if reader.reader != nil {
			reader = reader.reader
		}
		var count int
		if err := reader.db.QueryRow(`SELECT COUNT(*) FROM coord_messages
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
	if a.AgentExternalID == "" {
		message.AuthorKind = AuthorHuman
	} else {
		message.AuthorKind = AuthorAgent
	}
	id, err := a.Store.AppendCoordMessage(message)
	if err != nil {
		return 0, err
	}
	if message.DestinationKind == DestinationDiscussion {
		threadID, _ := strconv.ParseInt(message.DestinationID, 10, 64)
		if err := a.Store.TouchThread(threadID); err != nil {
			return 0, err
		}
	}
	return id, nil
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
	if _, err := a.requireRoomAccess(roomKey); err != nil {
		return nil, err
	}
	return a.Store.CoordPeers(roomKey, since)
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
	if _, _, err := a.messageTarget(messageID); err != nil {
		return nil, err
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

func (a CoordAccess) SearchThreads(project, query string, includeArchived bool, limit int) ([]Thread, error) {
	actor, err := a.actor()
	if err != nil {
		return nil, err
	}
	if !a.publicOnly {
		if _, err := a.requireRoomAccess(RoomKeyForProject(project)); err != nil {
			return nil, err
		}
	}
	return a.Store.SearchThreadsFor(project, query, actor, includeArchived, limit)
}

func (a CoordAccess) ThreadsForObject(kind, id string) ([]Thread, error) {
	actor, err := a.actor()
	if err != nil {
		return nil, err
	}
	threads, err := a.Store.ThreadsForObjectAs(kind, id, actor)
	if err != nil {
		return nil, err
	}
	visible := make([]Thread, 0, len(threads))
	for _, thread := range threads {
		if _, err := a.requireThreadAccess(thread.ID); err == nil {
			visible = append(visible, thread)
		} else if !errors.Is(err, ErrCoordNotFound) && !errors.Is(err, ErrCoordForbidden) {
			return nil, err
		}
	}
	return visible, nil
}

func (a CoordAccess) CreateThread(thread Thread) (int64, error) {
	if a.Store != nil && a.Store.writer != nil {
		return queueValue(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, thread}, func(d *Store, p []any) (int64, error) {
			return queuedCoordAccess(d, p).CreateThread(p[3].(Thread))
		})
	}
	if _, err := a.mutationActor(); err != nil {
		return 0, err
	}
	if _, err := a.requireRoomAccess(RoomKeyForProject(thread.Project)); err != nil {
		return 0, err
	}
	thread.Person = a.Principal.Label
	thread.AuthorPrincipalID = a.Principal.ID
	return a.Store.CreateThread(thread)
}

func (a CoordAccess) PromoteMessagesToThread(roomKey string, messageIDs []int64, thread Thread) (PromoteResult, error) {
	if a.Store != nil && a.Store.writer != nil {
		return queueValue(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, roomKey, messageIDs, thread}, func(d *Store, p []any) (PromoteResult, error) {
			return queuedCoordAccess(d, p).PromoteMessagesToThread(p[3].(string), p[4].([]int64), p[5].(Thread))
		})
	}
	if _, err := a.mutationActor(); err != nil {
		return PromoteResult{}, err
	}
	room, err := a.requireRoomAccess(roomKey)
	if err != nil {
		return PromoteResult{}, err
	}
	if room.Kind != RoomProject || room.Key != RoomKeyForProject(thread.Project) {
		return PromoteResult{}, ErrCoordForbidden
	}
	thread.Person = a.Principal.Label
	thread.AuthorPrincipalID = a.Principal.ID
	return a.Store.PromoteMessagesToThread(roomKey, messageIDs, thread, a.Principal.Label)
}

func (a CoordAccess) requireThreadMutation(threadID int64) (Thread, error) {
	if a.publicOnly {
		return Thread{}, ErrCoordForbidden
	}
	thread, err := a.requireThreadAccess(threadID)
	if err != nil {
		return Thread{}, err
	}
	if thread.AuthorPrincipalID == "" || thread.AuthorPrincipalID != a.Principal.ID {
		return Thread{}, ErrCoordForbidden
	}
	return thread, nil
}

func (a CoordAccess) ThreadLinks(threadID int64) ([]ThreadLink, error) {
	if _, err := a.requireThreadAccess(threadID); err != nil {
		return nil, err
	}
	return a.Store.ThreadLinks(threadID)
}

func (a CoordAccess) LinkThread(link ThreadLink) error {
	if a.Store != nil && a.Store.writer != nil {
		return queueWrite(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, link}, func(d *Store, p []any) error {
			return queuedCoordAccess(d, p).LinkThread(p[3].(ThreadLink))
		})
	}
	if _, err := a.requireThreadMutation(link.ThreadID); err != nil {
		return err
	}
	return a.Store.LinkThread(link)
}

func (a CoordAccess) SetThreadState(threadID int64, state string) error {
	if a.Store != nil && a.Store.writer != nil {
		return queueWrite(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, threadID, state}, func(d *Store, p []any) error {
			return queuedCoordAccess(d, p).SetThreadState(p[3].(int64), p[4].(string))
		})
	}
	if _, err := a.requireThreadMutation(threadID); err != nil {
		return err
	}
	return a.Store.SetThreadState(threadID, state)
}

func (a CoordAccess) SetThreadArchived(threadID int64, archived bool) error {
	if a.Store != nil && a.Store.writer != nil {
		return queueWrite(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, threadID, archived}, func(d *Store, p []any) error {
			return queuedCoordAccess(d, p).SetThreadArchived(p[3].(int64), p[4].(bool))
		})
	}
	if _, err := a.requireThreadMutation(threadID); err != nil {
		return err
	}
	return a.Store.SetThreadArchived(threadID, archived)
}

func (a CoordAccess) TouchThread(threadID int64) error {
	if a.Store != nil && a.Store.writer != nil {
		return queueWrite(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, threadID}, func(d *Store, p []any) error {
			return queuedCoordAccess(d, p).TouchThread(p[3].(int64))
		})
	}
	if _, err := a.requireThreadMutation(threadID); err != nil {
		return err
	}
	return a.Store.TouchThread(threadID)
}

func (a CoordAccess) ThreadSummary(threadID int64) (ThreadSummary, bool, error) {
	if _, err := a.requireThreadAccess(threadID); err != nil {
		return ThreadSummary{}, false, err
	}
	return a.Store.LatestThreadSummary(threadID)
}

func (a CoordAccess) PutThreadSummary(summary ThreadSummary) (int, error) {
	if a.Store != nil && a.Store.writer != nil {
		return queueValue(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, summary}, func(d *Store, p []any) (int, error) {
			return queuedCoordAccess(d, p).PutThreadSummary(p[3].(ThreadSummary))
		})
	}
	if _, err := a.requireThreadMutation(summary.ThreadID); err != nil {
		return 0, err
	}
	summary.Person = a.Principal.Label
	return a.Store.PutThreadSummary(summary)
}

func (a CoordAccess) ThreadOutcomes(threadID int64) ([]ThreadOutcome, error) {
	if _, err := a.requireThreadAccess(threadID); err != nil {
		return nil, err
	}
	return a.Store.ThreadOutcomes(threadID)
}

func (a CoordAccess) PutThreadOutcome(outcome ThreadOutcome) error {
	if a.Store != nil && a.Store.writer != nil {
		return queueWrite(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, outcome}, func(d *Store, p []any) error {
			return queuedCoordAccess(d, p).PutThreadOutcome(p[3].(ThreadOutcome))
		})
	}
	if _, err := a.requireThreadMutation(outcome.ThreadID); err != nil {
		return err
	}
	return a.Store.PutThreadOutcome(outcome)
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
		if err := a.canReadTx(tx, actor, DestinationRoom, room); err != nil {
			if errors.Is(err, ErrCoordNotFound) || errors.Is(err, ErrCoordForbidden) {
				continue
			}
			return nil, err
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
			var label string
			if queryErr := tx.QueryRow(`SELECT display_name FROM coord_agents WHERE external_id=?`, id).Scan(&label); queryErr == nil && strings.TrimSpace(label) != "" {
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
	if err := a.canReadTx(tx, actor, DestinationRoom, roomKey); err != nil {
		return err
	}
	if err := a.validateRoomParticipantsTx(tx, actor, roomKey, principals); err != nil {
		return err
	}
	return tx.Commit()
}

func (a CoordAccess) validateRoomParticipantsTx(tx *sql.Tx, actor, roomKey string, principals []string) error {
	allowed := map[string]bool{actor: true, a.Principal.ID: true}
	rows, err := tx.Query(`SELECT principal_id FROM coord_room_memberships WHERE room_key=? AND left_at=''`, roomKey)
	if err != nil {
		return err
	}
	var participantIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		allowed[id] = true
		participantIDs = append(participantIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range participantIDs {
		var owner string
		qerr := tx.QueryRow(`SELECT COALESCE(principal_id,'') FROM coord_agents WHERE external_id=?`, id).Scan(&owner)
		if qerr != nil && !errors.Is(qerr, sql.ErrNoRows) {
			return qerr
		}
		if owner != "" {
			allowed[owner] = true
		}
	}
	for _, id := range normalizeMembers(principals) {
		if !allowed[id] {
			return fmt.Errorf("%w: %s", ErrCoordUnknownRecipient, id)
		}
	}
	return nil
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
	if err := a.validateRoomParticipantsTx(tx, actor, in.RoomKey, in.Mentions); err != nil {
		return 0, err
	}
	message := CoordMessage{DestinationKind: DestinationRoom, DestinationID: in.RoomKey, SenderExternalID: actor, AuthorPrincipalID: a.Principal.ID, ClientID: in.ClientID, Body: in.Body, Intent: IntentStanding, ExpiresAt: strings.TrimSpace(in.ExpiresAt), Mentions: in.Mentions}
	if a.AgentExternalID == "" {
		message.AuthorKind = AuthorHuman
	} else {
		message.AuthorKind = AuthorAgent
	}
	id, err := appendCoordMessageTx(tx, message)
	if err != nil {
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
