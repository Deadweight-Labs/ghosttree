package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Arbeitspakete als eigene Seiten (REQ-601 Kriterium 4, REQ-599 Kriterium 16):
// die Paketliste unter /ui/{org}/{repo}/packages und das Board eines Pakets unter
// …/packages/{n}: seine Requests mit Rolle (Pflicht oder optional), den offenen
// und erfüllten Kriterien samt Nachweisen, dem verbundenen PR und den Sessions,
// die an den Requests gearbeitet haben. Der Branch steht nur nach der Gast-Regel
// da. Die Nummer ist die globale; ein Paket eines anderen Repositories führt auf
// dessen Adresse, wenn der Betrachter es lesen darf.

var (
	packageStateKeys = map[string]string{requestdomain.PackageOpen: "packages.state.open", requestdomain.PackageClosed: "packages.state.closed"}
	packageTone      = map[string]string{requestdomain.PackageOpen: "p-ok", requestdomain.PackageClosed: "p-mute"}
	packageRowTone   = map[string]string{requestdomain.PackageOpen: "open", requestdomain.PackageClosed: "closed"}
	packageRoleKeys  = map[string]string{requestdomain.RolePflicht: "packages.role.pflicht", requestdomain.RoleOptional: "packages.role.optional"}
)

type packagesView struct {
	Base     string
	Tabs     []issueStateTab
	Rows     []uiRow
	EmptyKey string
	// Branch is the branch the list is narrowed to (empty: all); ClearHref leaves it.
	Branch, ClearHref string
}

type packageSessionLine struct {
	Kind, Handle, Title, Href, StateLabel, Age string
	Active                                     bool
}

type packageView struct {
	ID                       int64
	Name, Base               string
	State, StateLabel, Tone  string
	Branch, Inherited        string
	PRText, PRHref, PRGitHub string
	Members                  []pullMember
	// Roles gives each member its role label in the same order as Members.
	Roles                  []string
	Done, Total, Percent   int
	RequiredOpen, Required int
	Sessions               []packageSessionLine
}

func init() {
	pageAssets["packages"] = struct{ CSS, JS []string }{CSS: []string{"ui-labels.css", "ui-issues.css"}}
	pageAssets["package"] = struct{ CSS, JS []string }{CSS: []string{"ui-labels.css", "ui-issues.css", "ui-issue.css", "ui-pulls.css"}}
}

// repoPackagesPage ist /ui/{org}/{repo}/packages[?state=closed][&branch=…].
func (a *app) repoPackagesPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	view := repoFrom(r)
	project := view.Project
	pa := a.access(r)
	if _, stop := a.gateList(w, r, pa, project, store.ResRequest); stop {
		return
	}
	state := requestdomain.PackageOpen
	if r.URL.Query().Get("state") == requestdomain.PackageClosed {
		state = requestdomain.PackageClosed
	}
	base := view.Href + "/packages"
	v := &packagesView{Base: base, EmptyKey: "packages.empty"}
	list := func(state string) []requestdomain.PackageSummary {
		out, err := pa.PackagesOfProject(project, state)
		if err != nil {
			return nil
		}
		return out
	}
	open, closed := list(requestdomain.PackageOpen), list(requestdomain.PackageClosed)
	shown := open
	if state == requestdomain.PackageClosed {
		shown = closed
	}
	// A branch narrows the list to the package that holds for it; a name the
	// viewer may not know narrows nothing.
	if branch := strings.TrimSpace(r.URL.Query().Get("branch")); branch != "" && pa.BranchView(project, branch) == branch {
		lineage, _ := a.store.BranchLineage(project, branch)
		v.Branch, v.ClearHref = branch, base
		var narrowed []requestdomain.PackageSummary
		if d, err := a.store.PackageForBranch(project, branch, lineage); err == nil {
			for _, s := range append(append([]requestdomain.PackageSummary{}, open...), closed...) {
				if s.ID == d.ID {
					narrowed = append(narrowed, s)
				}
			}
		}
		shown = narrowed
		state = ""
		if len(narrowed) > 0 {
			state = narrowed[0].State
		}
		v.EmptyKey = "packages.empty_branch"
	}
	// The numbers beside the tabs are what the list shows for the filter: with a branch
	// that is the package of the branch, counted by its state.
	openN, closedN := len(open), len(closed)
	if v.Branch != "" {
		openN, closedN = 0, 0
		for _, s := range shown {
			if s.State == requestdomain.PackageClosed {
				closedN++
			} else {
				openN++
			}
		}
	}
	v.Tabs = []issueStateTab{
		{Icon: "list", Label: "packages.tab.open", Count: strconv.Itoa(openN), Href: base, On: state == requestdomain.PackageOpen && v.Branch == ""},
		{Icon: "done", Label: "packages.tab.closed", Count: strconv.Itoa(closedN), Href: pathWith(base, "state", requestdomain.PackageClosed), On: state == requestdomain.PackageClosed && v.Branch == ""},
	}
	for _, p := range shown {
		v.Rows = append(v.Rows, packageRowOf(l, pa, view, p))
	}
	if len(v.Rows) == 0 && state == requestdomain.PackageClosed && v.Branch == "" {
		v.EmptyKey = "packages.empty_closed"
	}
	a.renderBrowser(w, r, "packages", pageData{Title: l.msg("repo.tab.packages"), Project: project, PackagesV: v})
}

// packageName ist der Name eines Pakets für den Betrachter: hat die Zugriffssicht
// ihn verborgen (ProjectAccess.PackageNameView), steht das Wort der Oberfläche
// mit der Nummer da.
func packageName(l lang, p requestdomain.Package) string {
	if p.NameHidden {
		return l.msg("packages.hidden_name", p.ID)
	}
	return p.Name
}

func packageRowOf(l lang, pa *store.ProjectAccess, view *repoView, p requestdomain.PackageSummary) uiRow {
	row := uiRow{Icon: "list", Tone: packageRowTone[p.State], StateLabel: label(l, packageStateKeys, p.State), Num: "#" + strconv.FormatInt(p.ID, 10), Title: packageName(l, p.Package),
		Href: view.Href + "/packages/" + strconv.FormatInt(p.ID, 10), Pill: label(l, packageStateKeys, p.State), PillTone: packageTone[p.State],
		Excerpt: l.msg("packages.row", p.Requests, p.OpenCriteria)}
	if at := parseTime(p.UpdatedAt); !at.IsZero() {
		now := overviewNow().UTC()
		row.Age, row.AgeTitle = shortAge(l, now, at), l.dateTime(at.Local())
	}
	if b := pa.BranchView(view.Project, p.Branch); b != "" {
		row.Meta = append(row.Meta, branchChipHTML(b, ""))
	}
	if p.PR > 0 {
		row.Chip, row.ChipHref = "#"+strconv.FormatInt(p.PR, 10), view.Href+"/pulls/"+strconv.FormatInt(p.PR, 10)
	}
	return row
}

// packageDetail ist /ui/{org}/{repo}/packages/{n}.
func (a *app) packageDetail(w http.ResponseWriter, r *http.Request, remote, id string) {
	l := a.langFor(r)
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		http.NotFound(w, r)
		return
	}
	pa := a.access(r)
	project, err := a.store.PackageRef(n)
	if err != nil {
		pa.Filtered()
		http.NotFound(w, r)
		return
	}
	if pa.Check(project, store.ResRequest, store.ActRead, store.Object{}) != nil {
		http.NotFound(w, r)
		return
	}
	if project != remote {
		if a.canOpenRepo(r, pa, project) {
			http.Redirect(w, r, repoHref(project)+"/packages/"+strconv.FormatInt(n, 10), http.StatusFound)
			return
		}
		http.NotFound(w, r)
		return
	}
	d, err := a.store.PackageByID(n)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	q.Set("project", remote)
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = q.Encode()
	view := a.repoViewFor(r2, pa, a.shellBaseFor(r2).who.kind, remote, "packages")
	r2 = r2.WithContext(context.WithValue(r2.Context(), repoCtxKey{}, view))

	full := pa.PackageView(d, true)
	v := &packageView{ID: full.ID, Name: packageName(l, full.Package), Base: view.Href + "/packages", State: full.State, StateLabel: label(l, packageStateKeys, full.State), Tone: packageTone[full.State],
		Branch: pa.BranchView(remote, full.Branch)}
	if v.Branch != "" && full.InheritedFrom != "" {
		v.Inherited = pa.BranchView(remote, full.InheritedFrom)
	}
	if full.PR > 0 {
		v.PRText, v.PRHref = "#"+strconv.FormatInt(full.PR, 10), view.Href+"/pulls/"+strconv.FormatInt(full.PR, 10)
		if view.External != "" {
			v.PRGitHub = view.External + "/pull/" + strconv.FormatInt(full.PR, 10)
		}
	}
	ids := make([]int64, 0, len(full.Members))
	for _, m := range full.Members {
		ids = append(ids, m.RequestID)
	}
	prog, _ := a.store.CriteriaProgress(ids)
	seen := map[string]bool{}
	now := overviewNow().UTC()
	for _, m := range full.Members {
		pm := pullMember{N: m.RequestID, Href: view.Href + "/issues/" + strconv.FormatInt(m.RequestID, 10), Title: m.Title, Optional: m.Role == requestdomain.RoleOptional,
			Open: m.Open > 0, Progress: rowProgress{Done: prog[m.RequestID].Done, Total: prog[m.RequestID].Total}}
		pm.Progress.Title = l.msg("pulls.progress", pm.Progress.Done, pm.Progress.Total)
		for _, c := range m.Criteria {
			pc := pullCriterion{Done: c.State == "met", Waived: c.State == "waived", Text: c.Description}
			for _, e := range c.Evidence {
				line := a.evidenceLineFor(l, pa, remote, e)
				if e.Kind == "commit" && commitRefPattern.MatchString(e.Ref) {
					line.Href = view.Href + "/commits/" + e.Ref
				}
				pc.Evidence = append(pc.Evidence, line)
			}
			pc.NoProof = c.State == "met" && len(c.Evidence) == 0
			pm.Warn = pm.Warn || pc.NoProof
			pm.Criteria = append(pm.Criteria, pc)
		}
		pm.Warn = pm.Warn && m.Open == 0
		for _, t := range m.Tasks {
			pm.Tasks = append(pm.Tasks, pullTaskLine{N: t.ID, Title: t.Title})
		}
		v.Members = append(v.Members, pm)
		v.Roles = append(v.Roles, label(l, packageRoleKeys, m.Role))
		v.Done += pm.Progress.Done
		v.Total += pm.Progress.Total
		if m.Role == requestdomain.RolePflicht {
			v.Required++
			if m.Open > 0 {
				v.RequiredOpen++
			}
		}
		// The sessions that worked on the request, as far as the viewer may see them.
		if detail, err := a.store.RequestByID(m.RequestID); err == nil {
			for _, wl := range a.workLines(l, pa, pa.RequestDetailView(detail).Work, now) {
				if wl.Href == "" || seen[wl.Href] {
					continue
				}
				seen[wl.Href] = true
				v.Sessions = append(v.Sessions, packageSessionLine{Kind: wl.Kind, Handle: wl.Handle, Title: wl.Title, Href: wl.Href, StateLabel: wl.StateLabel, Age: wl.Age, Active: wl.Active})
			}
		}
	}
	if v.Total > 0 {
		v.Percent = v.Done * 100 / v.Total
	}
	a.renderBrowser(w, r2, "package", pageData{Title: v.Name, Project: remote, PackageV: v})
}
