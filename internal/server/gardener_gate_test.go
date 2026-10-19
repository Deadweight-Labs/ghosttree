package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-457: harness "gardener" and "gardener:" references are what the review page
// acts on, so only the account the server names as the gardener's may write them.

// gardenerAPI is the access fixture with one more account, the gardener's, which
// is a member of the project, and named as the gardener's.
func gardenerAPI(t *testing.T) *accessAPIFixture {
	t.Helper()
	f := accessAPI(t, true)
	if _, err := f.st.AddAccount("gardener", "", false); err != nil {
		t.Fatal(err)
	}
	var err error
	if f.tok["gardener"], _, err = f.st.CreateToken("gardener", store.TokenSpec{Label: "garden"}); err != nil {
		t.Fatal(err)
	}
	code, _, err := f.st.CreateInvitation("person:1", 1, "", store.OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AcceptInvitation("person:7", code); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetProjectRole("person:1", accProject, "person:7", store.RoleMember, false, store.RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	f.st.SetGardenerAccount("gardener")
	return f
}

func gardenerNote(harness, ref string) map[string]any {
	return map[string]any{"type": "note", "title": "[Gärtner] Test vorhanden? #1: x", "body": "Test: a/x_test.go:TestX", "scope": map[string]string{"project": accProject},
		"confidence": "staged", "origin": "distilled", "harness": harness, "session_ref": ref}
}

func countKnowledge(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM knowledge`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOnlyTheGardenersAccountMayWriteGardenerMarks(t *testing.T) {
	f := gardenerAPI(t)
	ref := "gardener:regression:1:existing"

	// The gardener's account: the note is written, as its own, and counts as its.
	var saved store.Knowledge
	body := f.expect(t, "gardener", 200, "POST", "/api/knowledge", gardenerNote("gardener", ref))
	if err := json.Unmarshal([]byte(body), &saved); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	if saved.Harness != "gardener" || saved.SessionRef != ref || !f.st.WrittenByGardener(saved) {
		t.Errorf("saved = %+v", saved)
	}

	before := countKnowledge(t, f.st)
	// Everyone else is refused, whatever their role: the instance owner, a lead,
	// a reviewer, a member, a guest and an outsider.
	for _, who := range []string{"robin", "lena", "rex", "mia", "gus", "nora"} {
		for name, n := range map[string]map[string]any{
			"harness and reference": gardenerNote("gardener", ref),
			"harness only":          gardenerNote("gardener", ""),
			"reference only":        gardenerNote("", ref),
			"harness in other case": gardenerNote("Gardener", ""),
			"harness with space":    gardenerNote(" gardener ", ""),
			"reference in upper":    gardenerNote("claude", "GARDENER:relation:duplicate:1-2"),
		} {
			code, out := f.call(t, who, "POST", "/api/knowledge", n)
			if code != 403 && code != 404 {
				t.Errorf("%s writing %s = %d (%s), want a refusal", who, name, code, strings.TrimSpace(out))
			}
			if who != "nora" && who != "gus" && code == 403 && !strings.Contains(out, "gardener_reserved") {
				t.Errorf("%s writing %s: refused without saying why: %s", who, name, out)
			}
		}
	}
	// The ones who may write at all (a lead, a reviewer, a member, the owner) are refused for this and only this:
	for _, who := range []string{"robin", "lena", "rex", "mia"} {
		f.expect(t, who, 403, "POST", "/api/knowledge", gardenerNote("gardener", ref))
		f.expect(t, who, 200, "POST", "/api/knowledge", gardenerNote("claude", "session-less"))
	}
	if got := countKnowledge(t, f.st) - before; got != 4 {
		t.Errorf("%d entries written, want the 4 plain ones and no gardener entry", got)
	}
}

func TestGardenerMarksCannotRideOnASupersedeOrTheMigrationPath(t *testing.T) {
	f := gardenerAPI(t)
	old := f.id["k-mia"]
	before := countKnowledge(t, f.st)

	n := gardenerNote("gardener", "gardener:relation:duplicate:1-2")
	n["supersedes"] = []int64{old}
	n["supersede_reason"] = "Gärtner-Vorschlag"
	f.expect(t, "lena", 403, "POST", "/api/knowledge", n)
	if k, _ := f.st.KnowledgeByID(old); k.Status != "active" || k.SupersededBy != 0 {
		t.Errorf("the entry was superseded by a refused write: %+v", k)
	}

	// The path of the migration takes the whole entry from the client too.
	for _, who := range []string{"lena", "mia", "robin"} {
		f.expect(t, who, 403, "POST", "/api/migrated-knowledge", map[string]any{"Knowledge": gardenerNote("gardener", "gardener:regression:1:existing")})
		f.expect(t, who, 403, "POST", "/api/migrated-knowledge", map[string]any{"Knowledge": gardenerNote("", "gardener:regression:1:existing")})
	}
	if got := countKnowledge(t, f.st); got != before {
		t.Errorf("%d entries written by refused calls", got-before)
	}
	// An entry cannot be turned into one afterwards either: harness and reference
	// are not among the fields a patch may change.
	f.expect(t, "lena", 400, "PATCH", fmt.Sprintf("/api/knowledge/%d", old), map[string]string{"harness": "gardener"})
	f.expect(t, "lena", 400, "PATCH", fmt.Sprintf("/api/knowledge/%d", old), map[string]string{"session_ref": "gardener:regression:1:existing"})
}

func TestNobodyMayWriteGardenerMarksWhenNoAccountIsNamed(t *testing.T) {
	f := gardenerAPI(t)
	f.st.SetGardenerAccount("")
	out := f.expect(t, "gardener", 403, "POST", "/api/knowledge", gardenerNote("gardener", "gardener:regression:1:existing"))
	if !strings.Contains(out, "gardener_reserved") || !strings.Contains(out, "GHOSTTREE_GARDENER_ACCOUNT") {
		t.Errorf("the refusal does not say how to set it up: %s", out)
	}
	f.expect(t, "robin", 403, "POST", "/api/knowledge", gardenerNote("gardener", ""))
	// A name that no account holds is the same.
	f.st.SetGardenerAccount("nobody")
	f.expect(t, "gardener", 403, "POST", "/api/knowledge", gardenerNote("gardener", ""))
	// And what is not the gardener's still works for everyone, named or not.
	f.expect(t, "gardener", 200, "POST", "/api/knowledge", gardenerNote("claude", ""))
	f.expect(t, "mia", 200, "POST", "/api/knowledge", gardenerNote("gardenerbot", "gardener"))
}
