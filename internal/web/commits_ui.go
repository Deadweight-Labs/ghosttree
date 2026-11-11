package web

import (
	"html"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Commits in der Oberfläche (REQ-599): der Reiter „Commits“ mit der Liste je
// Projekt oder Branch und die Seite eines Commits unter
// /ui/{org}/{repo}/commits/{sha}. Der Server kennt nur Commits, an denen
// Einträge hängen oder die ein PR trägt; die Seite zeigt, was er weiß, und
// erfindet keine Dateiliste und keinen Build-Stand.

const commitsPerPage = 30

type commitDay struct {
	Label string
	Rows  []commitRow
}

type commitsView struct {
	Base, Branch, BranchLabel string
	Branches                  []codeBranchOpt
	Days                      []commitDay
	Newer, Older              string
	Empty                     bool
	// EmptyKey is the line shown when the list has no commit, EmptyHref and
	// EmptyAction the one way back out of it.
	EmptyKey, EmptyAction, EmptyHref string
	GitHub, GitHubTip                string
}

// commitsPage ist /ui/{org}/{repo}/commits[?branch=…&offset=…].
func (a *app) commitsPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	view := repoFrom(r)
	project := view.Project
	pa := a.access(r)
	names, def := a.codeBranches(r, pa, project)
	branch := r.URL.Query().Get("branch")
	if branch != "" && !slices.Contains(names, branch) {
		branch = ""
	}
	offset := 0
	if n, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && n > 0 {
		offset = min(n, 100000)
	}
	list, err := pa.Commits(project, branch, commitsPerPage, offset)
	if err != nil {
		a.pullError(w, r, err)
		return
	}
	v := &commitsView{Base: view.Href + "/commits", Branch: branch}
	href := func(b string, off int) string {
		args := []string{}
		if b != "" {
			args = append(args, "branch", b)
		}
		if off > 0 {
			args = append(args, "offset", strconv.Itoa(off))
		}
		return pathWith(v.Base, args...)
	}
	v.BranchLabel = l.msg("commits.all_branches")
	if branch != "" {
		v.BranchLabel = branch
	}
	v.Branches = append(v.Branches, codeBranchOpt{Name: l.msg("commits.all_branches"), Href: href("", 0), Current: branch == ""})
	for _, n := range names {
		v.Branches = append(v.Branches, codeBranchOpt{Name: n, Href: href(n, 0), Current: n == branch, Default: n == def})
	}
	seeAll := a.seesAllBranches(pa, project)
	now := overviewNow().UTC()
	for _, c := range list.Items {
		row := a.commitRowOf(l, view, c, names, seeAll, now)
		day := l.msg("commits.day", l.date(parseTime(c.At).Local(), true))
		if parseTime(c.At).IsZero() {
			day = l.msg("commits.day_unknown")
		}
		if n := len(v.Days); n == 0 || v.Days[n-1].Label != day {
			v.Days = append(v.Days, commitDay{Label: day})
		}
		v.Days[len(v.Days)-1].Rows = append(v.Days[len(v.Days)-1].Rows, row)
	}
	if offset > 0 {
		v.Newer = href(branch, max(0, offset-commitsPerPage))
	}
	if offset+len(list.Items) < list.Total {
		v.Older = href(branch, offset+commitsPerPage)
	}
	v.Empty = len(list.Items) == 0
	switch {
	case !v.Empty:
	case offset > 0:
		v.EmptyKey, v.EmptyAction, v.EmptyHref = "commits.empty_page", "commits.first_page", href(branch, 0)
	case branch != "":
		v.EmptyKey, v.EmptyAction, v.EmptyHref = "commits.empty_branch", "commits.all_branches", href("", 0)
	default:
		v.EmptyKey = "commits.empty"
	}
	if view.External != "" {
		v.GitHub, v.GitHubTip = view.External+"/commits", l.msg("commits.gh_list", v.BranchLabel)
		if branch != "" {
			v.GitHub = view.External + "/commits/" + pathEscapeAll(branch)
		} else {
			v.GitHubTip = l.msg("commits.gh_all")
		}
	}
	a.renderBrowser(w, r, "commits", pageData{Title: l.msg("repo.tab.commits"), Project: project, CommitsV: v})
}

// commitSide is what the side column adds to the head: pull requests and branches
// are in the head already, so only the requests the commit spoke for stand here.
type commitSide struct {
	Requests []sideRef
}

type commitView struct {
	Title, Short, SHA, Who, At, AtTitle string
	HasTitle                            bool
	PRs                                 []commitPR
	Branches                            []commitBranch
	GitHub, GitHubTip                   string
	// DoneHref is the quick filter "What was done here?" for this commit.
	DoneHref    string
	Items       []tlItem
	Files       []commitFile
	FilesTotal  int
	Side        commitSide
	Empty       bool
	CommitsHref string
	Entries     int
	// What GitHub says about the commit (P13.5); every part is empty, and its block
	// not shown, when it was not fetched or the viewer may not read it.
	Body                      string
	Co                        []string
	Parents                   []commitParent
	AvKind                    string
	CI, CIIcon, CITitle       string
	CodeHref, CodeTip         string
	Changed                   []commitChange
	ChangedTotal, ChangedMore int
	Additions, Deletions      string
	AtRaw                     string
}

type commitFile struct{ Path, Href, Desc, Who string }

// commitParent is a parent of the commit as a pill: Href leads to its page here
// when the server knows it, else to GitHub, else nowhere.
type commitParent struct{ Short, SHA, Href string }

// commitChange is one changed file with its lines. Bar is five blocks, "a" for
// added and "d" for removed, in the share the lines have.
type commitChange struct {
	Path, Href, Desc string
	// Add and Del are the lines as written ("+212", "−18").
	Add, Del string
	Bar      []string
}

// commitEntryHref ist die Adresse eines Eintrags im Repository.
func commitEntryHref(view *repoView, def string, visible branchVisible, e store.CommitEntry) string {
	switch e.Kind {
	case store.CommitKindKnowledge:
		return knowledgeHref(view.Project, e.ID)
	case store.CommitKindRequest:
		return view.Href + "/issues/" + strconv.FormatInt(e.ID, 10)
	case store.CommitKindPain:
		return view.Href + "/pains/" + strconv.FormatInt(e.ID, 10)
	case store.CommitKindIdea:
		return view.Href + "/ideas/" + strconv.FormatInt(e.ID, 10)
	case store.CommitKindFeature:
		return view.Href + "/features/" + strconv.FormatInt(e.ID, 10)
	case store.CommitKindGhost:
		if e.Path != "" {
			branch := firstNonEmpty(e.Branch, def)
			if !visible(branch) {
				branch = def
			}
			return codeLinks{base: view.Href, def: def}.blob(branch, e.Path)
		}
	case store.CommitKindMessage:
		return "/ui/coord?room=" + url.QueryEscape(store.RoomKeyForProject(view.Project))
	case store.CommitKindComment:
		switch e.ObjectKind {
		case store.CommentOnRequest:
			return view.Href + "/issues/" + strconv.FormatInt(e.ObjectID, 10)
		case store.CommentOnIdea:
			return view.Href + "/ideas/" + strconv.FormatInt(e.ObjectID, 10)
		case store.CommentOnPain:
			return view.Href + "/pains/" + strconv.FormatInt(e.ObjectID, 10)
		case store.CommentOnKnowledge:
			return view.Href + "/knowledge/" + strconv.FormatInt(e.ObjectID, 10)
		}
	}
	return ""
}

// commitEvKeys are the sentences of an entry by its kind.
var commitEvKeys = map[string]string{store.CommitKindKnowledge: "commit.ev.knowledge", store.CommitKindRequest: "commit.ev.request", store.CommitKindPain: "commit.ev.pain",
	store.CommitKindIdea: "commit.ev.idea", store.CommitKindFeature: "commit.ev.feature", store.CommitKindComment: "commit.ev.comment", store.CommitKindGhost: "commit.ev.ghost", store.CommitKindMessage: "commit.ev.message"}

var commitKindIcons = map[string]string{store.CommitKindKnowledge: "book", store.CommitKindRequest: "issue", store.CommitKindPain: "pain",
	store.CommitKindIdea: "idea", store.CommitKindFeature: "feature", store.CommitKindGhost: "ghost", store.CommitKindComment: "chat", store.CommitKindMessage: "chat"}

var commitKindTones = map[string]string{store.CommitKindKnowledge: "warn", store.CommitKindRequest: "ok", store.CommitKindPain: "warn",
	store.CommitKindIdea: "warn", store.CommitKindFeature: "don", store.CommitKindGhost: "acc", store.CommitKindMessage: "acc"}

// commitDetail ist /ui/{org}/{repo}/commits/{sha}.
func (a *app) commitDetail(w http.ResponseWriter, r *http.Request, remote, sha string) {
	l := a.langFor(r)
	pa := a.access(r)
	d, err := pa.CommitView(remote, sha)
	if err != nil {
		a.pullError(w, r, err)
		return
	}
	r2, view := a.repoDetailRequest(r, pa, remote, "commits")
	names, def := a.codeBranches(r2, pa, remote)
	seeAll := a.seesAllBranches(pa, remote)
	visible := a.branchRule(r2, pa, remote)
	now := overviewNow().UTC()
	row := a.commitRowOf(l, view, d.CommitInfo, names, seeAll, now)
	v := &commitView{Short: row.Short, SHA: d.SHA, Who: row.Who, PRs: row.PRs, Branches: row.Branches, HasTitle: d.Title != "", Entries: len(d.Said), CommitsHref: view.Href + "/commits",
		Body: d.Message, Co: row.Co, CI: row.CI, CIIcon: row.CIIcon, CITitle: row.CITitle, CodeHref: row.CodeHref, CodeTip: row.CodeTip}
	for _, p := range d.Parents {
		cp := commitParent{Short: shortSHA(p), SHA: p}
		if _, err := pa.CommitView(remote, p); err == nil {
			cp.Href = view.Href + "/commits/" + p
		} else if view.External != "" {
			cp.Href = view.External + "/commit/" + p
		}
		v.Parents = append(v.Parents, cp)
	}
	v.Title = firstNonEmpty(d.Title, l.msg("commit.untitled", row.Short))
	if t := parseTime(d.At); !t.IsZero() {
		v.At, v.AtTitle = shortAgo(l, now, t), l.dateTime(t.Local())
	}
	if view.External != "" {
		v.GitHub, v.GitHubTip = view.External+"/commit/"+d.SHA, l.msg("commit.gh", row.Short)
	}
	v.DoneHref = pathWith("/ui/search", "project", remote, "preset", "pr-ergebnis", "commit", d.SHA)
	reqSeen := map[int64]bool{}
	described := map[string]commitFile{}
	for _, e := range d.Said {
		if e.Kind == store.CommitKindGhost && e.Path != "" {
			described[e.Path] = commitFile{Path: e.Path, Href: commitEntryHref(view, def, visible, e), Desc: e.Title, Who: e.Person}
		}
	}
	changedPaths := map[string]bool{}
	for _, f := range d.Files {
		c := commitChange{Path: f.Path, Add: signed("+", f.Additions), Del: signed("−", f.Deletions), Bar: diffBar(f.Additions, f.Deletions)}
		if g, ok := described[f.Path]; ok {
			c.Href, c.Desc = g.Href, g.Desc
		}
		changedPaths[f.Path] = true
		v.Changed = append(v.Changed, c)
	}
	v.ChangedTotal, v.Additions, v.Deletions = max(d.FilesTotal, len(d.Files)), signed("+", d.Additions), signed("−", d.Deletions)
	v.ChangedMore = v.ChangedTotal - len(v.Changed)
	for _, e := range d.Said {
		href := commitEntryHref(view, def, visible, e)
		if e.Kind == store.CommitKindGhost {
			if changedPaths[e.Path] {
				continue
			}
			if len(v.Files) < 200 {
				v.Files = append(v.Files, commitFile{Path: e.Path, Href: href, Desc: e.Title, Who: e.Person})
			}
			v.FilesTotal++
			continue
		}
		v.Items = append(v.Items, commitItem(l, e, href, now))
		if e.Kind == store.CommitKindRequest && !reqSeen[e.ID] {
			reqSeen[e.ID] = true
			v.Side.Requests = append(v.Side.Requests, sideRef{N: e.ID, Title: e.Title, Href: href})
		}
	}
	v.Empty = len(d.Said) == 0
	a.renderBrowser(w, r2, "commit", pageData{Title: v.Title, Project: remote, CommitV: v})
}

// signed writes a count of lines with its sign.
func signed(sign string, n int) string { return sign + strconv.Itoa(n) }

// diffBar spreads five blocks over added and removed lines: each side that has
// lines keeps at least one block.
func diffBar(add, del int) []string {
	const n = 5
	if add+del == 0 {
		return nil
	}
	a := (add*n + (add+del)/2) / (add + del)
	if add > 0 && a == 0 {
		a = 1
	}
	if del > 0 && a == n {
		a = n - 1
	}
	bar := make([]string, 0, n)
	for i := 0; i < n; i++ {
		if i < a {
			bar = append(bar, "a")
		} else {
			bar = append(bar, "d")
		}
	}
	return bar
}

// commitItem ist ein Eintrag der Zeitleiste „Was wurde dazu gesagt“.
func commitItem(l lang, e store.CommitEntry, href string, now time.Time) tlItem {
	it := tlItem{Act: "commit." + e.Kind, Who: e.Person, Icon: commitKindIcons[e.Kind], Tone: commitKindTones[e.Kind]}
	if t := parseTime(e.At); !t.IsZero() {
		it.Age = shortAgo(l, now, t)
	}
	switch e.Kind {
	case store.CommitKindRequest, store.CommitKindPain, store.CommitKindIdea:
		kind := map[string]string{store.CommitKindRequest: "request", store.CommitKindPain: "pain", store.CommitKindIdea: "idea"}[e.Kind]
		it.Head = template.HTML(html.EscapeString(l.msg(commitEvKeys[e.Kind])) + " " + string(refPillHTML(l, kind, e.ID, href)))
		it.Body = template.HTML(html.EscapeString(e.Title))
	case store.CommitKindComment:
		// The sentence names what was commented on, as a pill that leads to the
		// comment; the box holds the words of the comment, plain.
		it.Head = template.HTML(html.EscapeString(l.msg("commit.ev.comment")))
		if kind, ok := map[string]string{store.CommentOnRequest: "request", store.CommentOnPain: "pain", store.CommentOnIdea: "idea"}[e.ObjectKind]; ok && e.ObjectID > 0 {
			it.Head += " " + refPillHTML(l, kind, e.ObjectID, href)
		}
		it.Body = template.HTML(html.EscapeString(e.Title))
	default:
		// The words stand plain; the way to the entry is a small link at the right end.
		it.Head = template.HTML(html.EscapeString(l.msg(commitEvKeys[e.Kind])))
		it.Body = template.HTML(html.EscapeString(e.Title))
		if href != "" {
			it.Right = template.HTML(`<a class="gbtn sm quiet" href="` + html.EscapeString(href) + `">` + html.EscapeString(l.msg("commit.ev.open")) + `</a>`)
		}
	}
	return it
}
