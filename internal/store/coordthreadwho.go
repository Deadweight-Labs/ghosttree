package store

import (
	"strings"
	"time"
)

// ThreadAuthor is someone who took part in one thread: a person once, an agent
// once per session. Who is merely a member of the room, or an agent that was
// registered there without ever writing in the thread, is not one.
type ThreadAuthor struct {
	// Kind is AuthorHuman or AuthorAgent.
	Kind string
	// PrincipalID is the person (person:N); for an agent, the owner the message
	// was written for, which may be empty.
	PrincipalID string
	// SenderID is the external ID of the agent session; empty for a person.
	SenderID string
	// Messages counts what they wrote in the thread, the message the thread was
	// started from included.
	Messages int
	// LastAt is the time of their last message ("" for the creator who wrote only the guiding question).
	LastAt string
}

// ThreadAuthors lists everyone who wrote in the thread, in the order of their
// first message: the message of the room the thread was started from counts, and
// so does the person who created the thread with a guiding question. The whole
// thread is read, not the window of the page.
func (a CoordAccess) ThreadAuthors(threadID int64) ([]ThreadAuthor, error) {
	t, err := a.requireThreadAccess(threadID)
	if err != nil {
		return nil, err
	}
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	rows, err := reader.db.Query(`SELECT author_kind,
			CASE author_kind WHEN 'human' THEN author_principal_id ELSE '' END,
			CASE author_kind WHEN 'agent' THEN sender_external_id ELSE '' END,
			MAX(CASE author_kind WHEN 'agent' THEN author_principal_id ELSE '' END),
			COUNT(*), MAX(created_at), MIN(id)
		FROM coord_messages
		WHERE author_kind IN ('human','agent')
		  AND ((destination_kind='discussion' AND destination_id=?)
		    OR id=COALESCE((SELECT anchor_message_id FROM thread_homes WHERE thread_id=?),0))
		GROUP BY author_kind,
			CASE author_kind WHEN 'human' THEN author_principal_id ELSE '' END,
			CASE author_kind WHEN 'agent' THEN sender_external_id ELSE '' END
		ORDER BY MIN(id)`, ThreadDestinationID(threadID), threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThreadAuthor
	seenPerson := map[string]bool{}
	for rows.Next() {
		var au ThreadAuthor
		var principal, owner string
		var first int64
		if err := rows.Scan(&au.Kind, &principal, &au.SenderID, &owner, &au.Messages, &au.LastAt, &first); err != nil {
			return nil, err
		}
		au.PrincipalID = principal
		if au.Kind == AuthorAgent {
			au.PrincipalID = owner
		} else {
			seenPerson[principal] = true
		}
		out = append(out, au)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Whoever created the thread wrote its guiding question.
	if creator := strings.TrimSpace(t.AuthorPrincipalID); creator != "" && strings.HasPrefix(creator, "person:") && !seenPerson[creator] {
		out = append([]ThreadAuthor{{Kind: AuthorHuman, PrincipalID: creator}}, out...)
	}
	return out, nil
}

// ThreadWaitCycles finds who waits on each other because of the open questions,
// approvals and blockers of this thread alone: the edges are the open items of
// the messages written in the thread, between agents. A cycle is reported, not
// resolved (see coordwait.go). The members are agent session IDs.
func (a CoordAccess) ThreadWaitCycles(threadID int64) ([]WaitCycle, error) {
	if _, err := a.requireThreadAccess(threadID); err != nil {
		return nil, err
	}
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	ref := time.Now().UTC()
	refText := ref.Format(time.RFC3339)
	rows, err := reader.db.Query(`SELECT m.sender_external_id,a.recipient_principal_id,a.reason,a.created_at,COALESCE(m.expires_at,'')
		FROM coord_attention a JOIN coord_messages m ON m.id=a.message_id
		WHERE a.state='open' AND a.reason IN (?,?,?) AND m.destination_kind='discussion' AND m.destination_id=?
		  AND a.recipient_principal_id<>m.sender_external_id
		ORDER BY a.created_at DESC, a.id DESC LIMIT 2000`,
		AttentionQuestion, AttentionApproval, AttentionBlocker, ThreadDestinationID(threadID))
	if err != nil {
		return nil, err
	}
	var open []waitRow
	candidates := map[string]bool{}
	for rows.Next() {
		var r waitRow
		if rows.Scan(&r.Sender, &r.Recipient, &r.Reason, &r.At, &r.Expires) == nil && !expiredAt(r.Expires, refText) {
			open = append(open, r)
			if _, perr := parsePersonPrincipalID(r.Recipient); perr != nil {
				candidates[r.Recipient] = true
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	isAgent := map[string]bool{}
	for id := range candidates {
		var found string
		if reader.db.QueryRow(`SELECT external_id FROM coord_agents WHERE external_id=?`, id).Scan(&found) == nil {
			isAgent[id] = true
		}
	}
	var edges []WaitEdge
	for _, r := range open {
		if _, perr := parsePersonPrincipalID(r.Recipient); perr == nil {
			r.Kind = "user"
		} else if isAgent[r.Recipient] {
			r.Kind = "peer"
		}
		if e, ok := r.edge(ref); ok {
			edges = append(edges, e)
		}
	}
	return DetectWaitCycles(edges), nil
}
