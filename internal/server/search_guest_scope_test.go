package server

import (
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Gast in A, Mitglied in B: branch:, package: und for: der API-Suche raten in A
// nichts, nur weil der Betrachter in B Mitglied ist.
func TestAPISearchBranchPackageAndAssigneeAreNoOracleForTheGuestOfTheProject(t *testing.T) {
	w := newQWorld(t)
	if err := w.f.st.SetProjectRole("person:1", accOther, "person:5", store.RoleMember, false, store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	pk := decode[[]requestdomain.PackageDetail](t, "["+w.f.expect(t, "mia", 200, "GET", "/api/packages?project="+accProject+"&branch=feat/x", nil)+"]")
	exists := func(id int64) string { return "package:" + i64(id) }
	for _, tc := range []struct{ yes, no string }{
		{"kind:request branch:feat/y", "kind:request branch:feat/nope"},
		{"kind:request branch:feat/x", "kind:request branch:feat/nope"},
		{"kind:request " + exists(pk[0].ID), "kind:request package:99999"},
		{"kind:task for:mia", "kind:task for:nobody"},
		{"is:pain branch:feat/y", "is:pain branch:feat/nope"},
	} {
		yes, no := w.refs(t, "gus", tc.yes), w.refs(t, "gus", tc.no)
		if strings.Join(yes, " ") != strings.Join(no, " ") {
			t.Errorf("guest of A: %q = %v, but %q = %v", tc.yes, yes, tc.no, no)
		}
	}
	// Der Standard-Branch verrät dem Gast nichts; ein Mitglied findet weiter.
	if got := w.refs(t, "mia", "kind:request branch:feat/x"); len(got) != 1 {
		t.Errorf("the member of A lost the branch filter: %v", got)
	}
}
