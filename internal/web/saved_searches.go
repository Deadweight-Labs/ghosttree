package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// searchSave keeps the text of a list's search field as a saved search of the
// signed-in person ("Save search" under the saved searches of the issue list).
// The search is personal: it appears for nobody else.
func (a *app) searchSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	project := scope.NormalizeRemote(strings.TrimSpace(r.FormValue("project")))
	name, q := strings.TrimSpace(r.FormValue("name")), strings.TrimSpace(r.FormValue("q"))
	if project == "" || name == "" || q == "" || len(name) > 80 || len(q) > 500 {
		http.Error(w, "project, name and search text are required", http.StatusBadRequest)
		return
	}
	pa := a.access(r)
	if _, err := pa.SavePreset(project, "user", presetSlug(name), "", []string{q}); err != nil {
		if errors.Is(err, store.ErrAccessNotFound) || errors.Is(err, store.ErrAccessForbidden) {
			a.accessDenied(w, r, err)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	back := repoHref(project) + "/issues"
	if b := r.FormValue("back"); b == back {
		back = b
	}
	http.Redirect(w, r, back+"?q="+url.QueryEscape(q), http.StatusSeeOther)
}

// presetSlug is the name a saved search is kept under: the store takes lower-case
// letters, digits and hyphens, at most 40 characters. What the person typed is
// folded to that ("Open bugs" becomes "open-bugs").
func presetSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	return out
}
