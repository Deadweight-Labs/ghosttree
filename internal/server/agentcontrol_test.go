package server

import (
	"net/url"
	"strconv"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const controlAgentID = "claude:h:ctl"

// controlAPI: robin ist Org-Owner, anna und ben Mitglieder. Der Agent gehoert ben.
func controlAPI(t *testing.T) orgFixture {
	t.Helper()
	f, _ := roleAPI(t)
	if err := f.st.SetProjectRole("person:1", apiRoleProject, "person:3", "member", false, store.RoleViaWeb); err != nil {
		t.Fatal(err)
	}
	resp := req(t, "POST", f.srv.URL+"/api/coord/agents", f.ben, store.CoordAgent{ExternalID: controlAgentID, Provider: "claude", RoomKey: "project:" + apiRoleProject})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("register agent = %d", resp.StatusCode)
	}
	return f
}

// Pause, Unterbrechung und Fortsetzen gibt es nur in der Weboberflaeche. Kein
// Bearer-Token darf sie ausloesen: nicht das des Owners, nicht das eines
// Mitglieds, nicht das des Agentenbesitzers, auch nicht mit agent_external_id.
func TestAgentControlWritesRefuseBearerTokens(t *testing.T) {
	f := controlAPI(t)
	for name, token := range map[string]string{"owner": f.robin, "member": f.anna, "agent owner": f.ben} {
		for _, path := range []string{"/api/agent-control", "/api/agent-control/resume"} {
			for _, body := range []map[string]any{
				{"agent": controlAgentID, "action": "pause"},
				{"agent": controlAgentID, "action": "pause", "agent_external_id": controlAgentID},
			} {
				code, out := f.call(t, "POST", path, token, body)
				if code != 403 || out["code"] != "web_session_required" {
					t.Fatalf("%s POST %s = %d %v", name, path, code, out)
				}
			}
		}
	}
	f.mustCall(t, 401, "POST", "/api/agent-control", "", map[string]any{"agent": controlAgentID, "action": "pause"})
	if _, ok, _ := f.st.ActiveAgentControl(controlAgentID); ok {
		t.Fatal("a refused request must not create a control")
	}
}

func TestAgentControlReadAndEvidenceBelongToTheAgentAccount(t *testing.T) {
	f := controlAPI(t)
	q := "?agent=" + url.QueryEscape(controlAgentID)
	// Kein aktiver Vorgang: control ist null.
	out := f.mustCall(t, 200, "GET", "/api/agent-control"+q, f.ben, nil)
	if out["control"] != nil {
		t.Fatalf("idle: %v", out)
	}
	// Fremde Konten sehen den Agenten nicht.
	f.mustCall(t, 404, "GET", "/api/agent-control"+q, f.robin, nil)
	f.mustCall(t, 404, "GET", "/api/agent-control"+q, f.anna, nil)
	f.mustCall(t, 404, "GET", "/api/agent-control?agent=claude:h:nobody", f.ben, nil)
	f.mustCall(t, 401, "GET", "/api/agent-control"+q, "", nil)
	f.mustCall(t, 400, "GET", "/api/agent-control", f.ben, nil)

	c, err := f.st.RequestAgentControl(store.Principal{ID: "person:1", Label: "robin", TokenKind: store.WebSessionKind}, controlAgentID, "pause", "", "web")
	if err != nil {
		t.Fatal(err)
	}
	out = f.mustCall(t, 200, "GET", "/api/agent-control"+q, f.ben, nil)
	ctl := out["control"].(map[string]any)
	if ctl["state"] != "requested" || int64(ctl["id"].(float64)) != c.ID || ctl["action"] != "pause" {
		t.Fatalf("active: %v", out)
	}

	evPath := "/api/agent-control/" + strconv.FormatInt(c.ID, 10) + "/events"
	body := map[string]any{"kind": "ack", "tool_use_id": "toolu_1", "tool_name": "Bash"}
	// Nur das Konto des Agenten meldet Belege; andere erfahren nichts.
	for _, token := range []string{f.robin, f.anna} {
		out := f.mustCall(t, 200, "POST", evPath, token, body)
		if out["recorded"] != false {
			t.Fatalf("foreign evidence recorded: %v", out)
		}
	}
	if out := f.mustCall(t, 200, "POST", evPath, f.ben, body); out["recorded"] != true {
		t.Fatalf("owner ack: %v", out)
	}
	f.mustCall(t, 200, "POST", evPath, f.ben, map[string]any{"kind": "proof", "tool_use_id": "toolu_1"})
	f.mustCall(t, 400, "POST", evPath, f.ben, map[string]any{"kind": "bogus", "tool_use_id": "x"})
	f.mustCall(t, 400, "POST", "/api/agent-control/abc/events", f.ben, body)
	if out := f.mustCall(t, 200, "POST", "/api/agent-control/9999/events", f.ben, body); out["recorded"] != false {
		t.Fatalf("unknown control: %v", out)
	}
	out = f.mustCall(t, 200, "GET", "/api/agent-control"+q, f.ben, nil)
	if out["control"].(map[string]any)["state"] != "effective" {
		t.Fatalf("after ack and proof: %v", out)
	}
}
