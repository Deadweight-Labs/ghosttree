package store

import (
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/rawcodec"
	"github.com/Deadweight-Labs/ghosttree/internal/transcript"
)

// SessionActivity is what a run did with its tools, counted from its transcript
// (REQ-688): the ghosttree view of a session shows it as one line, so a run that
// wrote nothing into ghosttree is not an empty page. Only counts, never content.
type SessionActivity struct {
	// Total is every tool call of the run; the kinds below add up to it when the
	// run was short enough to read (Detailed), otherwise only Total is known.
	Total int
	// Commands, Reads, Edits, Searches, Ghost and Other are the calls by kind of tool.
	Commands, Reads, Edits, Searches, Ghost, Other int
	Detailed                                       bool
	// At is the time of the last call, or empty.
	At string
}

// toolGroup puts a tool name of either harness into one of the kinds.
func toolGroup(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "ghosttree"):
		return "ghost"
	case strings.Contains(n, "bash"), strings.Contains(n, "shell"), strings.Contains(n, "exec"):
		return "commands"
	case strings.Contains(n, "patch"), strings.Contains(n, "edit"), strings.Contains(n, "write"), strings.Contains(n, "notebook"):
		return "edits"
	case strings.Contains(n, "read"), strings.Contains(n, "view"):
		return "reads"
	case strings.Contains(n, "grep"), strings.Contains(n, "glob"), strings.Contains(n, "search"), strings.Contains(n, "find"), strings.Contains(n, "fetch"):
		return "searches"
	}
	return "other"
}

// SessionActivity counts the tool calls of a session. Only pass a session the
// viewer may read.
func (s *Store) SessionActivity(sess Session) SessionActivity {
	if s.reader != nil {
		return s.reader.SessionActivity(sess)
	}
	var out SessionActivity
	if sess.ID <= 0 {
		return out
	}
	if err := s.db.QueryRowContext(s.ctx(), `SELECT COALESCE(SUM(tool_calls),0) FROM chunk_index WHERE session_id=?`, sess.ID).Scan(&out.Total); err != nil || out.Total == 0 {
		return SessionActivity{}
	}
	rows, err := s.db.QueryContext(s.ctx(), `SELECT c.seq, c.raw FROM session_chunks c JOIN chunk_index i ON i.session_id=c.session_id AND i.seq=c.seq
		WHERE c.session_id=? AND i.tool_calls>0 ORDER BY c.seq LIMIT ?`, sess.ID, maxStatChunks+1)
	if err != nil {
		return out
	}
	defer rows.Close()
	n := 0
	var counted SessionActivity
	for rows.Next() {
		var seq int
		var v rawcodec.Value
		if err := rows.Scan(&seq, &v); err != nil {
			return out
		}
		if n++; n > maxStatChunks {
			return out
		}
		raw, err := s.rawLine(v)
		if err != nil {
			return out
		}
		for _, b := range transcript.Parse(sess.Harness, seq, raw).Blocks {
			if b.Kind != transcript.KindTool {
				continue
			}
			counted.Total++
			switch toolGroup(b.Tool) {
			case "ghost":
				counted.Ghost++
			case "commands":
				counted.Commands++
			case "edits":
				counted.Edits++
			case "reads":
				counted.Reads++
			case "searches":
				counted.Searches++
			default:
				counted.Other++
			}
			if b.Time > counted.At {
				counted.At = b.Time
			}
		}
	}
	if rows.Err() != nil || counted.Total == 0 {
		return out
	}
	counted.Detailed = true
	return counted
}
