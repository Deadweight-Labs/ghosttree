package store

import (
	"strings"
	"time"
)

// Abfragen für die Startseite. Jede nimmt die Grenze des Betrachters in die
// Abfrage selbst (Projektliste), statt global zu holen und danach zu kappen:
// ein Fenster nach Anzahl oder Alter über die ganze Instanz würde sonst
// verborgene Zeilen mitzählen (#2447).

// PendingDevice ist ein offener Geräte-Login, wie ihn der Owner zur Freigabe
// sieht. Der User-Code gehört bewusst nicht dazu: wer ihn tippt, beweist, am
// Terminal zu sitzen.
type PendingDevice struct {
	Machine, Remote string
	StartedAt       time.Time
}

// Pending listet die offenen Geräte-Abläufe, älteste zuerst. Join-Paarungen
// gehören ihrer Join-Sitzung und erscheinen nicht.
func (d *DeviceFlows) Pending() []PendingDevice {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.purge(d.now())
	var out []PendingDevice
	for _, f := range d.byDevice {
		if f.join || f.state != "pending" {
			continue
		}
		out = append(out, PendingDevice{Machine: f.machine, Remote: f.remote, StartedAt: f.created})
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].StartedAt.Before(out[j-1].StartedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// CriteriaProgress ist der Fortschritt eines Requests: erledigte (met oder
// waived) von allen Kriterien.
type CriteriaProgress struct{ Done, Total int }

// CriteriaProgress zählt je Request der übergebenen IDs. Die Aufrufer geben nur
// IDs, die der Betrachter schon sehen darf.
func (s *Store) CriteriaProgress(ids []int64) (map[int64]CriteriaProgress, error) {
	if s.reader != nil {
		return s.reader.CriteriaProgress(ids)
	}
	out := make(map[int64]CriteriaProgress, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(`SELECT request_id, COUNT(*), COALESCE(SUM(state<>'open'),0)
		FROM request_criteria WHERE request_id IN (`+placeholders(len(ids))+`) GROUP BY request_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var p CriteriaProgress
		if err := rows.Scan(&id, &p.Total, &p.Done); err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, rows.Err()
}

// KnowledgeWindow beschreibt eine Wissensabfrage für die Startseite.
type KnowledgeWindow struct {
	// Pending: Einträge, die auf Prüfung warten. Sonst: freigegebene (aktiv,
	// trusted oder verified), seit Since angelegt.
	Pending bool
	Since   string
	// Restrict begrenzt auf Einträge ohne Projekt und auf Projects; Unclaimed*
	// wie bei der Request-Suche.
	Restrict        bool
	Projects        []string
	UnclaimedAuthor string
	UnclaimedAll    bool
	Limit           int
}

// KnowledgeWindow liefert neueste zuerst. Die Projektgrenze steckt in der
// Abfrage; die feine Prüfung je Eintrag bleibt beim Aufrufer.
func (s *Store) KnowledgeWindow(w KnowledgeWindow) ([]Knowledge, error) {
	if s.reader != nil {
		return s.reader.KnowledgeWindow(w)
	}
	if w.Limit <= 0 || w.Limit > 200 {
		w.Limit = 200
	}
	var where []string
	var args []any
	if w.Pending {
		where = append(where, `((status='active' AND confidence IN ('quarantined','staged')) OR status='stale')`)
	} else {
		where = append(where, `status='active' AND confidence IN ('trusted','verified')`)
	}
	if w.Since != "" {
		where = append(where, `created_at>=?`)
		args = append(args, w.Since)
	}
	if w.Restrict {
		clause := `project=''`
		if len(w.Projects) > 0 {
			clause += ` OR project IN (` + placeholders(len(w.Projects)) + `)`
			for _, p := range w.Projects {
				args = append(args, p)
			}
		}
		switch {
		case w.UnclaimedAll:
			clause += ` OR project NOT IN (SELECT remote FROM projects)`
		case w.UnclaimedAuthor != "":
			clause += ` OR (person=? AND project NOT IN (SELECT remote FROM projects))`
			args = append(args, w.UnclaimedAuthor)
		}
		where = append(where, `(`+clause+`)`)
	}
	args = append(args, w.Limit)
	rows, err := s.db.Query(`SELECT `+knowledgeCols+` FROM knowledge WHERE `+strings.Join(where, ` AND `)+
		` ORDER BY created_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return s.scanKnowledge(rows)
}
