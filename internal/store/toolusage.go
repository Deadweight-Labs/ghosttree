package store

import "strings"

// ToolCallRow is one project's use of a tool family.
type ToolCallRow struct {
	Project  string `json:"project"`
	Calls    int    `json:"calls"`
	Sessions int    `json:"sessions"`
}

// toolCallMarker is the prefix the collectors write in front of an archived
// call. Matching on it is what separates a call from a mention: a session
// discussing context_search contains the name, a session using it contains this
// marker followed by the name.
const toolCallMarker = "[tool call: "

// ToolCallsPerProject counts archived calls to a family of tools, per project.
//
// prefix names the family — "mcp__ghosttree__" for this tool's own MCP surface,
// "" for every tool. Only chunks collected after tool calls started being
// archived can be counted; earlier sessions record what a tool returned but not
// which one was called, so their absence here is a gap in the archive rather
// than evidence that nobody called anything.
func (s *Store) ToolCallsPerProject(prefix string) ([]ToolCallRow, error) {
	pattern := "%" + escapeLike(toolCallMarker+prefix) + "%"
	rows, err := s.db.Query(`SELECT se.project, COUNT(*), COUNT(DISTINCT c.session_id)
		FROM session_chunks c JOIN sessions se ON se.id = c.session_id
		WHERE c.text LIKE ? ESCAPE '\'
		GROUP BY se.project
		ORDER BY COUNT(*) DESC, se.project`, pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ToolCallRow{}
	for rows.Next() {
		var r ToolCallRow
		if err := rows.Scan(&r.Project, &r.Calls, &r.Sessions); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// escapeLike neutralises the wildcards in a literal. A tool name carrying an
// underscore — every MCP tool does — would otherwise match any character there
// and quietly count another tool's calls as this one's.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
