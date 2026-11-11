package web

import (
	"html/template"
	"net/http"
	"slices"
	"strconv"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The list of all branches of a project (REQ-599): /ui/{org}/{repo}/branches, the
// last entry of the branch menu of the Code tab. Everything on it is read from
// what the server already holds: the commits and pull requests it knows from the
// ledger and from the pull request source, and the knowledge filed against a
// branch. Nothing is asked of GitHub. A guest gets the default branch alone, as
// in the menu, because a branch name can give away what a guest may not read.

type branchRow struct {
	Name, Href string
	// NamePill is the name as a branch pill; it carries the link of the row.
	NamePill  template.HTML
	Default   bool
	Commit    *branchCommit
	PRs       []branchPR
	Knowledge int
	// Waiting are the people the open tasks of the branch's work package wait for.
	Waiting []string
}

type branchCommit struct{ Title, Href, At, Who string }

type branchPR struct {
	N                  int64
	Href, State, Title string
}

type branchesView struct {
	Rows              []branchRow
	Total             int
	GitHub, GitHubTip string
}

// branchesPage is /ui/{org}/{repo}/branches.
func (a *app) branchesPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	view := repoFrom(r)
	pa := a.access(r)
	if _, stop := a.gateList(w, r, pa, view.Project, store.ResGhost); stop {
		return
	}
	names, def := a.codeBranches(r, pa, view.Project)
	links := codeLinks{base: view.Href, def: def}
	commits := a.lastCommitOfBranches(pa, view.Project, names)
	prs := a.openPullsOfBranches(pa, view.Project, view)
	know := a.knowledgeOfBranches(pa, view.Project, def)
	v := &branchesView{Total: len(names)}
	k := pillKinds["branch"]
	waiting := a.branchWaiters(pa, view.Project, names)
	for _, n := range names {
		row := branchRow{Name: n, Href: links.tree(n, ""), Default: n == def, Commit: commits[n], PRs: prs[n], Knowledge: know[n], Waiting: waiting[n]}
		row.NamePill = pillHTML(pillSpec{Class: k.Class, Icon: mockIconBody(k.Icon), Text: n, Href: row.Href, Mono: k.Mono, Extra: "rl", Card: pillCard{Title: n}})
		v.Rows = append(v.Rows, row)
	}
	if view.External != "" {
		v.GitHub, v.GitHubTip = view.External+"/branches", l.msg("branches.gh")
	}
	a.renderBrowser(w, r, "branches", pageData{Title: l.msg("branches.title"), Project: view.Project, BranchesV: v})
}

// branchWaiters are the people each branch waits for. The open tasks of the
// project are read once; with none open nothing else is asked, and otherwise
// only the branches that have an open work package are looked into (the lean
// list of packages tells which), not every branch of the project.
func (a *app) branchWaiters(pa *store.ProjectAccess, project string, names []string) map[string][]string {
	out := map[string][]string{}
	tasks := a.openTasksByRequest(pa, project)
	if len(tasks) == 0 {
		return out
	}
	for _, p := range packageChoices(pa, project) {
		if p.State != requestdomain.PackageOpen || !slices.Contains(names, p.Branch) {
			continue
		}
		if _, done := out[p.Branch]; done {
			continue
		}
		d, err := a.store.PackageForBranch(project, p.Branch, nil)
		if err != nil {
			continue
		}
		if standing, ok := a.packageStanding(pa, d, tasks); ok && len(standing.Waiting) > 0 {
			out[p.Branch] = standing.Waiting
		}
	}
	return out
}

// lastCommitOfBranches finds, for each of the branches, the newest commit the
// server knows on it. One look at the newest commits of the project serves them
// all; a branch whose last commit is older than that has none shown.
func (a *app) lastCommitOfBranches(pa *store.ProjectAccess, project string, names []string) map[string]*branchCommit {
	out := map[string]*branchCommit{}
	list, err := pa.Commits(project, "", store.CommitMaxLimit, 0)
	if err != nil {
		return out
	}
	base := repoHref(project) + "/commits/"
	for _, c := range list.Items {
		for _, b := range c.Branches {
			if _, seen := out[b]; seen || !slices.Contains(names, b) {
				continue
			}
			out[b] = &branchCommit{Title: c.Title, Href: base + c.SHA, At: c.At, Who: firstNonEmpty(c.Author, c.AuthorName)}
		}
	}
	return out
}

// openPullsOfBranches groups the open pull requests of the project by the branch
// they come from.
func (a *app) openPullsOfBranches(pa *store.ProjectAccess, project string, view *repoView) map[string][]branchPR {
	out := map[string][]branchPR{}
	list, err := pa.PullRequests(project, store.PullListFilter{State: store.PullOpen, Limit: store.PullMaxLimit})
	if err != nil {
		return out
	}
	for _, p := range list.Items {
		out[p.Head] = append(out[p.Head], branchPR{N: p.Number, Href: view.Href + "/pulls/" + strconv.FormatInt(p.Number, 10), State: p.State, Title: p.Title})
	}
	return out
}

// knowledgeOfBranches counts the entries the viewer may read for each branch:
// the ones filed against it and the ones written on it. An entry of the project
// level belongs to the default branch, as the Code tab shows it there.
func (a *app) knowledgeOfBranches(pa *store.ProjectAccess, project, def string) map[string]int {
	pa.Filtered()
	out := map[string]int{}
	_, _ = a.knowledgeKeep(a.knowledgeWindowFor(pa, project), knowledgeMaxPages*100, func(k store.Knowledge) bool {
		if k.Scope.Project != project || (k.Status != "active" && k.Status != "stale") || !pa.CanSeeKnowledge(k) {
			return false
		}
		on := map[string]bool{}
		if k.Scope.Branch == "" {
			on[def] = true
		} else {
			on[k.Scope.Branch] = true
		}
		if k.GitAnchor != nil && k.GitAnchor.Branch != "" {
			on[k.GitAnchor.Branch] = true
		}
		for b := range on {
			out[b]++
		}
		return false
	})
	return out
}

// branchesHref is the address of the list of branches of a project.
func branchesHref(project string) string { return repoHref(project) + "/branches" }
