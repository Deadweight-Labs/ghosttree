package links

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Decision #4331: a machine has no repository, its page is /ui/m/{machine}.
func TestMachineAddressAndCard(t *testing.T) {
	b := New("https://gt.example.org", "github.com/x/y")
	if got := b.Machine("Build-1"); got != "https://gt.example.org/ui/m/build-1" {
		t.Errorf("address = %q", got)
	}
	for _, bad := range []string{"", "../etc", "a/b", "x y"} {
		if got := b.Machine(bad); got != "" {
			t.Errorf("address for %q = %q, want none", bad, got)
		}
	}
	if got := (Builder{}).Machine("build-1"); got != "" {
		t.Errorf("a builder without a server made %q", got)
	}
	c := b.MachineCard(store.Machine{Name: "build-1", Owner: "robin", OwnerOrg: "dw", Relation: store.RelMaintainer,
		Shares: []store.MachineShare{{Org: "partner", Role: "read"}}})
	for _, want := range []string{"Machine build-1", "state: maintainer", "shared with partner (read)", "with: dw", "link: https://gt.example.org/ui/m/build-1"} {
		if !strings.Contains(c.String(), want) {
			t.Errorf("card lacks %q:\n%s", want, c.String())
		}
	}
}
