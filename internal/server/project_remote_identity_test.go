package server

import "testing"

// A project made from a repository address is the project of every agent that
// works in a clone of that repository, whichever way its git remote is spelled:
// the entries land in it and no second project appears.
func TestAgentInACloneLandsInTheProjectMadeFromItsRemote(t *testing.T) {
	f := newOrgFixture(t)
	f.mustCall(t, 201, "POST", "/api/orgs", f.robin, map[string]any{"name": "Alpha", "slug": "alpha"})
	for _, tok := range []string{f.anna, f.ben} {
		inv := f.invite(t, "person:1", "alpha", "", "member")
		f.mustCall(t, 200, "POST", "/api/invitations/accept", tok, map[string]any{"code": inv["code"]})
	}
	f.mustCall(t, 200, "POST", "/api/projects/claim", f.anna, map[string]any{"remote": "https://gitlab.com/werkstatt/deploy-hook.git", "org": "alpha"})
	count := func() int {
		var n int
		if err := f.st.DB().QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	for _, spelling := range []string{"git@gitlab.com:werkstatt/deploy-hook.git", "ssh://git@gitlab.com/werkstatt/deploy-hook", "https://GitLab.com/werkstatt/deploy-hook/"} {
		out := f.mustCall(t, 201, "POST", "/api/requests", f.ben, map[string]any{"type": "feature", "title": "from " + spelling, "project": spelling})
		_ = out
	}
	if got := count(); got != before {
		t.Fatalf("a second project appeared: %d -> %d", before, got)
	}
	var n int
	if err := f.st.DB().QueryRow(`SELECT COUNT(*) FROM requests WHERE project='gitlab.com/werkstatt/deploy-hook'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("the requests landed in the project: %d (%v)", n, err)
	}
}
