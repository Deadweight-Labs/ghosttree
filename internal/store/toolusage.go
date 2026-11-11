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
	// The text of a chunk is derived from its line (IDEA-25), so the count walks
	// the chunks that hold a tool call in chunk_index and reads their text by the
	// same rule as ReadSession. LIKE was case-insensitive for ASCII; so is this.
	needle := strings.ToLower(toolCallMarker + prefix)
	rows, err := s.db.Query(`SELECT se.project, c.session_id, se.harness, c.text, c.raw
		FROM chunk_index ci
		JOIN session_chunks c ON c.id = ci.chunk_id
		JOIN sessions se ON se.id = c.session_id
		WHERE ci.tool_calls > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type tally struct {
		calls    int
		sessions map[int64]struct{}
	}
	byProject := map[string]*tally{}
	for rows.Next() {
		var project, harness, text string
		var session int64
		var raw rawcodec.Value
		if err := rows.Scan(&project, &session, &harness, &text, &raw); err != nil {
			return nil, err
		}
		if text == "" {
			line, err := s.rawLine(raw)
			if err != nil {
				return nil, fmt.Errorf("session %d: %w", session, err)
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
	if err := rows.Err(); err != nil {
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
