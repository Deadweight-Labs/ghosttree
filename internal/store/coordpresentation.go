package store

import (
	"database/sql"
	"fmt"
	"strings"
)

type CoordReplyPreview struct {
	MessageID int64
	Sequence  int64
	Author    string
	Body      string
	Missing   bool
}

type CoordDeliverySummary struct {
	Stored, Fetched, Injected, Acked int64
}

type CoordMessagePresentation struct {
	Message     CoordMessage
	AuthorLabel string
	Reply       *CoordReplyPreview
	ReplyCount  int64
	Mentions    []string
	Refs        []CoordRef
	Delivery    CoordDeliverySummary
}

func (a CoordAccess) MessagePresentationWindow(kind, id string, window MessageWindow) (MessagePage, []CoordMessagePresentation, error) {
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	tx, err := reader.db.Begin()
	if err != nil {
		return MessagePage{}, nil, err
	}
	defer tx.Rollback()
	actor, err := a.actorTx(tx)
	if err != nil {
		return MessagePage{}, nil, err
	}
	if err := a.canReadTx(tx, actor, kind, id); err != nil {
		return MessagePage{}, nil, err
	}
	page, err := coordMessageWindowTx(tx, kind, id, window)
	if err != nil {
		return MessagePage{}, nil, err
	}
	presented, err := coordMessagePresentationsTx(tx, kind, id, page.Messages)
	if err != nil {
		return MessagePage{}, nil, err
	}
	if a.guestViewsRoomTx(tx, messageRoomKeyTx(tx, kind, id)) {
		for i := range presented {
			raw, err := rawMentionsTx(tx, presented[i].Message.ID)
			if err != nil {
				return MessagePage{}, nil, err
			}
			if raw != nil {
				presented[i].Mentions = raw
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return MessagePage{}, nil, err
	}
	return page, presented, nil
}

func coordMessagePresentationsTx(tx *sql.Tx, kind, destinationID string, messages []CoordMessage) ([]CoordMessagePresentation, error) {
	out := make([]CoordMessagePresentation, len(messages))
	if len(messages) == 0 {
		return out, nil
	}
	index := make(map[int64]int, len(messages))
	ids := make([]any, 0, len(messages)+2)
	for i, message := range messages {
		out[i] = CoordMessagePresentation{Message: message, AuthorLabel: message.SenderExternalID}
		index[message.ID] = i
		ids = append(ids, message.ID)
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(messages)), ",")
	args := append([]any{kind, destinationID}, ids...)

	rows, err := tx.Query(`SELECT m.id,m.author_kind,COALESCE(a.display_name,''),COALESCE(p.name,'')
		FROM coord_messages m
		LEFT JOIN coord_agents a ON a.external_id=m.sender_external_id
		LEFT JOIN persons p ON ('person:' || p.id)=m.author_principal_id
		WHERE m.destination_kind=? AND m.destination_id=? AND m.id IN (`+marks+`)`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var messageID int64
		var authorKind, agentLabel, personLabel string
		if err := rows.Scan(&messageID, &authorKind, &agentLabel, &personLabel); err != nil {
			rows.Close()
			return nil, err
		}
		if authorKind == AuthorHuman && personLabel != "" {
			out[index[messageID]].AuthorLabel = personLabel
		} else if agentLabel != "" {
			out[index[messageID]].AuthorLabel = agentLabel
		} else if personLabel != "" {
			out[index[messageID]].AuthorLabel = personLabel
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(`SELECT m.id,mm.mentioned_external_id FROM coord_messages m
		JOIN coord_message_mentions mm ON mm.message_id=m.id
		WHERE m.destination_kind=? AND m.destination_id=? AND m.id IN (`+marks+`)
		ORDER BY m.id,mm.mentioned_external_id`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var mid int64
		var mention string
		if err := rows.Scan(&mid, &mention); err != nil {
			rows.Close()
			return nil, err
		}
		out[index[mid]].Mentions = append(out[index[mid]].Mentions, mention)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(`SELECT m.id,r.ref_kind,r.ref_id,r.ref_revision FROM coord_messages m
		JOIN coord_message_refs r ON r.message_id=m.id
		WHERE m.destination_kind=? AND m.destination_id=? AND m.id IN (`+marks+`)
		ORDER BY m.id,r.ref_kind,r.ref_id,r.ref_revision`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var mid int64
		var ref CoordRef
		if err := rows.Scan(&mid, &ref.Kind, &ref.ID, &ref.Revision); err != nil {
			rows.Close()
			return nil, err
		}
		ref.MutableHead = ref.Revision == ""
		out[index[mid]].Refs = append(out[index[mid]].Refs, ref)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(`SELECT child.id,child.reply_to,COALESCE(parent.sequence,0),COALESCE(parent.sender_external_id,''),COALESCE(parent.author_kind,''),COALESCE(pa.display_name,''),COALESCE(pp.name,''),COALESCE(parent.body,'')
		FROM coord_messages child LEFT JOIN coord_messages parent
		ON parent.id=child.reply_to AND parent.destination_kind=child.destination_kind AND parent.destination_id=child.destination_id
		LEFT JOIN coord_agents pa ON pa.external_id=parent.sender_external_id
		LEFT JOIN persons pp ON ('person:' || pp.id)=parent.author_principal_id
		WHERE child.destination_kind=? AND child.destination_id=? AND child.id IN (`+marks+`) AND child.reply_to IS NOT NULL`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var mid, parentID, seq int64
		var author, authorKind, agentLabel, personLabel, body string
		if err := rows.Scan(&mid, &parentID, &seq, &author, &authorKind, &agentLabel, &personLabel, &body); err != nil {
			rows.Close()
			return nil, err
		}
		if authorKind == AuthorHuman && personLabel != "" {
			author = personLabel
		} else if agentLabel != "" {
			author = agentLabel
		} else if personLabel != "" {
			author = personLabel
		}
		out[index[mid]].Reply = &CoordReplyPreview{MessageID: parentID, Sequence: seq, Author: author, Body: body, Missing: seq == 0}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(`SELECT reply_to,COUNT(*) FROM coord_messages
		WHERE destination_kind=? AND destination_id=? AND reply_to IN (`+marks+`)
		GROUP BY reply_to`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var parentID, count int64
		if err := rows.Scan(&parentID, &count); err != nil {
			rows.Close()
			return nil, err
		}
		if position, ok := index[parentID]; ok {
			out[position].ReplyCount = count
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(`SELECT m.id,d.state,COUNT(*) FROM coord_messages m JOIN coord_deliveries d ON d.message_id=m.id
		WHERE m.destination_kind=? AND m.destination_id=? AND m.id IN (`+marks+`) GROUP BY m.id,d.state`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var mid, count int64
		var state string
		if err := rows.Scan(&mid, &state, &count); err != nil {
			rows.Close()
			return nil, err
		}
		summary := &out[index[mid]].Delivery
		switch state {
		case DeliveryStored:
			summary.Stored = count
		case DeliveryFetched:
			summary.Fetched = count
		case DeliveryInjected:
			summary.Injected = count
		case DeliveryAcked:
			summary.Acked = count
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("project message presentation: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return out, nil
}
