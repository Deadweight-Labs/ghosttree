package server

import (
	"strings"
	"testing"
)

// Teilen einer Session ohne Projekt meldet nicht mehr Erfolg, sondern sagt warum
// es nicht geht (Abnahme 2026-10-11, Befund 2).
func TestSharingASessionWithoutProjectIsRefusedWithAReason(t *testing.T) {
	f := accessAPI(t, true)
	sid := workSession(t, f, "no-project-rex", 4, "")
	code, out := f.call(t, "rex", "PUT", idPath("/api/sessions/%d/share", sid), map[string]bool{"shared": true})
	if code != 409 || !strings.Contains(out, "share_needs_project") || !strings.Contains(out, "no project") {
		t.Fatalf("share without project: %d %s", code, out)
	}
	// Taking a share back stays possible.
	f.expect(t, "rex", 200, "PUT", idPath("/api/sessions/%d/share", sid), map[string]bool{"shared": false})
}
