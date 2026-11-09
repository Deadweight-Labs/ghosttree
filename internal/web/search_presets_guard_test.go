package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Eine gespeicherte Suche verdeckt keine mitgelieferte, und die Suche eines
// anderen überschreibt nur, wer sie auch löschen dürfte.
func TestSavedSearchCannotShadowASuppliedOneNorOverwriteAnothers(t *testing.T) {
	e := qualEnv(t)
	save := func(c *http.Client, name, scopeKind, q string) int {
		form := url.Values{"project": {shellProject}, "q": {q}, "name": {name}, "scope": {scopeKind}, "back": {"/ui/search"}}
		return postForm(t, e, c, "/ui/searches", form).StatusCode
	}
	for _, scopeKind := range []string{"user", "project", "org"} {
		if code := save(e.Owner, "pr-ergebnis", scopeKind, "kind:knowledge Needle"); code != http.StatusBadRequest {
			t.Errorf("a %s search named like a supplied one -> %d, want 400", scopeKind, code)
		}
	}
	// A search that was saved under such a name before never wins over the supplied one.
	if _, err := e.St.DB().Exec(`INSERT INTO search_presets(scope_kind,scope_ref,name,queries,created_by,created_at,updated_at) VALUES('project',?,?,?,?,?,?)`,
		shellProject, "pr-ergebnis", "kind:pain nothing-here", "anna", "2026-11-01T00:00:00Z", "2026-11-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	_, pr := fetchPage(t, e.Owner, e.Base+"/ui/search?project="+url.QueryEscape(shellProject)+"&preset=pr-ergebnis&pr=12")
	if !strings.Contains(pr, "kind:criterion is:done pr:12") || strings.Contains(pr, "nothing-here") {
		t.Errorf("the quick filter of a pull request page is not the supplied one")
	}
	// Another member may not overwrite a search of the project; the author and a lead may.
	if code := save(e.Member, "team-view", "project", "kind:knowledge Needle"); code != http.StatusSeeOther {
		t.Fatalf("anna saves for the project -> %d", code)
	}
	if code := save(e.Reviewer, "team-view", "project", "kind:request Needle"); code != http.StatusConflict {
		t.Errorf("rita overwrites the search of anna -> %d, want 409", code)
	}
	if code := save(e.Member, "team-view", "project", "kind:idea Needle"); code != http.StatusSeeOther {
		t.Errorf("anna overwrites her own -> %d", code)
	}
	if code := save(e.Lead, "team-view", "project", "kind:pain Needle"); code != http.StatusSeeOther {
		t.Errorf("a lead overwrites -> %d", code)
	}
	_, run := fetchPage(t, e.Member, e.Base+"/ui/search?project="+url.QueryEscape(shellProject)+"&preset=team-view")
	if !strings.Contains(run, "kind:pain Needle") {
		t.Errorf("the lead's version does not run")
	}
}
