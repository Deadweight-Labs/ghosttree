package mcpserver

import "testing"

// Die Koordinationsidentität darf von der Harness-Session abweichen, ohne dass
// Suche, unterbrochene Arbeit und Snapshots ihre echte Session-Ref verlieren.
func TestCoordRefOverrideLeavesSessionRefAlone(t *testing.T) {
	s := &Server{}
	s.SetSessionRef("sess-1")
	if s.coordRef() != "sess-1" {
		t.Fatalf("without override coord uses the session: %q", s.coordRef())
	}
	s.SetCoordRef("claude:h:42")
	if s.coordRef() != "claude:h:42" {
		t.Fatalf("coord ref: %q", s.coordRef())
	}
	if s.sessionRef != "sess-1" {
		t.Fatalf("session ref changed: %q", s.sessionRef)
	}
}
