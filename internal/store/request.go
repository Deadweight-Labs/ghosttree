package store

import (
	"database/sql"
	"fmt"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func (s *Store) CreateRequest(in requestdomain.CreateInput) (requestdomain.Detail, error) {
	r := in.Request
	if err := requestdomain.ValidateType(r.Type); err != nil {
		return requestdomain.Detail{}, err
	}
	if strings.TrimSpace(r.Title) == "" {
		return requestdomain.Detail{}, requestdomain.NewRuleError("title_required", "request title is required", "provide a concise outcome-oriented title", nil)
	}
	if r.Origin == "" {
		r.Origin = "agent"
	}
	if s.writer != nil {
		return queueValue(s, []any{in}, func(d *Store, p []any) (requestdomain.Detail, error) {
			return d.CreateRequest(p[0].(requestdomain.CreateInput))
		})
	}

	tx, err := s.db.Begin()
	if err != nil {
		return requestdomain.Detail{}, err
	}
	defer tx.Rollback()
	author := authorForWriteTx(tx, in.AuthorID, r.Person, r.Origin, r.SessionRef)
	if in.IdempotencyKey != "" {
		var id int64
		var project, person string
		err := tx.QueryRow(`SELECT id, project, person FROM requests WHERE idempotency_key=?`, in.IdempotencyKey).Scan(&id, &project, &person)
		if err == nil {
			// The key is unique across the instance, so a hit in another project or
			// by another author is a collision, never a replay: nothing of that
			// request is returned and the answer does not say where it lives.
			var existing int64
			_ = tx.QueryRow(`SELECT account_id FROM request_authors WHERE request_id=?`, id).Scan(&existing)
			if project != r.Scope.Project || person != r.Person || existing != author {
				return requestdomain.Detail{}, requestdomain.NewRuleError("idempotency_key_conflict", "this idempotency key is already in use", "choose a different idempotency key", nil)
			}
			tx.Rollback()
			d, err := s.RequestByID(id)
			d.Replayed = true
			return d, err
		}
		if err != sql.ErrNoRows {
			return requestdomain.Detail{}, err
		}
	}
	ts := now()
	res, err := tx.Exec(`INSERT INTO requests(type,title,description,state,priority,project,branch,machine,origin,person,session_ref,idempotency_key,created_at,updated_at)
		VALUES(?,?,?,'open',?,?,?,?,?,?,?,?,?,?)`, r.Type, r.Title, r.Description, r.Priority,
		r.Scope.Project, r.Scope.Branch, r.Scope.Machine, r.Origin, r.Person, r.SessionRef, in.IdempotencyKey, ts, ts)
	if err != nil {
		return requestdomain.Detail{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return requestdomain.Detail{}, err
	}
	for i, description := range in.Criteria {
		if strings.TrimSpace(description) == "" {
			return requestdomain.Detail{}, requestdomain.NewRuleError("criterion_required", "acceptance criterion cannot be empty", "remove the empty criterion or describe an observable outcome", nil)
		}
		if _, err := tx.Exec(`INSERT INTO request_criteria(request_id,number,description,state,created_at,updated_at) VALUES(?,?,?,'open',?,?)`, id, i+1, description, ts, ts); err != nil {
			return requestdomain.Detail{}, err
		}
	}
	if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at) VALUES(?,'request.created',?,?,?)`, id, r.Person, r.Title, ts); err != nil {
		return requestdomain.Detail{}, err
	}
	if _, err := tx.Exec(`INSERT INTO search_documents(kind,domain_id,title,body,project,branch,machine) VALUES('request',?,?,?,?,?,?)`, id, r.Title, r.Description, r.Scope.Project, r.Scope.Branch, r.Scope.Machine); err != nil {
		return requestdomain.Detail{}, err
	}
	if err := recordRequestAuthorTx(tx, id, author); err != nil {
		return requestdomain.Detail{}, err
	}
	if err := tx.Commit(); err != nil {
		return requestdomain.Detail{}, err
	}
	return s.RequestByID(id)
}

func (s *Store) RequestByID(id int64) (requestdomain.Detail, error) {
	if s.reader != nil {
		return s.reader.RequestByID(id)
	}
	var d requestdomain.Detail
	r := &d.Request
	err := s.db.QueryRow(`SELECT id,type,title,description,state,priority,project,branch,machine,origin,person,session_ref,idempotency_key,created_at,updated_at FROM requests WHERE id=?`, id).Scan(
		&r.ID, &r.Type, &r.Title, &r.Description, &r.State, &r.Priority, &r.Scope.Project, &r.Scope.Branch, &r.Scope.Machine,
		&r.Origin, &r.Person, &r.SessionRef, &r.IdempotencyKey, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return d, err
	}
	rows, err := s.db.Query(`SELECT id,request_id,number,description,state,created_at,updated_at FROM request_criteria WHERE request_id=? ORDER BY number`, id)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var c requestdomain.Criterion
		if err := rows.Scan(&c.ID, &c.RequestID, &c.Number, &c.Description, &c.State, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return d, err
		}
		d.Criteria = append(d.Criteria, c)
	}
	if err := rows.Err(); err != nil {
		return d, err
	}
	for i := range d.Criteria {
		evidenceRows, err := s.db.Query(`SELECT id,request_id,criterion_id,kind,ref,person,created_at FROM request_evidence WHERE criterion_id=? ORDER BY id`, d.Criteria[i].ID)
		if err != nil {
			return d, err
		}
		for evidenceRows.Next() {
			var e requestdomain.Evidence
			if err := evidenceRows.Scan(&e.ID, &e.RequestID, &e.CriterionID, &e.Kind, &e.Ref, &e.Person, &e.CreatedAt); err != nil {
				evidenceRows.Close()
				return d, err
			}
			d.Criteria[i].Evidence = append(d.Criteria[i].Evidence, e)
		}
		if err := evidenceRows.Close(); err != nil {
			return d, err
		}
	}
	activityRows, err := s.db.Query(`SELECT id,request_id,kind,person,data,created_at,session_id FROM request_activity WHERE request_id=? ORDER BY id`, id)
	if err != nil {
		return d, err
	}
	defer activityRows.Close()
	for activityRows.Next() {
		var a requestdomain.Activity
		if err := activityRows.Scan(&a.ID, &a.RequestID, &a.Kind, &a.Person, &a.Data, &a.CreatedAt, &a.SessionID); err != nil {
			return d, err
		}
		d.Activity = append(d.Activity, a)
	}
	if err := activityRows.Err(); err != nil {
		return d, err
	}
	relationRows, err := s.db.Query(`SELECT id,request_id,COALESCE(other_request_id,0),COALESCE(knowledge_id,0),kind,external_ref,created_at FROM request_relations WHERE request_id=? ORDER BY id`, id)
	if err != nil {
		return d, err
	}
	defer relationRows.Close()
	for relationRows.Next() {
		var relation requestdomain.Relation
		if err := relationRows.Scan(&relation.ID, &relation.RequestID, &relation.OtherRequestID, &relation.KnowledgeID, &relation.Kind, &relation.ExternalRef, &relation.CreatedAt); err != nil {
			return d, err
		}
		d.Relations = append(d.Relations, relation)
	}
	if err := relationRows.Err(); err != nil {
		return d, err
	}
	workRows, err := s.db.Query(`SELECT id,request_id,session_id,role,state,started_at,ended_at,summary FROM request_work WHERE request_id=? ORDER BY id DESC`, id)
	if err != nil {
		return d, err
	}
	defer workRows.Close()
	for workRows.Next() {
		var work requestdomain.Work
		if err := workRows.Scan(&work.ID, &work.RequestID, &work.SessionID, &work.Role, &work.State, &work.StartedAt, &work.EndedAt, &work.Summary); err != nil {
			return d, err
		}
		d.Work = append(d.Work, work)
	}
	if err := workRows.Err(); err != nil {
		return d, err
	}
	// Der Wortlaut zuletzt, und über einen Join, weil die Sichtung selbst keinen
	// Zeitstempel trägt: sie erbt ihn von der Sitzung, in der sie fiel. Ohne
	// Sitzung und Zeitpunkt wäre das Zitat nicht nachschlagbar, und genau das
	// Nachschlagen ist sein Zweck.
	sightingRows, err := s.db.Query(`SELECT rs.session_id, rs.chunk_seq, rs.quote,
			COALESCE(se.started_at,''), COALESCE(se.harness,'')
		FROM request_sightings rs LEFT JOIN sessions se ON se.id=rs.session_id
		WHERE rs.request_id=? ORDER BY se.started_at, rs.session_id, rs.chunk_seq`, id)
	if err != nil {
		return d, err
	}
	defer sightingRows.Close()
	for sightingRows.Next() {
		var sighting requestdomain.Sighting
		if err := sightingRows.Scan(&sighting.SessionID, &sighting.ChunkSeq, &sighting.Quote,
			&sighting.At, &sighting.Harness); err != nil {
			return d, err
		}
		d.Sightings = append(d.Sightings, sighting)
	}
	if err := sightingRows.Err(); err != nil {
		return d, err
	}
	return d, nil
}

// snippetChars bounds the description a search hit carries. Enough to tell two
// requests apart, short enough that a page of them stays readable.
const snippetChars = 200

// requestScopeWhere is the part of a request query that says which requests
// the caller may see at all: the scope axes and the visibility restriction.
func requestScopeWhere(filter requestdomain.SearchFilter) ([]string, []any) {
	var where []string
	var args []any
	// An unset axis on a request means "applies everywhere along it", so a
	// caller naming a branch or machine must still see the project-wide
	// entries. Matching exactly would hide most of the backlog.
	//
	// The project axis is the exception: a request without a project belongs to
	// nobody's backlog, so naming a project returns that project's requests only.
	if filter.Scope.Project != "" {
		where = append(where, `r.project=?`)
		args = append(args, filter.Scope.Project)
	}
	for _, axis := range []struct{ col, v string }{
		{"branch", filter.Scope.Branch}, {"machine", filter.Scope.Machine},
	} {
		if axis.v != "" {
			where = append(where, `(r.`+axis.col+`='' OR r.`+axis.col+`=?)`)
			args = append(args, axis.v)
		}
	}
	if filter.Restrict {
		var clauses []string
		// Aufträge ohne Projekt gehören ihrem Autor (Konto-Id).
		own, gargs := ownRequestSQL("r", filter.GlobalAccount, filter.GlobalOwner)
		clauses = append(clauses, `(r.project='' AND `+own+`)`)
		args = append(args, gargs...)
		if len(filter.Projects) > 0 {
			clauses = append(clauses, `r.project IN (`+placeholders(len(filter.Projects))+`)`)
			for _, p := range filter.Projects {
				args = append(args, p)
			}
		}
		clause := strings.Join(clauses, " OR ")
		if clause == "" {
			clause = "0"
		}
		switch {
		case filter.UnclaimedAll:
			clause += ` OR (r.project!='' AND r.project NOT IN (SELECT remote FROM projects))`
		case filter.UnclaimedAuthor != "":
			clause += ` OR (r.person=? AND r.project!='' AND r.project NOT IN (SELECT remote FROM projects))`
			args = append(args, filter.UnclaimedAuthor)
		}
		where = append(where, `(`+clause+`)`)
	}
	if filter.HideHeld {
		where = append(where, `NOT (r.origin='distilled' AND r.person='' AND EXISTS (
			SELECT 1 FROM sessions hs WHERE 'session:'||hs.id = substr(r.session_ref,1,length('session:'||hs.id))
			  AND (length(r.session_ref)=length('session:'||hs.id) OR substr(r.session_ref,length('session:'||hs.id)+1,1)='#')
			  AND hs.shared=0 AND COALESCE(hs.visibility,'') IN ('','private')
			  AND (CASE WHEN hs.account_id=0 THEN ? ELSE hs.account_id END) != ?))`)
		args = append(args, filter.HeldInstanceOwner, filter.HeldViewer)
	}
	return where, args
}

func (s *Store) CountOpenRequests(ax scope.Axes) (int, error) {
	if s.reader != nil {
		return s.reader.CountOpenRequests(ax)
	}
	return s.CountOpenRequestsFor(SessionViewer{}, ax)
}

// CountOpenRequestsFor zählt ohne die gehaltenen Aufträge aus privaten Sessions
// anderer Konten: die Zahl verriete sie sonst (REQ-438 D).
func (s *Store) CountOpenRequestsFor(v SessionViewer, ax scope.Axes) (int, error) {
	if s.reader != nil {
		return s.reader.CountOpenRequestsFor(v, ax)
	}
	where, args := ax.RequestUnionWhere()
	q := `SELECT COUNT(*) FROM requests WHERE state='open' AND ` + where
	if v.Enforced {
		q += ` AND NOT (origin='distilled' AND person='' AND EXISTS (
			SELECT 1 FROM sessions hs WHERE (session_ref='session:'||hs.id OR session_ref LIKE 'session:'||hs.id||'#%')
			  AND hs.shared=0 AND COALESCE(hs.visibility,'') IN ('','private')
			  AND (CASE WHEN hs.account_id=0 THEN ? ELSE hs.account_id END) != ?))`
		args = append(args, v.Owner, v.Account)
		// A request without a project counts only for its author, the instance
		// owner included for those without an owner.
		own, oargs := ownRequestSQL("requests", v.Account, v.isInstanceOwner())
		q += ` AND (project!='' OR ` + own + `)`
		args = append(args, oargs...)
	}
	var count int
	err := s.db.QueryRow(q, args...).Scan(&count)
	return count, err
}

func (s *Store) AddCriterion(requestID int64, description, person string) (requestdomain.Criterion, error) {
	if strings.TrimSpace(description) == "" {
		return requestdomain.Criterion{}, requestdomain.NewRuleError("criterion_required", "acceptance criterion cannot be empty", "describe an observable outcome", nil)
	}
	if s.writer != nil {
		return queueValue(s, []any{requestID, description, person}, func(d *Store, p []any) (requestdomain.Criterion, error) {
			return d.AddCriterion(p[0].(int64), p[1].(string), p[2].(string))
		})
	}

	tx, err := s.db.Begin()
	if err != nil {
		return requestdomain.Criterion{}, err
	}
	defer tx.Rollback()
	var number int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(number),0)+1 FROM request_criteria WHERE request_id=?`, requestID).Scan(&number); err != nil {
		return requestdomain.Criterion{}, err
	}
	var requestState string
	if err := tx.QueryRow(`SELECT state FROM requests WHERE id=?`, requestID).Scan(&requestState); err != nil {
		return requestdomain.Criterion{}, err
	}
	if requestState != "open" {
		return requestdomain.Criterion{}, terminalRequestError(requestID, requestState)
	}
	ts := now()
	res, err := tx.Exec(`INSERT INTO request_criteria(request_id,number,description,state,created_at,updated_at) VALUES(?,?,?,'open',?,?)`, requestID, number, description, ts, ts)
	if err != nil {
		return requestdomain.Criterion{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return requestdomain.Criterion{}, err
	}
	if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at) VALUES(?,'criterion.added',?,?,?)`, requestID, person, description, ts); err != nil {
		return requestdomain.Criterion{}, err
	}
	if err := tx.Commit(); err != nil {
		return requestdomain.Criterion{}, err
	}
	return requestdomain.Criterion{ID: id, RequestID: requestID, Number: number, Description: description, State: "open", CreatedAt: ts, UpdatedAt: ts}, nil
}

func (s *Store) SetCriterionState(id int64, state string, evidence requestdomain.Evidence) error {
	if state != "met" && state != "waived" {
		return requestdomain.NewRuleError("invalid_criterion_state", "criterion state must be met or waived", "choose met or waived", nil)
	}
	if err := requestdomain.ValidateEvidence(evidence); err != nil {
		return err
	}
	if s.writer != nil {
		return queueWrite(s, []any{id, state, evidence}, func(d *Store, p []any) error {
			return d.SetCriterionState(p[0].(int64), p[1].(string), p[2].(requestdomain.Evidence))
		})
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var requestID int64
	var requestState, criterionState string
	if err := tx.QueryRow(`SELECT c.request_id,r.state,c.state FROM request_criteria c JOIN requests r ON r.id=c.request_id WHERE c.id=?`, id).Scan(&requestID, &requestState, &criterionState); err != nil {
		return err
	}
	if requestState != "open" {
		return terminalRequestError(requestID, requestState)
	}
	if criterionState != "open" {
		return requestdomain.NewRuleError("criterion_terminal", "acceptance criterion is already resolved", "do not record the same transition twice", nil)
	}
	ts := now()
	if _, err := tx.Exec(`UPDATE request_criteria SET state=?,updated_at=? WHERE id=?`, state, ts, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO request_evidence(request_id,criterion_id,kind,ref,person,created_at) VALUES(?,?,?,?,?,?)`, requestID, id, evidence.Kind, evidence.Ref, evidence.Person, ts); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at) VALUES(?,?,?,?,?)`, requestID, "criterion."+state, evidence.Person, fmt.Sprintf("AC-%d", id), ts)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CompleteRequest(id int64, evidence requestdomain.Evidence) error {
	if err := requestdomain.ValidateEvidence(evidence); err != nil {
		return err
	}
	if s.writer != nil {
		return queueWrite(s, []any{id, evidence}, func(d *Store, p []any) error { return d.CompleteRequest(p[0].(int64), p[1].(requestdomain.Evidence)) })
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var requestState string
	if err := tx.QueryRow(`SELECT state FROM requests WHERE id=?`, id).Scan(&requestState); err != nil {
		return err
	}
	if requestState != "open" {
		return terminalRequestError(id, requestState)
	}
	rows, err := tx.Query(`SELECT number FROM request_criteria WHERE request_id=? AND state='open' ORDER BY number`, id)
	if err != nil {
		return err
	}
	var open []string
	for rows.Next() {
		var number int
		if err := rows.Scan(&number); err != nil {
			rows.Close()
			return err
		}
		open = append(open, fmt.Sprintf("AC-%d.%d", id, number))
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(open) > 0 {
		return requestdomain.NewRuleError("open_criteria", fmt.Sprintf("request REQ-%d cannot be completed", id), "satisfy or waive the remaining criteria", open)
	}
	ts := now()
	res, err := tx.Exec(`UPDATE requests SET state='done',updated_at=? WHERE id=? AND state='open' AND NOT EXISTS (SELECT 1 FROM request_criteria WHERE request_id=? AND state='open')`, ts, id, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return requestdomain.NewRuleError("request_not_completable", fmt.Sprintf("request REQ-%d changed while completing", id), "reload the request and resolve its current criteria", nil)
	}
	if _, err := tx.Exec(`INSERT INTO request_evidence(request_id,kind,ref,person,created_at) VALUES(?,?,?,?,?)`, id, evidence.Kind, evidence.Ref, evidence.Person, ts); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at) VALUES(?,'request.done',?,?,?)`, id, evidence.Person, evidence.Ref, ts); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DropRequest(id int64, reason, person string) error {
	if strings.TrimSpace(reason) == "" {
		return requestdomain.NewRuleError("reason_required", "dropping a request requires a reason", "explain why the requested outcome is no longer wanted", nil)
	}
	if s.writer != nil {
		return queueWrite(s, []any{id, reason, person}, func(d *Store, p []any) error { return d.DropRequest(p[0].(int64), p[1].(string), p[2].(string)) })
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.Exec(`UPDATE requests SET state='dropped',updated_at=? WHERE id=? AND state='open'`, ts, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		var state string
		if scanErr := tx.QueryRow(`SELECT state FROM requests WHERE id=?`, id).Scan(&state); scanErr != nil {
			return scanErr
		}
		return terminalRequestError(id, state)
	}
	if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at) VALUES(?,'request.dropped',?,?,?)`, id, person, reason, ts); err != nil {
		return err
	}
	return tx.Commit()
}

func terminalRequestError(id int64, state string) error {
	return requestdomain.NewRuleError("request_terminal", fmt.Sprintf("request REQ-%d is already %s", id, state), "terminal requests cannot be changed; create or reopen a separate request", nil)
}

// relationAddedData ist der Text des Eintrags "relation.added": die Art und das
// Ziel ("related REQ-4", "knowledge knowledge #7"); eine externe Verknüpfung
// steht als "<Art> ext <Verweis>".
func relationAddedData(r requestdomain.Relation) string {
	switch {
	case r.OtherRequestID != 0:
		return fmt.Sprintf("%s REQ-%d", r.Kind, r.OtherRequestID)
	case r.KnowledgeID != 0:
		return fmt.Sprintf("%s knowledge #%d", r.Kind, r.KnowledgeID)
	}
	return r.Kind + " ext " + r.ExternalRef
}

func (s *Store) AddRequestRelation(requestID int64, relation requestdomain.Relation, person string) (requestdomain.Relation, error) {
	valid := map[string]bool{"parent": true, "related": true, "blocks": true, "duplicates": true, "supersedes": true, "knowledge": true, "external": true}
	if !valid[relation.Kind] {
		return requestdomain.Relation{}, requestdomain.NewRuleError("invalid_relation", "unsupported request relation", "use parent, related, blocks, duplicates, supersedes, knowledge, or external", nil)
	}
	targets := 0
	if relation.OtherRequestID != 0 {
		targets++
	}
	if relation.KnowledgeID != 0 {
		targets++
	}
	if relation.ExternalRef != "" {
		targets++
	}
	if targets != 1 {
		return requestdomain.Relation{}, requestdomain.NewRuleError("relation_target_required", "relation requires exactly one target", "provide one request, knowledge, or external reference", nil)
	}
	var other, knowledge any
	if relation.OtherRequestID != 0 {
		other = relation.OtherRequestID
	}
	if relation.KnowledgeID != 0 {
		knowledge = relation.KnowledgeID
	}
	if s.writer != nil {
		return queueValue(s, []any{requestID, relation, person}, func(d *Store, p []any) (requestdomain.Relation, error) {
			return d.AddRequestRelation(p[0].(int64), p[1].(requestdomain.Relation), p[2].(string))
		})
	}

	tx, err := s.db.Begin()
	if err != nil {
		return requestdomain.Relation{}, err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.Exec(`INSERT INTO request_relations(request_id,other_request_id,knowledge_id,kind,external_ref,created_at) VALUES(?,?,?,?,?,?)`, requestID, other, knowledge, relation.Kind, relation.ExternalRef, ts)
	if err != nil {
		return requestdomain.Relation{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return requestdomain.Relation{}, err
	}
	if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at) VALUES(?,'relation.added',?,?,?)`, requestID, person, relationAddedData(relation), ts); err != nil {
		return requestdomain.Relation{}, err
	}
	if err := tx.Commit(); err != nil {
		return requestdomain.Relation{}, err
	}
	relation.ID, relation.RequestID, relation.CreatedAt = id, requestID, ts
	return relation, nil
}

func (s *Store) StartRequestWork(requestID, sessionID int64, role, person string) (requestdomain.Work, []string, error) {
	if role != "primary" && role != "related" {
		return requestdomain.Work{}, nil, requestdomain.NewRuleError("invalid_work_role", "work role must be primary or related", "choose primary or related", nil)
	}
	if s.writer != nil {
		type result struct {
			work     requestdomain.Work
			warnings []string
		}
		out, err := queueValue(s, []any{requestID, sessionID, role, person}, func(d *Store, p []any) (result, error) {
			work, warnings, err := d.StartRequestWork(p[0].(int64), p[1].(int64), p[2].(string), p[3].(string))
			return result{work: work, warnings: warnings}, err
		})
		return out.work, out.warnings, err
	}

	var existing requestdomain.Work
	err := s.db.QueryRow(`SELECT id,request_id,session_id,role,state,started_at,ended_at,summary FROM request_work WHERE request_id=? AND session_id=? AND role=?`, requestID, sessionID, role).Scan(
		&existing.ID, &existing.RequestID, &existing.SessionID, &existing.Role, &existing.State, &existing.StartedAt, &existing.EndedAt, &existing.Summary)
	if err == nil {
		if existing.State == "active" {
			return existing, nil, nil
		}
		// Resuming is what picking the request up again means. Leaving the row
		// finished would show a session with no active work while it works.
		return s.resumeRequestWork(existing, person)
	}
	if err != sql.ErrNoRows {
		return requestdomain.Work{}, nil, err
	}
	if role == "primary" {
		var currentRequest int64
		// Only active work blocks. A finished primary that still held the slot
		// forced every later task in the same session into role=related, which
		// quietly falsified every reading of what a session mainly worked on.
		err := s.db.QueryRow(`SELECT request_id FROM request_work
			WHERE session_id=? AND role='primary' AND state='active' LIMIT 1`, sessionID).Scan(&currentRequest)
		if err == nil {
			return requestdomain.Work{}, nil, requestdomain.NewRuleError("primary_exists", fmt.Sprintf("session is already working on REQ-%d", currentRequest), "finish or abandon that work before starting another", []string{fmt.Sprintf("REQ-%d", currentRequest)})
		}
		if err != sql.ErrNoRows {
			return requestdomain.Work{}, nil, err
		}
	}
	var warnings []string
	if role == "primary" {
		var active int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM request_work WHERE request_id=? AND role='primary' AND state='active'`, requestID).Scan(&active); err != nil {
			return requestdomain.Work{}, nil, err
		}
		if active > 0 {
			warnings = append(warnings, fmt.Sprintf("REQ-%d already has %d active primary session(s)", requestID, active))
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return requestdomain.Work{}, nil, err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.Exec(`INSERT INTO request_work(request_id,session_id,role,state,started_at) VALUES(?,?,?,'active',?)`, requestID, sessionID, role, ts)
	if err != nil {
		return requestdomain.Work{}, nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return requestdomain.Work{}, nil, err
	}
	if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at,session_id) VALUES(?,'work.started',?,?,?,?)`, requestID, person, fmt.Sprintf("session:%d role:%s", sessionID, role), ts, sessionID); err != nil {
		return requestdomain.Work{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return requestdomain.Work{}, nil, err
	}
	return requestdomain.Work{ID: id, RequestID: requestID, SessionID: sessionID, Role: role, State: "active", StartedAt: ts}, warnings, nil
}

// resumeRequestWork puts a finished association back to active. The handoff
// summary is kept: it describes the stretch that ended, and overwriting it
// would erase the one record of why the work was put down.
func (s *Store) resumeRequestWork(work requestdomain.Work, person string) (requestdomain.Work, []string, error) {
	if work.Role == "primary" {
		var blocking int64
		err := s.db.QueryRow(`SELECT request_id FROM request_work
			WHERE session_id=? AND role='primary' AND state='active' AND id<>? LIMIT 1`, work.SessionID, work.ID).Scan(&blocking)
		if err == nil {
			return requestdomain.Work{}, nil, requestdomain.NewRuleError("primary_exists", fmt.Sprintf("session is already working on REQ-%d", blocking), "finish or abandon that work before resuming another", []string{fmt.Sprintf("REQ-%d", blocking)})
		}
		if err != sql.ErrNoRows {
			return requestdomain.Work{}, nil, err
		}
	}
	ts := now()
	tx, err := s.db.Begin()
	if err != nil {
		return requestdomain.Work{}, nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE request_work SET state='active', ended_at='' WHERE id=?`, work.ID); err != nil {
		return requestdomain.Work{}, nil, err
	}
	if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at,session_id) VALUES(?,'work.resumed',?,?,?,?)`,
		work.RequestID, person, fmt.Sprintf("session:%d role:%s from:%s", work.SessionID, work.Role, work.State), ts, work.SessionID); err != nil {
		return requestdomain.Work{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return requestdomain.Work{}, nil, err
	}
	work.State, work.EndedAt = "active", ""
	return work, nil, nil
}

func (s *Store) FinishRequestWork(workID int64, state, summary, person string) (requestdomain.Work, error) {
	if state != "paused" && state != "completed" && state != "abandoned" {
		return requestdomain.Work{}, requestdomain.NewRuleError("invalid_work_state", "finished work state must be paused, completed, or abandoned", "choose paused, completed, or abandoned", nil)
	}
	if strings.TrimSpace(summary) == "" {
		return requestdomain.Work{}, requestdomain.NewRuleError("summary_required", "finishing work requires a handoff summary", "record what changed, what remains, and the next useful step", nil)
	}
	if s.writer != nil {
		return queueValue(s, []any{workID, state, summary, person}, func(d *Store, p []any) (requestdomain.Work, error) {
			return d.FinishRequestWork(p[0].(int64), p[1].(string), p[2].(string), p[3].(string))
		})
	}

	var current requestdomain.Work
	err := s.db.QueryRow(`SELECT id,request_id,session_id,role,state,started_at,ended_at,summary FROM request_work WHERE id=?`, workID).Scan(
		&current.ID, &current.RequestID, &current.SessionID, &current.Role, &current.State, &current.StartedAt, &current.EndedAt, &current.Summary)
	if err != nil {
		return requestdomain.Work{}, err
	}
	if current.State == state && current.Summary == summary {
		return current, nil
	}
	if current.State != "active" {
		return requestdomain.Work{}, requestdomain.NewRuleError("work_not_active", "only active work can be finished", "start a new work association to continue this request", nil)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return requestdomain.Work{}, err
	}
	defer tx.Rollback()
	ts := now()
	if _, err := tx.Exec(`UPDATE request_work SET state=?,ended_at=?,summary=? WHERE id=?`, state, ts, summary, workID); err != nil {
		return requestdomain.Work{}, err
	}
	if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at,session_id) VALUES(?,'work.finished',?,?,?,?)`, current.RequestID, person, summary, ts, current.SessionID); err != nil {
		return requestdomain.Work{}, err
	}
	if err := tx.Commit(); err != nil {
		return requestdomain.Work{}, err
	}
	current.State, current.EndedAt, current.Summary = state, ts, summary
	return current, nil
}

func (s *Store) SearchRequestSessions(requestID int64, query string, limit int, cursor string) (requestdomain.SessionPage, error) {
	if s.reader != nil {
		return s.reader.SearchRequestSessions(requestID, query, limit, cursor)
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	var cursorSession, cursorSeq int64
	if cursor != "" {
		if _, err := fmt.Sscanf(cursor, "%d:%d", &cursorSession, &cursorSeq); err != nil {
			return requestdomain.SessionPage{}, requestdomain.NewRuleError("invalid_cursor", "session search cursor is invalid", "use the opaque cursor returned by the previous page", nil)
		}
	}
	rows, err := s.db.Query(`SELECT se.id,c.seq,se.harness,w.state,w.summary,
		snippet(chunks_fts,0,'','','…',12)
		FROM chunks_fts f
		JOIN session_chunks c ON c.id=f.rowid
		JOIN sessions se ON se.id=c.session_id
		JOIN request_work w ON w.session_id=se.id AND w.request_id=?
		WHERE chunks_fts MATCH ? AND (?=0 OR se.id<? OR (se.id=? AND c.seq<?))
		ORDER BY se.id DESC,c.seq DESC LIMIT ?`, requestID, ftsQuery(query), cursorSession, cursorSession, cursorSession, cursorSeq, limit+1)
	if err != nil {
		return requestdomain.SessionPage{}, err
	}
	defer rows.Close()
	var page requestdomain.SessionPage
	for rows.Next() {
		var hit requestdomain.SessionHit
		if err := rows.Scan(&hit.SessionID, &hit.ChunkSeq, &hit.Harness, &hit.Outcome, &hit.Handoff, &hit.Snippet); err != nil {
			return page, err
		}
		page.Results = append(page.Results, hit)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Results) > limit {
		last := page.Results[limit-1]
		page.NextCursor = fmt.Sprintf("%d:%d", last.SessionID, last.ChunkSeq)
		page.Results = page.Results[:limit]
	}
	return page, nil
}
