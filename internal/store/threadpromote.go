package store

import (
	"fmt"
	"strconv"
	"strings"
)

// ThreadSource ist eine WÖRTLICHE Kopie des Beitrags, aus dem ein Thread
// hervorging — nicht ein Verweis darauf.
//
// Das ist die Bedingung aus Spec §6, und sie ist nicht verhandelbar: "Ein
// Link auf eine später gelöschte Nachricht reicht nicht." Chatverkehr hat
// eine kürzere Aufbewahrung als ein Thread; ein Thread, dessen Grundlage
// nach dreißig Tagen verschwindet, behauptet danach eine Herkunft, die
// niemand mehr prüfen kann. Die Bytes werden kopiert, nicht adressiert.
type ThreadSource struct {
	ThreadID   int64  `json:"thread_id,omitempty"`
	Kind       string `json:"source_kind"`
	ID         string `json:"source_id"`
	RoomKey    string `json:"room_key,omitempty"`
	Author     string `json:"author,omitempty"`
	AuthorKind string `json:"author_kind,omitempty"`
	Body       string `json:"body"`
	OriginalAt string `json:"original_at,omitempty"`
	CopiedAt   string `json:"copied_at,omitempty"`
}

// PromoteResult sagt, was die Übernahme getan hat — einschließlich dessen,
// was sie ABGELEHNT hat. Eine Übernahme, die stillschweigend weniger
// mitnimmt als verlangt, ist die gefährlichere Variante.
type PromoteResult struct {
	ThreadID   int64    `json:"thread_id"`
	Copied     int      `json:"copied"`
	Skipped    []string `json:"skipped,omitempty"`
	Restricted bool     `json:"restricted,omitempty"`
}

// PromoteMessagesToThread führt ausgewählte Beiträge als dauerhaftes Thema
// weiter.
//
// Zwei Zusagen, beide aus Spec §6:
//
// 1. DIE QUELLE WIRD GESICHERT, nicht verlinkt. Siehe ThreadSource.
//
// 2. DIE ÜBERNAHME VERGRÖSSERT DIE SICHTBARKEIT NICHT. Stammt ein Beitrag aus
// einem privaten Gespräch, erbt der Thread dessen Teilnehmerkreis und taucht
// für Unbeteiligte nirgends auf — nicht in der Suche, nicht in der Liste.
// Sonst wäre "als Thema weiterführen" der bequemste Weg, einen DM öffentlich
// zu machen, und niemand würde es merken.
//
// Beiträge aus verschiedenen privaten Räumen zu mischen wird abgelehnt: der
// entstehende Thread hätte zwei verschiedene berechtigte Kreise, und jede
// Auflösung davon gibt einer Seite etwas, das sie nicht hatte.
func (s *Store) PromoteMessagesToThread(roomKey string, messageIDs []int64, t Thread, by string) (PromoteResult, error) {
	if len(messageIDs) == 0 {
		return PromoteResult{}, fmt.Errorf("promoting needs at least one message")
	}
	if strings.TrimSpace(t.Title) == "" {
		return PromoteResult{}, fmt.Errorf("a promoted thread needs a title")
	}
	if s.writer != nil {
		return queueValue(s, []any{roomKey, messageIDs, t, by}, func(d *Store, p []any) (PromoteResult, error) {
			return d.PromoteMessagesToThread(p[0].(string), p[1].([]int64), p[2].(Thread), p[3].(string))
		})
	}

	private := strings.HasPrefix(roomKey, "direct:") || strings.HasPrefix(roomKey, "group:")
	var members []string
	if private {
		var err error
		members, err = s.CoordRoomMembers(roomKey)
		if err != nil {
			return PromoteResult{}, err
		}
		if len(members) == 0 {
			return PromoteResult{}, fmt.Errorf("cannot promote from an unknown private room")
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return PromoteResult{}, err
	}
	defer tx.Rollback()

	ts := now()
	res, err := tx.Exec(`INSERT INTO threads(project,title,question,state,archived,person,created_at,updated_at)
		VALUES(?,?,?,'open',0,?,?,?)`, t.Project, t.Title, t.Question, by, ts, ts)
	if err != nil {
		return PromoteResult{}, err
	}
	threadID, err := res.LastInsertId()
	if err != nil {
		return PromoteResult{}, err
	}

	out := PromoteResult{ThreadID: threadID, Restricted: private}
	for _, m := range members {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO thread_visibility(thread_id,member_external_id)
			VALUES(?,?)`, threadID, m); err != nil {
			return PromoteResult{}, err
		}
	}

	for _, id := range messageIDs {
		var body, sender, authorKind, createdAt, fromRoom string
		err := tx.QueryRow(`SELECT body,sender_external_id,author_kind,created_at,destination_id
			FROM coord_messages WHERE id=? AND destination_kind='room'`, id).
			Scan(&body, &sender, &authorKind, &createdAt, &fromRoom)
		if err != nil {
			out.Skipped = append(out.Skipped, strconv.FormatInt(id, 10)+" (not found)")
			continue
		}
		if fromRoom != roomKey {
			// Beiträge aus einem anderen Raum mitzunehmen hieße, dessen
			// Berechtigten etwas wegzunehmen oder dem neuen Kreis etwas zu
			// geben. Beides still.
			out.Skipped = append(out.Skipped, strconv.FormatInt(id, 10)+" (different room)")
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO thread_sources(
				thread_id,source_kind,source_id,room_key,author,author_kind,body,original_at,copied_at)
			VALUES(?,'coord_message',?,?,?,?,?,?,?)`,
			threadID, strconv.FormatInt(id, 10), roomKey, sender, authorKind, body, createdAt, ts); err != nil {
			return PromoteResult{}, err
		}
		out.Copied++
	}

	if out.Copied == 0 {
		return PromoteResult{}, fmt.Errorf("nothing was promoted: none of the messages belong to %s", roomKey)
	}
	return out, tx.Commit()
}

// ThreadSources liefert die gesicherten Originale. Sie sind der Beleg für die
// Herkunft eines Themas und überleben jede Chat-Aufbewahrungsfrist.
func (s *Store) ThreadSources(threadID int64) ([]ThreadSource, error) {
	if s.reader != nil {
		return s.reader.ThreadSources(threadID)
	}
	rows, err := s.db.Query(`SELECT thread_id,source_kind,source_id,room_key,author,author_kind,
			body,original_at,copied_at FROM thread_sources
		WHERE thread_id=? ORDER BY original_at, source_id`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThreadSource
	for rows.Next() {
		var src ThreadSource
		if err := rows.Scan(&src.ThreadID, &src.Kind, &src.ID, &src.RoomKey,
			&src.Author, &src.AuthorKind, &src.Body, &src.OriginalAt, &src.CopiedAt); err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// MayReadThread ist die Sichtbarkeitsgrenze eines Themas.
//
// Ein Thread ohne Eintrag in thread_visibility ist projektweit sichtbar —
// das ist der Normalfall und die richtige Voreinstellung für eine
// Untersuchung, die allen im Projekt nützt. Hat er einen, gilt genau dieser
// Kreis, geerbt aus dem privaten Gespräch, aus dem er hervorging.
func (s *Store) MayReadThread(threadID int64, agentExternalID string) (bool, error) {
	if s.reader != nil {
		return s.reader.MayReadThread(threadID, agentExternalID)
	}
	var restricted int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM thread_visibility WHERE thread_id=?`,
		threadID).Scan(&restricted); err != nil {
		return false, err
	}
	if restricted == 0 {
		return true, nil
	}
	var member int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM thread_visibility
		WHERE thread_id=? AND member_external_id=?`, threadID, agentExternalID).Scan(&member); err != nil {
		return false, err
	}
	return member > 0, nil
}
