package query

import "testing"

// REQ-616: ein akzeptierter Pain zählt in der Suchsprache als geschlossen.
func TestAcceptedPainIsClosed(t *testing.T) {
	if got := StateClass(KindPain, "accepted"); got != ClassClosed {
		t.Fatalf("StateClass(pain, accepted) = %q, want closed", got)
	}
	f := Facts{Kind: KindPain, State: "accepted"}
	if match(t, "is:open", f) {
		t.Error("an accepted pain is not open")
	}
	if !match(t, "is:closed", f) || !match(t, "is:accepted", f) || match(t, "is:done", f) {
		t.Error("an accepted pain is closed and is:accepted, but not done")
	}
	// Die Zustände davor und danach behalten ihre Klasse.
	for state, class := range map[string]string{"active": ClassOpen, "mitigated": ClassOpen, "resolved": ClassDone, "obsolete": ClassClosed} {
		if got := StateClass(KindPain, state); got != class {
			t.Errorf("StateClass(pain, %s) = %q, want %q", state, got, class)
		}
	}
}
