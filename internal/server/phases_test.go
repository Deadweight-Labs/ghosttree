package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The two hook routes write their partial times into the http_request line, as
// the group "phases" in milliseconds (PAIN-76). Other routes write none.
func TestHookRoutesLogTheirPhases(t *testing.T) {
	var logs bytes.Buffer
	h, token := newAuditHandler(t, &logs)
	get := func(path string) {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", path, w.Code, w.Body)
		}
	}
	get("/api/context/bootstrap?project=github.com/x/y&session=s1")
	get("/api/context/relevant?q=hlpvpngw+prüfen&session=s1")
	get("/api/whoami")

	events := decodeAuditEvents(t, &logs)
	if len(events) != 3 {
		t.Fatalf("events = %d: %s", len(events), logs.String())
	}
	want := map[string][]string{
		"/api/context/bootstrap": {"pool_wait", "access", "knowledge", "rights", "claim", "render"},
		"/api/context/relevant":  {"pool_wait", "access"},
	}
	for _, event := range events {
		route, _ := event["route"].(string)
		phases, has := event["phases"].(map[string]any)
		names, measured := want[route]
		if !measured {
			if has {
				t.Errorf("%s logs phases: %v", route, phases)
			}
			continue
		}
		if !has {
			t.Fatalf("%s logs no phases: %v", route, event)
		}
		for _, name := range names {
			v, ok := phases[name].(float64)
			if !ok || v < 0 {
				t.Errorf("%s: phase %q = %v, want milliseconds", route, name, phases[name])
			}
		}
	}
}

func TestPhaseSetAddsRepeatedNamesAndIsSafeWhenNil(t *testing.T) {
	var none *phaseSet
	none.start("x")()
	if none.attr() != nil {
		t.Fatal("nil set has an attribute")
	}
	p := &phaseSet{}
	p.add("a", 2*time.Millisecond)
	p.add("b", time.Millisecond)
	p.add("a", 3*time.Millisecond)
	attr := p.attr()
	if attr == nil || attr.Key != "phases" {
		t.Fatalf("attr = %v", attr)
	}
	group := attr.Value.Group()
	if len(group) != 2 || group[0].Key != "a" || group[0].Value.Float64() != 5 || group[1].Key != "b" {
		t.Fatalf("group = %v", group)
	}
}
