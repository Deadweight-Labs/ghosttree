package mcpserver

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// REQ-671 AC-3: an agent attaches a screenshot to a pain with `pain action=attach` and to an
// issue with `request action=attach`, by file or as base64, and the listed schemas of the
// two tools do not grow by a field for it (PAIN-71).
func TestAgentsAttachImagesToPainsAndRequestsThroughTheCoreTools(t *testing.T) {
	c, st := newTestClient(t)
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}})
	png := base64.StdEncoding.EncodeToString(testPNGBytes(t))

	if text, failed := callTool(t, session, "pain", map[string]any{"action": "record", "title": "Button bricht um"}); failed || !strings.Contains(text, "PAIN-1") {
		t.Fatalf("pain record: %v %s", failed, text)
	}
	if text, failed := callTool(t, session, "request", map[string]any{"action": "create", "type": "bug", "title": "Button bricht um", "description": "d", "criteria": []string{"c"}}); failed || !strings.Contains(text, "REQ-1") {
		t.Fatalf("request create: %v %s", failed, text)
	}

	// The pain is named as id, like the other pain actions; the image belongs to the pain itself.
	text, failed := callTool(t, session, "pain", map[string]any{"action": "attach", "id": 1, "data_base64": png, "alt": "Umbruch"})
	if failed || !strings.Contains(text, `"ref":"ATT-1"`) || !strings.Contains(text, `"owner":"pain"`) || !strings.Contains(text, `"mime":"image/png"`) {
		t.Fatalf("pain attach: %v %s", failed, text)
	}
	text, failed = callTool(t, session, "request", map[string]any{"action": "attach", "request_id": 1, "data_base64": png})
	if failed || !strings.Contains(text, `"ref":"ATT-2"`) || !strings.Contains(text, `"owner":"request"`) {
		t.Fatalf("request attach: %v %s", failed, text)
	}
	// What does not fit is refused, and nothing is guessed: an id is a pain here, never a request.
	for name, call := range map[string]struct {
		tool string
		args map[string]any
	}{
		"svg":                    {"pain", map[string]any{"action": "attach", "id": 1, "data_base64": base64.StdEncoding.EncodeToString([]byte(`<svg onload="x"/>`))}},
		"no image":               {"pain", map[string]any{"action": "attach", "id": 1}},
		"path and data":          {"pain", map[string]any{"action": "attach", "id": 1, "path": "/nonexistent.png", "data_base64": png}},
		"unknown pain":           {"pain", map[string]any{"action": "attach", "id": 99, "data_base64": png}},
		"no pain":                {"pain", map[string]any{"action": "attach", "data_base64": png}},
		"unknown request":        {"request", map[string]any{"action": "attach", "request_id": 99, "data_base64": png}},
		"a variant of a request": {"request", map[string]any{"action": "attach", "request_id": 1, "owner": "variant", "data_base64": png}},
		"neither idea nor issue": {"idea_attach", map[string]any{"data_base64": png}},
	} {
		if got, failed := callTool(t, session, call.tool, call.args); !failed {
			t.Errorf("%s accepted: %s", name, got)
		}
	}
	// Each object got exactly its one picture: the id of the pain did not land on the request.
	for subject, owner := range map[string]string{"pain": "pain", "request": "request"} {
		var n int
		st.DB().QueryRow(`SELECT COUNT(*) FROM attachments WHERE subject_kind=? AND subject_id=1 AND owner_kind=?`, subject, owner).Scan(&n)
		if n != 1 {
			t.Errorf("%d pictures on the %s, want 1", n, subject)
		}
	}

	// The two lines are listed, and the two tools gained no field for it: attach is a rare action.
	res, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name != "pain" && tool.Name != "request" {
			continue
		}
		if !strings.Contains(tool.Description, "attach") {
			t.Errorf("%s does not name its attach action", tool.Name)
		}
		raw, _ := json.Marshal(tool.InputSchema)
		props := string(raw)
		for _, field := range []string{"data_base64", `"path"`, `"alt"`} {
			if strings.Contains(props, field) {
				t.Errorf("%s lists %s in its schema: a rare action adds no field", tool.Name, field)
			}
		}
	}
}
