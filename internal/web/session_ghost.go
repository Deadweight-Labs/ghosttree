package web

import (
	"fmt"
	"html"
	"html/template"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The ghosttree view of a session page (UI review P13.1, mock git-native-v2
// session.html): not the transcript but what the session left behind, as the same
// timeline the issue page uses, newest first, with the agent as the maker of every
// entry. The data comes from Store.SessionResults and Store.SessionFiles; every
// entry is checked against what the viewer may see before it is written.

// filesShown is how many files an entry names before the rest folds.
const filesShown = 10

// madeLine is a line of the block "Entstanden" in the side bar.
type madeLine struct {
	Icon, Text string
}

// sessionGhost is what the ghosttree view and the side bar carry beyond the head.
type sessionGhost struct {
	Items []tlItem
	// Started is the start of the session as a time element's value and text.
	Started, StartedAt string
	Areas, Issues      []template.HTML
	Made               []madeLine
}

// sessionAgentLabel is how the page names the agent of a session: "Robins Claude", and
// without a known owner (a guest does not see it) just the harness.
func sessionAgentLabel(l lang, harness, owner string) string {
	fam := actor.Family(harness)
	if owner == "" {
		return fam
	}
	return possessive(l, owner) + " " + fam
}

// ghostView builds the entries and the side bar for one session. sess is the
// session as stored (for the queries), view the same as the viewer may see it;
// lines asks for the line counts of the files, which cost a read of the transcript
// and only the ghosttree view shows.
func (a *app) ghostView(l lang, pa *store.ProjectAccess, sess, view store.Session, links []store.SessionLink, lines bool) sessionGhost {
	var g sessionGhost
	g.Started, g.StartedAt = sess.StartedAt, clock(sess.StartedAt)

	who := sessionAgentLabel(l, sess.Harness, view.Owner)
	kind := harnessKind(sess.Harness)
	owner := view.Owner
	entry := func(at, icon, tone string, head, body template.HTML) tlItem {
		it := tlItem{Who: who, Kind: kind, Owner: owner, Icon: icon, Tone: tone, Head: head, Body: body, Age: clock(at), Stamp: at, at: parseStamp(at)}
		return it
	}
	sentence := func(key string, args ...any) template.HTML {
		return template.HTML(fmt.Sprintf(html.EscapeString(l.msg(key)), args...))
	}

	res, err := a.store.SessionResults(sess)
	if err != nil {
		res = store.SessionResults{}
	}

	issues := map[int64]bool{}
	for _, lk := range links {
		if lk.Kind == "request" {
			issues[lk.ID] = true
		}
	}
	commits := map[string]bool{}
	var criteria, knowledge int
	var items []tlItem

	for _, c := range res.Criteria {
		if !pa.CanSeeProject(c.Project, store.ResRequest) {
			continue
		}
		issues[c.RequestID] = true
		criteria++
		reqHref := requestURL(c.RequestID)
		crit := template.HTML(fmt.Sprintf(`<a href="%s#criterion-%d">%s</a>`, html.EscapeString(reqHref), c.Number, html.EscapeString(l.msg("issue.requirement_n", c.Number))))
		req := longPill(l, "request", c.RequestID, reqHref)
		it := entry(c.At, "check", "ok", sentence("sessions.ev.criterion_met", string(crit), string(req)), "")
		if c.State == "waived" {
			it = entry(c.At, "drop", "", sentence("sessions.ev.criterion_waived", string(crit), string(req)), "")
		}
		it.Body = template.HTML(html.EscapeString(ellipsize(c.Text, 200)))
		if c.Commit != "" {
			commits[c.Commit] = true
			href := ""
			if repoPathOK(c.Project) {
				href = repoHref(c.Project) + "/commits/" + c.Commit
			}
			it.Right = shaPill(l, shortSHA(c.Commit), href)
		}
		items = append(items, it)
	}

	for _, k := range res.Knowledge {
		if !pa.CanSeeKnowledge(k.Knowledge) {
			continue
		}
		knowledge++
		tone := "out"
		switch k.Type {
		case "pitfall":
			tone = "warn"
		case "decision":
			tone = "don"
		}
		href := knowledgeHref(k.Scope.Project, k.ID)
		body := template.HTML(`<span class="lb ` + tone + `">` + html.EscapeString(typeLabel(l, k.Type)) + `</span><a class="t" href="` + html.EscapeString(href) + `">` + html.EscapeString(ellipsize(k.Title, 140)) + `</a>`)
		items = append(items, entry(k.At, "book", "warn", template.HTML(html.EscapeString(l.msg("sessions.ev.knowledge"))), body))
	}

	cache := map[int64]bool{}
	var tasks int
	for _, t := range res.Tasks {
		task := humantask.Task{ID: t.ID, Project: t.Project, RequestID: t.RequestID, AssigneeAccount: t.AssigneeAccount, CreatedByAccount: t.CreatedByAccount}
		if !pa.CanSeeTask(task) || !a.taskRequestVisible(pa, t.Project, t.RequestID, t.AssigneeAccount, cache) {
			continue
		}
		tasks++
		link := string(longPill(l, "task", t.ID, taskHref(task)))
		var head template.HTML
		if t.Assignee != "" {
			head = sentence("issue.ev.task_created_for", html.EscapeString(t.Assignee), link)
		} else {
			head = sentence("issue.ev.task_created", link)
		}
		items = append(items, entry(t.At, "pin", "acc", head, ""))
	}

	var questions int
	for _, q := range res.Questions {
		remote, ok := strings.CutPrefix(q.RoomKey, "project:")
		if !ok || !pa.CanSeeProject(remote, store.ResRoom) {
			continue
		}
		questions++
		room := `<a href="` + html.EscapeString(coordRoomURL(q.RoomKey, "", 0)) + `">#` + html.EscapeString(coordShortRoomName(store.RoomProject, remote)) + `</a>`
		items = append(items, entry(q.At, "chat", "", sentence("sessions.ev.question", room), template.HTML(html.EscapeString(ellipsize(q.Body, 240)))))
	}

	for _, r := range res.Requests {
		if !pa.CanSeeProject(r.Project, store.ResRequest) {
			continue
		}
		issues[r.ID] = true
		items = append(items, entry(r.At, "issue", "", sentence("sessions.ev.request", string(longPill(l, "request", r.ID, requestURL(r.ID)))), ""))
	}

	var files []store.SessionFile
	if view.ExternalID != "" {
		files, _ = a.store.SessionFiles(sess, lines)
	}
	if len(files) > 0 {
		items = append(items, a.filesEntry(l, entry, files, sess))
	}

	items = append(items, entry(sess.StartedAt, "play", "acc", template.HTML(html.EscapeString(l.msg("sessions.ev.started"))), ""))
	sort.SliceStable(items, func(i, j int) bool { return items[i].at.After(items[j].at) })
	g.Items = items

	if view.ExternalID != "" {
		for _, p := range a.store.SessionAreas([]store.Session{sess}, 6)[sess.ID] {
			g.Areas = append(g.Areas, kindPill(l, "folder", p+"/", "", pillCard{Title: p}))
		}
	}
	ids := make([]int64, 0, len(issues))
	for id := range issues {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		g.Issues = append(g.Issues, numberedPill(l, "request", id, requestURL(id), ""))
	}

	add := func(icon, key string, n int) {
		if n > 0 {
			g.Made = append(g.Made, madeLine{Icon: icon, Text: l.msg(key, n)})
		}
	}
	add("book", "sessions.made.knowledge", knowledge)
	add("check", "sessions.made.criteria", criteria)
	add("commit", "sessions.made.commits", len(commits))
	add("file", "sessions.made.files", len(files))
	add("pin", "sessions.made.tasks", tasks)
	add("chat", "sessions.made.questions", questions)
	return g
}

// filesEntry is the one entry "touched N files" with the files as chips and what
// each gained and lost in lines, where the transcript still shows it.
func (a *app) filesEntry(l lang, entry func(at, icon, tone string, head, body template.HTML) tlItem, files []store.SessionFile, sess store.Session) tlItem {
	links := codeLinks{}
	if repoPathOK(sess.Scope.Project) && sess.Scope.Branch != "" {
		links = codeLinks{base: repoHref(sess.Scope.Project), def: sess.Scope.Branch}
	}
	row := func(f store.SessionFile) string {
		var b strings.Builder
		b.WriteString(`<span class="fl">`)
		if links.base != "" {
			b.WriteString(`<a class="chip" href="` + html.EscapeString(links.blob(sess.Scope.Branch, f.Path)) + `">` + html.EscapeString(f.Path) + `</a>`)
		} else {
			b.WriteString(`<span class="chip">` + html.EscapeString(f.Path) + `</span>`)
		}
		if f.HasStat {
			b.WriteString(`<span class="diffstat"><span class="a">+` + strconv.Itoa(f.Add) + `</span><span class="m">−` + strconv.Itoa(f.Del) + `</span></span>`)
		}
		b.WriteString(`</span>`)
		return b.String()
	}
	var b strings.Builder
	b.WriteString(`<div class="files">`)
	for i, f := range files {
		if i == filesShown {
			b.WriteString(`</div><details class="more-files"><summary>` + html.EscapeString(l.msg("sessions.ev.more_files", len(files)-filesShown)) + `</summary><div class="files">`)
		}
		b.WriteString(row(f))
	}
	b.WriteString(`</div>`)
	if len(files) > filesShown {
		b.WriteString(`</details>`)
	}
	return entry(files[0].At, "file", "", template.HTML(html.EscapeString(l.msg("sessions.ev.files", len(files)))), template.HTML(b.String()))
}

// taskRequestVisible is the rule canSeeTaskVia keeps in the store: a member does
// not get the title of a task whose issue is hidden from them; a task assigned to
// the viewer stays visible.
func (a *app) taskRequestVisible(pa *store.ProjectAccess, project string, requestID, assignee int64, cache map[int64]bool) bool {
	if v := pa.Viewer(); v.Account != 0 && v.Account == assignee {
		return true
	}
	if ok, seen := cache[requestID]; seen {
		return ok
	}
	ok := pa.CanSeeProject(project, store.ResRequest)
	cache[requestID] = ok
	return ok
}

// viewParam names the two views of the page: the ghosttree view and the raw log.
const (
	viewGhost = "ghosttree"
	viewRaw   = "raw"
)

// rawWanted says whether the page opens as the raw log. A link that points into
// the log (search hit, position, expanded, thinking, kind) always does; without
// any of it the page opens as the ghosttree view. "view" decides when it is set.
func rawWanted(qv url.Values) bool {
	switch qv.Get("view") {
	case viewRaw:
		return true
	case viewGhost:
		return false
	}
	return qv.Get("q") != "" || qv.Get("at") != "" || qv.Get("from") != "" || qv.Get("open") == "1" ||
		qv.Get("thinking") == "1" || qv.Get("tools") == "0" || qv.Get("kind") != "" || qv.Get("prompts") != ""
}
