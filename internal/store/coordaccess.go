package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var (
	ErrCoordNotFound  = errors.New("coordination target not found")
	ErrCoordForbidden = errors.New("coordination target forbidden")
)

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
	if _, err := a.requireRoomAccess(roomKey); err != nil {
		return err
	}
	if actor != principalID {
		return fmt.Errorf("%w: use manager-authorized group update", ErrCoordForbidden)
	}
	return a.Store.LeaveCoordRoom(roomKey, principalID, actor)
}
