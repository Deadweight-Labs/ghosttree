package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// oracleFixture legt eine für Gäste freigegebene Session an (rex) und danach
// hidden verborgene Sessions mit je einem sehr gut rankenden Treffer. Die
// freigegebene Session kommt immer zuerst, damit ihre Nummern in allen Läufen
// gleich sind.
func oracleFixture(t *testing.T, hidden int) *accessAPIFixture {
	t.Helper()
	f := accessAPI(t, true)
	shared, err := f.st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "o-shared", AccountID: 4, Scope: scope.Axes{Project: accProject}})
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("filler words about nothing in particular ", 40) + " zaunkoenig " + strings.Repeat("more filler text goes here ", 40)
	if err := f.st.AppendChunks(shared, []store.Chunk{{Seq: 0, Role: "user", Text: long, Raw: "{}"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetSessionVisibility(shared, f.st.Access(store.Principal{ID: "person:4", Label: "rex"}), store.VisGuests); err != nil {
		t.Fatal(err)
	}
	f.id["o-shared"] = shared
	for i := 0; i < hidden; i++ {
		id, err := f.st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: fmt.Sprintf("o-hidden-%d", i), AccountID: 3, Scope: scope.Axes{Project: accProject}})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.st.AppendChunks(id, []store.Chunk{{Seq: 0, Role: "user", Text: "zaunkoenig zaunkoenig zaunkoenig", Raw: "{}"}}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// observed zieht aus einer Antwort alles, was ein Gast sieht, ohne Zufallsadressen
// und Zeitstempel (die sich zwischen zwei Läufen unterscheiden dürfen).
func observed(t *testing.T, body string) string {
	t.Helper()
	var generic any
	if err := json.Unmarshal([]byte(body), &generic); err != nil {
		t.Fatalf("not json: %s", body)
	}
	var strip func(v any) any
	strip = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				switch k {
				case "public_id", "last_seen_at", "started_at":
					delete(x, k)
				default:
					x[k] = strip(e)
				}
			}
		case []any:
			for i := range x {
				x[i] = strip(x[i])
			}
		}
		return v
	}
	out, _ := json.Marshal(strip(generic))
	return string(out)
}

func TestSessionSearchForGuestsDoesNotDependOnHiddenHits(t *testing.T) {
	path := "/api/search?q=zaunkoenig&kind=sessions&limit=1"
	var answers []string
	for _, hidden := range []int{0, 200} {
		f := oracleFixture(t, hidden)
		out := f.expect(t, "gus", 200, "GET", path, nil)
		if !strings.Contains(out, "zaunkoenig") {
			t.Fatalf("hidden=%d: guest does not find the shared session: %s", hidden, out)
		}
		answers = append(answers, observed(t, out))
	}
	if answers[0] != answers[1] {
		t.Errorf("answer depends on hidden hits:\n0:   %s\n200: %s", answers[0], answers[1])
	}
}

func TestSessionListForGuestsDoesNotDependOnHiddenSessions(t *testing.T) {
	path := "/api/sessions?project=" + accProject + "&limit=2"
	var answers []string
	for _, hidden := range []int{0, 300} {
		f := oracleFixture(t, hidden)
		out := f.expect(t, "gus", 200, "GET", path, nil)
		if !strings.Contains(out, "filler words") && !strings.Contains(out, "o-shared") && !strings.Contains(out, "public_id") {
			t.Fatalf("hidden=%d: guest does not see the shared session: %s", hidden, out)
		}
		answers = append(answers, observed(t, out))
	}
	if answers[0] != answers[1] {
		t.Errorf("answer depends on hidden sessions:\n0:   %s\n300: %s", answers[0], answers[1])
	}
}

func TestGuestsNeverSeeTheInternalSessionNumber(t *testing.T) {
	f := oracleFixture(t, 3)
	for _, path := range []string{
		"/api/sessions?project=" + accProject,
		"/api/search?q=zaunkoenig&kind=sessions",
		"/api/search?q=zaunkoenig&kind=all",
	} {
		var out struct {
			Sessions []store.SessionHit `json:"sessions"`
		}
		body := f.expect(t, "gus", 200, "GET", path, nil)
		var list []store.Session
		if json.Unmarshal([]byte(body), &list) == nil {
			for _, s := range list {
				if s.ID != 0 {
					t.Errorf("%s: guest sees session number %d", path, s.ID)
				}
			}
			if len(list) == 0 {
				t.Errorf("%s: guest sees nothing", path)
			}
			continue
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Sessions) == 0 {
			t.Errorf("%s: guest sees nothing", path)
		}
		for _, h := range out.Sessions {
			if h.Session.ID != 0 {
				t.Errorf("%s: guest sees session number %d", path, h.Session.ID)
			}
		}
	}
	// A member keeps the number.
	var list []store.Session
	if err := json.Unmarshal([]byte(f.expect(t, "mia", 200, "GET", "/api/sessions?project="+accProject, nil)), &list); err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == 0 {
			t.Errorf("member lost the session number of %s", s.PublicID)
		}
	}
}

func TestGuestSessionCreationReturnsOnlyTheAddress(t *testing.T) {
	f := accessAPI(t, true)
	body := map[string]any{"harness": "claude-code", "external_id": "guest-agent", "scope": map[string]string{"project": accProject}}
	var guest map[string]any
	if err := json.Unmarshal([]byte(f.expect(t, "gus", 200, "POST", "/api/sessions?refs=public_id", body)), &guest); err != nil {
		t.Fatal(err)
	}
	if n, _ := guest["id"].(float64); n != 0 {
		t.Errorf("guest got the internal number: %v", guest)
	}
	pid, _ := guest["public_id"].(string)
	if pid == "" {
		t.Fatalf("guest got no address: %v", guest)
	}
	chunk := map[string]any{"chunks": []store.Chunk{{Seq: 0, Role: "user", Text: "hello", Raw: "{}"}}}
	f.expect(t, "gus", 204, "POST", "/api/sessions/"+pid+"/chunks", chunk)
	f.expect(t, "gus", 200, "GET", "/api/sessions/"+pid, nil)
	// The address belongs to the owner: nobody else may write to it.
	f.expect(t, "mia", 403, "POST", "/api/sessions/"+pid+"/chunks", chunk)
	// An unknown address answers like a foreign one.
	f.expect(t, "gus", 403, "POST", "/api/sessions/zzzzzzzzzzzz/chunks", chunk)

	// A member still gets both, and the number keeps working.
	var member map[string]any
	body["external_id"] = "member-agent"
	if err := json.Unmarshal([]byte(f.expect(t, "mia", 200, "POST", "/api/sessions", body)), &member); err != nil {
		t.Fatal(err)
	}
	id, _ := member["id"].(float64)
	if id == 0 || member["public_id"] == "" {
		t.Errorf("member response = %v", member)
	}
	f.expect(t, "mia", 204, "POST", fmt.Sprintf("/api/sessions/%d/chunks", int64(id)), chunk)
}

func TestSessionUploadChecksOwnershipBeforeReadingTheBody(t *testing.T) {
	f := accessAPI(t, true)
	// A foreign session with a body that is not even JSON: ownership answers first.
	code, _ := f.call(t, "mia", "POST", idPath("/api/sessions/%d/chunks", f.id["s-rex"]), "not a chunk list but a long string")
	if code != 403 {
		t.Errorf("foreign upload = %d, want 403 before the body is decoded", code)
	}
}

func TestShareDoesNotRevealWhetherAHiddenSessionExists(t *testing.T) {
	f := accessAPI(t, true)
	body := map[string]bool{"shared": true}
	// mia is a member but may not read rex's private session.
	hiddenCode, hiddenBody := f.call(t, "mia", "PUT", idPath("/api/sessions/%d/share", f.id["s-rex"]), body)
	unknownCode, unknownBody := f.call(t, "mia", "PUT", "/api/sessions/999999/share", body)
	if hiddenCode != 404 || hiddenCode != unknownCode || hiddenBody != unknownBody {
		t.Errorf("hidden = %d %q, unknown = %d %q: must be identical 404", hiddenCode, hiddenBody, unknownCode, unknownBody)
	}
	// A readable session of someone else is still a 403 (the caller may read it).
	if err := f.st.SetSessionVisibility(f.id["s-rex"], f.st.Access(store.Principal{ID: "person:4", Label: "rex"}), store.VisProject); err != nil {
		t.Fatal(err)
	}
	f.expect(t, "mia", 403, "PUT", idPath("/api/sessions/%d/share", f.id["s-rex"]), body)
}

func TestGuestNumbersAreUnknownOnEverySessionRoute(t *testing.T) {
	f := oracleFixture(t, 3)
	routes := []struct{ method, tail string }{
		{"GET", ""}, {"GET", "/raw"}, {"POST", "/chunks"}, {"PUT", "/share"},
	}
	payload := func(tail string) any {
		switch tail {
		case "/chunks":
			return map[string]any{"chunks": []store.Chunk{{Seq: 0, Role: "user", Text: "x", Raw: "{}"}}}
		case "/share":
			return map[string]bool{"shared": true}
		}
		return nil
	}
	for _, r := range routes {
		wantCode, wantBody := f.call(t, "gus", r.method, "/api/sessions/999999"+r.tail, payload(r.tail))
		for n := int64(1); n <= int64(len(f.id))+8; n++ {
			code, out := f.call(t, "gus", r.method, fmt.Sprintf("/api/sessions/%d%s", n, r.tail), payload(r.tail))
			if code != wantCode || out != wantBody {
				t.Errorf("guest %s /api/sessions/%d%s = %d %q, unknown = %d %q", r.method, n, r.tail, code, out, wantCode, wantBody)
			}
		}
	}
}

func TestGuestShareAnswerCarriesTheAddressNotTheNumber(t *testing.T) {
	f := accessAPI(t, true)
	body := map[string]any{"harness": "claude-code", "external_id": "guest-own", "scope": map[string]string{"project": accProject}}
	var made map[string]any
	if err := json.Unmarshal([]byte(f.expect(t, "gus", 200, "POST", "/api/sessions?refs=public_id", body)), &made); err != nil {
		t.Fatal(err)
	}
	pid, _ := made["public_id"].(string)
	out := f.expect(t, "gus", 200, "PUT", "/api/sessions/"+pid+"/share", map[string]bool{"shared": true})
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if _, has := got["id"]; has || got["public_id"] != pid {
		t.Errorf("guest share answer = %s", out)
	}
}

// Sessions ohne Projekt (kein Git-Remote) gehören keinem Projekt, in dem jemand
// eine Rolle hat. Ältere Collector kennen nur die Nummer: Admin und Mitglieder
// irgendeines Projekts müssen sie bekommen, sonst laden sie nach /0 hoch.
func TestOwnSessionNumberForSessionsWithoutProject(t *testing.T) {
	f := accessAPI(t, true)
	for _, project := range []string{"", "github.com/nobody/claimed"} {
		for _, who := range []string{"robin", "mia", "lena"} {
			body := map[string]any{"harness": "claude-code", "external_id": "np-" + who + project, "scope": map[string]string{"project": project}}
			var out map[string]any
			if err := json.Unmarshal([]byte(f.expect(t, who, 200, "POST", "/api/sessions", body)), &out); err != nil {
				t.Fatal(err)
			}
			if n, _ := out["id"].(float64); n == 0 {
				t.Errorf("%s project %q: no session number: %v", who, project, out)
			}
			if out["public_id"] == "" {
				t.Errorf("%s project %q: no address: %v", who, project, out)
			}
		}
	}
}

func TestGuestsWithoutRefsSupportGetCollectorTooOld(t *testing.T) {
	f := accessAPI(t, true)
	for _, who := range []string{"gus", "nora"} {
		for _, project := range []string{"", accProject} {
			if who == "nora" && project != "" {
				continue // claimed by another organization: refused earlier
			}
			body := map[string]any{"harness": "claude-code", "external_id": "old-" + who + project, "scope": map[string]string{"project": project}}
			out := f.expect(t, who, 409, "POST", "/api/sessions", body)
			if !strings.Contains(out, "collector_too_old") || !strings.Contains(out, "update ctx") {
				t.Errorf("%s project %q: answer = %s", who, project, out)
			}
			// Nothing was created by the refused request.
			var list []store.Session
			if err := json.Unmarshal([]byte(f.expect(t, "robin", 200, "GET", "/api/sessions?limit=500", nil)), &list); err != nil {
				t.Fatal(err)
			}
			for _, s := range list {
				if s.ExternalID == "old-"+who+project {
					t.Errorf("%s project %q: refused request created a session", who, project)
				}
			}
			// A client that understands addresses gets the address and no number.
			body["external_id"] = "new-" + who + project
			var out2 map[string]any
			if err := json.Unmarshal([]byte(f.expect(t, who, 200, "POST", "/api/sessions?refs=public_id", body)), &out2); err != nil {
				t.Fatal(err)
			}
			if _, has := out2["id"]; has || out2["public_id"] == "" {
				t.Errorf("%s project %q: response = %v", who, project, out2)
			}
		}
	}
}

func TestAdminSeesSessionNumbersInUnclaimedProjects(t *testing.T) {
	f := accessAPI(t, true)
	id, err := f.st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "home-session", AccountID: 3})
	if err != nil {
		t.Fatal(err)
	}
	pa := f.st.Access(store.Principal{ID: "person:1", Label: "robin"})
	if !pa.SeesSessionNumbers("") {
		t.Errorf("admin does not see the number of session %d without project", id)
	}
	if f.st.Access(store.Principal{ID: "person:5", Label: "gus"}).SeesSessionNumbers("") {
		t.Errorf("guest sees numbers of sessions without project")
	}
}
