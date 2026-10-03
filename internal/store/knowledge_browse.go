package store

import "strings"

// BrowseKnowledge liefert die lebenden Einträge (aktiv oder veraltet, nicht
// abgelöst, nicht verworfen), neueste zuerst, höchstens limit. project, typ und
// confidence engen ein, wenn sie gesetzt sind. Die feine Prüfung je Eintrag
// bleibt beim Aufrufer.
func (s *Store) BrowseKnowledge(project, typ, confidence string, limit int) ([]Knowledge, error) {
	if s.reader != nil {
		return s.reader.BrowseKnowledge(project, typ, confidence, limit)
	}
	if limit <= 0 || limit > 400 {
		limit = 400
	}
	where := []string{`status IN ('active','stale')`}
	var args []any
	for _, f := range []struct{ col, val string }{{"project", project}, {"type", typ}, {"confidence", confidence}} {
		if f.val != "" {
			where = append(where, f.col+`=?`)
			args = append(args, f.val)
		}
	}
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT `+knowledgeCols+` FROM knowledge WHERE `+strings.Join(where, ` AND `)+
		` ORDER BY created_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return s.scanKnowledge(rows)
}
