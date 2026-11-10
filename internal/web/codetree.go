package web

import (
	"html/template"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/prose"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The Code tab (REQ-599, Etappe B): the ghost tree as GitHub shows a file tree.
// /ui/{org}/{repo} is the top of the default branch, …/tree/{branch}/{path…} a
// folder and …/blob/{branch}/{path…} one file with its description. Code is
// never shown, only what ghosttree knows about the path; "Code auf GitHub" leads
// to the code itself.
//
// The tree follows the branch (REQ-597, criterion 8): the default branch shows
// the project level, any other branch the descriptions written on it and on the
// branches it was cut from (ghostsOnBranch). The tree is the union of the paths
// that have a description; a folder without a ghost file of its own is derived
// from the paths below it.

// codeMaxKnow and codeMaxNav keep one page bounded.
const (
	codeMaxKnow = 25
	codeMaxNav  = 60
	codeWorkAge = 14 * 24 * time.Hour
)

type codePath struct {
	Kind   string // tree | blob
	Branch string
	Path   string // "" is the top
}

type codePathKey struct{}

func codePathFrom(r *http.Request) *codePath {
	v, _ := r.Context().Value(codePathKey{}).(*codePath)
	return v
}

// Branch switch -------------------------------------------------------------

// pickDefaultBranch: the server does not know the default branch of the host.
// It takes the first of main, master, dev that the data mentions, else main.
func pickDefaultBranch(known []string) string { return store.PickDefaultBranch(known) }

// seesAllBranches: the viewer's role in this project (not the highest one over
// all projects) is at least member, or access is not enforced.
func (a *app) seesAllBranches(pa *store.ProjectAccess, remote string) bool {
	return pa.SeesAllBranches(remote)
}

// codeBranches lists the branches of the project, the default first. A guest
// gets the default alone: a branch name can give away what a guest may not read.
func (a *app) codeBranches(r *http.Request, pa *store.ProjectAccess, remote string) (names []string, def string) {
	known, _ := a.store.ProjectBranches(remote)
	def = pickDefaultBranch(known)
	if !a.seesAllBranches(pa, remote) {
		return []string{def}, def
	}
	rest := []string{}
	for _, n := range known {
		if n != def {
			rest = append(rest, n)
		}
	}
	sort.Strings(rest)
	return append([]string{def}, rest...), def
}

// ghostsOnBranch reads the descriptions as they stand on a branch: the default
// branch is the project level, another branch adds its own descriptions and
// those of the branches it descends from (as far as the pull requests tell).
func (a *app) ghostsOnBranch(remote, branch, def string) ([]store.GhostFile, error) {
	if branch == "" || branch == def {
		return a.store.GhostFilesUnder(remote, "")
	}
	lineage, err := a.store.BranchLineage(remote, branch)
	if err != nil {
		return nil, err
	}
	return a.store.GhostFilesUnderAt(remote, "", branch, lineage)
}

// matchBranch splits "feat/git-native/internal/store" the way GitHub does: the
// longest prefix that is a known branch is the branch, the rest is the path.
func matchBranch(known []string, segs []string) (branch string, rest []string, ok bool) {
	for n := len(segs); n >= 1; n-- {
		if name := strings.Join(segs[:n], "/"); slices.Contains(known, name) {
			return name, segs[n:], true
		}
	}
	return "", nil, false
}

// cleanPath joins path segments and refuses what is no path of a repository.
func cleanPath(segs []string) (string, bool) {
	for _, s := range segs {
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "\\\x00") {
			return "", false
		}
		for _, r := range s {
			if unicode.IsControl(r) {
				return "", false
			}
		}
	}
	return strings.Join(segs, "/"), true
}

// resolveCodePath reads the segments after tree/ or blob/. It answers 302 or
// 404 itself and returns false then.
func (a *app) resolveCodePath(w http.ResponseWriter, r *http.Request, pa *store.ProjectAccess, remote, kind string, segs []string) (*codePath, bool) {
	for len(segs) > 0 && segs[len(segs)-1] == "" {
		segs = segs[:len(segs)-1]
	}
	if len(segs) == 0 {
		http.Redirect(w, r, repoHref(remote), http.StatusFound)
		return nil, false
	}
	names, _ := a.codeBranches(r, pa, remote)
	branch, rest, ok := matchBranch(names, segs)
	if !ok {
		http.NotFound(w, r)
		return nil, false
	}
	p, ok := cleanPath(rest)
	if !ok {
		http.NotFound(w, r)
		return nil, false
	}
	return &codePath{Kind: kind, Branch: branch, Path: p}, true
}

// codeLinks makes the addresses of one repository page.
type codeLinks struct{ base, def string }

func (l codeLinks) tree(branch, p string) string {
	if p == "" && branch == l.def {
		return l.base
	}
	out := l.base + "/tree/" + pathEscapeAll(branch)
	if p != "" {
		out += "/" + pathEscapeAll(p)
	}
	return out
}

func (l codeLinks) blob(branch, p string) string {
	return l.base + "/blob/" + pathEscapeAll(branch) + "/" + pathEscapeAll(p)
}

// Index of the ghost tree -----------------------------------------------------

type codeIndex struct {
	byPath map[string]store.GhostFile
	paths  []string // sorted, without the top
}

func newCodeIndex(gs []store.GhostFile) *codeIndex {
	ix := &codeIndex{byPath: map[string]store.GhostFile{}}
	for _, g := range gs {
		ix.byPath[g.Path] = g
		if g.Path != "" {
			ix.paths = append(ix.paths, g.Path)
		}
	}
	sort.Strings(ix.paths)
	return ix
}

func (ix *codeIndex) hasChildren(p string) bool {
	prefix := p + "/"
	i := sort.SearchStrings(ix.paths, prefix)
	return i < len(ix.paths) && strings.HasPrefix(ix.paths[i], prefix)
}

func (ix *codeIndex) isDir(p string) bool {
	if p == "" {
		return true
	}
	return ix.hasChildren(p) || ix.byPath[p].Kind == "dir"
}

func (ix *codeIndex) exists(p string) bool {
	if p == "" {
		return true
	}
	_, own := ix.byPath[p]
	return own || ix.hasChildren(p)
}

type codeChild struct {
	Name, Path string
	Dir        bool
	Updated    string
}

// children are the entries one level below dir, folders first, with the time
// of the newest description in their subtree. newest is the most recent
// description below dir that carries a Git commit (for the commit line).
func (ix *codeIndex) children(dir string) (out []codeChild, newest *store.GhostFile) {
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	byName := map[string]*codeChild{}
	for i := sort.SearchStrings(ix.paths, prefix); i < len(ix.paths) && strings.HasPrefix(ix.paths[i], prefix); i++ {
		p := ix.paths[i]
		g := ix.byPath[p]
		name, deeper, _ := strings.Cut(strings.TrimPrefix(p, prefix), "/")
		c := byName[name]
		if c == nil {
			c = &codeChild{Name: name, Path: prefix + name}
			byName[name] = c
		}
		if deeper != "" || g.Kind == "dir" {
			c.Dir = true
		}
		if g.UpdatedAt > c.Updated {
			c.Updated = g.UpdatedAt
		}
		if g.GitAnchor != nil && g.GitAnchor.Commit != "" && (newest == nil || g.UpdatedAt > newest.UpdatedAt) {
			g := g
			newest = &g
		}
	}
	for _, c := range byName {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return out[i].Name < out[j].Name
	})
	return out, newest
}

// Knowledge that belongs to paths ---------------------------------------------

// codeKnow is one knowledge entry with the paths it names: paths the writer
// named, the areas of its origin and what its activation rule triggers on.
type codeKnow struct {
	K     store.Knowledge
	Named []string // exact paths and the fixed beginning of trigger patterns
	Areas []string // directories
}

func globPrefix(pat string) string {
	i := strings.IndexAny(pat, "*?[")
	if i < 0 {
		return pat
	}
	s := pat[:i]
	if j := strings.LastIndex(s, "/"); j >= 0 {
		return s[:j]
	}
	return ""
}

func (a *app) codeKnowledge(pa *store.ProjectAccess, remote string) []codeKnow {
	pa.Filtered()
	found, err := a.knowledgeKeep(a.knowledgeWindowFor(pa, remote), knowledgeMaxPages*100, func(k store.Knowledge) bool {
		return k.Scope.Project == remote && pa.CanSeeKnowledge(k) && pa.CanDeliverKnowledge(k)
	})
	if err != nil {
		return nil
	}
	out := make([]codeKnow, 0, len(found))
	for _, k := range found {
		c := codeKnow{K: k}
		if oc := k.OriginContext; oc != nil {
			for _, p := range oc.Paths {
				if p.Kind == originctx.KindNamed {
					c.Named = append(c.Named, strings.TrimSuffix(p.Path, "/"))
				}
			}
			for _, ar := range oc.Areas {
				if ar.Path != "" && ar.Path != "." {
					c.Areas = append(c.Areas, ar.Path)
				}
			}
		}
		for _, pat := range k.Activation.Paths {
			if g := strings.TrimSuffix(globPrefix(pat), "/"); g != "" {
				c.Named = append(c.Named, g)
			}
		}
		if len(c.Named) > 0 || len(c.Areas) > 0 {
			out = append(out, c)
		}
	}
	return out
}

// under: the entry names p or something below it. That is what the counters of
// a tree row add up, so a folder counts everything of its subtree.
func (c codeKnow) under(p string) bool {
	for _, list := range [][]string{c.Named, c.Areas} {
		for _, x := range list {
			if x == p || strings.HasPrefix(x, p+"/") {
				return true
			}
		}
	}
	return false
}

// aboutFile: the entry names this file (or triggers on it) or lies in an area
// that contains it.
func (c codeKnow) aboutFile(p string) (direct, area bool) {
	if slices.Contains(c.Named, p) {
		direct = true
	}
	if len(c.K.Activation.Paths) > 0 && activation.Matches(c.K.Activation, activation.Context{RepoPath: p}) {
		direct = true
	}
	for _, d := range c.Areas {
		if strings.HasPrefix(p, d+"/") {
			area = true
		}
	}
	return direct, area
}

// Views ------------------------------------------------------------------------

type repoCodeView struct {
	Branch codeBranchView
	Crumbs []codeCrumb // empty at the top
	Last   *codeCommit
	Rows   []codeRow
	Empty  bool
	Readme *codeDesc
	Blob   *codeBlob
	Nav    []codeNavNode
	GitHub string // "Code auf GitHub" (file pages)
	Filter bool
	Folder string // path of the folder shown, "" at the top
	// KnowledgeHref is the action of a repository without descriptions.
	KnowledgeHref string
	Current       string // path of the page, folder or file
}

type codeBranchView struct {
	Name    string
	Options []codeBranchOpt
}

type codeBranchOpt struct {
	Name, Href       string
	Current, Default bool
}

type codeCrumb struct {
	Label, Href string
	Last        bool
}

type codeCommit struct {
	AvKind, Who, Text, SHA, SHAHref, Updated string
	// Commits is the number of commits the server knows, CommitsHref the list.
	Commits     int
	CommitsHref string
}

type codeRow struct {
	Name, Href, Desc, Updated string
	Dir, Missing              bool
	Pitfalls, Knowledge       int
}

type codeNavNode struct {
	Name, Href string
	Depth      int
	Dir, Cur   bool
	More       int // "… n weitere" instead of a link
}

type codeDesc struct {
	Label, Path, By, AvKind, Updated, Area string
	Page                                   bool    // under the table: the readme box
	Cmd                                    cmdLine // what to tell the agent; the page has no editor
	HTML                                   template.HTML
	Empty                                  bool
}

type codeBlob struct {
	Desc    *codeDesc
	History []codeRev
	Know    []codeKnowRow
	KnowN   int
	Work    []codeWorkRow
}

type codeRev struct {
	N                                   int
	AvKind, Who, Note, SHA, SHAHref, At string
	Diff                                []codeDiffLine
}

type codeDiffLine struct {
	Add  bool
	Text string
}

type codeKnowRow struct {
	TypeKey, Title, Href, Updated, Area string
	Type                                string
	Trigger                             bool
}

type codeWorkRow struct {
	AvKind, Who, Machine, Title, Href, At string
}

// Handlers ------------------------------------------------------------------------

// knowTypeKey is the catalog key of the name of a knowledge type.
func knowTypeKey(t string) string {
	switch t {
	case "pitfall":
		return "knowledge.type.pitfall"
	case "decision":
		return "knowledge.type.decision"
	case "plan":
		return "knowledge.type.plan"
	case "instruction":
		return "knowledge.type.instruction"
	}
	return "knowledge.type.note"
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// actorOf names who wrote a description: the harness when an agent did, else
// the person.
func actorOf(person, harness string) (kind, name string) {
	if strings.TrimSpace(harness) != "" {
		kind = harnessKind(harness)
		name, _, _ = strings.Cut(strings.ToLower(strings.TrimSpace(harness)), "-")
		return kind, name
	}
	return avHuman, person
}

func (a *app) repoCode(w http.ResponseWriter, r *http.Request) {
	pa := a.access(r)
	view := repoFrom(r)
	if _, stop := a.gateList(w, r, pa, view.Project, store.ResGhost); stop {
		return
	}
	names, def := a.codeBranches(r, pa, view.Project)
	cp := codePathFrom(r)
	if cp == nil {
		cp = &codePath{Kind: "tree", Branch: def}
	}
	links := codeLinks{base: view.Href, def: def}

	ghosts, err := a.ghostsOnBranch(view.Project, cp.Branch, def)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ghosts = keep(ghosts, 0, pa.CanSeeGhost)
	ix := newCodeIndex(ghosts)
	if !ix.exists(cp.Path) {
		http.NotFound(w, r)
		return
	}
	switch {
	case cp.Kind == "blob" && (cp.Path == "" || ix.isDir(cp.Path)):
		http.Redirect(w, r, links.tree(cp.Branch, cp.Path), http.StatusFound)
		return
	case cp.Kind == "tree" && !ix.isDir(cp.Path):
		http.Redirect(w, r, links.blob(cp.Branch, cp.Path), http.StatusFound)
		return
	}

	v := &repoCodeView{Current: cp.Path, KnowledgeHref: view.Href + "/knowledge"}
	v.Branch = codeBranchView{Name: cp.Branch}
	for _, n := range names {
		o := codeBranchOpt{Name: n, Current: n == cp.Branch, Default: n == def}
		if cp.Kind == "blob" {
			o.Href = links.blob(n, cp.Path)
		} else {
			o.Href = links.tree(n, cp.Path)
		}
		v.Branch.Options = append(v.Branch.Options, o)
	}
	if !slices.ContainsFunc(v.Branch.Options, func(o codeBranchOpt) bool { return o.Current }) {
		v.Branch.Options = append(v.Branch.Options, codeBranchOpt{Name: cp.Branch, Current: true, Href: links.tree(cp.Branch, "")})
	}
	// The crumb: repository, then every folder; the last segment is not a link.
	if cp.Path != "" {
		v.Crumbs = append(v.Crumbs, codeCrumb{Label: view.Name, Href: links.tree(cp.Branch, "")})
		segs := strings.Split(cp.Path, "/")
		for i, s := range segs {
			c := codeCrumb{Label: s, Last: i == len(segs)-1}
			if !c.Last {
				c.Href = links.tree(cp.Branch, strings.Join(segs[:i+1], "/"))
			}
			v.Crumbs = append(v.Crumbs, c)
		}
	}

	title := view.Org + "/" + view.Name
	if cp.Path != "" {
		title = cp.Path + " · " + title
	}
	if cp.Kind == "blob" {
		a.codeBlobPage(r, pa, view, v, ix, links, cp)
		v.Filter = true
		a.renderBrowser(w, r, "repoblob", pageData{Title: title, CodeV: v})
		return
	}
	a.codeFolderPage(r, pa, view, v, ix, links, cp)
	// "Open on GitHub" leads to the same place there: the repository, or the folder.
	if view.External != "" {
		v.GitHub = view.External
		if cp.Path != "" {
			v.GitHub += "/tree/" + pathEscapeAll(cp.Branch) + "/" + pathEscapeAll(cp.Path)
		} else if cp.Branch != "" && cp.Branch != def {
			v.GitHub += "/tree/" + pathEscapeAll(cp.Branch)
		}
	}
	a.renderBrowser(w, r, "repocode", pageData{Title: title, CodeV: v})
}

func (a *app) codeFolderPage(r *http.Request, pa *store.ProjectAccess, view *repoView, v *repoCodeView, ix *codeIndex, links codeLinks, cp *codePath) {
	l := a.langFor(r)
	kn := a.codeKnowledge(pa, view.Project)
	children, newest := ix.children(cp.Path)
	for _, c := range children {
		row := codeRow{Name: c.Name, Dir: c.Dir, Updated: c.Updated}
		if c.Dir {
			row.Name += "/"
			row.Href = links.tree(cp.Branch, c.Path)
		} else {
			row.Href = links.blob(cp.Branch, c.Path)
		}
		if g, ok := ix.byPath[c.Path]; ok && strings.TrimSpace(g.Description) != "" {
			row.Desc = plainLine(g.Description, 160)
		}
		row.Missing = row.Desc == ""
		for _, k := range kn {
			if k.under(c.Path) {
				row.Knowledge++
				if k.K.Type == "pitfall" {
					row.Pitfalls++
				}
			}
		}
		v.Rows = append(v.Rows, row)
	}
	v.Empty = len(v.Rows) == 0
	v.Filter = len(v.Rows) > 0
	v.Folder = cp.Path
	if newest != nil {
		kind, who := actorOf(newest.Person, newest.Harness)
		c := &codeCommit{AvKind: kind, Who: who, Text: newest.Path, SHA: shortSHA(newest.GitAnchor.Commit), Updated: newest.UpdatedAt}
		if cp.Path != "" {
			c.Text = strings.TrimPrefix(newest.Path, cp.Path+"/")
		}
		c.SHAHref = view.Href + "/commits/" + newest.GitAnchor.Commit
		v.Last = c
	}
	// How many commits ghosttree knows for the project: the number beside the
	// last commit, a link into the commit list.
	if list, err := pa.Commits(view.Project, "", 1, 0); err == nil && list.Total > 0 {
		if v.Last == nil {
			v.Last = &codeCommit{}
		}
		v.Last.Commits, v.Last.CommitsHref = list.Total, view.Href+"/commits"
	}
	v.Readme = a.codeDescription(l, ix, cp.Path)
	v.Readme.Page = true
	if cp.Path == "" {
		v.Readme.Label = view.Name + "/"
	}
}

// codeDescription is the box with the description of a path. Without one it
// stays a single action: write it.
func (a *app) codeDescription(_ lang, ix *codeIndex, p string) *codeDesc {
	d := &codeDesc{Path: p, Label: p, Cmd: cmdLine{Icon: "file", Text: `context_remember action=describe_file path="` + p + `"`}}
	switch {
	case p == "":
		d.Label = "/"
		d.Cmd.Text = `context_remember action=describe_file path="."`
	case ix.isDir(p):
		d.Label = p + "/"
	}
	g, ok := ix.byPath[p]
	if !ok || strings.TrimSpace(g.Description) == "" {
		d.Empty = true
		return d
	}
	d.HTML = renderDescription(g.Description)
	d.AvKind, d.By = actorOf(g.Person, g.Harness)
	d.Updated = g.UpdatedAt
	if g.OriginContext != nil {
		d.Area = g.OriginContext.Main()
	}
	return d
}

func (a *app) codeBlobPage(r *http.Request, pa *store.ProjectAccess, view *repoView, v *repoCodeView, ix *codeIndex, links codeLinks, cp *codePath) {
	l := a.langFor(r)
	b := &codeBlob{Desc: a.codeDescription(l, ix, cp.Path)}
	v.Blob = b
	v.Nav = codeNav(ix, links, cp)
	if view.External != "" {
		v.GitHub = view.External + "/blob/" + pathEscapeAll(cp.Branch) + "/" + pathEscapeAll(cp.Path)
	}

	// The history: the current text and every one it replaced, newest first.
	cur := ix.byPath[cp.Path]
	past, _ := a.store.GhostFileHistory(view.Project, cp.Path, 20)
	type rev struct {
		person, harness, desc, at string
		sha                       string
	}
	curSHA := ""
	if cur.GitAnchor != nil {
		curSHA = cur.GitAnchor.Commit
	}
	revs := []rev{{cur.Person, cur.Harness, cur.Description, cur.UpdatedAt, curSHA}}
	if cur.Branch != "" {
		// A description of the branch has no history of its own; the text of the
		// project level is what it replaced there.
		if base, err := a.store.GhostFileByPath(view.Project, cp.Path); err == nil && pa.CanSeeGhost(base) {
			baseSHA := ""
			if base.GitAnchor != nil {
				baseSHA = base.GitAnchor.Commit
			}
			revs = append(revs, rev{base.Person, base.Harness, base.Description, base.UpdatedAt, baseSHA})
		}
	}
	for _, p := range past {
		// Every replaced text is held to the read rule of a description: a viewer
		// who may only read his own does not get the texts of others here.
		if !pa.CanSeeGhost(store.GhostFile{Project: p.Project, Path: p.Path, Person: p.Person}) {
			continue
		}
		c := ""
		if p.GitAnchor != nil {
			c = p.GitAnchor.Commit
		}
		revs = append(revs, rev{p.Person, p.Harness, p.Description, p.DescribedAt, c})
	}
	if len(revs) > 1 {
		for i, rv := range revs {
			c := codeRev{N: len(revs) - i, At: rv.at, SHA: shortSHA(rv.sha), Note: plainLine(rv.desc, 110)}
			c.AvKind, c.Who = actorOf(rv.person, rv.harness)
			if rv.sha != "" {
				c.SHAHref = view.Href + "/commits/" + rv.sha
			}
			if i+1 < len(revs) {
				for _, ch := range prose.Diff(revs[i+1].desc, rv.desc) {
					switch ch.Op {
					case prose.Added:
						c.Diff = append(c.Diff, codeDiffLine{Add: true, Text: ch.Text})
					case prose.Removed:
						c.Diff = append(c.Diff, codeDiffLine{Text: ch.Text})
					}
				}
			}
			b.History = append(b.History, c)
		}
	}

	// Knowledge about the path: named or triggered first, then what was learned in
	// an area that contains it.
	var direct, areas []codeKnowRow
	for _, k := range a.codeKnowledge(pa, view.Project) {
		d, ar := k.aboutFile(cp.Path)
		if !d && !ar {
			continue
		}
		row := codeKnowRow{Type: k.K.Type, TypeKey: knowTypeKey(k.K.Type), Title: k.K.Title, Href: knowledgeHref(k.K.Scope.Project, k.K.ID),
			Updated: k.K.CreatedAt, Trigger: len(k.K.Activation.Paths) > 0 && d}
		if !d {
			row.Area = k.Areas[0]
			for _, x := range k.Areas {
				if strings.HasPrefix(cp.Path, x+"/") {
					row.Area = x
				}
			}
			areas = append(areas, row)
			continue
		}
		direct = append(direct, row)
	}
	all := append(direct, areas...)
	b.KnowN = len(all)
	if len(all) > codeMaxKnow {
		all = all[:codeMaxKnow]
	}
	b.Know = all
	b.Work = a.codeWork(pa, view.Project, cp.Path)
}

// codeNav is the tree at the left of a file page: the top level, with every
// folder on the way to the file open.
func codeNav(ix *codeIndex, links codeLinks, cp *codePath) []codeNavNode {
	open := map[string]bool{"": true}
	segs := strings.Split(cp.Path, "/")
	for i := 1; i < len(segs); i++ {
		open[strings.Join(segs[:i], "/")] = true
	}
	var out []codeNavNode
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		kids, _ := ix.children(dir)
		for i, c := range kids {
			if i >= codeMaxNav {
				out = append(out, codeNavNode{Depth: depth, More: len(kids) - codeMaxNav})
				break
			}
			n := codeNavNode{Name: c.Name, Depth: min(depth, 5), Dir: c.Dir, Cur: c.Path == cp.Path}
			if c.Dir {
				n.Name += "/"
				n.Href = links.tree(cp.Branch, c.Path)
			} else {
				n.Href = links.blob(cp.Branch, c.Path)
			}
			out = append(out, n)
			if c.Dir && open[c.Path] {
				walk(c.Path, depth+1)
			}
		}
	}
	walk("", 0)
	return out
}

// codeWork lists who worked on the path lately: the sessions behind the
// activity, as far as the viewer may see them.
func (a *app) codeWork(pa *store.ProjectAccess, remote, p string) []codeWorkRow {
	since := overviewNow().UTC().Add(-codeWorkAge).Format(time.RFC3339)
	acts, err := a.store.PathActivitySince(remote, p, since, "")
	if err != nil || len(acts) == 0 {
		return nil
	}
	sessions, err := a.store.ListSessionsVisible(scope.Axes{Project: remote}, 300, "", pa.CanSeeSessionMeta, store.SessionPrefilter{})
	if err != nil {
		return nil
	}
	byExt := map[string]store.Session{}
	for _, s := range sessions {
		byExt[s.ExternalID] = s
	}
	var out []codeWorkRow
	seen := map[string]bool{}
	for _, act := range acts {
		s, ok := byExt[act.SessionExternalID]
		if !ok || seen[s.ExternalID] {
			continue
		}
		seen[s.ExternalID] = true
		view := pa.MetaView(s)
		kind, who := actorOf("", s.Harness)
		row := codeWorkRow{AvKind: kind, Who: who, Machine: view.Scope.Machine, Title: view.Title, At: act.At}
		if pa.HidesMachines(remote) {
			row.Machine = ""
		}
		if view.PublicID != "" && pa.CanSeeTranscript(s) {
			row.Href = sessionPageHref(remote, view.PublicID)
		}
		out = append(out, row)
		if len(out) >= 10 {
			break
		}
	}
	return out
}
