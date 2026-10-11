package web

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The invitation page says what the invitee shares and sees before the
// decision to join (REQ-438 D).
func TestJoinPageSaysWhatIsSharedAndWhatTheRoleSees(t *testing.T) {
	srv, st, _, org, _ := joinWeb(t)
	for role, want := range map[string][]string{
		store.RoleMember: {
			"Your sessions stay private. Only you see them until you share one.",
			"Members of " + joinProject + " see its knowledge, requests and room messages.",
		},
		store.RoleGuest: {
			"Your sessions stay private. Only you see them until you share one.",
			"As a guest you see released knowledge, requests and the room of " + joinProject + ", and only the sessions shared with guests.",
		},
	} {
		code := projectInvite(t, st, org, role)
		resp, err := anonClient().Get(srv.URL + "/join/" + code)
		if err != nil {
			t.Fatal(err)
		}
		page := body(t, resp)
		for _, w := range want {
			if !strings.Contains(page, w) {
				t.Errorf("%s page lacks %q: %s", role, w, page)
			}
		}
		other := "As a guest you see"
		if role == store.RoleGuest {
			other = "Members of "
		}
		if strings.Contains(page, other) {
			t.Errorf("%s page shows the sentence of the other role", role)
		}
	}
}
