package store

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// Linking pains and human tasks to requests shares one logic: a target must
// exist, lie in the same project and be visible to the viewer; the request
// shows what hangs on it; every change leaves a line in its activity.

func linkTargetMissing() error {
	return requestdomain.NewRuleError("link_target_not_found", "target not found",
		"the target must exist, lie in the same project and be visible to you", nil)
}

// resolveLinkTarget checks a request or knowledge target for a viewer and
// returns its title. Anything the viewer may not read answers like a missing
// target, so the answer says nothing about hidden objects.
func (a *ProjectAccess) resolveLinkTarget(project, kind string, id int64) (string, error) {
	project = strings.TrimSpace(project)
	if project == "" || id <= 0 {
		return "", linkTargetMissing()
	}
	switch kind {
	case "request":
		ref, err := a.st.RequestRef(RefRequest, id)
		if err != nil || ref.Project != project || !requestReadable(a, ref) {
			return "", linkTargetMissing()
		}
		return a.st.requestTitle(id), nil
	case "knowledge":
		k, err := a.st.KnowledgeRef(id)
		if err != nil || k.Scope.Project != project || a.CheckKnowledge(k, ActRead) != nil {
			return "", linkTargetMissing()
		}
		return a.st.knowledgeTitle(id), nil
	}
	return "", linkTargetMissing()
}

func requestReadable(a *ProjectAccess, ref ObjectRef) bool {
	obj, visible := a.RequestObject(ref)
	return visible && a.Check(ref.Project, ResRequest, ActRead, obj) == nil
}

func (s *Store) requestTitle(id int64) string {
	if s.reader != nil {
		return s.reader.requestTitle(id)
	}
	var t string
	_ = s.db.QueryRow(`SELECT title FROM requests WHERE id=?`, id).Scan(&t)
	return t
}

func (s *Store) knowledgeTitle(id int64) string {
	if s.reader != nil {
		return s.reader.knowledgeTitle(id)
	}
	var t string
	_ = s.db.QueryRow(`SELECT title FROM knowledge WHERE id=?`, id).Scan(&t)
	return t
}

// linkActivity writes an event on a request in the caller's transaction.
func linkActivity(tx txExec, requestID int64, kind, data, person string, sessionID int64, ts string) error {
	_, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at,session_id) VALUES(?,?,?,?,?,?)`,
		requestID, kind, person, data, ts, sessionID)
	return err
}

// requestAttachments lists what hangs on a request, filtered by the same
// functions every other list uses.
func (a *ProjectAccess) requestAttachments(requestID int64) ([]requestdomain.PainRef, []requestdomain.TaskRef) {
	var pains []requestdomain.PainRef
	if rows, err := a.st.painsLinkedTo("request", requestID); err == nil {
		for _, p := range rows {
			if a.CanSeePain(p) {
				pains = append(pains, requestdomain.PainRef{ID: p.ID, Title: p.Title, State: p.State, Remaining: p.Remaining})
			}
		}
	}
	var tasks []requestdomain.TaskRef
	if rows, err := a.st.tasksOfRequest(requestID); err == nil {
		for _, t := range rows {
			if a.CanSeeTask(t) {
				tasks = append(tasks, requestdomain.TaskRef{ID: t.ID, Kind: t.Kind, Title: t.Title, State: t.State, Location: t.Location})
			}
		}
	}
	return pains, tasks
}

// PainsFor lists the pains that motivate a request or a knowledge entry, for
// the viewer.
func (a *ProjectAccess) PainsFor(dstKind string, dstID int64) []pain.Pain {
	rows, err := a.st.painsLinkedTo(dstKind, dstID)
	if err != nil {
		return nil
	}
	var out []pain.Pain
	for _, p := range rows {
		if a.CanSeePain(p) {
			out = append(out, p)
		}
	}
	return out
}

func (s *Store) painsLinkedTo(dstKind string, dstID int64) ([]pain.Pain, error) {
	if s.reader != nil {
		return s.reader.painsLinkedTo(dstKind, dstID)
	}
	rows, err := s.db.Query(`SELECT `+painCols+` FROM entity_links l JOIN pains p ON p.id=l.src_id
		LEFT JOIN pain_authors au ON au.pain_id=p.id
		WHERE l.src_kind='pain' AND l.kind='motivates' AND l.dst_kind=? AND l.dst_id=? ORDER BY p.id`, dstKind, dstID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPains(rows)
}

func (s *Store) tasksOfRequest(requestID int64) ([]humantask.Task, error) {
	if s.reader != nil {
		return s.reader.tasksOfRequest(requestID)
	}
	rows, err := s.db.Query(`SELECT `+taskCols+taskFrom+` WHERE t.request_id=? ORDER BY t.id`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

// -------- visibility, the one filter every list goes through

func (a *ProjectAccess) painObject(p pain.Pain) Object {
	return Object{Own: a.valid && a.account != 0 && p.AuthorID == a.account}
}

// CanSeePain is the read filter for every list of pains.
func (a *ProjectAccess) CanSeePain(p pain.Pain) bool {
	return a.Allow(p.Project, ResPain, ActRead, a.painObject(p))
}

func (a *ProjectAccess) CheckPain(p pain.Pain, act Action) error {
	return a.Check(p.Project, ResPain, act, a.painObject(p))
}

func (a *ProjectAccess) taskObject(t humantask.Task) Object {
	return Object{
		Own:        a.valid && a.account != 0 && t.CreatedByAccount == a.account,
		Assigned:   a.valid && a.account != 0 && t.AssigneeAccount == a.account,
		Unassigned: t.AssigneeAccount == 0,
	}
}

// CanSeeTask is the read filter for every list of human tasks: members see all,
// a guest only what is assigned to them.
func (a *ProjectAccess) CanSeeTask(t humantask.Task) bool {
	return a.Allow(t.Project, ResTask, ActRead, a.taskObject(t))
}

// CheckTask checks an action on a task. A task the viewer may not read answers
// like an unknown one, whatever the action.
func (a *ProjectAccess) CheckTask(t humantask.Task, act Action) error {
	obj := a.taskObject(t)
	if act != ActRead {
		if err := a.Check(t.Project, ResTask, ActRead, obj); err != nil {
			return err
		}
	}
	return a.Check(t.Project, ResTask, act, obj)
}

// -------- helpers shared by pain.go and humantask.go

func idRef(prefix string, id int64) string { return prefix + strconv.FormatInt(id, 10) }

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
