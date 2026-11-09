package web

import (
	"database/sql"
	"errors"
	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var (
	painStateKeys = map[string]string{"active": "pains.state.active", "mitigated": "pains.state.mitigated", "accepted": "pains.state.accepted", "resolved": "pains.state.resolved", "obsolete": "pains.state.obsolete"}
	painEventKeys = map[string]string{"created": "pains.event.created", "state": "pains.event.state", "corrected": "pains.event.corrected",
		"link_added": "pains.event.link_added", "link_removed": "pains.event.link_removed"}
	painEvidenceKeys = map[string]string{"commit": "requests.evidence.commit", "test": "requests.evidence.test", "file": "requests.evidence.file",
		"decision": "requests.evidence.decision", "url": "requests.evidence.url", "session": "requests.evidence.session", "request": "requests.evidence.request"}
)

type painLinkLine struct {
	Kind, Text, Href string
	// Pill is the identifier pill of a request: the sidebar says REQ-2, the title is in the hover card.
	Pill template.HTML
}

type painView struct {
	ID                                int64
	Human, Title, Project, Person     string
	Actor                             actorView
	State, StateLabel, Remaining, Age string
	// Accepted is why one lives with the pain (state accepted).
	Accepted  string
	Blocks    []bodyBlock
	Motivates []painLinkLine
	// Items is the history as entries of the timeline (oldest first), Composer
	// the field that changes the state, Labels the pills of the column of facts,
	// People who took part, Viewer the signed-in person.
	Items         []tlItem
	Composer      *composerView
	Labels        []rowLabel
	LabelBox      *labelBoxView
	People        []string
	Viewer, Base  string
	StateTone     string
	CanWork       bool
	States        []filterOption
	EvidenceKinds []filterOption
	Token, Back   string
	Result        *requestResult
	Explain       *explainView
	// First is the head of the bubble that opens the timeline.
	First firstHead
	// CanIdea shows the form "Create idea" (a solution idea this pain motivates).
	CanIdea bool
}

func painURL(id int64) string { return "/ui/pains/" + strconv.FormatInt(id, 10) }

func lastSegment(project string) string {
	if i := strings.LastIndex(project, "/"); i >= 0 {
		return project[i+1:]
	}
	return project
}

// painPageIn shows the pain; with a remote it must belong to that project.
func (a *app) painPageIn(w http.ResponseWriter, r *http.Request, remote string) {
	l := a.langFor(r)
	w.Header().Set("Cache-Control", "no-store")
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	pa := a.access(r)
	detail, err := pa.PainDetailView(id)
	if err != nil {
		a.painFail(w, r, err)
		return
	}
	p := detail.Pain
	if remote != "" && p.Project != remote {
		http.NotFound(w, r)
		return
	}
	v := &painView{ID: p.ID, Human: p.HumanID(), Title: p.Title, Project: lastSegment(p.Project), Person: p.Person,
		State: p.State, StateLabel: label(l, painStateKeys, p.State), Remaining: p.Remaining, Accepted: p.AcceptedReason, Age: shortAgo(l, overviewNow().UTC(), parseTime(p.CreatedAt)),
		Blocks: bodyBlocks(p.Body, 0), Token: csrfOf(r), Back: "/ui/pains?project=" + url.QueryEscape(p.Project)}
	v.Actor = a.actorViewOf(l, pa, actor.Pain, p.ID, p.Person, true)
	v.CanWork = pa.CheckPain(p, store.ActWork) == nil
	v.CanIdea = v.CanWork && interactive(r) && pa.Allow(p.Project, store.ResIdea, store.ActCreate, store.Object{Own: true})
	v.Explain = a.explainFor(l, explain.KindPain, p.ID)
	for _, s := range pain.States {
		v.States = append(v.States, filterOption{Value: s, Label: label(l, painStateKeys, s), Selected: s == p.State})
	}
	for _, k := range pain.EvidenceKinds {
		v.EvidenceKinds = append(v.EvidenceKinds, filterOption{Value: k, Label: label(l, painEvidenceKeys, k), Selected: k == "commit"})
	}
	lg := l
	for _, l := range detail.Motivates {
		line := painLinkLine{Kind: l.Kind}
		switch l.Kind {
		case "request":
			line.Text, line.Href = "REQ-"+strconv.FormatInt(l.RequestID, 10)+" "+ellipsize(l.Title, 80), requestURL(l.RequestID)
			line.Pill = refPillHTML(lg, "request", l.RequestID, line.Href)
		case "knowledge":
			line.Text, line.Href = ellipsize(l.Title, 80), "/ui/knowledge/"+strconv.FormatInt(l.KnowledgeID, 10)
			line.Pill = numberedPill(lg, "knowledge", l.KnowledgeID, line.Href, "")
		case "idea":
			line.Text, line.Href = ellipsize(l.Title, 80), repoHref(p.Project)+"/ideas/"+strconv.FormatInt(l.IdeaID, 10)
			line.Pill = refPillHTML(lg, "idea", l.IdeaID, line.Href)
		default:
			line.Text = ellipsize(l.ExternalRef, 120)
			if strings.HasPrefix(l.ExternalRef, "https://") || strings.HasPrefix(l.ExternalRef, "http://") {
				line.Href = l.ExternalRef
				line.Pill = urlPill(lg, l.ExternalRef)
			}
		}
		v.Motivates = append(v.Motivates, line)
	}
	now := overviewNow().UTC()
	v.Viewer, v.StateTone = browserPrincipal(r).Label, painStateTone[p.State]
	v.Base = repoHref(p.Project)
	ago := func(at string) string { return shortAgo(l, now, parseTime(at)) }
	comments := a.commentTimeline(r, l, pa, store.CommentOnPain, p.ID, p.Person, v.Base+"/pains/"+strconv.FormatInt(p.ID, 10), editingComment(r), ago)
	events := withoutRepeatedPainNotes(detail.Events, comments)
	var createdGit *anchorView
	if v.hasFirst() {
		// The head of the first object says who recorded the pain, and when.
		for _, e := range events {
			if e.Kind == "created" {
				createdGit = anchorViewOf(pa, p.Project, e.GitAnchor, e.Via)
			}
		}
		events = slices.DeleteFunc(events, func(e pain.Event) bool { return e.Kind == "created" })
	}
	v.First = firstOf(l, v.Actor, firstNonEmpty(v.Actor.Label, v.Person), l.msg("pains.recorded_ch"), v.Age, l.msg("issue.author"), "")
	v.First.Git = createdGit
	v.Items = mergeTimeline(a.painItems(l, pa, p, events, ago), comments)
	seen := map[string]bool{}
	for _, e := range detail.Events {
		if e.Person != "" && !seen[e.Person] {
			seen[e.Person] = true
			v.People = append(v.People, e.Person)
		}
	}
	if labels, err := a.store.LabelsFor("pain", []int64{p.ID}); err == nil {
		v.Labels = rowLabels(labels[p.ID], v.Base+"/issues")
	}
	v.LabelBox = a.labelBox(r, pa, p.Project, store.CommentOnPain, p.ID, v.Labels)
	v.Composer = a.painComposer(l, v, p.State, interactive(r) && pa.Allow(p.Project, store.ResComment, store.ActCreate, store.Object{Own: true}))
	switch {
	case commentResult(r, l) != nil:
		v.Result = commentResult(r, l)
	case r.URL.Query().Get("fail") != "":
		v.Result = &requestResult{Text: l.msg(painFailKey(r.URL.Query().Get("fail"))), Kind: "failed", Fail: true}
	case r.URL.Query().Get("done") == "state":
		v.Result = &requestResult{Text: l.msg("pains.result.state"), Kind: "done"}
	case r.URL.Query().Get("done") == "idea":
		v.Result = &requestResult{Text: l.msg("pains.result.idea_made"), Kind: "done"}
	}
	a.renderBrowser(w, r, "pain", pageData{Title: p.HumanID(), PainV: v})
}

func (a *app) painFail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrAccessForbidden):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, store.ErrAccessNotFound), errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	case requestdomain.Code(err) != "":
		http.Error(w, "invalid input: "+requestdomain.Code(err), http.StatusBadRequest)
	default:
		http.Error(w, "could not save", http.StatusInternalServerError)
	}
}

// painState moves a pain along the transition table. The rules (evidence,
// remaining, note) are the store's; this only passes the form through.
func (a *app) painState(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// The composer sends the chosen state as "mode"; "state" is the older form field.
	to := r.FormValue("state")
	if to == "" {
		to = r.FormValue("mode")
	}
	if to == "comment" {
		pa := a.access(r)
		c, err := pa.AddComment(store.CommentOnPain, id, r.FormValue("note"), a.commentActor(r))
		if err != nil {
			a.commentFail(w, r, a.objectPage(pa, store.CommentOnPain, id), err)
			return
		}
		a.stampActor(r, actor.Comment, c.ID)
		http.Redirect(w, r, commentPage(c)+"?comment=added#comment-"+strconv.FormatInt(c.ID, 10), http.StatusSeeOther)
		return
	}
	if !slices.Contains(pain.States, to) {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	for _, f := range []string{"remaining", "evidence_ref", "note", "why"} {
		if len(r.FormValue(f)) > maxFieldBytes {
			http.Error(w, "invalid input", http.StatusBadRequest)
			return
		}
	}
	accountID, _ := store.AccountNumericID(browserPrincipal(r).ID)
	by := pain.Actor{AccountID: accountID, Person: personOf(r), GitAnchor: gitanchor.Web()}
	ev := pain.Evidence{Kind: r.FormValue("evidence_kind"), Ref: r.FormValue("evidence_ref")}
	if strings.TrimSpace(ev.Ref) == "" {
		ev = pain.Evidence{}
	}
	if _, err := a.access(r).SetPainState(id, to, r.FormValue("remaining"), ev, painStateNote(to, r.FormValue("why"), r.FormValue("note")), by); err != nil {
		if code := requestdomain.Code(err); code != "" {
			http.Redirect(w, r, a.painHref(r, id)+"?fail="+url.QueryEscape(code), http.StatusSeeOther)
			return
		}
		a.painFail(w, r, err)
		return
	}
	q := "?done=state"
	if text := strings.TrimSpace(r.FormValue("note")); text != "" {
		if c, err := a.access(r).AddComment(store.CommentOnPain, id, text, a.commentActor(r)); err != nil {
			q = "?comment=failed"
		} else {
			a.stampActor(r, actor.Comment, c.ID)
		}
	}
	http.Redirect(w, r, a.painHref(r, id)+q, http.StatusSeeOther)
}

// painStateNote is the note the store gets: for an accepted or obsolete pain
// the field "Warum" is the reason, and a comment beside it follows after a
// blank line so that nothing the person wrote is lost. Without "Warum" the note
// is the reason (older forms, the other states).
func painStateNote(to, why, note string) string {
	why, note = strings.TrimSpace(why), strings.TrimSpace(note)
	if (to != pain.Accepted && to != pain.Obsolete) || why == "" {
		return note
	}
	if note == "" {
		return why
	}
	return why + "\n\n" + note
}

// linkDataText writes the target of a link event ("request:1", "knowledge:3",
// "external:text") the way the rest of the page names it.
// painLinkTarget writes what a link event of a pain points at ("request:3",
// "idea:5", "knowledge:7", "external:https://…") as the long pill it is; what
// it does not know stays text.
func painLinkTarget(l lang, data string) string {
	kind, rest, _ := strings.Cut(data, ":")
	n, err := strconv.ParseInt(rest, 10, 64)
	switch {
	case kind == "request" && err == nil:
		return string(longPill(l, "request", n, requestURL(n)))
	case kind == "idea" && err == nil:
		return string(longPill(l, "idea", n, ""))
	case kind == "knowledge" && err == nil:
		return string(longPill(l, "knowledge", n, "/ui/knowledge/"+rest))
	case kind == "external" && (strings.HasPrefix(rest, "https://") || strings.HasPrefix(rest, "http://")):
		return string(urlPill(l, rest))
	}
	return html.EscapeString(ellipsize(linkDataText(data), 120))
}

func linkDataText(data string) string {
	kind, rest, _ := strings.Cut(data, ":")
	switch kind {
	case "request":
		return "REQ-" + rest
	case "knowledge":
		return "#" + rest
	case "external":
		return rest
	}
	return data
}

// painFailKey maps the fail= value of a redirect to its message; unknown values
// read as a plain failure.
func painFailKey(code string) string {
	switch code {
	case "remaining_required":
		return "pains.result.remaining_required"
	case "evidence_required":
		return "pains.result.evidence_required"
	case "evidence_invalid":
		return "pains.result.evidence_invalid"
	case "note_required":
		return "pains.result.note_required"
	case "accepted_reason_required":
		return "pains.result.accepted_reason_required"
	case "invalid_transition":
		return "pains.result.invalid_transition"
	case "idea_invalid":
		return "pains.result.idea_invalid"
	}
	return "pains.result.failed"
}

// hasFirst says whether the page opens with the bubble of the pain's text.
func (v *painView) hasFirst() bool { return len(v.Blocks) > 0 || v.Explain != nil }

// withoutRepeatedPainNotes leaves the note out of a change of state when a
// comment of the same person with the same text stands beside it: the status
// select writes the words as the note and as a comment, and the timeline shows
// them once, in the comment (the same rule as for an issue). A "Why" written
// before the comment stays as the note.
func withoutRepeatedPainNotes(events []pain.Event, comments []tlItem) []pain.Event {
	out := slices.Clone(events)
	for i, ev := range out {
		note := strings.TrimSpace(ev.Note)
		if note == "" {
			continue
		}
		at := parseTime(ev.CreatedAt)
		for _, c := range comments {
			v := c.Comment
			text := ""
			if v != nil {
				text = strings.TrimSpace(v.Raw)
			}
			if v == nil || v.Deleted || v.Who != ev.Person || text == "" || at.IsZero() || c.at.IsZero() {
				continue
			}
			rest, same := trimmedPainNote(note, text)
			if !same {
				continue
			}
			if d := c.at.Sub(at); d < sameMomentWithin && d > -sameMomentWithin {
				out[i].Note = rest
				break
			}
		}
	}
	return out
}

// trimmedPainNote takes the comment out of a note: the note is the comment
// alone, or "Why" with the comment after it (painStateNote). same says whether
// the comment was in it; rest is what stays.
func trimmedPainNote(note, comment string) (rest string, same bool) {
	note, comment = strings.TrimSpace(note), strings.TrimSpace(comment)
	if comment == "" || (note != comment && !strings.HasSuffix(note, "\n\n"+comment)) {
		return note, false
	}
	return strings.TrimSpace(strings.TrimSuffix(note, comment)), true
}
