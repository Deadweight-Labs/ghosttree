package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/query"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Schnellfilter (REQ-600 K9, REQ-599): die gespeicherten Suchen eines Projekts
// und die mitgelieferten stehen als Filter neben der Suche. Ein Schnellfilter
// läuft auf der Seite „Suche“: seine Zeilen laufen nacheinander, die Treffer
// werden zusammengelegt. Eine Suche lässt sich für mich, für das Projekt oder
// für die Org speichern, je nach Rolle; ob es erlaubt ist, entscheidet der Store.

// quickFilter ist ein Schnellfilter in der Seitenleiste. Hat er Platzhalter, ist
// er ein kleines Formular (Arg = Name des Platzhalters), sonst ein Link.
type quickFilter struct {
	Name, Title, Href string
	// Arg und ArgLabel: der eine Platzhalter, nach dem das Formular fragt.
	Arg, ArgLabel string
	Scope         string
	Project       string
	Current       bool
}

// saveScope ist ein Ort, an dem eine Suche gespeichert werden kann.
type saveScope struct {
	Value, Label string
	Selected     bool
}

var (
	// presetTitleKeys und presetArgKeys sind die Texte der mitgelieferten Suchen.
	presetTitleKeys = map[string]string{
		"pr-ergebnis":  "search.preset.pr_ergebnis",
		"branch-offen": "search.preset.branch_offen",
		"ordner":       "search.preset.ordner",
		"seit":         "search.preset.seit",
		"fuer-mich":    "search.preset.fuer_mich",
	}
	presetArgKeys = map[string]string{"pr": "search.arg.pr", "branch": "search.arg.branch", "area": "search.arg.area", "since": "search.arg.since"}
	// scopeKeys sind die Orte, an denen eine Suche gilt.
	saveScopeKeys = map[string]string{"user": "search.scope.user", "project": "search.scope.project", "org": "search.scope.org"}
)

var commitArgRE = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// presetTitle ist der Anzeigename: der Text der mitgelieferten Suche, sonst der Name.
func presetTitle(l lang, name string) string {
	if key, ok := presetTitleKeys[name]; ok {
		return l.msg(key)
	}
	return name
}

// presetQueries liefert die Zeilen einer Suche nach Namen: die mitgelieferte geht
// immer vor (ihre Namen sind für gespeicherte Suchen gesperrt), so meint der Knopf
// „Was wurde hier erledigt?“ immer die mitgelieferte Suche.
func presetQueries(pa *store.ProjectAccess, project, name string) (queries []string, builtin, ok bool) {
	if b, found := query.BuiltinByName(name); found {
		return b.Queries, true, true
	}
	if project != "" {
		if saved, err := pa.ListPresets(project); err == nil {
			for _, p := range saved {
				if strings.EqualFold(p.Name, name) {
					return p.Queries, false, true
				}
			}
		}
	}
	return nil, false, false
}

// presetLines setzt die Platzhalter eines Schnellfilters aus der Adresse ein und
// liefert die Suchzeilen. Was nicht geht (unbekannter Name, fehlendes Argument),
// steht als Hinweis im View und die Suche läuft nicht. An einer Commit-Seite
// gilt „pr-ergebnis“ für den Commit: commit=… ersetzt pr=…
func (a *app) presetLines(l lang, r *http.Request, pa *store.ProjectAccess, v *searchView, name string) []string {
	v.Preset, v.PresetName = name, presetTitle(l, name)
	queries, builtin, ok := presetQueries(pa, v.Project, name)
	if !ok {
		v.Notice, v.NoticeKind = l.msg("search.err.preset_unknown"), "err"
		return nil
	}
	args := map[string]string{}
	need := query.ArgsOf(queries)
	for _, k := range need {
		args[k] = strings.TrimSpace(r.URL.Query().Get(k))
	}
	if sha := strings.TrimSpace(r.URL.Query().Get("commit")); builtin && name == "pr-ergebnis" && sha != "" && args["pr"] == "" {
		if !commitArgRE.MatchString(sha) {
			v.Notice, v.NoticeKind = l.msg("search.err.preset_args", presetArgNames(l, need)), "err"
			return nil
		}
		swapped := make([]string, len(queries))
		for i, q := range queries {
			swapped[i] = strings.ReplaceAll(q, "pr:{pr}", "commit:"+strings.ToLower(sha))
		}
		queries, need = swapped, query.ArgsOf(swapped)
		args = map[string]string{}
	}
	lines, err := query.Expand(queries, args)
	if err != nil {
		v.Notice, v.NoticeKind = l.msg("search.err.preset_args", presetArgNames(l, need)), "err"
		return nil
	}
	v.Lines, v.Searched = lines, true
	return lines
}

func presetArgNames(l lang, need []string) string {
	var names []string
	for _, k := range need {
		if key, ok := presetArgKeys[k]; ok {
			names = append(names, l.msg(key))
		} else {
			names = append(names, k)
		}
	}
	return strings.Join(names, ", ")
}

// searchSidebar füllt die Seitenleiste der Seite: die Schnellfilter und, wenn die
// Suche zu speichern ist, die Orte zum Speichern.
func (a *app) searchSidebar(l lang, r *http.Request, pa *store.ProjectAccess, v *searchView, project string, guest bool) {
	saved := map[string]bool{}
	if project != "" {
		if list, err := pa.ListPresets(project); err == nil {
			for _, p := range list {
				if len(p.Queries) == 0 {
					continue
				}
				saved[strings.ToLower(p.Name)] = true
				qf := quickFilter{Name: p.Name, Title: p.Name, Scope: p.Scope, Project: project, Current: v.Preset == p.Name}
				a.fillQuickFilter(l, &qf, p.Queries, project)
				v.Quick = append(v.Quick, qf)
			}
		}
	}
	for _, b := range query.Builtin {
		if saved[b.Name] {
			continue
		}
		qf := quickFilter{Name: b.Name, Title: presetTitle(l, b.Name), Scope: "builtin", Project: project, Current: v.Preset == b.Name}
		a.fillQuickFilter(l, &qf, b.Queries, project)
		v.Quick = append(v.Quick, qf)
	}
	// Without a project the old saved searches of every project stay as links to
	// that project's issue list.
	if project == "" {
		opts, _ := a.projectOptions(r, pa)
		for _, o := range opts {
			list, err := pa.ListPresets(o.Remote)
			if err != nil {
				continue
			}
			for _, p := range list {
				if len(p.Queries) > 0 {
					v.Presets = append(v.Presets, presetLink{Name: p.Name + " · " + projectLabel(o.Remote), Query: p.Queries[0], Href: repoHref(o.Remote) + "/issues?q=" + url.QueryEscape(p.Queries[0])})
				}
			}
		}
	}
	if project != "" && !guest && interactive(r) && v.Searched && v.Preset == "" && v.Notice == "" {
		v.CanSave = true
		v.Scopes = a.saveScopes(l, r, pa, project)
	}
}

// fillQuickFilter macht aus den Zeilen einen Link, oder bei einem Platzhalter ein
// Formular. Hat eine Suche mehrere Platzhalter, fragt das Formular nach dem ersten und
// die Seite sagt, was noch fehlt.
func (a *app) fillQuickFilter(l lang, qf *quickFilter, queries []string, project string) {
	args := query.ArgsOf(queries)
	qs := url.Values{"preset": {qf.Name}}
	if project != "" {
		qs.Set("project", project)
	}
	if len(args) == 0 {
		qf.Href = "/ui/search?" + qs.Encode()
		return
	}
	qf.Arg = args[0]
	qf.ArgLabel = presetArgNames(l, args[:1])
}

// saveScopes sind die Orte, an denen der Betrachter eine Suche speichern darf:
// für sich immer, für das Projekt ab member, für die Org als Owner der Org.
func (a *app) saveScopes(l lang, r *http.Request, pa *store.ProjectAccess, project string) []saveScope {
	out := []saveScope{{Value: "user", Label: l.msg(saveScopeKeys["user"]), Selected: true}}
	if pa.Check(project, store.ResLabel, store.ActCreate, store.Object{Own: true}) == nil {
		out = append(out, saveScope{Value: "project", Label: l.msg(saveScopeKeys["project"])})
	}
	if p, ok := a.store.ProjectByRemote(project); ok && p.OrgID != 0 {
		if orgs, err := a.store.ListOrgs(browserPrincipal(r).ID); err == nil {
			for _, o := range orgs {
				if o.ID == p.OrgID && o.Role == store.OrgOwner {
					out = append(out, saveScope{Value: "org", Label: l.msg(saveScopeKeys["org"])})
					break
				}
			}
		}
	}
	return out
}
