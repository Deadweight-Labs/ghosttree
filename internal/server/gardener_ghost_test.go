package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
)

// putGhostAt writes a description as who, with the anchor header a client sends.
func (f *accessAPIFixture) putGhostAt(t *testing.T, who string, g map[string]any, anchor string) (int, string) {
	t.Helper()
	b, _ := json.Marshal(g)
	r, err := http.NewRequest("POST", f.srv.URL+"/api/ghosts", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+f.tok[who])
	if anchor != "" {
		r.Header.Set(gitanchor.Header, gitanchor.Encode(gitanchor.Anchor{Commit: anchor, Branch: "main"}))
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

// REQ-629: the gardener's account writes a description itself, on the merge
// commit it describes. Nobody else may, it may not without that reference and
// anchor, and never on a branch.
func TestTheGardenerWritesADescriptionOnTheMergeCommitAndNobodyElseMay(t *testing.T) {
	f := gardenerAPI(t)
	merge, other := strings.Repeat("a", 40), strings.Repeat("b", 40)
	ghost := func(path, ref, text string) map[string]any {
		return map[string]any{"project": accProject, "path": path, "kind": "file", "description": text, "harness": "gardener", "session_ref": ref,
			"content_sha": strings.Repeat("c", 64), "git_blob": strings.Repeat("d", 40), "line_count": 3}
	}
	text := "Hält den Zähler des Dienstes. Invariante: genau ein Schreiber."
	before := countGhosts(t, f)
	for _, who := range []string{"robin", "lena", "mia"} {
		if code, out := f.putGhostAt(t, who, ghost("internal/a.go", "gardener:merge:"+merge, text), merge); code != 403 || !strings.Contains(out, "gardener_reserved") {
			t.Errorf("%s writing as the gardener = %d %s", who, code, out)
		}
	}
	for name, c := range map[string]struct {
		g      map[string]any
		anchor string
	}{
		"no anchor":           {ghost("internal/a.go", "gardener:merge:"+merge, text), ""},
		"another anchor":      {ghost("internal/a.go", "gardener:merge:"+merge, text), other},
		"short commit":        {ghost("internal/a.go", "gardener:merge:aaaaaaa", text), "aaaaaaa"},
		"a note's reference":  {ghost("internal/a.go", "gardener:ghost-note:5", text), merge},
		"another harness":     {ghost("internal/a.go", "gardener:merge:"+merge, text), merge},
		"harness in capitals": {ghost("internal/a.go", "gardener:merge:"+merge, text), merge},
		"on a branch":         {ghost("internal/a.go", "gardener:merge:"+merge, text), merge},
	} {
		switch name {
		case "another harness":
			c.g["harness"] = "mcp"
		case "harness in capitals":
			c.g["harness"] = "Gardener"
		case "on a branch":
			c.g["branch"] = "feat/x"
		}
		code, out := f.putGhostAt(t, "gardener", c.g, c.anchor)
		if code != 403 || !strings.Contains(out, "gardener_reserved") {
			t.Errorf("%s: = %d %s, want 403", name, code, out)
		}
	}
	if n := countGhosts(t, f); n != before {
		t.Fatalf("%d descriptions written by refused calls", n-before)
	}

	if code, out := f.putGhostAt(t, "gardener", ghost("internal/a.go", "gardener:merge:"+merge, text), merge); code != 200 {
		t.Fatalf("the gardener's description = %d %s", code, out)
	}
	g, err := f.st.GhostFileByPath(accProject, "internal/a.go")
	if err != nil || g.Harness != "gardener" || g.Person != "gardener" || g.SessionRef != "gardener:merge:"+merge || g.GitAnchor == nil || g.GitAnchor.Commit != merge {
		t.Fatalf("stored = %+v (%v)", g, err)
	}
	if code, out := f.putGhostAt(t, "gardener", ghost("internal/b.go", "gardener:run:"+other, text), other); code != 200 {
		t.Errorf("a description of a run = %d %s", code, out)
	}
}

// A person adds a paragraph to the gardener's description. The gardener may then
// rewrite its own paragraph, but the server refuses a text that drops or changes
// the person's.
func TestTheGardenerKeepsTheParagraphsOfOthersWordForWord(t *testing.T) {
	f := gardenerAPI(t)
	merge, later := strings.Repeat("a", 40), strings.Repeat("e", 40)
	ghost := func(who, text, commit string) (int, string) {
		g := map[string]any{"project": accProject, "path": "internal/a.go", "kind": "file", "description": text,
			"content_sha": strings.Repeat("c", 64), "git_blob": strings.Repeat("d", 40), "line_count": 3}
		if who == "gardener" {
			g["harness"], g["session_ref"] = "gardener", "gardener:merge:"+commit
		} else {
			g["harness"] = "mcp"
		}
		return f.putGhostAt(t, who, g, commit)
	}
	own := "Hält den Zähler des Dienstes. Invariante: genau ein Schreiber."
	human := "Hier lief der Export einmal doppelt; das Flag verhindert es."
	if code, out := ghost("gardener", own, merge); code != 200 {
		t.Fatalf("first description = %d %s", code, out)
	}
	if code, out := ghost("mia", own+"\n\n"+human, merge); code != 200 {
		t.Fatalf("the person's addition = %d %s", code, out)
	}
	for name, text := range map[string]string{
		"dropped": "Zählt jetzt zwei Dinge. Invariante: genau ein Schreiber je Ding.",
		"changed": "Zählt jetzt zwei Dinge.\n\n" + strings.Replace(human, "doppelt", "zweimal", 1),
	} {
		if code, out := ghost("gardener", text, later); code != 409 || !strings.Contains(out, "kept_paragraphs") {
			t.Errorf("the gardener's text with the person's paragraph %s = %d %s, want 409", name, code, out)
		}
	}
	rewrite := "Zählt jetzt zwei Dinge. Invariante: genau ein Schreiber je Ding.\n\n" + human
	if code, out := ghost("gardener", rewrite, later); code != 200 {
		t.Fatalf("the gardener rewriting its own paragraph = %d %s", code, out)
	}
	if g, _ := f.st.GhostFileByPath(accProject, "internal/a.go"); g.Description != rewrite || g.GitAnchor == nil || g.GitAnchor.Commit != later {
		t.Errorf("stored = %+v", g)
	}
}

func countGhosts(t *testing.T, f *accessAPIFixture) int {
	t.Helper()
	var n int
	if err := f.st.DB().QueryRow(`SELECT COUNT(*) FROM ghost_files`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
