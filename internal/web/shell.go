package web

import (
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// navItem ist ein Eintrag der Seitenleiste. Label ist ein Katalogschlüssel.
type navItem struct {
	Key, Label, Href string
	Current          bool
	Sub              []navItem
}

type projectOption struct {
	Remote   string
	Label    string
	Selected bool
}

// shellBase ist, was jede Seite einer Anfrage aus Rolle und Projektliste
// braucht. Es wird je Anfrage einmal berechnet (shellMemo) und danach kopiert.
type shellBase struct {
	who      viewer
	projects []projectOption
	all      bool
}

type shellMemoKey struct{}

// shellMemo hängt an der Anfrage (requirePerson) und hält die Hülle.
type shellMemo struct{ base *shellBase }

// shellBaseFor liefert Betrachter und Projektliste mit der Vorauswahl "erstes
// eigenes Projekt", wo "All projects" nicht zur Wahl steht. Ohne Memo in der
// Anfrage (Tests) wird jedes Mal gerechnet.
func (a *app) shellBaseFor(r *http.Request) shellBase {
	memo, _ := r.Context().Value(shellMemoKey{}).(*shellMemo)
	if memo != nil && memo.base != nil {
		b := *memo.base
		b.projects = slices.Clone(b.projects)
		return b
	}
	pa := a.access(r)
	b := shellBase{who: a.viewerOf(r, pa)}
	b.projects, b.all = a.projectOptions(r, pa)
	// Ohne gültiges ?project= bekommt, wer nicht "All projects" wählen kann, sein
	// erstes Projekt vorgewählt. Ein Projektname ohne Rolle wirkt wie keiner.
	if b.who.kind != viewerOwner && b.all && len(b.projects) > 0 {
		b.projects[0].Selected, b.all = true, false
	}
	if memo != nil {
		stored := b
		stored.projects = slices.Clone(b.projects)
		memo.base = &stored
	}
	return b
}

// shellView ist, was die Hülle (Seitenleiste, Kopf, Kontomenü) für genau diesen
// Betrachter zeigt. Alles hier stammt aus Rolle und Sichtbarkeit des
// Betrachters; es gibt keine Zähler und keine Einträge für Bereiche, die er
// nicht sieht (#2447).
type shellView struct {
	On            bool
	Primary       []navItem
	Secondary     []navItem
	Projects      []projectOption
	AllSelected   bool
	CanAll        bool
	ProjectAction string
	Initial       string
	RoleKey       string
	Host          string
}

type viewerKind int

const (
	viewerGuest viewerKind = iota
	viewerMember
	viewerOwner
)

// viewer ist der eingeordnete Betrachter der Hülle.
type viewer struct {
	kind    viewerKind
	roleKey string
	// reviewer: darf Wissen prüfen (Reviewer-Flag, Lead, Owner, Admin).
	reviewer bool
}

// viewerOf ordnet den Betrachter ein: Owner/Admin, Member (auch Lead) oder
// Guest. Guest ist, wer höchstens Guest-Rollen hat. Wer noch in keinem Projekt
// eine Rolle hat (frische Instanz, Konto ohne Organisation), bekommt die
// Navigation eines Members; die Seiten dahinter filtern selbst.
func (a *app) viewerOf(r *http.Request, pa *store.ProjectAccess) viewer {
	orgOwner := false
	if orgs, err := a.store.ListOrgs(browserPrincipal(r).ID); err == nil {
		for _, o := range orgs {
			orgOwner = orgOwner || o.Role == store.OrgOwner
		}
	}
	rank, flag := 0, false
	for _, remote := range pa.Projects() {
		role := pa.Role(remote)
		rank = max(rank, store.RoleRank(role.Role))
		flag = flag || role.CanReview
	}
	reviewer := flag || rank >= store.RoleRank(store.RoleLead) || pa.IsAdmin() || orgOwner
	switch {
	case orgOwner || rank >= store.RoleRank(store.RoleOwner):
		return viewer{viewerOwner, "role.owner", true}
	case pa.IsAdmin():
		return viewer{viewerOwner, "role.admin", true}
	case rank == store.RoleRank(store.RoleLead):
		return viewer{viewerMember, "role.lead", reviewer}
	case rank == store.RoleRank(store.RoleGuest):
		return viewer{viewerGuest, "role.guest", false}
	}
	return viewer{viewerMember, "role.member", reviewer}
}

// projectAware sind die Seiten, die ?project= auswerten.
var projectAware = map[string]bool{"knowledge": true, "review": true, "context": true, "sessions": true, "agents": true}

// navSection ist der Eintrag der Seitenleiste, unter dem eine Seite steht.
func navKeyFor(section string, kind viewerKind) string {
	switch section {
	case "overview":
		return "overview"
	case "agents":
		return "agents"
	case "requests", "request":
		return "requests"
	case "knowledge", "knowledgeitem", "review", "context":
		return "knowledge"
	case "sessions", "session":
		return "sessions"
	case "coord":
		return "rooms"
	case "tokens", "device", "devicecheck", "devicedone":
		if kind == viewerOwner {
			return "admin-devices"
		}
		return "account-devices"
	case "orgs":
		if kind == viewerOwner {
			return "admin-org"
		}
	}
	return ""
}

func (a *app) shellFor(r *http.Request, name string) shellView {
	base := a.shellBaseFor(r)
	who := base.who
	kind := who.kind
	current := navKeyFor(name, kind)
	item := func(key, label, href string) navItem {
		return navItem{Key: key, Label: label, Href: href, Current: key == current}
	}
	v := shellView{On: true, RoleKey: who.roleKey, Host: a.publicHost(r), CanAll: kind == viewerOwner}
	if label := strings.TrimSpace(browserPrincipal(r).Label); label != "" {
		v.Initial = strings.ToUpper(string([]rune(label)[:1]))
	}

	v.Primary = []navItem{item("overview", "nav.overview", "/ui/overview")}
	if kind != viewerGuest {
		v.Primary = append(v.Primary, item("agents", "nav.agents", "/ui/agents"))
	}
	// Sessions zeigt jeder; die Seite filtert selbst, der Eintrag verrät nichts.
	v.Primary = append(v.Primary, item("sessions", "nav.sessions", "/ui/sessions"))
	if kind != viewerGuest {
		v.Primary = append(v.Primary, item("rooms", "nav.rooms", "/ui/coord"))
	}
	knowledge := item("knowledge", "nav.knowledge", "/ui/knowledge")
	if who.reviewer && current == "knowledge" {
		sub := func(key, label, href, section string) navItem {
			return navItem{Key: key, Label: label, Href: href, Current: name == section}
		}
		knowledge.Sub = []navItem{
			sub("knowledge-search", "nav.search", "/ui/knowledge", "knowledge"),
			sub("knowledge-review", "nav.review", "/ui/review", "review"),
			sub("knowledge-context", "nav.context", "/ui/context", "context"),
		}
	}
	v.Primary = append(v.Primary, knowledge, item("requests", "nav.requests", "/ui/requests"))

	switch kind {
	case viewerOwner:
		v.Secondary = []navItem{item("admin-org", "nav.organization", "/ui/orgs"), item("admin-devices", "nav.devices", "/ui/account/tokens")}
	case viewerMember:
		v.Secondary = []navItem{item("account-devices", "nav.my_devices", "/ui/account/tokens")}
	}

	v.ProjectAction = "/ui/knowledge"
	if projectAware[name] {
		v.ProjectAction = r.URL.Path
	}
	v.Projects, v.AllSelected = base.projects, base.all
	return v
}

// publicHost ist der Host, den die Fußzeile zeigt: der der öffentlichen URL,
// sonst der der Anfrage. Ein Proxy-Host aus Headern wird nie gezeigt.
func (a *app) publicHost(r *http.Request) string {
	if a.publicOrigin != "" {
		if u, err := url.Parse(a.publicOrigin); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return r.Host
}

// projectOptions listet die Projekte, in denen der Betrachter eine Rolle hat,
// und markiert das gewählte (?project=). Projekte ohne Rolle erscheinen nie.
func (a *app) projectOptions(r *http.Request, pa *store.ProjectAccess) ([]projectOption, bool) {
	list, err := a.store.ListProjects(browserPrincipal(r).ID, 0)
	if err != nil {
		return nil, true
	}
	chosen := scope.NormalizeRemote(r.URL.Query().Get("project"))
	var visible []string
	for _, p := range list {
		if pa.Role(p.Remote).Role != "" {
			visible = append(visible, p.Remote)
		}
	}
	labels := projectLabels(visible)
	var out []projectOption
	found := false
	for _, remote := range visible {
		sel := chosen != "" && remote == chosen
		found = found || sel
		out = append(out, projectOption{Remote: remote, Label: labels[remote], Selected: sel})
	}
	slices.SortFunc(out, func(x, y projectOption) int { return strings.Compare(x.Remote, y.Remote) })
	return out, !found
}

func (a *app) rootRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ui/", http.StatusSeeOther)
}

// roomsAlias hält den neuen Namen /ui/rooms gültig, bis Räume (P4) umziehen.
func (a *app) roomsAlias(w http.ResponseWriter, r *http.Request) {
	target := "/ui/coord"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (a *app) favicon(w http.ResponseWriter, r *http.Request) {
	raw, err := files.ReadFile("static/favicon.svg")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(raw)
}

// projectLabels gibt jeder Remote ihren kurzen Namen; teilen sich sichtbare
// Projekte denselben, steht das Owner-Segment davor (owner/name). Verglichen
// wird nur unter den übergebenen, also sichtbaren Projekten.
func projectLabels(remotes []string) map[string]string {
	count := map[string]int{}
	for _, r := range remotes {
		count[projectLabel(r)]++
	}
	out := make(map[string]string, len(remotes))
	for _, r := range remotes {
		label := projectLabel(r)
		if count[label] > 1 {
			parts := strings.Split(strings.Trim(r, "/"), "/")
			if len(parts) >= 2 {
				label = parts[len(parts)-2] + "/" + label
			}
		}
		out[r] = label
	}
	return out
}

// projectLabel ist der kurze Name eines Projekts: der letzte Pfadteil der Remote.
func projectLabel(remote string) string {
	trimmed := strings.Trim(remote, "/")
	if i := strings.LastIndex(trimmed, "/"); i >= 0 && i+1 < len(trimmed) {
		return trimmed[i+1:]
	}
	return remote
}
