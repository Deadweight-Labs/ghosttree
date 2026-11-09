package web

import (
	"context"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Features in der Oberfläche (REQ-599, REQ-612): der Reiter „Features“ eines
// Repositorys mit der Liste, die Seite eines Features unter
// /ui/{org}/{repo}/features/{n} und die Kurzform /ui/f/{n}. Alles liest, was
// die Store-Funktionen dem Betrachter geben; ein Feature eines fremden Projekts
// ist hier so unbekannt wie eines, das es nicht gibt.

// featureListMax begrenzt die Liste; mehr Features hat kein Projekt.
const featureListMax = 200

var featureStateTone = map[string]string{feature.Active: "p-ok", feature.Retired: "p-mute"}

var featureStateKeys = map[string]string{feature.Active: "features.state.active", feature.Retired: "features.state.retired"}

// featureUsage nennt die vier Wege der Bedienung in der Reihenfolge der Seite.
var featureUsage = []struct{ Key, Label string }{
	{"cli", "features.usage.cli"}, {"mcp", "features.usage.mcp"}, {"api", "features.usage.api"}, {"ui", "features.usage.ui"},
}

type featuresView struct {
	Q, Base        string
	Tabs           []issueStateTab
	Rows           []uiRow
	Empty, NoMatch bool
	EmptyKey       string
	ClearHref      string
}

type featureAreaLine struct{ Path, Href string }

type featureRef struct{ Num, Title, Brief, Href string }

type featurePR struct{ Text, Title, Href string }

type featureView struct {
	ID                  int64
	Name, Summary       string
	State, StateLabel   string
	Tone                string
	Revision            int
	Person, Age         string
	Blocks              []bodyBlock
	Limits              []bodyBlock
	Usage               []featureUsageLine
	Areas               []featureAreaLine
	Reason              string
	Items               []tlItem
	Ideas, Origin, Reqs []featureRef
	PRs                 []featurePR
	People              []string
}

type featureUsageLine struct {
	Label  string
	Blocks []bodyBlock
}

// registerNewPages hängt die Seiten dieser Datei, der Maschinen und der Werkstatt
// an den Router. Die Routen stehen in newWebRoutes (web_neu.go).
func (a *app) registerNewPages(mux *http.ServeMux) {
	a.handle(mux, "GET /ui/f/{n}", a.requirePerson(http.HandlerFunc(a.featureShort)))
	a.registerMachinePages(mux)
	a.registerWorkshopPages(mux)
	a.registerTaskPages(mux)
}

// featuresPage ist /ui/{org}/{repo}/features.
func (a *app) featuresPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	view := repoFrom(r)
	project := view.Project
	pa := a.access(r)
	if _, stop := a.gateList(w, r, pa, project, store.ResFeature); stop {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 200 {
		q = q[:200]
	}
	state := feature.Active
	if r.URL.Query().Get("state") == feature.Retired {
		state = feature.Retired
	}
	all, err := pa.SearchFeatures(feature.Filter{Project: project, State: "all", Limit: featureListMax})
	if err != nil {
		a.featureError(w, r, err)
		return
	}
	shown := all
	if q != "" {
		if shown, err = pa.SearchFeatures(feature.Filter{Project: project, State: "all", Query: q, Limit: featureListMax}); err != nil {
			a.featureError(w, r, err)
			return
		}
	}
	active, retired := 0, 0
	for _, f := range all {
		if f.State == feature.Retired {
			retired++
		} else {
			active++
		}
	}
	base := view.Href + "/features"
	v := &featuresView{Q: q, Base: base, EmptyKey: "features.empty"}
	href := func(state string) string {
		args := []string{}
		if q != "" {
			args = append(args, "q", q)
		}
		if state == feature.Retired {
			args = append(args, "state", feature.Retired)
		}
		return pathWith(base, args...)
	}
	v.Tabs = []issueStateTab{
		{Icon: "feature", Label: "features.tab.active", Count: strconv.Itoa(active), Href: href(feature.Active), On: state == feature.Active},
		{Icon: "drop", Label: "features.tab.retired", Count: strconv.Itoa(retired), Href: href(feature.Retired), On: state == feature.Retired},
	}
	pkgs := a.featurePackages(l, pa, project)
	now := overviewNow().UTC()
	for _, f := range shown {
		if (f.State == feature.Retired) != (state == feature.Retired) {
			continue
		}
		v.Rows = append(v.Rows, a.featureRowOf(l, pa, view, f, pkgs, now))
	}
	switch {
	case len(v.Rows) > 0:
	case q != "":
		v.NoMatch, v.ClearHref = true, base
	case state == feature.Retired:
		v.EmptyKey = "features.empty_retired"
	}
	a.renderBrowser(w, r, "features", pageData{Title: l.msg("repo.tab.features"), Project: project, FeaturesV: v})
}

// featureError antwortet auf einen Fehler der Store-Aufrufe: fremd und fehlend
// als 404, fehlendes Recht als 403.
func (a *app) featureError(w http.ResponseWriter, r *http.Request, err error) {
	if a.accessDenied(w, r, err) {
		return
	}
	http.NotFound(w, r)
}

// featurePackages ordnet die Pull-Request-Nummern der Pakete des Projekts zu:
// was der Betrachter an Paketen lesen darf, nicht mehr.
func (a *app) featurePackages(l lang, pa *store.ProjectAccess, project string) map[int64]string {
	out := map[int64]string{}
	list, err := pa.PackagesOfProject(project, "")
	if err != nil {
		return out
	}
	for _, p := range list {
		if p.PR > 0 {
			out[p.PR] = packageName(l, p.Package)
		}
	}
	return out
}

// featurePRs sind die Pull Requests, die Fassungen des Features getragen haben,
// aufsteigend und ohne Doppelte.
func featurePRs(versions []feature.Version) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, v := range versions {
		for _, pr := range v.PRs() {
			if !seen[pr] {
				seen[pr] = true
				out = append(out, pr)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (a *app) featureRowOf(l lang, pa *store.ProjectAccess, view *repoView, f feature.Feature, pkgs map[int64]string, now time.Time) uiRow {
	row := uiRow{Icon: "feature", Tone: "feat", StateLabel: label(l, featureStateKeys, f.State), Num: "#" + strconv.FormatInt(f.ID, 10),
		Title: f.Name, Href: view.Href + "/features/" + strconv.FormatInt(f.ID, 10), Kind: l.msg("features.rev", f.Revision),
		Age: shortAge(l, now, parseTime(f.UpdatedAt)), AgeTitle: l.dateTime(parseTime(f.UpdatedAt).Local()), Person: f.Person,
		Excerpt: ellipsize(f.Summary, 180), Pill: label(l, featureStateKeys, f.State), PillTone: featureStateTone[f.State]}
	for _, ref := range f.Affects {
		if ref.Kind != feature.KindIdea {
			continue
		}
		row.Meta = append(row.Meta, template.HTML(`<a class="fm" href="`+html.EscapeString(view.Href+"/ideas/"+strconv.FormatInt(ref.ID, 10))+`" title="`+html.EscapeString(ref.Title)+`">`+string(mockIcon("idea"))+`#`+strconv.FormatInt(ref.ID, 10)+`</a>`))
	}
	if versions, err := pa.FeatureVersions(f.ID, false); err == nil {
		for _, pr := range featurePRs(versions) {
			title := pkgs[pr]
			row.Meta = append(row.Meta, template.HTML(`<span class="fm" title="`+html.EscapeString(title)+`">`+string(mockIcon("pr"))+`#`+strconv.FormatInt(pr, 10)+`</span>`))
		}
	}
	return row
}

// featureShort ist /ui/f/{n}: die globale Nummer führt auf die Adresse im
// Repository des Features (Entscheidung #4331).
func (a *app) featureShort(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.ParseInt(r.PathValue("n"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := a.access(r).FeatureView(n)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, repoHref(f.Project)+"/features/"+strconv.FormatInt(n, 10), http.StatusFound)
}

// featureDetail ist /ui/{org}/{repo}/features/{n}.
func (a *app) featureDetail(w http.ResponseWriter, r *http.Request, remote, id string) {
	l := a.langFor(r)
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	pa := a.access(r)
	f, err := pa.FeatureView(n)
	if err != nil || f.Project != remote {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	q.Set("project", remote)
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = q.Encode()
	view := a.repoViewFor(r2, pa, a.shellBaseFor(r2).who.kind, remote, "features")
	r2 = r2.WithContext(context.WithValue(r2.Context(), repoCtxKey{}, view))
	now := overviewNow().UTC()
	v := &featureView{ID: f.ID, Name: f.Name, Summary: f.Summary, State: f.State, StateLabel: label(l, featureStateKeys, f.State),
		Tone: featureStateTone[f.State], Revision: f.Revision, Person: f.Person, Age: shortAgo(l, now, parseTime(f.UpdatedAt)),
		Blocks: bodyBlocks(f.Behavior, 0), Limits: bodyBlocks(f.Limits, 0), Reason: f.Reason}
	for _, u := range featureUsage {
		text := map[string]string{"cli": f.CLI, "mcp": f.MCP, "api": f.API, "ui": f.UI}[u.Key]
		if blocks := bodyBlocks(text, 0); len(blocks) > 0 {
			v.Usage = append(v.Usage, featureUsageLine{Label: l.msg(u.Label), Blocks: blocks})
		}
	}
	_, def := a.codeBranches(r2, pa, remote)
	links := codeLinks{base: view.Href, def: def}
	for _, area := range f.Areas {
		v.Areas = append(v.Areas, featureAreaLine{Path: area, Href: links.tree(def, strings.TrimSuffix(area, "/"))})
	}
	ideaRef := func(ref feature.Ref) featureRef {
		return featureRef{Num: "#" + strconv.FormatInt(ref.ID, 10), Title: ref.Title, Href: view.Href + "/ideas/" + strconv.FormatInt(ref.ID, 10),
			Brief: label(l, workshopPhaseKeys, ref.State)}
	}
	reqRef := func(ref feature.Ref) featureRef {
		return featureRef{Num: "#" + strconv.FormatInt(ref.ID, 10), Title: ref.Title, Href: view.Href + "/issues/" + strconv.FormatInt(ref.ID, 10), Brief: l.msg("ref.kind.issue")}
	}
	for _, ref := range f.Affects {
		if ref.Kind == feature.KindIdea {
			v.Ideas = append(v.Ideas, ideaRef(ref))
		} else {
			v.Reqs = append(v.Reqs, reqRef(ref))
		}
	}
	for _, ref := range f.Origin {
		if ref.Kind == feature.KindIdea {
			v.Origin = append(v.Origin, ideaRef(ref))
		} else {
			v.Origin = append(v.Origin, reqRef(ref))
		}
	}
	versions, _ := pa.FeatureVersions(n, false)
	pkgs := a.featurePackages(l, pa, remote)
	for _, pr := range featurePRs(versions) {
		line := featurePR{Text: "#" + strconv.FormatInt(pr, 10), Title: pkgs[pr]}
		if view.External != "" {
			line.Href = view.External + "/pull/" + strconv.FormatInt(pr, 10)
		}
		v.PRs = append(v.PRs, line)
	}
	v.Items = a.featureItems(l, pa, view, versions, f, now)
	seen := map[string]bool{}
	for _, ver := range versions {
		if ver.Person != "" && !seen[ver.Person] {
			seen[ver.Person] = true
			v.People = append(v.People, ver.Person)
		}
	}
	a.renderBrowser(w, r2, "feature", pageData{Title: f.Name, Project: remote, FeatureV: v})
}

// featureItems ist die Zeitleiste: jede Fassung ein Eintrag, dahinter, wenn das
// Feature eingestellt ist, das Einstellen.
func (a *app) featureItems(l lang, pa *store.ProjectAccess, view *repoView, versions []feature.Version, f feature.Feature, now time.Time) []tlItem {
	var items []tlItem
	for _, ver := range versions {
		it := tlItem{ID: "version-" + strconv.Itoa(ver.Revision), Act: "feature.version", Who: ver.Person, Icon: "pencil",
			Age: shortAgo(l, now, parseTime(ver.CreatedAt)), Body: template.HTML(html.EscapeString(ver.Message)),
			Git: anchorViewOf(pa, view.Project, ver.GitAnchor, originVia(ver.OriginContext))}
		if ver.Revision <= 1 {
			it.Icon, it.Head = "plus", template.HTML(html.EscapeString(l.msg("features.ev.created")))
		} else {
			it.Head = template.HTML(html.EscapeString(l.msg("features.ev.version", ver.Revision)))
		}
		var pills strings.Builder
		if ver.Idea > 0 {
			pills.WriteString(" " + string(refPillHTML(l, "idea", ver.Idea, view.Href+"/ideas/"+strconv.FormatInt(ver.Idea, 10))))
		}
		for _, req := range ver.Requests {
			pills.WriteString(" " + string(refPillHTML(l, "request", req, view.Href+"/issues/"+strconv.FormatInt(req, 10))))
		}
		it.Head += template.HTML(pills.String())
		items = append(items, it)
	}
	if f.State == feature.Retired {
		items = append(items, tlItem{ID: "retired", Act: "feature.retired", Icon: "drop", Head: template.HTML(html.EscapeString(l.msg("features.ev.retired"))),
			Age: shortAgo(l, now, parseTime(f.UpdatedAt)), Body: template.HTML(html.EscapeString(f.Reason))})
	}
	return items
}

// featureLink ist die Adresse eines Features im Repository (für Verweise aus
// anderen Seiten).
func featureLink(remote string, id int64) string {
	return repoHref(remote) + "/features/" + url.PathEscape(strconv.FormatInt(id, 10))
}

// featureCount ist die Zahl der aktiven Features für den Reiter, bis repoCountAt.
func (a *app) featureCount(pa *store.ProjectAccess, remote string) string {
	list, err := pa.SearchFeatures(feature.Filter{Project: remote, Limit: repoCountAt})
	if err != nil {
		return ""
	}
	return countText(len(list))
}
