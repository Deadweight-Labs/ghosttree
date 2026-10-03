package store

import (
	"encoding/json"
	"strings"
)

// AgentWork is what an agent is doing right now, as far as the viewer may see
// it: the open request its session works on and the page of that session.
type AgentWork struct {
	RequestID       int64
	RequestTitle    string
	SessionPublicID string
}

// AgentWorkFor maps agent external IDs to their active request work and
// session address. A request shows only where the viewer may see requests of
// its project, the session address only where the viewer may read the
// transcript; everything else stays empty (#2447).
func (s *Store) AgentWorkFor(pa *ProjectAccess, externalIDs []string) (map[string]AgentWork, error) {
	if s.reader != nil {
		return s.reader.AgentWorkFor(pa, externalIDs)
	}
	out := map[string]AgentWork{}
	if len(externalIDs) == 0 {
		return out, nil
	}
	ids, _ := json.Marshal(externalIDs)
	owner := instanceOwnerID(s.db)
	rows, err := s.db.Query(`SELECT `+prefixCols("s.", sessionCols)+`, a.external_id, COALESCE(r.id,0), COALESCE(r.title,''), COALESCE(r.project,'')
		FROM coord_agents a
		JOIN sessions s ON s.external_id=a.session_id AND a.session_id!=''
			AND (a.principal_id='' OR (CASE WHEN s.account_id=0 THEN ? ELSE s.account_id END)=CAST(substr(a.principal_id,8) AS INTEGER))
		LEFT JOIN request_work w ON w.session_id=s.id AND w.state='active'
		LEFT JOIN requests r ON r.id=w.request_id AND r.state='open'
		WHERE a.external_id IN (SELECT value FROM json_each(?))
		ORDER BY s.id, (w.role='primary') DESC, w.id`, owner, string(ids))
	if err != nil {
		return nil, err
	}
	type hit struct {
		agent, title, project string
		sess                  Session
		request               int64
	}
	var hits []hit
	for rows.Next() {
		var h hit
		if err := scanSession(rows, &h.sess, &h.agent, &h.request, &h.title, &h.project); err != nil {
			rows.Close()
			return nil, err
		}
		h.sess.AccountID = effectiveOwner(h.sess.AccountID, owner)
		hits = append(hits, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Rows are read first, then checked: the role check asks the database itself.
	// The first hit per agent wins: its primary work comes first.
	for _, h := range hits {
		w := out[h.agent]
		if w.SessionPublicID == "" {
			w.SessionPublicID = pa.MetaView(h.sess).PublicID
		}
		if w.RequestID == 0 && h.request != 0 && pa.CanSeeProject(h.project, ResRequest) {
			w.RequestID, w.RequestTitle = h.request, h.title
		}
		out[h.agent] = w
	}
	return out, nil
}

// prefixCols puts a table alias before every column of a comma list.
func prefixCols(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = alias + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}
