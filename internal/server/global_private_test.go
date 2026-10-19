package server

import (
	"fmt"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// "Global" heißt "überall für mich": was robin ohne Projekt und Maschine ablegt,
// sieht und bekommt nur robin, auf keinem Weg ein anderes Konto, auch nicht der
// frische Account ohne Org und nicht der Gast (Abnahme 2026-10-11, Befund 1).
func TestGlobalEntriesReachOnlyTheirAuthorOnEveryRoute(t *testing.T) {
	f := accessAPI(t, true)
	g, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "robin global request", Person: "robin"}})
	if err != nil {
		t.Fatal(err)
	}
	rid, kid := g.Request.ID, f.id["k-global"]
	routes := []string{
		"/api/knowledge",
		"/api/search?q=pitfall&kind=knowledge",
		"/api/search?q=global",
		"/api/context/bootstrap?project=" + accProject + "&machine=x",
		"/api/context/bootstrap",
		"/api/context/relevant?q=global+pitfall",
		"/api/requests",
		"/api/requests?q=global",
		"/api/knowledge/regression-gaps",
	}
	for _, who := range []string{"lena", "mia", "rex", "gus", "nora"} {
		for _, route := range routes {
			_, out := f.call(t, who, "GET", route, nil)
			if strings.Contains(out, "global pitfall") || strings.Contains(out, "robin global request") {
				t.Errorf("%s GET %s shows robin's global entry: %s", who, route, out)
			}
		}
		f.expect(t, who, 404, "GET", idPath("/api/knowledge/%d", kid), nil)
		f.expect(t, who, 404, "GET", fmt.Sprintf("/api/requests/%d", rid), nil)
		f.expect(t, who, 404, "PATCH", idPath("/api/knowledge/%d", kid), map[string]string{"body": "x"})
		f.expect(t, who, 404, "POST", fmt.Sprintf("/api/requests/%d/work", rid), map[string]any{"role": "related"})
	}
	for _, route := range []string{"/api/knowledge", "/api/search?q=pitfall&kind=knowledge", "/api/context/bootstrap?project=" + accProject} {
		if out := f.expect(t, "robin", 200, "GET", route, nil); !strings.Contains(out, "global pitfall") {
			t.Errorf("robin lost his own global entry on %s: %s", route, out)
		}
	}
	f.expect(t, "robin", 200, "GET", idPath("/api/knowledge/%d", kid), nil)
	if out := f.expect(t, "robin", 200, "GET", "/api/requests", nil); !strings.Contains(out, "robin global request") {
		t.Errorf("robin lost his global request: %s", out)
	}
	f.expect(t, "robin", 200, "GET", fmt.Sprintf("/api/requests/%d", rid), nil)
}
