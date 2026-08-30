package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
)

type snapshotQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type boundedPayloadBuffer struct {
	buffer bytes.Buffer
	limit  int64
	err    error
}

func newBoundedPayloadBuffer(limit int64, limitErr error) *boundedPayloadBuffer {
	return &boundedPayloadBuffer{limit: limit, err: limitErr}
}

func (b *boundedPayloadBuffer) Write(p []byte) (int, error) {
	if b.limit < 0 {
		return b.buffer.Write(p)
	}
	remaining := b.limit - int64(b.buffer.Len())
	if remaining <= 0 {
		return 0, b.err
	}
	if int64(len(p)) <= remaining {
		return b.buffer.Write(p)
	}
	n, _ := b.buffer.Write(p[:remaining])
	return n, b.err
}

func (b *boundedPayloadBuffer) Bytes() []byte { return b.buffer.Bytes() }
func (b *boundedPayloadBuffer) Len() int      { return b.buffer.Len() }

type snapshotCollector struct {
	limits       snapshot.Limits
	entries      []snapshot.Entry
	payloadBytes int64
}

func newSnapshotCollector(limits snapshot.Limits) *snapshotCollector {
	return &snapshotCollector{limits: limits}
}

func (c *snapshotCollector) preflight() error {
	if c.limits.MaxEntriesPerSnapshot >= 0 && int64(len(c.entries)) >= c.limits.MaxEntriesPerSnapshot {
		return &snapshot.RuleError{Code: "snapshot_limit_exceeded"}
	}
	return nil
}

func (c *snapshotCollector) add(domain, key string, encode func(io.Writer) error) error {
	if err := c.preflight(); err != nil {
		return err
	}
	if !utf8.ValidString(key) {
		return &snapshot.RuleError{Code: "snapshot_invalid_utf8"}
	}

	capacity := c.limits.MaxEntryPayloadBytes
	if c.limits.MaxSnapshotPayloadBytes >= 0 {
		if c.payloadBytes > c.limits.MaxSnapshotPayloadBytes {
			return &snapshot.RuleError{Code: "snapshot_limit_exceeded"}
		}
		remaining := c.limits.MaxSnapshotPayloadBytes - c.payloadBytes
		if capacity < 0 || remaining < capacity {
			capacity = remaining
		}
	}
	limitErr := &snapshot.RuleError{Code: "snapshot_limit_exceeded"}
	buffer := newBoundedPayloadBuffer(capacity, limitErr)
	if err := encode(buffer); err != nil {
		var ruleErr *snapshot.RuleError
		if errors.As(err, &ruleErr) {
			return ruleErr
		}
		code := "snapshot_invalid_payload"
		if strings.Contains(err.Error(), "UTF-8") {
			code = "snapshot_invalid_utf8"
		}
		return &snapshot.RuleError{Code: code}
	}
	raw := append([]byte(nil), buffer.Bytes()...)
	c.entries = append(c.entries, snapshot.Entry{Domain: domain, Key: key, Payload: raw, PayloadDigest: snapshot.EntryDigest(raw), PayloadSize: int64(len(raw))})
	c.payloadBytes += int64(len(raw))
	return nil
}

type snapshotActorV1 struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}
type snapshotEvidenceV1 struct {
	SessionRef string `json:"session_ref"`
	ChunkSeq   int64  `json:"chunk_seq"`
	Quote      string `json:"quote"`
	At         string `json:"at"`
	Harness    string `json:"harness"`
}
type knowledgePayloadV1 struct {
	ID              int64                `json:"id"`
	Type            string               `json:"type"`
	Title           string               `json:"title"`
	Body            string               `json:"body"`
	Project         string               `json:"project"`
	Branch          string               `json:"branch"`
	Machine         string               `json:"machine"`
	Confidence      string               `json:"confidence"`
	Status          string               `json:"status"`
	Origin          string               `json:"origin"`
	SupersededBy    int64                `json:"superseded_by"`
	Person          snapshotActorV1      `json:"person"`
	ConfirmedBy     snapshotActorV1      `json:"confirmed_by"`
	LastModifiedBy  snapshotActorV1      `json:"last_modified_by"`
	Harness         string               `json:"harness"`
	SessionRef      string               `json:"session_ref"`
	ObservedAt      string               `json:"observed_at"`
	RegressionState string               `json:"regression_state"`
	RegressionTest  string               `json:"regression_test"`
	ActivationPaths []string             `json:"activation_paths"`
	Evidence        []snapshotEvidenceV1 `json:"evidence"`
	CreatedAt       string               `json:"created_at"`
	UpdatedAt       string               `json:"updated_at"`
}
type ghostPayloadV1 struct {
	ID          int64           `json:"id"`
	Project     string          `json:"project"`
	Path        string          `json:"path"`
	Kind        string          `json:"kind"`
	Description string          `json:"description"`
	ContentSHA  string          `json:"content_sha"`
	GitBlob     string          `json:"git_blob"`
	LineCount   int64           `json:"line_count"`
	Person      snapshotActorV1 `json:"person"`
	Harness     string          `json:"harness"`
	SessionRef  string          `json:"session_ref"`
	DescribedAt string          `json:"described_at"`
	UpdatedAt   string          `json:"updated_at"`
}
type ghostReviewPayloadV1 struct {
	Project      string          `json:"project"`
	Path         string          `json:"path"`
	GitBlob      string          `json:"git_blob"`
	Person       snapshotActorV1 `json:"person"`
	At           string          `json:"at"`
	NothingToSay bool            `json:"nothing_to_say"`
}
type documentPayloadV1 struct {
	ID                int64           `json:"id"`
	Project           string          `json:"project"`
	Slug              string          `json:"slug"`
	Kind              string          `json:"kind"`
	Title             string          `json:"title"`
	HeadRevision      int64           `json:"head_revision"`
	Status            string          `json:"status"`
	Person            snapshotActorV1 `json:"person"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
	Body              string          `json:"body"`
	Digest            string          `json:"digest"`
	Message           string          `json:"message"`
	RevisionPerson    snapshotActorV1 `json:"revision_person"`
	RevisionCreatedAt string          `json:"revision_created_at"`
}
type requestCriterionV1 struct {
	ID          int64               `json:"id"`
	Number      int64               `json:"number"`
	Description string              `json:"description"`
	State       string              `json:"state"`
	CreatedAt   string              `json:"created_at"`
	UpdatedAt   string              `json:"updated_at"`
	Evidence    []requestEvidenceV1 `json:"evidence"`
}
type requestEvidenceV1 struct {
	ID          int64           `json:"id"`
	CriterionID int64           `json:"criterion_id,omitempty"`
	Kind        string          `json:"kind"`
	Ref         string          `json:"ref"`
	Person      snapshotActorV1 `json:"person"`
	CreatedAt   string          `json:"created_at"`
}
type requestRelationV1 struct {
	ID             int64  `json:"id"`
	OtherRequestID int64  `json:"other_request_id"`
	KnowledgeID    int64  `json:"knowledge_id"`
	Kind           string `json:"kind"`
	ExternalRef    string `json:"external_ref"`
	CreatedAt      string `json:"created_at"`
}
type requestActivityV1 struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	Person    snapshotActorV1 `json:"person"`
	Data      string          `json:"data"`
	CreatedAt string          `json:"created_at"`
}
type requestWorkV1 struct {
	ID         int64  `json:"id"`
	SessionRef string `json:"session_ref"`
	Role       string `json:"role"`
	State      string `json:"state"`
	StartedAt  string `json:"started_at"`
	EndedAt    string `json:"ended_at"`
	Summary    string `json:"summary"`
}
type requestSightingV1 struct {
	SessionRef string `json:"session_ref"`
	ChunkSeq   int64  `json:"chunk_seq"`
	Quote      string `json:"quote"`
	At         string `json:"at"`
	Harness    string `json:"harness"`
}
type requestPayloadV1 struct {
	ID          int64                `json:"id"`
	Type        string               `json:"type"`
	Title       string               `json:"title"`
	Description string               `json:"description"`
	State       string               `json:"state"`
	Priority    string               `json:"priority"`
	Project     string               `json:"project"`
	Branch      string               `json:"branch"`
	Machine     string               `json:"machine"`
	Origin      string               `json:"origin"`
	Person      snapshotActorV1      `json:"person"`
	SessionRef  string               `json:"session_ref"`
	CreatedAt   string               `json:"created_at"`
	UpdatedAt   string               `json:"updated_at"`
	Criteria    []requestCriterionV1 `json:"criteria"`
	Evidence    []requestEvidenceV1  `json:"evidence"`
	Relations   []requestRelationV1  `json:"relations"`
	Activity    []requestActivityV1  `json:"activity"`
	Sightings   []requestSightingV1  `json:"sightings"`
	Work        []requestWorkV1      `json:"work"`
}

func captureContextEntries(ctx context.Context, q snapshotQueryer, project string, schemaVersion uint32, limits snapshot.Limits) ([]snapshot.Entry, error) {
	collector := newSnapshotCollector(limits)
	captures := []func(context.Context, snapshotQueryer, string, *snapshotCollector) error{
		func(ctx context.Context, q snapshotQueryer, project string, collector *snapshotCollector) error {
			return captureDocuments(ctx, q, project, schemaVersion, collector)
		},
		captureGhostReviews,
		captureGhosts,
		captureKnowledge,
		captureRequests,
	}
	for _, capture := range captures {
		if err := capture(ctx, q, project, collector); err != nil {
			return nil, err
		}
	}
	entries := collector.entries
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Domain != entries[j].Domain {
			return entries[i].Domain < entries[j].Domain
		}
		return entries[i].Key < entries[j].Key
	})
	for i := 1; i < len(entries); i++ {
		if entries[i-1].Domain == entries[i].Domain && entries[i-1].Key == entries[i].Key {
			return nil, &snapshot.RuleError{Code: "snapshot_nondeterministic_order"}
		}
	}
	return entries, nil
}

func actor(id sql.NullInt64, label string) snapshotActorV1 {
	if label == "" {
		return snapshotActorV1{}
	}
	if id.Valid {
		return snapshotActorV1{ID: fmt.Sprintf("person:%d", id.Int64), Label: label}
	}
	return snapshotActorV1{ID: "name:" + label, Label: label}
}

func captureKnowledge(ctx context.Context, q snapshotQueryer, project string, collector *snapshotCollector) error {
	rows, err := q.QueryContext(ctx, `SELECT k.id,k.type,k.title,k.body,k.project,k.branch,k.machine,k.confidence,k.status,k.origin,k.superseded_by,k.person,p.id,k.confirmed_by,cp.id,k.last_modified_by,mp.id,k.harness,k.session_ref,k.observed_at,k.regression_state,k.regression_test,k.created_at,k.updated_at FROM knowledge k LEFT JOIN persons p ON p.name=k.person LEFT JOIN persons cp ON cp.name=k.confirmed_by LEFT JOIN persons mp ON mp.name=k.last_modified_by WHERE k.project=? ORDER BY k.id`, project)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := collector.preflight(); err != nil {
			return err
		}
		var p knowledgePayloadV1
		var pn, cn, mn sql.NullInt64
		var pl, cl, ml string
		if err := rows.Scan(&p.ID, &p.Type, &p.Title, &p.Body, &p.Project, &p.Branch, &p.Machine, &p.Confidence, &p.Status, &p.Origin, &p.SupersededBy, &pl, &pn, &cl, &cn, &ml, &mn, &p.Harness, &p.SessionRef, &p.ObservedAt, &p.RegressionState, &p.RegressionTest, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return err
		}
		p.Person, p.ConfirmedBy, p.LastModifiedBy = actor(pn, pl), actor(cn, cl), actor(mn, ml)
		pr, err := q.QueryContext(ctx, `SELECT pattern FROM instruction_activation_path WHERE knowledge_id=? ORDER BY pattern`, p.ID)
		if err != nil {
			return err
		}
		for pr.Next() {
			var v string
			if err := pr.Scan(&v); err != nil {
				pr.Close()
				return err
			}
			p.ActivationPaths = append(p.ActivationPaths, v)
		}
		if err := pr.Err(); err != nil {
			pr.Close()
			return err
		}
		pr.Close()
		if p.ActivationPaths == nil {
			p.ActivationPaths = []string{}
		}
		er, err := q.QueryContext(ctx, `SELECT e.session_id,e.chunk_seq,e.quote,COALESCE(s.started_at,''),COALESCE(s.harness,'') FROM knowledge_evidence e LEFT JOIN sessions s ON s.id=e.session_id WHERE e.knowledge_id=? ORDER BY e.session_id,e.chunk_seq`, p.ID)
		if err != nil {
			return err
		}
		for er.Next() {
			var e snapshotEvidenceV1
			var sid int64
			if err := er.Scan(&sid, &e.ChunkSeq, &e.Quote, &e.At, &e.Harness); err != nil {
				er.Close()
				return err
			}
			e.SessionRef = fmt.Sprintf("session:%d", sid)
			p.Evidence = append(p.Evidence, e)
		}
		if err := er.Err(); err != nil {
			er.Close()
			return err
		}
		er.Close()
		if p.Evidence == nil {
			p.Evidence = []snapshotEvidenceV1{}
		}
		if err := collector.add("knowledge", fmt.Sprint(p.ID), func(w io.Writer) error {
			return snapshot.WriteCanonical(w, p)
		}); err != nil {
			return err
		}
	}
	return rows.Err()
}

func captureGhosts(ctx context.Context, q snapshotQueryer, project string, collector *snapshotCollector) error {
	rows, err := q.QueryContext(ctx, `SELECT g.id,g.project,g.path,g.kind,g.description,g.content_sha,g.git_blob,g.line_count,g.person,p.id,g.harness,g.session_ref,g.described_at,g.updated_at FROM ghost_files g LEFT JOIN persons p ON p.name=g.person WHERE g.project=? ORDER BY g.path`, project)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := collector.preflight(); err != nil {
			return err
		}
		var p ghostPayloadV1
		var label string
		var id sql.NullInt64
		if err := rows.Scan(&p.ID, &p.Project, &p.Path, &p.Kind, &p.Description, &p.ContentSHA, &p.GitBlob, &p.LineCount, &label, &id, &p.Harness, &p.SessionRef, &p.DescribedAt, &p.UpdatedAt); err != nil {
			return err
		}
		p.Person = actor(id, label)
		prefix := "file/"
		if p.Kind == "dir" {
			prefix = "directory/"
		}
		if err := collector.add("ghost", prefix+p.Path, func(w io.Writer) error {
			return snapshot.WriteCanonical(w, p)
		}); err != nil {
			return err
		}
	}
	return rows.Err()
}
func captureGhostReviews(ctx context.Context, q snapshotQueryer, project string, collector *snapshotCollector) error {
	rows, err := q.QueryContext(ctx, `SELECT g.project,g.path,g.git_blob,g.person,p.id,g.at FROM ghost_reviews g LEFT JOIN persons p ON p.name=g.person WHERE g.project=? ORDER BY g.path`, project)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := collector.preflight(); err != nil {
			return err
		}
		var p ghostReviewPayloadV1
		var label string
		var id sql.NullInt64
		if err := rows.Scan(&p.Project, &p.Path, &p.GitBlob, &label, &id, &p.At); err != nil {
			return err
		}
		p.Person = actor(id, label)
		p.NothingToSay = true
		if err := collector.add("ghost-review", p.Path, func(w io.Writer) error {
			return snapshot.WriteCanonical(w, p)
		}); err != nil {
			return err
		}
	}
	return rows.Err()
}
func captureDocuments(ctx context.Context, q snapshotQueryer, project string, schemaVersion uint32, collector *snapshotCollector) error {
	rows, err := q.QueryContext(ctx, `SELECT d.id,d.project,d.slug,d.kind,d.title,d.head_revision,d.status,d.person,p.id,d.created_at,d.updated_at,r.body,r.digest,r.message,r.person,rp.id,r.created_at FROM documents d JOIN document_revisions r ON r.document_id=d.id AND r.revision=d.head_revision LEFT JOIN persons p ON p.name=d.person LEFT JOIN persons rp ON rp.name=r.person WHERE d.project=? ORDER BY d.slug`, project)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := collector.preflight(); err != nil {
			return err
		}
		var p documentPayloadV1
		var pl, rl string
		var pid, rid sql.NullInt64
		if err := rows.Scan(&p.ID, &p.Project, &p.Slug, &p.Kind, &p.Title, &p.HeadRevision, &p.Status, &pl, &pid, &p.CreatedAt, &p.UpdatedAt, &p.Body, &p.Digest, &p.Message, &rl, &rid, &p.RevisionCreatedAt); err != nil {
			return err
		}
		p.Person, p.RevisionPerson = actor(pid, pl), actor(rid, rl)
		key := p.Slug
		if schemaVersion == snapshot.SchemaVersionV2 {
			if p.ID <= 0 {
				return &snapshot.RuleError{Code: "snapshot_invalid_payload"}
			}
			key = fmt.Sprint(p.ID)
		}
		if err := collector.add("document", key, func(w io.Writer) error {
			return snapshot.WriteCanonical(w, p)
		}); err != nil {
			return err
		}
	}
	return rows.Err()
}

func captureRequests(ctx context.Context, q snapshotQueryer, project string, collector *snapshotCollector) error {
	rows, err := q.QueryContext(ctx, `SELECT r.id,r.type,r.title,r.description,r.state,r.priority,r.project,r.branch,r.machine,r.origin,r.person,p.id,r.session_ref,r.created_at,r.updated_at FROM requests r LEFT JOIN persons p ON p.name=r.person WHERE r.project=? ORDER BY r.id`, project)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := collector.preflight(); err != nil {
			return err
		}
		var p requestPayloadV1
		var label string
		var pid sql.NullInt64
		if err := rows.Scan(&p.ID, &p.Type, &p.Title, &p.Description, &p.State, &p.Priority, &p.Project, &p.Branch, &p.Machine, &p.Origin, &label, &pid, &p.SessionRef, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return err
		}
		p.Person = actor(pid, label)
		if err := captureRequestLists(ctx, q, &p); err != nil {
			return err
		}
		if err := collector.add("request", fmt.Sprint(p.ID), func(w io.Writer) error {
			return snapshot.WriteCanonical(w, p)
		}); err != nil {
			return err
		}
	}
	return rows.Err()
}

func captureRequestLists(ctx context.Context, q snapshotQueryer, p *requestPayloadV1) error {
	cr, err := q.QueryContext(ctx, `SELECT id,number,description,state,created_at,updated_at FROM request_criteria WHERE request_id=? ORDER BY number,id`, p.ID)
	if err != nil {
		return err
	}
	for cr.Next() {
		var c requestCriterionV1
		if err := cr.Scan(&c.ID, &c.Number, &c.Description, &c.State, &c.CreatedAt, &c.UpdatedAt); err != nil {
			cr.Close()
			return err
		}
		er, err := q.QueryContext(ctx, `SELECT e.id,e.criterion_id,e.kind,e.ref,e.person,pe.id,e.created_at FROM request_evidence e LEFT JOIN persons pe ON pe.name=e.person WHERE e.criterion_id=? ORDER BY e.id`, c.ID)
		if err != nil {
			cr.Close()
			return err
		}
		for er.Next() {
			var e requestEvidenceV1
			var label string
			var id sql.NullInt64
			if err := er.Scan(&e.ID, &e.CriterionID, &e.Kind, &e.Ref, &label, &id, &e.CreatedAt); err != nil {
				er.Close()
				cr.Close()
				return err
			}
			e.Person = actor(id, label)
			c.Evidence = append(c.Evidence, e)
		}
		if err := er.Err(); err != nil {
			er.Close()
			cr.Close()
			return err
		}
		er.Close()
		if c.Evidence == nil {
			c.Evidence = []requestEvidenceV1{}
		}
		p.Criteria = append(p.Criteria, c)
	}
	if err := cr.Err(); err != nil {
		cr.Close()
		return err
	}
	cr.Close()
	if p.Criteria == nil {
		p.Criteria = []requestCriterionV1{}
	}
	er, err := q.QueryContext(ctx, `SELECT e.id,e.kind,e.ref,e.person,pe.id,e.created_at FROM request_evidence e LEFT JOIN persons pe ON pe.name=e.person WHERE e.request_id=? AND e.criterion_id IS NULL ORDER BY e.id`, p.ID)
	if err != nil {
		return err
	}
	for er.Next() {
		var e requestEvidenceV1
		var label string
		var id sql.NullInt64
		if err := er.Scan(&e.ID, &e.Kind, &e.Ref, &label, &id, &e.CreatedAt); err != nil {
			er.Close()
			return err
		}
		e.Person = actor(id, label)
		p.Evidence = append(p.Evidence, e)
	}
	if err := er.Err(); err != nil {
		er.Close()
		return err
	}
	er.Close()
	if p.Evidence == nil {
		p.Evidence = []requestEvidenceV1{}
	}
	rr, err := q.QueryContext(ctx, `SELECT id,COALESCE(other_request_id,0),COALESCE(knowledge_id,0),kind,external_ref,created_at FROM request_relations WHERE request_id=? ORDER BY id`, p.ID)
	if err != nil {
		return err
	}
	for rr.Next() {
		var v requestRelationV1
		if err := rr.Scan(&v.ID, &v.OtherRequestID, &v.KnowledgeID, &v.Kind, &v.ExternalRef, &v.CreatedAt); err != nil {
			rr.Close()
			return err
		}
		p.Relations = append(p.Relations, v)
	}
	if err := rr.Err(); err != nil {
		rr.Close()
		return err
	}
	rr.Close()
	if p.Relations == nil {
		p.Relations = []requestRelationV1{}
	}
	ar, err := q.QueryContext(ctx, `SELECT a.id,a.kind,a.person,pa.id,a.data,a.created_at FROM request_activity a LEFT JOIN persons pa ON pa.name=a.person WHERE a.request_id=? ORDER BY a.id`, p.ID)
	if err != nil {
		return err
	}
	for ar.Next() {
		var v requestActivityV1
		var label string
		var id sql.NullInt64
		if err := ar.Scan(&v.ID, &v.Kind, &label, &id, &v.Data, &v.CreatedAt); err != nil {
			ar.Close()
			return err
		}
		v.Person = actor(id, label)
		p.Activity = append(p.Activity, v)
	}
	if err := ar.Err(); err != nil {
		ar.Close()
		return err
	}
	ar.Close()
	if p.Activity == nil {
		p.Activity = []requestActivityV1{}
	}
	wr, err := q.QueryContext(ctx, `SELECT w.id,w.session_id,w.role,w.state,w.started_at,w.ended_at,w.summary FROM request_work w WHERE w.request_id=? ORDER BY w.id`, p.ID)
	if err != nil {
		return err
	}
	for wr.Next() {
		var v requestWorkV1
		var sid int64
		if err := wr.Scan(&v.ID, &sid, &v.Role, &v.State, &v.StartedAt, &v.EndedAt, &v.Summary); err != nil {
			wr.Close()
			return err
		}
		v.SessionRef = fmt.Sprintf("session:%d", sid)
		p.Work = append(p.Work, v)
	}
	if err := wr.Err(); err != nil {
		wr.Close()
		return err
	}
	wr.Close()
	if p.Work == nil {
		p.Work = []requestWorkV1{}
	}
	sr, err := q.QueryContext(ctx, `SELECT s.session_id,s.chunk_seq,s.quote,COALESCE(se.started_at,''),COALESCE(se.harness,'') FROM request_sightings s LEFT JOIN sessions se ON se.id=s.session_id WHERE s.request_id=? ORDER BY se.started_at,s.session_id,s.chunk_seq`, p.ID)
	if err != nil {
		return err
	}
	for sr.Next() {
		var v requestSightingV1
		var sid int64
		if err := sr.Scan(&sid, &v.ChunkSeq, &v.Quote, &v.At, &v.Harness); err != nil {
			sr.Close()
			return err
		}
		v.SessionRef = fmt.Sprintf("session:%d", sid)
		p.Sightings = append(p.Sightings, v)
	}
	if err := sr.Err(); err != nil {
		sr.Close()
		return err
	}
	sr.Close()
	if p.Sightings == nil {
		p.Sightings = []requestSightingV1{}
	}
	return nil
}
