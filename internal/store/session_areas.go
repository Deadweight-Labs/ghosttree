package store

import (
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
)

// SessionAreas names the folders each of the given sessions worked in, heaviest
// first, at most n per session, for the second line of the session list. It reads
// path_activity, so it knows what the retention still holds (ActivityRetention);
// older sessions, and sessions without a path, are not in the map. Only pass
// sessions the viewer may read.
func (s *Store) SessionAreas(sessions []Session, n int) map[int64][]string {
	if s.reader != nil {
		return s.reader.SessionAreas(sessions, n)
	}
	out := map[int64][]string{}
	owner := instanceOwnerID(s.db)
	type key struct {
		ext     string
		account int64
	}
	var ids []any
	want := map[key][]int64{}
	for _, se := range sessions {
		if se.ExternalID == "" {
			continue
		}
		acc := se.AccountID
		if acc == 0 {
			acc = owner
		}
		k := key{se.ExternalID, acc}
		if len(want[k]) == 0 {
			ids = append(ids, se.ExternalID)
		}
		want[k] = append(want[k], se.ID)
	}
	if len(ids) == 0 {
		return out
	}
	from := time.Now().UTC().Add(-ActivityRetention).Format(time.RFC3339)
	q := `SELECT session_external_id, CASE WHEN account_id=0 THEN ? ELSE account_id END, path, writes, checkout
		FROM path_activity WHERE quality<>'unattributed' AND at>=? AND session_external_id IN (?` + strings.Repeat(",?", len(ids)-1) + `)
		ORDER BY at DESC, id DESC LIMIT 20000`
	rows, err := s.db.Query(q, append([]any{owner, from}, ids...)...)
	if err != nil {
		return out
	}
	defer rows.Close()
	touches := map[key][]originctx.Touch{}
	for rows.Next() {
		var k key
		var t originctx.Touch
		var writes int
		if err := rows.Scan(&k.ext, &k.account, &t.Path, &writes, &t.Checkout); err != nil {
			return out
		}
		if _, ok := want[k]; !ok {
			continue
		}
		t.Writes = writes != 0
		touches[k] = append(touches[k], t)
	}
	for k, ts := range touches {
		c := originctx.Build(k.ext, "", ts, nil)
		if c == nil {
			continue
		}
		var areas []string
		for _, a := range c.Areas {
			if a.Path == "." || a.Path == "" {
				continue
			}
			areas = append(areas, a.Path)
			if len(areas) == n {
				break
			}
		}
		for _, id := range want[k] {
			out[id] = areas
		}
	}
	return out
}
