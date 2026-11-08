package store

import (
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
)

// thread_events (REQ-597, Tabelle in store.go): was an einem Thread geschah, mit
// dem Git-Stand des Schreibers. Ein Thread selbst ist veränderlich (Zustand,
// Archiv), und seine Zeile hat keine Revisionen; die Ereignisse sind der Teil,
// der festgeschrieben werden kann. Dieselbe Form wie request_activity: eine
// Zeile je Schreibvorgang, anchor_id und origin_id kommen über anchoredTables
// dazu. Nachrichten gehören nicht hierher: sie sind Raum-Nachrichten
// (coord_messages) und tragen ihren Anker dort.

// Arten von Thread-Ereignissen.
const (
	ThreadEventOpened  = "thread.opened"
	ThreadEventState   = "thread.state"
	ThreadEventOutcome = "thread.outcome"
	// ThreadEventLinked: ein Objekt wurde mit dem Thread verknüpft.
	ThreadEventLinked = "thread.linked"
	// ThreadEventSummary: eine neue Fassung der Zusammenfassung.
	ThreadEventSummary = "thread.summary"
)

// ThreadEvent ist ein Schreibvorgang an einem Thread.
type ThreadEvent struct {
	ID        int64  `json:"id"`
	ThreadID  int64  `json:"thread_id"`
	Kind      string `json:"kind"`
	Person    string `json:"person,omitempty"`
	Data      string `json:"data,omitempty"`
	CreatedAt string `json:"created_at"`
	// GitAnchor ist der Git-Stand des Schreibers; nil ohne.
	GitAnchor *gitanchor.Anchor `json:"git_anchor,omitempty"`
	// Via ist "web" für ein Ereignis aus der Weboberfläche.
	Via string `json:"via,omitempty"`
}

// insertThreadEventTx hält ein Ereignis mit seinem Anker fest, in der
// Transaktion des Schreibvorgangs selbst: ein Ereignis ohne Zustandsänderung oder
// umgekehrt soll es nicht geben.
func insertThreadEventTx(tx txExec, threadID int64, kind, person, data string, anchor *gitanchor.Anchor) error {
	anchorID, originID, err := insertWriteMetaTx(tx, anchor, nil)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO thread_events(thread_id,kind,person,data,created_at,anchor_id,origin_id) VALUES(?,?,?,?,?,?,?)`,
		threadID, kind, person, data, now(), anchorID, originID)
	return err
}

// ThreadEvents liefert die Ereignisse eines Threads, älteste zuerst. Wer den
// Thread lesen darf, prüft der Aufrufer (CoordAccess).
func (s *Store) ThreadEvents(threadID int64) ([]ThreadEvent, error) {
	if s.reader != nil {
		return s.reader.ThreadEvents(threadID)
	}
	rows, err := s.db.Query(`SELECT id,thread_id,kind,person,data,created_at FROM thread_events WHERE thread_id=? ORDER BY id`, threadID)
	if err != nil {
		return nil, err
	}
	var out []ThreadEvent
	var ids []int64
	for rows.Next() {
		var e ThreadEvent
		if err := rows.Scan(&e.ID, &e.ThreadID, &e.Kind, &e.Person, &e.Data, &e.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, e)
		ids = append(ids, e.ID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	anchors, err := s.anchorsFor("thread_events", ids)
	if err != nil {
		return nil, err
	}
	via, err := s.viaFor("thread_events", ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].GitAnchor = anchors[out[i].ID]
		out[i].Via = via[out[i].ID]
	}
	return out, nil
}

func outcomeEventData(kind, refID, state string) string {
	return kind + " " + strconv.Quote(refID) + " -> " + state
}
