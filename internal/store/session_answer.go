package store

import (
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/rawcodec"
	"github.com/Deadweight-Labs/ghosttree/internal/transcript"
)

// answerScan is how many of the last assistant lines SessionAnswer looks through for
// one that carries text: the lines before the answer are tool calls, and after it
// come bookkeeping lines (title, last prompt).
const answerScan = 40

// SessionAnswer is the last thing the agent said in words: the closing message of a
// run. The ghosttree view of a session shows it, so a run that only used tools and
// then reported back is not just "10 tool calls" (REQ-690, N10). Empty when the
// session has no assistant text. Only pass a session the viewer may read.
func (s *Store) SessionAnswer(sess Session) (text, at string) {
	if s.reader != nil {
		return s.reader.SessionAnswer(sess)
	}
	if sess.ID <= 0 {
		return "", ""
	}
	rows, err := s.db.QueryContext(s.ctx(), `SELECT seq, raw FROM session_chunks WHERE session_id=? ORDER BY seq DESC LIMIT ?`, sess.ID, answerScan)
	if err != nil {
		return "", ""
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var v rawcodec.Value
		if err := rows.Scan(&seq, &v); err != nil {
			return "", ""
		}
		raw, err := s.rawLine(v)
		if err != nil {
			return "", ""
		}
		blocks := transcript.Parse(sess.Harness, seq, raw).Blocks
		for i := len(blocks) - 1; i >= 0; i-- {
			if b := blocks[i]; b.Kind == transcript.KindAssistant && strings.TrimSpace(b.Text) != "" {
				return strings.TrimSpace(b.Text), b.Time
			}
		}
	}
	return "", ""
}
