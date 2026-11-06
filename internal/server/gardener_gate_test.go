package server

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-457: harness "gardener" and "gardener:" references are what the review page
// acts on, so only the account whose number is set as the gardener's (`ctx account
// set-gardener`) may write them. The number, not a name: a name is what a person
// may change for themselves.

// gardenerAPI is the access fixture with one more account, the gardener's, which
// is a member of the project, and set as the gardener's.
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
	if err := f.st.SetGardenerAccount(7); err != nil {
		t.Fatal(err)
	}
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

// "Proposed by the gardener, confirmed by <person>" on a description is set only by
// the review page. Through the API nobody may claim it, not the gardener's own
// account either (it proposes, it does not describe), and an ordinary description
// is written as before.
func TestNobodyMayWriteAGardenerMarkOnADescriptionThroughTheAPI(t *testing.T) {
	f := gardenerAPI(t)
	ghost := func(harness, ref string) map[string]any {
		return map[string]any{"project": accProject, "path": "internal/x.go", "kind": "file", "description": "Eine Beschreibung.", "harness": harness, "session_ref": ref,
			"content_sha": strings.Repeat("a", 64), "git_blob": strings.Repeat("b", 40), "line_count": 3}
	}
	count := func() int {
		var n int
		if err := f.st.DB().QueryRow(`SELECT COUNT(*) FROM ghost_files`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	for _, who := range []string{"gardener", "robin", "lena", "rex", "mia"} {
		for name, g := range map[string]map[string]any{
			"harness":               ghost("gardener", ""),
			"harness in other case": ghost("Gardener", ""),
			"reference":             ghost("mcp", "gardener:ghost-note:5"),
			"reference in upper":    ghost("mcp", "GARDENER:ghost-note:5"),
		} {
			code, out := f.call(t, who, "POST", "/api/ghosts", g)
			if code != 403 || !strings.Contains(out, "gardener_reserved") {
				t.Errorf("%s writing a description with a mark in its %s = %d (%s), want 403 gardener_reserved", who, name, code, strings.TrimSpace(out))
			}
		}
	}
	if n := count(); n != before {
		t.Fatalf("%d descriptions written by refused calls", n-before)
	}
	f.expect(t, "mia", 200, "POST", "/api/ghosts", ghost("mcp", ""))
	if g, err := f.st.GhostFileByPath(accProject, "internal/x.go"); err != nil || g.Harness != "mcp" || g.Person != "mia" {
		t.Errorf("an ordinary description = %+v (%v)", g, err)
	}
}

func TestNobodyMayWriteGardenerMarksWhenNoAccountIsSet(t *testing.T) {
	f := gardenerAPI(t)
	if err := f.st.ClearGardenerAccount(); err != nil {
		t.Fatal(err)
	}
	out := f.expect(t, "gardener", 403, "POST", "/api/knowledge", gardenerNote("gardener", "gardener:regression:1:existing"))
	if !strings.Contains(out, "gardener_reserved") || !strings.Contains(out, "ctx account set-gardener") {
		t.Errorf("the refusal does not say how to set it up: %s", out)
	}
	f.expect(t, "robin", 403, "POST", "/api/knowledge", gardenerNote("gardener", ""))
	// And what is not the gardener's still works for everyone, set or not.
	f.expect(t, "gardener", 200, "POST", "/api/knowledge", gardenerNote("claude", ""))
	f.expect(t, "mia", 200, "POST", "/api/knowledge", gardenerNote("gardenerbot", "gardener"))
}

// The role is the account's number. Renaming the gardener's account does not
// take it away, and an account that gets the gardener's name does not get it.
func TestTheGardenersRoleDoesNotFollowTheName(t *testing.T) {
	f := gardenerAPI(t)
	ref := "gardener:regression:1:existing"
	// Whoever may rename themselves renames the gardener's account (the person who
	// owns it) and gives its old name to the account of a member.
	if _, err := f.st.SetOwnName("person:7", "garden-keeper"); err != nil {
		t.Fatal(err)
	}
	f.expect(t, "gardener", 200, "POST", "/api/knowledge", gardenerNote("gardener", ref))
	for _, q := range []string{`UPDATE persons SET name='garden-keeper-2' WHERE id=7`, `UPDATE persons SET name='gardener' WHERE id=3`} {
		if _, err := f.st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if a, _ := f.st.AccountByName("gardener"); a.ID != "person:3" {
		t.Fatalf("the name did not move: %+v", a)
	}
	// mia (person:3) now carries the name "gardener".
	out := f.expect(t, "mia", 403, "POST", "/api/knowledge", gardenerNote("gardener", ref))
	if !strings.Contains(out, "gardener_reserved") {
		t.Errorf("refused without saying why: %s", out)
	}
	f.expect(t, "mia", 403, "POST", "/api/knowledge", gardenerNote("", ref))
	f.expect(t, "gardener", 200, "POST", "/api/knowledge", gardenerNote("gardener", ref))
}

// Nothing a person can reach over HTTP sets the gardener's account: the only
// callers of the two functions that write it are the account command and tests.
// The packages that answer requests (the API, the web pages, the MCP server) are
// read for them, so a new route that did would fail here and be looked at.
func TestNoRequestHandlerSetsTheGardenersAccount(t *testing.T) {
	for _, dir := range []string{".", "../web", "../mcpserver", "../client"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			raw, err := os.ReadFile(dir + "/" + e.Name())
			if err != nil {
				t.Fatal(err)
			}
			for _, call := range []string{"SetGardenerAccount(", "ClearGardenerAccount(", "gardener_account"} {
				if strings.Contains(string(raw), call) {
					t.Errorf("%s/%s mentions %s: a request must not set the gardener's account", dir, e.Name(), call)
				}
			}
		}
	}
	// And the account stays what it was after requests of people.
	f := gardenerAPI(t)
	for _, who := range []string{"robin", "lena", "mia"} {
		f.call(t, who, "POST", "/api/knowledge", map[string]any{"type": "note", "title": "gardener_account", "body": "1", "scope": map[string]string{"project": accProject}})
	}
	if id := f.st.GardenerAccountID(); id != 7 {
		t.Errorf("the gardener's account is %d after requests of people", id)
	}
}
