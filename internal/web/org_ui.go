package web

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Die Seite einer Organisation (REQ-599, Phase 2 B): /ui/{org} mit dem Band aus
// Repositories, Agenten, Personen, Räumen und (für Owner) Einstellungen. Alle
// Zahlen und Listen kommen aus dem, was der Betrachter sehen darf; die
// Einstellungen sind die bestehende Verwaltungsseite /ui/orgs.

type orgRepoRow struct {
	Name, Href string
	// Shared: every member of the org has the default role in it (shared_projects).
	// It is not public reading, which does not exist yet (REQ-598).
	Shared bool
	// User: the row of a person's own namespace, which is private or shared
	// with Collaborators people, never "with the org".
	User          bool
	Collaborators int
	Remote        string
	// Issues and Knowledge are the numbers the repository's own tabs show.
	Issues, Knowledge string
}

type orgPersonRow struct {
	Name, Role string
	Self       bool
	Guest      bool
}

type orgRoomRow struct {
	Name, Href, Repo string
}

// orgServiceRow is a service account on the Services tab: what it is, whom it
// works for, where it may work and when its token was last used.
type orgServiceRow struct {
	Name, For, Used string
	Gardener        bool
	Roles           []orgServiceRole
}

type orgServiceRole struct{ Repo, Href, Role string }

// orgHandoverRow is a project waiting between a person's namespace and an
// organization whose connected prefix covers it.
type orgHandoverRow struct {
	Remote, Name, Owner, Org string
	Consented, CanAct        bool
}

type orgPageView struct {
	// Handover: on an organization's page, projects in people's namespaces
	// its prefix covers (owners only); on a person's page, their projects an
	// organization's prefix covers. Token is the form token.
	Handover []orgHandoverRow
	Token    string
	Org      store.Org
	Tab      string
	Repos    []orgRepoRow
	People   []orgPersonRow
	Services []orgServiceRow
	Rooms    []orgRoomRow
	Groups   []agentGroup
	// Sections is the same list by state: waiting on the viewer, active, inactive.
	Sections agentSections
	Cmd      cmdBlock
	// The agent tab: the values of the filter menus and the number at work.
	Active                                 int
	AgentOwners, AgentMachines, AgentRepos []string
	Connect                                string
}

// orgFor finds the organisation of the viewer by its address.
func (a *app) orgFor(r *http.Request, slug string) (store.Org, bool) {
	orgs, err := a.store.ListOrgs(browserPrincipal(r).ID)
	if err != nil {
		return store.Org{}, false
	}
	for _, o := range orgs {
		// A guest sees only its project, never the organization (14 A, #4376).
		if strings.EqualFold(o.Slug, slug) && !o.Guest {
			return o, true
		}
	}
	return store.Org{}, false
}

// orgProjects are the projects of the organisation the viewer may see.
func (a *app) orgProjects(r *http.Request, org store.Org) []store.Project {
	list, _ := a.store.ListProjects(browserPrincipal(r).ID, org.ID)
	return a.access(r).VisibleProjects(list)
}

// orgBandFor builds the band of an organisation with the number of each list.
func (a *app) orgBandFor(r *http.Request, org store.Org, current string) *bandView {
	if org.IsUser() {
		return a.userBandFor(r, org, current)
	}
	counts := map[string]int{"repos": len(a.orgProjects(r, org))}
	if members, err := a.store.ListOrgMembersFor(org.ID, browserPrincipal(r).ID, a.store.AccessEnforced()); err == nil {
		for _, m := range members {
			if m.Kind != store.AccountKindService {
				counts["people"]++
			}
		}
	}
	showServices := false
	if services, err := a.store.OrgServices(browserPrincipal(r).ID, org.ID); err == nil {
		counts["services"], showServices = len(services), true
	}
	counts["rooms"] = counts["repos"]
	counts["machines"] = len(a.orgMachines(r, org))
	if groups, _ := a.orgAgentGroups(r, org); groups != nil {
		n := 0
		for _, g := range groups {
			n += len(g.Cards)
		}
		counts["agents"] = n
	}
	opts := orgBandOpts{Owner: org.Role == store.OrgOwner, Services: showServices}
	if n := a.gardenOpenCount(r, org); n >= 0 {
		counts["gardener"], opts.Gardener = n, true
	}
	b := orgBand("/ui/"+url.PathEscape(org.Slug), current, counts, opts)
	b.Head = &bandHead{Name: org.Name}
	if org.AvatarLogin != "" {
		b.Head.Avatar = "/ui/avatar/org/" + strconv.FormatInt(org.ID, 10)
		b.Head.GitHub = "https://github.com/" + url.PathEscape(org.AvatarLogin)
	}
	return b
}

// orgAgentGroups are the agents at the projects of the organisation, by machine.
func (a *app) orgAgentGroups(r *http.Request, org store.Org) ([]agentGroup, error) {
	cards, err := a.orgAgentCards(r, org)
	if err != nil {
		return nil, err
	}
	return groupAgents(cards, nil), nil
}

// orgAgentCards are the agents at the projects of the organisation, in the order
// of the agent list.
func (a *app) orgAgentCards(r *http.Request, org store.Org) ([]agentCard, error) {
	pa := a.access(r)
	now := overviewNow().UTC()
	rows, err := a.listAgents(r, pa, "", now, agentsMax*4)
	if err != nil {
		return nil, err
	}
	projects := map[string]bool{}
	for _, p := range a.orgProjects(r, org) {
		projects[p.Remote] = true
	}
	var keep []agentRow
	for _, row := range rows {
		if projects[row.ProjectTitle] {
			keep = append(keep, row)
		}
	}
	return a.agentCards(r, pa, keep, now)
}

// orgPage renders one tab of an organisation.
func (a *app) orgPage(w http.ResponseWriter, r *http.Request, org store.Org, tab string) {
	l := a.langFor(r)
	v := &orgPageView{Org: org, Tab: tab}
	switch tab {
	case "repos":
		for _, p := range a.orgProjects(r, org) {
			name := p.Remote
			if i := strings.LastIndex(name, "/"); i >= 0 {
				name = name[i+1:]
			}
			pa := a.access(r)
			row := orgRepoRow{Name: name, Href: repoHref(p.Remote), Shared: p.Shared, Remote: p.Remote, User: org.IsUser(),
				Issues: orZero(a.openIssues(pa, p.Remote)), Knowledge: orZero(a.knowledgeCount(pa, p.Remote))}
			if row.User {
				row.Collaborators = a.store.ProjectCollaborators(p.Remote)
			}
			v.Repos = append(v.Repos, row)
		}
		v.Token = csrfOf(r)
		admin := a.isAdmin(browserPrincipal(r))
		if org.IsUser() {
			list, _ := a.store.PendingHandovers(browserPrincipal(r).ID)
			for _, h := range list {
				v.Handover = append(v.Handover, orgHandoverRow{Remote: h.Remote, Name: lastSegment(h.Remote), Owner: h.Owner, Org: h.Org, Consented: h.Consented, CanAct: !h.Consented && interactive(r)})
			}
		} else if list, err := a.store.PendingTakeovers(browserPrincipal(r).ID, org.ID); err == nil {
			for _, h := range list {
				v.Handover = append(v.Handover, orgHandoverRow{Remote: h.Remote, Name: lastSegment(h.Remote), Owner: h.Owner, Org: h.Org, Consented: h.Consented, CanAct: (h.Consented || admin) && interactive(r)})
			}
		}
	case "people":
		members, _ := a.store.ListOrgMembersFor(org.ID, browserPrincipal(r).ID, a.store.AccessEnforced())
		for _, m := range members {
			if m.Kind == store.AccountKindService {
				continue // services have their own tab
			}
			v.People = append(v.People, orgPersonRow{Name: store.NormalizeAccountName(m.Account), Role: m.Role, Self: m.AccountID == browserPrincipal(r).ID, Guest: m.Guest})
		}
	case "services":
		services, err := a.store.OrgServices(browserPrincipal(r).ID, org.ID)
		if err != nil {
			// Guests do not see who works for the organization.
			http.NotFound(w, r)
			return
		}
		now := overviewNow().UTC()
		for _, sv := range services {
			row := orgServiceRow{Name: sv.Name, Gardener: sv.Gardener, For: l.msg("org.service.instance"), Used: l.msg("org.service.never_used")}
			if sv.OfOrg {
				row.For = l.msg("org.service.of_org", org.Name)
			}
			if sv.LastUsed != "" {
				row.Used = l.msg("org.service.last_used", shortAge(l, now, parseTime(sv.LastUsed)))
			}
			for remote, role := range sv.Roles {
				name := remote
				if i := strings.LastIndex(name, "/"); i >= 0 {
					name = name[i+1:]
				}
				row.Roles = append(row.Roles, orgServiceRole{Repo: name, Href: repoHref(remote), Role: role})
			}
			sort.Slice(row.Roles, func(i, j int) bool { return row.Roles[i].Repo < row.Roles[j].Repo })
			v.Services = append(v.Services, row)
		}
	case "rooms":
		for _, p := range a.orgProjects(r, org) {
			name := p.Remote
			if i := strings.LastIndex(name, "/"); i >= 0 {
				name = name[i+1:]
			}
			v.Rooms = append(v.Rooms, orgRoomRow{Name: l.msg("coord.general"), Repo: name, Href: pathWith("/ui/coord", "room", store.RoomKeyForProject(p.Remote))})
		}
	case "agents":
		cards, _ := a.orgAgentCards(r, org)
		v.Groups = groupAgents(cards, nil)
		v.Sections = sectionAgents(cards, csrfOf(r), r.URL.RequestURI())
		v.fillAgentFilters(a.viewerOf(r, a.access(r)).kind != viewerGuest)
	}
	a.renderBrowser(w, r, "org", pageData{Title: org.Name, OrgName: org.Name, OrgBand: a.orgBandFor(r, org, tab), OrgV: v})
}

// orgTab answers /ui/{org}/{tab} for the tabs of an organisation. It reports
// whether it answered; an address that is a repository goes on to the repository.
func (a *app) orgTab(w http.ResponseWriter, r *http.Request) bool {
	tab := r.PathValue("b")
	switch tab {
	case "agents", "people", "services", "rooms", "settings", "gardener":
	default:
		return false
	}
	if r.PathValue("c") != "" {
		return false
	}
	org, ok := a.orgFor(r, r.PathValue("a"))
	if !ok {
		return a.redirectMovedOrg(w, r, r.PathValue("a"), "/"+tab)
	}
	// A repository of that name inside the organisation wins.
	remote, _, _ := parseRepoPath([]string{r.PathValue("a"), tab})
	pa := a.access(r)
	opts, _ := a.projectOptions(r, pa)
	for _, o := range opts {
		if o.Remote == remote {
			return false
		}
	}
	if tab == "gardener" {
		a.gardenTab(w, r, org)
		return true
	}
	if tab == "settings" {
		if org.Role != store.OrgOwner {
			http.NotFound(w, r)
			return true
		}
		http.Redirect(w, r, "/ui/orgs?org="+url.QueryEscape(org.Slug), http.StatusFound)
		return true
	}
	a.orgPage(w, r, org, tab)
	return true
}

func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

// fillAgentFilters collects what the filter menus of the agent list offer, each
// value once, in the order the agents appear.
func (v *orgPageView) fillAgentFilters(canConnect bool) {
	seen := map[string]bool{}
	add := func(list *[]string, kind, value string) {
		if value != "" && !seen[kind+"\x00"+value] {
			seen[kind+"\x00"+value] = true
			*list = append(*list, value)
		}
	}
	for _, g := range v.Groups {
		v.Active += g.Active
		for _, c := range g.Cards {
			add(&v.AgentOwners, "o", c.Owner)
			add(&v.AgentMachines, "m", c.Machine)
			add(&v.AgentRepos, "r", c.Project)
		}
	}
	if canConnect {
		v.Connect = "/ui/overview?connect=1"
	}
}

// userBandFor is the band of a person's own namespace, with the person's
// picture when they linked GitHub.
func (a *app) userBandFor(r *http.Request, org store.Org, current string) *bandView {
	counts := map[string]int{"repos": len(a.orgProjects(r, org)), "machines": len(a.orgMachines(r, org))}
	opts := orgBandOpts{Owner: org.Role == store.OrgOwner}
	if n := a.gardenOpenCount(r, org); n >= 0 {
		counts["gardener"], opts.Gardener = n, true
	}
	b := userBand("/ui/"+url.PathEscape(org.Slug), current, counts, opts)
	b.Head = &bandHead{Name: org.Name, Handle: org.Slug}
	owner := "person:" + strconv.FormatInt(org.AccountID, 10)
	if link, ok := a.store.AccountGitHub(owner); ok {
		b.Head.Avatar = "/ui/avatar/" + strconv.FormatInt(org.AccountID, 10)
		b.Head.GitHub = "https://github.com/" + url.PathEscape(link.Login)
	}
	return b
}

// redirectMovedOrg answers the address of an organization under a former slug
// with a permanent redirect to the current one (REQ-623), for members only: to
// anybody else the old address is as unknown as any other.
func (a *app) redirectMovedOrg(w http.ResponseWriter, r *http.Request, slug, suffix string) bool {
	cur, ok := a.store.OrgSlugTarget(slug)
	if !ok {
		return false
	}
	if _, member := a.orgFor(r, cur); !member {
		return false
	}
	target := "/ui/" + url.PathEscape(cur) + suffix
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusFound)
	return true
}

// handoverConsent is the owner's "hand it over" for a project an
// organization's prefix covers (POST /ui/handover).
func (a *app) handoverConsent(w http.ResponseWriter, r *http.Request) {
	remote := r.FormValue("remote")
	if err := a.store.ConsentHandover(browserPrincipal(r).ID, remote); err != nil {
		http.NotFound(w, r)
		return
	}
	ns, _ := a.store.UserNamespace(browserPrincipal(r).ID)
	http.Redirect(w, r, "/ui/"+url.PathEscape(ns.Slug), http.StatusSeeOther)
}

// handoverTakeOver moves a waiting project into the organization (POST
// /ui/orgs/takeover): an owner of it after the former owner agreed, or the
// instance admin.
func (a *app) handoverTakeOver(w http.ResponseWriter, r *http.Request) {
	p, err := a.store.TakeOverProject(browserPrincipal(r).ID, r.FormValue("remote"))
	if err != nil {
		http.Error(w, "not taken over", http.StatusConflict)
		return
	}
	http.Redirect(w, r, "/ui/"+url.PathEscape(p.Org), http.StatusSeeOther)
}
