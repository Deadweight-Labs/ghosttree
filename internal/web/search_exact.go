package web

import (
	"slices"
	"strconv"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// facetOfRef is the kind of the search page (its facet) an object of a kind of
// reference belongs to. A pull request is no kind of the page: its hit is shown
// on the page of all kinds only.
var facetOfRef = map[string]string{
	links.KindRequest: "requests", links.KindIdea: "ideas", links.KindKnowledge: "knowledge",
	links.KindTask: "tasks", links.KindPain: "pains",
}

// exactHits are the objects a search term names by their number (#217, pr:217,
// REQ-12, ...; links.ParseNumber), in the order of the kinds the term can mean.
// They are read through the readers of the hover cards (links.AccessReader), so
// the rights are those of reading the object: what the viewer may not read, or
// what lies in a project the search is not narrowed to, is as unknown as a number
// that does not exist. A kind the viewer gets no group of (tasks and pains for a
// guest) gives no hit either. A pull request number belongs to a project, so a
// search of all projects finds it in each project the viewer can read.
func (a *app) exactHits(l lang, pa *store.ProjectAccess, term links.NumberTerm, project string, kinds []string, now time.Time) []searchHit {
	rd := links.AccessReader{PA: pa}
	n := term.N
	// inScope: the search is narrowed to a project and the object lies in another.
	inScope := func(p string) bool { return project == "" || p == "" || p == project }
	var out []searchHit
	for _, kind := range term.Kinds() {
		if facet, ok := facetOfRef[kind]; ok && !slices.Contains(kinds, facet) {
			continue
		}
		switch kind {
		case links.KindRequest:
			d, err := rd.GetRequest(n)
			if err != nil || !inScope(d.Request.Scope.Project) {
				continue
			}
			o := originOf(l, now, d.Request.CreatedAt, factsFor(a.store, "request", []int64{n})[n])
			out = append(out, searchHit{Title: d.Request.Title, Sub: requestStateLabel(l, d.Request.State), Project: lastSegment(d.Request.Scope.Project), RefKind: "request", RefN: n,
				Href: requestURL(n), Date: o.Date, Stamp: o.Stamp, Area: o.Area, Facet: "requests", Mark: searchKindMark["requests"]})
		case links.KindIdea:
			i, err := rd.GetIdea(n)
			if err != nil || !inScope(i.Project) {
				continue
			}
			o := originOf(l, now, i.CreatedAt, factsFor(a.store, "idea", []int64{n})[n])
			out = append(out, searchHit{Title: i.Title, Sub: label(l, ideaStateKeys, i.State), Project: lastSegment(i.Project), RefKind: "idea", RefN: n,
				Href: repoHref(i.Project) + "/ideas/" + strconv.FormatInt(n, 10), Date: o.Date, Stamp: o.Stamp, Area: o.Area, Facet: "ideas", Mark: searchKindMark["ideas"]})
		case links.KindTask:
			t, err := rd.GetHumanTask(n)
			if err != nil || !inScope(t.Project) {
				continue
			}
			sub := ""
			if t.State != "open" {
				sub = label(l, taskStateKeys, t.State)
			}
			o := originOf(l, now, t.CreatedAt, factsFor(a.store, "task", []int64{n})[n])
			out = append(out, searchHit{Title: t.Title, Sub: sub, Project: lastSegment(t.Project), RefKind: "task", RefN: n, Href: taskHref(t),
				Date: o.Date, Stamp: o.Stamp, Area: o.Area, Facet: "tasks", Mark: searchKindMark["tasks"]})
		case links.KindPain:
			d, err := rd.GetPain(n)
			if err != nil || !inScope(d.Pain.Project) {
				continue
			}
			o := originOf(l, now, d.Pain.CreatedAt, factsFor(a.store, "pain", []int64{n})[n])
			out = append(out, searchHit{Title: d.Pain.Title, Sub: label(l, painStateKeys, d.Pain.State), Project: lastSegment(d.Pain.Project), RefKind: "pain", RefN: n,
				Href: painURL(n), Date: o.Date, Stamp: o.Stamp, Area: o.Area, Facet: "pains", Mark: searchKindMark["pains"]})
		case links.KindKnowledge:
			k, err := rd.KnowledgeByID(n)
			// The same rule as the knowledge search: a hit is an entry in force.
			if err != nil || !inScope(k.Scope.Project) || !pa.CanSeeKnowledge(k) || (k.Status != "active" && k.Status != "stale") {
				continue
			}
			row := a.knowledgeRowFor(l, pa, k, true)
			o := originOf(l, now, k.CreatedAt, factsFor(a.store, "knowledge", []int64{n})[n])
			out = append(out, searchHit{Title: row.Title, Sub: row.TypeLabel, Project: row.Project, RefKind: "knowledge", RefN: n, Href: row.Href,
				Date: o.Date, Stamp: o.Stamp, Area: o.Area, Facet: "knowledge", Mark: searchKindMark["knowledge"]})
		case links.KindPull:
			for _, p := range visibleProjects(pa, project) {
				pr, err := rd.PullRequest(p, n)
				if err != nil {
					continue
				}
				o := originOf(l, now, pr.OpenedAt, store.ObjectFacts{})
				out = append(out, searchHit{Title: pr.Title, Sub: label(l, pullStateKeys, pr.State), Project: lastSegment(pr.Project), RefKind: "pr", RefN: n, PRState: pr.State,
					Href: repoHref(pr.Project) + "/pulls/" + strconv.FormatInt(n, 10), Date: o.Date, Stamp: o.Stamp, Mark: "pr"})
			}
		}
	}
	// Hits of more than one project: the repository tells them apart.
	multi := slices.ContainsFunc(out, func(h searchHit) bool { return h.Project != out[0].Project })
	for i := range out {
		out[i].Exact = true
		out[i].Marked = markWords(out[i].Title, nil)
		out[i].Lead = out[i].Sub
		if multi {
			out[i].Lead = out[i].Project
			if out[i].Sub != "" {
				out[i].Lead += " · " + out[i].Sub
			}
		}
	}
	return out
}
