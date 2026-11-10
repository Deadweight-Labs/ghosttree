package web

import (
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// GET /ui/refs is GET /api/refs for the browser (REQ-599, store part 6): the
// hover cards of references that the page did not know when it was written,
// above all the pills of the Markdown preview of a composer. The API takes a
// token in a header; the browser has only its session cookie, so the page asks
// here. The answer is the same: up to 50 references (?ref=REQ-12&ref=IDEA-3,
// ?project= for pull requests), one entry each in the order of the request, and
// "does not exist", "may not read" and "has no card" are the same entry
// (gefunden=false). When none of the references is readable the answer is 200
// with an empty list, always the same body. The rights are those of reading the object
// (ProjectAccess). Links leave as addresses without host.

func (a *app) refsLookup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q := r.URL.Query()
	refs := q["ref"]
	if len(refs) == 0 || len(refs) > links.MaxPeekRefs {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	project := scope.NormalizeRemote(q.Get("project"))
	pa := a.access(r)
	pa.Filtered()
	list, err := links.Lookup(links.New("https://ghosttree.invalid", project), links.AccessReader{PA: pa}, refs, project)
	if err != nil {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	found := false
	for i := range list {
		if !list[i].Gefunden {
			continue
		}
		found = true
		if u, err := url.Parse(list[i].Link); err == nil {
			list[i].Link = u.RequestURI()
			if u.Fragment != "" {
				list[i].Link += "#" + u.Fragment
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if !found {
		// None readable: an empty list, the same body for "unknown" and "not allowed".
		list = []links.Info{}
	}
	_ = json.NewEncoder(w).Encode(struct {
		Refs []links.Info `json:"refs"`
	}{list})
}
