package store

import (
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// InterruptedThread ist eine angefangene und nicht zu Ende gebrachte Arbeit an
// einem offenen Auftrag.
type InterruptedThread struct {
	RequestID    int64  `json:"request_id"`
	Type         string `json:"type"`
	Title        string `json:"title"`
	Priority     string `json:"priority,omitempty"`
	OpenCriteria int    `json:"open_criteria"`
	Handoff      string `json:"handoff"`
	Since        string `json:"since"`
	Derived      bool   `json:"derived"`
}

// InterruptedWork beantwortet, woran hier zuletzt gearbeitet wurde, ohne dass
// es zu Ende gebracht wurde.
//
// Zwei Wege führen hierher, und der zweite ist der Grund für diese Abfrage:
// entweder hat jemand die Arbeit ausdrücklich pausiert und eine Übergabe
// hinterlassen, oder die Sitzung ist einfach still geworden. Der zweite Fall
// ist der häufigere, weil niemand daran denkt, einen Zustand zu setzen — er
// wird deshalb aus last_seen_at abgeleitet, so wie session-lease eine alt
// gewordene Lease liest, statt auf eine Abmeldung zu warten.
//
// idleBefore ist der Zeitpunkt, vor dem eine Sitzung als still gilt.
// excludeSession ist die external_id der fragenden Sitzung: eine
// wiederaufgenommene Sitzung feuert session-start ein zweites Mal, und ihr
// eigener Faden ist nicht der, den sie sucht.
func (s *Store) InterruptedWork(ax scope.Axes, idleBefore, excludeSession string, limit int) ([]InterruptedThread, error) {
	if limit <= 0 {
		limit = 3
	}
	scopeWhere, args := ax.UnionWhere()
	// Der Scope steht in einer Unterabfrage, weil sessions und requests beide
	// eine Spalte project tragen und UnionWhere sie unqualifiziert nennt.
	query := `SELECT r.id, r.type, r.title, r.priority,
		(SELECT COUNT(*) FROM request_criteria c WHERE c.request_id=r.id AND c.state='open'),
		COALESCE(w.summary,''), w.state,
		MAX(CASE WHEN w.state='paused' THEN w.ended_at ELSE se.last_seen_at END) AS since
		FROM request_work w
		JOIN sessions se ON se.id = w.session_id
		JOIN requests r ON r.id = w.request_id
		WHERE w.request_id IN (SELECT id FROM requests WHERE state='open' AND ` + scopeWhere + `)
		  AND (w.state='paused' OR (w.state='active' AND se.last_seen_at < ?))
		  AND (? = '' OR se.external_id != ?)
		GROUP BY r.id
		ORDER BY since DESC LIMIT ?`
	args = append(args, idleBefore, excludeSession, excludeSession, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InterruptedThread{}
	for rows.Next() {
		var t InterruptedThread
		var state string
		if err := rows.Scan(&t.RequestID, &t.Type, &t.Title, &t.Priority, &t.OpenCriteria, &t.Handoff, &state, &t.Since); err != nil {
			return nil, err
		}
		t.Derived = state == "active"
		out = append(out, t)
	}
	return out, rows.Err()
}
