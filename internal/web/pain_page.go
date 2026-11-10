package web

import (
	"context"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The page of one pain in the repository (REQ-599): title, state, the box "what
// stays open", the pain as the first entry, its history as entries (oldest
// first), a column of facts and a field at the foot that changes the state. The
// state rules are the store's (pain.ValidateTransition); this file only shows
// them: the field offers the states the pain can go to and the parts each one
// needs. There are no comments on a pain, so the field is the status change.

// painStateTone is the colour class of a state word (the pills of the lists).
var painStateTone = map[string]string{pain.Active: "p-bad", pain.Mitigated: "p-warn", pain.Accepted: "p-mute", pain.Resolved: "p-done", pain.Obsolete: "p-mute"}

// painTargets are the states a pain can be moved to from its own.
func painTargets(from string) []string {
	switch from {
	case pain.Active:
		return []string{pain.Mitigated, pain.Accepted, pain.Resolved, pain.Obsolete}
	case pain.Mitigated:
		return []string{pain.Mitigated, pain.Accepted, pain.Resolved, pain.Obsolete, pain.Active}
	case pain.Accepted:
		return []string{pain.Accepted, pain.Mitigated, pain.Resolved, pain.Obsolete, pain.Active}
	}
	return []string{pain.Active}
}

// painComposer is the field under the history: text alone is a comment, the
// status select adds a change of state to it (with the parts that state needs).
// Nil when the viewer may neither work on the pain nor comment.
func (a *app) painComposer(l lang, v *painView, from string, canComment bool) *composerView {
	if !v.CanWork && !canComment {
		return nil
	}
	cv := &composerView{Action: "/ui/pains/" + strconv.FormatInt(v.ID, 10) + "/state", Token: v.Token, Name: "note", Avatar: v.Viewer,
		ModeLabel: l.msg("issue.cx.status"), Placeholder: l.msg("pains.cx.comment_ph")}
	if canComment {
		cv.Modes = append(cv.Modes, composerMode{Value: "comment", Label: l.msg("comments.cx.comment"), Submit: l.msg("comments.cx.submit"), Class: "pri",
			Placeholder: l.msg("pains.cx.comment_ph"), Selected: true})
	}
	if !v.CanWork {
		return cv
	}
	for _, st := range painTargets(from) {
		m := composerMode{Value: st, Label: label(l, painStateKeys, st), Hint: "", Submit: l.msg("pains.cx.set", label(l, painStateKeys, st)), SubmitText: l.msg("pains.cx.set_text", label(l, painStateKeys, st)), Class: "pri",
			Placeholder: l.msg("pains.cx.note"), Selected: !canComment && st == from}
		switch st {
		case pain.Obsolete:
			m.Placeholder = l.msg("pains.cx.why_obsolete")
		case pain.Accepted:
			m.Placeholder = l.msg("pains.cx.why_accepted")
		case pain.Active:
			m.Placeholder = l.msg("pains.cx.why_active")
		}
		cv.Modes = append(cv.Modes, m)
	}
	if !slices.ContainsFunc(cv.Modes, func(m composerMode) bool { return m.Selected }) {
		cv.Modes[0].Selected = true
	}
	var kinds []composerOption
	for _, k := range v.EvidenceKinds {
		kinds = append(kinds, composerOption{Value: k.Value, Label: k.Label, Selected: k.Selected})
	}
	cv.Fields = []composerField{
		{Name: "remaining", Label: l.msg("pains.remaining_field"), Kind: "text", Show: pain.Mitigated, Wide: true},
		{Name: "why", Label: l.msg("pains.why"), Kind: "text", Show: pain.Accepted + " " + pain.Obsolete, Wide: true},
		{Name: "evidence_kind", Label: l.msg("requests.field_evidence_kind"), Kind: "select", Show: pain.Mitigated + " " + pain.Resolved, Options: kinds},
		{Name: "evidence_ref", Label: l.msg("requests.field_evidence"), Kind: "text", Show: pain.Mitigated + " " + pain.Resolved, Wide: true},
	}
	return cv
}

// painItems turns the events of a pain into entries of the timeline component,
// oldest first.
func (a *app) painItems(l lang, pa *store.ProjectAccess, p pain.Pain, events []pain.Event, now func(string) string) []tlItem {
	var items []tlItem
	sentence := func(key string, args ...any) template.HTML {
		return template.HTML(fmt.Sprintf(html.EscapeString(l.msg(key)), args...))
	}
	for _, e := range events {
		it := tlItem{ID: "event-" + strconv.FormatInt(e.ID, 10), Act: "pain." + e.Kind, Who: e.Person, Icon: "pain", Age: now(e.CreatedAt), Git: anchorViewOf(pa, p.Project, e.GitAnchor, e.Via), at: parseTime(e.CreatedAt)}
		switch e.Kind {
		case "created":
			it.Icon, it.Head = "pain", sentence("pains.ev.created")
		case "state":
			it.Icon = "pain"
			pill := `<span class="lb ` + painStateTone[e.ToState] + `">` + html.EscapeString(label(l, painStateKeys, e.ToState)) + `</span>`
			it.Head = sentence("pains.ev.state", pill)
			var body strings.Builder
			if e.Remaining != "" {
				body.WriteString(`<b class="xtl">` + html.EscapeString(l.msg("pains.stays_open")) + `</b>` + string(mdText(e.Remaining)))
			} else if e.Note != "" {
				body.WriteString(string(mdText(e.Note)))
			}
			if e.EvidenceKind != "" {
				ev := a.evidenceLineFor(l, pa, p.Project, requestdomain.Evidence{Kind: e.EvidenceKind, Ref: e.EvidenceRef})
				ev.Label = label(l, painEvidenceKeys, e.EvidenceKind)
				if body.Len() > 0 {
					body.WriteString(" ")
				}
				body.WriteString(html.EscapeString(ev.Label) + " " + string(proofChips(l, []evidenceLine{ev})))
			}
			it.Body = template.HTML(body.String())
		case "corrected":
			it.Icon, it.Head, it.Body = "pencil", sentence("pains.ev.corrected"), mdText(e.Note)
		case "link_added":
			it.Icon, it.Head = "link", sentence("pains.ev.link_added", painLinkTarget(l, e.Data))
		case "link_removed":
			it.Icon, it.Head = "x", sentence("pains.ev.link_removed", painLinkTarget(l, e.Data))
		default:
			it.Head = template.HTML(html.EscapeString(label(l, painEventKeys, e.Kind)))
		}
		items = append(items, it)
	}
	return items
}

// painInRepo is /ui/{org}/{repo}/pains/{id}. A pain of another project is as
// unknown here as one that does not exist.
func (a *app) painInRepo(w http.ResponseWriter, r *http.Request, remote, id string) {
	r.SetPathValue("id", id)
	pa := a.access(r)
	q := r.URL.Query()
	q.Set("project", remote)
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = q.Encode()
	view := a.repoViewFor(r2, pa, a.shellBaseFor(r2).who.kind, remote, "issues")
	r2 = r2.WithContext(context.WithValue(r2.Context(), repoCtxKey{}, view))
	a.painPageIn(w, r2, remote)
}
