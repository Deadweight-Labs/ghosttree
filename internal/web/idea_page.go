package web

import (
	"context"
	"errors"
	"fmt"
	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The page of one idea in the repository (REQ-599): title, state, the wish as the
// first entry, what the workshop made of it as entries (versions of the text,
// summary, concept, decisions), a column of facts and a field at the foot to
// dismiss or reopen it or to comment on it. Reactions sit under the wish and
// under each comment. Accepting an idea happens when an issue comes out of it, not here.

type ideaFact struct{ Num, Title, Brief, Href string }

type ideaPageView struct {
	ID                 int64
	Title, Person, Age string
	Actor              actorView
	// First is the head of the bubble that opens the timeline.
	First                   firstHead
	State, StateLabel, Tone string
	Blocks                  []bodyBlock
	Reason                  string
	Items                   []tlItem
	Composer                *composerView
	Labels                  []rowLabel
	LabelBox                *labelBoxView
	Pains, Issues           []ideaFact
	People                  []string
	Base, Token, Viewer     string
	Result                  *requestResult
	Phase                   string
	// Git is the anchor the wish was written at.
	Git *anchorView
	// Reactions is the row under the wish.
	Reactions *reactionsView
	// Workshop is the idea workshop (workshop.go); nil until someone worked the idea out.
	Workshop *workshopView
	// Images are the pictures on the idea itself (and on answers); CanAttach shows
	// the form that adds one.
	Images    []attachImage
	CanAttach bool
	HasImages bool
}

var ideaStateTone = map[string]string{idea.Open: "p-ok", idea.Accepted: "p-done", idea.Dismissed: "p-mute"}

// ideaInRepo is /ui/{org}/{repo}/ideas/{id}. An idea of another project is as
// unknown here as one that does not exist.
func (a *app) ideaInRepo(w http.ResponseWriter, r *http.Request, remote, id string) {
	l := a.langFor(r)
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	pa := a.access(r)
	q := r.URL.Query()
	q.Set("project", remote)
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = q.Encode()
	ws, err := pa.IdeaWorkshop(n)
	if err != nil || ws.Idea.Project != remote {
		http.NotFound(w, r)
		return
	}
	i := ws.Idea
	view := a.repoViewFor(r2, pa, a.shellBaseFor(r2).who.kind, remote, "issues")
	r2 = r2.WithContext(context.WithValue(r2.Context(), repoCtxKey{}, view))
	now := overviewNow().UTC()
	age := func(at string) string { return shortAgo(l, now, parseTime(at)) }
	v := &ideaPageView{ID: i.ID, Title: i.Title, Person: i.Person, Age: shortAgo(l, now, parseTime(i.CreatedAt)), State: i.State, StateLabel: label(l, ideaStateKeys, i.State),
		Tone: ideaStateTone[i.State], Blocks: bodyBlocks(i.Body, 0), Reason: i.Reason, Base: view.Href, Token: csrfOf(r), Viewer: browserPrincipal(r).Label,
		Git: anchorViewOf(pa, remote, i.GitAnchor, originVia(i.OriginContext))}
	v.Actor = a.actorViewOf(l, pa, actor.Idea, i.ID, i.Person, true)
	for _, lb := range i.Labels {
		v.Labels = append(v.Labels, rowLabel{Text: lb.Name, Class: labelClass(lb.Color), Href: view.Href + "/issues?q=" + url.QueryEscape("label:"+lb.Name)})
	}
	v.LabelBox = a.labelBox(r, pa, remote, store.CommentOnIdea, i.ID, v.Labels)
	for _, p := range i.Pains {
		v.Pains = append(v.Pains, ideaFact{Num: "#" + strconv.FormatInt(p.ID, 10), Title: p.Title, Brief: l.msg("ref.kind.pain"), Href: view.Href + "/pains/" + strconv.FormatInt(p.ID, 10)})
	}
	for _, rid := range i.Requests {
		if !a.requestReadable(pa, rid) {
			continue
		}
		f := ideaFact{Num: "#" + strconv.FormatInt(rid, 10), Href: view.Href + "/issues/" + strconv.FormatInt(rid, 10), Brief: l.msg("ref.kind.issue")}
		if d, err := a.store.RequestByID(rid); err == nil {
			f.Title = d.Request.Title
		}
		v.Issues = append(v.Issues, f)
	}
	v.First = firstOf(l, v.Actor, firstNonEmpty(v.Actor.Label, v.Person), l.msg("ideas.wished"), v.Age, l.msg("issue.author"), "")
	v.Items = mergeTimeline(a.ideaItems(l, ws, age), a.commentTimeline(r, l, pa, store.CommentOnIdea, i.ID, i.Person, view.Href+"/ideas/"+strconv.FormatInt(i.ID, 10), editingComment(r), age))
	if s, err := pa.Reactions(store.ReactOnIdea, i.ID); err == nil {
		v.Reactions = a.reactionsFor(r, l, store.ReactOnIdea, i.ID, s)
	}
	a.attachWorkshop(l, r, pa, ws, v)
	v.Images = attachImages(l, ws.Attachments, func(m idea.AttachmentMeta) bool { return m.Owner != idea.OwnerVariant })
	if ph := idea.EffectivePhase(i.State, i.Phase); interactive(r) && ph != idea.PhaseDismissed && ph != idea.PhaseDone && pa.CheckIdea(i, store.ActWork) == nil {
		v.CanAttach = true
	}
	for _, m := range ws.Attachments {
		v.HasImages = v.HasImages || m.Owner == idea.OwnerVariant
	}
	v.HasImages = v.HasImages || len(v.Images) > 0
	seen := map[string]bool{}
	for _, p := range append([]string{i.Person}, peopleOf(ws)...) {
		if p != "" && !seen[p] {
			seen[p] = true
			v.People = append(v.People, p)
		}
	}
	if interactive(r) {
		v.Composer = a.ideaComposer(l, v, i.State, pa.CheckIdea(i, store.ActEdit) == nil, pa.Allow(remote, store.ResComment, store.ActCreate, store.Object{Own: true}))
	}
	switch {
	case commentResult(r, l) != nil:
		v.Result = commentResult(r, l)
	case r.URL.Query().Get("fail") != "":
		v.Result = &requestResult{Text: l.msg(ideaFailKey(r.URL.Query().Get("fail"))), Kind: "failed", Fail: true}
	case r.URL.Query().Get("done") == "image":
		v.Result = &requestResult{Text: l.msg("ideas.result.image"), Kind: "done"}
	case r.URL.Query().Get("done") == "from_pain":
		v.Result = &requestResult{Text: l.msg("ideas.result.from_pain"), Kind: "done"}
	case r.URL.Query().Get("done") == "state":
		v.Result = &requestResult{Text: l.msg("ideas.result.state"), Kind: "done"}
	}
	a.renderBrowser(w, r2, "idea", pageData{Title: i.Title, Project: remote, IdeaV: v})
}

func peopleOf(ws store.IdeaWorkshop) []string {
	var out []string
	for _, t := range ws.Raw {
		out = append(out, t.Person)
	}
	for _, t := range ws.ConceptRevs {
		out = append(out, t.Person)
	}
	for _, d := range ws.Decisions {
		out = append(out, d.Person)
	}
	return out
}

// ideaItems are the workshop's work as entries of the timeline, oldest first.
func (a *app) ideaItems(l lang, ws store.IdeaWorkshop, age func(string) string) []tlItem {
	all := a.workshopDated(l, ws, age)
	sentence := func(key string, args ...any) template.HTML {
		return template.HTML(fmt.Sprintf(html.EscapeString(l.msg(key)), args...))
	}
	for _, t := range ws.Raw {
		if t.Revision <= 1 {
			continue
		}
		all = append(all, datedItem{t.CreatedAt, tlItem{ID: "text-raw-" + strconv.Itoa(t.Revision), Act: "idea.raw", Who: t.Person, Icon: "pencil", Head: sentence("ideas.ev.raw", t.Revision), Age: age(t.CreatedAt), Body: mdText(t.Message)}})
	}
	if s := ws.Summary; s != nil {
		all = append(all, datedItem{s.CreatedAt, tlItem{ID: "text-summary", Act: "idea.summary", Who: s.Person, Icon: "list", Head: sentence("ideas.ev.summary"), Age: age(s.CreatedAt), Body: mdText(s.Message)}})
	}
	for _, t := range ws.ConceptRevs {
		all = append(all, datedItem{t.CreatedAt, tlItem{ID: "text-concept-" + strconv.Itoa(t.Revision), Act: "idea.concept", Who: t.Person, Icon: "book", Head: sentence("ideas.ev.concept", t.Revision), Age: age(t.CreatedAt), Body: mdText(t.Message)}})
	}
	for _, d := range ws.Decisions {
		all = append(all, datedItem{d.CreatedAt, tlItem{ID: "decision-" + strconv.FormatInt(d.ID, 10), Act: "idea.decision", Who: d.Person, Icon: "chat", Head: sentence("ideas.ev.decision") + " " + longPill(l, "decision", d.ID, "#workshop"), Age: age(d.CreatedAt)}})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].at < all[j].at })
	items := make([]tlItem, len(all))
	for i, d := range all {
		items[i] = d.item
	}
	return items
}

// ideaComposer is the field at the foot: text alone is a comment; the status
// select adds to it dismissing an open idea (the words are the reason and the
// comment) or reopening a dismissed one. An accepted idea has nothing to change
// here, only to comment on. edit says the viewer may change the idea, comment
// that they may write comments.
func (a *app) ideaComposer(l lang, v *ideaPageView, from string, edit, comment bool) *composerView {
	cv := &composerView{Action: "/ui/ideas/" + strconv.FormatInt(v.ID, 10) + "/state", Token: v.Token, Name: "reason", Avatar: v.Viewer, ModeLabel: l.msg("issue.cx.status"),
		Placeholder: l.msg("ideas.cx.comment_ph")}
	if comment {
		cv.Modes = append(cv.Modes, composerMode{Value: "comment", Label: l.msg("comments.cx.comment"), Submit: l.msg("comments.cx.submit"), Class: "pri", Placeholder: l.msg("ideas.cx.comment_ph"), Selected: true})
	}
	if edit {
		switch from {
		case idea.Open:
			cv.Modes = append(cv.Modes, composerMode{Value: idea.Dismissed, Label: l.msg("ideas.cx.dismiss"), Submit: l.msg("ideas.cx.dismiss"), SubmitText: l.msg("ideas.cx.dismiss_text"), Class: "dangerf",
				Placeholder: l.msg("ideas.cx.why"), Selected: !comment})
		case idea.Dismissed:
			cv.Modes = append(cv.Modes, composerMode{Value: idea.Open, Label: l.msg("ideas.cx.reopen"), Submit: l.msg("ideas.cx.reopen"), SubmitText: l.msg("ideas.cx.reopen_text"), Class: "pri",
				Placeholder: l.msg("ideas.cx.note"), Selected: !comment})
		}
	}
	if len(cv.Modes) == 0 {
		return nil
	}
	return cv
}

// ideaState is POST /ui/ideas/{id}/state: dismiss with a reason or reopen. The
// rules (reason needed, which moves are allowed) are the store's.
func (a *app) ideaState(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	to := r.FormValue("mode")
	if !slices.Contains([]string{idea.Dismissed, idea.Open, "comment"}, to) {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	pa := a.access(r)
	if v := r.FormValue("reason"); tooLong(v, maxFieldChars) && to != "comment" {
		if view, verr := pa.IdeaView(id); verr == nil {
			a.tooLongForm(w, r, repoHref(view.Project)+"/ideas/"+strconv.FormatInt(id, 10), "reason", v, maxFieldChars)
			return
		}
	}
	if to == "comment" {
		c, err := pa.AddComment(store.CommentOnIdea, id, r.FormValue("reason"), a.commentActor(r))
		if err != nil {
			a.commentFail(w, r, a.objectPage(pa, store.CommentOnIdea, id), err)
			return
		}
		a.stampActor(r, actor.Comment, c.ID)
		http.Redirect(w, r, commentPage(c)+"?comment=added#comment-"+strconv.FormatInt(c.ID, 10), http.StatusSeeOther)
		return
	}
	i, err := pa.SetIdeaState(id, to, r.FormValue("reason"))
	if err != nil {
		if errors.Is(err, store.ErrAccessForbidden) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if code := requestdomain.Code(err); code != "" {
			if ideaView, verr := pa.IdeaView(id); verr == nil {
				a.failFormTo(w, r, repoHref(ideaView.Project)+"/ideas/"+strconv.FormatInt(id, 10), http.StatusUnprocessableEntity, a.langFor(r).msg(ideaFailKey(code)), "reason")
				return
			}
		}
		http.NotFound(w, r)
		return
	}
	q := "?done=state"
	if text := strings.TrimSpace(r.FormValue("reason")); text != "" {
		if c, err := pa.AddComment(store.CommentOnIdea, id, text, a.commentActor(r)); err != nil {
			q = "?comment=failed"
		} else {
			a.stampActor(r, actor.Comment, c.ID)
		}
	}
	http.Redirect(w, r, repoHref(i.Project)+"/ideas/"+strconv.FormatInt(id, 10)+q, http.StatusSeeOther)
}

// ideaFailKey is the message of a refusal on an idea (the fail= value of a redirect
// or the rule that refused the form).
func ideaFailKey(code string) string {
	switch code {
	case "reason_required":
		return "ideas.result.reason_required"
	case "pain_link":
		return "ideas.result.pain_link"
	case "img_none":
		return "ideas.result.img_none"
	case "img_type":
		return "ideas.result.img_type"
	case "img_size":
		return "ideas.result.img_size"
	case "img_bad":
		return "ideas.result.img_bad"
	case "img_other":
		return "ideas.result.img_other"
	}
	return "ideas.result.failed"
}
