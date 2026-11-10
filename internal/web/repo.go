package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The repository pages live at /ui/{org}/{repo}[/{tab}[/{id}]]: org and repo are
// the owner and name of the project's normalized remote without "github.com/".
// A project on another host puts the host first (/ui/gitlab.com/{org}/{repo}).
// Every fixed route (/ui/requests, /ui/orgs, …) wins over these wildcards, so an
// owner named like a fixed route is reached through the host form instead
// (repoHref). Etappe A renders the existing pages with the project set; the
// repository head above them is the same for every tab.

const (
	repoHost    = "github.com"
	repoCountAt = 100
)

type repoCtxKey struct{}

// repoView is the head of a repository page: breadcrumb and tab row.
type repoView struct {
	Org, Name, Initials string
	OrgHref, Href       string
	// External is the page of the project on its host, for github.com only.
	External string
	Project  string
	Tabs     []repoTab
}

type repoTab struct {
	Key, Label, Href string
	// Count is shown beside the label, "" shows nothing.
	Count   string
	Current bool
	// More puts the tab into the second group of the band (behind the separator,
	// as icon with number) instead of the row.
	More bool
	// Mentions is the number of unread mentions for the viewer behind the tab.
	Mentions int
}

// repoTabIcons names the icon of each tab of the band.
var repoTabIcons = map[string]string{"code": "code", "issues": "issue", "pulls": "pr", "knowledge": "book", "docs": "file", "runbooks": "play", "rooms": "chat", "sessions": "term", "commits": "commit", "tasks": "tasks", "packages": "list", "settings": "gear"}

// Band is the tab band under the header: the row, and behind the separator the
// tabs of the second group.
func (v *repoView) Band() *bandView {
	b := &bandView{Label: "repo.tabs"}
	for _, t := range v.Tabs {
		bt := bandTab{Key: t.Key, Label: t.Label, Href: t.Href, Icon: repoTabIcons[t.Key], Count: t.Count, Mentions: t.Mentions, Current: t.Current}
		if t.More {
			b.Extras = append(b.Extras, bt)
		} else {
			b.Tabs = append(b.Tabs, bt)
		}
	}
	return b
}

func repoFrom(r *http.Request) *repoView {
	v, _ := r.Context().Value(repoCtxKey{}).(*repoView)
	return v
}

var (
	reservedOnce sync.Once
	reservedSegs map[string]bool
)

// reservedSegment: the first path segment of a fixed route under /ui/. A
// repository owner with such a name cannot be reached at /ui/{owner}/{repo}.
func reservedSegment(seg string) bool {
	reservedOnce.Do(func() {
		reservedSegs = map[string]bool{}
		for pattern := range webRoutes {
			_, path, _ := strings.Cut(pattern, " ")
			rest, ok := strings.CutPrefix(path, "/ui/")
			if !ok {
				continue
			}
			first, _, _ := strings.Cut(rest, "/")
			if first != "" && !strings.HasPrefix(first, "{") {
				reservedSegs[first] = true
			}
		}
	})
	return reservedSegs[seg]
}

// repoHref is the address of a project's code page. The host is left out for
// github.com unless the owner collides with a fixed route.
func repoHref(remote string) string {
	remote = scope.NormalizeRemote(remote)
	rest, ok := strings.CutPrefix(remote, repoHost+"/")
	if !ok || reservedSegment(strings.SplitN(rest, "/", 2)[0]) {
		return "/ui/" + pathEscapeAll(remote)
	}
	return "/ui/" + pathEscapeAll(rest)
}

func pathEscapeAll(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// parseRepoPath reads the segments after /ui/: the project's remote and what
// follows it. ok is false when there are too few segments.
func parseRepoPath(segs []string) (remote string, rest []string, ok bool) {
	if len(segs) >= 3 && strings.Contains(segs[0], ".") {
		return scope.NormalizeRemote(strings.Join(segs[:3], "/")), segs[3:], true
	}
	if len(segs) >= 2 {
		return scope.NormalizeRemote(repoHost + "/" + segs[0] + "/" + segs[1]), segs[2:], true
	}
	return "", nil, false
}

func (a *app) registerRepoPages(mux *http.ServeMux) {
	a.handle(mux, "GET /ui/{a}", a.requirePerson(http.HandlerFunc(a.orgRoute)))
	for _, pattern := range []string{"GET /ui/{a}/{b}", "GET /ui/{a}/{b}/{c}", "GET /ui/{a}/{b}/{c}/{d}"} {
		a.handle(mux, pattern, a.requirePerson(http.HandlerFunc(a.repoRoute)))
	}
	// Folders and files below a branch: any depth (…/tree/{branch}/{path…}).
	a.handle(mux, "GET /ui/{a}/{b}/{c}/{d}/{rest...}", a.requirePerson(http.HandlerFunc(a.repoRoute)))
}

// orgRoute is /ui/{org}. The organization page itself belongs to the sharing
// work (REQ-596); until it moves here the address leads to the team page. An
// organization the viewer is not in is as unknown as one that does not exist.
func (a *app) orgRoute(w http.ResponseWriter, r *http.Request) {
	slug := strings.ToLower(r.PathValue("a"))
	if orgs, err := a.store.ListOrgs(browserPrincipal(r).ID); err == nil {
		for _, o := range orgs {
			if strings.ToLower(o.Slug) != slug {
				continue
			}
			if o.Guest {
				// A guest sees its project, not the organization (14 A, #4376).
				http.NotFound(w, r)
				return
			}
			a.orgPage(w, r, o, "repos")
			return
		}
	}
	if a.redirectMovedOrg(w, r, slug, "") {
		return
	}
	// The owner segment of a project the viewer can see counts as the
	// organization too: that is what the breadcrumb of a repository links to.
	pa := a.access(r)
	opts, _ := a.projectOptions(r, pa)
	for _, o := range opts {
		if rest, ok := strings.CutPrefix(o.Remote, repoHost+"/"); ok && strings.SplitN(rest, "/", 2)[0] == slug {
			http.Redirect(w, r, "/ui/orgs", http.StatusFound)
			return
		}
	}
	http.NotFound(w, r)
}

// repoRoute answers every address below /ui/{org}/{repo}. A project the viewer
// has no role in answers 404, exactly as one that does not exist.
func (a *app) repoRoute(w http.ResponseWriter, r *http.Request) {
	if a.machineInOrg(w, r) || a.orgTab(w, r) {
		return
	}
	var segs []string
	for _, k := range []string{"a", "b", "c", "d"} {
		if v := r.PathValue(k); v != "" {
			segs = append(segs, v)
		}
	}
	// Below tree/ and blob/ the path has any depth: the wildcard of the longest
	// address carries the rest.
	if deep := r.PathValue("rest"); deep != "" {
		segs = append(segs, strings.Split(deep, "/")...)
	}
	remote, rest, ok := parseRepoPath(segs)
	isCode := ok && len(rest) > 0 && (rest[0] == "tree" || rest[0] == "blob")
	if !ok || (len(rest) > 2 && !isCode) {
		http.NotFound(w, r)
		return
	}
	pa := a.access(r)
	opts, _ := a.projectOptions(r, pa)
	if !slices.ContainsFunc(opts, func(o projectOption) bool { return o.Remote == remote }) {
		http.NotFound(w, r)
		return
	}
	tab := "code"
	if len(rest) > 0 {
		tab = rest[0]
	}
	var cp *codePath
	if isCode {
		if cp, ok = a.resolveCodePath(w, r, pa, remote, rest[0], rest[1:]); !ok {
			return
		}
		tab = "code"
	}
	switch {
	case len(rest) == 2 && tab != "code":
		a.repoDetail(w, r, remote, tab, rest[1])
		return
	case tab == "pains":
		// Pains are a segment of the issue list since decision #4315.
		http.Redirect(w, r, repoHref(remote)+"/issues?q="+url.QueryEscape("is:pain is:open"), http.StatusFound)
		return
	case tab == "ideas":
		http.Redirect(w, r, repoHref(remote)+"/issues?q="+url.QueryEscape("is:idea is:open"), http.StatusFound)
		return
	case tab == "rooms":
		if a.viewerOf(r, pa).kind == viewerGuest {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/ui/coord?room="+url.QueryEscape(store.RoomKeyForProject(remote)), http.StatusFound)
		return
	}
	var h http.HandlerFunc
	switch tab {
	case "code":
		h = a.repoCode
	case "issues":
		h = a.issuesPage
	case "pulls":
		h = a.pullsPage
	case "commits":
		h = a.commitsPage
	case "branches":
		// The list of branches is a page of the Code tab: the tab stays open.
		h = a.branchesPage
		tab = "code"
	case "runbooks":
		h = a.runbooksPage
	case "tasks":
		h = a.repoTasksPage
	case "packages":
		h = a.repoPackagesPage
	case "features":
		h = a.featuresPage
	case "knowledge":
		h = a.knowledgePage
	case "docs":
		h = a.docsPage
	case "review", "context":
		// Pages of the knowledge tab: the tab stays open in the band.
		h = a.reviewPage
		if tab == "context" {
			h = a.contextPage
		}
		tab = "knowledge"
	case "sessions":
		h = a.sessionsPage
	case "settings":
		h = a.repoSettings
	default:
		http.NotFound(w, r)
		return
	}
	// The existing page reads its project from ?project=; the repository in the
	// address is the one project, whatever the query says. This has to happen
	// before the first look at the shell: its project choice is read once.
	q := r.URL.Query()
	q.Set("project", remote)
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = q.Encode()
	view := a.repoViewFor(r2, pa, a.shellBaseFor(r2).who.kind, remote, tab)
	ctx := context.WithValue(r2.Context(), repoCtxKey{}, view)
	if cp != nil {
		ctx = context.WithValue(ctx, codePathKey{}, cp)
	}
	h(w, r2.WithContext(ctx))
}

// repoDetail leads /ui/{org}/{repo}/{tab}/{id} to the page of that object. The
// pages behind check access themselves.
func (a *app) repoDetail(w http.ResponseWriter, r *http.Request, remote, tab, id string) {
	var target string
	switch tab {
	case "issues":
		if id == "new" {
			a.issueNew(w, r, remote)
			return
		}
		a.issueDetail(w, r, remote, id)
		return
	case "runbooks":
		if id == runbookNew {
			a.runbookNewPage(w, r, remote)
			return
		}
		a.runbookPage(w, r, remote, id)
		return
	case "tasks":
		a.taskDetail(w, r, remote, id)
		return
	case "packages":
		a.packageDetail(w, r, remote, id)
		return
	case "features":
		a.featureDetail(w, r, remote, id)
		return
	case "pulls":
		a.pullDetail(w, r, remote, id)
		return
	case "commits":
		a.commitDetail(w, r, remote, id)
		return
	case "pains":
		a.painInRepo(w, r, remote, id)
		return
	case "ideas":
		a.ideaInRepo(w, r, remote, id)
		return
	case "knowledge":
		a.knowledgeInRepo(w, r, remote, id)
		return
	case "docs":
		a.docPage(w, r, remote, id)
		return
	case "sessions":
		a.sessionInRepo(w, r, remote, id)
		return
	case "rooms":
		target = "/ui/coord/thread/"
	default:
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, target+url.PathEscape(id), http.StatusFound)
}

// repoViewFor builds the head for one project: the breadcrumb and the tabs with
// their counts. A guest has no rooms; the counts are the ones the viewer's own
// lists show.
func (a *app) repoViewFor(r *http.Request, pa *store.ProjectAccess, kind viewerKind, remote, current string) *repoView {
	owner, name := remote, remote
	if i := strings.LastIndex(remote, "/"); i >= 0 {
		name = remote[i+1:]
		owner = remote[:i]
	}
	external := ""
	if rest, ok := strings.CutPrefix(owner, repoHost+"/"); ok {
		owner = rest
		external = "https://" + remote
	}
	base := repoHref(remote)
	v := &repoView{Org: owner, Name: name, Initials: initialsOf(owner), Href: base, OrgHref: "/ui/" + pathEscapeAll(owner), External: external, Project: remote}
	tab := func(key, label string, count string, more bool) {
		href := base
		if key != "code" {
			href += "/" + key
		}
		v.Tabs = append(v.Tabs, repoTab{Key: key, Label: label, Href: href, Count: count, Current: key == current, More: more})
	}
	mentions := func(key string) {
		if r != nil && key == "rooms" {
			v.Tabs[len(v.Tabs)-1].Mentions = a.roomMentions(r, remote)
		}
	}
	tab("code", "repo.tab.code", "", false)
	tab("issues", "repo.tab.issues", a.openIssues(pa, remote), false)
	tab("pulls", "repo.tab.pulls", a.pullCount(pa, remote), false)
	tab("knowledge", "repo.tab.knowledge", a.knowledgeCount(pa, remote), false)
	// Documents: the tab shows once there is one the viewer may read.
	if n := a.docCount(pa, remote); n > 0 || current == "docs" {
		tab("docs", "repo.tab.docs", countText(n), false)
	}
	tab("runbooks", "repo.tab.runbooks", "", false)
	// Behind the separator, as in the mock: rooms, then the lists that show as icon
	// with their number (sessions, features, commits), settings last.
	if kind != viewerGuest {
		tab("rooms", "repo.tab.rooms", "", true)
		mentions("rooms")
	}
	tab("sessions", "repo.tab.sessions", a.sessionCount(pa, remote), true)
	tab("features", "repo.tab.features", a.featureCount(pa, remote), true)
	tab("commits", "repo.tab.commits", a.commitCount(pa, remote), true)
	tab("tasks", "repo.tab.tasks", a.tasksTabCount(pa, remote), true)
	tab("packages", "repo.tab.packages", a.packageCount(pa, remote), true)
	if pa != nil && pa.Check(remote, store.ResLabel, store.ActEdit, store.Object{}) == nil {
		tab("settings", "repo.tab.settings", "", true)
	}
	return v
}

// sessionCount is the number of sessions the viewer can list for the project, up
// to repoCountAt.
func (a *app) sessionCount(pa *store.ProjectAccess, remote string) string {
	if pa == nil {
		return ""
	}
	page, err := a.store.BrowseSessions(pa, store.SessionFilter{Project: remote}, "", repoCountAt)
	if err != nil {
		return ""
	}
	return countText(len(page.Rows))
}

// packageCount is the number of open work packages the viewer can list.
func (a *app) packageCount(pa *store.ProjectAccess, remote string) string {
	if pa == nil {
		return ""
	}
	list, err := pa.PackagesOfProject(remote, requestdomain.PackageOpen)
	if err != nil {
		return ""
	}
	return countText(len(list))
}

// commitCount is the number of commits ghosttree knows for the project.
func (a *app) commitCount(pa *store.ProjectAccess, remote string) string {
	if pa == nil {
		return ""
	}
	list, err := pa.Commits(remote, "", 1, 0)
	if err != nil {
		return ""
	}
	return countText(list.Total)
}

// roomMentions is the number of unread mentions of the viewer in the room of the
// project: the number the inbox shows for that room, nothing else.
func (a *app) roomMentions(r *http.Request, remote string) int {
	summaries, err := a.browserCoord(r).RoomSummaries()
	if err != nil {
		return 0
	}
	n := 0
	for _, s := range summaries {
		if s.Room.Key == store.RoomKeyForProject(remote) {
			n += int(s.MentionUnread)
		}
	}
	return n
}

func initialsOf(s string) string {
	var out []rune
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out = append(out, unicode.ToUpper(r))
		}
		if len(out) == 2 {
			break
		}
	}
	return string(out)
}

func countText(n int) string {
	switch {
	case n <= 0:
		return ""
	case n >= repoCountAt:
		return strconv.Itoa(repoCountAt-1) + "+"
	}
	return strconv.Itoa(n)
}

// openIssueCount counts the open issues the viewer can list, up to repoCountAt.
// It asks the list's own query: the store's counter reads the branch axis of a
// request differently and misses requests that have a branch.
func (a *app) openIssueCount(pa *store.ProjectAccess, remote string) int {
	page, err := a.store.SearchRequestsWith(pa.RequestFilter(requestdomain.SearchFilter{Scope: scope.Axes{Project: remote}, State: "open", Limit: repoCountAt}), store.SearchAuto)
	if err != nil {
		return 0
	}
	return len(page.Results)
}

func (a *app) openIssues(pa *store.ProjectAccess, remote string) string {
	return countText(a.openIssueCount(pa, remote))
}

func (a *app) knowledgeCount(pa *store.ProjectAccess, remote string) string {
	pa.Filtered()
	// The tab counts what the entry list behind it shows (knowledgePage), proposals
	// waiting for review included: the same read and the same test.
	return a.knowledgeCountText(pa, remote)
}
