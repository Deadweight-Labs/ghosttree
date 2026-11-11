package server

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// An invented value. It stands behind a label, the one kind of secret that has
// no format of its own.
const guardLeak = "Hx7!kQ2pzWm"

// guardSecret is a text with that value in it, guardHarmless one that talks
// about the same thing without it.
func guardSecret(topic string) string { return topic + ": Mail Passwort: " + guardLeak + " gilt" }
func guardHarmless(topic string) string {
	return topic + ": das Mail-Passwort liegt im Vault unter secrets/mail, nicht hier"
}

// TestEveryWritePathRefusesASecretAndTakesHarmlessText (PAIN-17) goes through
// the routes that store free text of an agent or a person. Each is called once
// with a secret, which has to be answered with 422 secret_detected without the
// value and with what to do, and once with harmless text, which has to work.
func TestEveryWritePathRefusesASecretAndTakesHarmlessText(t *testing.T) {
	f := accessAPI(t, true)

	// The objects the routes work on.
	req := decode[requestdomain.Detail](t, f.expect(t, "robin", 201, "POST", "/api/requests",
		map[string]any{"type": "feature", "title": "Ziel", "project": accProject, "criteria": []string{"eins"}}))
	rid, crit := req.Request.ID, req.Criteria[0].ID
	ideaObj := decode[idea.Idea](t, f.expect(t, "robin", 201, "POST", "/api/ideas", map[string]any{"project": accProject, "title": "Wunsch"}))
	iid := ideaObj.ID
	painObj := decode[pain.Detail](t, f.expect(t, "robin", 201, "POST", "/api/pains", map[string]any{"project": accProject, "title": "Schmerz"})).Pain
	dec := decode[idea.Decision](t, f.expect(t, "robin", 201, "POST", "/api/decisions", map[string]any{
		"subject": "idea", "id": iid, "question": "Welche Anzeige", "variants": []map[string]any{{"title": "Name"}, {"title": "Name und Harness"}}}))
	droppable := decode[requestdomain.Detail](t, f.expect(t, "robin", 201, "POST", "/api/requests",
		map[string]any{"type": "change", "title": "Weg damit", "project": accProject})).Request.ID
	dismissable := decode[idea.Idea](t, f.expect(t, "robin", 201, "POST", "/api/ideas", map[string]any{"project": accProject, "title": "Nicht mehr"})).ID
	statePain := decode[pain.Detail](t, f.expect(t, "robin", 201, "POST", "/api/pains", map[string]any{"project": accProject, "title": "Schmerz 3"})).Pain.ID
	comment := decode[store.Comment](t, f.expect(t, "robin", 201, "POST", "/api/comments", map[string]any{"kind": "request", "id": rid, "body": "erster"}))
	feat := decode[struct{ ID int64 }](t, f.expect(t, "robin", 201, "POST", "/api/features", map[string]any{"project": accProject, "name": "Urheber", "summary": "Wer schrieb was."}))

	type route struct {
		name, method, path string
		body               func(text string) any
	}
	routes := []route{
		{"knowledge title", "POST", "/api/knowledge", func(s string) any {
			return map[string]any{"type": "note", "title": s, "body": "b", "scope": map[string]string{"project": accProject}}
		}},
		{"knowledge body", "POST", "/api/knowledge", func(s string) any {
			return map[string]any{"type": "note", "title": "Notiz", "body": s, "scope": map[string]string{"project": accProject}}
		}},
		{"knowledge supersede reason", "POST", "/api/knowledge", func(s string) any {
			return map[string]any{"type": "note", "title": "Neu", "body": "b", "scope": map[string]string{"project": accProject},
				"supersedes": []int64{f.id["k-mia"]}, "supersede_reason": s}
		}},
		{"knowledge retire", "POST", fmt.Sprintf("/api/knowledge/%d/retire", f.id["k-other"]), func(s string) any { return map[string]any{"reason": s} }},
		{"knowledge edit", "PATCH", fmt.Sprintf("/api/knowledge/%d", f.id["k-global"]), func(s string) any { return map[string]any{"body": s} }},
		{"request description", "POST", "/api/requests", func(s string) any {
			return map[string]any{"type": "feature", "title": "Neu", "description": s, "project": accProject}
		}},
		{"request criterion at creation", "POST", "/api/requests", func(s string) any {
			return map[string]any{"type": "feature", "title": "Neu", "project": accProject, "criteria": []string{"ok", s}}
		}},
		{"request criterion", "POST", fmt.Sprintf("/api/requests/%d/criteria", rid), func(s string) any { return map[string]any{"description": s} }},
		{"request correction", "PATCH", fmt.Sprintf("/api/requests/%d", rid), func(s string) any {
			return map[string]any{"patch": map[string]string{"description": s}, "reason": "genauer"}
		}},
		{"request evidence", "PATCH", fmt.Sprintf("/api/criteria/%d", crit), func(s string) any {
			return map[string]any{"state": "met", "evidence_kind": "session", "evidence_ref": s}
		}},
		{"request drop", "POST", fmt.Sprintf("/api/requests/%d/drop", droppable), func(s string) any { return map[string]any{"reason": s} }},
		{"request external relation", "POST", fmt.Sprintf("/api/requests/%d/relations", rid), func(s string) any { return map[string]any{"kind": "external", "external_ref": s} }},
		{"pain state", "POST", fmt.Sprintf("/api/pains/%d/state", statePain), func(s string) any {
			return map[string]any{"state": "mitigated", "remaining": s, "evidence_kind": "url", "evidence_ref": "https://x.example/1"}
		}},
		{"pain body", "POST", "/api/pains", func(s string) any { return map[string]any{"project": accProject, "title": "Schmerz 2", "body": s} }},
		{"pain correction", "PATCH", fmt.Sprintf("/api/pains/%d", painObj.ID), func(s string) any { return map[string]any{"body": s, "reason": "mehr Text"} }},
		{"idea title and text", "POST", "/api/ideas", func(s string) any {
			return map[string]any{"project": accProject, "title": "Wunsch 2", "body": s, "allow_duplicate": true}
		}},
		{"idea dismiss reason", "POST", fmt.Sprintf("/api/ideas/%d/state", dismissable), func(s string) any { return map[string]any{"state": "dismissed", "reason": s} }},
		{"idea raw text", "POST", fmt.Sprintf("/api/ideas/%d/texts", iid), func(s string) any { return map[string]any{"part": "raw", "body": s} }},
		{"idea concept", "POST", fmt.Sprintf("/api/ideas/%d/concept", iid), func(s string) any { return map[string]any{"about": s} }},
		{"idea requirement", "POST", fmt.Sprintf("/api/ideas/%d/concept", iid), func(s string) any { return map[string]any{"requirements": []string{s}} }},
		{"decision question", "POST", "/api/decisions", func(s string) any {
			return map[string]any{"subject": "idea", "id": iid, "question": s, "variants": []map[string]any{{"title": "A"}, {"title": "B"}}}
		}},
		{"decision variant", "POST", fmt.Sprintf("/api/decisions/%d/variants", dec.ID), func(s string) any { return map[string]any{"title": "C", "pro": s} }},
		{"decision vote", "POST", fmt.Sprintf("/api/decisions/%d/votes", dec.ID), func(s string) any { return map[string]any{"variant": "A", "reason": s} }},
		{"comment", "POST", "/api/comments", func(s string) any { return map[string]any{"kind": "request", "id": rid, "body": s} }},
		{"comment edit", "PATCH", fmt.Sprintf("/api/comments/%d", comment.ID), func(s string) any { return map[string]any{"body": s} }},
		{"feature", "POST", "/api/features", func(s string) any { return map[string]any{"project": accProject, "name": "Anderes", "summary": s} }},
		{"feature version", "POST", fmt.Sprintf("/api/features/%d/versions", feat.ID), func(s string) any { return map[string]any{"behavior": s} }},
		{"package name", "POST", "/api/packages", func(s string) any { return map[string]any{"project": accProject, "branch": "feat/x", "name": s} }},
		{"human task description", "POST", "/api/tasks", func(s string) any {
			return map[string]any{"project": accProject, "kind": "input", "title": "Frage", "description": s, "request_id": rid}
		}},
		{"human task step", "POST", "/api/tasks", func(s string) any {
			return map[string]any{"project": accProject, "kind": "action", "title": "Tun", "steps": []map[string]string{{"text": s}}, "request_id": rid}
		}},
	}
	for _, r := range routes {
		code, out := f.call(t, "robin", r.method, r.path, r.body(guardSecret(r.name)))
		if code != 422 {
			t.Errorf("%s: secret answered %d, want 422: %s", r.name, code, strings.TrimSpace(out))
			continue
		}
		var answer struct{ Code, Message string }
		if err := json.Unmarshal([]byte(out), &answer); err != nil || answer.Code != "secret_detected" {
			t.Errorf("%s: answer %s", r.name, out)
		}
		if strings.Contains(out, guardLeak) {
			t.Errorf("%s: the answer repeats the value: %s", r.name, out)
		}
		// What an agent reads: where, and what to do.
		for _, want := range []string{"nothing was stored", "line 1", "column", "Do not repeat the value"} {
			if !strings.Contains(answer.Message, want) && !strings.Contains(answer.Message, strings.ToLower(want)) {
				t.Errorf("%s: message lacks %q: %s", r.name, want, answer.Message)
			}
		}
		// The writer's own client turns the answer into the error the MCP tool shows.
		apiErr := client.APIError{Status: code}
		if err := json.Unmarshal([]byte(out), &apiErr); err != nil || !strings.Contains(apiErr.Error(), "Do not repeat the value") {
			t.Errorf("%s: client error %q", r.name, apiErr.Error())
		}
		code, out = f.call(t, "robin", r.method, r.path, r.body(guardHarmless(r.name)))
		if code/100 != 2 {
			t.Errorf("%s: harmless text answered %d: %s", r.name, code, strings.TrimSpace(out))
		}
	}
}

// TestCoordWritePathsRefuseASecret is the same for what is said in a room and
// in a thread: the message, the thread, its summary and its outcome. A message
// is refused and not redacted: the sender is an agent that reads the answer and
// writes it again, and the others would otherwise read a message that no longer
// says what it was meant to.
func TestCoordWritePathsRefuseASecret(t *testing.T) {
	srv, st, token, _ := coordinationAccessServer(t)
	project := "github.com/x/guard"
	room := store.RoomKeyForProject(project)
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "sess-a", PrincipalID: "person:1", Person: "owner", Provider: "test", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: "sess-b", PrincipalID: "person:1", Person: "owner", Provider: "test", RoomKey: room}); err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body any) (int, string) {
		res := req(t, method, srv.URL+path, token, body)
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(out)
	}
	secret, harmless := guardSecret("Absprache"), guardHarmless("Absprache")
	check := func(name string, send func(text string) (int, string)) {
		t.Helper()
		code, out := send(secret)
		var answer struct{ Code, Message string }
		_ = json.Unmarshal([]byte(out), &answer)
		if code != 422 || answer.Code != "secret_detected" || strings.Contains(out, guardLeak) ||
			!strings.Contains(answer.Message, "line 1") || !strings.Contains(answer.Message, "Do not repeat the value") {
			t.Errorf("%s: secret answered %d: %s", name, code, out)
		}
		if code, out := send(harmless); code != 200 {
			t.Errorf("%s: harmless text answered %d: %s", name, code, out)
		}
	}
	n := 0
	check("message", func(text string) (int, string) {
		n++
		return call("POST", "/api/coord/messages", store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room,
			SenderExternalID: "sess-a", ClientID: fmt.Sprintf("m%d", n), Body: text})
	})
	check("message answer options", func(text string) (int, string) {
		n++
		return call("POST", "/api/coord/messages", map[string]any{"destination_kind": store.DestinationRoom, "destination_id": room,
			"sender_external_id": "sess-a", "client_id": fmt.Sprintf("o%d", n), "body": "Welche?", "intent": store.IntentQuestion, "mentions": []string{"sess-b"}, "options": []string{"ja", text}})
	})
	var threadID int64
	check("thread", func(text string) (int, string) {
		code, out := call("POST", "/api/threads?agent_external_id=sess-a", store.Thread{Project: project, Title: "Thread", Question: text})
		if code == 200 {
			var made struct{ ID int64 }
			_ = json.Unmarshal([]byte(out), &made)
			threadID = made.ID
		}
		return code, out
	})
	if threadID == 0 {
		t.Fatal("no thread to write into")
	}
	path := fmt.Sprintf("/api/threads/%d", threadID)
	check("thread message", func(text string) (int, string) {
		n++
		return call("POST", "/api/coord/messages", store.CoordMessage{DestinationKind: store.DestinationDiscussion, DestinationID: store.ThreadDestinationID(threadID),
			SenderExternalID: "sess-a", ClientID: fmt.Sprintf("t%d", n), Body: text})
	})
	check("thread summary", func(text string) (int, string) {
		return call("POST", path+"/summary?agent_external_id=sess-a", store.ThreadSummary{Body: text, CoversThrough: 1})
	})
	check("thread outcome", func(text string) (int, string) {
		return call("POST", path+"/outcomes?agent_external_id=sess-a", store.ThreadOutcome{Kind: "request", RefID: "1", Note: text})
	})
}
