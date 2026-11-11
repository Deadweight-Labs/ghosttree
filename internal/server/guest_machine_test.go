package server

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// A guest sees no machine names: not in an entry, and not as a filter that
// would tell a machine with notes from one without.
func TestGuestSeesNoMachineOfAnEntryAndCannotProbeByMachineFilter(t *testing.T) {
	f := accessAPI(t, true)
	f.expect(t, "mia", 200, "POST", "/api/knowledge", map[string]any{
		"type": "instruction", "title": "box note", "body": "x",
		"scope": scope.Axes{Project: accProject, Machine: "mia-box"},
	})
	base := "/api/knowledge?project=" + accProject
	for who, wantMachine := range map[string]bool{"mia": true, "gus": false} {
		out := f.expect(t, who, 200, "GET", base+"&machine=mia-box", nil)
		if !strings.Contains(out, "box note") {
			t.Fatalf("%s: entry missing: %s", who, out)
		}
		if got := strings.Contains(out, "mia-box"); got != wantMachine {
			t.Errorf("%s: machine name in the list = %v, want %v: %s", who, got, wantMachine, out)
		}
	}
	// The filter must not separate entries of one machine from the rest for a guest.
	with := f.expect(t, "gus", 200, "GET", base+"&machine=mia-box", nil)
	without := f.expect(t, "gus", 200, "GET", base+"&machine=no-such-box", nil)
	if with != without {
		t.Errorf("the machine filter answers differently for a guest:\n%s\n%s", with, without)
	}
	if out := f.expect(t, "gus", 200, "GET", "/api/search?project="+accProject+"&machine=mia-box&q=box", nil); strings.Contains(out, "mia-box") {
		t.Errorf("search shows the machine to a guest: %s", out)
	}
}
