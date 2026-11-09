package server

import "testing"

// Die Suche eines anderen im Projekt überschreibt nur, wer sie auch löschen dürfte: 409 sonst.
func TestSavedSearchOfAnotherAnswers409ToAMember(t *testing.T) {
	f := accessAPI(t, true)
	save := func(who string, code int, queries ...string) {
		t.Helper()
		f.expect(t, who, code, "POST", "/api/search-presets", map[string]any{"project": accProject, "scope": "project", "name": "team-sicht", "queries": queries})
	}
	save("mia", 200, "is:open")
	save("rex", 409, "is:done")
	save("mia", 200, "is:closed")
	save("lena", 200, "is:done")
}
