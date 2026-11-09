package store

import (
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/transcript"
)

// What a session left behind (UI review P13.1): the requirements it met, the
// knowledge, tasks and room questions it wrote, the files it changed. The author
// stamp (entry_actors, REQ-620) is the one place that says which session wrote an
// entry, so every list here starts from it: by the session row id, or by the
// external id for an entry written before the collector uploaded the session
// (the same rule as resolveActorSessions, one account only). Nothing is filtered
// for the viewer here; each row carries what the access check needs (project,
// scope, assignee).

// SessionCriterion is a requirement the session closed (met or waived).
type SessionCriterion struct {
	At          string
	CriterionID int64
	Number      int
	Text        string
	State       string // met | waived
	RequestID   int64
	Project     string
	Commit      string // the newest commit proof, "" without one
}

// SessionKnowledgeEntry is an entry of knowledge the session wrote.
type SessionKnowledgeEntry struct {
	At string
	Knowledge
}

// SessionTask is a task of a person the session opened.
type SessionTask struct {
	At        string
	ID        int64
	Title     string
	Project   string
	RequestID int64
	// Assignee is the name of the person the task waits for, "" for nobody.
	Assignee         string
	AssigneeAccount  int64
	CreatedByAccount int64
}

// SessionQuestion is a question the session put to a room.
type SessionQuestion struct {
	At      string
	Message int64
	Body    string
	// RoomKey is "project:<remote>" for the room of a repository; Label is the
	// name of any other room.
	RoomKey, Label string
}

// SessionRequest is an issue the session opened.
type SessionRequest struct {
	At      string
	ID      int64
	Title   string
	Project string
}

// SessionResults is everything above for one session, each list newest first.
type SessionResults struct {
	Criteria  []SessionCriterion
	Knowledge []SessionKnowledgeEntry
	Tasks     []SessionTask
	Questions []SessionQuestion
	Requests  []SessionRequest
}

// stampOf is the condition "this entry was written by that session" on the alias
// ea of entry_actors, with its arguments.
func stampOf(sess Session, owner int64) (string, []any) {
	cond := `(ea.session_id=?`
	args := []any{sess.ID}
	if sess.ExternalID != "" {
		cond += ` OR (ea.session_id IS NULL AND ea.session_ref=? AND ea.account_id=?)`
		args = append(args, sess.ExternalID, effectiveOwner(sess.AccountID, owner))
	}
	return cond + `)`, args
}

// SessionResults reads what the session wrote, from the author stamps. A viewer
// sees only what the caller lets through; see the access checks in the web layer.
func (s *Store) SessionResults(sess Session) (SessionResults, error) {
	if s.reader != nil {
		return s.reader.SessionResults(sess)
	}
	var out SessionResults
	if sess.ID <= 0 {
		return out, nil
	}
	cond, args := stampOf(sess, instanceOwnerID(s.db))

	rows, err := s.db.QueryContext(s.ctx(), `SELECT ea.at, c.id, c.number, c.description, c.state, c.request_id, r.project,
		COALESCE((SELECT ev.ref FROM request_evidence ev WHERE ev.criterion_id=c.id AND ev.kind='commit' ORDER BY ev.id DESC LIMIT 1),'')
		FROM entry_actors ea JOIN request_criteria c ON c.id=ea.entry_id JOIN requests r ON r.id=c.request_id
		WHERE ea.kind='criterion' AND c.state IN ('met','waived') AND `+cond+` ORDER BY ea.at DESC, c.id DESC`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var c SessionCriterion
		if err := rows.Scan(&c.At, &c.CriterionID, &c.Number, &c.Text, &c.State, &c.RequestID, &c.Project, &c.Commit); err != nil {
			rows.Close()
			return out, err
		}
		out.Criteria = append(out.Criteria, c)
	}
	if err := closeRows(rows); err != nil {
		return out, err
	}

	rows, err = s.db.QueryContext(s.ctx(), `SELECT ea.at, k.id, k.type, k.title, k.project, k.branch, k.machine, k.confidence, k.status, k.person
		FROM entry_actors ea JOIN knowledge k ON k.id=ea.entry_id
		WHERE ea.kind='knowledge' AND `+cond+` ORDER BY ea.at DESC, k.id DESC`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var k SessionKnowledgeEntry
		if err := rows.Scan(&k.At, &k.ID, &k.Type, &k.Title, &k.Scope.Project, &k.Scope.Branch, &k.Scope.Machine, &k.Confidence, &k.Status, &k.Person); err != nil {
			rows.Close()
			return out, err
		}
		out.Knowledge = append(out.Knowledge, k)
	}
	if err := closeRows(rows); err != nil {
		return out, err
	}

	rows, err = s.db.QueryContext(s.ctx(), `SELECT ea.at, t.id, t.title, t.project, t.request_id, COALESCE(p.name,''), t.assignee_account, t.created_by_account
		FROM entry_actors ea JOIN human_tasks t ON t.id=ea.entry_id LEFT JOIN persons p ON p.id=t.assignee_account
		WHERE ea.kind='task' AND `+cond+` ORDER BY ea.at DESC, t.id DESC`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var t SessionTask
		if err := rows.Scan(&t.At, &t.ID, &t.Title, &t.Project, &t.RequestID, &t.Assignee, &t.AssigneeAccount, &t.CreatedByAccount); err != nil {
			rows.Close()
			return out, err
		}
		out.Tasks = append(out.Tasks, t)
	}
	if err := closeRows(rows); err != nil {
		return out, err
	}

	rows, err = s.db.QueryContext(s.ctx(), `SELECT ea.at, m.id, m.body, m.destination_id, COALESCE(r.label,'')
		FROM entry_actors ea JOIN coord_messages m ON m.id=ea.entry_id LEFT JOIN coord_rooms r ON r.room_key=m.destination_id
		WHERE ea.kind='message' AND m.destination_kind='room' AND m.intent='question' AND `+cond+` ORDER BY ea.at DESC, m.id DESC`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var q SessionQuestion
		if err := rows.Scan(&q.At, &q.Message, &q.Body, &q.RoomKey, &q.Label); err != nil {
			rows.Close()
			return out, err
		}
		out.Questions = append(out.Questions, q)
	}
	if err := closeRows(rows); err != nil {
		return out, err
	}

	rows, err = s.db.QueryContext(s.ctx(), `SELECT ea.at, r.id, r.title, r.project
		FROM entry_actors ea JOIN requests r ON r.id=ea.entry_id
		WHERE ea.kind='request' AND `+cond+` ORDER BY ea.at DESC, r.id DESC`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var q SessionRequest
		if err := rows.Scan(&q.At, &q.ID, &q.Title, &q.Project); err != nil {
			rows.Close()
			return out, err
		}
		out.Requests = append(out.Requests, q)
	}
	if err := closeRows(rows); err != nil {
		return out, err
	}
	return out, nil
}

func closeRows(rows *sql.Rows) error {
	err := rows.Err()
	if cerr := rows.Close(); err == nil {
		err = cerr
	}
	return err
}

// SessionFile is a file a session changed. HasStat says whether the transcript
// still shows what was written; Add and Del are the lines then.
type SessionFile struct {
	Path     string
	At       string
	Add, Del int
	HasStat  bool
}

// maxStatChunks bounds the transcript lines read for the line counts: a session
// longer than that shows its files without counts.
const maxStatChunks = 4000

// SessionFiles names the files a session changed, newest change first, from
// path_activity (what the retention still holds; see ActivityRetention). The
// line counts (when lines is set) come from the edits in the transcript. Only pass a session the
// viewer may read and may see paths of (a guest does not).
func (s *Store) SessionFiles(sess Session, lines bool) ([]SessionFile, error) {
	if s.reader != nil {
		return s.reader.SessionFiles(sess, lines)
	}
	if sess.ID <= 0 || sess.ExternalID == "" {
		return nil, nil
	}
	owner := instanceOwnerID(s.db)
	acc := effectiveOwner(sess.AccountID, owner)
	from := time.Now().UTC().Add(-ActivityRetention).Format(time.RFC3339)
	rows, err := s.db.QueryContext(s.ctx(), `SELECT path, checkout, MAX(at) FROM path_activity
		WHERE quality<>'unattributed' AND writes<>0 AND at>=? AND session_external_id=? AND CASE WHEN account_id=0 THEN ? ELSE account_id END=?
		GROUP BY path, checkout ORDER BY MAX(at) DESC LIMIT 200`, from, sess.ExternalID, owner, acc)
	if err != nil {
		return nil, err
	}
	type raw struct{ path, checkout, at string }
	var seen []raw
	for rows.Next() {
		var r raw
		if err := rows.Scan(&r.path, &r.checkout, &r.at); err != nil {
			rows.Close()
			return nil, err
		}
		seen = append(seen, r)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	var out []SessionFile
	index := map[string]int{}
	rawPath := map[string]string{}
	for _, r := range seen {
		p := originctx.RepoPath(r.path, r.checkout)
		if p == "" {
			continue
		}
		if i, ok := index[p]; ok {
			if r.at > out[i].At {
				out[i].At = r.at
			}
			rawPath[r.path] = p
			continue
		}
		index[p] = len(out)
		rawPath[r.path] = p
		out = append(out, SessionFile{Path: p, At: r.at})
	}
	if len(out) == 0 {
		return nil, nil
	}
	var add, del map[string]int
	ok := false
	if lines {
		add, del, ok = s.sessionEditLines(sess)
	}
	if ok {
		for i := range out {
			for raw, p := range rawPath {
				if p != out[i].Path {
					continue
				}
				if _, has := add[raw]; has {
					out[i].Add += add[raw]
					out[i].Del += del[raw]
					out[i].HasStat = true
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out, nil
}

// sessionEditLines counts, per path as the tool named it, the lines the edits of
// the transcript added and removed.
func (s *Store) sessionEditLines(sess Session) (add, del map[string]int, ok bool) {
	rows, err := s.db.QueryContext(s.ctx(), `SELECT c.seq, c.raw FROM session_chunks c JOIN chunk_index i ON i.session_id=c.session_id AND i.seq=c.seq
		WHERE c.session_id=? AND i.tool_calls>0 ORDER BY c.seq LIMIT ?`, sess.ID, maxStatChunks+1)
	if err != nil {
		return nil, nil, false
	}
	defer rows.Close()
	add, del = map[string]int{}, map[string]int{}
	n := 0
	for rows.Next() {
		var seq int
		var raw string
		if err := rows.Scan(&seq, &raw); err != nil {
			return nil, nil, false
		}
		if n++; n > maxStatChunks {
			return nil, nil, false
		}
		if !strings.Contains(raw, `"file_path"`) && !strings.Contains(raw, "apply_patch") && !strings.Contains(raw, "*** ") {
			continue
		}
		for _, b := range transcript.Parse(sess.Harness, seq, raw).Blocks {
			for _, f := range b.Diff {
				for _, l := range f.Lines {
					switch l.Op {
					case '+':
						add[f.Path]++
					case '-':
						del[f.Path]++
					}
				}
				if _, has := add[f.Path]; !has {
					add[f.Path] = 0
				}
			}
		}
	}
	return add, del, rows.Err() == nil
}
