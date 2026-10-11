package store

import "strings"

// Abfragen für die Startseite. Jede nimmt die Grenze des Betrachters in die
// Abfrage selbst (Projektliste), statt global zu holen und danach zu kappen:
// ein Fenster nach Anzahl oder Alter über die ganze Instanz würde sonst
// verborgene Zeilen mitzählen (#2447).

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
	// GlobalAccount und GlobalPersons: Einträge ohne Projekt gehören ihrem Autor,
	// dem Konto aus knowledge_authors oder, ohne diese Zeile, dem Label.
	GlobalAccount int64
	GlobalPersons []string
	Limit         int
	Offset        int
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
		own := `id IN (SELECT knowledge_id FROM knowledge_authors WHERE account_id=?)`
		args = append(args, w.GlobalAccount)
		if len(w.GlobalPersons) > 0 {
			own += ` OR (id NOT IN (SELECT knowledge_id FROM knowledge_authors) AND person IN (` + placeholders(len(w.GlobalPersons)) + `))`
			for _, p := range w.GlobalPersons {
				args = append(args, p)
			}
		}
		clause := `(project='' AND (` + own + `))`
		if len(w.Projects) > 0 {
			clause += ` OR project IN (` + placeholders(len(w.Projects)) + `)`
			for _, p := range w.Projects {
				args = append(args, p)
			}
		}
		switch {
		case w.UnclaimedAll:
			clause += ` OR (project!='' AND project NOT IN (SELECT remote FROM projects))`
		case w.UnclaimedAuthor != "":
			clause += ` OR (person=? AND project!='' AND project NOT IN (SELECT remote FROM projects))`
			args = append(args, w.UnclaimedAuthor)
		}
		where = append(where, `(`+clause+`)`)
	}
	args = append(args, w.Limit, max(w.Offset, 0))
	rows, err := s.db.Query(`SELECT `+knowledgeCols+` FROM knowledge WHERE `+strings.Join(where, ` AND `)+
		` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	return s.scanKnowledge(rows)
}
