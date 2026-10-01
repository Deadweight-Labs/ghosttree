package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Steuerung eines Agenten durch einen Menschen: Pause und Unterbrechung
// (REQ-361, AC-1229). Ein Vorgang gilt erst als wirksam, wenn der Harness
// nachweislich reagiert hat. Der Zustand wird deshalb nicht gespeichert,
// sondern beim Lesen aus den Belegen abgeleitet.

const (
	ControlPause     = "pause"
	ControlInterrupt = "interrupt"

	ControlRequested    = "requested"
	ControlAcknowledged = "acknowledged"
	ControlEffective    = "effective"
	ControlResumed      = "resumed"

	// ControlEventAck: der Hook hat bei einem Werkzeugaufruf die Pause
	// ausgegeben. Beweist den Aufruf des Hooks, nicht die Reaktion.
	ControlEventAck = "ack"
	// ControlEventProof: das Transkript enthaelt hook_stopped_continuation fuer
	// denselben Werkzeugaufruf.
	ControlEventProof = "proof"
)

// InterruptGap ist die benannte Luecke, die jede Unterbrechung begleitet.
const InterruptGap = "pause at the next tool boundary; a running call is not aborted, and nothing stops while the model streams without a tool call"

// maxControlEvents begrenzt die Belege je Vorgang.
const maxControlEvents = 500

var (
	ErrControlForbidden = errors.New("only a project lead or the agent's account owner may control it, from an interactive web session")
	ErrControlNotFound  = errors.New("agent or control not found")
	ErrControlActive    = errors.New("another control is already active for this agent")
)

// ControlEvent ist ein Beleg zu einem Vorgang.
type ControlEvent struct {
	ID         int64  `json:"id,omitempty"`
	ControlID  int64  `json:"control_id,omitempty"`
	Kind       string `json:"kind"`
	ToolUseID  string `json:"tool_use_id"`
	ToolName   string `json:"tool_name,omitempty"`
	AgentID    string `json:"agent_id,omitempty"` // Subagent, wenn der Aufruf aus einem kam
	SessionID  string `json:"session_id,omitempty"`
	RecordedAt string `json:"recorded_at,omitempty"`
}

// AgentControl ist ein Vorgang samt abgeleitetem Zustand.
type AgentControl struct {
	ID               int64          `json:"id"`
	Agent            string         `json:"agent"`
	Project          string         `json:"project,omitempty"`
	Action           string         `json:"action"`
	Reason           string         `json:"reason,omitempty"`
	State            string         `json:"state"`
	RequestedBy      string         `json:"requested_by"`
	RequestedByLabel string         `json:"requested_by_label"`
	RequestedAt      string         `json:"requested_at"`
	AckedAt          string         `json:"acked_at,omitempty"`
	EffectiveAt      string         `json:"effective_at,omitempty"`
	ResumedBy        string         `json:"resumed_by,omitempty"`
	ResumedByLabel   string         `json:"resumed_by_label,omitempty"`
	ResumedAt        string         `json:"resumed_at,omitempty"`
	Gap              string         `json:"gap,omitempty"`
	Events           []ControlEvent `json:"events,omitempty"`
}

func ensureAgentControl(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS agent_controls(
  id INTEGER PRIMARY KEY,
  agent TEXT NOT NULL,
  project TEXT NOT NULL DEFAULT '',
  action TEXT NOT NULL CHECK(action IN ('pause','interrupt')),
  reason TEXT NOT NULL DEFAULT '',
  requested_by TEXT NOT NULL,
  requested_by_label TEXT NOT NULL DEFAULT '',
  via TEXT NOT NULL DEFAULT '',
  requested_at TEXT NOT NULL,
  resumed_by TEXT NOT NULL DEFAULT '',
  resumed_by_label TEXT NOT NULL DEFAULT '',
  resumed_at TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS agent_controls_agent ON agent_controls(agent, id);
CREATE TABLE IF NOT EXISTS agent_control_events(
  id INTEGER PRIMARY KEY,
  control_id INTEGER NOT NULL REFERENCES agent_controls(id),
  kind TEXT NOT NULL CHECK(kind IN ('ack','proof')),
  tool_use_id TEXT NOT NULL,
  tool_name TEXT NOT NULL DEFAULT '',
  agent_id TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '',
  recorded_at TEXT NOT NULL,
  UNIQUE(control_id, kind, tool_use_id));
CREATE TRIGGER IF NOT EXISTS agent_control_events_no_update BEFORE UPDATE ON agent_control_events
  BEGIN SELECT RAISE(ABORT, 'agent_control_events is append-only'); END;
CREATE TRIGGER IF NOT EXISTS agent_control_events_no_delete BEFORE DELETE ON agent_control_events
  BEGIN SELECT RAISE(ABORT, 'agent_control_events is append-only'); END;`)
	return err
}

// mayControlTx: Mensch in einer Web-Sitzung UND (Besitzer des Agentenkontos
// ODER mindestens lead im Projekt des Agenten). Der Store prueft das selbst,
// damit kein Aufrufer die Pruefung vergessen kann.
func mayControlTx(q rowQuerier, actor Principal, agent string) (project string, ok bool) {
	project = agentProjectTx(q, agent)
	if actor.TokenKind != WebSessionKind {
		return project, false
	}
	id, err := parsePersonPrincipalID(actor.ID)
	if err != nil {
		return project, false
	}
	if _, account, registered := agentAccountTx(q, agent); registered && account != 0 && account == id {
		return project, true
	}
	if project != "" && RoleRank(projectRoleTx(q, project, id).Role) >= RoleRank(RoleLead) {
		return project, true
	}
	return project, false
}

func agentRegisteredTx(q rowQuerier, agent string) bool {
	var one int
	return q.QueryRow(`SELECT 1 FROM coord_agents WHERE external_id=?`, agent).Scan(&one) == nil
}

// MayControlAgent sagt der Oberflaeche, ob sie die Schaltflaechen zeigt.
func (s *Store) MayControlAgent(actor Principal, agent string) bool {
	if s.reader != nil {
		return s.reader.MayControlAgent(actor, agent)
	}
	if !agentRegisteredTx(s.db, agent) {
		return false
	}
	_, ok := mayControlTx(s.db, actor, agent)
	return ok
}

// RequestAgentControl legt einen Vorgang an. Ist fuer den Agenten schon einer
// aktiv, kommt bei gleicher Aktion derselbe zurueck, bei anderer ErrControlActive.
func (s *Store) RequestAgentControl(actor Principal, agent, action, reason, via string) (AgentControl, error) {
	if s.writer != nil {
		return queueValue(s, []any{actor, agent, action, reason, via}, func(d *Store, p []any) (AgentControl, error) {
			return d.RequestAgentControl(p[0].(Principal), p[1].(string), p[2].(string), p[3].(string), p[4].(string))
		})
	}
	if action != ControlPause && action != ControlInterrupt {
		return AgentControl{}, fmt.Errorf("%w: action must be pause or interrupt", ErrInvalidInput)
	}
	agent = strings.TrimSpace(agent)
	reason = strings.TrimSpace(reason)
	if len(reason) > 500 {
		return AgentControl{}, fmt.Errorf("%w: reason is limited to 500 characters", ErrInvalidInput)
	}
	if !agentRegisteredTx(s.db, agent) {
		return AgentControl{}, ErrControlNotFound
	}
	tx, err := s.db.Begin()
	if err != nil {
		return AgentControl{}, err
	}
	defer tx.Rollback()
	project, ok := mayControlTx(tx, actor, agent)
	if !ok {
		return AgentControl{}, ErrControlForbidden
	}
	if active, found, err := activeControlTx(tx, agent); err != nil {
		return AgentControl{}, err
	} else if found {
		if active.Action != action {
			return AgentControl{}, ErrControlActive
		}
		return active, nil
	}
	res, err := tx.Exec(`INSERT INTO agent_controls(agent,project,action,reason,requested_by,requested_by_label,via,requested_at)
		VALUES(?,?,?,?,?,?,?,?)`, agent, project, action, reason, actor.ID, actor.Label, via, now())
	if err != nil {
		return AgentControl{}, err
	}
	id, _ := res.LastInsertId()
	c, err := loadControlTx(tx, id)
	if err != nil {
		return AgentControl{}, err
	}
	return c, tx.Commit()
}

// ResumeAgentControl hebt den aktiven Vorgang auf. Die Zeile bleibt als Audit.
func (s *Store) ResumeAgentControl(actor Principal, agent string) (AgentControl, error) {
	if s.writer != nil {
		return queueValue(s, []any{actor, agent}, func(d *Store, p []any) (AgentControl, error) {
			return d.ResumeAgentControl(p[0].(Principal), p[1].(string))
		})
	}
	agent = strings.TrimSpace(agent)
	if !agentRegisteredTx(s.db, agent) {
		return AgentControl{}, ErrControlNotFound
	}
	tx, err := s.db.Begin()
	if err != nil {
		return AgentControl{}, err
	}
	defer tx.Rollback()
	if _, ok := mayControlTx(tx, actor, agent); !ok {
		return AgentControl{}, ErrControlForbidden
	}
	active, found, err := activeControlTx(tx, agent)
	if err != nil {
		return AgentControl{}, err
	}
	if !found {
		return AgentControl{}, ErrControlNotFound
	}
	if _, err := tx.Exec(`UPDATE agent_controls SET resumed_by=?, resumed_by_label=?, resumed_at=? WHERE id=?`,
		actor.ID, actor.Label, now(), active.ID); err != nil {
		return AgentControl{}, err
	}
	c, err := loadControlTx(tx, active.ID)
	if err != nil {
		return AgentControl{}, err
	}
	return c, tx.Commit()
}

// RecordControlEvent nimmt einen Beleg an, nur vom Besitzer des Agentenkontos
// (die Belege kommen von dessen Rechner). Gibt false zurueck, wenn der Beleg
// schon bekannt oder das Limit erreicht ist.
func (s *Store) RecordControlEvent(actorPrincipal string, controlID int64, ev ControlEvent) (bool, error) {
	if s.writer != nil {
		return queueValue(s, []any{actorPrincipal, controlID, ev}, func(d *Store, p []any) (bool, error) {
			return d.RecordControlEvent(p[0].(string), p[1].(int64), p[2].(ControlEvent))
		})
	}
	if ev.Kind != ControlEventAck && ev.Kind != ControlEventProof {
		return false, fmt.Errorf("%w: kind must be ack or proof", ErrInvalidInput)
	}
	ev.ToolUseID = strings.TrimSpace(ev.ToolUseID)
	if ev.ToolUseID == "" || len(ev.ToolUseID) > 200 || len(ev.ToolName) > 200 || len(ev.AgentID) > 200 || len(ev.SessionID) > 200 {
		return false, fmt.Errorf("%w: tool_use_id is required and fields are limited to 200 characters", ErrInvalidInput)
	}
	var agent string
	if err := s.db.QueryRow(`SELECT agent FROM agent_controls WHERE id=?`, controlID).Scan(&agent); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrControlNotFound
		}
		return false, err
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if _, account, registered := agentAccountTx(s.db, agent); err != nil || !registered || account != actor {
		return false, ErrControlForbidden
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM agent_control_events WHERE control_id=?`, controlID).Scan(&n); err != nil {
		return false, err
	}
	if n >= maxControlEvents {
		return false, nil
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO agent_control_events(control_id,kind,tool_use_id,tool_name,agent_id,session_id,recorded_at)
		VALUES(?,?,?,?,?,?,?)`, controlID, ev.Kind, ev.ToolUseID, ev.ToolName, ev.AgentID, ev.SessionID, now())
	if err != nil {
		return false, err
	}
	rows, _ := res.RowsAffected()
	return rows > 0, nil
}

// ActiveAgentControl ist der nicht aufgehobene Vorgang des Agenten.
func (s *Store) ActiveAgentControl(agent string) (AgentControl, bool, error) {
	if s.reader != nil {
		return s.reader.ActiveAgentControl(agent)
	}
	return activeControlTx(s.db, agent)
}

// LatestAgentControl ist der juengste Vorgang, auch ein aufgehobener.
func (s *Store) LatestAgentControl(agent string) (AgentControl, bool, error) {
	if s.reader != nil {
		return s.reader.LatestAgentControl(agent)
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM agent_controls WHERE agent=? ORDER BY id DESC LIMIT 1`, agent).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentControl{}, false, nil
	}
	if err != nil {
		return AgentControl{}, false, err
	}
	c, err := loadControlTx(s.db, id)
	return c, err == nil, err
}

// AgentControlHistory listet die Vorgaenge, neueste zuerst.
func (s *Store) AgentControlHistory(agent string, limit int) ([]AgentControl, error) {
	if s.reader != nil {
		return s.reader.AgentControlHistory(agent, limit)
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT id FROM agent_controls WHERE agent=? ORDER BY id DESC LIMIT ?`, agent, limit)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := make([]AgentControl, 0, len(ids))
	for _, id := range ids {
		c, err := loadControlTx(s.db, id)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

type controlQuerier interface {
	rowQuerier
	Query(query string, args ...any) (*sql.Rows, error)
}

func activeControlTx(q controlQuerier, agent string) (AgentControl, bool, error) {
	var id int64
	err := q.QueryRow(`SELECT id FROM agent_controls WHERE agent=? AND resumed_at='' ORDER BY id DESC LIMIT 1`, agent).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentControl{}, false, nil
	}
	if err != nil {
		return AgentControl{}, false, err
	}
	c, err := loadControlTx(q, id)
	return c, err == nil, err
}

func loadControlTx(q controlQuerier, id int64) (AgentControl, error) {
	var c AgentControl
	err := q.QueryRow(`SELECT id,agent,project,action,reason,requested_by,requested_by_label,requested_at,resumed_by,resumed_by_label,resumed_at
		FROM agent_controls WHERE id=?`, id).Scan(&c.ID, &c.Agent, &c.Project, &c.Action, &c.Reason, &c.RequestedBy,
		&c.RequestedByLabel, &c.RequestedAt, &c.ResumedBy, &c.ResumedByLabel, &c.ResumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentControl{}, ErrControlNotFound
	}
	if err != nil {
		return AgentControl{}, err
	}
	rows, err := q.Query(`SELECT id,control_id,kind,tool_use_id,tool_name,agent_id,session_id,recorded_at
		FROM agent_control_events WHERE control_id=? ORDER BY id`, id)
	if err != nil {
		return AgentControl{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var e ControlEvent
		if err := rows.Scan(&e.ID, &e.ControlID, &e.Kind, &e.ToolUseID, &e.ToolName, &e.AgentID, &e.SessionID, &e.RecordedAt); err != nil {
			return AgentControl{}, err
		}
		c.Events = append(c.Events, e)
	}
	if err := rows.Err(); err != nil {
		return AgentControl{}, err
	}
	c.deriveState()
	return c, nil
}

// deriveState leitet Zustand und Zeitpunkte aus den Belegen ab. Wirksam ist ein
// Vorgang erst, wenn zu EINEM Werkzeugaufruf sowohl ein Hook-Ack als auch ein
// Transkript-Beleg vorliegt; die Reihenfolge des Eintreffens spielt keine Rolle.
func (c *AgentControl) deriveState() {
	acks := map[string]string{}
	proofs := map[string]string{}
	for _, e := range c.Events {
		switch e.Kind {
		case ControlEventAck:
			acks[e.ToolUseID] = e.RecordedAt
			if c.AckedAt == "" || e.RecordedAt < c.AckedAt {
				c.AckedAt = e.RecordedAt
			}
		case ControlEventProof:
			proofs[e.ToolUseID] = e.RecordedAt
		}
	}
	for id, a := range acks {
		if p, ok := proofs[id]; ok {
			at := a
			if p > at {
				at = p
			}
			if c.EffectiveAt == "" || at < c.EffectiveAt {
				c.EffectiveAt = at
			}
		}
	}
	switch {
	case c.ResumedAt != "":
		c.State = ControlResumed
	case c.EffectiveAt != "":
		c.State = ControlEffective
	case c.AckedAt != "":
		c.State = ControlAcknowledged
	default:
		c.State = ControlRequested
	}
	if c.Action == ControlInterrupt {
		c.Gap = InterruptGap
	}
}
