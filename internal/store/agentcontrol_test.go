package store

import (
	"errors"
	"strings"
	"testing"
)

const controlAgent = "claude:h:pausable"

func web(id, label string) Principal {
	return Principal{ID: id, Label: label, TokenKind: WebSessionKind}
}

// controlFixture: robin (person:1) ist Org-Owner und damit Projekt-Owner; anna
// (person:2) hat eine Mitgliedsrolle, ben (person:3) ist Lead, cleo (person:4)
// besitzt den Agenten ohne Projektrolle ausser member. Der Agent gehoert cleo.
func controlFixture(t *testing.T) *Store {
	t.Helper()
	st := roleFixture(t)
	if err := setRole(st, "person:1", "person:2", RoleMember, false); err != nil {
		t.Fatal(err)
	}
	if err := setRole(st, "person:1", "person:3", RoleLead, false); err != nil {
		t.Fatal(err)
	}
	if err := setRole(st, "person:1", "person:4", RoleMember, false); err != nil {
		t.Fatal(err)
	}
	registerRoleAgent(t, st, controlAgent, "person:4", RoomKeyForProject(roleProject), "member")
	return st
}

func TestAgentControlStatesAreDerivedFromEvidence(t *testing.T) {
	st := controlFixture(t)
	c, err := st.RequestAgentControl(web("person:1", "robin"), controlAgent, ControlPause, "stop please", RoleViaWeb)
	if err != nil {
		t.Fatal(err)
	}
	if c.State != ControlRequested || c.Action != ControlPause || c.Project != roleProject || c.RequestedByLabel != "robin" {
		t.Fatalf("new control = %+v", c)
	}
	// Zweite Anforderung derselben Art ist idempotent und legt nichts neu an.
	again, err := st.RequestAgentControl(web("person:1", "robin"), controlAgent, ControlPause, "x", RoleViaWeb)
	if err != nil || again.ID != c.ID {
		t.Fatalf("repeat = %+v %v", again, err)
	}
	if _, err := st.RequestAgentControl(web("person:1", "robin"), controlAgent, ControlInterrupt, "", RoleViaWeb); !errors.Is(err, ErrControlActive) {
		t.Fatalf("different action while active: %v", err)
	}

	owner := "person:4"
	ack := ControlEvent{Kind: ControlEventAck, ToolUseID: "toolu_1", ToolName: "Bash", SessionID: "s1"}
	if ok, err := st.RecordControlEvent(owner, c.ID, ack); err != nil || !ok {
		t.Fatalf("ack: %v %v", ok, err)
	}
	if ok, _ := st.RecordControlEvent(owner, c.ID, ack); ok {
		t.Fatal("a repeated ack must not be recorded twice")
	}
	got, _, _ := st.ActiveAgentControl(controlAgent)
	if got.State != ControlAcknowledged || got.AckedAt == "" || got.EffectiveAt != "" {
		t.Fatalf("after ack = %+v", got)
	}
	// Ein Beleg fuer einen anderen Aufruf macht nichts wirksam.
	if _, err := st.RecordControlEvent(owner, c.ID, ControlEvent{Kind: ControlEventProof, ToolUseID: "toolu_other"}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := st.ActiveAgentControl(controlAgent); got.State != ControlAcknowledged {
		t.Fatalf("proof for another call must not make it effective: %+v", got)
	}
	if _, err := st.RecordControlEvent(owner, c.ID, ControlEvent{Kind: ControlEventProof, ToolUseID: "toolu_1"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = st.ActiveAgentControl(controlAgent)
	if got.State != ControlEffective || got.EffectiveAt == "" {
		t.Fatalf("ack and matching proof = %+v", got)
	}

	r, err := st.ResumeAgentControl(web("person:1", "robin"), controlAgent)
	if err != nil || r.State != ControlResumed || r.ResumedByLabel != "robin" || r.ResumedAt == "" {
		t.Fatalf("resume = %+v %v", r, err)
	}
	if _, ok, _ := st.ActiveAgentControl(controlAgent); ok {
		t.Fatal("no active control after resume")
	}
	last, ok, _ := st.LatestAgentControl(controlAgent)
	if !ok || last.State != ControlResumed || last.ID != c.ID {
		t.Fatalf("latest = %+v", last)
	}
	// Danach darf neu pausiert werden: neue Zeile, alte bleibt als Audit.
	n, err := st.RequestAgentControl(web("person:1", "robin"), controlAgent, ControlInterrupt, "", RoleViaWeb)
	if err != nil || n.ID == c.ID || n.Action != ControlInterrupt || n.Gap == "" {
		t.Fatalf("interrupt = %+v %v", n, err)
	}
	hist, _ := st.AgentControlHistory(controlAgent, 10)
	if len(hist) != 2 || hist[0].ID != n.ID {
		t.Fatalf("history = %+v", hist)
	}
}

func TestAgentControlProofBeforeAckStillBecomesEffective(t *testing.T) {
	st := controlFixture(t)
	c, _ := st.RequestAgentControl(web("person:1", "robin"), controlAgent, ControlPause, "", RoleViaWeb)
	if _, err := st.RecordControlEvent("person:4", c.ID, ControlEvent{Kind: ControlEventProof, ToolUseID: "t9"}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := st.ActiveAgentControl(controlAgent); got.State != ControlRequested {
		t.Fatalf("a proof without ack proves no hook reaction: %+v", got)
	}
	if _, err := st.RecordControlEvent("person:4", c.ID, ControlEvent{Kind: ControlEventAck, ToolUseID: "t9", AgentID: "sub1"}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := st.ActiveAgentControl(controlAgent); got.State != ControlEffective {
		t.Fatalf("late ack = %+v", got)
	}
}

func TestAgentControlPermissions(t *testing.T) {
	st := controlFixture(t)
	for _, tc := range []struct {
		name string
		p    Principal
		ok   bool
	}{
		{"project owner", web("person:1", "robin"), true},
		{"lead", web("person:3", "ben"), true},
		{"account owner of the agent", web("person:4", "cleo"), true},
		{"plain member", web("person:2", "anna"), false},
		{"no role at all", web("person:5", "dev"), false}, // not seen: ErrControlNotFound
		{"lead through a bearer token", Principal{ID: "person:3", Label: "ben", TokenKind: "cli"}, false},
		{"owner through a legacy token", Principal{ID: "person:1", Label: "robin"}, false},
		{"the agent account through a token", Principal{ID: "person:4", Label: "cleo", TokenKind: "device"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := st.RequestAgentControl(tc.p, controlAgent, ControlPause, "", RoleViaWeb)
			if tc.ok && err != nil {
				t.Fatalf("want allowed: %v", err)
			}
			want := ErrControlForbidden
			if tc.p.ID == "person:5" {
				want = ErrControlNotFound // indistinguishable from an unknown agent
			}
			if !tc.ok && !errors.Is(err, want) {
				t.Fatalf("want %v, got %v", want, err)
			}
			if tc.ok {
				if _, err := st.ResumeAgentControl(tc.p, controlAgent); err != nil {
					t.Fatal(err)
				}
			}
			if got := st.MayControlAgent(tc.p, controlAgent); got != tc.ok {
				t.Fatalf("MayControlAgent = %v", got)
			}
		})
	}
	if _, err := st.RequestAgentControl(web("person:1", "robin"), "claude:h:unknown", ControlPause, "", RoleViaWeb); !errors.Is(err, ErrControlNotFound) {
		t.Fatalf("unknown agent: %v", err)
	}
	if _, err := st.RequestAgentControl(web("person:1", "robin"), controlAgent, "kill", "", RoleViaWeb); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown action: %v", err)
	}
	// Resume braucht dieselbe Berechtigung: ein Member darf nicht entpausieren.
	if _, err := st.RequestAgentControl(web("person:1", "robin"), controlAgent, ControlPause, "", RoleViaWeb); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ResumeAgentControl(web("person:2", "anna"), controlAgent); !errors.Is(err, ErrControlForbidden) {
		t.Fatalf("member resume: %v", err)
	}
	if _, err := st.ResumeAgentControl(Principal{ID: "person:1", TokenKind: "cli"}, controlAgent); !errors.Is(err, ErrControlForbidden) {
		t.Fatalf("token resume: %v", err)
	}
}

func TestAgentControlEventsOnlyFromTheAgentOwner(t *testing.T) {
	st := controlFixture(t)
	c, _ := st.RequestAgentControl(web("person:1", "robin"), controlAgent, ControlPause, "", RoleViaWeb)
	for _, who := range []string{"person:1", "person:2", "person:3", "person:5"} {
		if ok, err := st.RecordControlEvent(who, c.ID, ControlEvent{Kind: ControlEventAck, ToolUseID: "t"}); ok || !errors.Is(err, ErrControlForbidden) {
			t.Fatalf("%s may not report evidence for cleo's agent: %v %v", who, ok, err)
		}
	}
	if _, err := st.RecordControlEvent("person:4", 999, ControlEvent{Kind: ControlEventAck, ToolUseID: "t"}); !errors.Is(err, ErrControlNotFound) {
		t.Fatalf("unknown control: %v", err)
	}
	if _, err := st.RecordControlEvent("person:4", c.ID, ControlEvent{Kind: "nope", ToolUseID: "t"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad kind: %v", err)
	}
	if _, err := st.RecordControlEvent("person:4", c.ID, ControlEvent{Kind: ControlEventAck}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing tool_use_id: %v", err)
	}
	if ok, err := st.RecordControlEvent("person:4", c.ID, ControlEvent{Kind: ControlEventAck, ToolUseID: "t"}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err := st.db.Exec(`UPDATE agent_control_events SET tool_name='x'`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("events must be append-only: %v", err)
	}
}

// The account owner as a mere member cannot lift a lead's pause; the requester
// and anyone at least as senior can.
func TestAgentControlResumeNeedsTheRequestersRank(t *testing.T) {
	st := controlFixture(t)
	lead, owner, member := web("person:3", "ben"), web("person:4", "cleo"), web("person:2", "anna")
	if _, err := st.RequestAgentControl(lead, controlAgent, ControlPause, "", RoleViaWeb); err != nil {
		t.Fatal(err)
	}
	if st.MayResumeAgentControl(owner, controlAgent) {
		t.Fatal("UI must not offer resume to a member who owns the account")
	}
	if _, err := st.ResumeAgentControl(owner, controlAgent); !errors.Is(err, ErrControlResumeRank) {
		t.Fatalf("member owner lifting a lead's pause: %v", err)
	}
	if _, err := st.ResumeAgentControl(member, controlAgent); !errors.Is(err, ErrControlForbidden) {
		t.Fatalf("unrelated member: %v", err)
	}
	if _, ok, _ := st.ActiveAgentControl(controlAgent); !ok {
		t.Fatal("the pause must still hold")
	}
	if !st.MayResumeAgentControl(web("person:1", "robin"), controlAgent) {
		t.Fatal("a more senior person may resume")
	}
	if _, err := st.ResumeAgentControl(lead, controlAgent); err != nil {
		t.Fatalf("requester: %v", err)
	}
	// The other direction: the owner paused, a lead lifts it (rank >=).
	if _, err := st.RequestAgentControl(owner, controlAgent, ControlPause, "", RoleViaWeb); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ResumeAgentControl(lead, controlAgent); err != nil {
		t.Fatalf("lead lifting the owner's pause: %v", err)
	}
}

func TestAgentControlRefusesAgentsItCannotReach(t *testing.T) {
	st := controlFixture(t)
	room := RoomKeyForProject(roleProject)
	if _, err := st.RegisterCoordAgent(CoordAgent{ExternalID: "codex:h:x", Provider: "codex", RoomKey: room, DisplayName: "x", PrincipalID: "person:4"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RegisterCoordAgent(CoordAgent{ExternalID: "claude:h:nobody", Provider: "claude", RoomKey: room, DisplayName: "y", PrincipalID: "cli:odd"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"codex:h:x", "claude:h:nobody"} {
		if gap := st.AgentControlGap(id); gap == "" {
			t.Fatalf("%s must carry a named gap", id)
		}
		if _, err := st.RequestAgentControl(web("person:1", "robin"), id, ControlPause, "", RoleViaWeb); !errors.Is(err, ErrControlUnsupported) {
			t.Fatalf("%s: %v", id, err)
		}
		if _, ok, _ := st.ActiveAgentControl(id); ok {
			t.Fatalf("%s: a refused request must not leave a control behind", id)
		}
	}
	if st.AgentControlGap(controlAgent) != "" {
		t.Fatal("a claude agent with a person account is supported")
	}
}
