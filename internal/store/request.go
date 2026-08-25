package store

import (
	"database/sql"
	"fmt"
	"strconv"
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
	tx, err := s.db.Begin()
	if err != nil {
		return requestdomain.Detail{}, err
	}
	defer tx.Rollback()
	if in.IdempotencyKey != "" {
		var id int64
		err := tx.QueryRow(`SELECT id FROM requests WHERE idempotency_key=?`, in.IdempotencyKey).Scan(&id)
		if err == nil {
			tx.Rollback()
			return s.RequestByID(id)
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
	if err := tx.Commit(); err != nil {
		return requestdomain.Detail{}, err
	}
	return s.RequestByID(id)
}

func (s *Store) RequestByID(id int64) (requestdomain.Detail, error) {
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
	activityRows, err := s.db.Query(`SELECT id,request_id,kind,person,data,created_at FROM request_activity WHERE request_id=? ORDER BY id`, id)
	if err != nil {
		return d, err
	}
	defer activityRows.Close()
	for activityRows.Next() {
		var a requestdomain.Activity
		if err := activityRows.Scan(&a.ID, &a.RequestID, &a.Kind, &a.Person, &a.Data, &a.CreatedAt); err != nil {
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

func (s *Store) SearchRequests(filter requestdomain.SearchFilter) (requestdomain.SearchPage, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 25 {
		limit = 25
	}
	// substr, not the full column: a search page is a list, and a list shows a
	// snippet. Sending every description in full made twenty-four requests
	// exceed what a tool result may return, and it is no better for a card or a
	// terminal row. GetRequest answers with the whole text.
	description := "substr(r.description,1," + strconv.Itoa(snippetChars) + ")"
	if filter.FullDescription {
		description = "r.description"
	}
	query := `SELECT r.id,r.type,r.title,` + description + `,r.state,r.priority,r.project,r.branch,r.machine,r.origin,r.person,r.session_ref,r.created_at,r.updated_at,
		(SELECT COUNT(*) FROM request_criteria c WHERE c.request_id=r.id AND c.state='open'),
		COALESCE((SELECT w.summary FROM request_work w WHERE w.request_id=r.id AND w.summary!='' ORDER BY w.id DESC LIMIT 1),''),
		(SELECT COUNT(DISTINCT g.session_id) FROM request_sightings g WHERE g.request_id=r.id)
		FROM requests r`
	var where []string
	var args []any
	// A query of nothing but ordinary words asks to see the backlog, not to
	// find one entry in it. Matching it would answer "was ist noch zu tun" with
	// whichever request happens to contain all five words — measured on
	// 2026-08-24: one of twenty-four, effectively at random.
	if ClassifySearch(filter.Query) == SearchFullText {
		query += ` JOIN search_documents d ON d.kind='request' AND d.domain_id=r.id JOIN search_documents_fts f ON f.rowid=d.id`
		where = append(where, `search_documents_fts MATCH ?`)
		args = append(args, ftsQuery(filter.Query))
	}
	// An unset axis on a request means "applies everywhere along it", so a
	// caller naming a branch or machine must still see the project-wide
	// entries. Matching exactly would hide most of the backlog.
	for _, axis := range []struct{ col, v string }{
		{"project", filter.Scope.Project}, {"branch", filter.Scope.Branch}, {"machine", filter.Scope.Machine},
	} {
		if axis.v != "" {
			where = append(where, `(r.`+axis.col+`='' OR r.`+axis.col+`=?)`)
			args = append(args, axis.v)
		}
	}
	if filter.State != "" {
		where = append(where, `r.state=?`)
		args = append(args, filter.State)
	}
	if filter.Type != "" {
		where = append(where, `r.type=?`)
		args = append(args, filter.Type)
	}
	if filter.Cursor != "" {
		where = append(where, `r.id<?`)
		args = append(args, filter.Cursor)
	}
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY r.id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return requestdomain.SearchPage{}, err
	}
	defer rows.Close()
	var page requestdomain.SearchPage
	for rows.Next() {
		var hit requestdomain.SearchHit
		r := &hit.Request
		if err := rows.Scan(&r.ID, &r.Type, &r.Title, &r.Description, &r.State, &r.Priority, &r.Scope.Project, &r.Scope.Branch, &r.Scope.Machine, &r.Origin, &r.Person, &r.SessionRef, &r.CreatedAt, &r.UpdatedAt, &hit.OpenCriteria, &hit.LatestHandoff, &hit.Sightings); err != nil {
			return page, err
		}
		if filter.Query != "" {
			hit.MatchReason = "full-text match"
		}
		page.Results = append(page.Results, hit)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Results) > limit {
		page.NextCursor = fmt.Sprintf("%d", page.Results[limit-1].Request.ID)
		page.Results = page.Results[:limit]
	}
	return page, nil
}

func (s *Store) CountOpenRequests(ax scope.Axes) (int, error) {
	where, args := ax.UnionWhere()
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM requests WHERE state='open' AND `+where, args...).Scan(&count)
	return count, err
}

func (s *Store) AddCriterion(requestID int64, description, person string) (requestdomain.Criterion, error) {
	if strings.TrimSpace(description) == "" {
		return requestdomain.Criterion{}, requestdomain.NewRuleError("criterion_required", "acceptance criterion cannot be empty", "describe an observable outcome", nil)
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
	if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at) VALUES(?,'relation.added',?,?,?)`, requestID, person, relation.Kind, ts); err != nil {
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
	if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at) VALUES(?,'work.started',?,?,?)`, requestID, person, fmt.Sprintf("session:%d role:%s", sessionID, role), ts); err != nil {
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
	if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at) VALUES(?,'work.resumed',?,?,?)`,
		work.RequestID, person, fmt.Sprintf("session:%d role:%s from:%s", work.SessionID, work.Role, work.State), ts); err != nil {
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
	if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at) VALUES(?,'work.finished',?,?,?)`, current.RequestID, person, summary, ts); err != nil {
		return requestdomain.Work{}, err
	}
	if err := tx.Commit(); err != nil {
		return requestdomain.Work{}, err
	}
	current.State, current.EndedAt, current.Summary = state, ts, summary
	return current, nil
}

func (s *Store) SearchRequestSessions(requestID int64, query string, limit int, cursor string) (requestdomain.SessionPage, error) {
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
