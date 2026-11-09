package web

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Open decisions in the inbox (REQ-599). A decision that waits for a person (DEC-n,
// kind human) is a row of "Waiting for you": its question, a short text, and its
// variants as buttons. A click decides through POST /ui/inbox/answer
// (inbox_answer.go, ProjectAccess.QuickAnswer), "With a reason" opens a composer
// with the choice and an optional reason. Nothing is decided here that a person did
// not click: the rows only offer the way.
//
// Who sees the row is who may close the decision (edit right on its idea or
// issue); somebody who may only read or vote finds it on the page of the idea.

func init() {
	// decide: the signpost, the sign of a choice (decision of Robin, variant B).
	// The same path stands in ui-pill.js.
	mockIcons["decide"] = `<path d="M8 1.8v12.4M5.8 14.2h4.4"/><path d="M8 3.2h4.4L14 4.9l-1.6 1.7H8z"/><path d="M8 8.2H3.6L2 9.9l1.6 1.7H8z"/>`
	refKinds["decision"] = refKind{"DEC", "k-dec", "ref.kind.decision", mockIcons["decide"]}
}

// decisionHref is the page a decision lives on: the idea or issue it is asked
// of, anchored at the decision on an idea (the issue page has no anchor for it).
func decisionHref(d idea.Decision) string {
	if d.Project == "" {
		return ""
	}
	id := strconv.FormatInt(d.SubjectID, 10)
	if d.Subject == idea.SubjectRequest {
		return repoHref(d.Project) + "/issues/" + id
	}
	return repoHref(d.Project) + "/ideas/" + id + "#decision-" + strconv.FormatInt(d.ID, 10)
}

// inboxDecisions turns the decisions that wait for a person, and that the viewer
// may close, into rows. SearchDecisions already left out every decision of a
// subject the viewer may not read.
func (a *app) inboxDecisions(l lang, pa *store.ProjectAccess, project string, now time.Time) []forYouRow {
	var rows []forYouRow
	for _, p := range visibleProjects(pa, project) {
		list, err := pa.SearchDecisions(store.DecisionFilter{Project: p, NeedsHuman: true, Limit: tasksShown})
		if err != nil {
			continue
		}
		for _, d := range list {
			if !pa.CanCloseDecision(d) {
				continue
			}
			created := parseTime(d.CreatedAt)
			row := forYouRow{Mark: "decision", Tone: "ask", Title: d.Question, Brief: links.Clean(d.Detail, 220), Href: decisionHref(d),
				Rest: l.msg("inbox.dec.of", lastSegment(d.Project)), Action: l.msg("inbox.act.open"), rank: rankBlocks, at: created, decisionID: d.ID}
			if !created.IsZero() {
				row.Age = shortAgo(l, now, created)
				row.AgeOld = olderThan(d.CreatedAt, now, taskOldAfter)
			}
			rows = append(rows, row)
		}
	}
	return rows
}

// inboxDecisionComposer is the composer behind "With a reason": the choice among
// the variants, which is never preset, and one optional line for the reason.
func (a *app) inboxDecisionComposer(l lang, token string, id int64, options []inboxOption) *composerView {
	choice := composerField{Name: "option", Label: l.msg("inbox.dec.choice"), Kind: "select", Wide: true,
		Options: []composerOption{{Value: "", Label: l.msg("inbox.dec.pick"), Selected: true}}}
	for _, o := range options {
		choice.Options = append(choice.Options, composerOption{Value: o.Key, Label: o.Key + " · " + o.Text})
	}
	return &composerView{Action: "/ui/inbox/answer", Token: token, Name: "reason", Placeholder: l.msg("inbox.dec.why"), Submit: l.msg("inbox.dec.decide"),
		Cancel: l.msg("inbox.reply_cancel"), NoHint: true, NoTools: true, Fields: []composerField{choice},
		Hidden: []hiddenField{{"kind", store.AnswerOnDecision}, {"id", strconv.FormatInt(id, 10)}, {"next", "1"}}}
}

// inboxDecisionRows adds the composer to the decision rows that offer buttons.
// A decision with fewer than two variants has no buttons and no composer: it is
// decided on its page.
func (a *app) inboxDecisionRows(l lang, token string, rows []inboxRow) {
	for i := range rows {
		if rows[i].DecisionN != 0 && len(rows[i].Options) > 0 {
			rows[i].Reply = a.inboxDecisionComposer(l, token, rows[i].DecisionN, rows[i].Options)
			rows[i].OwnKey = "inbox.dec.with_reason"
		}
	}
}

// inboxFailKey is the message of the fail= value of the inbox: the reasons a
// decision was not taken, otherwise the one of a task.
func inboxFailKey(code string) string {
	switch code {
	case "dec-choice":
		return "inbox.dec.fail.choice"
	case "dec-reason":
		return "inbox.dec.fail.reason"
	case "dec-closed":
		return "inbox.dec.fail.closed"
	case "dec-failed":
		return "inbox.dec.fail.failed"
	}
	return taskFailKey(code)
}

// decisionFailCode maps the rule that refused an answer to the fail= value.
func decisionFailCode(rule string) string {
	switch rule {
	case "option_not_found":
		return "dec-choice"
	case "reason_too_long":
		return "dec-reason"
	case "decision_not_open", "decision_closed":
		return "dec-closed"
	}
	return "dec-failed"
}

// inboxFailRedirect leads a form without script back to the inbox with the reason.
func inboxFailRedirect(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/ui/inbox?"+url.Values{"fail": {code}}.Encode(), http.StatusSeeOther)
}
