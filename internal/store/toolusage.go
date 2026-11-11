package store

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/rawcodec"
)

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
	if s.reader != nil {
		return s.reader.ToolCallsPerProject(prefix)
	}
	// The text of a chunk is derived from its line (IDEA-25), so the count reads
	// it by the same rule as ReadSession. LIKE was case-insensitive for ASCII; so
	// is this.
	//
	// Two sets of chunks are looked at: those that chunk_index says hold a tool
	// call, and those that chunk_index has no row for yet (the backfill has not
	// reached them, so nothing is known about them and they are derived like the
	// rest). Deriving parses, redacts and caps the line, which is what the stored
	// text went through, so the count is the one the stored text gave. Its cost
	// is bounded twice: only chunks with a call or without an index row are read
	// at all, and when the prefix is a plain tool name the line itself must hold
	// it before it is derived (the name is in the line as written; the marker is
	// only the parser's way to show it).
	needle := strings.ToLower(toolCallMarker + prefix)
	lineNeedle := ""
	if plainToolPrefix(prefix) {
		lineNeedle = strings.ToLower(prefix)
	}
	type tally struct {
		calls    int
		sessions map[int64]struct{}
	}
	byProject := map[string]*tally{}
	count := func(query string) error {
		rows, err := s.db.Query(query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var project, harness, text string
			var session, chunk int64
			var raw rawcodec.Value
			if err := rows.Scan(&project, &session, &chunk, &harness, &text, &raw); err != nil {
				return err
			}
			if text == "" {
				line, err := s.rawLine(raw)
				if err != nil {
					return fmt.Errorf("session %d: %w", session, err)
				}
				if lineNeedle != "" && !strings.Contains(strings.ToLower(line), lineNeedle) {
					continue
				}
				text = chunkTextOf(harness, "", line)
			}
			if !strings.Contains(strings.ToLower(text), needle) {
				continue
			}
			t := byProject[project]
			if t == nil {
				t = &tally{sessions: map[int64]struct{}{}}
				byProject[project] = t
			}
			t.calls++
			t.sessions[session] = struct{}{}
		}
		return rows.Err()
	}
	if err := count(`SELECT se.project, c.session_id, c.id, se.harness, c.text, c.raw
		FROM chunk_index ci
		JOIN session_chunks c ON c.id = ci.chunk_id
		JOIN sessions se ON se.id = c.session_id
		WHERE ci.tool_calls > 0`); err != nil {
		return nil, err
	}
	if err := count(`SELECT se.project, c.session_id, c.id, se.harness, c.text, c.raw
		FROM session_chunks c
		JOIN sessions se ON se.id = c.session_id
		WHERE NOT EXISTS (SELECT 1 FROM chunk_index ci WHERE ci.chunk_id = c.id)`); err != nil {
		return nil, err
	}
	out := make([]ToolCallRow, 0, len(byProject))
	for project, t := range byProject {
		out = append(out, ToolCallRow{Project: project, Calls: t.calls, Sessions: len(t.sessions)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Project < out[j].Project
	})
	return out, nil
}

// plainToolPrefix says whether a tool-name prefix is made of characters that a
// JSON line carries unescaped, so that a line which shows the tool holds the
// prefix as written.
func plainToolPrefix(prefix string) bool {
	if prefix == "" {
		return false
	}
	for _, r := range prefix {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.', r == ':':
		default:
			return false
		}
	}
	return true
}
