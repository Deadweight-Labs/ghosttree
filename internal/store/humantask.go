package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

const taskCols = `t.id,t.project,t.request_id,COALESCE(t.work_id,0),t.kind,t.title,t.description,t.location,t.check_hint,t.state,
	t.assignee_account,COALESCE(pe.name,''),t.created_by_account,t.created_by_person,t.done_by_person,t.done_note,t.done_via,t.done_at,
	t.state_note,t.confirmed_at,t.created_at,t.updated_at,COALESCE(w.summary,''),COALESCE(w.session_id,0)`

const taskFrom = ` FROM human_tasks t LEFT JOIN persons pe ON pe.id=t.assignee_account AND t.assignee_account>0
	LEFT JOIN request_work w ON w.id=t.work_id`

func scanTasks(rows *sql.Rows) ([]humantask.Task, error) {
	var out []humantask.Task
	for rows.Next() {
		var t humantask.Task
		if err := rows.Scan(&t.ID, &t.Project, &t.RequestID, &t.WorkID, &t.Kind, &t.Title, &t.Description, &t.Location, &t.CheckHint, &t.State,
			&t.AssigneeAccount, &t.Assignee, &t.CreatedByAccount, &t.CreatedBy, &t.DoneBy, &t.DoneNote, &t.DoneVia, &t.DoneAt,
			&t.StateNote, &t.ConfirmedAt, &t.CreatedAt, &t.UpdatedAt, &t.Handoff, &t.HandoffSession); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TaskByID loads a task without an access check; callers check.
func (s *Store) TaskByID(id int64) (humantask.Task, error) {
	if s.reader != nil {
		return s.reader.TaskByID(id)
	}
	rows, err := s.db.Query(`SELECT `+taskCols+taskFrom+` WHERE t.id=?`, id)
	if err != nil {
		return humantask.Task{}, err
	}
	defer rows.Close()
	list, err := scanTasks(rows)
	if err != nil {
		return humantask.Task{}, err
	}
	if len(list) == 0 {
		return humantask.Task{}, sql.ErrNoRows
	}
	return list[0], nil
}

// GetHumanTask returns one task if the viewer may read it.
func (a *ProjectAccess) GetHumanTask(id int64) (humantask.Task, error) {
	t, err := a.st.TaskByID(id)
	if err != nil {
		a.Filtered()
		return t, err
	}
	if err := a.CheckTask(t, ActRead); err != nil {
		return humantask.Task{}, err
	}
	return t, nil
}

func workInvalid() error {
	return requestdomain.NewRuleError("work_invalid", "that work does not belong to the request or you may not work on it",
		"use the work id from request_start_work for this request", nil)
}

// CreateHumanTask asks the human for something only they can provide. Secrets
// and file contents never enter: there is no column for a value, and the text
// fields are checked for token patterns before anything is written.
func (a *ProjectAccess) CreateHumanTask(in humantask.CreateInput, actor humantask.Actor) (humantask.Task, error) {
	st := a.st
	if err := humantask.ValidateCreate(in); err != nil {
		return humantask.Task{}, err
	}
	if err := humantask.CheckFields("title", in.Title, "description", in.Description, "location", in.Location, "check_hint", in.CheckHint); err != nil {
		return humantask.Task{}, err
	}
	requestID, workID := in.RequestID, in.WorkID
	if workID > 0 {
		wreq, ok := st.workRequest(workID)
		if !ok || (requestID != 0 && requestID != wreq) {
			return humantask.Task{}, workInvalid()
		}
		requestID = wreq
	}
	if requestID == 0 && in.SessionID > 0 {
		if sess, err := st.SessionByID(in.SessionID); err == nil && a.CanSeeTranscript(sess) {
			workID, requestID = st.activePrimaryWork(in.SessionID)
		}
	}
	if requestID == 0 {
		return humantask.Task{}, requestdomain.NewRuleError("request_required", "a human task hangs on a request: request_start_work first, or give request_id",
			"request_start_work first, or give request_id", nil)
	}
	ref, err := st.RequestRef(RefRequest, requestID)
	if err != nil {
		a.Filtered()
		return humantask.Task{}, linkTargetMissing()
	}
	project := strings.TrimSpace(in.Project)
	if project == "" {
		project = ref.Project
	}
	if _, err := a.resolveLinkTarget(project, "request", requestID); err != nil {
		return humantask.Task{}, err
	}
	if err := a.Check(project, ResTask, ActCreate, Object{Own: true}); err != nil {
		return humantask.Task{}, err
	}
	if workID > 0 {
		obj, _ := a.RequestObject(ref)
		if !a.Decide(project, ResRequest, ActWork, obj).Allowed {
			return humantask.Task{}, workInvalid()
		}
	}
	if st.requestState(requestID) != "open" {
		return humantask.Task{}, requestdomain.NewRuleError("request_not_open", "the request is not open",
			"attach the task to an open request", nil)
	}
	var assignee int64
	if name := strings.TrimSpace(in.Assignee); name != "" {
		id, ok := st.personByName(name)
		if !ok || RoleRank(st.ProjectRole(project, principalOfID(id)).Role) < 1 {
			return humantask.Task{}, requestdomain.NewRuleError("assignee_not_member", "the assignee has no role in this project",
				"assign to a member of the project or leave it open", nil)
		}
		assignee = id
	}
	in.Title = strings.TrimSpace(in.Title)
	id, err := st.commitTask(in, actor, project, requestID, workID, assignee)
	if err != nil {
		return humantask.Task{}, err
	}
	return st.TaskByID(id)
}

func (s *Store) commitTask(in humantask.CreateInput, actor humantask.Actor, project string, requestID, workID, assignee int64) (int64, error) {
	if s.writer != nil {
		return queueValue(s, []any{in, actor, project, requestID, workID, assignee}, func(d *Store, p []any) (int64, error) {
			return d.commitTask(p[0].(humantask.CreateInput), p[1].(humantask.Actor), p[2].(string), p[3].(int64), p[4].(int64), p[5].(int64))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := now()
	var work any
	if workID > 0 {
		work = workID
	}
	res, err := tx.Exec(`INSERT INTO human_tasks(project,request_id,work_id,kind,title,description,location,check_hint,assignee_account,
		created_by_account,created_by_person,created_session_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		project, requestID, work, in.Kind, in.Title, in.Description, strings.TrimSpace(in.Location), in.CheckHint, assignee,
		actor.AccountID, actor.Person, actor.SessionID, ts, ts)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if err := linkActivity(tx, requestID, "task.created", fmt.Sprintf("TASK-%d %s %s", id, in.Kind, in.Title), actor.Person, actor.SessionID, ts); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) workRequest(workID int64) (int64, bool) {
	if s.reader != nil {
		return s.reader.workRequest(workID)
	}
	var id int64
	err := s.db.QueryRow(`SELECT request_id FROM request_work WHERE id=?`, workID).Scan(&id)
	return id, err == nil
}

func (s *Store) activePrimaryWork(sessionID int64) (workID, requestID int64) {
	if s.reader != nil {
		return s.reader.activePrimaryWork(sessionID)
	}
	_ = s.db.QueryRow(`SELECT id,request_id FROM request_work WHERE session_id=? AND role='primary' AND state='active'`, sessionID).Scan(&workID, &requestID)
	return
}

func (s *Store) requestState(id int64) string {
	if s.reader != nil {
		return s.reader.requestState(id)
	}
	var st string
	_ = s.db.QueryRow(`SELECT state FROM requests WHERE id=?`, id).Scan(&st)
	return st
}

func (s *Store) personByName(name string) (int64, bool) {
	if s.reader != nil {
		return s.reader.personByName(name)
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM persons WHERE lower(name)=lower(?)`, name).Scan(&id)
	return id, err == nil
}

// MarkTasksDone records the human's claim that tasks are done. All or nothing:
// the first id that is unknown, hidden or not allowed stops everything. Done is
// a claim, not proof; the agent confirms after checking.
func (a *ProjectAccess) MarkTasksDone(ids []int64, note string, actor humantask.Actor) ([]humantask.Task, error) {
	note = strings.TrimSpace(note)
	if err := humantask.CheckText("note", note); err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	var uniq []int64
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	if len(uniq) == 0 {
		return nil, requestdomain.NewRuleError("task_required", "no task named", "give at least one task id", nil)
	}
	for _, id := range uniq {
		t, err := a.st.TaskByID(id)
		if err != nil {
			a.Filtered()
			return nil, err
		}
		if err := a.CheckTask(t, ActWork); err != nil {
			return nil, err
		}
	}
	if err := a.st.commitTaskChange("done", uniq, note, actor); err != nil {
		return nil, err
	}
	return a.st.tasksByIDs(uniq), nil
}

func (s *Store) tasksByIDs(ids []int64) []humantask.Task {
	var out []humantask.Task
	for _, id := range ids {
		if t, err := s.TaskByID(id); err == nil {
			out = append(out, t)
		}
	}
	return out
}

// commitTaskChange applies one transition to each task in a single
// transaction; op is done, confirm, reopen or drop.
func (s *Store) commitTaskChange(op string, ids []int64, reason string, actor humantask.Actor) error {
	if s.writer != nil {
		return queueWrite(s, []any{op, ids, reason, actor}, func(d *Store, p []any) error {
			return d.commitTaskChange(p[0].(string), p[1].([]int64), p[2].(string), p[3].(humantask.Actor))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	for _, id := range ids {
		rows, err := tx.Query(`SELECT `+taskCols+taskFrom+` WHERE t.id=?`, id)
		if err != nil {
			return err
		}
		list, err := scanTasks(rows)
		rows.Close()
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return sql.ErrNoRows
		}
		if err := applyTaskChange(tx, op, list[0], reason, actor, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// validateTaskChange is the transition table of human tasks.
func validateTaskChange(op string, t humantask.Task, reason string) error {
	switch op {
	case "done":
		if t.State != humantask.Open {
			return humantask.InvalidTransition(t.State, humantask.Done)
		}
	case "confirm":
		if t.State != humantask.Done {
			return humantask.InvalidTransition(t.State, humantask.Confirmed)
		}
	case "reopen":
		if t.State == humantask.Open {
			return humantask.InvalidTransition(t.State, humantask.Open)
		}
		if reason == "" {
			return humantask.ReasonRequired()
		}
	case "drop":
		if t.State != humantask.Open && t.State != humantask.Done {
			return humantask.InvalidTransition(t.State, humantask.Dropped)
		}
		if reason == "" {
			return humantask.ReasonRequired()
		}
	default:
		return fmt.Errorf("unknown task change %q", op)
	}
	return nil
}

func applyTaskChange(tx txExec, op string, t humantask.Task, reason string, actor humantask.Actor, ts string) error {
	if err := validateTaskChange(op, t, reason); err != nil {
		return err
	}
	ref := fmt.Sprintf("TASK-%d", t.ID)
	switch op {
	case "done":
		via := "token"
		if actor.Web {
			via = "web"
		}
		if _, err := tx.Exec(`UPDATE human_tasks SET state='done',done_by_account=?,done_by_person=?,done_note=?,done_via=?,done_at=?,updated_at=? WHERE id=?`,
			actor.AccountID, actor.Person, reason, via, ts, ts, t.ID); err != nil {
			return err
		}
		return linkActivity(tx, t.RequestID, "task.done", ref, actor.Person, actor.SessionID, ts)
	case "confirm":
		if _, err := tx.Exec(`UPDATE human_tasks SET state='confirmed',confirmed_session_id=?,confirmed_at=?,updated_at=? WHERE id=?`, actor.SessionID, ts, ts, t.ID); err != nil {
			return err
		}
		return linkActivity(tx, t.RequestID, "task.confirmed", ref, actor.Person, actor.SessionID, ts)
	case "reopen":
		if _, err := tx.Exec(`UPDATE human_tasks SET state='open',state_note=?,done_by_account=0,done_by_person='',done_note='',done_via='',done_at='',
			confirmed_session_id=0,confirmed_at='',updated_at=? WHERE id=?`, reason, ts, t.ID); err != nil {
			return err
		}
		return linkActivity(tx, t.RequestID, "task.reopened", ref+": "+reason, actor.Person, actor.SessionID, ts)
	case "drop":
		if _, err := tx.Exec(`UPDATE human_tasks SET state='dropped',state_note=?,updated_at=? WHERE id=?`, reason, ts, t.ID); err != nil {
			return err
		}
		return linkActivity(tx, t.RequestID, "task.dropped", ref+": "+reason, actor.Person, actor.SessionID, ts)
	}
	return fmt.Errorf("unknown task change %q", op)
}

// ConfirmHumanTask: the agent has checked the claim against the check hint.
func (a *ProjectAccess) ConfirmHumanTask(id int64, actor humantask.Actor) (humantask.Task, error) {
	return a.changeTask("confirm", id, "", actor)
}

// ReopenHumanTask puts a done, confirmed or dropped task back to open.
func (a *ProjectAccess) ReopenHumanTask(id int64, reason string, actor humantask.Actor) (humantask.Task, error) {
	return a.changeTask("reopen", id, reason, actor)
}

// DropHumanTask discards an open or done task.
func (a *ProjectAccess) DropHumanTask(id int64, reason string, actor humantask.Actor) (humantask.Task, error) {
	return a.changeTask("drop", id, reason, actor)
}

func (a *ProjectAccess) changeTask(op string, id int64, reason string, actor humantask.Actor) (humantask.Task, error) {
	t, err := a.st.TaskByID(id)
	if err != nil {
		a.Filtered()
		return t, err
	}
	if err := a.CheckTask(t, ActEdit); err != nil {
		return humantask.Task{}, err
	}
	reason = strings.TrimSpace(reason)
	if err := humantask.CheckText("reason", reason); err != nil {
		return humantask.Task{}, err
	}
	// Check the transition against what was just read so a bad one answers
	// before the writer is involved; the transaction checks it again.
	if err := validateTaskChange(op, t, reason); err != nil {
		return humantask.Task{}, err
	}
	if err := a.st.commitTaskChange(op, []int64{id}, reason, actor); err != nil {
		return humantask.Task{}, err
	}
	return a.st.TaskByID(id)
}

// TaskFilter limits ListHumanTasks. State: open, done, confirmed, dropped,
// pending (open and done, the default) or all.
type TaskFilter struct {
	Project   string
	State     string
	Mine      bool
	RequestID int64
	Limit     int
}

func (s *Store) ListHumanTasks(pa *ProjectAccess, f TaskFilter) ([]humantask.Task, error) {
	if s.reader != nil {
		return s.reader.ListHumanTasks(pa, f)
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 200
	}
	rows, err := s.taskRows(f, pa.account)
	if err != nil {
		return nil, err
	}
	out := make([]humantask.Task, 0, len(rows))
	for _, t := range rows {
		if pa.CanSeeTask(t) {
			out = append(out, t)
			if len(out) >= f.Limit {
				break
			}
		}
	}
	return out, nil
}

func (s *Store) taskRows(f TaskFilter, account int64) ([]humantask.Task, error) {
	if s.reader != nil {
		return s.reader.taskRows(f, account)
	}
	where, args := []string{`t.project=?`}, []any{f.Project}
	switch f.State {
	case "", "pending":
		where = append(where, `t.state IN ('open','done')`)
	case "all":
	default:
		where = append(where, `t.state=?`)
		args = append(args, f.State)
	}
	if f.RequestID != 0 {
		where = append(where, `t.request_id=?`)
		args = append(args, f.RequestID)
	}
	if f.Mine {
		where = append(where, `(t.assignee_account=? OR (t.assignee_account=0 AND t.state='open'))`)
		args = append(args, account)
	}
	rows, err := s.db.Query(`SELECT `+taskCols+taskFrom+` WHERE `+strings.Join(where, ` AND `)+
		` ORDER BY CASE t.state WHEN 'open' THEN 0 WHEN 'done' THEN 1 WHEN 'confirmed' THEN 2 ELSE 3 END, t.id LIMIT 2000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

// WaitingTasksFor is what the bootstrap shows: tasks the human finished and the
// agent has not confirmed, then tasks still open, each capped at limit.
func (s *Store) WaitingTasksFor(pa *ProjectAccess, project string, limit int) (humantask.Waiting, error) {
	if s.reader != nil {
		return s.reader.WaitingTasksFor(pa, project, limit)
	}
	var w humantask.Waiting
	if strings.TrimSpace(project) == "" {
		return w, nil
	}
	rows, err := s.taskRows(TaskFilter{Project: project, State: "pending"}, pa.account)
	if err != nil {
		return w, err
	}
	for _, t := range rows {
		if !pa.CanSeeTask(t) {
			continue
		}
		if t.HandoffSession != 0 {
			if sess, err := s.SessionByID(t.HandoffSession); err != nil || !pa.CanSeeTranscript(sess) {
				t.Handoff = ""
			}
		}
		if t.State == humantask.Done {
			if len(w.Done) < limit {
				w.Done = append(w.Done, t)
			} else {
				w.MoreDone++
			}
			continue
		}
		if len(w.Open) < limit {
			w.Open = append(w.Open, t)
		} else {
			w.MoreOpen++
		}
	}
	return w, nil
}
