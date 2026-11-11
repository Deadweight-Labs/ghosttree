package web

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
)

// The old lists of requests, tasks and pains are gone (REQ-599); their addresses
// still lead somewhere, because agents, links in the history and bookmarks name
// them. Requests and pains are part of the issue list of a repository now, tasks
// are the part "Tasks" of the inbox.

// repoChoice is the repository an address without one leads to: the project
// named in ?project= when the viewer can open it, else the one chosen in the
// header, else the first the viewer can open. "" when there is none.
func (a *app) repoChoice(r *http.Request) string {
	pa := a.access(r)
	opts, _ := a.projectOptions(r, pa)
	open := func(remote string) bool {
		return remote != "" && slices.ContainsFunc(opts, func(o projectOption) bool { return o.Remote == remote })
	}
	if p := a.projectParam(r); open(p) {
		return p
	}
	if len(opts) > 0 {
		return opts[0].Remote
	}
	return ""
}

// requestsPage is /ui/requests: the issue list of the repository.
func (a *app) requestsPage(w http.ResponseWriter, r *http.Request) {
	target := "/ui/inbox"
	if remote := a.repoChoice(r); remote != "" {
		target = repoHref(remote) + "/issues"
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// painsPage is /ui/pains: the pains in the issue list of the repository.
func (a *app) painsPage(w http.ResponseWriter, r *http.Request) {
	target := "/ui/inbox"
	if remote := a.repoChoice(r); remote != "" {
		target = repoHref(remote) + "/issues?q=" + url.QueryEscape("is:pain is:open")
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// tasksPage is /ui/tasks: the tasks in the inbox, narrowed to the repository
// that was asked for.
func (a *app) tasksPage(w http.ResponseWriter, r *http.Request) {
	q := url.Values{"f": {"aufgaben"}}
	if p := r.URL.Query().Get("project"); p != "" {
		q.Set("project", p)
	}
	http.Redirect(w, r, "/ui/inbox?"+q.Encode(), http.StatusFound)
}

// painPage is /ui/pains/{id}: the page of the pain in its repository when the
// viewer can open that; a pain without a repository stays on this address.
func (a *app) painPage(w http.ResponseWriter, r *http.Request) {
	if remote := a.painRepo(r); remote != "" {
		target := repoHref(remote) + "/pains/" + url.PathEscape(r.PathValue("id"))
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusFound)
		return
	}
	a.painPageIn(w, r, "")
}

// painRepo is the project of the pain when the viewer can read the pain and open
// the project's repository page; "" otherwise.
func (a *app) painRepo(r *http.Request) string {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return ""
	}
	return a.painRepoOf(r, id)
}

// painRepoOf is painRepo for a pain named by its number.
func (a *app) painRepoOf(r *http.Request, id int64) string {
	pa := a.access(r)
	d, err := pa.PainDetailView(id)
	if err != nil || d.Pain.Project == "" {
		return ""
	}
	opts, _ := a.projectOptions(r, pa)
	if !slices.ContainsFunc(opts, func(o projectOption) bool { return o.Remote == d.Pain.Project }) {
		return ""
	}
	return d.Pain.Project
}

// painHref is where the page of a pain is: in its repository when the viewer can
// open that, else at the address without one.
func (a *app) painHref(r *http.Request, id int64) string {
	if remote := a.painRepoOf(r, id); remote != "" {
		return repoHref(remote) + "/pains/" + strconv.FormatInt(id, 10)
	}
	return painURL(id)
}
