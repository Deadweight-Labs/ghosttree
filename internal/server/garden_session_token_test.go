package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A gardener session reads with a token bound to one project and to reading:
// the gardener's account holds roles in two projects, the token sees one.
func TestGardenSessionTokenIsBoundToOneProjectAndToReading(t *testing.T) {
	f := gardenerAPI(t)
	gs := store.DefaultGardenSettings(1)
	gs.Enabled, gs.Repos = true, []string{accProject, accOther}
	if err := f.st.SetGardenSettings("person:1", gs); err != nil {
		t.Fatal(err)
	}
	// The gardener's own token reaches both projects.
	if out := f.expect(t, "gardener", 200, "GET", "/api/knowledge?project="+accOther, nil); !strings.Contains(out, "other project pitfall") {
		t.Fatalf("setup: the gardener's account does not read the other project: %s", out)
	}

	// Nobody but the gardener's account gets one.
	for _, who := range []string{"robin", "lena", "mia"} {
		f.expect(t, who, 403, "POST", "/api/garden/session-token", map[string]string{"project": accProject})
	}
	out := f.expect(t, "gardener", 201, "POST", "/api/garden/session-token", map[string]string{"project": accProject})
	if f.tok["session"] = extractToken(out); f.tok["session"] == "" {
		t.Fatalf("answer: %s", out)
	}

	// Reads in its project work.
	if out := f.expect(t, "session", 200, "GET", "/api/knowledge?project="+accProject, nil); !strings.Contains(out, "mia pitfall") {
		t.Errorf("the session does not read its project: %s", out)
	}
	// Nothing of the other project, however it asks.
	for _, path := range []string{
		"/api/knowledge?project=" + accOther,
		"/api/search?q=pitfall",
		"/api/search?q=pitfall&project=" + accOther,
	} {
		code, out := f.call(t, "session", "GET", path, nil)
		if strings.Contains(out, "other project pitfall") {
			t.Errorf("GET %s shows the other project (%d): %s", path, code, out)
		}
	}
	if code, out := f.call(t, "session", "GET", idPath("/api/knowledge/%d", f.id["k-other"]), nil); code != 404 && code != 403 {
		t.Errorf("an entry of the other project = %d %s", code, out)
	}
	// The token logs nobody in on the web and is no full account for anything else.
	if _, ok := f.st.AuthenticatePrincipal(f.tok["session"]); ok {
		t.Error("the session token passes AuthenticatePrincipal (web login)")
	}
	if p, ok := f.st.AuthenticateScopedPrincipal(f.tok["session"]); !ok || p.ScopeProject != accProject || !p.ReadOnly {
		t.Errorf("scoped principal = %+v, %v", p, ok)
	}
	// Entries without a project (the gardener's own global note) are not
	// reachable either, and nothing of this depends on the access mode.
	own, err := f.st.InsertKnowledge(store.Knowledge{Type: "note", Title: "gardener global", Body: "b", Person: "gardener", AuthorID: 7, Confidence: "trusted"})
	if err != nil {
		t.Fatal(err)
	}
	for _, enforce := range []bool{true, false} {
		f.st.SetAccessMode(store.AccessMode{Enforce: enforce})
		f.expect(t, "gardener", 200, "GET", idPath("/api/knowledge/%d", own), nil)
		if code, out := f.call(t, "session", "GET", idPath("/api/knowledge/%d", own), nil); code == 200 {
			t.Errorf("enforce=%v: the session reads a global entry: %s", enforce, out)
		}
		if code, out := f.call(t, "session", "GET", idPath("/api/knowledge/%d", f.id["k-other"]), nil); code == 200 {
			t.Errorf("enforce=%v: the session reads the other project: %s", enforce, out)
		}
		if out := f.expect(t, "session", 200, "GET", "/api/search?q=pitfall", nil); strings.Contains(out, "other project pitfall") || strings.Contains(out, "gardener global") {
			t.Errorf("enforce=%v: search shows more than the project: %s", enforce, out)
		}
	}
	f.st.SetAccessMode(store.AccessMode{Enforce: true})
	// No write, no organization, no coordination, no new token.
	f.expect(t, "session", 403, "POST", "/api/knowledge", map[string]any{"type": "note", "title": "x", "body": "y", "scope": map[string]string{"project": accProject}})
	f.expect(t, "session", 403, "GET", "/api/orgs", nil)
	f.expect(t, "session", 403, "GET", "/api/machines", nil)
	f.expect(t, "session", 403, "POST", "/api/garden/session-token", map[string]string{"project": accOther})
}

func extractToken(body string) string {
	var m map[string]string
	if json.Unmarshal([]byte(body), &m) != nil {
		return ""
	}
	return m["token"]
}
