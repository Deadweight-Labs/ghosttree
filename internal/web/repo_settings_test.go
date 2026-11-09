package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The settings of a repository: the tab and the page are there for those who may
// edit labels (lead and up), a member or guest gets neither, and the forms create,
// change and delete a label of the project.
func TestRepoSettingsTabAndLabels(t *testing.T) {
	e := ovEnv(t)
	settings := e.Base + "/ui/x/shell/settings"
	if _, page := fetchPage(t, e.Lead, e.Base+"/ui/x/shell"); !strings.Contains(page, `data-ic="settings"`) {
		t.Error("a lead has no settings tab")
	}
	for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest} {
		if code, _ := fetchPage(t, c, settings); code != http.StatusNotFound {
			t.Errorf("%s opens the settings: %d", name, code)
		}
		if _, page := fetchPage(t, c, e.Base+"/ui/x/shell"); strings.Contains(page, `data-ic="settings"`) {
			t.Errorf("%s sees the settings tab", name)
		}
	}
	form := func(c *http.Client, path string, v url.Values) *http.Response {
		v.Set("csrf_token", renderedCSRFToken(t, c, e.Base+"/ui/overview"))
		v.Set("project", shellProject)
		return sameOriginPostForm(t, c, e.Base+path, v)
	}
	if resp := form(e.Lead, "/ui/labels/create", url.Values{"name": {"ui"}, "color": {"0969da"}}); resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "done=created") {
		t.Fatalf("create = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp := form(e.Lead, "/ui/labels/create", url.Values{"name": {"UI"}, "color": {"#0969da"}}); !strings.Contains(resp.Header.Get("Location"), "fail=label_exists") {
		t.Errorf("a second label of the same name: %s", resp.Header.Get("Location"))
	}
	if resp := form(e.Member, "/ui/labels/create", url.Values{"name": {"member-label"}, "color": {"0969da"}}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("a member may create labels (rule: from member): %d", resp.StatusCode)
	}
	_, page := fetchPage(t, e.Lead, settings)
	for _, want := range []string{`class="lb lc-0969da"`, ">ui<", `action="/ui/labels/`, "member-label"} {
		if !strings.Contains(page, want) {
			t.Errorf("settings lack %q", want)
		}
	}
	list, err := e.St.Access(store.Principal{ID: "person:4", Label: "lars"}).ListLabels(shellProject)
	if err != nil || len(list) != 2 {
		t.Fatalf("labels = %v %v", list, err)
	}
	id := list[0].ID
	if resp := form(e.Lead, "/ui/labels/"+strconv.FormatInt(id, 10)+"/update", url.Values{"name": {"design"}, "color": {"#fbca04"}, "description": {"Aussehen"}}); !strings.Contains(resp.Header.Get("Location"), "done=updated") {
		t.Errorf("update: %s", resp.Header.Get("Location"))
	}
	if resp := form(e.Member, "/ui/labels/"+strconv.FormatInt(id, 10)+"/delete", url.Values{}); resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "fail=forbidden") {
		t.Errorf("a member deleting a label: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp := form(e.Lead, "/ui/labels/"+strconv.FormatInt(id, 10)+"/delete", url.Values{}); !strings.Contains(resp.Header.Get("Location"), "done=deleted") {
		t.Errorf("delete: %s", resp.Header.Get("Location"))
	}
}
