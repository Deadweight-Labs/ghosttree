package store

import (
	"testing"
	"time"
)

// The list of sessions names the folders a session worked in, heaviest first,
// from the path activity of its own account only.
func TestSessionAreasNameTheFoldersOfOneAccountsSession(t *testing.T) {
	st := openTest(t)
	now := time.Now().UTC().Format(time.RFC3339)
	var events []PathActivity
	for _, p := range []string{"internal/web/a.go", "internal/web/b.go", "internal/store/x.go", "cmd/ctx/main.go", "README.md"} {
		events = append(events, PathActivity{SessionExternalID: "s1", Tool: "Edit", Path: p, Writes: true, Quality: ActivityIntent, At: now})
	}
	events = append(events, PathActivity{SessionExternalID: "s1", Tool: "Edit", Path: "other/y.go", Quality: ActivityIntent, At: now, AccountID: 99})
	if err := st.RecordPathActivity(events); err != nil {
		t.Fatal(err)
	}
	got := st.SessionAreas([]Session{{ID: 7, ExternalID: "s1"}, {ID: 8, ExternalID: "none"}}, 2)
	if len(got[7]) != 2 || got[7][0] != "internal/web" {
		t.Errorf("areas = %v, want two with internal/web first", got[7])
	}
	for _, a := range got[7] {
		if a == "other" || a == "." {
			t.Errorf("area %q: the root and another account's folders stay out", a)
		}
	}
	if _, ok := got[8]; ok {
		t.Error("a session without paths has no areas")
	}
}
