package store

import "encoding/json"

// RunState is what the hooks of an agent say about a session, apart from how
// recently its transcript was uploaded. The transcript keeps arriving for a
// minute or two after a run is over (the collector sends what is left), so the
// age of the last upload alone says "running" for a session that has ended.
type RunState struct {
	// Ended: SessionEnd closed the session and nothing has opened it again.
	Ended bool
	// Idle: the agent finished its turn (Stop hook) and waits for the next
	// prompt; it is alive but does not work.
	Idle bool
}

// SessionRunStates maps session ids (sessions.id) to what the hooks say. A
// session that no agent registered for, one that runs without hooks, has no
// entry: for it the age of the last upload is all there is. The principal check
// is the one of AgentWorkFor, so a session is only matched with its own agent.
// Several agents of one session (subagents) count as ended when all of them are,
// and as idle when every one that is not ended is idle.
func (s *Store) SessionRunStates(sessionIDs []int64) (map[int64]RunState, error) {
	if s.reader != nil {
		return s.reader.SessionRunStates(sessionIDs)
	}
	out := map[int64]RunState{}
	if len(sessionIDs) == 0 {
		return out, nil
	}
	ids, _ := json.Marshal(sessionIDs)
	rows, err := s.db.Query(`SELECT s.id, a.ended_at<>'', a.turn_tracked=1 AND a.turn_open=0
		FROM coord_agents a
		JOIN sessions s ON s.external_id=a.session_id AND a.session_id!=''
			AND (a.principal_id='' OR (CASE WHEN s.account_id=0 THEN ? ELSE s.account_id END)=CAST(substr(a.principal_id,8) AS INTEGER))
		WHERE s.id IN (SELECT value FROM json_each(?))`, instanceOwnerID(s.db), string(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type tally struct{ agents, ended, idle int }
	seen := map[int64]*tally{}
	for rows.Next() {
		var id int64
		var ended, idle bool
		if err := rows.Scan(&id, &ended, &idle); err != nil {
			return nil, err
		}
		t := seen[id]
		if t == nil {
			t = &tally{}
			seen[id] = t
		}
		t.agents++
		switch {
		case ended:
			t.ended++
		case idle:
			t.idle++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for id, t := range seen {
		out[id] = RunState{Ended: t.ended == t.agents, Idle: t.ended < t.agents && t.ended+t.idle == t.agents}
	}
	return out, nil
}
