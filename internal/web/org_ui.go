package web

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Die Seite einer Organisation (REQ-599, Phase 2 B): /ui/{org} mit dem Band aus
// Repositories, Agenten, Personen, Räumen und (für Owner) Einstellungen. Alle
// Zahlen und Listen kommen aus dem, was der Betrachter sehen darf; die
// Einstellungen sind die bestehende Verwaltungsseite /ui/orgs.

type orgRepoRow struct {
	Name, Href string
	Public     bool
	Remote     string
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

type orgPageView struct {
	Org    store.Org
	Tab    string
	Repos  []orgRepoRow
	People []orgPersonRow
	Rooms  []orgRoomRow
	Groups []agentGroup
	Cmd    cmdBlock
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
		if strings.EqualFold(o.Slug, slug) {
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
	counts := map[string]int{"repos": len(a.orgProjects(r, org))}
	if members, err := a.store.ListOrgMembersFor(org.ID, browserPrincipal(r).ID, a.store.AccessEnforced()); err == nil {
		counts["people"] = len(members)
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
	return orgBand("/ui/"+url.PathEscape(org.Slug), current, counts, org.Role == store.OrgOwner)
}

// orgAgentGroups are the agents at the projects of the organisation, by machine.
func (a *app) orgAgentGroups(r *http.Request, org store.Org) ([]agentGroup, error) {
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
	cards, err := a.agentCards(r, pa, keep, now)
	if err != nil {
		return nil, err
	}
	return groupAgents(cards, nil), nil
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
			v.Repos = append(v.Repos, orgRepoRow{Name: name, Href: repoHref(p.Remote), Public: p.Shared, Remote: p.Remote,
				Issues: orZero(a.openIssues(pa, p.Remote)), Knowledge: orZero(a.knowledgeCount(pa, p.Remote))})
		}
	case "people":
		members, _ := a.store.ListOrgMembersFor(org.ID, browserPrincipal(r).ID, a.store.AccessEnforced())
		for _, m := range members {
			v.People = append(v.People, orgPersonRow{Name: store.NormalizeAccountName(m.Account), Role: m.Role, Self: m.AccountID == browserPrincipal(r).ID, Guest: m.Guest})
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
		v.Groups, _ = a.orgAgentGroups(r, org)
		v.fillAgentFilters(a.viewerOf(r, a.access(r)).kind != viewerGuest)
	}
	a.renderBrowser(w, r, "org", pageData{Title: org.Name, OrgName: org.Name, OrgBand: a.orgBandFor(r, org, tab), OrgV: v})
}

// orgTab answers /ui/{org}/{tab} for the tabs of an organisation. It reports
// whether it answered; an address that is a repository goes on to the repository.
func (a *app) orgTab(w http.ResponseWriter, r *http.Request) bool {
	tab := r.PathValue("b")
	switch tab {
	case "agents", "people", "rooms", "settings":
	default:
		return false
	}
	if r.PathValue("c") != "" {
		return false
	}
	org, ok := a.orgFor(r, r.PathValue("a"))
	if !ok {
		return false
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
