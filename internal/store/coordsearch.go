package store

import (
	"strings"
)

// MessageHit ist eine Raum-Nachricht als Treffer der Suche (REQ-600): so viel,
// dass man sie wiederfindet und verlinken kann, nicht der ganze Beitrag.
type MessageHit struct {
	ID         int64  `json:"id"`
	RoomKey    string `json:"room_key"`
	RoomLabel  string `json:"room_label,omitempty"`
	Sequence   int64  `json:"sequence"`
	Sender     string `json:"sender"`
	AuthorKind string `json:"author_kind,omitempty"`
	Body       string `json:"body"`
	CreatedAt  string `json:"created_at"`
	// Project ist das Projekt des Raums, leer bei einer Direkt- oder Gruppen-Unterhaltung.
	Project string `json:"project,omitempty"`
}

// maxMessageHitBody kürzt den Text eines Treffers.
const maxMessageHitBody = 600

// SearchMessages sucht in den Raum-Nachrichten, die dieser Leser auch sonst
// lesen darf: in den Räumen, die Rooms() ihm zeigt (Projektraum nach Rolle oder
// Mitgliedschaft, Direkt- und Gruppenräume nur als Mitglied). Ein Raum, den er
// nicht lesen darf, kommt gar nicht erst in die Abfrage; die Menge steht vor dem
// Limit fest, ein verborgener Treffer verdrängt also keinen sichtbaren. Maschinen-
// räume bleiben draußen: sie gehören zu keinem Projekt, und ihre Namen sind Rechnernamen.
// project beschränkt den Projektraum; Direkt- und Gruppenräume stehen daneben.
// Neueste zuerst, höchstens limit (begrenzt auf 500).
func (a CoordAccess) SearchMessages(project, text string, limit int) ([]MessageHit, error) {
	limit = min(max(limit, 1), 500)
	rooms, err := a.Rooms()
	if err != nil {
		return nil, err
	}
	byKey := map[string]CoordRoom{}
	var keys []any
	for _, r := range rooms {
		switch r.Kind {
		case RoomProject:
			if project == "" || r.Key != RoomKeyForProject(project) {
				continue
			}
		case RoomDirect, RoomGroup:
		default:
			continue
		}
		if _, dup := byKey[r.Key]; dup {
			continue
		}
		byKey[r.Key] = r
		keys = append(keys, r.Key)
	}
	if len(keys) == 0 {
		return []MessageHit{}, nil
	}
	reader := a.Store
	if reader.reader != nil {
		reader = reader.reader
	}
	where := []string{"destination_kind='room'", "destination_id IN (" + placeholders(len(keys)) + ")"}
	args := keys
	for _, w := range strings.Fields(text) {
		w = strings.Trim(w, `"`)
		if w == "" {
			continue
		}
		where = append(where, `body LIKE ? ESCAPE '\'`)
		args = append(args, "%"+likeEscape(w)+"%")
	}
	args = append(args, limit)
	rows, err := reader.db.Query(`SELECT id,destination_id,sequence,sender_external_id,author_kind,body,created_at
		FROM coord_messages WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MessageHit{}
	for rows.Next() {
		var h MessageHit
		if err := rows.Scan(&h.ID, &h.RoomKey, &h.Sequence, &h.Sender, &h.AuthorKind, &h.Body, &h.CreatedAt); err != nil {
			return nil, err
		}
		room := byKey[h.RoomKey]
		h.RoomLabel = room.Label
		if room.Kind == RoomProject {
			h.Project = strings.TrimPrefix(room.Key, "project:")
		}
		if r := []rune(h.Body); len(r) > maxMessageHitBody {
			h.Body = string(r[:maxMessageHitBody]) + "…"
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
