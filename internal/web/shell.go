package web

import (
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// navItem ist ein Eintrag der Seitenleiste. Label ist ein Katalogschlüssel.
type navItem struct {
	Key, Label, Href string
	Current          bool
	// Count is the number beside the entry; 0 shows nothing. Quiet marks a
	// number that is there to look at, not one that concerns the viewer.
	Count int
	Quiet bool
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
type shellMemo struct {
	base *shellBase
	// tasks is the number of open tasks for the viewer, once counted.
	tasks    int
	tasksSet bool
	// review is the number of proposals waiting for the viewer's review.
	review    int
	reviewSet bool
	// inbox is what the inbox lists, by repo filter, read once per request: the bell counts it.
	inbox map[string]*inboxData
}

// shellBaseFor liefert Betrachter und Projektliste. Ohne gültige Wahl gilt für
// alle "All projects", das heißt alle Projekte, in denen der Betrachter eine
// Rolle hat. Ohne Memo in der Anfrage (Tests) wird jedes Mal gerechnet.
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
	if memo != nil {
		stored := b
		stored.projects = slices.Clone(b.projects)
		memo.base = &stored
	}
	return b
}

// shellView ist, was die Hülle (Seitenleiste, Kopf, Kontomenü) für genau diesen
// Betrachter zeigt. Alles hier stammt aus Rolle und Sichtbarkeit des
// Betrachters; es gibt keine Einträge für Bereiche, die er nicht sieht (#2447).
// Der einzige Zähler sind die offenen Aufgaben für ihn, gezählt über dieselbe
// Sichtprüfung wie die Liste selbst (ListHumanTasks): er verrät nichts, was die
// Liste nicht zeigt.
type shellView struct {
	On            bool
	Primary       []navItem
	Projects      []projectOption
	AllSelected   bool
	CanAll        bool
	ProjectAction string
	Initial       string
	RoleKey       string
	// Guest: the viewer is a guest; empty states show them no agent command.
	Guest bool
	Host  string
	// Tabs sit under the page title: Requests | Tasks | Pains, Agents | Sessions,
	// Search | Review | Agent context, Team | Devices. TabsLabel names them.
	Tabs      []navItem
	TabsLabel string
	// KeepTabs shows the tabs also under the band of a repository (Agents | Sessions | Models, which the band does not
	// carry); the other pages of a repository have their own segments or tabs.
	KeepTabs bool
	// Team is the account menu entry "Team & devices"; empty for a guest.
	Team string
	// Inbox is the number of rows waiting for the viewer, 0 shows nothing.
	Inbox int
	// Orgs is the organization switcher in the header; nil without an organization.
	Orgs *orgSwitch
	// Scope is the repository a global page is narrowed to (?project= or the
	// remembered choice), shown behind the page name in the header. nil for all.
	Scope *shellScope
	// New is the "+" menu of the header: what the viewer can create from here.
	New *newMenu
}

// shellScope names the repository the page is narrowed to.
type shellScope struct {
	Remote, Label, Href string
}

// newMenu are the targets of the "+" menu. Issue, idea and pain lead to the form
// of one repository (the one of the page, else the chosen or first one); Repo
// leads to where an organization owner creates one; Org leads to the form for a
// new organization. A target the viewer cannot reach is empty and not shown.
type newMenu struct {
	Issue, Idea, Pain, Repo, Org string
}

// Any is true when the menu has at least one target.
func (m *newMenu) Any() bool { return m != nil && (m.Issue != "" || m.Repo != "" || m.Org != "") }

// newMenuFor builds the "+" menu for the viewer on this request.
func (a *app) newMenuFor(r *http.Request, base shellBase, work store.Org) *newMenu {
	if base.who.kind == viewerGuest {
		return nil
	}
	m := &newMenu{}
	repo := ""
	if rv := repoFrom(r); rv != nil {
		repo = rv.Href
	} else {
		for _, p := range base.projects {
			if (repo == "" || p.Selected) && repoAddressable(p.Remote) {
				repo = repoHref(p.Remote)
			}
		}
	}
	if repo != "" {
		m.Issue = repo + "/issues/new"
		m.Idea = m.Issue + "?kind=idea"
		m.Pain = m.Issue + "?kind=pain"
	}
	if browserPrincipal(r).May(store.RightOrgCreate) {
		m.Org = newOrgHref
	}
	// Every member creates projects in the organization (17 A, #4376): in the one
	// the viewer works in.
	if work.ID != 0 {
		m.Repo = orgSettingsHref(work, "#projects")
	}
	if !m.Any() {
		return nil
	}
	return m
}

// orgSwitch is the header menu to jump to an organization: the one the viewer
// has chosen as default (else the first) is the label.
type orgSwitch struct {
	Current string
	// CurrentHref is the page of the organization the viewer works in, for the header of the inbox.
	CurrentHref string
	Items       []orgSwitchItem
	// New leads to the form for a new organization; empty when the viewer
	// cannot create one from here.
	New string
}

// newOrgHref is the page with the form for a new organization.
const newOrgHref = "/ui/orgs/new"

type orgSwitchItem struct {
	Label, Href string
	Current     bool
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
			orgOwner = orgOwner || store.IsOrgOwnerRole(o.Role)
		}
	}
	best, reviewer := "", pa.IsAdmin() || orgOwner
	for _, remote := range pa.Projects() {
		role := pa.Role(remote)
		best = store.HighestRole(best, role.Role)
		reviewer = reviewer || role.Has(store.RightKnowledgeVerify)
	}
	switch {
	case orgOwner || store.RoleAtLeast(best, store.RoleOwner):
		return viewer{viewerOwner, a.activeRoleKey(r, pa, "role.owner"), true}
	case pa.IsAdmin():
		return viewer{viewerOwner, "role.admin", true}
	case store.RoleAtLeast(best, store.RoleLead):
		return viewer{viewerMember, "role.lead", reviewer}
	case (store.RoleInfo{Role: best}).GuestOnly():
		return viewer{viewerGuest, "role.guest", false}
	}
	return viewer{viewerMember, "role.member", reviewer}
}

// activeRoleKey is the role the account menu names: the one in the organization
// the viewer works in, not the highest anywhere. Someone who owns a side project
// in a namespace of their own is still a member of the organization they work
// in (REQ-683). fallback is the broad role; it stays when the viewer works in no
// organization.
func (a *app) activeRoleKey(r *http.Request, pa *store.ProjectAccess, fallback string) string {
	orgs := a.orgsOf(r)
	work := a.pickOrg(r, orgs)
	if work.ID == 0 {
		return fallback
	}
	if store.IsOrgOwnerRole(work.Role) {
		return "role.owner"
	}
	best := ""
	for _, remote := range pa.Projects() {
		if p, ok := a.store.ProjectByRemote(remote); ok && p.OrgID == work.ID {
			best = store.HighestRole(best, pa.Role(remote).Role)
		}
	}
	switch {
	case store.RoleAtLeast(best, store.RoleOwner):
		return "role.owner"
	case store.RoleAtLeast(best, store.RoleLead):
		return "role.lead"
	case (store.RoleInfo{Role: best}).GuestOnly():
		return "role.guest"
	case pa.IsAdmin():
		return "role.admin"
	}
	return "role.member"
}

// projectAware sind die Seiten, die ?project= auswerten.
var projectAware = map[string]bool{"inbox": true, "overview": true, "setup": true, "knowledge": true, "review": true, "context": true, "sessions": true, "agents": true, "coord": true, "search": true}

// projectNeutral sind die Seiten über Konto und Verwaltung. Sie filtern nach
// nichts; die Projektauswahl steht dort nicht, damit sie keinen Filter vortäuscht.
var projectNeutral = map[string]bool{"issue": true, "pain": true, "knowledgeitem": true, "session": true, "orgs": true, "tokens": true, "device": true, "devicecheck": true, "devicedone": true, "interactive": true, "profile": true}

// navSection ist der Eintrag der Seitenleiste, unter dem eine Seite steht.
func navKeyFor(section string, kind viewerKind) string {
	switch section {
	case "inbox", "overview", "setup":
		return "overview"
	case "agents":
		return "agents"
	case "requesttoolarge", "pain", "issue", "issues", "newissue", "idea":
		return "requests"
	case "knowledge", "knowledgeitem", "review", "context":
		return "knowledge"
	case "sessions", "session":
		if kind == viewerGuest {
			return "sessions"
		}
		return "agents"
	case "coord":
		return "rooms"
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
	v := shellView{On: true, RoleKey: who.roleKey, Guest: kind == viewerGuest, Host: a.publicHost(r), CanAll: kind == viewerOwner || len(base.projects) > 1}
	if label := strings.TrimSpace(browserPrincipal(r).Label); label != "" {
		v.Initial = strings.ToUpper(string([]rune(label)[:1]))
	}

	// Five entries: Home, Agents, Requests, Rooms, Knowledge. A guest has no
	// agents and no rooms; what is left of Agents for them is the sessions list.
	v.Primary = []navItem{item("overview", "nav.overview", "/ui/inbox")}
	if kind != viewerGuest {
		v.Primary = append(v.Primary, item("agents", "nav.agents", "/ui/agents"))
	} else {
		v.Primary = append(v.Primary, item("sessions", "nav.sessions", "/ui/sessions"))
	}
	open := a.openTaskCount(r, selectedProjectOf(base))
	requests := item("requests", "nav.requests", "/ui/requests")
	requests.Count = open
	v.Primary = append(v.Primary, requests)
	if kind != viewerGuest {
		v.Primary = append(v.Primary, item("rooms", "nav.rooms", "/ui/coord"))
	}
	knowledge := item("knowledge", "nav.knowledge", "/ui/knowledge")
	if who.reviewer {
		knowledge.Count, knowledge.Quiet = a.reviewCount(r, selectedProjectOf(base)), true
	}
	v.Primary = append(v.Primary, knowledge)

	tab := func(key, label, href string, on bool) navItem {
		return navItem{Key: key, Label: label, Href: href, Current: on}
	}
	switch name {
	case "agents", "sessions", "agentmodels":
		if kind != viewerGuest {
			// Under the band of a repository the tabs keep that repository.
			to := func(href string) string { return href }
			if rv := repoFrom(r); rv != nil && rv.Project != "" {
				v.KeepTabs = true
				to = func(href string) string { return pathWith(href, "project", rv.Project) }
			}
			v.Tabs = []navItem{tab("agents", "nav.tab_agents", to("/ui/agents"), name == "agents"), tab("sessions", "nav.sessions", to("/ui/sessions"), name == "sessions"),
				tab("models", "nav.tab_models", to("/ui/agents/models"), name == "agentmodels")}
			v.TabsLabel = "nav.agents"
		}
	case "knowledge", "review", "context":
		if who.reviewer {
			review := tab("review", "nav.review", "/ui/review", name == "review")
			review.Count = knowledge.Count
			v.Tabs = []navItem{tab("search", "nav.search", "/ui/knowledge", name == "knowledge"), review, tab("context", "nav.context", "/ui/context", name == "context")}
			v.TabsLabel = "nav.knowledge"
		}
	case "orgs", "tokens":
		if kind != viewerGuest {
			v.Tabs = []navItem{tab("team", "nav.tab_team", "/ui/orgs", name == "orgs"), tab("devices", "nav.tab_devices", "/ui/account/tokens", name == "tokens")}
			v.TabsLabel = "nav.team_devices"
		}
	}
	// Organization and devices are one entry in the account menu; it opens the
	// organization the viewer works in.
	orgs := a.orgsOf(r)
	work := a.pickOrg(r, orgs)
	if kind != viewerGuest {
		v.Team = "/ui/orgs"
		if work.ID != 0 {
			v.Team = orgSettingsHref(work, "")
		}
		for i := range v.Tabs {
			if v.Tabs[i].Key == "team" {
				v.Tabs[i].Href = v.Team
			}
		}
	}

	// The bell leads to the inbox, which lists every repo unless one is named in its address.
	v.Inbox = a.inboxFor(r, "").Count()
	v.Orgs = a.orgSwitchFor(r, orgs, work)
	v.New = a.newMenuFor(r, base, work)

	v.ProjectAction = "/ui/knowledge"
	if projectAware[name] {
		v.ProjectAction = r.URL.Path
	}
	if !projectNeutral[name] && repoFrom(r) == nil {
		v.Projects, v.AllSelected = base.projects, base.all
		if sel := selectedProjectOf(base); sel != "" {
			for _, p := range base.projects {
				if p.Remote == sel {
					v.Scope = &shellScope{Remote: p.Remote, Label: p.Label, Href: repoHref(p.Remote)}
				}
			}
		}
	}
	return v
}

// orgSwitchFor lists the organizations of the viewer for the header. Each entry
// leads to the organization's page.
func (a *app) orgSwitchFor(r *http.Request, orgs []store.Org, work store.Org) *orgSwitch {
	if len(orgs) == 0 {
		return nil
	}
	sw := &orgSwitch{Current: work.Name}
	if browserPrincipal(r).May(store.RightOrgCreate) {
		sw.New = newOrgHref
	}
	for _, o := range orgs {
		item := orgSwitchItem{Label: o.Name, Href: "/ui/" + url.PathEscape(o.Slug), Current: o.ID == work.ID}
		if item.Current {
			sw.CurrentHref = item.Href
		}
		sw.Items = append(sw.Items, item)
	}
	return sw
}

// selectedProjectOf is the project chosen in the shell, "" for all projects.
func selectedProjectOf(base shellBase) string {
	if base.all {
		return ""
	}
	for _, p := range base.projects {
		if p.Selected {
			return p.Remote
		}
	}
	return ""
}

// openTaskCount is the number of open tasks for the viewer in the chosen
// project (or all they can see): assigned to them, or for everyone. It counts
// what ListHumanTasks lets through, nothing else, and once per request.
func (a *app) openTaskCount(r *http.Request, project string) int {
	memo, _ := r.Context().Value(shellMemoKey{}).(*shellMemo)
	if memo != nil && memo.tasksSet {
		return memo.tasks
	}
	pa := a.access(r)
	n := 0
	for _, p := range visibleProjects(pa, project) {
		tasks, err := a.store.ListHumanTasks(pa, store.TaskFilter{Project: p, State: humantask.Open, Mine: true, Limit: tasksShown})
		if err != nil {
			continue
		}
		for _, t := range tasks {
			if t.State == humantask.Open && pa.CheckTask(t, store.ActWork) == nil {
				n++
			}
		}
	}
	if memo != nil {
		memo.tasks, memo.tasksSet = n, true
	}
	return n
}

// reviewCount is the number of proposals the viewer can review in the chosen
// project, capped like the review page itself, once per request. It counts what
// the review page lists: the same query, the same visibility check.
func (a *app) reviewCount(r *http.Request, project string) int {
	memo, _ := r.Context().Value(shellMemoKey{}).(*shellMemo)
	if memo != nil && memo.reviewSet {
		return memo.review
	}
	pa := a.access(r)
	n := 0
	if project == "" || pa.Role(project).Role != "" {
		if pending, err := a.store.PendingKnowledge(project, a.overfetch(reviewShown), pa.Viewer()); err == nil {
			n = len(keep(pending, reviewShown, pa.CanSeeKnowledge))
		}
	}
	if memo != nil {
		memo.review, memo.reviewSet = n, true
	}
	return n
}

// publicHost ist der Host, den das Kontomenü zeigt: der der öffentlichen URL,
// sonst der der Anfrage. Ein Proxy-Host aus Headern wird nie gezeigt. Eine
// Adresse dieses Rechners (127.0.0.1, localhost, ::1) sagt nichts darüber, auf
// welchem Server man ist, und bleibt weg (P49).
func (a *app) publicHost(r *http.Request) string {
	if a.publicOrigin != "" {
		if u, err := url.Parse(a.publicOrigin); err == nil && u.Host != "" {
			return u.Host
		}
	}
	if hostOnly(r.Host) == "localhost" || net.ParseIP(hostOnly(r.Host)).IsLoopback() {
		return ""
	}
	return r.Host
}

// hostOnly is the host of a "host:port" without the port and without the brackets of an IPv6 address.
func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return strings.Trim(hostport, "[]")
}

// projectCookie hält die Projektauswahl für die Browsersitzung, damit sie beim
// Wechsel der Seite bleibt. Der Wert ist nur ein Wunsch: er gilt erst, wenn der
// Betrachter im Projekt eine Rolle hat.
const projectCookie = "gt_project"

func (a *app) projectCookieFor(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: projectCookie, Value: value, Path: "/", HttpOnly: true,
		Secure: a.secureCookies(r), SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

// chosenProject ist das Projekt, das die Anfrage meint: ?project= (leer heißt
// "All projects"), sonst das Cookie. explicit sagt, ob ?project= vorkam.
func chosenProject(r *http.Request) (remote string, explicit bool) {
	q := r.URL.Query()
	if q.Has("project") {
		return scope.NormalizeRemote(q.Get("project")), true
	}
	if c, err := r.Cookie(projectCookie); err == nil {
		return scope.NormalizeRemote(c.Value), false
	}
	return "", false
}

// resolveProject ist das Projekt, das gilt: ein nutzbares ?project=, sonst das
// Cookie, sonst "" (All projects). Nutzbar heißt: unter den sichtbaren Projekten
// des Betrachters. Ein leeres ?project= wählt ausdrücklich "All projects".
func resolveProject(r *http.Request, visible []string) string {
	chosen, explicit := chosenProject(r)
	if explicit && (chosen == "" || slices.Contains(visible, chosen)) {
		return chosen
	}
	// The inbox is the one list for the whole account: a repo browsed earlier does not narrow it. It
	// narrows only when the address names a repo, and the bell leads there without one.
	if r.URL.Path == "/ui/inbox" {
		return ""
	}
	if c, err := r.Cookie(projectCookie); err == nil {
		if remote := scope.NormalizeRemote(c.Value); slices.Contains(visible, remote) {
			return remote
		}
	}
	return ""
}

// rememberProject legt eine ausdrückliche Wahl im Cookie ab. "All projects"
// löscht es; ein Projekt ohne Rolle wird nicht gemerkt.
func (a *app) rememberProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !r.URL.Query().Has("project") {
		return
	}
	remote, _ := chosenProject(r)
	if remote == "" {
		if _, err := r.Cookie(projectCookie); err == nil {
			http.SetCookie(w, a.projectCookieFor(r, "", -1))
		}
		return
	}
	if pa := a.access(r); pa.Role(remote).Role == "" {
		return
	}
	if c, err := r.Cookie(projectCookie); err == nil && c.Value == remote {
		return
	}
	http.SetCookie(w, a.projectCookieFor(r, remote, 0))
}

// projectOptions listet die Projekte, in denen der Betrachter eine Rolle hat,
// und markiert das gewählte (?project=, sonst das Cookie). Projekte ohne Rolle
// erscheinen nie und gelten als nicht gewählt.
func (a *app) projectOptions(r *http.Request, pa *store.ProjectAccess) ([]projectOption, bool) {
	list, err := a.store.ListProjects(browserPrincipal(r).ID, 0)
	if err != nil {
		return nil, true
	}
	var visible []string
	for _, p := range list {
		if pa.Role(p.Remote).Role != "" {
			visible = append(visible, p.Remote)
		}
	}
	chosen := resolveProject(r, visible)
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
