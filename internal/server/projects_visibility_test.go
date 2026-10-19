package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func projectRemotes(t *testing.T, f orgFixture, token, path string) []string {
	t.Helper()
	resp := req(t, "GET", f.srv.URL+path, token, nil)
	defer resp.Body.Close()
	var list []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, p := range list {
		out = append(out, p["remote"].(string))
	}
	return out
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Org-Mitgliedschaft allein zeigt keine Projektnamen (Spec 8).
func TestProjectListHidesProjectsWithoutRole(t *testing.T) {
	const mine, foreign = apiRoleProject, "github.com/dw/foreign"
	setup := func(t *testing.T) orgFixture {
		f, _ := roleAPI(t)
		if _, err := f.st.EnsureProject("person:1", foreign); err != nil {
			t.Fatal(err)
		}
		// anna (person:2) bekommt nur auf "mine" eine Rolle; ben (person:3) auf keine.
		if err := f.st.SetProjectRole("person:1", mine, "person:2", store.RoleMember, false, store.RoleViaAPI); err != nil {
			t.Fatal(err)
		}
		return f
	}

	t.Run("enforce", func(t *testing.T) {
		f := setup(t)
		f.st.SetAccessMode(store.AccessMode{Enforce: true})
		if got := projectRemotes(t, f, f.robin, "/api/projects"); !has(got, mine) || !has(got, foreign) {
			t.Fatalf("owner must see all projects, got %v", got)
		}
		got := projectRemotes(t, f, f.anna, "/api/projects?org=alpha")
		if !has(got, mine) || has(got, foreign) {
			t.Fatalf("member sees only projects with a role, got %v", got)
		}
		if got := projectRemotes(t, f, f.ben, "/api/projects"); len(got) != 0 {
			t.Fatalf("member without any role must see nothing, got %v", got)
		}
	})

	t.Run("log mode keeps the old list and logs would deny", func(t *testing.T) {
		f := setup(t)
		var buf bytes.Buffer
		f.st.SetAccessMode(store.AccessMode{Logger: slog.New(slog.NewTextHandler(&buf, nil))})
		if got := projectRemotes(t, f, f.ben, "/api/projects"); !has(got, mine) || !has(got, foreign) {
			t.Fatalf("log mode must not filter, got %v", got)
		}
		if !bytes.Contains(buf.Bytes(), []byte("access: would deny")) {
			t.Fatalf("expected a would-deny line, got %q", buf.String())
		}
	})
}
