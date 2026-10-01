package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const (
	ctlProject = "github.com/x/ctl"
	ctlAgent   = "claude:h:ui-agent"
)

// ctlFixture: alice (person:1) ist Org- und damit Projekt-Owner, anna (person:2)
// ist Mitglied ohne Projektrolle, bob (person:3) besitzt den Claude-Agenten.
type ctlEnv struct {
	srv         *httptest.Server
	st          *store.Store
	aliceTok    string
	alice, anna *http.Client
	room        string
}

func ctlFixture(t *testing.T) ctlEnv {
	t.Helper()
	s, st, aliceTok := testWeb(t)
	for _, name := range []string{"anna", "bob"} {
		if _, err := st.AddPerson(name); err != nil {
			t.Fatal(err)
		}
	}
	org, err := st.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimProject("person:1", ctlProject, "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectRole("person:1", ctlProject, "person:2", store.RoleMember, false, store.RoleViaWeb); err != nil {
		t.Fatal(err)
	}
	room := store.RoomKeyForProject(ctlProject)
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: ctlAgent, PrincipalID: "person:3", Person: "bob", Provider: "claude", DisplayName: "ui agent", RoomKey: room, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	materializeWebRoomFor(t, st, room, "person:1", "alice")
	materializeWebRoomFor(t, st, room, "person:2", "anna")
	return ctlEnv{srv: s, st: st, aliceTok: aliceTok, alice: loginInteractive(t, s, st, "alice"), anna: loginInteractive(t, s, st, "anna"), room: room}
}

func (e ctlEnv) post(t *testing.T, c *http.Client, form url.Values) *http.Response {
	t.Helper()
	form.Set("csrf_token", renderedCSRFToken(t, c, e.srv.URL+"/ui/coord?room="+url.QueryEscape(e.room)))
	form.Set("room", e.room)
	form.Set("agent", ctlAgent)
	return sameOriginPostForm(t, c, e.srv.URL+"/ui/coord/agent/control", form)
}

func TestAgentControlOnTheParticipantShowsOnlyProvenStates(t *testing.T) {
	e := ctlFixture(t)
	st, alice := e.st, e.alice
	pageURL := e.srv.URL + "/ui/coord?room=" + url.QueryEscape(e.room)
	page := coordPageBody(t, alice, pageURL)
	for _, want := range []string{`action="/ui/coord/agent/control"`, "Pausieren", "Unterbrechen", "nächsten Werkzeugaufruf"} {
		if !strings.Contains(page, want) {
			t.Fatalf("idle participant lacks %q", want)
		}
	}
	if strings.Contains(page, "Fortsetzen") {
		t.Fatal("nothing to resume yet")
	}

	resp := e.post(t, alice, url.Values{"action": {"pause"}, "reason": {"Pause bitte"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("pause = %d %s", resp.StatusCode, body(t, resp))
	}
	c, ok, _ := st.ActiveAgentControl(ctlAgent)
	if !ok || c.Action != store.ControlPause || c.RequestedByLabel != "alice" || c.Reason != "Pause bitte" {
		t.Fatalf("control = %+v %v", c, ok)
	}
	page = coordPageBody(t, alice, pageURL)
	if !strings.Contains(page, "Pause angefordert") || !strings.Contains(page, "nicht pausiert") || !strings.Contains(page, "Fortsetzen") {
		t.Fatalf("requested state not shown honestly: %s", page[strings.Index(page, "coord-agent-control"):])
	}
	if strings.Contains(page, "Pausiert (belegt)") {
		t.Fatal("requested must never read as paused")
	}

	owner := "person:3"
	_, _ = st.RecordControlEvent(owner, c.ID, store.ControlEvent{Kind: store.ControlEventAck, ToolUseID: "toolu_1", ToolName: "Bash", AgentID: "sub1"})
	page = coordPageBody(t, alice, pageURL)
	if !strings.Contains(page, "Bestätigt (Hook)") || strings.Contains(page, "Pausiert (belegt)") {
		t.Fatalf("acknowledged state wrong: %s", page[strings.Index(page, "coord-agent-control"):])
	}
	if !strings.Contains(page, "sub1") {
		t.Fatal("the subagent that was blocked must be named")
	}

	_, _ = st.RecordControlEvent(owner, c.ID, store.ControlEvent{Kind: store.ControlEventProof, ToolUseID: "toolu_1"})
	page = coordPageBody(t, alice, pageURL)
	if !strings.Contains(page, "Pausiert (belegt)") {
		t.Fatalf("effective state missing: %s", page[strings.Index(page, "coord-agent-control"):])
	}

	resp = e.post(t, alice, url.Values{"action": {"resume"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("resume = %d", resp.StatusCode)
	}
	page = coordPageBody(t, alice, pageURL)
	if !strings.Contains(page, "Fortgesetzt") || strings.Contains(page, "Pausiert (belegt)") {
		t.Fatalf("resumed state wrong: %s", page[strings.Index(page, "coord-agent-control"):])
	}

	// Unterbrechung nennt die Luecke.
	resp = e.post(t, alice, url.Values{"action": {"interrupt"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("interrupt = %d", resp.StatusCode)
	}
	page = coordPageBody(t, alice, pageURL)
	if !strings.Contains(page, "Unterbrechung angefordert") || !strings.Contains(page, "laufender Aufruf wird nicht abgebrochen") {
		t.Fatalf("interrupt gap not named: %s", page[strings.Index(page, "coord-agent-control"):])
	}
}

func TestAgentControlIsRefusedForTokenSessionsAndPlainMembers(t *testing.T) {
	e := ctlFixture(t)
	st, alice, anna, srv, room := e.st, e.alice, e.anna, e.srv.URL, e.room
	// Ein Mitglied ohne Lead-Rolle sieht keine Schaltflaeche und darf nicht.
	if page := coordPageBody(t, anna, srv+"/ui/coord?room="+url.QueryEscape(room)); strings.Contains(page, "/ui/coord/agent/control") {
		t.Fatal("a plain member must not see the control form")
	}
	if resp := e.post(t, anna, url.Values{"action": {"pause"}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member pause = %d", resp.StatusCode)
	}
	// Eine Sitzung aus eingefuegtem Token ist nur lesend, auch fuer den Owner.
	pasted := login(t, e.srv, e.aliceTok)
	if page := coordPageBody(t, pasted, srv+"/ui/coord?room="+url.QueryEscape(room)); strings.Contains(page, "/ui/coord/agent/control") {
		t.Fatal("a pasted-token session must not see the control form")
	}
	resp := e.post(t, pasted, url.Values{"action": {"pause"}})
	page := body(t, resp)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(page, "Anmeldung per Login") {
		t.Fatalf("pasted-token pause = %d %s", resp.StatusCode, page)
	}
	if _, ok, _ := st.ActiveAgentControl(ctlAgent); ok {
		t.Fatal("a refused request created a control")
	}
	if resp := e.post(t, alice, url.Values{"action": {"explode"}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown action = %d", resp.StatusCode)
	}
	// Codex-Agenten: kein Schalter, eine benannte Luecke.
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "codex:h:x", PrincipalID: "person:3", Provider: "codex", DisplayName: "codex x", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	page = coordPageBody(t, alice, srv+"/ui/coord?room="+url.QueryEscape(room))
	if !strings.Contains(page, "Pause für Codex nicht unterstützt") {
		t.Fatal("codex must show the gap")
	}
	if strings.Contains(page, `name="agent" value="codex:h:x"`) {
		t.Fatal("codex must not get a control form")
	}
}
