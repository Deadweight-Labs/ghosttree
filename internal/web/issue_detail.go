package web

import (
	"context"
	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The page of one issue in the repository (REQ-599): title, status, blocker box
// for open human tasks, the requirements as a checklist, a sidebar with the
// parent, what blocks it, branch or work package and the sessions, and the
// history. It shows what requestView holds; the POST actions are the ones of the
// old request page (/ui/requests/{id}/…) and lead back here.

var (
	prefixedID     = regexp.MustCompile(`\b(?:REQ|PAIN|TASK|IDEA)-(\d+)\b`)
	criterionHuman = regexp.MustCompile(`^AC-\d+\.(\d+)$`)
)

// blockerLine is an open human task the issue waits on.
type blockerLine struct {
	N                            int64
	Kind, Title, Where, Assignee string
	// Href is the page of the task in its repository.
	Href string
}

// issueLink is another issue this one is related to.
type issueLink struct {
	Rel, Label, Title, Href string
	N                       int64
}

// branchLine is the branch of the issue and, when it has one, its work package.
type branchLine struct {
	Name, Package, Progress string
	Percent                 int
	HasPackage              bool
}

// issueDetail is /ui/{org}/{repo}/issues/{n}. A request of another project is as
// unknown here as one that does not exist.
func (a *app) issueDetail(w http.ResponseWriter, r *http.Request, remote, id string) {
	r.SetPathValue("id", id)
	pa := a.access(r)
	q := r.URL.Query()
	q.Set("project", remote)
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = q.Encode()
	view := a.repoViewFor(r2, pa, a.shellBaseFor(r2).who.kind, remote, "issues")
	r2 = r2.WithContext(context.WithValue(r2.Context(), repoCtxKey{}, view))
	v, detail, ok := a.buildRequestView(w, r2)
	if !ok {
		return
	}
	if v.ProjectRemote != remote {
		http.NotFound(w, r)
		return
	}
	a.fillIssue(r2, pa, remote, view.Href+"/issues", view.Href, detail, v)
	a.renderBrowser(w, r2, "issue", pageData{Title: v.Title, Project: remote, IssueV: v})
}

// issueGlobal is the page of an issue that has no repository the viewer can
// open (a request without a project): the same page without the repository
// band, its actions under /ui/requests/{id}.
func (a *app) issueGlobal(w http.ResponseWriter, r *http.Request) {
	v, detail, ok := a.buildRequestView(w, r)
	if !ok {
		return
	}
	a.fillIssue(r, a.access(r), v.ProjectRemote, "/ui/requests", "", detail, v)
	a.renderBrowser(w, r, "issue", pageData{Title: v.Title, Project: v.ProjectRemote, IssueV: v})
}

// fillIssue adds what only the issue page shows. base is the address of the
// list the issue is in ("…/issues"), repo the address of its repository ("" when
// the page has none).
func (a *app) fillIssue(r *http.Request, pa *store.ProjectAccess, remote, base, repo string, detail requestdomain.Detail, v *requestView) {
	l := a.langFor(r)
	req := detail.Request
	v.Base = base
	var current []rowLabel
	for _, lb := range detail.Labels {
		current = append(current, rowLabel{Text: lb.Name, Class: labelClass(lb.Color), Href: base + "?q=" + url.QueryEscape("label:"+lb.Name)})
	}
	v.LabelBox = a.labelBox(r, pa, remote, store.CommentOnRequest, req.ID, current)
	v.Back = v.Base
	v.Panel = r.URL.Query().Get("panel")
	if r.URL.Query().Get("edit") == "1" {
		v.Panel = "edit"
	}
	// The history names things by number only, like everywhere else.
	for i, act := range v.Activity {
		if m := criterionHuman.FindStringSubmatch(act.Text); m != nil && act.Code {
			v.Activity[i].Text = m[1]
		} else {
			v.Activity[i].Text = prefixedID.ReplaceAllString(act.Text, "#$1")
		}
	}
	for _, i := range detail.Ideas {
		v.Ideas = append(v.Ideas, ideaFact{Num: "#" + strconv.FormatInt(i.ID, 10), Title: i.Title, Brief: l.msg("ref.kind.idea"), Href: repo + "/ideas/" + strconv.FormatInt(i.ID, 10)})
	}
	// A commit proof leads to the commit on GitHub when the project lives there.
	if rest, ok := strings.CutPrefix(remote, "github.com/"); ok {
		for i := range v.Criteria {
			for j, e := range v.Criteria[i].Evidence {
				if e.Kind == "commit" && e.Href == "" && hexCommit.MatchString(e.Ref) {
					v.Criteria[i].Evidence[j].Href = "https://github.com/" + rest + "/commit/" + e.Ref
				}
			}
		}
	}
	if tasks, err := a.store.ListHumanTasks(pa, store.TaskFilter{Project: remote, State: humantask.Open, RequestID: req.ID, Limit: 50}); err == nil {
		for _, t := range tasks {
			if t.State == humantask.Open {
				v.Blockers = append(v.Blockers, blockerLine{N: t.ID, Kind: label(l, taskKindKeys, t.Kind), Title: t.Title, Where: t.Location, Assignee: firstNonEmpty(t.Assignee, req.Person), Href: taskHref(t)})
			}
		}
	}
	for _, rel := range detail.Relations {
		if rel.OtherRequestID == 0 || !a.requestReadable(pa, rel.OtherRequestID) {
			continue
		}
		other, err := a.store.RequestByID(rel.OtherRequestID)
		if err != nil {
			continue
		}
		v.Links = append(v.Links, issueLink{Rel: rel.Kind, Label: label(l, relationKeys, rel.Kind), N: other.Request.ID, Title: ellipsize(other.Request.Title, 80),
			Href: v.Base + "/" + strconv.FormatInt(other.Request.ID, 10)})
	}
	if name := req.Scope.Branch; name != "" {
		b := &branchLine{Name: name}
		if d, err := a.store.PackageForBranch(remote, name, nil); err == nil && slices.ContainsFunc(d.Members, func(m requestdomain.PackageMemberView) bool { return m.RequestID == req.ID }) {
			done, total := 0, 0
			view := pa.PackageView(d, false)
			for _, m := range view.Members {
				done += m.Met + m.Waived
				total += m.Open + m.Met + m.Waived
			}
			b.HasPackage, b.Package = true, packageName(l, view.Package)
			if total > 0 {
				b.Percent, b.Progress = done*100/total, strconv.Itoa(done)+"/"+strconv.Itoa(total)
			}
		}
		v.Branch = b
	}
	now := overviewNow().UTC()
	assignees := map[int64]string{}
	for _, b := range v.Blockers {
		assignees[b.N] = b.Assignee
	}
	pkg := ""
	if v.Branch != nil {
		pkg = v.Branch.Package
	}
	ago := func(at string) string { return shortAgo(l, now, parseTime(at)) }
	acts := a.visibleActivity(pa, detail)
	v.Events = issueEvents(l, acts, v.Criteria, assignees, pkg, ago)
	anchors := map[int64]*anchorView{}
	for _, act := range acts {
		anchors[act.ID] = anchorViewOf(pa, remote, act.GitAnchor, act.Via)
	}
	for i := range v.Events {
		v.Events[i].Git = anchors[v.Events[i].ID]
	}
	comments := a.commentTimeline(r, l, pa, store.CommentOnRequest, req.ID, req.Person, v.Base+"/"+strconv.FormatInt(req.ID, 10), editingComment(r), ago)
	v.Timeline = mergeTimeline(timelineItems(l, withoutRepeatedNotes(v.Events, comments)), comments)
	if v.Result == nil {
		v.Result = commentResult(r, l)
	}
	// A requirement leads to the entry that closed it; the entry leads back.
	for _, e := range v.Events {
		if e.Criterion == 0 || e.ID == 0 {
			continue
		}
		for i := range v.Criteria {
			if v.Criteria[i].Number == e.Criterion {
				v.Criteria[i].Event = "event-" + strconv.FormatInt(e.ID, 10)
			}
		}
	}
	if s, err := pa.Reactions(store.ReactOnRequest, req.ID); err == nil {
		v.Reactions = a.reactionsFor(r, l, store.ReactOnRequest, req.ID, s)
	}
	v.Composer = a.issueComposer(r, l, pa, v)
	v.Live = a.liveAgents(pa, v.Work, now, l)
	for i := range v.Work {
		for _, la := range v.Live {
			if la.Live && la.External == v.Work[i].External && v.Work[i].External != "" {
				v.Work[i].LiveNow = true
			}
		}
	}
	if created := parseTime(req.CreatedAt); !created.IsZero() {
		if now.Sub(created) < time.Minute {
			v.Opened = l.msg("issue.opened_now")
		} else {
			v.Opened = l.msg("issue.opened", shortAge(l, now, created))
		}
	}
}

// issueComposer is the field at the foot of the history. Text alone is a
// comment; the status select adds a change to it: close the issue (the proof is
// named in the fields, the words are the comment) or drop it (the words are the
// reason and the comment). A viewer who may not edit the issue, but may comment,
// gets the field with the one mode "Comment"; a closed issue can still be
// commented on.
func (a *app) issueComposer(r *http.Request, l lang, pa *store.ProjectAccess, v *requestView) *composerView {
	if !interactive(r) {
		return nil
	}
	canComment := v.ProjectRemote != "" && pa.Allow(v.ProjectRemote, store.ResComment, store.ActCreate, store.Object{Own: true})
	canClose := v.Open && v.CanEdit
	if !canComment && !canClose {
		return nil
	}
	id := strconv.FormatInt(v.ID, 10)
	cv := &composerView{Action: "/ui/requests/" + id + "/close", Token: v.Token, Name: "reason", Avatar: browserPrincipal(r).Label,
		ModeLabel: l.msg("issue.cx.status"), Placeholder: l.msg("comments.cx.placeholder")}
	if canComment {
		cv.Modes = append(cv.Modes, composerMode{Value: "comment", Label: l.msg("comments.cx.comment"), Submit: l.msg("comments.cx.submit"), Class: "pri",
			Placeholder: l.msg("comments.cx.placeholder"), Selected: true})
	}
	if canClose {
		var kinds []composerOption
		for _, k := range v.Kinds {
			kinds = append(kinds, composerOption{Value: k.Value, Label: k.Label, Selected: k.Selected})
		}
		if v.CanComplete {
			cv.Modes = append(cv.Modes, composerMode{Value: "complete", Label: l.msg("issue.cx.complete"), Submit: l.msg("issue.cx.complete_btn"), SubmitText: l.msg("issue.cx.complete_text"),
				Placeholder: l.msg("issue.cx.complete_ph"), Selected: !canComment})
			cv.Fields = append(cv.Fields,
				composerField{Name: "evidence_kind", Label: l.msg("requests.field_evidence_kind"), Kind: "select", Show: "complete", Options: kinds},
				composerField{Name: "evidence_ref", Label: l.msg("requests.field_evidence"), Kind: "text", Show: "complete", Wide: true})
		}
		cv.Modes = append(cv.Modes, composerMode{Value: "drop", Label: l.msg("issue.cx.drop"), Submit: l.msg("issue.cx.drop_btn"), SubmitText: l.msg("issue.cx.drop_text"), Class: "dangerf",
			Placeholder: l.msg("issue.cx.drop_placeholder"), Selected: !canComment && !v.CanComplete})
	}
	return cv
}

// requestClose is POST /ui/requests/{id}/close, the form of the composer under
// the history: mode "comment" writes a comment, mode "complete" completes with
// the proof named (or the text as the note) and mode "drop" drops with the text
// as the reason. A change of state with text also writes the text as a comment.
func (a *app) requestClose(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(r.Form.Get("reason"))
	switch r.FormValue("mode") {
	case "comment":
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		pa := a.access(r)
		c, err := pa.AddComment(store.CommentOnRequest, id, text, a.commentActor(r))
		if err != nil {
			a.commentFail(w, r, a.objectPage(pa, store.CommentOnRequest, id), err)
			return
		}
		a.stampActor(r, actor.Comment, c.ID)
		http.Redirect(w, r, commentPage(c)+"?comment=added#comment-"+strconv.FormatInt(c.ID, 10), http.StatusSeeOther)
	case "complete":
		if strings.TrimSpace(r.Form.Get("evidence_ref")) == "" {
			r.Form.Set("evidence_ref", r.Form.Get("reason"))
		}
		r.Form.Set("comment", text)
		a.requestComplete(w, r)
	case "drop":
		r.Form.Set("comment", text)
		a.requestDrop(w, r)
	default:
		http.Error(w, "invalid input", http.StatusBadRequest)
	}
}

// alsoComment writes the form's "comment" as a comment on the issue once its
// change of state went through, and names the outcome for the redirect: nothing
// when there was no text or it was written, comment=failed when it was not.
func (a *app) alsoComment(r *http.Request, id int64) url.Values {
	text := strings.TrimSpace(r.FormValue("comment"))
	if text == "" {
		return nil
	}
	c, err := a.access(r).AddComment(store.CommentOnRequest, id, text, a.commentActor(r))
	if err != nil {
		return url.Values{"comment": {"failed"}}
	}
	a.stampActor(r, actor.Comment, c.ID)
	return nil
}
