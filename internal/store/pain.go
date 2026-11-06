package store

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

const painCols = `p.id,p.project,p.title,p.body,p.state,p.remaining,p.person,p.created_at,p.updated_at,COALESCE(au.account_id,0)`

const painFrom = ` FROM pains p LEFT JOIN pain_authors au ON au.pain_id=p.id`

func scanPains(rows *sql.Rows) ([]pain.Pain, error) {
	var out []pain.Pain
	for rows.Next() {
		var p pain.Pain
		if err := rows.Scan(&p.ID, &p.Project, &p.Title, &p.Body, &p.State, &p.Remaining, &p.Person, &p.CreatedAt, &p.UpdatedAt, &p.AuthorID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PainByID loads a pain without any access check; callers check.
func (s *Store) PainByID(id int64) (pain.Pain, error) {
	if s.reader != nil {
		return s.reader.PainByID(id)
	}
	rows, err := s.db.Query(`SELECT `+painCols+painFrom+` WHERE p.id=?`, id)
	if err != nil {
		return pain.Pain{}, err
	}
	defer rows.Close()
	list, err := scanPains(rows)
	if err != nil {
		return pain.Pain{}, err
	}
	if len(list) == 0 {
		return pain.Pain{}, sql.ErrNoRows
	}
	return list[0], nil
}

// resolvedTarget is a validated edge target.
type resolvedTarget struct {
	Kind  string
	ID    int64
	Ref   string
	Title string
}

func (a *ProjectAccess) resolveTargets(project string, targets []pain.Target) ([]resolvedTarget, error) {
	var out []resolvedTarget
	for _, t := range targets {
		n := 0
		if t.RequestID != 0 {
			n++
		}
		if t.KnowledgeID != 0 {
			n++
		}
		if strings.TrimSpace(t.ExternalRef) != "" {
			n++
		}
		if n != 1 {
			return nil, requestdomain.NewRuleError("link_target_required", "a link needs exactly one target",
				"give one of request_id, knowledge_id or external_ref", nil)
		}
		switch {
		case t.RequestID != 0:
			title, err := a.resolveLinkTarget(project, "request", t.RequestID)
			if err != nil {
				return nil, err
			}
			out = append(out, resolvedTarget{Kind: "request", ID: t.RequestID, Title: title})
		case t.KnowledgeID != 0:
			title, err := a.resolveLinkTarget(project, "knowledge", t.KnowledgeID)
			if err != nil {
				return nil, err
			}
			out = append(out, resolvedTarget{Kind: "knowledge", ID: t.KnowledgeID, Title: title})
		default:
			out = append(out, resolvedTarget{Kind: "external", Ref: strings.TrimSpace(t.ExternalRef)})
		}
	}
	return out, nil
}

func targetData(t resolvedTarget) string {
	if t.Kind == "external" {
		return "external:" + t.Ref
	}
	return fmt.Sprintf("%s:%d", t.Kind, t.ID)
}

// insertLinkTx stores one edge; a duplicate is a success without a new row.
func insertLinkTx(tx txExec, painID int64, project string, t resolvedTarget, a pain.Actor, ts string) (int64, bool, error) {
	res, err := tx.Exec(`INSERT OR IGNORE INTO entity_links(project,src_kind,src_id,kind,dst_kind,dst_id,dst_ref,account_id,created_at)
		VALUES(?,'pain',?,'motivates',?,?,?,?,?)`, project, painID, t.Kind, t.ID, t.Ref, a.AccountID, ts)
	if err != nil {
		return 0, false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		var id int64
		err := tx.QueryRow(`SELECT id FROM entity_links WHERE src_kind='pain' AND src_id=? AND kind='motivates' AND dst_kind=? AND dst_id=? AND dst_ref=?`,
			painID, t.Kind, t.ID, t.Ref).Scan(&id)
		return id, false, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT INTO pain_events(pain_id,kind,data,person,account_id,session_id,created_at) VALUES(?,'link_added',?,?,?,?,?)`,
		painID, targetData(t), a.Person, a.AccountID, a.SessionID, ts); err != nil {
		return 0, false, err
	}
	if t.Kind == "request" {
		if err := linkActivity(tx, t.ID, "pain.linked", fmt.Sprintf("PAIN-%d", painID), a.Person, a.SessionID, ts); err != nil {
			return 0, false, err
		}
	}
	return id, true, nil
}

type CreatePainInput struct {
	Project   string
	Title     string
	Body      string
	Motivates []pain.Target
	Actor     pain.Actor
	// AuthorID is the writing account; 0 lets the store resolve Actor.Person.
	AuthorID int64
}

// CreatePain records a problem without a solution: a title, optionally a text
// and what it motivates. No criteria and no solution field exist.
func (a *ProjectAccess) CreatePain(in CreatePainInput) (pain.Detail, error) {
	in.Project = strings.TrimSpace(in.Project)
	if strings.TrimSpace(in.Title) == "" {
		return pain.Detail{}, pain.TitleRequired()
	}
	if in.Project == "" {
		return pain.Detail{}, requestdomain.NewRuleError("project_required", "a pain belongs to a project", "name the project", nil)
	}
	if err := a.Check(in.Project, ResPain, ActCreate, Object{Own: true}); err != nil {
		return pain.Detail{}, err
	}
	targets, err := a.resolveTargets(in.Project, in.Motivates)
	if err != nil {
		return pain.Detail{}, err
	}
	id, err := a.st.commitPain(in, targets)
	if err != nil {
		return pain.Detail{}, err
	}
	return a.st.painDetail(a, id)
}

func (s *Store) commitPain(in CreatePainInput, targets []resolvedTarget) (int64, error) {
	if s.writer != nil {
		return queueValue(s, []any{in, targets}, func(d *Store, p []any) (int64, error) {
			return d.commitPain(p[0].(CreatePainInput), p[1].([]resolvedTarget))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.Exec(`INSERT INTO pains(project,title,body,state,remaining,person,created_at,updated_at) VALUES(?,?,?,'active','',?,?,?)`,
		in.Project, strings.TrimSpace(in.Title), in.Body, in.Actor.Person, ts, ts)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	account := authorForWriteTx(tx, in.AuthorID, in.Actor.Person, "agent", "")
	if _, err := tx.Exec(`INSERT INTO pain_authors(pain_id,account_id) VALUES(?,?)`, id, account); err != nil {
		return 0, err
	}
	actor := in.Actor
	actor.AccountID = account
	if _, err := tx.Exec(`INSERT INTO pain_events(pain_id,kind,to_state,person,account_id,session_id,created_at) VALUES(?,'created','active',?,?,?,?)`,
		id, actor.Person, account, actor.SessionID, ts); err != nil {
		return 0, err
	}
	for _, t := range targets {
		if _, _, err := insertLinkTx(tx, id, in.Project, t, actor, ts); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// SetPainState moves a pain along the transition table. Nothing else writes
// pains.state: a pain does not age and does not resolve because a request ends.
func (a *ProjectAccess) SetPainState(id int64, to, remaining string, ev pain.Evidence, note string, actor pain.Actor) (pain.Detail, error) {
	p, err := a.st.PainByID(id)
	if err != nil {
		a.Filtered()
		return pain.Detail{}, err
	}
	if err := a.CheckPain(p, ActWork); err != nil {
		return pain.Detail{}, err
	}
	remaining, note = strings.TrimSpace(remaining), strings.TrimSpace(note)
	ev.Kind, ev.Ref = strings.TrimSpace(ev.Kind), strings.TrimSpace(ev.Ref)
	if err := pain.ValidateTransition(p.State, to, remaining, ev, note); err != nil {
		return pain.Detail{}, err
	}
	if ev.Kind != "" || ev.Ref != "" {
		if err := pain.ValidateEvidence(ev); err != nil {
			return pain.Detail{}, err
		}
		ref, err := a.checkEvidenceRef(p.Project, ev)
		if err != nil {
			return pain.Detail{}, err
		}
		ev.Ref = ref
	}
	if to != pain.Mitigated {
		remaining = ""
	}
	if err := a.st.commitPainState(id, p.State, to, remaining, ev, note, actor); err != nil {
		return pain.Detail{}, err
	}
	return a.st.painDetail(a, id)
}

// checkEvidenceRef verifies that a request or decision named as evidence exists
// and is readable, and normalizes its spelling.
func (a *ProjectAccess) checkEvidenceRef(project string, ev pain.Evidence) (string, error) {
	bad := requestdomain.NewRuleError("evidence_invalid", "the evidence points at nothing you can see",
		"name an existing request (REQ-n) or decision (#n) of this project", nil)
	switch ev.Kind {
	case "request":
		n, ok := pain.ParseNumberRef(ev.Ref, "REQ-")
		if !ok {
			return "", bad
		}
		if _, err := a.resolveLinkTarget(project, "request", n); err != nil {
			return "", bad
		}
		return idRef("REQ-", n), nil
	case "decision":
		n, ok := pain.ParseNumberRef(ev.Ref, "")
		if !ok {
			return "", bad
		}
		if _, err := a.resolveLinkTarget(project, "knowledge", n); err != nil {
			return "", bad
		}
		return idRef("#", n), nil
	case "session":
		if canon, ok := canonicalSessionRef(ev.Ref); ok {
			return canon, nil
		}
	}
	return ev.Ref, nil
}

func (s *Store) commitPainState(id int64, from, to, remaining string, ev pain.Evidence, note string, actor pain.Actor) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, from, to, remaining, ev, note, actor}, func(d *Store, q []any) error {
			return d.commitPainState(q[0].(int64), q[1].(string), q[2].(string), q[3].(string), q[4].(pain.Evidence), q[5].(string), q[6].(pain.Actor))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var cur string
	if err := tx.QueryRow(`SELECT state FROM pains WHERE id=?`, id).Scan(&cur); err != nil {
		return err
	}
	if cur != from {
		if err := pain.ValidateTransition(cur, to, remaining, ev, note); err != nil {
			return err
		}
		from = cur
	}
	ts := now()
	if _, err := tx.Exec(`UPDATE pains SET state=?,remaining=?,updated_at=? WHERE id=?`, to, remaining, ts, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO pain_events(pain_id,kind,from_state,to_state,remaining,evidence_kind,evidence_ref,note,person,account_id,session_id,created_at)
		VALUES(?,'state',?,?,?,?,?,?,?,?,?,?)`, id, from, to, remaining, ev.Kind, ev.Ref, note, actor.Person, actor.AccountID, actor.SessionID, ts); err != nil {
		return err
	}
	return tx.Commit()
}

// CorrectPain changes title or text; the reason is written to the history.
func (a *ProjectAccess) CorrectPain(id int64, title, body *string, reason string, actor pain.Actor) (pain.Detail, error) {
	p, err := a.st.PainByID(id)
	if err != nil {
		a.Filtered()
		return pain.Detail{}, err
	}
	if err := a.CheckPain(p, ActEdit); err != nil {
		return pain.Detail{}, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return pain.Detail{}, pain.ReasonRequired()
	}
	var changed []string
	newTitle, newBody := p.Title, p.Body
	if title != nil {
		if strings.TrimSpace(*title) == "" {
			return pain.Detail{}, pain.TitleRequired()
		}
		newTitle = strings.TrimSpace(*title)
		changed = append(changed, "title")
	}
	if body != nil {
		newBody = *body
		changed = append(changed, "body")
	}
	if len(changed) == 0 {
		return pain.Detail{}, requestdomain.NewRuleError("nothing_to_change", "give a new title or body", "pass title or body", nil)
	}
	if err := a.st.commitPainCorrection(id, newTitle, newBody, reason, strings.Join(changed, ","), actor); err != nil {
		return pain.Detail{}, err
	}
	return a.st.painDetail(a, id)
}

func (s *Store) commitPainCorrection(id int64, title, body, reason, changed string, actor pain.Actor) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, title, body, reason, changed, actor}, func(d *Store, q []any) error {
			return d.commitPainCorrection(q[0].(int64), q[1].(string), q[2].(string), q[3].(string), q[4].(string), q[5].(pain.Actor))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	if _, err := tx.Exec(`UPDATE pains SET title=?,body=?,updated_at=? WHERE id=?`, title, body, ts, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO pain_events(pain_id,kind,note,data,person,account_id,session_id,created_at) VALUES(?,'corrected',?,?,?,?,?,?)`,
		id, reason, changed, actor.Person, actor.AccountID, actor.SessionID, ts); err != nil {
		return err
	}
	return tx.Commit()
}

// AddPainLink adds an edge pain -> request, knowledge entry or external text.
func (a *ProjectAccess) AddPainLink(id int64, target pain.Target, actor pain.Actor) (pain.Detail, error) {
	p, err := a.st.PainByID(id)
	if err != nil {
		a.Filtered()
		return pain.Detail{}, err
	}
	if err := a.CheckPain(p, ActWork); err != nil {
		return pain.Detail{}, err
	}
	targets, err := a.resolveTargets(p.Project, []pain.Target{target})
	if err != nil {
		return pain.Detail{}, err
	}
	if err := a.st.commitPainLink(id, p.Project, targets[0], actor); err != nil {
		return pain.Detail{}, err
	}
	return a.st.painDetail(a, id)
}

func (s *Store) commitPainLink(id int64, project string, t resolvedTarget, actor pain.Actor) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, project, t, actor}, func(d *Store, q []any) error {
			return d.commitPainLink(q[0].(int64), q[1].(string), q[2].(resolvedTarget), q[3].(pain.Actor))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, _, err := insertLinkTx(tx, id, project, t, actor, now()); err != nil {
		return err
	}
	return tx.Commit()
}

// RemovePainLink removes an edge and says why in the history.
func (a *ProjectAccess) RemovePainLink(linkID int64, reason string, actor pain.Actor) error {
	var painID, dstID int64
	var dstKind, dstRef string
	if err := a.st.linkRow(linkID, &painID, &dstKind, &dstID, &dstRef); err != nil {
		a.Filtered()
		return err
	}
	p, err := a.st.PainByID(painID)
	if err != nil {
		a.Filtered()
		return err
	}
	if err := a.CheckPain(p, ActWork); err != nil {
		return err
	}
	// An edge to something the viewer may not read answers like an unknown link id.
	if dstKind == "request" || dstKind == "knowledge" {
		if _, err := a.resolveLinkTarget(p.Project, dstKind, dstID); err != nil {
			a.Filtered()
			return sql.ErrNoRows
		}
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return pain.ReasonRequired()
	}
	return a.st.commitPainUnlink(linkID, painID, resolvedTarget{Kind: dstKind, ID: dstID, Ref: dstRef}, reason, actor)
}

func (s *Store) commitPainUnlink(linkID, painID int64, t resolvedTarget, reason string, actor pain.Actor) error {
	if s.writer != nil {
		return queueWrite(s, []any{linkID, painID, t, reason, actor}, func(d *Store, q []any) error {
			return d.commitPainUnlink(q[0].(int64), q[1].(int64), q[2].(resolvedTarget), q[3].(string), q[4].(pain.Actor))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM entity_links WHERE id=?`, linkID); err != nil {
		return err
	}
	ts := now()
	if _, err := tx.Exec(`INSERT INTO pain_events(pain_id,kind,note,data,person,account_id,session_id,created_at) VALUES(?,'link_removed',?,?,?,?,?,?)`,
		painID, reason, targetData(t), actor.Person, actor.AccountID, actor.SessionID, ts); err != nil {
		return err
	}
	if t.Kind == "request" {
		if err := linkActivity(tx, t.ID, "pain.unlinked", fmt.Sprintf("PAIN-%d", painID), actor.Person, actor.SessionID, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) linkRow(id int64, painID *int64, dstKind *string, dstID *int64, dstRef *string) error {
	if s.reader != nil {
		return s.reader.linkRow(id, painID, dstKind, dstID, dstRef)
	}
	return s.db.QueryRow(`SELECT src_id,dst_kind,dst_id,dst_ref FROM entity_links WHERE id=? AND src_kind='pain'`, id).Scan(painID, dstKind, dstID, dstRef)
}

// PainDetailView is the detail for one viewer: targets and history lines that
// point at something the viewer may not read are left out without a trace.
func (a *ProjectAccess) PainDetailView(id int64) (pain.Detail, error) {
	p, err := a.st.PainByID(id)
	if err != nil {
		a.Filtered()
		return pain.Detail{}, err
	}
	if err := a.CheckPain(p, ActRead); err != nil {
		return pain.Detail{}, err
	}
	return a.st.painDetail(a, id)
}

func (s *Store) painDetail(pa *ProjectAccess, id int64) (pain.Detail, error) {
	p, err := s.PainByID(id)
	if err != nil {
		return pain.Detail{}, err
	}
	d := pain.Detail{Pain: p, Motivates: []pain.Link{}, Events: []pain.Event{}}
	links, events, err := s.painLinksAndEvents(id)
	if err != nil {
		return d, err
	}
	visible := map[string]bool{}
	for _, l := range links {
		key := targetData(resolvedTarget{Kind: l.Kind, ID: l.RequestID + l.KnowledgeID, Ref: l.ExternalRef})
		switch l.Kind {
		case "request":
			title, err := pa.resolveLinkTarget(p.Project, "request", l.RequestID)
			if err != nil {
				continue
			}
			l.Title = title
		case "knowledge":
			title, err := pa.resolveLinkTarget(p.Project, "knowledge", l.KnowledgeID)
			if err != nil {
				continue
			}
			l.Title = title
		}
		visible[key] = true
		d.Motivates = append(d.Motivates, l)
	}
	for _, e := range events {
		if e.Kind == "link_added" || e.Kind == "link_removed" {
			if !pa.linkDataVisible(p.Project, e.Data, visible) {
				continue
			}
		}
		if e.EvidenceKind == "request" || e.EvidenceKind == "decision" {
			if _, err := pa.checkEvidenceRef(p.Project, pain.Evidence{Kind: e.EvidenceKind, Ref: e.EvidenceRef}); err != nil {
				e.EvidenceKind, e.EvidenceRef = "", ""
			}
		}
		if e.EvidenceKind == "session" && !pa.sessionEvidenceVisible(p.Project, e.EvidenceRef) {
			e.EvidenceKind, e.EvidenceRef = "", ""
		}
		d.Events = append(d.Events, e)
	}
	return d, nil
}

// sessionEvidenceVisible: a session reference names a transcript. Request
// evidence withholds it from viewers who may not read it; so does a pain. A
// numeric reference must be a session the viewer can read, a public address is
// shown to those who see session numbers at all.
func (a *ProjectAccess) sessionEvidenceVisible(project, ref string) bool {
	if !a.SeesSessionNumbers(project) {
		return false
	}
	canon, ok := canonicalSessionRef(ref)
	if !ok {
		return false
	}
	_, ok = rewriteSessionRef(canon, func(id int64) (string, bool) {
		sess, err := a.st.SessionByID(id)
		if err != nil {
			// A session that no longer exists holds nothing back (as for requests).
			return "", true
		}
		return "", a.CanSeeTranscript(sess)
	})
	return ok
}

// canonicalSessionRef brings every spelling of a session reference (#n, n,
// session:n, session:n#seq) to the form requests use, session:n[#seq]. Anything
// else is not a session reference this store can resolve.
func canonicalSessionRef(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if sessionRefPattern.MatchString(ref) {
		return ref, true
	}
	if n, ok := pain.ParseNumberRef(ref, ""); ok {
		return "session:" + strconv.FormatInt(n, 10), true
	}
	return "", false
}

// linkDataVisible says whether the target named in a history line is one the
// viewer may read. External text is always readable with the pain.
func (a *ProjectAccess) linkDataVisible(project, data string, current map[string]bool) bool {
	kind, rest, _ := strings.Cut(data, ":")
	switch kind {
	case "external":
		return true
	case "request", "knowledge":
		var n int64
		fmt.Sscanf(rest, "%d", &n)
		_, err := a.resolveLinkTarget(project, kind, n)
		return err == nil
	}
	return false
}

func (s *Store) painLinksAndEvents(id int64) ([]pain.Link, []pain.Event, error) {
	if s.reader != nil {
		return s.reader.painLinksAndEvents(id)
	}
	lrows, err := s.db.Query(`SELECT id,dst_kind,dst_id,dst_ref FROM entity_links WHERE src_kind='pain' AND src_id=? ORDER BY id`, id)
	if err != nil {
		return nil, nil, err
	}
	var links []pain.Link
	for lrows.Next() {
		var l pain.Link
		var dst int64
		if err := lrows.Scan(&l.ID, &l.Kind, &dst, &l.ExternalRef); err != nil {
			lrows.Close()
			return nil, nil, err
		}
		switch l.Kind {
		case "request":
			l.RequestID = dst
		case "knowledge":
			l.KnowledgeID = dst
		}
		links = append(links, l)
	}
	lrows.Close()
	erows, err := s.db.Query(`SELECT id,kind,from_state,to_state,remaining,evidence_kind,evidence_ref,note,data,person,created_at FROM pain_events WHERE pain_id=? ORDER BY id`, id)
	if err != nil {
		return nil, nil, err
	}
	defer erows.Close()
	var events []pain.Event
	for erows.Next() {
		var e pain.Event
		if err := erows.Scan(&e.ID, &e.Kind, &e.FromState, &e.ToState, &e.Remaining, &e.EvidenceKind, &e.EvidenceRef, &e.Note, &e.Data, &e.Person, &e.CreatedAt); err != nil {
			return nil, nil, err
		}
		events = append(events, e)
	}
	return links, events, erows.Err()
}
