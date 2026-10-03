package store

import "encoding/json"

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
	rows, err := s.db.Query(`SELECT a.external_id, s.id, COALESCE(r.id,0), COALESCE(r.title,''), COALESCE(r.project,'')
		FROM coord_agents a
		JOIN sessions s ON s.external_id=a.session_id AND a.session_id!=''
			AND (a.principal_id='' OR (CASE WHEN s.account_id=0 THEN ? ELSE s.account_id END)=CAST(substr(a.principal_id,8) AS INTEGER))
		LEFT JOIN request_work w ON w.session_id=s.id AND w.state='active'
		LEFT JOIN requests r ON r.id=w.request_id AND r.state='open'
		WHERE a.external_id IN (SELECT value FROM json_each(?))
		ORDER BY s.id, w.id`, instanceOwnerID(s.db), string(ids))
	if err != nil {
		return nil, err
	}
	type hit struct {
		agent, title, project string
		session, request      int64
	}
	var hits []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.agent, &h.session, &h.request, &h.title, &h.project); err != nil {
			rows.Close()
			return nil, err
		}
		hits = append(hits, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Rows are read first, then checked: the role check asks the database itself.
	for _, h := range hits {
		w := out[h.agent]
		if w.SessionPublicID == "" {
			if sess, err := s.SessionByID(h.session); err == nil {
				w.SessionPublicID = pa.MetaView(sess).PublicID
			}
		}
		if h.request != 0 && pa.CanSeeProject(h.project, ResRequest) {
			w.RequestID, w.RequestTitle = h.request, h.title
		}
		out[h.agent] = w
	}
	return out, nil
}
