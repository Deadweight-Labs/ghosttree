package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	IntentQuestion = "question"
	IntentApproval = "approval"
	IntentBlocker  = "blocker"
	IntentHandoff  = "handoff"
	IntentAck      = "ack"

	AttentionQuestion = IntentQuestion
	AttentionApproval = IntentApproval
	AttentionBlocker  = IntentBlocker
	AttentionHandoff  = IntentHandoff

	AttentionOpen      = "open"
	AttentionResolved  = "resolved"
	AttentionDismissed = "dismissed"
	AttentionExpired   = "expired"

	AttentionActionAnswer   = "answer"
	AttentionActionApprove  = "approve"
	AttentionActionReject   = "reject"
	AttentionActionAccept   = "accept"
	AttentionActionResolve  = "resolve"
	AttentionActionDismiss  = "dismiss"
	AttentionActionWithdraw = "withdraw"
)

var (
	ErrAttentionClosed            = errors.New("coordination attention item is closed")
	ErrInvalidAttentionAction     = errors.New("invalid coordination attention action")
	ErrAttentionRecipientRequired = errors.New("coordination attention requires a recipient")
)

type AttentionItem struct {
	ID              int64  `json:"id"`
	RecipientID     string `json:"recipient_id"`
	DestinationKind string `json:"destination_kind"`
	DestinationID   string `json:"destination_id"`
	Reason          string `json:"reason"`
	State           string `json:"state"`
	CreatedAt       string `json:"created_at"`
	ResolvedAt      string `json:"resolved_at,omitempty"`
	MessageID       int64  `json:"message_id"`
	Sequence        int64  `json:"sequence,omitempty"`
	Body            string `json:"body,omitempty"`
	AuthorID        string `json:"author_id,omitempty"`
	SenderID        string `json:"sender_id,omitempty"`
	HomeRoomKey     string `json:"home_room_key,omitempty"`
	IsRecipient     bool   `json:"is_recipient,omitempty"`
	CanWithdraw     bool   `json:"can_withdraw,omitempty"`
}

func attentionReasonForIntent(intent string) (string, bool) {
	switch strings.TrimSpace(intent) {
	case IntentQuestion, IntentApproval, IntentBlocker, IntentHandoff:
		return strings.TrimSpace(intent), true
	default:
		return "", false
	}
}

func (a CoordAccess) Attention() ([]AttentionItem, error) {
	if a.Store == nil || strings.TrimSpace(a.Principal.ID) == "" || a.publicOnly {
		return nil, ErrCoordForbidden
	}
	if a.Store.writer != nil {
		return queueValue(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly}, func(d *Store, p []any) ([]AttentionItem, error) {
			return queuedCoordAccess(d, p).Attention()
		})
	}
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
	rows, err := tx.Query(`SELECT attention.id,attention.recipient_principal_id,
		m.destination_kind,m.destination_id,attention.reason,attention.state,
		attention.created_at,attention.resolved_at,attention.message_id,m.sequence,
		m.body,m.author_principal_id,m.sender_external_id,COALESCE(m.expires_at,'')
		FROM coord_attention attention JOIN coord_messages m ON m.id=attention.message_id
		WHERE attention.recipient_principal_id=? OR m.sender_external_id=? OR m.author_principal_id=?
		ORDER BY attention.created_at DESC, attention.message_id DESC, attention.id DESC`, actor, actor, a.Principal.ID)
	if err != nil {
		return nil, err
	}
	var candidates []struct {
		item      AttentionItem
		sender    string
		expiresAt string
	}
	for rows.Next() {
		var candidate struct {
			item      AttentionItem
			sender    string
			expiresAt string
		}
		if err := rows.Scan(&candidate.item.ID, &candidate.item.RecipientID,
			&candidate.item.DestinationKind, &candidate.item.DestinationID,
			&candidate.item.Reason, &candidate.item.State, &candidate.item.CreatedAt,
			&candidate.item.ResolvedAt, &candidate.item.MessageID, &candidate.item.Sequence,
			&candidate.item.Body, &candidate.item.AuthorID, &candidate.sender, &candidate.expiresAt); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	nowTS := now()
	out := make([]AttentionItem, 0, len(candidates))
	for _, candidate := range candidates {
		if err := a.canReadTx(tx, actor, candidate.item.DestinationKind, candidate.item.DestinationID); err != nil {
			if errors.Is(err, ErrCoordNotFound) || errors.Is(err, ErrCoordForbidden) {
				continue
			}
			return nil, err
		}
		if candidate.item.DestinationKind == DestinationDiscussion {
			threadID, _ := parseThreadDestination(candidate.item.DestinationID)
			if home, found, err := threadHomeTx(tx, threadID); err != nil {
				return nil, err
			} else if found {
				candidate.item.HomeRoomKey = home.RoomKey
			}
		} else {
			candidate.item.HomeRoomKey = candidate.item.DestinationID
		}
		candidate.item.SenderID = candidate.sender
		candidate.item.IsRecipient = candidate.item.RecipientID == actor
		// Ausgehende Einträge zeigen, wer eine Erwähnung wirklich erreicht hat,
		// und damit, wer im Raum ist. Ein Gast sieht sie nicht; was er erwähnt hat,
		// zeigt ihm die Nachricht selbst.
		if !candidate.item.IsRecipient && a.guestViewForMessageTx(tx, candidate.item.DestinationKind, candidate.item.DestinationID) {
			continue
		}
		candidate.item.CanWithdraw = candidate.item.AuthorID == a.Principal.ID || candidate.sender == actor
		// Gastsicht (nur eigene Empfängerzeilen kommen hier an): das Konto
		// hinter einem fremden Agenten bleibt verborgen.
		if candidate.item.AuthorID != a.Principal.ID && a.guestViewForMessageTx(tx, candidate.item.DestinationKind, candidate.item.DestinationID) {
			candidate.item.AuthorID = ""
		}
		if candidate.item.State == AttentionOpen && expiredAt(candidate.expiresAt, nowTS) {
			candidate.item.State = AttentionExpired
			if _, err := tx.Exec(`UPDATE coord_attention SET state='expired' WHERE id=? AND state='open'`, candidate.item.ID); err != nil {
				return nil, err
			}
		}
		out = append(out, candidate.item)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func parseThreadDestination(id string) (int64, error) {
	threadID, err := strconv.ParseInt(id, 10, 64)
	if err != nil || threadID <= 0 {
		return 0, ErrCoordNotFound
	}
	return threadID, nil
}

func (a CoordAccess) ResolveAttention(id int64, action string) error {
	if a.Store != nil && a.Store.writer != nil {
		return queueWrite(a.Store, []any{a.Principal, a.AgentExternalID, a.publicOnly, id, action}, func(d *Store, p []any) error {
			return queuedCoordAccess(d, p).ResolveAttention(p[3].(int64), p[4].(string))
		})
	}
	if a.Store == nil || strings.TrimSpace(a.Principal.ID) == "" || a.publicOnly {
		return ErrCoordForbidden
	}
	action = strings.TrimSpace(action)
	if !validAttentionAction(action) {
		return fmt.Errorf("%w: %s", ErrInvalidAttentionAction, action)
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
	var recipient, reason, state, kind, destination, sender, authorPrincipal, expiresAt string
	err = tx.QueryRow(`SELECT attention.recipient_principal_id,attention.reason,attention.state,
		m.destination_kind,m.destination_id,m.sender_external_id,m.author_principal_id,COALESCE(m.expires_at,'')
		FROM coord_attention attention JOIN coord_messages m ON m.id=attention.message_id
		WHERE attention.id=?`, id).Scan(&recipient, &reason, &state, &kind, &destination, &sender, &authorPrincipal, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCoordNotFound
	}
	if err != nil {
		return err
	}
	if err := a.canReadTx(tx, actor, kind, destination); err != nil {
		return err
	}
	isAuthor := actor == sender || a.Principal.ID == authorPrincipal
	if action == AttentionActionWithdraw {
		// Ein Gast sieht keine ausgehenden Einträge; Zurückziehen würde sonst an
		// erratenen Ids verraten, welche Erwähnung jemanden erreicht hat.
		if !isAuthor || (actor != recipient && a.guestViewForMessageTx(tx, kind, destination)) {
			return ErrCoordNotFound
		}
	} else if actor != recipient {
		return ErrCoordNotFound
	}
	if state == AttentionOpen && expiredAt(expiresAt, now()) {
		if _, err := tx.Exec(`UPDATE coord_attention SET state='expired' WHERE id=? AND state='open'`, id); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return ErrAttentionClosed
	}
	if state != AttentionOpen {
		return ErrAttentionClosed
	}
	if !actionAllowedForReason(reason, action) {
		return fmt.Errorf("%w: %s for %s", ErrInvalidAttentionAction, action, reason)
	}
	next := AttentionResolved
	if action == AttentionActionDismiss || action == AttentionActionWithdraw {
		next = AttentionDismissed
	}
	if _, err := tx.Exec(`UPDATE coord_attention SET state=?,resolved_at=? WHERE id=? AND state='open'`, next, now(), id); err != nil {
		return err
	}
	if reason != AttentionHandoff {
		if err := reconcileWaitCyclesTx(tx, messageRoomKeyTx(tx, kind, destination), time.Now().UTC()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func validAttentionAction(action string) bool {
	switch action {
	case AttentionActionAnswer, AttentionActionApprove, AttentionActionReject,
		AttentionActionAccept, AttentionActionResolve, AttentionActionDismiss, AttentionActionWithdraw:
		return true
	default:
		return false
	}
}

func actionAllowedForReason(reason, action string) bool {
	if action == AttentionActionDismiss || action == AttentionActionWithdraw || action == AttentionActionResolve {
		return true
	}
	switch reason {
	case AttentionQuestion:
		return action == AttentionActionAnswer
	case AttentionApproval:
		return action == AttentionActionApprove || action == AttentionActionReject
	case AttentionHandoff:
		return action == AttentionActionAccept
	case AttentionBlocker:
		return false
	default:
		return false
	}
}
