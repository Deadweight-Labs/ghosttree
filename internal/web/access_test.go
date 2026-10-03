package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestEveryWebRouteIsClassified(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := newApp(st).(*appHandler)
	registered := map[string]bool{}
	for _, pattern := range h.app.registered {
		registered[pattern] = true
		if _, ok := webRoutes[pattern]; !ok {
			t.Errorf("route %s is not classified", pattern)
		}
	}
	counts := map[webClass]int{}
	for pattern, class := range webRoutes {
		counts[class]++
		if !registered[pattern] {
			t.Errorf("table lists %s, which is not registered", pattern)
		}
	}
	t.Logf("%d web routes: %v", len(webRoutes), counts)
}

func TestNoWebRouteBypassesTheRegistrationHelper(t *testing.T) {
	fset := token.NewFileSet()
	entries, _ := os.ReadDir(".")
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && (sel.Sel.Name == "Handle" || sel.Sel.Name == "HandleFunc") {
				if recv, ok := sel.X.(*ast.Ident); ok && recv.Name == "mux" && !isHandleHelper(fset, call) {
					t.Errorf("%s: mux.%s bypasses a.handle", fset.Position(call.Pos()), sel.Sel.Name)
				}
			}
			return true
		})
	}
}

// isHandleHelper erlaubt die beiden Aufrufe innerhalb von app.handle selbst.
func isHandleHelper(fset *token.FileSet, call *ast.CallExpr) bool {
	src, err := os.ReadFile(fset.Position(call.Pos()).Filename)
	if err != nil {
		return false
	}
	line := strings.Split(string(src), "\n")[fset.Position(call.Pos()).Line-1]
	return strings.Contains(line, "mux.Handle(pattern, h)") || strings.Contains(line, "mux.HandleFunc(pattern, h)")
}

func TestWebPagesFollowVisibility(t *testing.T) {
	const project = "github.com/dw/p"
	st, err := store.Open(t.TempDir() + "/web.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tokens := map[string]string{}
	for i, name := range []string{"robin", "lena", "mia", "gus", "nora"} {
		if _, err := st.AddAccount(name, "", i == 0); err != nil {
			t.Fatal(err)
		}
		if tokens[name], _, err = st.CreateToken(name, store.TokenSpec{Label: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	org, err := st.CreateOrg("person:1", "Alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"person:2", "person:3", "person:4"} {
		code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
		if _, err := st.AcceptInvitation(who, code); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.EnsureProject("person:1", project); err != nil {
		t.Fatal(err)
	}
	for who, role := range map[string]string{"person:2": store.RoleLead, "person:3": store.RoleMember, "person:4": store.RoleGuest} {
		if err := st.SetProjectRole("person:1", project, who, role, false, store.RoleViaAPI); err != nil {
			t.Fatal(err)
		}
	}
	p := scope.Axes{Project: project}
	trusted, _ := st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "trusted pitfall", Body: "b", Scope: p, Person: "robin", Confidence: "trusted"})
	_, _ = st.InsertKnowledge(store.Knowledge{Type: "note", Title: "staged note", Body: "b", Scope: p, Person: "robin", Confidence: "staged"})
	_ = trusted
	detail, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "visible request", Scope: p, Person: "robin"}})
	if err != nil {
		t.Fatal(err)
	}
	sessions := map[string]string{}
	for name, acct := range map[string]int64{"robin": 1, "mia": 3} {
		id, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "s-" + name, AccountID: acct, Scope: p})
		if err != nil {
			t.Fatal(err)
		}
		_ = st.AppendChunks(id, []store.Chunk{{Seq: 0, Role: "user", Text: "transcript of " + name, Raw: "{}"}})
		stored, _ := st.SessionByID(id)
		sessions[name] = stored.PublicID
	}
	st.SetAccessMode(store.AccessMode{Enforce: true})
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)

	page := func(who, path string, want int) string {
		t.Helper()
		c := login(t, srv, tokens[who])
		resp, err := c.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		out := body(t, resp)
		if resp.StatusCode != want {
			t.Errorf("%s GET %s = %d, want %d", who, path, resp.StatusCode, want)
		}
		return out
	}
	reqPath := "/ui/requests/" + strconv.FormatInt(detail.Request.ID, 10)
	for _, who := range []string{"robin", "lena", "mia", "gus"} {
		if out := page(who, "/ui/requests", 200); !strings.Contains(out, "visible request") {
			t.Errorf("%s does not see the project's request", who)
		}
		page(who, reqPath, 200)
	}
	if out := page("nora", "/ui/requests", 200); strings.Contains(out, "visible request") {
		t.Errorf("stranger sees the request list entry")
	}
	page("nora", reqPath, 404)

	know := "/ui/knowledge?project=" + project
	// A hidden project filter answers like an unknown one (no existence oracle).
	hidden := page("nora", know, 200)
	unknown := page("nora", "/ui/knowledge?project=github.com/dw/unknown", 200)
	if strings.Contains(hidden, "trusted pitfall") || strings.Contains(hidden, "staged note") {
		t.Errorf("stranger sees knowledge of the hidden project")
	}
	csrfToken := regexp.MustCompile(`name="csrf[^"]*"(?: value="[^"]*")?`)
	norm := func(s, proj string) string {
		s = strings.ReplaceAll(s, proj, "PROJECT")
		return csrfToken.ReplaceAllString(s, `name="csrf" value="X"`)
	}
	if norm(hidden, project) != norm(unknown, "github.com/dw/unknown") {
		t.Errorf("hidden project knowledge list differs from unknown project list")
	}
	if out := page("gus", know, 200); !strings.Contains(out, "trusted pitfall") || strings.Contains(out, "staged note") {
		t.Errorf("guest knowledge page: trusted shown=%v staged hidden=%v", strings.Contains(out, "trusted pitfall"), !strings.Contains(out, "staged note"))
	}
	if out := page("mia", know, 200); !strings.Contains(out, "staged note") {
		t.Errorf("member does not see staged knowledge")
	}
	if out := page("gus", "/ui/review", 200); strings.Contains(out, "staged note") {
		t.Errorf("guest sees the review queue")
	}
	if out := page("nora", "/ui/context?project="+project, 200); strings.Contains(out, "trusted pitfall") {
		t.Errorf("stranger context preview shows project knowledge")
	}
	if out := page("mia", "/ui/context?project="+project, 200); !strings.Contains(out, "trusted pitfall") {
		t.Errorf("member context preview lacks project knowledge")
	}

	if out := page("mia", "/ui/sessions", 200); strings.Count(out, `class="srow`) != 2 {
		t.Errorf("member does not list sessions: %s", out)
	}
	if out := page("gus", "/ui/sessions", 200); strings.Contains(out, `class="srow`) {
		t.Errorf("guest lists sessions")
	}
	page("mia", "/ui/sessions/"+sessions["mia"], 200)
	page("mia", "/ui/sessions/"+sessions["robin"], 404)
	page("robin", "/ui/sessions/"+sessions["mia"], 200)
	page("lena", "/ui/sessions/"+sessions["mia"], 200)
	page("gus", "/ui/sessions/"+sessions["mia"], 404)
	page("nora", "/ui/sessions/"+sessions["mia"], 404)
	_ = http.StatusOK
}

// Im Log-Modus zeigt die Wissensseite jedem Konto dieselben Einträge wie der
// Store, ohne Kappung bei 50.
func TestWebKnowledgePageInLogModeIsUnfiltered(t *testing.T) {
	const project = "github.com/dw/p"
	srv, st, token := testWeb(t)
	for i := 0; i < 60; i++ {
		if _, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: "entry-" + strconv.Itoa(i), Body: "b", Scope: scope.Axes{Project: project}, Person: "x", Confidence: "staged"}); err != nil {
			t.Fatal(err)
		}
	}
	c := login(t, srv, token)
	resp, err := c.Get(srv.URL + "/ui/knowledge?project=" + project)
	if err != nil {
		t.Fatal(err)
	}
	out := body(t, resp)
	for i := 0; i < 60; i++ {
		if !strings.Contains(out, "entry-"+strconv.Itoa(i)+"<") && !strings.Contains(out, "entry-"+strconv.Itoa(i)+" ") && !strings.Contains(out, ">entry-"+strconv.Itoa(i)) {
			t.Fatalf("entry-%d missing from the page", i)
		}
	}
}
