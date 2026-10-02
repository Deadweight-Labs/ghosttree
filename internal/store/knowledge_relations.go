package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Relations between knowledge entries (REQ-157). Two kinds: supersedes
// ("from supersedes to": from is the newer parent) and sibling (undirected
// grouping). This file is only the data model and its store API. Delivery
// still reads knowledge.status and superseded_by; no read path consults
// these tables yet.

const (
	RelSupersedes = "supersedes"
	RelSibling    = "sibling"

	RelProposed = "proposed"
	RelActive   = "active"
	RelRejected = "rejected"
	RelRevoked  = "revoked"

	RelOriginHuman     = "human"
	RelOriginAgent     = "agent"
	RelOriginDistiller = "distiller"
	RelOriginMigrated  = "migrated"
)

var (
	ErrRelationInvalid  = errors.New("invalid knowledge relation")
	ErrRelationNotFound = errors.New("knowledge relation not found")
	ErrRelationExists   = errors.New("a live relation of this kind already exists between these entries")
	ErrRelationConflict = errors.New("these entries are already related in the other kind")
	ErrRelationCycle    = errors.New("supersession would create a cycle")
	ErrRelationProject  = errors.New("both entries must belong to the same project")
	ErrRelationState    = errors.New("relation is not in a state that allows this")
)

type KnowledgeRelation struct {
	ID        int64  `json:"id"`
	Project   string `json:"project"`
	FromID    int64  `json:"from_id"`
	ToID      int64  `json:"to_id"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
	Origin    string `json:"origin"`
	GroupID   int64  `json:"group_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
	DecidedBy string `json:"decided_by,omitempty"`
	DecidedAt string `json:"decided_at,omitempty"`
}

type KnowledgeGroup struct {
	ID           int64   `json:"id"`
	Project      string  `json:"project"`
	Label        string  `json:"label,omitempty"`
	Volatility   string  `json:"volatility"`
	VolatilityBy string  `json:"volatility_by,omitempty"`
	VolatilityAt string  `json:"volatility_at,omitempty"`
	CreatedAt    string  `json:"created_at"`
	Members      []int64 `json:"members"`
}

type KnowledgeRelationEvent struct {
	ID         int64  `json:"id"`
	RelationID int64  `json:"relation_id,omitempty"`
	GroupID    int64  `json:"group_id,omitempty"`
	Action     string `json:"action"`
	Actor      string `json:"actor"`
	ActorRole  string `json:"actor_role,omitempty"`
	Via        string `json:"via,omitempty"`
	Detail     string `json:"detail,omitempty"`
	CreatedAt  string `json:"created_at"`
}

// RelationActor names who caused a change; it lands in the event log.
type RelationActor struct {
	Name string
	Role string
	Via  string // mcp|cli|web|distiller|migration
}

func (a RelationActor) valid() error {
	if strings.TrimSpace(a.Name) == "" {
		return fmt.Errorf("%w: actor required", ErrRelationInvalid)
	}
	return nil
}

type RelationInput struct {
	FromID, ToID int64
	Kind         string
	State        string // "" = active; the only other accepted value is proposed
	Origin       string // "" = human; migrated is reserved for the backfill
	Reason       string
}

func validVolatility(v string) bool {
	switch v {
	case "unrated", "volatile", "slow", "timeless":
		return true
	}
	return false
}

// volatilityRank orders the merge rule: the more volatile value wins, and
// unrated only survives when both sides are unrated.
func volatilityRank(v string) int {
	switch v {
	case "volatile":
		return 3
	case "slow":
		return 2
	case "timeless":
		return 1
	}
	return 0
}

type relQuerier interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
	Exec(string, ...any) (sql.Result, error)
}

func insertRelationEvent(q relQuerier, relationID, groupID int64, action string, a RelationActor, detail string) error {
	_, err := q.Exec(`INSERT INTO knowledge_relation_events(relation_id,group_id,action,actor,actor_role,via,detail,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		nullInt(relationID), nullInt(groupID), action, a.Name, a.Role, a.Via, detail, now())
	return err
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// AddRelation creates a relation. Active relations take effect at once (a
// sibling edge joins or merges groups); proposed ones wait for DecideRelation.
func (s *Store) AddRelation(in RelationInput, actor RelationActor) (KnowledgeRelation, error) {
	if s.writer != nil {
		return queueValue(s, []any{in, actor}, func(d *Store, p []any) (KnowledgeRelation, error) {
			return d.AddRelation(p[0].(RelationInput), p[1].(RelationActor))
		})
	}
	if err := actor.valid(); err != nil {
		return KnowledgeRelation{}, err
	}
	if in.Origin == "" {
		in.Origin = RelOriginHuman
	}
	if in.Origin == RelOriginMigrated {
		return KnowledgeRelation{}, fmt.Errorf("%w: origin migrated is reserved", ErrRelationInvalid)
	}
	switch in.Origin {
	case RelOriginHuman, RelOriginAgent, RelOriginDistiller:
	default:
		return KnowledgeRelation{}, fmt.Errorf("%w: origin %q", ErrRelationInvalid, in.Origin)
	}
	if in.State == "" {
		in.State = RelActive
	}
	if in.State != RelActive && in.State != RelProposed {
		return KnowledgeRelation{}, fmt.Errorf("%w: initial state %q", ErrRelationInvalid, in.State)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return KnowledgeRelation{}, err
	}
	defer tx.Rollback()
	rel, err := addRelationTx(tx, in, actor)
	if err != nil {
		return KnowledgeRelation{}, err
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeRelation{}, err
	}
	return rel, nil
}

func addRelationTx(tx *sql.Tx, in RelationInput, actor RelationActor) (KnowledgeRelation, error) {
	if in.Kind != RelSupersedes && in.Kind != RelSibling {
		return KnowledgeRelation{}, fmt.Errorf("%w: kind %q", ErrRelationInvalid, in.Kind)
	}
	if in.FromID == in.ToID || in.FromID <= 0 || in.ToID <= 0 {
		return KnowledgeRelation{}, fmt.Errorf("%w: an entry cannot be related to itself", ErrRelationInvalid)
	}
	if in.Kind == RelSupersedes && strings.TrimSpace(in.Reason) == "" {
		return KnowledgeRelation{}, fmt.Errorf("%w: a reason is required for supersedes", ErrRelationInvalid)
	}
	if in.Kind == RelSibling && in.FromID > in.ToID {
		in.FromID, in.ToID = in.ToID, in.FromID
	}
	project, fromStatus, err := relationEnd(tx, in.FromID)
	if err != nil {
		return KnowledgeRelation{}, err
	}
	toProject, _, err := relationEnd(tx, in.ToID)
	if err != nil {
		return KnowledgeRelation{}, err
	}
	if project != toProject {
		return KnowledgeRelation{}, ErrRelationProject
	}
	if in.Kind == RelSupersedes && (fromStatus == "archived" || fromStatus == "deprecated") {
		return KnowledgeRelation{}, fmt.Errorf("%w: a %s entry cannot supersede another", ErrRelationInvalid, fromStatus)
	}
	if err := checkRelationFree(tx, in); err != nil {
		return KnowledgeRelation{}, err
	}
	ts := now()
	decidedBy, decidedAt := "", ""
	if in.State == RelActive {
		decidedBy, decidedAt = actor.Name, ts
	}
	res, err := tx.Exec(`INSERT INTO knowledge_relations(project,from_id,to_id,kind,state,origin,reason,created_by,created_at,decided_by,decided_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		project, in.FromID, in.ToID, in.Kind, in.State, in.Origin, in.Reason, actor.Name, ts, decidedBy, decidedAt)
	if err != nil {
		return KnowledgeRelation{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return KnowledgeRelation{}, err
	}
	action := "activated"
	if in.State == RelProposed {
		action = "proposed"
	}
	if err := insertRelationEvent(tx, id, 0, action, actor, in.Reason); err != nil {
		return KnowledgeRelation{}, err
	}
	if in.State == RelActive && in.Kind == RelSibling {
		if err := joinGroupTx(tx, id, project, in.FromID, in.ToID, actor); err != nil {
			return KnowledgeRelation{}, err
		}
	}
	return relationByID(tx, id)
}

func relationEnd(q relQuerier, id int64) (project, status string, err error) {
	err = q.QueryRow(`SELECT project,status FROM knowledge WHERE id=?`, id).Scan(&project, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("%w: knowledge entry %d does not exist", ErrRelationInvalid, id)
	}
	return project, status, err
}

// checkRelationFree enforces the pair rules and, for supersedes, acyclicity
// over the live (proposed and active) edges.
func checkRelationFree(q relQuerier, in RelationInput) error {
	var n int
	if err := q.QueryRow(`SELECT COUNT(*) FROM knowledge_relations WHERE kind=? AND from_id=? AND to_id=? AND state IN ('proposed','active')`,
		in.Kind, in.FromID, in.ToID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrRelationExists
	}
	other := RelSibling
	if in.Kind == RelSibling {
		other = RelSupersedes
	}
	if err := q.QueryRow(`SELECT COUNT(*) FROM knowledge_relations WHERE kind=? AND state IN ('proposed','active')
		AND ((from_id=? AND to_id=?) OR (from_id=? AND to_id=?))`, other, in.FromID, in.ToID, in.ToID, in.FromID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrRelationConflict
	}
	if in.Kind != RelSupersedes {
		return nil
	}
	// The new edge is from -> to. It closes a cycle if from is reachable
	// from to through live supersedes edges.
	seen := map[int64]bool{in.ToID: true}
	queue := []int64{in.ToID}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		rows, err := q.Query(`SELECT to_id FROM knowledge_relations WHERE kind='supersedes' AND from_id=? AND state IN ('proposed','active')`, cur)
		if err != nil {
			return err
		}
		var next []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			next = append(next, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, id := range next {
			if id == in.FromID {
				return ErrRelationCycle
			}
			if !seen[id] {
				seen[id] = true
				queue = append(queue, id)
			}
		}
	}
	return nil
}

const relationCols = `id,project,from_id,to_id,kind,state,origin,COALESCE(group_id,0),reason,created_by,created_at,decided_by,decided_at`

func scanRelation(sc interface{ Scan(...any) error }) (KnowledgeRelation, error) {
	var r KnowledgeRelation
	err := sc.Scan(&r.ID, &r.Project, &r.FromID, &r.ToID, &r.Kind, &r.State, &r.Origin, &r.GroupID, &r.Reason, &r.CreatedBy, &r.CreatedAt, &r.DecidedBy, &r.DecidedAt)
	return r, err
}

func relationByID(q relQuerier, id int64) (KnowledgeRelation, error) {
	r, err := scanRelation(q.QueryRow(`SELECT `+relationCols+` FROM knowledge_relations WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrRelationNotFound
	}
	return r, err
}

// DecideRelation approves or rejects a proposed relation. Approval re-checks
// the pair rules, since other edges may have appeared since the proposal.
func (s *Store) DecideRelation(id int64, approve bool, actor RelationActor, reason string) (KnowledgeRelation, error) {
	if s.writer != nil {
		return queueValue(s, []any{id, approve, actor, reason}, func(d *Store, p []any) (KnowledgeRelation, error) {
			return d.DecideRelation(p[0].(int64), p[1].(bool), p[2].(RelationActor), p[3].(string))
		})
	}
	if err := actor.valid(); err != nil {
		return KnowledgeRelation{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return KnowledgeRelation{}, err
	}
	defer tx.Rollback()
	rel, err := relationByID(tx, id)
	if err != nil {
		return KnowledgeRelation{}, err
	}
	if rel.State != RelProposed {
		return KnowledgeRelation{}, ErrRelationState
	}
	ts := now()
	state, action := RelRejected, "rejected"
	if approve {
		state, action = RelActive, "activated"
		// Free the live slot while re-checking against everything else.
		if _, err := tx.Exec(`UPDATE knowledge_relations SET state='rejected' WHERE id=?`, id); err != nil {
			return KnowledgeRelation{}, err
		}
		if err := checkRelationFree(tx, RelationInput{FromID: rel.FromID, ToID: rel.ToID, Kind: rel.Kind}); err != nil {
			return KnowledgeRelation{}, err
		}
	}
	if _, err := tx.Exec(`UPDATE knowledge_relations SET state=?,decided_by=?,decided_at=? WHERE id=?`, state, actor.Name, ts, id); err != nil {
		return KnowledgeRelation{}, err
	}
	if err := insertRelationEvent(tx, id, 0, action, actor, reason); err != nil {
		return KnowledgeRelation{}, err
	}
	if approve && rel.Kind == RelSibling {
		if err := joinGroupTx(tx, id, rel.Project, rel.FromID, rel.ToID, actor); err != nil {
			return KnowledgeRelation{}, err
		}
	}
	out, err := relationByID(tx, id)
	if err != nil {
		return KnowledgeRelation{}, err
	}
	return out, tx.Commit()
}

// RevokeRelation withdraws a live relation. Nothing is deleted: the row stays
// as state=revoked so "who hid this, and why" remains answerable.
func (s *Store) RevokeRelation(id int64, actor RelationActor, reason string) (KnowledgeRelation, error) {
	if s.writer != nil {
		return queueValue(s, []any{id, actor, reason}, func(d *Store, p []any) (KnowledgeRelation, error) {
			return d.RevokeRelation(p[0].(int64), p[1].(RelationActor), p[2].(string))
		})
	}
	if err := actor.valid(); err != nil {
		return KnowledgeRelation{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return KnowledgeRelation{}, err
	}
	defer tx.Rollback()
	rel, err := relationByID(tx, id)
	if err != nil {
		return KnowledgeRelation{}, err
	}
	if rel.State != RelActive && rel.State != RelProposed {
		return KnowledgeRelation{}, ErrRelationState
	}
	if _, err := tx.Exec(`UPDATE knowledge_relations SET state='revoked',decided_by=?,decided_at=? WHERE id=?`, actor.Name, now(), id); err != nil {
		return KnowledgeRelation{}, err
	}
	if err := insertRelationEvent(tx, id, 0, "revoked", actor, reason); err != nil {
		return KnowledgeRelation{}, err
	}
	if rel.Kind == RelSibling && rel.State == RelActive && rel.GroupID != 0 {
		// A revoked edge belongs to no group, so a dissolved group can go.
		if _, err := tx.Exec(`UPDATE knowledge_relations SET group_id=NULL WHERE id=?`, id); err != nil {
			return KnowledgeRelation{}, err
		}
		if err := splitGroupTx(tx, rel.GroupID, actor); err != nil {
			return KnowledgeRelation{}, err
		}
	}
	out, err := relationByID(tx, id)
	if err != nil {
		return KnowledgeRelation{}, err
	}
	return out, tx.Commit()
}

// joinGroupTx puts the endpoints of a newly active sibling edge into one
// group: new group if neither has one, join if one has, merge if both have
// different ones. The edge itself records the group.
func joinGroupTx(tx *sql.Tx, relID int64, project string, a, b int64, actor RelationActor) error {
	ga, err := entryGroup(tx, a)
	if err != nil {
		return err
	}
	gb, err := entryGroup(tx, b)
	if err != nil {
		return err
	}
	var gid int64
	switch {
	case ga == 0 && gb == 0:
		res, err := tx.Exec(`INSERT INTO knowledge_groups(project,created_at) VALUES(?,?)`, project, now())
		if err != nil {
			return err
		}
		if gid, err = res.LastInsertId(); err != nil {
			return err
		}
	case ga == 0:
		gid = gb
	case gb == 0 || ga == gb:
		gid = ga
	default:
		gid = ga
		if gb < ga {
			gid = gb
		}
		loser := ga + gb - gid
		if err := mergeGroupsTx(tx, gid, loser, actor); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`UPDATE knowledge_relations SET group_id=? WHERE id=?`, gid, relID)
	return err
}

func entryGroup(q relQuerier, knowledgeID int64) (int64, error) {
	var g sql.NullInt64
	err := q.QueryRow(`SELECT group_id FROM knowledge_relations WHERE kind='sibling' AND state='active' AND (from_id=? OR to_id=?) LIMIT 1`, knowledgeID, knowledgeID).Scan(&g)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return g.Int64, err
}

func mergeGroupsTx(tx *sql.Tx, keep, loser int64, actor RelationActor) error {
	var kv, lv, klabel, llabel string
	if err := tx.QueryRow(`SELECT volatility,label FROM knowledge_groups WHERE id=?`, keep).Scan(&kv, &klabel); err != nil {
		return err
	}
	if err := tx.QueryRow(`SELECT volatility,label FROM knowledge_groups WHERE id=?`, loser).Scan(&lv, &llabel); err != nil {
		return err
	}
	merged := kv
	if volatilityRank(lv) > volatilityRank(kv) {
		merged = lv
	}
	label := klabel
	if label == "" {
		label = llabel
	}
	if _, err := tx.Exec(`UPDATE knowledge_relations SET group_id=? WHERE group_id=?`, keep, loser); err != nil {
		return err
	}
	if merged != kv {
		if _, err := tx.Exec(`UPDATE knowledge_groups SET volatility=?,volatility_by=?,volatility_at=? WHERE id=?`, merged, actor.Name, now(), keep); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE knowledge_groups SET label=? WHERE id=?`, label, keep); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM knowledge_groups WHERE id=?`, loser); err != nil {
		return err
	}
	return insertRelationEvent(tx, 0, keep, "group_merged", actor,
		fmt.Sprintf("merged #%d (%s) into #%d (%s), result %s", loser, lv, keep, kv, merged))
}

// splitGroupTx recomputes the connected components of a group's remaining
// active sibling edges. The component holding the smallest entry id keeps the
// group id; the others get new rows with the old volatility and label.
func splitGroupTx(tx *sql.Tx, gid int64, actor RelationActor) error {
	rows, err := tx.Query(`SELECT id,from_id,to_id FROM knowledge_relations WHERE group_id=? AND kind='sibling' AND state='active'`, gid)
	if err != nil {
		return err
	}
	type edge struct{ id, a, b int64 }
	var edges []edge
	for rows.Next() {
		var e edge
		if err := rows.Scan(&e.id, &e.a, &e.b); err != nil {
			rows.Close()
			return err
		}
		edges = append(edges, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	var project, label, vol, volBy, volAt, created string
	if err := tx.QueryRow(`SELECT project,label,volatility,volatility_by,volatility_at,created_at FROM knowledge_groups WHERE id=?`, gid).
		Scan(&project, &label, &vol, &volBy, &volAt, &created); err != nil {
		return err
	}
	if len(edges) == 0 {
		if _, err := tx.Exec(`DELETE FROM knowledge_groups WHERE id=?`, gid); err != nil {
			return err
		}
		return insertRelationEvent(tx, 0, gid, "group_split", actor, "no members left, group dissolved")
	}
	parent := map[int64]int64{}
	var find func(int64) int64
	find = func(x int64) int64 {
		if _, ok := parent[x]; !ok {
			parent[x] = x
		}
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	for _, e := range edges {
		ra, rb := find(e.a), find(e.b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	minOf := map[int64]int64{} // root -> smallest member
	for node := range parent {
		r := find(node)
		if m, ok := minOf[r]; !ok || node < m {
			minOf[r] = node
		}
	}
	if len(minOf) == 1 {
		return nil
	}
	roots := make([]int64, 0, len(minOf))
	for r := range minOf {
		roots = append(roots, r)
	}
	sort.Slice(roots, func(i, j int) bool { return minOf[roots[i]] < minOf[roots[j]] })
	newIDs := []string{}
	for _, r := range roots[1:] {
		res, err := tx.Exec(`INSERT INTO knowledge_groups(project,label,volatility,volatility_by,volatility_at,created_at) VALUES(?,?,?,?,?,?)`,
			project, label, vol, volBy, volAt, now())
		if err != nil {
			return err
		}
		ng, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for _, e := range edges {
			if find(e.a) == r {
				if _, err := tx.Exec(`UPDATE knowledge_relations SET group_id=? WHERE id=?`, ng, e.id); err != nil {
					return err
				}
			}
		}
		newIDs = append(newIDs, fmt.Sprintf("#%d", ng))
	}
	return insertRelationEvent(tx, 0, gid, "group_split", actor,
		fmt.Sprintf("kept #%d, new groups %s, volatility %s", gid, strings.Join(newIDs, ", "), vol))
}

// SetGroupVolatility sets the group's volatility. Any member may do this (the
// caller checks access); every change is an event with person and time.
func (s *Store) SetGroupVolatility(groupID int64, value string, actor RelationActor) (KnowledgeGroup, error) {
	if s.writer != nil {
		return queueValue(s, []any{groupID, value, actor}, func(d *Store, p []any) (KnowledgeGroup, error) {
			return d.SetGroupVolatility(p[0].(int64), p[1].(string), p[2].(RelationActor))
		})
	}
	if err := actor.valid(); err != nil {
		return KnowledgeGroup{}, err
	}
	if !validVolatility(value) {
		return KnowledgeGroup{}, fmt.Errorf("%w: volatility %q", ErrRelationInvalid, value)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return KnowledgeGroup{}, err
	}
	defer tx.Rollback()
	var old string
	if err := tx.QueryRow(`SELECT volatility FROM knowledge_groups WHERE id=?`, groupID).Scan(&old); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return KnowledgeGroup{}, ErrRelationNotFound
		}
		return KnowledgeGroup{}, err
	}
	if _, err := tx.Exec(`UPDATE knowledge_groups SET volatility=?,volatility_by=?,volatility_at=? WHERE id=?`, value, actor.Name, now(), groupID); err != nil {
		return KnowledgeGroup{}, err
	}
	if err := insertRelationEvent(tx, 0, groupID, "volatility_set", actor, old+" -> "+value); err != nil {
		return KnowledgeGroup{}, err
	}
	g, err := groupByID(tx, groupID)
	if err != nil {
		return KnowledgeGroup{}, err
	}
	return g, tx.Commit()
}

func groupByID(q relQuerier, id int64) (KnowledgeGroup, error) {
	var g KnowledgeGroup
	err := q.QueryRow(`SELECT id,project,label,volatility,volatility_by,volatility_at,created_at FROM knowledge_groups WHERE id=?`, id).
		Scan(&g.ID, &g.Project, &g.Label, &g.Volatility, &g.VolatilityBy, &g.VolatilityAt, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrRelationNotFound
	}
	if err != nil {
		return g, err
	}
	rows, err := q.Query(`SELECT from_id,to_id FROM knowledge_relations WHERE group_id=? AND kind='sibling' AND state='active'`, id)
	if err != nil {
		return g, err
	}
	defer rows.Close()
	set := map[int64]bool{}
	for rows.Next() {
		var a, b int64
		if err := rows.Scan(&a, &b); err != nil {
			return g, err
		}
		set[a], set[b] = true, true
	}
	if err := rows.Err(); err != nil {
		return g, err
	}
	g.Members = make([]int64, 0, len(set))
	for m := range set {
		g.Members = append(g.Members, m)
	}
	sort.Slice(g.Members, func(i, j int) bool { return g.Members[i] < g.Members[j] })
	return g, nil
}

// GroupOf returns the sibling group an entry belongs to; ok is false when it
// has none.
func (s *Store) GroupOf(knowledgeID int64) (KnowledgeGroup, bool, error) {
	if s.reader != nil {
		return s.reader.GroupOf(knowledgeID)
	}
	gid, err := entryGroup(s.db, knowledgeID)
	if err != nil || gid == 0 {
		return KnowledgeGroup{}, false, err
	}
	g, err := groupByID(s.db, gid)
	return g, err == nil, err
}

// GroupByID returns a group with its current members.
func (s *Store) GroupByID(id int64) (KnowledgeGroup, error) {
	if s.reader != nil {
		return s.reader.GroupByID(id)
	}
	return groupByID(s.db, id)
}

// RelationByID returns one relation in any state.
func (s *Store) RelationByID(id int64) (KnowledgeRelation, error) {
	if s.reader != nil {
		return s.reader.RelationByID(id)
	}
	return relationByID(s.db, id)
}

// RelationsOf lists every relation (any state) that touches the entry, oldest
// first. Callers filter by visibility; the store does not.
func (s *Store) RelationsOf(knowledgeID int64) ([]KnowledgeRelation, error) {
	if s.reader != nil {
		return s.reader.RelationsOf(knowledgeID)
	}
	rows, err := s.db.Query(`SELECT `+relationCols+` FROM knowledge_relations WHERE from_id=? OR to_id=? ORDER BY id`, knowledgeID, knowledgeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KnowledgeRelation
	for rows.Next() {
		r, err := scanRelation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RelationEvents lists the events of one relation, or of a group when
// relationID is 0, oldest first.
func (s *Store) RelationEvents(relationID, groupID int64) ([]KnowledgeRelationEvent, error) {
	if s.reader != nil {
		return s.reader.RelationEvents(relationID, groupID)
	}
	rows, err := s.db.Query(`SELECT id,COALESCE(relation_id,0),COALESCE(group_id,0),action,actor,actor_role,via,detail,created_at
		FROM knowledge_relation_events WHERE (?<>0 AND relation_id=?) OR (?<>0 AND group_id=?) ORDER BY id`,
		relationID, relationID, groupID, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KnowledgeRelationEvent
	for rows.Next() {
		var e KnowledgeRelationEvent
		if err := rows.Scan(&e.ID, &e.RelationID, &e.GroupID, &e.Action, &e.Actor, &e.ActorRole, &e.Via, &e.Detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// BackfillKnowledgeRelations turns every legacy superseded_by value into an
// active supersedes edge (origin=migrated). It runs on every open, in one
// transaction, and is idempotent: a pair that already has an edge in any
// state is skipped, so a later revoke is not undone by the next start. It
// does not touch knowledge.status or superseded_by, which delivery still
// reads, so nothing about behaviour changes. Rows whose parent is missing,
// self-referencing or in another project are skipped and counted.
func BackfillKnowledgeRelations(db *sql.DB) (skipped int, err error) {
	has, err := knowledgeHasColumn(db, "superseded_by")
	if err != nil || !has {
		return 0, err
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT k.id, k.superseded_by, k.project, p.project IS NULL OR p.project<>k.project,
			COALESCE(NULLIF(k.last_modified_by,''),NULLIF(k.person,''),'migration'), k.updated_at
		FROM knowledge k LEFT JOIN knowledge p ON p.id=k.superseded_by
		WHERE k.superseded_by>0
		  AND NOT EXISTS(SELECT 1 FROM knowledge_relations r WHERE r.kind='supersedes' AND r.from_id=k.superseded_by AND r.to_id=k.id)
		ORDER BY k.id`)
	if err != nil {
		return 0, err
	}
	type legacy struct {
		id, parent int64
		project    string
		bad        bool
		by, at     string
	}
	var todo []legacy
	for rows.Next() {
		var l legacy
		if err := rows.Scan(&l.id, &l.parent, &l.project, &l.bad, &l.by, &l.at); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, l)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	actor := RelationActor{Name: "migration", Via: "migration"}
	for _, l := range todo {
		if l.bad || l.parent == l.id {
			skipped++
			continue
		}
		ts := l.at
		res, err := tx.Exec(`INSERT INTO knowledge_relations(project,from_id,to_id,kind,state,origin,reason,created_by,created_at,decided_by,decided_at) VALUES(?,?,?,'supersedes','active','migrated','migrated from superseded_by',?,?,'migration',?)`,
			l.project, l.parent, l.id, l.by, ts, now())
		if err != nil {
			return 0, err
		}
		rid, err := res.LastInsertId()
		if err != nil {
			return 0, err
		}
		if err := insertRelationEvent(tx, rid, 0, "activated", actor, "backfill from superseded_by"); err != nil {
			return 0, err
		}
	}
	return skipped, tx.Commit()
}

func knowledgeHasColumn(db *sql.DB, name string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(knowledge)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var col, typ string
		var def sql.NullString
		if err := rows.Scan(&cid, &col, &typ, &notNull, &def, &pk); err != nil {
			return false, err
		}
		if col == name {
			return true, nil
		}
	}
	return false, rows.Err()
}
