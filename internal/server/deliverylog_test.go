package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/deliverylog"
)

func getWithHeaders(t *testing.T, f *accessAPIFixture, who, path string) (string, http.Header) {
	t.Helper()
	resp := req(t, "GET", f.srv.URL+path, f.tok[who], nil)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s = %d %s", path, resp.StatusCode, b)
	}
	return string(b), resp.Header
}

// REQ-636: the server tells the hook how the briefing divides, and the parts
// are the entries the delivery memory recorded, with the characters each took.
func TestBootstrapAnswersWithItsPartsInAHeader(t *testing.T) {
	f := accessAPI(t, true)
	body, h := getWithHeaders(t, f, "mia", "/api/context/bootstrap?project="+accProject+"&session=sess-parts")
	parts := deliverylog.ParseHeader(h.Get(deliverylog.HeaderName))
	if len(parts) == 0 {
		t.Fatalf("no parts announced for:\n%s", body)
	}
	sum := 0
	var ids []int64
	for _, p := range parts {
		if p.Via != deliverylog.ViaBootstrap || p.Chars <= 0 {
			t.Errorf("part %+v", p)
		}
		if p.Kind == deliverylog.KindKnowledge {
			ids = append(ids, p.ID)
			sum += p.Chars
		}
	}
	if sum <= 0 || sum > deliverylog.Runes(body) {
		t.Errorf("entry characters %d against a body of %d", sum, deliverylog.Runes(body))
	}
	// Each announced entry stands in the body, and each recorded delivery is announced.
	rows, err := f.st.DB().Query(`SELECT knowledge_id FROM context_deliveries WHERE session_key='sess-parts' AND knowledge_id != 0`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	remembered := map[int64]bool{}
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		remembered[id] = true
	}
	if len(remembered) != len(ids) {
		t.Errorf("announced %v, remembered %v", ids, remembered)
	}
	for _, id := range ids {
		if !remembered[id] {
			t.Errorf("entry %d announced but not remembered", id)
		}
	}
}

func TestRelevantAnswersWithPartsAndNothingToSayHasNoHeader(t *testing.T) {
	f := accessAPI(t, true)
	id := addPitfall(t, f, "quillfeather zerbricht beim Neustart")
	body, h := getWithHeaders(t, f, "mia", relevantURL("was+ist+mit+quillfeather", "sess-rel"))
	parts := deliverylog.ParseHeader(h.Get(deliverylog.HeaderName))
	if len(parts) != 1 || parts[0].Via != deliverylog.ViaRelevance || parts[0].Kind != deliverylog.KindKnowledge || parts[0].ID != id {
		t.Fatalf("parts %+v", parts)
	}
	line := body[strings.Index(body, "- ["):]
	if parts[0].Chars != deliverylog.Runes(line) {
		t.Errorf("chars %d, the line has %d", parts[0].Chars, deliverylog.Runes(line))
	}
	// Said once per session: the second answer is empty and announces nothing.
	body, h = getWithHeaders(t, f, "mia", relevantURL("was+ist+mit+quillfeather", "sess-rel"))
	if body != "" || h.Get(deliverylog.HeaderName) != "" {
		t.Errorf("second answer %q header %q", body, h.Get(deliverylog.HeaderName))
	}
}

// Writing follows the tool counts (a member of the project, not a guest, not a
// stranger). Reading is sums over the projects the reader may see in full, and a
// wrong window is refused instead of silently covering everything.
func TestContextLogNeedsTheRightToWriteAndReportsAreSums(t *testing.T) {
	f := accessAPI(t, true)
	batch := map[string]any{"project": accProject, "session_ref": "s-log", "harness": "claude",
		"events": []map[string]any{{"call": "abc123", "parts": []map[string]any{
			{"via": "path", "kind": "ghost", "path": "secret/place.go", "chars": 321},
			{"via": "path", "kind": "frame", "chars": 9}}}},
		"signals": []map[string]string{{"name": "CLAUDE_CODE_ENTRYPOINT", "value": "cli"}}}
	f.expect(t, "mia", 200, "POST", "/api/context-log", batch)
	f.expect(t, "nora", 404, "POST", "/api/context-log", batch)
	f.expect(t, "gus", 403, "POST", "/api/context-log", batch)
	f.expect(t, "mia", 400, "POST", "/api/context-log", map[string]any{"project": accProject, "session_ref": "s-log"})
	f.expect(t, "mia", 400, "POST", "/api/context-log", map[string]any{"project": accProject, "session_ref": "s-log",
		"signals": []map[string]string{{"name": "SECRET_TOKEN", "value": "x"}}})

	read := f.expect(t, "mia", 200, "GET", "/api/usage/deliveries?since=2020-01-01&project="+accProject, nil)
	var rep deliverylog.DeliveryUsage
	if err := json.Unmarshal([]byte(read), &rep); err != nil || rep.Chars != 330 || rep.Sessions != 1 {
		t.Fatalf("report %s %v", read, err)
	}
	if strings.Contains(read, "secret/place.go") || strings.Contains(read, "s-log") {
		t.Errorf("the report names a path or a session:\n%s", read)
	}
	f.expect(t, "gus", 200, "GET", "/api/usage/deliveries?since=2020-01-01&project="+accProject, nil)
	f.expect(t, "nora", 404, "GET", "/api/usage/deliveries?since=2020-01-01&project="+accProject, nil)
	// Without a project: the projects of the reader. A stranger sees nothing of mia's.
	var other deliverylog.DeliveryUsage
	if err := json.Unmarshal([]byte(f.expect(t, "nora", 200, "GET", "/api/usage/deliveries?since=2020-01-01", nil)), &other); err != nil || other.Chars != 0 {
		t.Errorf("a stranger sees %+v", other)
	}
	var sig deliverylog.SignalUsage
	if err := json.Unmarshal([]byte(f.expect(t, "mia", 200, "GET", "/api/usage/signals?since=2020-01-01&project="+accProject, nil)), &sig); err != nil ||
		len(sig.Lines) != 1 || sig.Lines[0].Value != "cli" || sig.Lines[0].Sessions != 1 {
		t.Errorf("signals %+v %v", sig, err)
	}
	for _, bad := range []string{"", "?since=yesterday", "?since=2026-13", "?since=2026-11-10;drop"} {
		f.expect(t, "mia", 400, "GET", "/api/usage/tools"+bad, nil)
	}
	f.expect(t, "mia", 200, "GET", "/api/usage/tools?since=2026-11-10", nil)
}

// The server does not trust the client to have cleaned a signal value: what is
// not a short token is stored as "(invalid)", and the report lists it so.
func TestContextLogStoresOnlyShortTokensAsSignalValues(t *testing.T) {
	f := accessAPI(t, true)
	post := func(ref, value string) {
		t.Helper()
		f.expect(t, "mia", 200, "POST", "/api/context-log", map[string]any{"project": accProject, "session_ref": ref, "harness": "claude",
			"signals": []map[string]string{{"name": "CLAUDE_CODE_ENTRYPOINT", "value": value}}})
	}
	post("s-ok", "sdk-cli")
	post("s-path", "/home/robin/.ssh/id_ed25519")
	post("s-long", strings.Repeat("a", 33))
	post("s-ctl", "a\u0000b")
	var sig deliverylog.SignalUsage
	body := f.expect(t, "mia", 200, "GET", "/api/usage/signals?since=2020-01-01&project="+accProject, nil)
	if err := json.Unmarshal([]byte(body), &sig); err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, l := range sig.Lines {
		got[l.Value] = l.Sessions
	}
	if got["sdk-cli"] != 1 || got["(invalid)"] != 3 || len(got) != 2 {
		t.Errorf("signal values in the report: %v", got)
	}
	if strings.Contains(body, "ssh") || strings.Contains(body, "robin") {
		t.Errorf("a value that is not a token reached the report:\n%s", body)
	}
}

// REQ-645: the use of the delivered context is a report of sums with the rights
// of the delivery report. It shows the share per route and kind and per profile
// and names no path, entry number or session.
func TestUsageContextReportsSumsWithTheRightsOfTheDeliveryReport(t *testing.T) {
	f := accessAPI(t, true)
	batch := map[string]any{"project": accProject, "session_ref": "s-use-secret", "harness": "claude",
		"events": []map[string]any{{"call": "use1", "profile": "lean", "parts": []map[string]any{
			{"via": "bootstrap", "kind": "knowledge", "id": 41, "chars": 100},
			{"via": "bootstrap", "kind": "knowledge", "id": 42, "chars": 300},
			{"via": "path", "kind": "ghost", "path": "secret/place.go", "chars": 500}}}}}
	f.expect(t, "mia", 200, "POST", "/api/context-log", batch)
	// What the job would have written: 41 picked up, 42 not used.
	if _, err := f.st.DB().Exec(`UPDATE context_delivery_log SET used=1, judged_at='2026-11-11T10:00:00Z' WHERE object_id=41`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB().Exec(`UPDATE context_delivery_log SET used=0, judged_at='2026-11-11T10:00:00Z' WHERE object_id=42`); err != nil {
		t.Fatal(err)
	}
	path := "/api/usage/context?since=2020-01-01&project=" + accProject
	body := f.expect(t, "mia", 200, "GET", path, nil)
	var rep deliverylog.ContextUse
	if err := json.Unmarshal([]byte(body), &rep); err != nil {
		t.Fatal(err)
	}
	var know, ghost *deliverylog.UseLine
	for i := range rep.Lines {
		switch rep.Lines[i].Kind {
		case "knowledge":
			know = &rep.Lines[i]
		case "ghost":
			ghost = &rep.Lines[i]
		}
	}
	if know == nil || know.Parts != 2 || know.Judged != 2 || know.PickedUp != 1 || know.Followed != 0 || know.Pending != 0 ||
		know.Chars != 400 || know.JudgedChars != 400 || know.UsedChars != 100 {
		t.Errorf("knowledge line %+v", know)
	}
	if ghost == nil || ghost.Parts != 1 || ghost.Judged != 0 || ghost.Pending != 0 {
		t.Errorf("a path description is counted and never judged: %+v", ghost)
	}
	if len(rep.Profiles) != 1 || rep.Profiles[0].Profile != "lean" || rep.Profiles[0].Kind != "knowledge" || rep.Profiles[0].PickedUp != 1 {
		t.Errorf("profiles %+v", rep.Profiles)
	}
	for _, leak := range []string{"secret/place.go", "s-use-secret", `"id"`} {
		if strings.Contains(body, leak) {
			t.Errorf("the report contains %q:\n%s", leak, body)
		}
	}
	f.expect(t, "gus", 200, "GET", path, nil)
	f.expect(t, "nora", 404, "GET", path, nil)
	var other deliverylog.ContextUse
	if err := json.Unmarshal([]byte(f.expect(t, "nora", 200, "GET", "/api/usage/context?since=2020-01-01", nil)), &other); err != nil || len(other.Lines) != 0 {
		t.Errorf("a stranger sees %+v", other)
	}
	for _, bad := range []string{"", "?since=yesterday", "?since=2026-11-10;drop"} {
		f.expect(t, "mia", 400, "GET", "/api/usage/context"+bad, nil)
	}
}
