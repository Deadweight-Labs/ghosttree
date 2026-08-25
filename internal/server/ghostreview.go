package server

import (
	"net/http"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// putGhostReview records that a path was looked at and deliberately left
// undescribed. The blob is required rather than optional: without it the
// decision would apply to every future version of the file, which is exactly
// the decision-for-ever this state is designed to avoid.
func (a *api) putGhostReview(w http.ResponseWriter, r *http.Request) {
	var in store.GhostReview
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Project == "" {
		writeErr(w, http.StatusBadRequest, "project is required")
		return
	}
	if in.GitBlob == "" {
		writeErr(w, http.StatusBadRequest, "git_blob is required")
		return
	}
	in.Person = personOf(r)
	if err := a.st.PutGhostReview(in); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"path": in.Path})
}

func (a *api) ghostReviews(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("project") == "" {
		writeErr(w, http.StatusBadRequest, "project is required")
		return
	}
	out, err := a.st.GhostReviewsUnder(q.Get("project"), q.Get("prefix"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		// [] and not null: the caller iterates over this, and null reads like a
		// failure rather than like "none".
		out = []store.GhostReview{}
	}
	writeJSON(w, 200, out)
}
