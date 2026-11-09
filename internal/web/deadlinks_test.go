package web

import (
	"html"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
)

var uiHrefRE = regexp.MustCompile(`href="(/ui/[^"#]*)`)

// settle follows redirects by hand (the test clients do not) and returns the
// status of the page they end on.
func settle(t *testing.T, c *http.Client, base, path string) (int, string) {
	t.Helper()
	for hop := 0; hop < 6; hop++ {
		resp, err := c.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		if resp.StatusCode < 300 || resp.StatusCode > 399 || loc == "" {
			return resp.StatusCode, path
		}
		path = loc
	}
	return 0, path
}

// Every page a person can open links only to pages that exist for them: 200, or
// a redirect that ends on a 200. Forms are not links (their targets are POST).
func TestNoPageLinksToADeadAddress(t *testing.T) {
	e := ovEnv(t)
	fillVisible(t, e.St)
	req := rqMake(t, e.St, rqSpec{Title: "Linked request", Criteria: []string{"one"}})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "A task"})
	for _, g := range []struct{ path, kind string }{{"internal", "dir"}, {"internal/server.go", "file"}, {"README.md", "file"}} {
		if _, err := e.St.PutGhostFile(ghostFile(shellProject, g.path, g.kind, "Server.")); err != nil {
			t.Fatal(err)
		}
	}
	pages := []string{"/ui/inbox", "/ui/overview", "/ui/agents", "/ui/tasks", "/ui/knowledge", "/ui/review",
		"/ui/context", "/ui/sessions", "/ui/coord", "/ui/orgs", "/ui/account/tokens", "/ui/profile", "/ui/device", "/ui/search?q=request",
		"/ui/x/shell", "/ui/x/shell/issues", "/ui/x/shell/pains", "/ui/x/shell/knowledge", "/ui/x/shell/sessions", "/ui/x/shell/rooms",
		"/ui/x/shell/tree/main/internal", "/ui/x/shell/blob/main/internal/server.go",
		"/ui/requests/" + itoa(req.Request.ID)}
	for name, c := range map[string]*http.Client{"owner": e.Owner, "member": e.Member, "guest": e.Guest} {
		seen := map[string]string{}
		var targets []string
		for _, page := range pages {
			status, final := settle(t, c, e.Base, page)
			if status == http.StatusNotFound && name == "guest" && (strings.HasPrefix(page, "/ui/agents") || strings.HasPrefix(page, "/ui/x/shell/rooms")) {
				continue
			}
			if status != http.StatusOK {
				t.Errorf("%s: GET %s ends on %d (%s)", name, page, status, final)
				continue
			}
			resp, err := c.Get(e.Base + final)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range uiHrefRE.FindAllStringSubmatch(body(t, resp), -1) {
				link := html.UnescapeString(m[1])
				if _, ok := seen[link]; !ok {
					seen[link] = page
					targets = append(targets, link)
				}
			}
		}
		slices.Sort(targets)
		for _, link := range targets {
			if status, final := settle(t, c, e.Base, link); status != http.StatusOK {
				t.Errorf("%s: %s links to %s, which ends on %d (%s)", name, seen[link], link, status, final)
			}
		}
		t.Logf("%s: %d distinct links checked", name, len(targets))
	}
}
