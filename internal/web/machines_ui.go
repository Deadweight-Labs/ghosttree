package web

import (
	"errors"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Maschinen in der Oberfläche (REQ-599, REQ-615): die Seite einer Maschine unter
// /ui/m/{maschine} und /ui/{org}/machines/{maschine}, die Liste der Maschinen
// einer Org und die Verwaltung der Freigaben. Wer die Maschine nicht erreicht
// (kein Besitzer, keine Org, keine Freigabe), bekommt 404, genau wie bei einer
// Maschine, die es nicht gibt. Verwaltet wird nur aus einer Web-Sitzung (#2411);
// den Weg dorthin sperren requireInteractive und der Store selbst.
//
// Einträge mit Projekt UND Maschine sind Projektwissen (Entscheidung #4349): sie
// stehen hier nur, wenn die Projektrolle sie freigibt, und ein Gast des Projekts
// bekommt sie nie zu sehen, denn mit ihnen verriete die Seite den Maschinennamen.

const (
	machineSessionsMax = 8
	machineAgentsMax   = 20
)

type machineEntryRow struct {
	Kind, Title, Href, Type, Project string
	Icon                             string
	// Shared sind die Orgs, an die der Eintrag freigegeben ist; CanShare sagt, ob
	// der Betrachter ihn freigeben darf (Verwalter, Eintrag ohne Projekt).
	Shared   []string
	CanShare bool
	// Free sind die Orgs, an die er noch freigegeben werden könnte.
	Free []composerOption
	ID   int64
}

type machineShareRow struct {
	Org, OrgHref, Role, RoleLabel, OSUser, PathPrefix, By, Age string
}

type machinePageView struct {
	Name, Base, Token                   string
	Relation, RelationLabel             string
	RelationTone                        string
	Owner, OwnerOrg, OwnerOrgHref       string
	FirstSeen, LastSeen                 string
	CanManage                           bool
	Entries                             []machineEntryRow
	Agents                              []agentCard
	Sessions                            []listRow
	SessionsHref                        string
	Shares                              []machineShareRow
	ShareOrgs, OwnerOrgs                []composerOption
	Roles                               []composerOption
	Items                               []tlItem
	Result                              *requestResult
	PersonOwned, HasAgents, HasSessions bool
}

// machineLinker gibt für den Betrachter die Adresse der Maschinenseite, oder ""
// für eine Maschine, die er nicht erreicht: Sessions und Agenten nennen eine
// Maschine nur dann als Link, wenn die Seite dahinter ihm offensteht.
func (a *app) machineLinker(r *http.Request) func(string) string {
	var reach map[string]bool
	return func(name string) string {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			return ""
		}
		if reach == nil {
			reach = map[string]bool{}
			if list, err := a.store.MachinesFor(browserPrincipal(r).ID); err == nil {
				for _, m := range list {
					reach[m.Name] = true
				}
			}
		}
		if !reach[name] {
			return ""
		}
		return "/ui/m/" + url.PathEscape(name)
	}
}

var machineRelationKeys = map[string]string{
	store.RelOwner: "machine.rel.owner", store.RelMaintainer: "machine.rel.maintainer", store.RelMember: "machine.rel.member",
	store.RelSharedRead: "machine.rel.shared_read", store.RelSharedWrite: "machine.rel.shared_write",
}

var machineRelationTone = map[string]string{
	store.RelOwner: "p-done", store.RelMaintainer: "p-done", store.RelMember: "p-ok", store.RelSharedRead: "p-warn", store.RelSharedWrite: "p-warn",
}

var machineRoleKeys = map[string]string{store.ShareRead: "machine.role.read", store.ShareWrite: "machine.role.write"}

// machineFailKeys übersetzt die Ursache einer gescheiterten Verwaltung.
var machineFailKeys = map[string]string{
	"unproven": "machine.fail.unproven", "not_shared": "machine.fail.not_shared", "not_shareable": "machine.fail.not_shareable",
	"org": "machine.fail.org", "invalid": "machine.fail.invalid", "failed": "machine.fail.failed",
}

var machineDoneKeys = map[string]string{
	"share": "machine.done.share", "unshare": "machine.done.unshare", "owner": "machine.done.owner",
	"entry_share": "machine.done.entry_share", "entry_unshare": "machine.done.entry_unshare",
}

func (a *app) registerMachinePages(mux *http.ServeMux) {
	a.handle(mux, "GET /ui/m/{machine}", a.requirePerson(http.HandlerFunc(a.machineRoute)))
	for suffix, h := range map[string]http.HandlerFunc{
		"share": a.machineShare, "unshare": a.machineUnshare, "owner": a.machineOwner, "entry": a.machineEntryShare,
	} {
		a.handle(mux, "POST /ui/m/{machine}/"+suffix, a.requirePerson(a.requireInteractive(limitBody(a.requireCSRF(h)))))
	}
}

// machineRoute ist /ui/m/{maschine}.
func (a *app) machineRoute(w http.ResponseWriter, r *http.Request) {
	m, ok := a.machineFor(w, r, r.PathValue("machine"))
	if !ok {
		return
	}
	a.machinePage(w, r, m, nil, "")
}

// machineFor liest die Maschine für den Betrachter; was er nicht erreicht, ist 404.
func (a *app) machineFor(w http.ResponseWriter, r *http.Request, name string) (store.Machine, bool) {
	m, err := a.store.MachineFor(browserPrincipal(r).ID, name)
	if err != nil {
		http.NotFound(w, r)
		return store.Machine{}, false
	}
	return m, true
}

// machineInOrg beantwortet /ui/{org}/machines und /ui/{org}/machines/{maschine}.
// Es meldet, ob es geantwortet hat; ein Repository dieses Namens in der Org
// geht vor, wie bei den übrigen Reitern der Org.
func (a *app) machineInOrg(w http.ResponseWriter, r *http.Request) bool {
	if r.PathValue("b") != "machines" || r.PathValue("d") != "" || r.PathValue("rest") != "" {
		return false
	}
	org, ok := a.orgFor(r, r.PathValue("a"))
	if !ok {
		return false
	}
	machine := r.PathValue("c")
	if machine == "" {
		remote, _, _ := parseRepoPath([]string{r.PathValue("a"), "machines"})
		opts, _ := a.projectOptions(r, a.access(r))
		for _, o := range opts {
			if o.Remote == remote {
				return false
			}
		}
		a.orgMachinesPage(w, r, org)
		return true
	}
	m, ok := a.machineFor(w, r, machine)
	if !ok {
		return true
	}
	if !machineOfOrg(m, org) {
		http.Redirect(w, r, "/ui/m/"+url.PathEscape(m.Name), http.StatusFound)
		return true
	}
	a.machinePage(w, r, m, &org, "")
	return true
}

// machineOfOrg: die Maschine gehört der Org oder ist an sie freigegeben.
func machineOfOrg(m store.Machine, org store.Org) bool {
	if strings.EqualFold(m.OwnerOrg, org.Slug) {
		return true
	}
	for _, s := range m.Shares {
		if strings.EqualFold(s.Org, org.Slug) {
			return true
		}
	}
	return false
}

// orgMachines sind die Maschinen der Org und die, die sie freigegeben bekam,
// soweit der Betrachter sie erreicht.
func (a *app) orgMachines(r *http.Request, org store.Org) []store.Machine {
	list, err := a.store.MachinesFor(browserPrincipal(r).ID)
	if err != nil {
		return nil
	}
	var out []store.Machine
	for _, m := range list {
		if machineOfOrg(m, org) {
			out = append(out, m)
		}
	}
	return out
}

// orgMachinesPage ist der Reiter „Maschinen“ einer Org.
func (a *app) orgMachinesPage(w http.ResponseWriter, r *http.Request, org store.Org) {
	l := a.langFor(r)
	now := overviewNow().UTC()
	base := "/ui/" + url.PathEscape(org.Slug) + "/machines/"
	v := &orgMachinesView{}
	for _, m := range a.orgMachines(r, org) {
		row := uiRow{Icon: "server", Tone: "feat", StateLabel: l.msg("machine.title"), Title: m.Name, Href: base + url.PathEscape(m.Name),
			Kind: label(l, machineRelationKeys, m.Relation), Age: shortAge(l, now, parseTime(m.LastSeen)), AgeTitle: l.dateTime(parseTime(m.LastSeen).Local()),
			Person: m.Owner, Pill: ownerPill(l, m, org), PillTone: machineRelationTone[m.Relation]}
		v.Rows = append(v.Rows, row)
	}
	a.renderBrowser(w, r, "orgmachines", pageData{Title: l.msg("ui.org.tab.machines"), OrgName: org.Name, OrgBand: a.orgBandFor(r, org, "machines"), OrgMachinesV: v})
}

type orgMachinesView struct{ Rows []uiRow }

// ownerPill nennt, in welchem Verhältnis die Maschine zur Org steht.
func ownerPill(l lang, m store.Machine, org store.Org) string {
	if strings.EqualFold(m.OwnerOrg, org.Slug) {
		return l.msg("machine.of_org")
	}
	for _, s := range m.Shares {
		if strings.EqualFold(s.Org, org.Slug) {
			return l.msg("machine.shared_with_org", label(l, machineRoleKeys, s.Role))
		}
	}
	return ""
}

// machinePage zeichnet die Seite. org ist gesetzt, wenn sie unter einer Org
// aufgerufen wurde: dann trägt sie das Band der Org.
func (a *app) machinePage(w http.ResponseWriter, r *http.Request, m store.Machine, org *store.Org, resultKey string) {
	l := a.langFor(r)
	pa := a.access(r)
	now := overviewNow().UTC()
	manage := m.Relation == store.RelOwner || m.Relation == store.RelMaintainer
	v := &machinePageView{Name: m.Name, Base: "/ui/m/" + url.PathEscape(m.Name), Token: csrfOf(r), Relation: m.Relation, RelationLabel: label(l, machineRelationKeys, m.Relation), RelationTone: machineRelationTone[m.Relation],
		Owner: m.Owner, OwnerOrg: m.OwnerOrg, FirstSeen: shortAgo(l, now, parseTime(m.FirstSeen)), LastSeen: shortAgo(l, now, parseTime(m.LastSeen)),
		CanManage: manage && interactive(r), PersonOwned: m.OwnerOrg == ""}
	if m.OwnerOrg != "" {
		v.OwnerOrgHref = "/ui/" + url.PathEscape(m.OwnerOrg)
	}
	for _, s := range m.Shares {
		v.Shares = append(v.Shares, machineShareRow{Org: s.Org, OrgHref: "/ui/" + url.PathEscape(s.Org), Role: s.Role, RoleLabel: label(l, machineRoleKeys, s.Role),
			OSUser: s.OSUser, PathPrefix: s.PathPrefix, By: s.GrantedBy, Age: shortAge(l, now, parseTime(s.CreatedAt))})
	}
	orgs, _ := a.store.ListOrgs(browserPrincipal(r).ID)
	shared := map[string]bool{strings.ToLower(m.OwnerOrg): true}
	for _, s := range m.Shares {
		shared[strings.ToLower(s.Org)] = true
	}
	for _, o := range orgs {
		if !shared[strings.ToLower(o.Slug)] {
			v.ShareOrgs = append(v.ShareOrgs, composerOption{Value: o.Slug, Label: o.Name})
		}
		if o.Role == store.OrgOwner && m.OwnerOrg == "" {
			v.OwnerOrgs = append(v.OwnerOrgs, composerOption{Value: o.Slug, Label: o.Name})
		}
	}
	v.Roles = []composerOption{{Value: store.ShareRead, Label: l.msg("machine.role.read"), Selected: true}, {Value: store.ShareWrite, Label: l.msg("machine.role.write")}}

	entries, _ := pa.MachineEntries(m.Name)
	for _, e := range entries {
		// Ein Gast des Projekts erfährt über den Eintrag nichts von der Maschine.
		if e.Project != "" && pa.IsProjectGuest(e.Project) {
			continue
		}
		row := machineEntryRow{Kind: e.Kind, ID: e.ID, Title: e.Title, Project: projectName(e.Project), Icon: "book"}
		if e.Kind == store.EntryKnowledge {
			row.Href = "/ui/knowledge/" + strconv.FormatInt(e.ID, 10)
			row.Type = label(l, typeKeys, e.Type)
		} else {
			row.Icon, row.Type = "play", l.msg("machine.runbook")
			if e.Project != "" {
				row.Href = repoHref(e.Project) + "/runbooks/" + url.PathEscape(e.Title)
			}
		}
		if e.Project == "" {
			row.Shared = a.store.EntryShareOrgs(browserPrincipal(r), e.Kind, e.ID)
			row.CanShare = v.CanManage
			for _, o := range v.Shares {
				if !slices.ContainsFunc(row.Shared, func(s string) bool { return strings.EqualFold(s, o.Org) }) {
					row.Free = append(row.Free, composerOption{Value: o.Org, Label: o.Org})
				}
			}
			if m.OwnerOrg != "" && !slices.ContainsFunc(row.Shared, func(s string) bool { return strings.EqualFold(s, m.OwnerOrg) }) {
				row.Free = append(row.Free, composerOption{Value: m.OwnerOrg, Label: m.OwnerOrg})
			}
		}
		v.Entries = append(v.Entries, row)
	}

	if a.shellBaseFor(r).who.kind != viewerGuest {
		if rows, err := a.listAgents(r, pa, "", now, agentsMax*4); err == nil {
			if cards, err := a.agentCards(r, pa, rows, now); err == nil {
				for _, c := range cards {
					if strings.EqualFold(c.Machine, m.Name) && len(v.Agents) < machineAgentsMax {
						v.Agents = append(v.Agents, c)
					}
				}
			}
		}
	}
	if page, err := a.store.BrowseSessions(pa, store.SessionFilter{Machine: m.Name}, "", machineSessionsMax*3); err == nil {
		var ids []int64
		for _, row := range page.Rows {
			if row.Readable {
				ids = append(ids, row.Session.ID)
			}
		}
		links, _ := a.store.SessionLinks(pa, ids)
		for _, row := range page.Rows {
			if !row.Readable || len(v.Sessions) >= machineSessionsMax {
				continue
			}
			lr := a.listRow(l, row, links[row.Session.ID], time.Now().UTC())
			lr.Machine = ""
			v.Sessions = append(v.Sessions, lr)
		}
		v.SessionsHref = "/ui/sessions?machine=" + url.QueryEscape(m.Name)
	}
	v.HasAgents, v.HasSessions = len(v.Agents) > 0, len(v.Sessions) > 0
	v.Items = machineItems(l, m, now)

	q := r.URL.Query()
	switch {
	case q.Get("fail") != "":
		key, ok := machineFailKeys[q.Get("fail")]
		if !ok {
			key = "machine.fail.failed"
		}
		v.Result = &requestResult{Text: l.msg(key), Kind: "failed", Fail: true}
	case q.Get("done") != "":
		if key, ok := machineDoneKeys[q.Get("done")]; ok {
			v.Result = &requestResult{Text: l.msg(key), Kind: "done"}
		}
	}
	data := pageData{Title: m.Name, MachineV: v}
	if org != nil {
		data.OrgName, data.OrgBand = org.Name, a.orgBandFor(r, *org, "machines")
	}
	a.renderBrowser(w, r, "machine", data)
}

var machineEventKeys = map[string]string{
	"share": "machine.ev.share", "unshare": "machine.ev.unshare", "owner_org": "machine.ev.owner_org",
	"entry_share": "machine.ev.entry_share", "entry_unshare": "machine.ev.entry_unshare", "release": "machine.ev.release", "transfer": "machine.ev.transfer",
}

var machineEventIcons = map[string]string{
	"share": "link", "unshare": "drop", "owner_org": "people", "entry_share": "link", "entry_unshare": "drop", "release": "drop", "transfer": "people",
}

// machineItems ist das Protokoll der Freigaben, das nur Verwalter lesen: wer
// wann was an wen freigegeben oder zurückgenommen hat, jüngstes zuletzt.
func machineItems(l lang, m store.Machine, now time.Time) []tlItem {
	var items []tlItem
	for i := len(m.Events) - 1; i >= 0; i-- {
		e := m.Events[i]
		head := html.EscapeString(l.msg(machineEventKeyOf(e.Action)))
		if e.Subject != "" {
			head += " <b>" + html.EscapeString(e.Subject) + "</b>"
		}
		body := ""
		if e.Detail != "" {
			body = html.EscapeString(machineEventDetail(l, e))
		}
		items = append(items, tlItem{ID: "event-" + strconv.Itoa(len(m.Events)-i), Act: "machine." + e.Action, Who: e.Actor, Icon: machineEventIcons[e.Action],
			Head: template.HTML(head), Age: shortAgo(l, now, parseTime(e.CreatedAt)), Body: template.HTML(body)})
	}
	return items
}

func machineEventKeyOf(action string) string {
	if key, ok := machineEventKeys[action]; ok {
		return key
	}
	return "machine.ev.other"
}

// ---- Verwaltung (nur aus einer Web-Sitzung)

// machineManage schließt die Verwaltungsformulare ab: die Maschine muss den
// Betrachter etwas angehen (sonst 404), das Ergebnis führt zurück auf die Seite.
func (a *app) machineManage(w http.ResponseWriter, r *http.Request, done string, act func(store.Principal, string) error) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m, ok := a.machineFor(w, r, r.PathValue("machine"))
	if !ok {
		return
	}
	back := "/ui/m/" + url.PathEscape(m.Name)
	if m.Relation != store.RelOwner && m.Relation != store.RelMaintainer {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	for _, f := range []string{"org", "role", "os_user", "path_prefix", "kind", "do"} {
		if tooLong(r.FormValue(f), maxFieldChars) {
			http.Error(w, "invalid input", http.StatusBadRequest)
			return
		}
	}
	if err := act(browserPrincipal(r), m.Name); err != nil {
		switch {
		case errors.Is(err, store.ErrWebSessionRequired), errors.Is(err, store.ErrMachineNotManager):
			http.Error(w, "forbidden", http.StatusForbidden)
		case errors.Is(err, store.ErrMachineNotFound), errors.Is(err, store.ErrAccessNotFound):
			http.NotFound(w, r)
		default:
			http.Redirect(w, r, back+"?fail="+machineFailCode(err), http.StatusSeeOther)
		}
		return
	}
	http.Redirect(w, r, back+"?done="+done, http.StatusSeeOther)
}

func machineFailCode(err error) string {
	switch {
	case errors.Is(err, store.ErrMachineUnproven):
		return "unproven"
	case errors.Is(err, store.ErrNoMachineShare), errors.Is(err, store.ErrShareNotFound):
		return "not_shared"
	case errors.Is(err, store.ErrEntryNotShareable):
		return "not_shareable"
	case errors.Is(err, store.ErrOrgNotFound):
		return "org"
	case errors.Is(err, store.ErrInvalidInput):
		return "invalid"
	}
	return "failed"
}

func (a *app) machineShare(w http.ResponseWriter, r *http.Request) {
	a.machineManage(w, r, "share", func(p store.Principal, name string) error {
		_, err := a.store.ShareMachine(p, name, strings.TrimSpace(r.FormValue("org")), r.FormValue("role"), strings.TrimSpace(r.FormValue("os_user")), strings.TrimSpace(r.FormValue("path_prefix")))
		return err
	})
}

func (a *app) machineUnshare(w http.ResponseWriter, r *http.Request) {
	a.machineManage(w, r, "unshare", func(p store.Principal, name string) error {
		return a.store.UnshareMachine(p, name, strings.TrimSpace(r.FormValue("org")))
	})
}

func (a *app) machineOwner(w http.ResponseWriter, r *http.Request) {
	a.machineManage(w, r, "owner", func(p store.Principal, name string) error {
		return a.store.SetMachineOwnerOrg(p, name, strings.TrimSpace(r.FormValue("org")))
	})
}

// machineEntryShare gibt einen Eintrag der Maschine an eine Org frei oder
// nimmt die Freigabe zurück (do=unshare).
func (a *app) machineEntryShare(w http.ResponseWriter, r *http.Request) {
	unshare := r.FormValue("do") == "unshare"
	done := "entry_share"
	if unshare {
		done = "entry_unshare"
	}
	a.machineManage(w, r, done, func(p store.Principal, _ string) error {
		kind := r.FormValue("kind")
		id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
		if err != nil || (kind != store.EntryKnowledge && kind != store.EntryRunbook) {
			return store.ErrInvalidInput
		}
		org := strings.TrimSpace(r.FormValue("org"))
		if unshare {
			return a.store.UnshareEntry(p, kind, id, org)
		}
		return a.store.ShareEntry(p, kind, id, org)
	})
}

// machineEventDetail macht aus dem Vermerk eines Ereignisses einen Text für Menschen:
// die Rolle einer Freigabe, die Art und Nummer eines Eintrags.
func machineEventDetail(l lang, e store.MachineEvent) string {
	switch e.Action {
	case "share":
		return label(l, machineRoleKeys, e.Detail)
	case "entry_share", "entry_unshare":
		kind, num, ok := strings.Cut(e.Detail, " ")
		if !ok {
			return e.Detail
		}
		if kind == store.EntryRunbook {
			return l.msg("machine.runbook") + " #" + num
		}
		return l.msg("machine.entries") + " #" + num
	}
	return e.Detail
}
